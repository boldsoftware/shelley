package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/llm"
)

// Split, so the source holds no key-shaped literal for secret scanners.
const testSecret = "sk-" + "proj-TESTsecretVALUE1234567890"

type testSecrets struct {
	on         bool
	mu         sync.Mutex
	injections []string
}

func (s *testSecrets) ToolEnabled(context.Context) bool { return s.on }

func (s *testSecrets) Value(_ context.Context, id string) (string, error) {
	if id != "openai-key-k3f9" {
		return "", fmt.Errorf("no secret %s", id)
	}
	return testSecret, nil
}

func (s *testSecrets) RecordInjection(_ context.Context, id, conversationID, machine, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.injections = append(s.injections, strings.Join([]string{id, conversationID, machine, path}, " "))
	return nil
}

func (s *testSecrets) Redact(_ context.Context, text string) (string, error) {
	return strings.ReplaceAll(text, testSecret, SecretPlaceholder("openai-key-k3f9")), nil
}

func secretTools(t *testing.T, secrets Secrets, cfg ToolSetConfig) map[string]*llm.Tool {
	cfg.Secrets = secrets
	cfg.ConversationID = "c1"
	tools := map[string]*llm.Tool{}
	for _, tool := range NewToolSet(t.Context(), cfg).Tools() {
		tools[tool.Name] = tool
	}
	return tools
}

// write_secret writes the value, mode 0600, on the VM or here, and says
// where without saying what.
func TestWriteSecret(t *testing.T) {
	t.Parallel()
	here, there := t.TempDir(), t.TempDir()
	secrets := &testSecrets{on: true}
	tools := secretTools(t, secrets, ToolSetConfig{WorkingDir: there, HostDir: here, VM: localVM{}})
	call := func(input string) llm.ToolOut {
		t.Helper()
		return tools[WriteSecretName].Run(t.Context(), json.RawMessage(input))
	}
	// An existing file loses its looser mode.
	if err := os.WriteFile(filepath.Join(here, "key"), []byte("old contents, longer than the key"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ target, path, want string }{
		{"box", "conf/key", filepath.Join(there, "conf/key")},
		{"localhost", "key", filepath.Join(here, "key")},
	} {
		out := call(`{"id":"openai-key-k3f9","path":"` + c.path + `","ssh_target":"` + c.target + `"}`)
		if out.Error != nil {
			t.Fatalf("%s: %v", c.target, out.Error)
		}
		if text := out.LLMContent[0].Text; strings.Contains(text, testSecret) || !strings.Contains(text, "{{secret:openai-key-k3f9}}") || !strings.Contains(text, c.want) {
			t.Fatalf("%s: result %q", c.target, text)
		}
		data, err := os.ReadFile(c.want)
		if err != nil || string(data) != testSecret {
			t.Fatalf("%s: file %q, %v", c.target, data, err)
		}
		if fi, err := os.Stat(c.want); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %v, %v", c.target, fi.Mode(), err)
		}
	}
	if fi, err := os.Stat(filepath.Join(there, "conf")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("new directory: %v, %v", fi.Mode(), err)
	}
	want := []string{
		"openai-key-k3f9 c1 box " + filepath.Join(there, "conf/key"),
		"openai-key-k3f9 c1 localhost " + filepath.Join(here, "key"),
	}
	if !slices.Equal(secrets.injections, want) {
		t.Fatalf("injections %q", secrets.injections)
	}
	if out := call(`{"id":"{{secret:openai-key-k3f9}}","path":"x","ssh_target":"box"}`); out.Error == nil || !strings.Contains(out.Error.Error(), "pass the ID alone") {
		t.Fatalf("placeholder as id: %v", out.Error)
	}
	if out := call(`{"id":"gone","path":"x","ssh_target":"box"}`); out.Error == nil || !strings.Contains(out.Error.Error(), "no secret gone") {
		t.Fatalf("missing secret: %v", out.Error)
	}

	// Off with the flag.
	if _, ok := secretTools(t, &testSecrets{}, ToolSetConfig{WorkingDir: there})[WriteSecretName]; ok {
		t.Fatal("write_secret offered with the flag off")
	}
}

// Every tool's output, errors, display data and progress come back with
// stored secrets as placeholders.
func TestRedactToolOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte(testSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	// Redaction applies with the flag off too.
	tools := secretTools(t, &testSecrets{}, ToolSetConfig{WorkingDir: dir})
	ctx := t.Context()
	run := func(command string) llm.ToolOut {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"command": command})
		return tools["bash"].Run(ctx, input)
	}
	check := func(what, s string) {
		t.Helper()
		if strings.Contains(s, testSecret) || !strings.Contains(s, "{{secret:openai-key-k3f9}}") {
			t.Fatalf("%s: %q", what, s)
		}
	}
	out := run("cat key")
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	check("result", out.LLMContent[0].Text)
	display, err := json.Marshal(out.Display)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(display), testSecret) {
		t.Fatalf("display: %s", display)
	}
	out = run("cat key; exit 1")
	if out.Error == nil {
		t.Fatal("want an error")
	}
	check("error", out.Error.Error())

	// Progress, through a tool that reports the secret.
	var progress []string
	ctx = llm.WithToolProgress(ctx, func(p llm.ToolProgress) { progress = append(progress, p.Output) })
	echo := redactTools([]*llm.Tool{{Name: "echo", Run: func(ctx context.Context, _ json.RawMessage) llm.ToolOut {
		llm.GetToolProgress(ctx)(llm.ToolProgress{Output: "so far: " + testSecret})
		return llm.ToolOut{LLMContent: llm.TextContent("done")}
	}}}, &testSecrets{})[0]
	echo.Run(ctx, nil)
	if len(progress) != 1 {
		t.Fatalf("progress: %q", progress)
	}
	check("progress", progress[0])
}
