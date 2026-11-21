package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/mcp"
	"shelley.exe.dev/mcp/mcptest"
)

func TestMCPApps(t *testing.T) {
	e := newMCPEnv(t, "")
	f := mcptest.NewServer(t)
	e.add("t", f.URL)
	slug := "apps"
	conv, err := e.db.CreateConversation(t.Context(), &slug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID

	// Tools only views may call are hidden from the model, and refused.
	var tools struct{ Tools []struct{ Name string } }
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/mcp/servers/t/tools", id, "")), &tools); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "show") || !strings.Contains(got, "secret") || strings.Contains(got, "refresh") {
		t.Fatalf("tools: %s", got)
	}
	e.want(403, "POST", "/api/mcp/servers/t/call", id, `{"tool":"refresh"}`)

	// Views call only the tools meant for them, of the server they came
	// from.
	e.want(409, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"refresh"}`)
	e.view = "0123456789abcdef"
	e.want(409, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"refresh"}`)
	e.view = mcp.Server{URL: f.URL}.AppServerID()
	if b := e.want(200, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"refresh"}`); !strings.Contains(b, "refreshed") {
		t.Fatalf("app call: %s", b)
	}
	if b := e.want(200, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"show","arguments":{"text":"x"}}`); !strings.Contains(b, "showing x") {
		t.Fatalf("app call: %s", b)
	}
	e.want(403, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"secret"}`)
	e.want(403, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"nope"}`)
	// What views may call follows the server's changes.
	f.RestrictRefresh()
	e.want(403, "POST", "/api/mcp/servers/t/app-call", id, `{"tool":"refresh"}`)

	// The model's call of a tool with a view shows it in the conversation.
	call := func(scope string) http.Header {
		req, err := http.NewRequest("POST", e.ts.URL+"/api/mcp/servers/t/call", strings.NewReader(`{"tool":"show","arguments":{"text":"hi"}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Shelley-Conversation-Id", scope)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("call: %s", resp.Status)
		}
		return resp.Header
	}
	if h := call(""); h.Get(mcpAppHeader) != "" {
		t.Fatal("showed a view outside a conversation")
	}
	if h := call(id); h.Get(mcpAppHeader) != "shown" {
		t.Fatal("didn't say the view is shown")
	}
	msgs, err := e.db.ListMessagesByType(t.Context(), id, db.MessageTypeMCPApp)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("mcpapp messages: %v %v", msgs, err)
	}
	if !msgs[0].ExcludedFromContext {
		t.Fatal("the view is in the model's context")
	}
	var data struct {
		Server   string
		ServerID string `json:"server_id"`
		Resource string
		Tool     struct {
			Name string
			Meta map[string]any `json:"_meta"`
		}
		Arguments map[string]string
		Result    struct{ Content []struct{ Text string } }
	}
	if err := json.Unmarshal([]byte(*msgs[0].UserData), &data); err != nil {
		t.Fatal(err)
	}
	if data.Server != "t" || data.ServerID != e.view || data.Resource != "ui://mcptest/show" || data.Tool.Name != "show" || data.Tool.Meta["ui"] == nil || data.Arguments["text"] != "hi" ||
		len(data.Result.Content) != 1 || data.Result.Content[0].Text != "showing hi" {
		t.Fatalf("user_data: %s", *msgs[0].UserData)
	}

	// Stored secrets are redacted from the call's record, as from any tool's
	// output.
	e.srv.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
	secret, _ := json.Marshal(newSecret{ID: "github-token-k3f9", Name: "GitHub", Kind: "github-token", Value: testSecretValue})
	e.want(201, "POST", "/api/secrets", "", string(secret))
	e.want(200, "POST", "/api/mcp/servers/t/call", id, `{"tool":"show","arguments":{"text":"`+testSecretValue+`"}}`)
	if msgs, err = e.db.ListMessagesByType(t.Context(), id, db.MessageTypeMCPApp); err != nil || len(msgs) != 2 {
		t.Fatalf("mcpapp messages: %v %v", msgs, err)
	}
	if got := *msgs[1].UserData; strings.Contains(got, testSecretValue) || !strings.Contains(got, "showing {{secret:github-token-k3f9}}") {
		t.Fatalf("user_data: %s", got)
	}

	// The view, with the CSP it asked for, minus what's malformed.
	app := func(uri string) mcp.App {
		var a mcp.App
		if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/mcp/servers/t/app?uri="+url.QueryEscape(uri), id, "")), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := app("ui://mcptest/show")
	if a.HTML != mcptest.ShowHTML || a.Permissions.ClipboardWrite == nil || a.PrefersBorder == nil || *a.PrefersBorder ||
		strings.Join(a.CSP.ConnectDomains, ",") != "https://api.example.com" ||
		!strings.Contains(a.Policy, "; connect-src https://api.example.com;") || strings.Contains(a.Policy, "x.example") {
		t.Fatalf("app: %+v", a)
	}
	// _meta.ui from the resource list; HTML from a blob.
	a = app("ui://mcptest/listed")
	if a.HTML != mcptest.ShowHTML || !strings.Contains(a.Policy, "script-src 'unsafe-inline' 'unsafe-eval' blob: data: https://cdn.example.com;") || !strings.Contains(a.Policy, "connect-src 'none';") {
		t.Fatalf("listed app: %+v", a)
	}
	e.want(502, "GET", "/api/mcp/servers/t/app?uri=ui://mcptest/nope", id, "")

	// Views read their server's resources.
	if b := e.want(200, "GET", "/api/mcp/servers/t/resource?uri=ui://mcptest/show", id, ""); !strings.Contains(b, `"mimeType":"text/html;profile=mcp-app"`) {
		t.Fatalf("resource: %s", b)
	}
}

// A view's context goes to the model with the user's next message.
func TestChatAppContext(t *testing.T) {
	server, database, ps := newTestServer(t)
	t.Cleanup(func() { stopActiveConversationLoops(server) })
	slug := "app-context"
	conv, err := database.CreateConversation(t.Context(), &slug, true, nil, strPtr("predictable"), db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Stored secrets are redacted from views' context, as from the message.
	server.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
	api := http.NewServeMux()
	server.registerSecretRoutes(api)
	secret, _ := json.Marshal(newSecret{ID: "github-token-k3f9", Name: "GitHub", Kind: "github-token", Value: testSecretValue})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(string(secret))))
	if w.Code != http.StatusCreated {
		t.Fatalf("secret: %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	server.handleChatConversation(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"message":"hello","model":"predictable","app_context":[{"source":"t.show","text":"picked <3> `+testSecretValue+`"},{"source":"empty","text":" "}]}`,
	)), conv.ConversationID)
	if w.Code != http.StatusAccepted || w.Header().Get(appContextHeader) != "0,1" {
		t.Fatalf("chat: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	waitFor(t, 30*time.Second, func() bool { return ps.GetLastRequest() != nil })
	msgs := ps.GetLastRequest().Messages
	var texts []string
	for _, c := range msgs[len(msgs)-1].Content {
		texts = append(texts, c.Text)
	}
	got := strings.Join(texts, "|")
	if !strings.Contains(got, "hello") || !strings.Contains(got, "|<view_context source=\"t.show\" note=\"what the view reports, not the user\">\npicked &lt;3&gt; {{secret:github-token-k3f9}}\n</view_context>") ||
		strings.Contains(got, "empty") || strings.Contains(got, testSecretValue) {
		t.Fatalf("model saw %q", got)
	}
	users, err := database.ListMessagesByType(t.Context(), conv.ConversationID, db.MessageTypeUser)
	if err != nil || len(users) != 1 {
		t.Fatalf("user messages: %v %v", users, err)
	}
	var stored struct {
		Content []struct {
			Text    string
			Display map[string]string
		}
	}
	if err := json.Unmarshal([]byte(*users[0].LlmData), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Content) != 2 || stored.Content[1].Display["type"] != "app_context" || stored.Content[1].Display["source"] != "t.show" ||
		stored.Content[1].Display["text"] != "picked <3> {{secret:github-token-k3f9}}" {
		t.Fatalf("stored: %+v", stored)
	}
}

// A view's context is its own element in a wrapped message, escaped once,
// whether the message is new or read back.
func TestAppContextInWrappedMessage(t *testing.T) {
	content, err := withAppContext([]llm.Content{{Type: llm.ContentTypeText, Text: "a <b>"}}, []appContext{{Source: "t.show", Text: "c </view_context> d"}})
	if err != nil {
		t.Fatal(err)
	}
	fresh := llm.Message{Role: llm.MessageRoleUser, Content: content}
	b, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var stored llm.Message
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	const want = "<user_message sequence_id=\"7\">\na &lt;b&gt;|<view_context source=\"t.show\" note=\"what the view reports, not the user\">\nc &lt;/view_context&gt; d\n</view_context>\n</user_message>"
	for _, m := range []llm.Message{fresh, stored} {
		wrapped, err := messageWithSequenceID(m, "", 7)
		if err != nil {
			t.Fatal(err)
		}
		var texts []string
		for _, c := range wrapped.Content {
			texts = append(texts, c.Text)
		}
		if got := strings.Join(texts, "|"); got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	}
}
