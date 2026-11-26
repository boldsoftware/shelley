package server

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// liveSessionRequest is what a call sends the Live provider.
type liveSessionRequest struct {
	Session struct {
		Model        string            `json:"model"`
		Instructions string            `json:"instructions"`
		Audio        json.RawMessage   `json:"audio"`
		Input        []liveHistoryItem `json:"input"`
	} `json:"session"`
	Transport struct {
		SDP string `json:"sdp"`
	} `json:"transport"`
}

// A call speaks as the current voice prompt of the profile its conversation
// names (a draft's selection, or else the default), carries the policies and
// only the typed dialogue, and asks no model.
func TestLiveSession(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	sessions := make(chan liveSessionRequest, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req liveSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Session.Input == nil {
			http.Error(w, "session.input must be an array", http.StatusBadRequest)
			return
		}
		sessions <- req
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"session":{"id":"live_x","secret":"hidden"},"transport":{"type":"webrtc","sdp":"answer"}}`)
	}))
	t.Cleanup(provider.Close)
	e.srv.liveSessionURL, e.srv.liveSessionClient = provider.URL, provider.Client()
	call := func(id, settings string) liveSessionRequest {
		t.Helper()
		body := e.want(201, "POST", "/api/conversation/"+id+"/live-session", `{"sdp":"v=0\r\nm=audio test"`+settings+`}`)
		if strings.Contains(body, "hidden") || !strings.Contains(body, `"sdp":"answer"`) {
			t.Fatalf("response = %s", body)
		}
		return <-sessions
	}
	voicePrompt := func(req liveSessionRequest) string {
		_, after, _ := strings.Cut(req.Session.Instructions, "<voice_guidance>\n")
		prompt, _, _ := strings.Cut(after, "\n</voice_guidance>")
		return prompt
	}

	const juniper = "You are Juniper. Speak warmly and briefly."
	e.want(201, "POST", "/api/profiles", `{"name":"Personal","model":"model-b","voice_prompt":"`+juniper+`"}`)
	cwd, model := t.TempDir(), "model-b"
	conv, err := e.database.CreateConversation(t.Context(), nil, true, &cwd, &model, db.ConversationOptions{Profile: "Personal"})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	for _, m := range []struct {
		typ      db.MessageType
		content  llm.Content
		userData any
		final    bool
	}{
		{db.MessageTypeUser, llm.StringContent("fix the bug"), nil, false},
		{db.MessageTypeUser, llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "t", ToolResult: []llm.Content{llm.StringContent("TOOL SECRET OUTPUT")}}, nil, false},
		{db.MessageTypeUser, llm.StringContent("subagent secret"), map[string]string{"sender_conversation_id": "child"}, false},
		{db.MessageTypeUser, llm.StringContent("Spoken request:\nlint it" + liveVoiceExchangeUnnumbered + "LIVE: Sure."), nil, false},
		{db.MessageTypeUser, llm.StringContent("Spoken request:\nqueued lint"), liveDeliveryUserData{DeliveryID: "delivery"}, false},
		{db.MessageTypeAgent, llm.StringContent("internal progress"), nil, false},
		{db.MessageTypeAgent, llm.StringContent("fixed it"), nil, true},
	} {
		role := llm.MessageRoleUser
		if m.typ == db.MessageTypeAgent {
			role = llm.MessageRoleAssistant
		}
		if _, err := e.database.CreateMessage(t.Context(), db.CreateMessageParams{ConversationID: id, Type: m.typ, UserData: m.userData, LLMData: llm.Message{Role: role, Content: []llm.Content{m.content}, EndOfTurn: m.final}}); err != nil {
			t.Fatal(err)
		}
	}

	req := call(id, "")
	if len(req.Session.Audio) != 0 || strings.Contains(req.Session.Instructions, liveProgressPolicy) || !strings.Contains(req.Session.Instructions, liveAcceptedPolicy) || strings.Contains(req.Session.Instructions, liveQuietHandoffPolicy) {
		t.Fatalf("default call overrides upstream behavior: %+v", req.Session)
	}
	if req.Session.Model != "gpt-live-1" || req.Transport.SDP != "v=0\r\nm=audio test" || voicePrompt(req) != juniper {
		t.Fatalf("session = %+v", req)
	}
	for _, want := range []string{"Delegation policy:", "Answer yourself first.", "are status questions, not small talk", "Interruption policy:", "Backend state: idle.", "Working directory: " + cwd + ".", "Backend model: model-b."} {
		if !strings.Contains(req.Session.Instructions, want) {
			t.Errorf("instructions lack %q:\n%s", want, req.Session.Instructions)
		}
	}
	var dialogue []string
	for _, item := range req.Session.Input {
		dialogue = append(dialogue, item.Role+": "+item.Content[0].Text)
	}
	// Live speaks first: the input ends with the greeting instruction.
	if got := strings.Join(dialogue, "|"); got != "user: fix the bug|user: Spoken request:\nlint it|user: Spoken request:\nqueued lint|assistant: fixed it|developer: "+liveGreeting {
		t.Errorf("Live got more than the dialogue: %q", got)
	}
	if n := len(e.ps.GetRecentRequests()); n != 0 || len(listMessages(t, e.database, id)) != 7 {
		t.Fatalf("a call asked a model %d times or wrote messages", n)
	}

	// An edit applies to the next call; "" means no persona.
	e.want(200, "PUT", "/api/profiles/Personal", `{"model":"model-b","voice_prompt":""}`)
	if req := call(id, ""); strings.Contains(req.Session.Instructions, "voice_guidance") {
		t.Errorf("instructions have voice guidance without a voice prompt:\n%s", req.Session.Instructions)
	}
	e.want(200, "PUT", "/api/profiles/Personal", `{"model":"model-b","voice_prompt":"`+juniper+`"}`)

	// A draft speaks as the profile its first send would start from.
	var draft struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/draft", `{"draft":""}`)), &draft); err != nil {
		t.Fatal(err)
	}
	req = call(draft.ConversationID, "")
	if voicePrompt(req) != "" {
		t.Errorf("the default profile has no voice prompt: %s", req.Session.Instructions)
	}
	if len(req.Session.Input) != 1 || req.Session.Input[0].Role != "developer" {
		t.Errorf("empty draft history = %+v", req.Session.Input)
	}
	req = call(draft.ConversationID, `,"cwd":"`+cwd+`","conversation_options":{"profile":"Personal","thinking_level":"high"}`)
	if voicePrompt(req) != juniper || !strings.Contains(req.Session.Instructions, "Backend model: model-b.") || !strings.Contains(req.Session.Instructions, "Working directory: "+cwd+".") {
		t.Errorf("draft selection: %s", req.Session.Instructions)
	}
	if row, err := e.database.GetConversationByID(t.Context(), draft.ConversationID); err != nil || !row.IsDraft || row.Model != nil {
		t.Fatalf("calling changed the draft: %+v", row)
	}
	for _, tt := range []struct {
		name, settings, voice string
		pace                  int
		progress              bool
	}{
		{"saved defaults", `{"voice":"marin","speaking_pace":0,"progress_updates":false}`, "", 0, false},
		{"voice only", `{"voice":"cinder","speaking_pace":0,"progress_updates":false}`, "cinder", 0, false},
		{"pace only", `{"voice":"marin","speaking_pace":1,"progress_updates":false}`, "", 1, false},
		{"progress only", `{"voice":"marin","speaking_pace":0,"progress_updates":true}`, "", 0, true},
		{"all enabled", `{"voice":"cinder","speaking_pace":1,"progress_updates":true}`, "cinder", 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.want(200, "PUT", "/api/live-settings", tt.settings)
			req := call(id, "")
			if tt.voice == "" {
				if len(req.Session.Audio) != 0 {
					t.Errorf("unexpected voice override: %s", req.Session.Audio)
				}
			} else if !strings.Contains(string(req.Session.Audio), `"voice":"`+tt.voice+`"`) {
				t.Errorf("missing selected voice: %s", req.Session.Audio)
			}
			wantGreeting := liveGreeting
			if tt.progress {
				wantGreeting = liveProgressGreeting
			}
			if got := req.Session.Input[len(req.Session.Input)-1].Content[0].Text; got != wantGreeting {
				t.Fatalf("greeting = %q, want %q", got, wantGreeting)
			}
			if strings.Contains(req.Session.Instructions, liveProgressPolicy) != tt.progress {
				t.Error("progress policy does not match opt-in")
			}
			if strings.Contains(req.Session.Instructions, livePaceInstruction(1)) != (tt.pace == 1) {
				t.Error("pace instruction does not match opt-in")
			}
		})
	}
}

//go:embed testdata/live_baseline_instructions.txt
var liveBaselineInstructions string

func TestLiveInstructionsOptIn(t *testing.T) {
	got := liveInstructions("Speak warmly.", "Backend state: idle.", 0, false)
	if got != liveBaselineInstructions {
		t.Fatal("default instructions differ from upstream baseline")
	}
	enabled := liveInstructions("Speak warmly.", "Backend state: idle.", 0, true)
	if !strings.Contains(enabled, liveQuietHandoffPolicy) || strings.Contains(enabled, liveAcceptedPolicy) {
		t.Fatal("opt-in handoffs must be silent")
	}
	if !strings.Contains(enabled, liveProgressPolicy) {
		t.Fatal("opt-in instructions lack progress policy")
	}
	paced := liveInstructions("Speak warmly.", "Backend state: idle.", 1, false)
	if !strings.Contains(paced, livePaceInstruction(1)) || strings.Contains(paced, liveProgressPolicy) {
		t.Fatal("pace setting unexpectedly changes progress policy")
	}
}
