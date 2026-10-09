package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestLiveSessionEndpoint(t *testing.T) {
	t.Setenv("SHELLEY_LIVE_SESSION_URL", "")
	t.Setenv("OPENAI_API_KEY", "")
	if got := liveSessionEndpoint(); got != "https://llm.int.exe.xyz/v1/live/sessions" {
		t.Fatalf("default endpoint = %q, want free managed LLM integration", got)
	}
	if got := liveSessionAPIKey(liveSessionEndpoint()); got != "" {
		t.Fatalf("managed integration request must not send a key: %q", got)
	}

	t.Setenv("OPENAI_API_KEY", "sk-direct")
	if got := liveSessionEndpoint(); got != "https://llm.int.exe.xyz/v1/live/sessions" {
		t.Fatalf("configured backend key must not bypass the free gateway: %q", got)
	}
	if got := liveSessionAPIKey(liveSessionEndpoint()); got != "" {
		t.Fatalf("default gateway request must not forward the backend key: %q", got)
	}

	t.Setenv("SHELLEY_LIVE_SESSION_URL", "https://api.openai.com/v1/live/sessions")
	if got := liveSessionEndpoint(); got != "https://api.openai.com/v1/live/sessions" {
		t.Fatalf("explicit direct endpoint = %q", got)
	}
	if got := liveSessionAPIKey(liveSessionEndpoint()); got != "sk-direct" {
		t.Fatalf("direct provider key = %q", got)
	}

	t.Setenv("SHELLEY_LIVE_SESSION_URL", "https://llm.int.exe.xyz/v1/live/sessions")
	if got := liveSessionAPIKey(liveSessionEndpoint()); got != "" {
		t.Fatalf("integration override must not forward direct provider key: %q", got)
	}
}

func TestLiveSessionCreatesWebRTCSessionWithoutExposingKey(t *testing.T) {
	s, database, _ := newTestServer(t)
	cwd, model := "/work/project", "predictable"
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		typ db.MessageType
		msg llm.Message
	}{
		{db.MessageTypeUser, llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{llm.StringContent("fix the bug")}}},
		{db.MessageTypeTool, llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "t", ToolResult: []llm.Content{llm.StringContent("TOOL SECRET OUTPUT")}}}}},
		{db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.StringContent("fixed it")}, EndOfTurn: true}},
	} {
		if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{ConversationID: conversation.ConversationID, Type: m.typ, LLMData: m.msg}); err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/live/sessions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Session struct {
				Model        string            `json:"model"`
				Instructions string            `json:"instructions"`
				Input        []liveHistoryItem `json:"input"`
				Delegation   struct {
					Type string `json:"type"`
				} `json:"delegation"`
			} `json:"session"`
			Transport struct {
				Type string `json:"type"`
				SDP  string `json:"sdp"`
			} `json:"transport"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Session.Model != "gpt-live-1" || body.Session.Delegation.Type != "client" ||
			body.Transport.Type != "webrtc" || body.Transport.SDP != "v=0\r\nm=audio test" {
			t.Errorf("upstream body = %+v", body)
		}
		for _, want := range []string{
			"Backchannel policy:", "Interruption policy:", "Delegation policy:",
			"Delegate to the backend when:", "Do not delegate when:",
			"greeting you or making small talk", "brief clarification",
			"coding, a tool, a command, or factual investigation",
			"Do not invent or guess results",
		} {
			if !strings.Contains(body.Session.Instructions, want) {
				t.Errorf("instructions missing %q: %q", want, body.Session.Instructions)
			}
		}
		for _, banned := range []string{"every spoken request", "approval", "immediately"} {
			if strings.Contains(body.Session.Instructions, banned) {
				t.Errorf("instructions still force-relay (%q): %q", banned, body.Session.Instructions)
			}
		}
		for _, want := range []string{"currently idle", "Working directory: /work/project.", "Shelley's model: predictable."} {
			if !strings.Contains(body.Session.Instructions, want) {
				t.Errorf("instructions missing %q: %q", want, body.Session.Instructions)
			}
		}
		if len(body.Session.Input) != 2 || body.Session.Input[0].Role != "user" || body.Session.Input[0].Content[0].Text != "fix the bug" ||
			body.Session.Input[1].Role != "assistant" || body.Session.Input[1].Content[0].Text != "fixed it" {
			t.Errorf("session input = %+v", body.Session.Input)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"session":{"id":"live_example","secret":"hidden"},"transport":{"type":"webrtc","sdp":"answer"}}`)
	}))
	defer upstream.Close()
	s.liveSessionURL = upstream.URL + "/v1/live/sessions"
	s.liveSessionKey = "test-key"
	s.liveSessionClient = upstream.Client()

	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversation.ConversationID+"/live-session", strings.NewReader(`{"sdp":"v=0\r\nm=audio test"}`))
	w := httptest.NewRecorder()
	s.handleLiveSession(w, req, conversation.ConversationID)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); strings.Contains(got, "test-key") || strings.Contains(got, "hidden") ||
		!strings.Contains(got, `"id":"live_example"`) || !strings.Contains(got, `"sdp":"answer"`) {
		t.Fatalf("response = %s", got)
	}
}

func TestLiveSessionRejectsUnknownConversationAndOversizedOffer(t *testing.T) {
	s, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		body   string
		status int
	}{
		{"missing", `{"sdp":"v=0"}`, http.StatusNotFound},
		{conversation.ConversationID, `{"sdp":""}`, http.StatusBadRequest},
		{conversation.ConversationID, `{"sdp":"` + strings.Repeat("x", 128<<10) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		w := httptest.NewRecorder()
		s.handleLiveSession(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+tc.id+"/live-session", strings.NewReader(tc.body)), tc.id)
		if w.Code != tc.status {
			t.Errorf("id %q: status = %d, want %d: %s", tc.id, w.Code, tc.status, w.Body.String())
		}
	}
}

func TestLiveSessionDoesNotForwardProviderErrorsOrCredentials(t *testing.T) {
	s, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("keyless catalog request sent Authorization: %q", got)
		}
		http.Error(w, "sensitive upstream details", http.StatusForbidden)
	}))
	defer upstream.Close()
	s.liveSessionURL = upstream.URL
	s.liveSessionKey = ""
	s.liveSessionClient = upstream.Client()
	w := httptest.NewRecorder()
	s.handleLiveSession(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversation.ConversationID+"/live-session", strings.NewReader(`{"sdp":"v=0\r\n"}`)), conversation.ConversationID)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "sensitive") {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
}
