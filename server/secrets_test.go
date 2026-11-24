package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/secretdetect"
)

// Split, so the source holds no key-shaped literal for secret scanners.
const (
	testSecretValue = "ghp_" + "TESTsecretVALUE0123456789abcdefABCDE"
	testOtherValue  = "ghp_" + "OTHERsecretVALUE0123456789abcdefABCD"
)

var testMarkerRE = regexp.MustCompile(`\[Secret extracted by Shelley; shelley secret (github-pat-[a-z0-9]{4}) prints the path of a file holding it\]`)

// A secret in a message from the chat API is stored, sealed, for a day, and
// replaced by a marker; it never reaches messages, model requests, logs, or
// the database unsealed. `shelley secret` reads it (POST .../reveal), and
// tool output that holds it gets the marker too.
func TestSecrets(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ps := predictable.NewService()
	s := NewServer(database, &testLLMManager{service: ps}, claudetool.ToolSetConfig{}, logger, true, "predictable", "")
	s.hooksDir = t.TempDir()
	s.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
	api := http.NewServeMux()
	s.registerSecretRoutes(api)
	handler := LoggerMiddleware(logger)(api)
	call := func(method, path, body string, want int, header ...string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	list := func() []Secret {
		t.Helper()
		var out []Secret
		if err := json.Unmarshal(call("GET", "/api/secrets", "", 200), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	dir := t.TempDir()
	conv, err := database.CreateConversation(t.Context(), nil, true, &dir, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID

	// The composer asks which spans will be taken out, in UTF-16 code
	// units: none with the flag off.
	detect, _ := json.Marshal(map[string]string{"text": "🔑 " + testSecretValue + " ok {{secret=hunter2hunter2}}"})
	if err := database.SetFeatureFlagOverride(t.Context(), FlagSecrets.Name, "false"); err != nil {
		t.Fatal(err)
	}
	if got := string(call("POST", "/api/secrets/detect", string(detect), 200)); strings.TrimSpace(got) != "[]" {
		t.Fatalf("detect with the flag off: %s", got)
	}
	if err := database.DeleteFeatureFlagOverride(t.Context(), FlagSecrets.Name); err != nil {
		t.Fatal(err)
	}
	var spans []SecretSpan
	json.Unmarshal(call("POST", "/api/secrets/detect", string(detect), 200), &spans)
	mark := 3 + len(testSecretValue) + len(" ok ")
	if want := []SecretSpan{
		{Start: 3, End: 3 + len(testSecretValue), Kind: "github-pat", Label: secretdetect.KindLabel("github-pat")},
		{Start: mark, End: mark + len("{{secret=hunter2hunter2}}"), Kind: "secret", Label: secretdetect.KindLabel("secret"), Manual: true},
	}; !slices.Equal(spans, want) {
		t.Fatalf("detect: %+v, want %+v", spans, want)
	}

	// The user sends it, twice in one message.
	postChatMessage(t, s, id, "echo: the token is "+testSecretValue+", again "+testSecretValue)
	waitForIdle(t, s, id)
	secrets := list()
	if len(secrets) != 1 || secrets[0].Kind != "github-pat" || *secrets[0].ConversationID != id ||
		time.Until(secrets[0].ExpiresAt) < 23*time.Hour || time.Until(secrets[0].ExpiresAt) > 25*time.Hour {
		t.Fatalf("list %+v", secrets)
	}
	secretID := secrets[0].ID
	if fi, err := os.Stat(s.secrets.keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}

	// The agent reads it into a file (as shelley secret does), and cats
	// the file; the user pastes it again.
	file := filepath.Join(dir, "token")
	var got map[string]string
	json.Unmarshal(call("POST", "/api/secrets/"+secretID+"/reveal", "", 200), &got)
	if got["value"] != testSecretValue {
		t.Fatalf("read %v", got)
	}
	if err := os.WriteFile(file, []byte(got["value"]), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"bash: cat token", "echo: once more " + testSecretValue} {
		postChatMessage(t, s, id, message)
		waitForIdle(t, s, id)
	}

	messages, err := database.ListMessages(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var stored strings.Builder
	for _, m := range messages {
		for _, field := range []*string{m.LlmData, m.UserData, m.DisplayData} {
			if field != nil {
				stored.WriteString(*field + "\n")
			}
		}
	}
	requests, err := json.Marshal(ps.GetRecentRequests())
	if err != nil {
		t.Fatal(err)
	}
	var sealed []generated.ListSealedSecretsRow
	database.Queries(t.Context(), func(q *generated.Queries) error {
		sealed, err = q.ListSealedSecrets(t.Context())
		return err
	})
	if len(sealed) != 1 {
		t.Fatalf("sealed rows %v %v", sealed, err)
	}
	for what, s := range map[string]string{"messages": stored.String(), "model requests": string(requests), "logs": logs.String(), "database": string(sealed[0].Sealed)} {
		if strings.Contains(s, testSecretValue) {
			t.Errorf("the value is in the %s:\n%s", what, s)
		}
	}
	marker := secretdetect.Marker(secretID)
	if m := testMarkerRE.FindStringSubmatch(marker); m == nil || m[1] != secretID {
		t.Fatalf("marker %q", marker)
	}
	if n := strings.Count(string(requests), marker); n < 4 {
		t.Errorf("marker %d times in model requests, want the first message's two, the bash output and the last message:\n%s", n, requests)
	}

	// Management: revealed, deleted.
	if body := call("POST", "/api/secrets/"+secretID+"/reveal", "", 200); !strings.Contains(string(body), testSecretValue) {
		t.Fatalf("reveal %s", body)
	}
	call("DELETE", "/api/secrets/"+secretID, "", 204)
	if body := call("POST", "/api/secrets/"+secretID+"/reveal", "", 404); string(body) != secretID+" expired or was deleted\n" {
		t.Fatalf("reveal after delete: %q", body)
	}
	call("DELETE", "/api/secrets/"+secretID, "", 404)
	if got, err := s.secrets.Redact(t.Context(), testSecretValue); err != nil || got != testSecretValue {
		t.Fatalf("redaction after delete: %q %v", got, err)
	}

	// A mark names its secret; one too short to store is refused.
	if out, err := s.secrets.extract(t.Context(), id, "use {{secret:deploy-key=hunter2hunter2}} here", extraction{}); err != nil || out != "use "+secretdetect.Marker("deploy-key")+" here" {
		t.Fatalf("mark: %q %v", out, err)
	}
	if _, err := s.secrets.extract(t.Context(), id, "{{secret=abc}}", extraction{}); !errors.Is(err, db.ErrInvalid) {
		t.Fatalf("short mark: %v", err)
	}
	// A stored value inside a new secret doesn't split it; a name too long
	// for an ID gets a generated one.
	long := strings.Repeat("a", 65)
	out, err := s.secrets.extract(t.Context(), id, "{{secret=xxhunter2hunter2yy}} {{secret:"+long+"=hunter3hunter3}} hunter2hunter2", extraction{})
	if m := regexp.MustCompile(`^\[Secret extracted by Shelley; shelley secret (secret-[a-z0-9]{4}) prints the path of a file holding it\] \[Secret extracted by Shelley; shelley secret (secret-[a-z0-9]{4}) prints the path of a file holding it\] (.*)$`).FindStringSubmatch(out); err != nil || m == nil || m[3] != secretdetect.Marker("deploy-key") {
		t.Fatalf("marks: %q %v", out, err)
	} else {
		for i, want := range []string{"xxhunter2hunter2yy", "hunter3hunter3"} {
			if got, err := s.secrets.Value(t.Context(), m[i+1]); err != nil || got != want {
				t.Fatalf("%s = %q %v, want %q", m[i+1], got, err, want)
			}
		}
	}

	// A stored value keeps a token inside it, and the composer hears of it.
	combo := "dbpass-Zq9 ghp_" + "COMBOsecretVALUE0123456789abcdefABCD tail-Xy7"
	if _, err := s.secrets.extract(t.Context(), id, "{{secret:combo="+combo+"}}", extraction{}); err != nil {
		t.Fatal(err)
	}
	if out, err := s.secrets.extract(t.Context(), id, "again: "+combo+".", extraction{marksOnly: true}); err != nil || out != "again: "+secretdetect.Marker("combo")+"." {
		t.Fatalf("stored value with a token inside: %q %v", out, err)
	}
	// So is its JSON-escaped form, as in a pasted service account file.
	if _, err := s.secrets.extract(t.Context(), id, "{{secret:ml=line1-Qz8\nline2-Wx4}}", extraction{}); err != nil {
		t.Fatal(err)
	}
	if out, err := s.secrets.extract(t.Context(), id, `{"k": "line1-Qz8\nline2-Wx4"}`, extraction{}); err != nil || out != `{"k": "`+secretdetect.Marker("ml")+`"}` {
		t.Fatalf("escaped stored value: %q %v", out, err)
	}
	detect, _ = json.Marshal(map[string]string{"text": "again: " + combo})
	json.Unmarshal(call("POST", "/api/secrets/detect", string(detect), 200), &spans)
	if want := []SecretSpan{{Start: 7, End: 7 + len(combo), Stored: true}}; !slices.Equal(spans, want) {
		t.Fatalf("detect stored: %+v, want %+v", spans, want)
	}

	// An expired secret is gone: not listed, read or redacted.
	s.secrets.lifetime = -time.Second
	out, err = s.secrets.extract(t.Context(), id, testOtherValue, extraction{})
	if err != nil || !testMarkerRE.MatchString(out) {
		t.Fatalf("extract: %q %v", out, err)
	}
	expired := testMarkerRE.FindStringSubmatch(out)[1]
	call("POST", "/api/secrets/"+expired+"/reveal", "", 404)
	if secrets := list(); len(secrets) != 5 || !slices.ContainsFunc(secrets, func(s Secret) bool { return s.ID == "deploy-key" }) {
		t.Fatalf("list %+v", secrets)
	}
	if got, err := s.secrets.Redact(t.Context(), testOtherValue); err != nil || got != testOtherValue {
		t.Fatalf("redaction after expiry: %q %v", got, err)
	}
}

// A new conversation's first message: its secrets belong to it. With
// keep_found_secrets, what detection finds goes as it is, but values
// already stored and marks are still taken out. A mark that can't be
// stored fails the message before it makes a conversation.
func TestSecretsInNewConversation(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	s.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
	// start returns the new conversation, waiting for its turn to end.
	start := func(req ChatRequest) (*httptest.ResponseRecorder, string) {
		t.Helper()
		req.Model, req.Cwd = "predictable", t.TempDir()
		body, _ := json.Marshal(NewConversationRequest{ChatRequest: req})
		w := httptest.NewRecorder()
		s.handleNewConversation(w, httptest.NewRequest("POST", "/api/conversations/new", bytes.NewReader(body)))
		if w.Code != http.StatusCreated {
			return w, ""
		}
		var resp struct {
			ConversationID string `json:"conversation_id"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		waitForIdle(t, s, resp.ConversationID)
		return w, resp.ConversationID
	}
	count := func() (n int64) {
		t.Helper()
		if err := database.Queries(t.Context(), func(q *generated.Queries) (err error) {
			n, err = q.CountConversations(t.Context())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	sent := func(conversationID string) string {
		t.Helper()
		messages, err := database.ListMessages(t.Context(), conversationID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Type == string(db.MessageTypeUser) && m.LlmData != nil {
				var msg struct{ Content []struct{ Text string } }
				json.Unmarshal([]byte(*m.LlmData), &msg)
				return msg.Content[0].Text
			}
		}
		t.Fatalf("no user message in %s", conversationID)
		return ""
	}

	w, first := start(ChatRequest{Message: "echo: " + testSecretValue})
	if w.Code != http.StatusCreated {
		t.Fatalf("new conversation: %d %s", w.Code, w.Body)
	}
	secrets, err := s.secrets.list(t.Context())
	if err != nil || len(secrets) != 1 || secrets[0].ConversationID == nil || *secrets[0].ConversationID != first {
		t.Fatalf("secrets %+v %v", secrets, err)
	}
	if strings.Contains(w.Body.String(), testSecretValue) || strings.Contains(sent(first), testSecretValue) {
		t.Fatalf("response %s, message %q", w.Body, sent(first))
	}

	msg := "echo: " + testOtherValue + " " + testSecretValue + " {{secret:mine=hunter2hunter2}}"
	w, kept := start(ChatRequest{Message: msg, KeepFoundSecrets: true})
	if w.Code != http.StatusCreated {
		t.Fatalf("keep_found_secrets: %d %s", w.Code, w.Body)
	}
	want := "echo: " + testOtherValue + " " + secretdetect.Marker(secrets[0].ID) + " " + secretdetect.Marker("mine")
	if got := sent(kept); got != want {
		t.Fatalf("keep_found_secrets sent %q, want %q", got, want)
	}

	before := count()
	if w, _ = start(ChatRequest{Message: "echo: {{secret=abc}}"}); w.Code != http.StatusBadRequest {
		t.Fatalf("short mark: %d %s", w.Code, w.Body)
	}
	if after := count(); after != before {
		t.Fatalf("conversations %d -> %d", before, after)
	}
}

// A message from a channel gets the chat API's treatment; a mark that can't
// be stored, an error no one would see, goes as it is.
func TestChannelMessageSecrets(t *testing.T) {
	t.Parallel()
	server, database, h := channelTestServer(t, predictable.NewService())
	server.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
	const chat = "chat-secrets"
	text := "key " + testSecretValue + " and {{secret=abc}}"
	if w := postJSON(t, h, "/api/channels/messages", channelEvent{
		Type: "message", ID: "m1", ChatID: chat, Sender: "+15550100", Text: text,
	}); w.Code != http.StatusNoContent {
		t.Fatalf("receive: %d %s", w.Code, w.Body.String())
	}
	assistantID := chatAssistant(t, server, chat)
	secrets, err := server.secrets.list(t.Context())
	if err != nil || len(secrets) != 1 || secrets[0].ConversationID == nil || *secrets[0].ConversationID != assistantID {
		t.Fatalf("secrets %+v %v", secrets, err)
	}
	want := "m1: key " + secretdetect.Marker(secrets[0].ID) + " and {{secret=abc}}"
	if got := channelUserMessages(t, database, assistantID); !slices.Equal(got, []string{want}) {
		t.Fatalf("stored channel message = %q, want %q", got, want)
	}
}

// lockedBuffer is a log destination that conversation loops write to while
// the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
