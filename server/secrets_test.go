package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/predictable"
)

const testSecretValue = "ghp_TESTsecretVALUE0123456789abcdefABCD"

// A secret's value goes in through POST /api/secrets only, and never
// reaches messages, model requests, logs, or the database unsealed: tool
// output that holds it, and chat messages that repeat it, get the
// placeholder.
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
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}

	dir := t.TempDir()
	conv, err := database.CreateConversation(t.Context(), nil, true, &dir, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	secret, _ := json.Marshal(newSecret{ID: "github-token-k3f9", Name: "GitHub", Kind: "github-token", Value: testSecretValue, ConversationID: id})
	if err := database.SetFeatureFlagOverride(t.Context(), FlagSecrets.Name, "false"); err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/secrets", string(secret), 403)
	if err := database.DeleteFeatureFlagOverride(t.Context(), FlagSecrets.Name); err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/secrets", string(secret), 201) // on by default
	call("POST", "/api/secrets", string(secret), 409)
	call("POST", "/api/secrets", `{"id":"short-k3f9","name":"x","kind":"token","value":"12345"}`, 400)
	call("POST", "/api/secrets", `{"id":"Bad ID","name":"x","kind":"token","value":"123456"}`, 400)
	if fi, err := os.Stat(s.secrets.keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}

	// The agent writes it to a file, reads the file, and the user pastes
	// it again.
	for _, message := range []string{
		`write_secret: {"id":"github-token-k3f9","path":"token"}`,
		"bash: cat token",
		"echo: the token is " + testSecretValue,
	} {
		postChatMessage(t, s, id, message)
		waitForIdle(t, s, id)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "token")); err != nil || string(data) != testSecretValue {
		t.Fatalf("injected file %q %v", data, err)
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
	if n := strings.Count(stored.String(), "{{secret:github-token-k3f9}}"); n < 3 {
		t.Errorf("placeholder in %d messages, want the tool result, the bash output and the user's message:\n%s", n, stored.String())
	}

	// Management: listed with its injection, renamed, revealed, deleted.
	var list []Secret
	json.Unmarshal(call("GET", "/api/secrets", "", 200), &list)
	if len(list) != 1 || list[0].Name != "GitHub" || *list[0].ConversationID != id || len(list[0].Injections) != 1 ||
		list[0].Injections[0].Path != filepath.Join(dir, "token") || list[0].Injections[0].Machine != "localhost" {
		t.Fatalf("list %+v", list)
	}
	call("PATCH", "/api/secrets/github-token-k3f9", `{"name":"GitHub CI"}`, 204)
	if body := call("POST", "/api/secrets/github-token-k3f9/reveal", "", 200); !strings.Contains(string(body), testSecretValue) {
		t.Fatalf("reveal %s", body)
	}
	call("DELETE", "/api/secrets/github-token-k3f9", "", 204)
	call("POST", "/api/secrets/github-token-k3f9/reveal", "", 404)
	if got, err := s.secrets.Redact(t.Context(), testSecretValue); err != nil || got != testSecretValue {
		t.Fatalf("redaction after delete: %q %v", got, err)
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
