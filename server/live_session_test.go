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

// liveSessionRequest is what a call sends the Live provider.
type liveSessionRequest struct {
	Session struct {
		Model        string            `json:"model"`
		Instructions string            `json:"instructions"`
		Input        []liveHistoryItem `json:"input"`
		Audio        *struct {
			Output *struct {
				Voice string `json:"voice"`
			} `json:"output"`
		} `json:"audio"`
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
	if req.Session.Model != "gpt-live-1" || req.Transport.SDP != "v=0\r\nm=audio test" || voicePrompt(req) != juniper {
		t.Fatalf("session = %+v", req)
	}
	if req.Session.Audio != nil {
		t.Fatalf("default voice should omit session.audio, got %+v", req.Session.Audio)
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

	// A global voice applies to the next call across existing conversations and drafts; ""/marin omits audio.
	e.want(200, "PUT", "/api/live-voice", `{"voice":"cedar"}`)
	req = call(id, "")
	if req.Session.Audio == nil || req.Session.Audio.Output == nil || req.Session.Audio.Output.Voice != "cedar" {
		t.Fatalf("session audio = %+v, want voice cedar", req.Session.Audio)
	}

	// A draft speaks as the profile its first send would start from, with the global voice.
	var draft struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/draft", `{"draft":""}`)), &draft); err != nil {
		t.Fatal(err)
	}
	req = call(draft.ConversationID, "")
	if voicePrompt(req) != "" || req.Session.Audio == nil || req.Session.Audio.Output == nil || req.Session.Audio.Output.Voice != "cedar" {
		t.Errorf("the default profile has no voice prompt, but uses global voice cedar: audio=%+v %s", req.Session.Audio, req.Session.Instructions)
	}
	if len(req.Session.Input) != 1 || req.Session.Input[0].Role != "developer" {
		t.Errorf("empty draft history = %+v", req.Session.Input)
	}
	req = call(draft.ConversationID, `,"cwd":"`+cwd+`","conversation_options":{"profile":"Personal","thinking_level":"high"}`)
	if voicePrompt(req) != juniper || req.Session.Audio == nil || req.Session.Audio.Output == nil || req.Session.Audio.Output.Voice != "cedar" || !strings.Contains(req.Session.Instructions, "Backend model: model-b.") || !strings.Contains(req.Session.Instructions, "Working directory: "+cwd+".") {
		t.Errorf("draft selection: audio=%+v %s", req.Session.Audio, req.Session.Instructions)
	}
	if row, err := e.database.GetConversationByID(t.Context(), draft.ConversationID); err != nil || !row.IsDraft || row.Model != nil {
		t.Fatalf("calling changed the draft: %+v", row)
	}

	// Resetting the global voice omits session.audio again.
	e.want(200, "PUT", "/api/live-voice", `{"voice":""}`)
	if req := call(id, ""); req.Session.Audio != nil {
		t.Fatalf("cleared global voice still sent audio: %+v", req.Session.Audio)
	}
}
