package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
)

const (
	terraModelID    = "gpt-6.1-terra"
	terraToolCallID = "call_terra_1"
	terraToolOutput = "terra-tool-ok"
)

// terraResponsesUpstream is a mock OpenAI Responses endpoint. It answers the
// first streamed tool-enabled turn with a bash function_call and, once the
// input carries that call's function_call_output, with a final text message.
// Tool-less requests (slug generation on the workhorse model) get plain text.
type terraResponsesUpstream struct {
	mu       sync.Mutex
	requests []terraUpstreamRequest
}

type terraUpstreamRequest struct {
	path, auth string
	body       map[string]any
}

func (u *terraResponsesUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u.mu.Lock()
	u.requests = append(u.requests, terraUpstreamRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
	seq := len(u.requests)
	u.mu.Unlock()
	if stream, _ := body["stream"].(bool); !stream {
		// Prompt-cache prewarms are the only non-streamed Responses calls.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"warm","status":"completed","model":%q,"output":[],"usage":{"input_tokens":1,"output_tokens":0}}`, terraModelID)
		return
	}
	item := map[string]any{
		"type": "function_call", "call_id": terraToolCallID, "name": "bash",
		"arguments": `{"command":"echo ` + terraToolOutput + `"}`,
	}
	if _, hasTools := body["tools"]; !hasTools || terraToolOutputReplayed(body) {
		item = map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": "terra finished"}},
		}
	}
	itemDone, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
		"id": fmt.Sprintf("resp_%d", seq), "status": "completed", "model": terraModelID,
		"output": []any{item}, "usage": map[string]any{"input_tokens": 12, "output_tokens": 3},
	}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "event: response.output_item.done\ndata: %s\n\n", itemDone)
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completed)
}

func terraToolOutputReplayed(body map[string]any) bool {
	input, _ := body["input"].([]any)
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call_output" && item["call_id"] == terraToolCallID {
			output, _ := item["output"].(string)
			return strings.Contains(output, terraToolOutput)
		}
	}
	return false
}

// terraTurns returns the streamed, tool-enabled Terra requests, skipping
// prompt-cache prewarms and the tool-less slug-generation call.
func (u *terraResponsesUpstream) terraTurns() []terraUpstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []terraUpstreamRequest
	for _, req := range u.requests {
		_, hasTools := req.body["tools"]
		if stream, _ := req.body["stream"].(bool); stream && hasTools && req.body["model"] == terraModelID {
			out = append(out, req)
		}
	}
	return out
}

// openAIRewriteClient sends requests addressed to api.openai.com to upstream,
// so a direct-key service built with the catalog's default URL hits the mock.
func openAIRewriteClient(t *testing.T, upstream *httptest.Server) *http.Client {
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.openai.com" {
			return nil, fmt.Errorf("unexpected upstream host %s", req.URL.Host)
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

func TestGPT61TerraStreamsToolCallThroughResponses(t *testing.T) {
	wantLevels := []string{"off", "low", "medium", "high", "xhigh", "max"}
	tests := []struct {
		name       string
		manager    func(t *testing.T, database *db.DB, upstream *httptest.Server) (*models.Manager, string)
		wantSource string
		wantAuth   string
	}{
		{
			name: "direct OPENAI_API_KEY",
			manager: func(t *testing.T, database *db.DB, upstream *httptest.Server) (*models.Manager, string) {
				built := modelsources.Build(models.All(), []modelsources.Source{modelsources.Env("", "sk-terra-env", "", "")}, openAIRewriteClient(t, upstream), nil)
				mgr, err := models.NewManager(&models.Config{Models: built})
				if err != nil {
					t.Fatal(err)
				}
				return mgr, terraModelID
			},
			wantSource: "$OPENAI_API_KEY",
			wantAuth:   "Bearer sk-terra-env",
		},
		{
			name: "saved openai-responses provider",
			manager: func(t *testing.T, database *db.DB, upstream *httptest.Server) (*models.Manager, string) {
				row := db.ModelProvider{ProviderID: "own", ProviderType: "openai-responses", Endpoint: "https://api.openai.com/v1", APIKey: "sk-terra-saved", DisplayName: "OpenAI", Models: []db.ProviderModel{{ID: terraModelID}}}
				if err := database.CreateModelProvider(t.Context(), row); err != nil {
					t.Fatal(err)
				}
				mgr, err := models.NewManager(&models.Config{DB: database, HTTPC: openAIRewriteClient(t, upstream)})
				if err != nil {
					t.Fatal(err)
				}
				if err := mgr.RefreshProviders(); err != nil {
					t.Fatal(err)
				}
				return mgr, models.ProviderRouteID(row.ProviderID, terraModelID)
			},
			wantSource: "OpenAI",
			wantAuth:   "Bearer sk-terra-saved",
		},
		{
			name: "exe.dev llm integration",
			manager: func(t *testing.T, database *db.DB, upstream *httptest.Server) (*models.Manager, string) {
				integration := &modelsources.LLMIntegrationConfig{
					Name: "llm", Host: "llm.int.exe.xyz", URL: upstream.URL,
					Models: []modelsources.IntegrationModel{{
						ID: "openai/" + terraModelID, Provider: "openai", NativeID: terraModelID,
						APIs: []string{"openai_chat", "openai_responses"},
					}},
				}
				built := modelsources.Build(models.All(), []modelsources.Source{modelsources.LLMIntegration(integration, "")}, upstream.Client(), nil)
				mgr, err := models.NewManager(&models.Config{Models: built})
				if err != nil {
					t.Fatal(err)
				}
				return mgr, terraModelID
			},
			wantSource: "llm.int.exe.xyz",
			wantAuth:   "Bearer implicit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &terraResponsesUpstream{}
			upstream := httptest.NewServer(mock)
			defer upstream.Close()
			database, cleanup := setupTestDB(t)
			t.Cleanup(cleanup)
			mgr, modelID := tt.manager(t, database, upstream)
			s := NewServer(database, mgr, claudetool.ToolSetConfig{EnableBrowser: false}, slog.New(slog.NewTextHandler(io.Discard, nil)), false, modelID, "")
			s.hooksDir = t.TempDir()
			if s.terminals != nil {
				s.terminals.SetSpawner(InProcessSpawner)
				t.Cleanup(func() { os.RemoveAll(s.terminals.dir) })
			}
			t.Cleanup(func() { stopActiveConversationLoops(s) })

			// The model list the picker reads advertises Terra with the
			// capabilities from the models.dev snapshot.
			rec := httptest.NewRecorder()
			s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("models status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var listed []ModelInfo
			if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
				t.Fatal(err)
			}
			idx := slices.IndexFunc(listed, func(m ModelInfo) bool { return m.ID == modelID })
			if idx < 0 {
				t.Fatalf("models = %+v, want %s", listed, modelID)
			}
			info := listed[idx]
			if info.APIModelName != terraModelID || info.APIType != string(models.APITypeOpenAIResponses) || info.Source != tt.wantSource ||
				!info.SupportsImages || !info.SupportsReasoning || !slices.Equal(info.ReasoningLevels, wantLevels) || info.MaxContextTokens != 272_000 {
				t.Fatalf("model info = %+v", info)
			}

			h := &TestHarness{t: t, db: database, server: s, timeout: 10 * time.Second}
			body, _ := json.Marshal(ChatRequest{Message: "run the terra tool", Model: modelID})
			req := httptest.NewRequest(http.MethodPost, "/api/conversations/new", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			rec = httptest.NewRecorder()
			s.handleNewConversation(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("new conversation status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var created struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			h.convID = created.ConversationID
			conversation, err := database.GetConversationByID(t.Context(), h.convID)
			if err != nil {
				t.Fatal(err)
			}
			if conversation.Model == nil || *conversation.Model != modelID {
				t.Fatalf("persisted model = %v, want %q", conversation.Model, modelID)
			}

			if got := h.WaitToolResult(); !strings.Contains(got, terraToolOutput) {
				t.Fatalf("tool result = %q, want %q", got, terraToolOutput)
			}
			if got := h.WaitResponse(); got != "terra finished" {
				t.Fatalf("final response = %q", got)
			}

			turns := mock.terraTurns()
			if len(turns) != 2 {
				t.Fatalf("streamed Terra turns = %d, want 2", len(turns))
			}
			for i, turn := range turns {
				if turn.path != "/v1/responses" || turn.auth != tt.wantAuth {
					t.Fatalf("turn %d: path %q auth %q", i, turn.path, turn.auth)
				}
				reasoning, _ := turn.body["reasoning"].(map[string]any)
				text, _ := turn.body["text"].(map[string]any)
				if reasoning["effort"] != "medium" || text["verbosity"] != "low" {
					t.Fatalf("turn %d: reasoning %v text %v, want catalog defaults", i, reasoning, text)
				}
				tools, _ := turn.body["tools"].([]any)
				if !slices.ContainsFunc(tools, func(raw any) bool {
					tool, _ := raw.(map[string]any)
					return tool["type"] == "function" && tool["name"] == "bash"
				}) {
					t.Fatalf("turn %d: bash function tool missing from %v", i, tools)
				}
			}
			if terraToolOutputReplayed(turns[0].body) || !terraToolOutputReplayed(turns[1].body) {
				t.Fatal("function_call_output was not replayed on the second turn only")
			}
		})
	}
}
