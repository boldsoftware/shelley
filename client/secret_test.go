package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `shelley secret ID` writes a secret the server took out of a message to
// a new 0600 file and prints its path; the server records the read.
func TestSecretCommand(t *testing.T) {
	e := newMCPEnv(t)
	hc, base, err := (&clientConfig{serverURL: "unix://" + e.sock}).newHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body, out any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shelley-Request", "1")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %s", method, path, resp.Status)
		}
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
	}
	value := "ghp_" + "TESTsecretVALUE0123456789abcdefABCDE" // split for secret scanners
	do("POST", "/api/conversations/new", map[string]string{"message": "the token is " + value, "model": "predictable", "cwd": t.TempDir()}, nil)
	var secrets []struct {
		ID string `json:"id"`
	}
	do("GET", "/api/secrets", nil, &secrets)
	if len(secrets) != 1 {
		t.Fatalf("secrets %+v", secrets)
	}

	t.Setenv("SHELLEY_SOCKET", e.sock)
	var stdout, stderr bytes.Buffer
	if code := runSecret([]string{secrets[0].ID}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	path := strings.TrimSuffix(stdout.String(), "\n")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != value {
		t.Fatalf("file %q: %q %v", path, data, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", fi, err)
	}

	// An unknown secret: an error, and no file left behind.
	stdout.Reset()
	stderr.Reset()
	if code := runSecret([]string{"github-pat-zzzz"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "expired or was deleted") {
		t.Fatalf("exit %d: %q", code, stderr.String())
	}
	if files, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "shelley-secret-*")); len(files) != 1 {
		t.Fatalf("temp files: %v", files)
	}
	if code := runSecret(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no ID: exit %d", code)
	}
}
