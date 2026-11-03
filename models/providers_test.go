package models

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
)

func TestProviderModelAdmission(t *testing.T) {
	for _, tc := range []struct {
		endpoint, model string
		want            bool
	}{
		{"https://api.groq.com/openai/v1", "llama-3.1-8b-instant", true},
		{"https://api.mistral.ai/v1", "pixtral-12b", true},
		{"https://api.together.ai/v1", "essentialai/Rnj-1-Instruct", true},
		{"https://gateway.example/v1", "claude-sonnet-5-5", true},
		{"https://gateway.example/v1", "unrecognized-embedding", false},
	} {
		if got := ProviderModelEligible(tc.endpoint, tc.model); got != tc.want {
			t.Errorf("%s %s: got %t", tc.endpoint, tc.model, got)
		}
	}
}

func TestProviderSnapshotsStayOfflineAndAccountBound(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer upstream.Close()
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	const wire = "gpt-6.1-sol"
	row := db.ModelProvider{ProviderID: "a", DisplayName: SourceCustomLabel, ProviderType: "openai-responses", Endpoint: upstream.URL, APIKey: "alpha", Models: []db.ProviderModel{{ID: wire}}}
	if err := database.CreateModelProvider(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	other := row
	other.ProviderID, other.APIKey = "b", "beta"
	if err := database.CreateModelProvider(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(&Config{DB: database, Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshProviders(); err != nil {
		t.Fatal(err)
	}
	id := ProviderRouteID("a", wire)
	service, source, initial, err := manager.GetServiceSnapshot(id)
	if err != nil || source != "a" || service.(*loggingService).service.(*reasoningService).Service.(*oai.ResponsesService).APIKey != "alpha" {
		t.Fatalf("wrong account: %s, %v", source, err)
	}
	update := func() {
		t.Helper()
		old, err := database.GetModelProvider(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		if err := database.UpdateModelProvider(t.Context(), row, old.Version); err != nil {
			t.Fatal(err)
		}
		if err := manager.RefreshProviders(); err != nil {
			t.Fatal(err)
		}
	}
	row.DisplayName = "Renamed"
	row.Models = append(row.Models, db.ProviderModel{ID: "gpt-5.6-sol"})
	update()
	_, _, revision, err := manager.GetServiceSnapshot(id)
	if err != nil || revision != initial {
		t.Fatalf("catalog/label edit invalidated service: %v", err)
	}
	row.Models = nil
	update()
	if slices.Contains(manager.GetAvailableModels(), id) {
		t.Fatal("unlisted route remained in picker")
	}
	if _, _, revision, err := manager.GetServiceSnapshot(id); err != nil || revision != initial {
		t.Fatalf("unlisting changed the connection revision: %v", err)
	}
	row.APIKey = "rotated"
	update()
	service, source, revision, err = manager.GetServiceSnapshot(id)
	if err != nil || source != "a" || revision == initial || service.(*loggingService).service.(*reasoningService).Service.(*oai.ResponsesService).APIKey != "rotated" {
		t.Fatalf("unlisted route did not use current credentials: %v", err)
	}
	before := manager.GetAvailableModels()
	if err := manager.RefreshCustomModels(); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshBuiltModels([]Built{predictableBuilt()}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, manager.GetAvailableModels()) {
		t.Fatal("legacy refresh reordered or removed provider routes")
	}
	otherBuilt := predictableBuilt()
	otherBuilt.ID = "new-first"
	if err := manager.RefreshBuiltModels([]Built{otherBuilt, predictableBuilt()}); err != nil {
		t.Fatal(err)
	}
	if order := manager.GetAvailableModels(); order[0] != "new-first" || order[1] != "predictable" {
		t.Fatalf("legacy catalog ordering was frozen: %v", order)
	}
	restarted, err := NewManager(&Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RefreshProviders(); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetService(ProviderRouteID("a", "private-unlisted-model")); err != nil {
		t.Fatalf("private unlisted route: %v", err)
	}
	if _, err := restarted.GetService(id); err != nil {
		t.Fatalf("unlisted route unavailable after restart: %v", err)
	}
	if err := database.DeleteModelProvider(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetService(id); err == nil {
		t.Fatal("deleted account resolved to a service")
	}
	listed := ProviderRouteID("b", wire)
	if err := database.DeleteModelProvider(t.Context(), "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetService(listed); err != nil || manager.GetModelInfo(listed) == nil || !manager.HasModel(listed) {
		t.Fatal("listed catalog lookups bypassed the registry cache")
	}
	if _, _, _, err := manager.GetServiceSnapshot(listed); err == nil {
		t.Fatal("turn boundary reused a removed account from the cache")
	}
	if err := manager.RefreshProviders(); err != nil {
		t.Fatal(err)
	}
	if manager.HasModel(listed) {
		t.Fatal("publication retained a removed route")
	}
	if calls != 0 {
		t.Fatalf("offline model operations contacted provider %d times", calls)
	}
}

func TestSavedProviderUsesConservativeExistingAdapters(t *testing.T) {
	for _, tc := range []struct{ protocol, wire, path, response, tokenField string }{
		{"anthropic", "claude-sonnet-5-5", "/v1/messages", `{"id":"r","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, "max_tokens"},
		{"openai", "gpt-6.1-sol", "/v1/chat/completions", `{"id":"r","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, "max_completion_tokens"},
		{"openai-responses", "gpt-6.1-sol", "/v1/responses", `{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`, "max_output_tokens"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				for _, key := range []string{"thinking", "output_config", "reasoning_effort", "reasoning"} {
					if _, ok := body[key]; ok {
						t.Errorf("unrecognized endpoint received %s", key)
					}
				}
				if body[tc.tokenField] != float64(8192) {
					t.Errorf("output limit = %v", body[tc.tokenField])
				}
				include, _ := body["include"].([]any)
				if tc.protocol == "openai-responses" && !slices.ContainsFunc(include, func(v any) bool { return v == "reasoning.encrypted_content" }) {
					t.Error("reasoning replay state was not requested")
				}
				if tc.protocol == "anthropic" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":%s}\n\n", tc.response)
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, tc.response)
				}
			}))
			defer upstream.Close()
			manager, err := NewManager(&Config{HTTPC: upstream.Client()})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := manager.providerEntry(db.ModelProvider{ProviderID: "a", ProviderType: tc.protocol, Endpoint: upstream.URL + "/v1", APIKey: "secret"}, tc.wire)
			if err != nil {
				t.Fatal(err)
			}
			if entry.service.SupportsImages() || entry.service.(interface{ SupportsReasoning() bool }).SupportsReasoning() {
				t.Fatal("unrecognized endpoint advertised advanced capabilities")
			}
			if _, err := entry.service.Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hi")}, ThinkingLevel: llm.ThinkingLevelHigh}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSavedProviderReasoningDoesNotOverrideInternalRequests(t *testing.T) {
	manager, err := NewManager(&Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ protocol, endpoint, model string }{
		{"anthropic", "https://api.anthropic.com/v1", "claude-opus-4-8"},
		// Sonnet 5.5 advertises effort levels with no toggle, so "off" is absent
		// from its advertised set -- which is what this test needs to prove an
		// internal off request still reaches the adapter. (Haiku 5.5 used to serve
		// that role; models.dev now lists a toggle for it.)
		{"anthropic", "https://api.anthropic.com/v1", "claude-sonnet-5-5"},
		{"openai", "https://api.openai.com/v1", "gpt-6.1-sol"},
		{"openai-responses", "https://api.openai.com/v1", "gpt-6.1-sol"},
	} {
		t.Run(tc.protocol+"/"+tc.model, func(t *testing.T) {
			entry, err := manager.providerEntry(db.ModelProvider{ProviderID: "account", ProviderType: tc.protocol, Endpoint: tc.endpoint}, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			service := entry.service.(*reasoningService)
			levels := llm.SupportedReasoningLevels(service)
			if len(levels) == 0 || slices.Contains(levels, llm.ThinkingLevelOff) {
				t.Fatal("fixture must advertise reasoning levels without off")
			}
			capture := &captureThinkingService{got: llm.ThinkingLevelHigh}
			service.Service = capture
			if !slices.Equal(levels, llm.SupportedReasoningLevels(service)) {
				t.Fatal("lost provider-advertised levels")
			}
			if _, err := service.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelOff}); err != nil {
				t.Fatal(err)
			}
			if capture.got != llm.ThinkingLevelOff {
				t.Fatal("internal request did not reach the adapter for normalization")
			}
		})
	}
}
