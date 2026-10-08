package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

type profileEnv struct {
	t        *testing.T
	database *db.DB
	ps       *predictable.Service
	ts       *httptest.Server
	srv      *Server
}

func newProfileEnv(t *testing.T) *profileEnv {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	srv := NewServer(database, &twoModelLLMManager{service: ps}, claudetool.ToolSetConfig{},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})), false, "model-a", "")
	srv.hooksDir = t.TempDir()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	ts := httptest.NewServer(srv.tcpHandler(mux))
	t.Cleanup(ts.Close)
	return &profileEnv{t: t, database: database, ps: ps, ts: ts, srv: srv}
}

// want sends a request and fails unless it gets code; it returns the body.
func (e *profileEnv) want(code int, method, path, body string) string {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != code {
		e.t.Fatalf("%s %s: got %d, want %d: %s", method, path, resp.StatusCode, code, b)
	}
	return string(b)
}

func (e *profileEnv) profiles() map[string]Profile {
	e.t.Helper()
	var list []Profile
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/profiles", "")), &list); err != nil {
		e.t.Fatal(err)
	}
	out := map[string]Profile{}
	for _, p := range list {
		out[p.Name] = p
	}
	return out
}

// waitTurn waits for the conversation's last message to be an agent's end of
// turn after the before'th.
func (e *profileEnv) waitTurn(id string, before int) {
	e.t.Helper()
	waitFor(e.t, 10*time.Second, func() bool {
		msgs := listMessages(e.t, e.database, id)
		last := msgs[len(msgs)-1]
		return len(msgs) > before && last.Type == string(db.MessageTypeAgent) && last.LlmData != nil && strings.Contains(*last.LlmData, `"EndOfTurn":true`)
	})
}

// lastTurnRequest is the last request with tools, which slug generation's
// lack.
func (e *profileEnv) lastTurnRequest() *llm.Request {
	reqs := e.ps.GetRecentRequests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if len(reqs[i].Tools) > 0 {
			return reqs[i]
		}
	}
	e.t.Fatal("no turn request")
	return nil
}

func systemText(req *llm.Request) string {
	var b strings.Builder
	for _, s := range req.System {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestProfilesCRUD(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	if p := e.profiles(); len(p) != 1 || !p["Default"].Default || p["Default"].Model != "" || p["Default"].SystemPrompt != "" {
		t.Fatalf("initial profiles: %+v", p)
	}
	e.want(201, "POST", "/api/profiles", `{"name":" Fast ","model":"model-b","thinking_level":"low","tool_overrides":{"bash":"off"}}`)
	e.want(409, "POST", "/api/profiles", `{"name":"Fast"}`)
	e.want(400, "POST", "/api/profiles", `{"name":""}`)
	e.want(400, "POST", "/api/profiles", `{"name":"X","model":"nope"}`)
	e.want(400, "POST", "/api/profiles", `{"name":"X","tool_overrides":{"bash":"maybe"}}`)
	// Settings sit at the top level; a nested object is a mistake, not a no-op.
	e.want(400, "POST", "/api/profiles", `{"name":"X","settings":{"model":"model-b"}}`)
	e.want(400, "POST", "/api/profiles", `{"name":"X","system_prompt":"{{.Nope}}"}`)
	e.want(400, "POST", "/api/profiles", `{"name":"X","system_prompt":"{{if}}"}`)
	e.want(201, "POST", "/api/profiles", `{"name":"Terse","system_prompt":"Be terse in {{.WorkingDirectory}}.{{if .GitInfo}} Repo {{.GitInfo.Root}}.{{end}}"}`)

	p := e.profiles()
	if f := p["Fast"]; f.Model != "model-b" || f.ThinkingLevel != "low" || f.ToolOverrides["bash"] != "off" || f.Default {
		t.Fatalf("Fast: %+v", f)
	}

	// Making Fast the default takes it from Default; the default can't be
	// deleted. PUT replaces the settings; "default" isn't one of them.
	e.want(200, "POST", "/api/profiles/Fast/default", "")
	e.want(404, "POST", "/api/profiles/Nope/default", "")
	e.want(400, "PUT", "/api/profiles/Fast", `{"model":"model-b","default":true}`)
	e.want(200, "PUT", "/api/profiles/Fast", `{"model":"model-b"}`)
	p = e.profiles()
	if !p["Fast"].Default || p["Default"].Default || p["Fast"].ThinkingLevel != "" || p["Fast"].ToolOverrides == nil {
		t.Fatalf("after making Fast the default: %+v", p)
	}
	e.want(409, "DELETE", "/api/profiles/Fast", "")
	e.want(404, "PUT", "/api/profiles/Nope", `{}`)
	e.want(404, "DELETE", "/api/profiles/Nope", "")
	e.want(204, "DELETE", "/api/profiles/Default", "")
	if p := e.profiles(); len(p) != 2 {
		t.Fatalf("after delete: %+v", p)
	}
	// Names are path segments.
	e.want(400, "POST", "/api/profiles", `{"name":"a/b"}`)
	e.want(400, "POST", "/api/profiles", `{"name":".."}`)
	e.want(400, "POST", "/api/profiles", `{"name":"`+strings.Repeat("x", 65)+`"}`)
	// A template must render outside a git repository too.
	e.want(400, "POST", "/api/profiles", `{"name":"X","system_prompt":"{{.GitInfo.Root}}"}`)

	// The check says what's wrong, and where, in the template's terms.
	for tmpl, want := range map[string]string{
		`Hi.`:                                `{"error":null}`,
		"Hi.\n{{.Nope}}":                     `{"error":{"line":2,"column":3,"message":"unknown variable .Nope"}}`,
		"é {{.Nope}}":                        `{"error":{"line":1,"column":5,"message":"unknown variable .Nope"}}`,
		`{{.GitInfo.Root}}`:                  `{"error":{"line":1,"column":11,"message":".GitInfo can be missing; use it inside {{if .GitInfo}}…{{end}}"}}`,
		`{{if .IsExeDev}}`:                   `{"error":{"line":1,"message":"the template ends before a }} or {{end}}"}}`,
		`{{range .WorkingDirectory}}{{end}}`: `{"error":{"line":1,"column":9,"message":".WorkingDirectory: range can't iterate over "}}`,
		"1\n{{index \"abc\" `x\ny`}}":        `{"error":{"line":2,"column":3,"message":"index \"abc\" ` + "`x\\ny`" + `: error calling index: cannot index slice/array with type string"}}`,
		"{{define \"a\"}}{{.Nope}}{{end}}{{template \"a\" .}}": `{"error":{"line":1,"column":17,"message":"unknown variable .Nope"}}`,
	} {
		body, _ := json.Marshal(map[string]string{"template": tmpl})
		if got := strings.TrimSpace(e.want(200, "POST", "/api/system-prompt/check", string(body))); got != want {
			t.Errorf("check %q:\n got %s\nwant %s", tmpl, got, want)
		}
	}
	if got := e.want(400, "POST", "/api/profiles", `{"name":"X","system_prompt":"{{.Nope}}"}`); !strings.Contains(got, "line 1: unknown variable .Nope") {
		t.Errorf("save error: %q", got)
	}

	var sp struct {
		Template  string                 `json:"template"`
		Variables []SystemPromptVariable `json:"variables"`
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/system-prompt", "")), &sp); err != nil {
		t.Fatal(err)
	}
	if sp.Template != systemPromptTemplate || len(sp.Variables) == 0 {
		t.Fatalf("system prompt: %d bytes, %d variables", len(sp.Template), len(sp.Variables))
	}
	if err := validateSystemPromptTemplate(sp.Template); err != nil {
		t.Fatalf("the built-in template fails validation: %v", err)
	}
}

// A conversation starts from a profile, and can switch to another mid-way:
// the next turn uses the new model, tools, and system prompt, and a marker
// says what changed.
func TestConversationSwitchesProfiles(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", `{"name":"Terse","model":"model-b","tool_overrides":{"bash":"off"},"system_prompt":"Be terse in {{.WorkingDirectory}}."}`)

	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	cwd := t.TempDir()
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+cwd+`","conversation_options":{"profile":"Terse"}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ConversationID
	e.waitTurn(id, 0)
	conv, err := e.database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if opts := db.ParseConversationOptions(conv.ConversationOptions); *conv.Model != "model-b" || opts.Profile != "Terse" || opts.ToolOverrides["bash"] != "off" {
		t.Fatalf("new conversation: model %s, options %+v", *conv.Model, opts)
	}
	req := e.lastTurnRequest()
	if got := strings.TrimSpace(systemText(req)); got != "Be terse in "+cwd+"." || hasTool(req, "bash") {
		t.Fatalf("first turn: system %q, bash %v", got, hasTool(req, "bash"))
	}

	// Switch to the default profile, but keep model-b.
	e.want(400, "POST", "/api/conversation/"+id+"/settings", `{"profile":"Nope"}`)
	e.want(400, "POST", "/api/conversation/"+id+"/settings", `{"modle":"model-b"}`)
	e.want(400, "POST", "/api/conversation/"+id+"/settings", `{"system_prompt":"{{.Nope}}"}`)
	var settings ConversationSettings
	if err := json.Unmarshal([]byte(e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"profile":"Default","model":"model-b"}`)), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Profile != "Default" || settings.Model != "model-b" || settings.SystemPrompt != "" || settings.ToolOverrides == nil {
		t.Fatalf("settings after switching: %+v", settings)
	}
	if got := e.want(200, "GET", "/api/conversation/"+id+"/settings", ""); strings.TrimSpace(got) != `{"profile":"Default","model":"model-b","thinking_level":"","tool_overrides":{},"compact_nudge_tokens":0,"system_prompt":""}` {
		t.Fatalf("GET settings: %s", got)
	}
	marker := lastModelChange(listMessages(t, e.database, id))
	var ud ModelChangeUserData
	if err := json.Unmarshal([]byte(*marker.UserData), &ud); err != nil {
		t.Fatal(err)
	}
	if ud.ProfileTo != "Default" || ud.To != "" || !ud.SystemPromptChanged || len(ud.ToolsOn) != 1 || ud.ToolsOn[0] != "bash" {
		t.Fatalf("marker: %+v", ud)
	}
	if ud.Previous == nil || ud.Previous.Profile != "Terse" || ud.Previous.ToolOverrides["bash"] != "off" {
		t.Fatalf("marker's previous settings: %+v", ud.Previous)
	}
	if ud.Text != "Profile changed to Default; tools turned on: bash; system prompt changed." {
		t.Fatalf("marker text: %q", ud.Text)
	}

	n := len(listMessages(t, e.database, id))
	e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"echo: again"}`)
	e.waitTurn(id, n)
	req = e.lastTurnRequest()
	if got := systemText(req); !strings.Contains(got, "Shelley") || !hasTool(req, "bash") {
		t.Fatalf("after switching: system %q, bash %v", got, hasTool(req, "bash"))
	}
	conv, _ = e.database.GetConversationByID(t.Context(), id)
	if opts := db.ParseConversationOptions(conv.ConversationOptions); *conv.Model != "model-b" || opts.Profile != "Default" || opts.SystemPrompt != "" || len(opts.ToolOverrides) != 0 {
		t.Fatalf("after switching: model %s, options %+v", *conv.Model, opts)
	}

	// Changing nothing records nothing.
	n = len(listMessages(t, e.database, id))
	e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"model":"model-b","tool_overrides":{}}`)
	if got := len(listMessages(t, e.database, id)); got != n {
		t.Fatalf("a no-op change added %d messages", got-n)
	}
	// Saving the settings as a new profile only relabels the conversation:
	// a marker, but no new system prompt.
	e.want(201, "POST", "/api/profiles", `{"name":"Same","model":"model-b"}`)
	e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"profile":"Same"}`)
	msgs := listMessages(t, e.database, id)
	if len(msgs) != n+1 || !strings.Contains(*msgs[n].UserData, `"text":"Profile changed to Same."`) {
		t.Fatalf("relabeling added %d messages, last %s", len(msgs)-n, *msgs[len(msgs)-1].UserData)
	}
	e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"model":"model-a","thinking_level":"high"}`)
	if ud := lastModelChange(listMessages(t, e.database, id)); !strings.Contains(*ud.UserData, `"text":"Model changed from Model B to Model A; reasoning changed from model's default to high."`) {
		t.Fatalf("marker: %s", *ud.UserData)
	}

	// A fork from before the switch goes back to Terse's settings.
	var fork struct {
		ConversationID string `json:"conversation_id"`
	}
	firstAgent := msgs[0]
	for _, m := range msgs {
		if m.Type == string(db.MessageTypeAgent) {
			firstAgent = m
			break
		}
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversation/"+id+"/fork", fmt.Sprintf(`{"sequence_id":%d}`, firstAgent.SequenceID))), &fork); err != nil {
		t.Fatal(err)
	}
	if got := e.want(200, "GET", "/api/conversation/"+fork.ConversationID+"/settings", ""); !strings.Contains(got, `"profile":"Terse","model":"model-b"`) || !strings.Contains(got, `"bash":"off"`) || !strings.Contains(got, `"system_prompt":"Be terse in {{.WorkingDirectory}}."`) {
		t.Fatalf("fork's settings: %s", got)
	}
}

// Settings named in a new conversation's options override its profile's,
// even with "".
func TestNewConversationOverridesProfile(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", `{"name":"Deep","model":"model-b","thinking_level":"high","tool_overrides":{"bash":"off"}}`)
	e.want(200, "POST", "/api/profiles/Deep/default", "")
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"thinking_level":""}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	got := e.want(200, "GET", "/api/conversation/"+created.ConversationID+"/settings", "")
	if !strings.Contains(got, `"profile":"Deep","model":"model-b","thinking_level":"","tool_overrides":{"bash":"off"}`) {
		t.Fatalf("settings: %s", got)
	}
	e.want(400, "POST", "/api/conversations/new", `{"message":"hello","conversation_options":{"profile":"Nope"}}`)

	// A system prompt is a setting like the rest, and a template.
	e.want(400, "POST", "/api/conversations/new", `{"message":"hello","conversation_options":{"system_prompt":"{{.Nope}}"}}`)
	e.want(400, "POST", "/api/conversations/draft", `{"draft":"hello","conversation_options":{"system_prompt":"{{.Nope}}"}}`)
	body = e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"system_prompt":"Be a pirate."}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	if got := e.want(200, "GET", "/api/conversation/"+created.ConversationID+"/settings", ""); !strings.Contains(got, `"profile":"Deep","model":"model-b"`) || !strings.Contains(got, `"system_prompt":"Be a pirate."`) {
		t.Fatalf("settings: %s", got)
	}
}

// A draft saved without a model starts on its profile's when sent without
// one too.
func TestDraftWithoutModelTakesProfiles(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", `{"name":"Deep","model":"model-b"}`)
	e.want(200, "POST", "/api/profiles/Deep/default", "")
	var draft struct {
		ConversationID string `json:"conversation_id"`
		Model          *string
	}
	body := e.want(201, "POST", "/api/conversations/draft", `{"draft":"hi","cwd":"`+t.TempDir()+`"}`)
	if err := json.Unmarshal([]byte(body), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Model != nil {
		t.Fatalf("draft model = %q, want unset", *draft.Model)
	}
	e.want(202, "POST", "/api/conversation/"+draft.ConversationID+"/chat", `{"message":"hello"}`)
	e.waitTurn(draft.ConversationID, 0)
	got := e.want(200, "GET", "/api/conversation/"+draft.ConversationID+"/settings", "")
	if !strings.Contains(got, `"profile":"Deep","model":"model-b"`) {
		t.Fatalf("settings: %s", got)
	}
}

// A send that resolved its model before a settings change doesn't build the
// loop on the old model.
func TestStaleModelSendRejected(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","model":"model-a","cwd":"`+t.TempDir()+`"}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	e.want(200, "POST", "/api/conversation/"+created.ConversationID+"/settings", `{"model":"model-b"}`)
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), created.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.AcceptUserMessage(t.Context(), e.ps, "model-a", llm.UserStringMessage("stale"))
	if !errors.Is(err, errConversationModelMismatch) {
		t.Fatalf("AcceptUserMessage on the old model: %v", err)
	}
}

// Markers name the reasoning in effect, so an unset level shows as its
// model's default.
func TestSettingsMarkerResolvesDefaultReasoning(t *testing.T) {
	models := []ModelInfo{{ID: "a", DefaultReasoningLevel: "medium"}, {ID: "b", DefaultReasoningLevel: "high"}}
	st := func(model, level string) ConversationSettings {
		return ConversationSettings{Settings: Settings{Model: model, ThinkingLevel: level, ToolOverrides: map[string]string{}}}
	}
	m, _, _ := settingsMarker(db.ConversationOptions{}, st("a", ""), st("b", ""), models)
	if m == nil || m.ReasoningFrom != "medium" || m.ReasoningTo != "high" {
		t.Fatalf("a -> b: %+v", m)
	}
	if m, _, reload := settingsMarker(db.ConversationOptions{}, st("a", ""), st("a", "medium"), models); m != nil || reload {
		t.Fatalf("spelling out the default: %+v, reload %v", m, reload)
	}
}

// A message queued on the model a settings change has since replaced is
// sent on the conversation's model.
func TestQueuedMessageFollowsModelSwitch(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","model":"model-a","cwd":"`+t.TempDir()+`"}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ConversationID
	e.waitTurn(id, 0)
	e.want(200, "POST", "/api/conversation/"+id+"/settings", `{"model":"model-b"}`)
	if _, err := e.database.AppendQueuedMessage(t.Context(), id, db.QueuedMessage{
		ID: "q", Llm: []byte(`{"Role":0,"Content":[{"Type":2,"Text":"queued on a"}]}`),
		CreatedAt: time.Now(), Model: "model-a",
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	<-manager.drainPendingMessages(e.srv)
	if q := queuedMessages(t, e.database, id); len(q) != 0 {
		t.Fatalf("still queued: %+v", q)
	}
	if !userMessageRowExists(t, e.database, id, "queued on a") {
		t.Fatal("queued message not sent")
	}
}
