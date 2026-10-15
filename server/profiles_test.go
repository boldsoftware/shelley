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
	"path/filepath"
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
	if p := e.profiles(); len(p) != 2 || !p["Default"].Default || p["Default"].Model != "" || p["Default"].SystemPrompt != "" {
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
	if p := e.profiles(); len(p) != 3 {
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

// The pre-installed Assistant is opt-in and uses the server's configured
// model. Its template must render in a real turn, not just the profile editor.
func TestPreinstalledAssistant(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	assistant, ok := e.profiles()["Assistant"]
	if !ok || assistant.Default || assistant.Model != "" {
		t.Fatalf("Assistant: present %v, profile %+v", ok, assistant)
	}
	if err := validateSystemPromptTemplate(assistant.SystemPrompt); err != nil {
		t.Fatalf("Assistant template: %v", err)
	}
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	cwd := t.TempDir()
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+cwd+`","conversation_options":{"profile":"Assistant"}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	var settings ConversationSettings
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/conversation/"+created.ConversationID+"/settings", "")), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Profile != "Assistant" || settings.Model != "model-a" {
		t.Fatalf("Assistant conversation: %+v", settings)
	}
	req := e.lastTurnRequest()
	if !hasTool(req, claudetool.MessageUserName) || !hasTool(req, "compact_in_place") || hasTool(req, "llm_one_shot") {
		t.Fatalf("Assistant didn't get its messaging tools")
	}
	if hasTool(req, "output_iframe") {
		t.Fatal("text-first Assistant must not offer output_iframe")
	}
	prompt := systemText(req)
	if !strings.Contains(prompt, cwd) || !strings.Contains(prompt, "life and work") {
		t.Fatalf("Assistant prompt didn't render: %q", prompt)
	}
}

// Saving without changes must not opt a built-in out of shipped updates;
// rejected edits must not do so either. Each editable field does opt out.
func TestBuiltinProfileEdits(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"model", "tools", "prompt", "voice", "reasoning", "compact"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			e := newProfileEnv(t)
			initial := e.profiles()["Default"]
			if !initial.Builtin || initial.UserEdited {
				t.Fatalf("initial Default: %+v", initial)
			}
			save := func(st Settings, voice string) Profile {
				t.Helper()
				body, err := json.Marshal(storedProfile{Settings: st, VoicePrompt: voice})
				if err != nil {
					t.Fatal(err)
				}
				var p Profile
				if err := json.Unmarshal([]byte(e.want(200, "PUT", "/api/profiles/Default", string(body))), &p); err != nil {
					t.Fatal(err)
				}
				return p
			}
			if p := save(initial.Settings, initial.VoicePrompt); p.UserEdited || !p.Builtin {
				t.Fatalf("no-op save opted out: %+v", p)
			}
			for _, body := range []string{
				`{"model":"missing-model"}`,
				`{"tool_overrides":{"bash":"maybe"}}`,
				`{"system_prompt":"{{.Nope}}"}`,
				`{"voice_prompt":"` + strings.Repeat("x", 4001) + `"}`,
			} {
				e.want(400, "PUT", "/api/profiles/Default", body)
			}
			if p := e.profiles()["Default"]; p.UserEdited || !p.Settings.equal(initial.Settings) || p.VoicePrompt != initial.VoicePrompt {
				t.Fatalf("failed validation changed profile: %+v", p)
			}
			edited := initial.Settings
			voice := initial.VoicePrompt
			switch field {
			case "model":
				edited.Model = "model-b"
			case "tools":
				edited.ToolOverrides = map[string]string{"bash": "off"}
			case "prompt":
				edited.SystemPrompt = "A custom prompt."
			case "voice":
				voice = "A custom voice."
			case "reasoning":
				edited.ThinkingLevel = "low"
			case "compact":
				edited.CompactNudgeTokens = 12000
			}
			if p := save(edited, voice); !p.Builtin || !p.UserEdited || !p.Settings.equal(edited) || p.VoicePrompt != voice {
				t.Fatalf("edit not persisted: %+v", p)
			}
			if p := e.profiles()["Default"]; !p.UserEdited {
				t.Fatalf("list lost edit status: %+v", p)
			}
		})
	}
}

// Restore affects the profile, not conversations that already copied its
// settings, and must preserve the user's default-profile selection.
func TestRestoreBuiltinProfile(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	initial := e.profiles()["Default"]
	e.want(200, "PUT", "/api/profiles/Default", `{"model":"model-b","thinking_level":"low","compact_nudge_tokens":12000,"tool_overrides":{"bash":"off"},"system_prompt":"Custom chat prompt.","voice_prompt":"Custom voice prompt."}`)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`"}`)), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	before, err := e.database.GetConversationByID(t.Context(), created.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if opts := db.ParseConversationOptions(before.ConversationOptions); opts.Profile != "Default" || opts.SystemPrompt != "Custom chat prompt." || opts.ToolOverrides["bash"] != "off" || *before.Model != "model-b" {
		t.Fatalf("conversation didn't copy edited profile: %+v", before)
	}
	var restored Profile
	if err := json.Unmarshal([]byte(e.want(200, "POST", "/api/profiles/Default/restore", "")), &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Builtin || restored.UserEdited || !restored.Default || !restored.Settings.equal(initial.Settings) || restored.VoicePrompt != initial.VoicePrompt {
		t.Fatalf("restore didn't recover shipped defaults: %+v", restored)
	}
	if p := e.profiles()["Default"]; !p.Builtin || p.UserEdited || !p.Default || !p.Settings.equal(initial.Settings) || p.VoicePrompt != initial.VoicePrompt {
		t.Fatalf("restored profile wasn't persisted: %+v", p)
	}
	after, err := e.database.GetConversationByID(t.Context(), created.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ConversationOptions != before.ConversationOptions || *after.Model != *before.Model {
		t.Fatalf("restore changed existing chat: before %+v, after %+v", before, after)
	}
	// Restoring an already-current profile is safe too.
	e.want(200, "POST", "/api/profiles/Default/restore", "")
	e.want(201, "POST", "/api/profiles", `{"name":"Custom","model":"model-b","voice_prompt":"Keep me."}`)
	custom := e.profiles()["Custom"]
	if custom.Builtin {
		t.Fatalf("custom profile reported built-in: %+v", custom)
	}
	e.want(400, "POST", "/api/profiles/Custom/restore", "")
	if p := e.profiles()["Custom"]; !p.Settings.equal(custom.Settings) || p.VoicePrompt != custom.VoicePrompt || p.UserEdited != custom.UserEdited {
		t.Fatalf("rejected restore changed custom profile: %+v", p)
	}
	e.want(404, "POST", "/api/profiles/Missing/restore", "")
	// Deleted built-ins aren't resurrected by restore.
	e.want(200, "POST", "/api/profiles/Custom/default", "")
	e.want(204, "DELETE", "/api/profiles/Default", "")
	e.want(404, "POST", "/api/profiles/Default/restore", "")
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
	if got := e.want(200, "GET", "/api/conversation/"+id+"/settings", ""); strings.TrimSpace(got) != `{"profile":"Default","model":"model-b","thinking_level":"","tool_overrides":{},"compact_nudge_tokens":0,"idle_compaction_minutes":0,"idle_compaction_prompt":"","cache_keep_warm_minutes":0,"system_prompt":""}` {
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

// A nudge spelled out at the default is the default: a conversation started
// with the UI's 0 from a profile that stores it keeps following the profile.
func TestDefaultNudgeSpelledOutIsTheProfiles(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", fmt.Sprintf(`{"name":"Spelled","model":"model-b","compact_nudge_tokens":%d}`, defaultCompactNudgeTokens))
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Spelled","compact_nudge_tokens":0}}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	conv, err := e.database.GetConversationByID(t.Context(), created.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if db.ParseConversationOptions(conv.ConversationOptions).SettingsEdited {
		t.Fatal("the default nudge, spelled differently, edited the profile's settings")
	}
}

func TestNewConversationHookOverridesProfileModel(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	defer stopActiveConversationLoops(e.srv)
	hook := "#!/bin/sh\ncat >/dev/null\necho '{\"model\":\"model-b\"}'\n"
	if err := os.WriteFile(filepath.Join(e.srv.hooksDir, hookNewConversation), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"echo: hello"}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	conv, err := e.database.GetConversationByID(t.Context(), created.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if got := conversationSettings(*conv); got.Model != "model-b" || !got.Edited {
		t.Fatalf("hook model was not retained: %+v", got)
	}
}

func TestDistillNewGenerationOverridesProfileModel(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	defer stopActiveConversationLoops(e.srv)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"echo: hello"}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ConversationID
	e.waitTurn(id, 0)
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	e.want(201, "POST", "/api/conversations/distill-new-generation", fmt.Sprintf(`{"source_conversation_id":%q,"model":"model-b"}`, id))
	waitFor(t, 10*time.Second, func() bool { return !manager.IsDistilling() })
	before := len(listMessages(t, e.database, id))
	e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"echo: next"}`)
	e.waitTurn(id, before)
	conv, err := e.database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := conversationSettings(*conv); got.Model != "model-b" || !got.Edited || manager.GetModel() != "model-b" {
		t.Fatalf("distill model was not retained: %+v, running model %q", got, manager.GetModel())
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

// A profile's voice prompt is the profile's own, not a setting: a PUT
// without it, like the model picker's, keeps it, and conversations never
// copy it.
func TestProfileVoicePrompt(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	created := e.want(201, "POST", "/api/profiles", `{"name":"Tutor","model":"model-b","voice_prompt":"  You are Jules. Speak French.\n"}`)
	if !strings.Contains(created, `"voice_prompt":"You are Jules. Speak French."`) {
		t.Fatalf("created: %s", created)
	}
	e.want(200, "PUT", "/api/profiles/Tutor", `{"model":"model-a","thinking_level":"low"}`)
	if got := e.profiles()["Tutor"]; got.VoicePrompt != "You are Jules. Speak French." || got.Model != "model-a" {
		t.Fatalf("a settings-only PUT changed the voice prompt: %+v", got)
	}
	e.want(200, "PUT", "/api/profiles/Tutor", `{"model":"model-a","voice_prompt":"VOICE-PROMPT-SENTINEL"}`)
	e.want(400, "PUT", "/api/profiles/Tutor", `{"model":"model-a","voice_prompt":"`+strings.Repeat("x", 4001)+`"}`)
	if got := e.profiles()["Tutor"]; got.VoicePrompt != "VOICE-PROMPT-SENTINEL" || got.ThinkingLevel != "" {
		t.Fatalf("replaced: %+v", got)
	}

	var conv struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", `{"message":"hello","cwd":"`+t.TempDir()+`","conversation_options":{"profile":"Tutor"}}`)), &conv); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(conv.ConversationID, 0)
	stored, err := e.database.GetConversationByID(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.ConversationOptions, "VOICE-PROMPT") || db.ParseConversationOptions(stored.ConversationOptions).Profile != "Tutor" {
		t.Fatalf("a conversation copied the voice prompt: %s", stored.ConversationOptions)
	}
}

func TestProfileIdlePolicyIsCopiedAndCanBeDisabled(t *testing.T) {
	e := newProfileEnv(t)
	for _, invalid := range []string{
		`{"name":"Invalid","idle_compaction_minutes":-1}`,
		fmt.Sprintf(`{"name":"Overflow","idle_compaction_minutes":%d}`, maxIdleCompactionMinutes+1),
		`{"name":"Cold","cache_keep_warm_minutes":-1}`,
		fmt.Sprintf(`{"name":"Endless","cache_keep_warm_minutes":%d}`, maxIdleCompactionMinutes+1),
		`{"name":"Toolless","idle_compaction_minutes":7,"tool_overrides":{"compact_in_place":"off"}}`,
	} {
		e.want(400, "POST", "/api/profiles", invalid)
	}
	e.want(201, "POST", "/api/profiles", `{"name":"Quiet","idle_compaction_minutes":7,"idle_compaction_prompt":"Keep plans.","cache_keep_warm_minutes":3,"tool_overrides":{"compact_in_place":"on"}}`)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new",
		`{"message":"hello","conversation_options":{"profile":"Quiet"}}`)), &created); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(created.ConversationID, 0)
	e.want(200, "PUT", "/api/profiles/Quiet", `{"idle_compaction_minutes":11,"idle_compaction_prompt":"Keep dates.","cache_keep_warm_minutes":5,"tool_overrides":{"compact_in_place":"on"}}`)
	path := "/api/conversation/" + created.ConversationID + "/settings"
	read := func(body string, want [2]int, prompt string) {
		t.Helper()
		var got ConversationSettings
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if g := [2]int{got.IdleCompactionMinutes, got.CacheKeepWarmMinutes}; g != want || got.IdleCompactionPrompt != prompt {
			t.Fatalf("idle compaction, keep warm = %v %q, want %v %q", g, got.IdleCompactionPrompt, want, prompt)
		}
	}
	read(e.want(200, "GET", path, ""), [2]int{7, 3}, "Keep plans.")
	read(e.want(200, "POST", path, `{"profile":"Quiet"}`), [2]int{11, 5}, "Keep dates.")
	e.want(400, "POST", path, `{"tool_overrides":{"compact_in_place":"off"}}`)
	read(e.want(200, "POST", path, `{"idle_compaction_minutes":0,"idle_compaction_prompt":"","cache_keep_warm_minutes":0}`), [2]int{}, "")
	read(e.want(200, "GET", path, ""), [2]int{}, "")
}

// Copy-on-write: a conversation takes its profile's edits at its next turn
// until it edits its own settings, even back to the profile's; switching to
// the profile clears that. A deleted profile leaves them as they are.
func TestConversationFollowsProfile(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", `{"name":"P","model":"model-a","system_prompt":"Version one."}`)
	start := func() string {
		t.Helper()
		var created struct {
			ConversationID string `json:"conversation_id"`
		}
		body := e.want(201, "POST", "/api/conversations/new", `{"message":"hello","conversation_options":{"profile":"P"}}`)
		if err := json.Unmarshal([]byte(body), &created); err != nil {
			t.Fatal(err)
		}
		e.waitTurn(created.ConversationID, 0)
		return created.ConversationID
	}
	turn := func(id string) *llm.Request {
		t.Helper()
		n := len(listMessages(t, e.database, id))
		e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"echo: again"}`)
		e.waitTurn(id, n)
		return e.lastTurnRequest()
	}
	edit := func(prompt string) {
		t.Helper()
		e.want(200, "PUT", "/api/profiles/P", `{"model":"model-b","tool_overrides":{"bash":"off"},"system_prompt":"`+prompt+`"}`)
	}
	shared, own, button := start(), start(), start()
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"tool_overrides":{"browser":"off"}}`)
	// The Compact in Place button's change is the conversation's own too.
	if w := postEnableCompactInPlace(t, e.srv, button); w.Code != 200 {
		t.Fatalf("enable compact_in_place: %d %s", w.Code, w.Body)
	}

	edit("Version two.")
	req := turn(shared)
	conv, err := e.database.GetConversationByID(t.Context(), shared)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(systemText(req)); got != "Version two." || hasTool(req, "bash") || *conv.Model != "model-b" {
		t.Fatalf("shared conversation didn't follow: system %q, bash %v, model %s", got, hasTool(req, "bash"), *conv.Model)
	}
	marker := lastModelChange(listMessages(t, e.database, shared))
	if marker == nil || !strings.Contains(*marker.UserData, "system prompt changed") {
		t.Fatal("following recorded no marker")
	}
	if got := strings.TrimSpace(systemText(turn(own))); got != "Version one." {
		t.Fatalf("changed conversation followed: %q", got)
	}
	if req := turn(button); !hasTool(req, claudetool.CompactInPlaceName) {
		t.Fatal("following undid the Compact in Place button")
	}

	// Reverting follows again; a change that changes nothing doesn't count.
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"profile":"P"}`)
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"tool_overrides":{"bash":"off"}}`)
	edit("Version three.")
	if got := strings.TrimSpace(systemText(turn(own))); got != "Version three." {
		t.Fatalf("reverted conversation didn't follow: %q", got)
	}
	// Changing a setting and changing it back is still an edit.
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"tool_overrides":{}}`)
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"tool_overrides":{"bash":"off"}}`)
	edit("Version four.")
	if got := strings.TrimSpace(systemText(turn(own))); got != "Version three." {
		t.Fatalf("edited conversation followed: %q", got)
	}

	// A fork restoring settings from before a later change keeps them.
	msgs := listMessages(t, e.database, own)
	cutoff := msgs[len(msgs)-1].SequenceID
	e.want(200, "POST", "/api/conversation/"+own+"/settings", `{"thinking_level":"high"}`)
	fork := forkAt(t, e.srv, own, cutoff)
	edit("Version five.")
	if got := strings.TrimSpace(systemText(turn(fork))); got != "Version three." {
		t.Fatalf("fork of an edited conversation followed: %q", got)
	}

	e.want(204, "DELETE", "/api/profiles/P", "")
	if got := strings.TrimSpace(systemText(turn(shared))); got != "Version two." {
		t.Fatalf("deleting the profile changed the conversation: %q", got)
	}
}
