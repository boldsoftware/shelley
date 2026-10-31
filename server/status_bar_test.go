package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func writeStatusBarHook(t *testing.T, dir, script string) {
	t.Helper()
	writeShellScript(t, filepath.Join(dir, hookStatusBar), script)
}

func TestStatusBarHookOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, script string
		want         StatusBar
		wantErr      string
	}{
		{"status", `echo '{"body":" main ","bgcolor":"#204a87","color":"white"}'`, StatusBar{Items: []StatusBarItem{{Body: "main"}}, BGColor: "#204a87", Color: "white"}, ""},
		{"single item with native action", `echo '{"body":[{"body":"models","title":"Open models","action":"open_models"}]}'`, StatusBar{Items: []StatusBarItem{{Body: "models", Title: "Open models", Action: "open_models"}}}, ""},
		{"default diff with omitted args", `echo '{"body":[{"body":"diff","action":"open_diff"}]}'`, StatusBar{Items: []StatusBarItem{{Body: "diff", Action: "open_diff"}}}, ""},
		{"default diff with empty args", `echo '{"body":[{"body":"diff","action":"open_diff","args":{}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "diff", Action: "open_diff"}}}, ""},
		{"abbreviated commit", `echo '{"body":[{"body":"commit","action":"open_diff","args":{"commit":"4cd8f2e"}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "commit", Action: "open_diff", Args: &StatusBarActionArgs{Commit: "4cd8f2e"}}}}, ""},
		{"uppercase commit", `echo '{"body":[{"body":"commit","action":"open_diff","args":{"commit":"4CD8F2E"}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "commit", Action: "open_diff", Args: &StatusBarActionArgs{Commit: "4cd8f2e"}}}}, ""},
		{"full commit", `echo '{"body":[{"body":"commit","action":"open_diff","args":{"commit":"4cd8f2ec56cd45fbe60e792100d7afdb24c4b3ef"}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "commit", Action: "open_diff", Args: &StatusBarActionArgs{Commit: "4cd8f2ec56cd45fbe60e792100d7afdb24c4b3ef"}}}}, ""},
		{"URL action", `echo '{"body":[{"body":"docs","action":"open_url","args":{"url":"https://example.com/docs"}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "docs", Action: "open_url", Args: &StatusBarActionArgs{URL: "https://example.com/docs"}}}}, ""},
		{"HTTP URL action", `echo '{"body":[{"body":"local","action":"open_url","args":{"url":"http://localhost:3000/docs"}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "local", Action: "open_url", Args: &StatusBarActionArgs{URL: "http://localhost:3000/docs"}}}}, ""},
		{"multiple items with root and item styles", `echo '{"bgcolor":"var(--bg-secondary)","color":"var(--text-secondary)","body":[{"body":"main"},{"body":"12 changed files","action":"open_diff","args":{"commit":"abcd123"},"bgcolor":"red","color":"white"},{"body":"Sonnet","action":"open_models"},{"body":"Proposal","action":"open_url","args":{"url":"https://example.com/proposal"}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "main"}, {Body: "12 changed files", BGColor: "red", Color: "white", Action: "open_diff", Args: &StatusBarActionArgs{Commit: "abcd123"}}, {Body: "Sonnet", Action: "open_models"}, {Body: "Proposal", Action: "open_url", Args: &StatusBarActionArgs{URL: "https://example.com/proposal"}}}, BGColor: "var(--bg-secondary)", Color: "var(--text-secondary)"}, ""},
		{"null legacy colors", `echo '{"body":"main","bgcolor":null,"color":null}'`, StatusBar{Items: []StatusBarItem{{Body: "main"}}}, ""},
		{"null legacy background", `echo '{"body":"main","bgcolor":null,"color":"white"}'`, StatusBar{Items: []StatusBarItem{{Body: "main"}}, Color: "white"}, ""},
		{"null colors with array body", `echo '{"body":[{"body":"main"}],"bgcolor":null,"color":null}'`, StatusBar{Items: []StatusBarItem{{Body: "main"}}}, ""},
		{"empty stdout", `true`, StatusBar{}, ""},
		{"empty object", `echo '{}'`, StatusBar{}, ""},
		{"omitted body", `echo '{"bgcolor":"red"}'`, StatusBar{}, ""},
		{"null legacy body", `echo '{"body":null,"color":"white"}'`, StatusBar{}, ""},
		{"empty body", `echo '{"body":""}'`, StatusBar{}, ""},
		{"whitespace body", `printf '%s\n' '{"body":"  \t "}'`, StatusBar{}, ""},
		{"empty body array", `echo '{"body":[]}'`, StatusBar{}, ""},
		{"null item fields", `echo '{"body":[{"body":"x","color":null,"action":null,"args":null}]}'`, StatusBar{Items: []StatusBarItem{{Body: "x"}}}, ""},
		{"other action accepts empty args", `echo '{"body":[{"body":"x","action":"open_models","args":{}}]}'`, StatusBar{Items: []StatusBarItem{{Body: "x", Action: "open_models"}}}, ""},
		{"bare root array", `echo '[{"body":"main"}]'`, StatusBar{}, "invalid JSON output: json: cannot unmarshal array"},
		{"non-object root", `echo 'null'`, StatusBar{}, ""},
		{"failure", `echo '{"body":"x"}'; echo 'not a repo' >&2; exit 3`, StatusBar{}, "exit status 3: not a repo"},
		{"not JSON", `echo main`, StatusBar{}, "invalid JSON output: invalid character 'm' looking for beginning of value"},
		{"unknown root field", `echo '{"text":"main"}'`, StatusBar{}, `unknown field "text"`},
		{"root rejects item fields", `echo '{"body":"x","action":"open_files"}'`, StatusBar{}, `unknown field "action"`},
		{"root rejects href", `echo '{"body":"x","href":"https://example.com"}'`, StatusBar{}, `unknown field "href"`},
		{"item rejects href", `echo '{"body":[{"body":"x","href":"https://example.com"}]}'`, StatusBar{}, `unknown field "href"`},
		{"item rejects flat commit", `echo '{"body":[{"body":"x","action":"open_diff","commit":"HEAD"}]}'`, StatusBar{}, `unknown field "commit"`},
		{"root color type", `echo '{"body":"x","color":12}'`, StatusBar{}, "cannot unmarshal number"},
		{"non-object body item", `echo '{"body":[{"body":"x"},null]}'`, StatusBar{Items: []StatusBarItem{{Body: "x"}}}, ""},
		{"nested body array", `echo '{"body":[[{"body":"x"}]]}'`, StatusBar{}, "cannot unmarshal array"},
		{"number body", `echo '{"body":12}'`, StatusBar{}, "invalid JSON output: body must be a string or an array of items"},
		{"object body", `echo '{"body":{"body":"nested"}}'`, StatusBar{}, "invalid JSON output: body must be a string or an array of items"},
		{"unknown action", `echo '{"body":[{"body":"x","action":"open_unknown"}]}'`, StatusBar{}, `invalid JSON output: body item 1: unknown action "open_unknown"`},
		{"non-string action", `echo '{"body":[{"body":"x","action":1}]}'`, StatusBar{}, "cannot unmarshal number"},
		{"open_url requires url", `echo '{"body":[{"body":"docs","action":"open_url"}]}'`, StatusBar{}, "invalid JSON output: body item 1: args.url must be an absolute HTTP(S) URL with a hostname"},
		{"args without action", `echo '{"body":[{"body":"x","args":{}}]}'`, StatusBar{}, "invalid JSON output: body item 1: args need an action"},
		{"array args", `echo '{"body":[{"body":"x","action":"open_diff","args":[]}]}'`, StatusBar{}, "cannot unmarshal array"},
		{"unknown diff arg", `echo '{"body":[{"body":"x","action":"open_diff","args":{"commmit":"HEAD"}}]}'`, StatusBar{}, `unknown field "commmit"`},
		{"empty URL", `echo '{"body":[{"body":"x","action":"open_url","args":{"url":""}}]}'`, StatusBar{}, "args.url must be an absolute HTTP(S) URL"},
		{"non-http URL", `echo '{"body":[{"body":"x","action":"open_url","args":{"url":"javascript:alert(1)"}}]}'`, StatusBar{}, "args.url must be an absolute HTTP(S) URL"},
		{"relative URL", `echo '{"body":[{"body":"x","action":"open_url","args":{"url":"/docs"}}]}'`, StatusBar{}, "args.url must be an absolute HTTP(S) URL"},
		{"URL without hostname", `echo '{"body":[{"body":"x","action":"open_url","args":{"url":"https:///docs"}}]}'`, StatusBar{}, "args.url must be an absolute HTTP(S) URL"},
		{"URL action rejects commit", `echo '{"body":[{"body":"x","action":"open_url","args":{"url":"https://example.com","commit":"abcd"}}]}'`, StatusBar{}, "invalid JSON output: body item 1: args.commit is not supported by open_url"},
		{"other action rejects url", `echo '{"body":[{"body":"x","action":"open_models","args":{"url":"https://example.com"}}]}'`, StatusBar{}, "invalid JSON output: body item 1: args.url is not supported by open_models"},
		{"two values", `echo '{}{}'`, StatusBar{}, "invalid JSON output: trailing data after the value"},
		{"trailing bracket", `echo '{"body":"x"}]'`, StatusBar{}, "invalid JSON output: trailing data after the value"},
		{"too long", `head -c 5000 /dev/zero | tr '\0' x`, StatusBar{}, "output exceeds 4096 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeStatusBarHook(t, dir, tc.script)
			got, err := runStatusBarHookIn(t.Context(), dir, StatusBarHookInput{Cwd: t.TempDir()})
			if !reflect.DeepEqual(got, tc.want) || (tc.wantErr == "" && err != nil) || !strings.Contains(fmt.Sprint(err), tc.wantErr) {
				t.Fatalf("got %+v, %v; want %+v, %s", got, err, tc.want, tc.wantErr)
			}
		})
	}

	for _, commit := range []string{"  ", "abc", "HEAD", "main", "HEAD~1", "HEAD~2..HEAD", strings.Repeat("a", 65)} {
		t.Run("invalid diff commit "+commit, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeStatusBarHook(t, dir, fmt.Sprintf(`echo '{"body":[{"body":"diff","action":"open_diff","args":{"commit":%q}}]}'`, commit))
			_, err := runStatusBarHookIn(t.Context(), dir, StatusBarHookInput{Cwd: dir})
			want := "invalid JSON output: body item 1: args.commit must be a full or abbreviated commit hash (4-64 hexadecimal characters)"
			if fmt.Sprint(err) != want {
				t.Fatalf("got %v, want %s", err, want)
			}
		})
	}

	dir := t.TempDir()
	if got, err := runStatusBarHookIn(t.Context(), dir, StatusBarHookInput{Cwd: t.TempDir()}); !reflect.DeepEqual(got, StatusBar{}) || err != nil {
		t.Fatalf("missing hook: %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, hookStatusBar), []byte("#!/bin/sh\necho '{\"body\":\"hi\"}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := runStatusBarHookIn(t.Context(), dir, StatusBarHookInput{Cwd: t.TempDir()}); !reflect.DeepEqual(got, StatusBar{}) || err != nil {
		t.Fatalf("non-executable hook: %+v, %v", got, err)
	}
}

// The hook runs in the conversation's directory with its context on stdin.
func TestStatusBarHookInput(t *testing.T) {
	t.Parallel()
	hooks, cwd := t.TempDir(), t.TempDir()
	writeStatusBarHook(t, hooks, `cat > input.json; echo "$GIT_OPTIONAL_LOCKS" > locks; echo '{"body":"ok"}'`)
	input := StatusBarHookInput{ConversationID: "cABC", Slug: "fix-it", Cwd: cwd, Model: "predictable"}
	if got, err := runStatusBarHookIn(t.Context(), hooks, input); !reflect.DeepEqual(got, StatusBar{Items: []StatusBarItem{{Body: "ok"}}}) || err != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, missing := range []string{"", filepath.Join(cwd, "gone"), filepath.Join(hooks, hookStatusBar)} {
		if got, err := runStatusBarHookIn(t.Context(), hooks, StatusBarHookInput{Cwd: missing}); !reflect.DeepEqual(got, StatusBar{}) || err != nil {
			t.Fatalf("cwd %q: got %+v, %v", missing, got, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(cwd, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got StatusBarHookInput
	if err := json.Unmarshal(data, &got); err != nil || got != input {
		t.Fatalf("stdin %s: %+v, %v", data, got, err)
	}
	// So a "git status" here can't make the agent's "git add" fail.
	if locks, err := os.ReadFile(filepath.Join(cwd, "locks")); string(locks) != "0\n" {
		t.Fatalf("GIT_OPTIONAL_LOCKS = %q, %v", locks, err)
	}
}

func TestStatusBarEndpoint(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	cwd := t.TempDir()
	slug, model := "status-demo", "predictable"
	conv, err := database.CreateConversation(t.Context(), &slug, true, &cwd, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	get := func(id string, wantCode int, want StatusBar) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/conversation/"+id+"/status-bar", nil))
		if rec.Code != wantCode {
			t.Fatalf("status %d, want %d: %s", rec.Code, wantCode, rec.Body)
		}
		if wantCode != http.StatusOK {
			return
		}
		var got StatusBar
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v (%v), want %+v", got, err, want)
		}
	}

	get(conv.ConversationID, http.StatusOK, StatusBar{})
	writeStatusBarHook(t, s.hooksDir, `printf '{"body":"%s in %s","bgcolor":"red"}' "$(sed 's/.*"slug":"\([^"]*\)".*/\1/')" "$(basename "$PWD")"`)
	get(conv.ConversationID, http.StatusOK, StatusBar{Items: []StatusBarItem{{Body: slug + " in " + filepath.Base(cwd)}}, BGColor: "red"})
	writeStatusBarHook(t, s.hooksDir, `exit 1`)
	get(conv.ConversationID, http.StatusOK, StatusBar{Error: "status-bar hook failed: exit status 1"})
	get("cNOPE", http.StatusNotFound, StatusBar{})
}
