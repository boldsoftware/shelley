package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models"
)

func TestModelsCommandDBFlagDetection(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"models"}, false},
		{[]string{"-db", "shelley.db", "models"}, true},
		{[]string{"-db=/tmp/x.db", "models"}, true},
	} {
		var global GlobalConfig
		fs := flag.NewFlagSet("shelley", flag.ContinueOnError)
		registerGlobalFlags(fs, &global)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if got := flagWasSet(fs, "db"); got != tc.want {
			t.Errorf("flagWasSet(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestModelsCommandListsCustomModels(t *testing.T) {
	database, err := db.New(db.Config{DSN: filepath.Join(t.TempDir(), "shelley.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	const apiKey = "sentinel-custom-model-key"
	if _, err := database.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID: "claude-opus-5-5-api-anthropic-com", DisplayName: "claude-opus-5-5", ProviderType: "anthropic",
		Endpoint: "https://api.anthropic.com/v1/messages", ApiKey: apiKey, ModelName: "claude-opus-5-5",
	}); err != nil {
		t.Fatal(err)
	}
	custom, err := database.GetModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	built := []models.Built{{ID: "builtin-model", Provider: models.ProviderAnthropic, APIType: models.APITypeAnthropicMessages, BaseURL: "https://gateway.example", Source: "exe.dev gateway"}}

	var without bytes.Buffer
	printModels(&without, built, "builtin-model", nil)
	if strings.Contains(without.String(), "claude-opus-5-5-api-anthropic-com") || !strings.HasSuffix(without.String(), "\n1 models\n") {
		t.Fatalf("without custom models:\n%s", without.String())
	}

	var with bytes.Buffer
	printModels(&with, built, "builtin-model", custom)
	out := with.String()
	t.Logf("output:\n%s", out)
	if strings.Index(out, "builtin-model") > strings.Index(out, "claude-opus-5-5-api-anthropic-com") || !strings.HasSuffix(out, "\n2 models\n") {
		t.Fatalf("custom models not appended after built-in models:\n%s", out)
	}
	var row string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "claude-opus-5-5-api-anthropic-com ") {
			row = line
		}
	}
	if got := strings.Fields(row); len(got) != 4 || got[1] != "anthropic" || got[2] != "https://api.anthropic.com/v1/messages" || got[3] != models.SourceCustomLabel {
		t.Fatalf("custom row = %q", row)
	}
	if strings.Contains(out, apiKey) {
		t.Fatal("output leaks the API key")
	}
}

func TestModelsCommandBinary(t *testing.T) {
	t.Parallel()
	binary := shelleyBinary(t)
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"-disable-llm-integration"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "ANTHROPIC_API_KEY=", "OPENAI_API_KEY=", "GEMINI_API_KEY=", "FIREWORKS_API_KEY=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("shelley %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	customRow := func(out string) []string {
		for line := range strings.SplitSeq(out, "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && fields[len(fields)-1] == models.SourceCustomLabel {
				return fields
			}
		}
		return nil
	}

	t.Run("with -db", func(t *testing.T) {
		const apiKey = "sentinel-models-command-key"
		dbPath := filepath.Join(t.TempDir(), "custom.db")
		database, err := db.New(db.Config{DSN: dbPath})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := database.CreateModel(t.Context(), generated.CreateModelParams{
			ModelID: "my-custom-model", DisplayName: "My Custom Model", ProviderType: "anthropic",
			Endpoint: "https://api.anthropic.com/v1/messages", ApiKey: apiKey, ModelName: "claude-opus-5-5",
		}); err != nil {
			t.Fatal(err)
		}
		database.Close()

		out := run(t, t.TempDir(), "-db", dbPath, "models")
		if got := customRow(out); !slices.Equal(got, []string{"my-custom-model", "anthropic", "https://api.anthropic.com/v1/messages", "custom"}) {
			t.Fatalf("custom row = %q in output:\n%s", got, out)
		}
		if strings.Contains(out, apiKey) {
			t.Fatalf("output leaks the API key:\n%s", out)
		}
	})

	t.Run("without -db", func(t *testing.T) {
		dir := t.TempDir()
		out := run(t, dir, "models")
		if got := customRow(out); got != nil {
			t.Fatalf("unexpected custom row %q in output:\n%s", got, out)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("models without -db created files: %v", entries)
		}
	})
}
