package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

// Split, so the source holds no key-shaped literal for secret scanners.
const testSecret = "sk-" + "proj-TESTsecretVALUE1234567890"

const testMarker = "[secret openai-key-k3f9]"

type testSecrets struct{}

func (testSecrets) Redact(_ context.Context, text string) (string, error) {
	return strings.ReplaceAll(text, testSecret, testMarker), nil
}

// Every tool's output, errors, display data and progress come back with
// stored secrets as markers.
func TestRedactToolOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte(testSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := ToolSetConfig{WorkingDir: dir, Secrets: testSecrets{}}
	tools := map[string]*llm.Tool{}
	for _, tool := range NewToolSet(t.Context(), cfg).Tools() {
		tools[tool.Name] = tool
	}
	ctx := t.Context()
	run := func(command string) llm.ToolOut {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"command": command})
		return tools["bash"].Run(ctx, input)
	}
	check := func(what, s string) {
		t.Helper()
		if strings.Contains(s, testSecret) || !strings.Contains(s, testMarker) {
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
	}}}, testSecrets{})[0]
	echo.Run(ctx, nil)
	if len(progress) != 1 {
		t.Fatalf("progress: %q", progress)
	}
	check("progress", progress[0])
}
