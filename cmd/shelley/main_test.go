package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
	"shelley.exe.dev/slug"
)

type tieredModelProvider struct {
	ids   []string
	infos map[string]*models.ModelInfo
}

func (p *tieredModelProvider) GetService(string) (llm.Service, error) { return nil, nil }
func (p *tieredModelProvider) GetAvailableModels() []string           { return p.ids }
func (p *tieredModelProvider) HasModel(string) bool                   { return true }
func (p *tieredModelProvider) GetModelInfo(id string) *models.ModelInfo {
	return p.infos[id]
}
func (p *tieredModelProvider) RefreshCustomModels() error { return nil }

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple Test", "simple-test"},
		{"Create a Python Script", "create-a-python-script"},
		{"Multiple   Spaces", "multiple-spaces"},
		{"Special@#$%Characters", "specialcharacters"},
		{"Under_Score_Test", "under-score-test"},
		{"--multiple-hyphens--", "multiple-hyphens"},
		{"CamelCase Example", "camelcase-example"},
		{"123 Numbers Test 456", "123-numbers-test-456"},
		{"   leading and trailing   ", "leading-and-trailing"},
		{"", ""},
	}

	for _, test := range tests {
		result := slug.Sanitize(test.input)
		if result != test.expected {
			t.Errorf("slug.Sanitize(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestBuildLLMConfigSkipsGatewayWhenReflectionFoundLLMIntegration(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		return modelsources.LLMIntegrationDiscoveryResult{Found: true}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	if err := os.WriteFile(configPath, []byte(`{"llm_gateway":"https://gateway.example.com"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range cfg.Models {
		if model.Source == "exe.dev gateway" {
			t.Fatalf("gateway model %q was built despite discovered LLM integration", model.ID)
		}
	}
	if findBuiltModelSource(cfg.Models, "predictable") != "builtin" {
		t.Fatalf("predictable model missing from config")
	}
}

func TestBuildLLMConfigAppliesExeEnvironmentBeforeDiscovery(t *testing.T) {
	oldEnv, err := exeenv.Current()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exeenv.Configure(oldEnv) })

	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		env, err := exeenv.Current()
		if err != nil {
			t.Fatal(err)
		}
		if got := env.ReflectionURL(); got != "https://reflection.int.example.test" {
			t.Fatalf("discovery environment = %q", got)
		}
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	config := `{
		"llm_gateway": "https://gateway.example.com/",
		"default_model": "configured-default",
		"exe_environment": {"scheme": "https", "box_host": "example.test"}
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "configured-default" {
		t.Fatalf("DefaultModel = %q", cfg.DefaultModel)
	}
	gatewayFound := false
	for _, model := range cfg.Models {
		gatewayFound = gatewayFound || model.Source == "exe.dev gateway"
	}
	if !gatewayFound {
		t.Fatal("parsed llm_gateway was not reused")
	}
}

// TestGlobalFlagsDefaultModelEmptyByDefault guards the production fix: the
// -default-model flag must default to empty so shelley.json's default_model
// takes effect on VMs (where the systemd unit passes no -default-model). A
// non-empty flag default would shadow the config field via the
// defaultModel=="" guard in buildLLMModelSources. This parses through a real
// FlagSet rather than constructing GlobalConfig directly, so reverting the
// flag default fails here.
func TestGlobalFlagsDefaultModelEmptyByDefault(t *testing.T) {
	var global GlobalConfig
	fs := flag.NewFlagSet("shelley", flag.ContinueOnError)
	registerGlobalFlags(fs, &global)
	if err := fs.Parse([]string{"serve"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if global.DefaultModel != "" {
		t.Fatalf("DefaultModel default = %q, want empty", global.DefaultModel)
	}

	// An explicit flag is still captured.
	global = GlobalConfig{}
	fs = flag.NewFlagSet("shelley", flag.ContinueOnError)
	registerGlobalFlags(fs, &global)
	if err := fs.Parse([]string{"-default-model", "claude-opus-5", "serve"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if global.DefaultModel != "claude-opus-5" {
		t.Fatalf("DefaultModel = %q, want claude-opus-5", global.DefaultModel)
	}
}

// TestBuildLLMConfigDefaultModelPrecedence pins the default-model precedence
// that the shelley.json default-model mechanism relies on: an explicit
// -default-model flag wins, otherwise shelley.json's default_model is honored.
// The flag value is obtained via real flag parsing (see
// TestGlobalFlagsDefaultModelEmptyByDefault) so on VMs, where the flag is
// unset, the shelley.json value is what takes effect.
func TestBuildLLMConfigDefaultModelPrecedence(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	config := `{"default_model": "gpt-5.6-sol"}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// parseGlobal registers and parses the global flags exactly as main does,
	// then points -config at our temp file. This ties the flag default to the
	// precedence assertion so neither can regress silently.
	parseGlobal := func(t *testing.T, args ...string) GlobalConfig {
		t.Helper()
		var global GlobalConfig
		fs := flag.NewFlagSet("shelley", flag.ContinueOnError)
		registerGlobalFlags(fs, &global)
		if err := fs.Parse(append([]string{"-config", configPath}, args...)); err != nil {
			t.Fatalf("parse: %v", err)
		}
		return global
	}

	// Flag unset (the VM case): shelley.json's default_model wins.
	cfg, err := buildLLMConfig(parseGlobal(t, "serve"), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "gpt-5.6-sol" {
		t.Fatalf("flag unset: DefaultModel = %q, want gpt-5.6-sol", cfg.DefaultModel)
	}

	// Flag explicitly set: it overrides shelley.json.
	cfg, err = buildLLMConfig(parseGlobal(t, "-default-model", "claude-opus-5", "serve"), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "claude-opus-5" {
		t.Fatalf("flag set: DefaultModel = %q, want claude-opus-5", cfg.DefaultModel)
	}
}

func TestModelsCommandDefaultIDMatchesServerVisibility(t *testing.T) {
	modelList := []models.Built{
		{ID: "first-ready"},
		{ID: "configured"},
		{ID: "predictable"},
	}
	tests := []struct {
		name            string
		configured      string
		predictableOnly bool
		want            string
	}{
		{name: "configured model", configured: "configured", want: "configured"},
		{name: "missing configured model", configured: "missing", want: "first-ready"},
		{name: "predictable hidden normally", configured: "predictable", want: "first-ready"},
		{name: "predictable only", configured: "configured", predictableOnly: true, want: "predictable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelsCommandDefaultID(tt.configured, modelList, tt.predictableOnly); got != tt.want {
				t.Fatalf("modelsCommandDefaultID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildLLMConfigRejectsInvalidExeEnvironmentBeforeDiscovery(t *testing.T) {
	oldDiscover := discoverLLMIntegrations
	discoveryCalls := 0
	discoverLLMIntegrations = func(context.Context, *http.Client, *slog.Logger) modelsources.LLMIntegrationDiscoveryResult {
		discoveryCalls++
		return modelsources.LLMIntegrationDiscoveryResult{}
	}
	t.Cleanup(func() { discoverLLMIntegrations = oldDiscover })

	configPath := filepath.Join(t.TempDir(), "shelley.json")
	if err := os.WriteFile(configPath, []byte(`{"exe_environment":{"scheme":"ftp","box_host":"example.test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := buildLLMConfig(GlobalConfig{ConfigPath: configPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err == nil || !strings.Contains(err.Error(), "exe_environment") {
		t.Fatalf("buildLLMConfig() error = %v", err)
	}
	if discoveryCalls != 0 {
		t.Fatalf("discovery called %d times before config validation", discoveryCalls)
	}
}

func TestToolModelsHideUnknownIntegrationModelsButKeepCustomModels(t *testing.T) {
	provider := &tieredModelProvider{
		ids: []string{"gpt-5.6-sol", "upstream-only", "my-custom-model"},
		infos: map[string]*models.ModelInfo{
			"gpt-5.6-sol":     {Source: "llm.int.exe.xyz"},
			"upstream-only":   {Source: "llm.int.exe.xyz"},
			"my-custom-model": {Source: models.SourceCustomLabel},
		},
	}

	got := setupToolSetConfig(nil, provider, nil).BuildAvailableModels()
	if len(got) != 2 || got[0].ID != "gpt-5.6-sol" || got[1].ID != "my-custom-model" {
		t.Fatalf("available tool models = %+v, want known and custom models", got)
	}
}

func findBuiltModelSource(built []models.Built, id string) string {
	for _, model := range built {
		if model.ID == id {
			return model.Source
		}
	}
	return ""
}

func TestCLICommands(t *testing.T) {
	// Build the binary once for this test and its subtests
	tempDir := t.TempDir()
	binary := filepath.Join(tempDir, "shelley")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	t.Run("help message", func(t *testing.T) {
		cmd := exec.Command(binary)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected command to fail with no arguments")
		}
		outputStr := string(output)
		if !strings.Contains(outputStr, "Commands:") {
			t.Errorf("Expected help message, got: %s", outputStr)
		}
	})

	t.Run("serve flag parsing", func(t *testing.T) {
		// Test that serve command accepts flags - we can't easily test the full server
		// but we can test that it doesn't immediately error on flag parsing
		cmd := exec.Command(binary, "serve", "-h")
		output, err := cmd.CombinedOutput()
		// With flag package, -h should cause exit with code 2
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				if exitError.ExitCode() == 2 {
					// This is expected for -h flag
					outputStr := string(output)
					if !strings.Contains(outputStr, "-port") || !strings.Contains(outputStr, "-db") {
						t.Errorf("Expected serve help to show -port and -db flags, got: %s", outputStr)
					}
					if !strings.Contains(outputStr, "-systemd-activation") {
						t.Errorf("Expected serve help to show -systemd-activation flag, got: %s", outputStr)
					}
					return
				}
			}
		}
		// If no error or different error, that's also fine for this basic test
		t.Logf("Serve command output: %s", string(output))
	})
}

func TestSystemdListenerErrors(t *testing.T) {
	// Save original environment
	origPID := os.Getenv("LISTEN_PID")
	origFDs := os.Getenv("LISTEN_FDS")
	defer func() {
		os.Setenv("LISTEN_PID", origPID)
		os.Setenv("LISTEN_FDS", origFDs)
	}()

	t.Run("no LISTEN_FDS", func(t *testing.T) {
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS not set")
		}
		if !strings.Contains(err.Error(), "LISTEN_FDS not set") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("wrong LISTEN_PID", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "1")
		os.Setenv("LISTEN_PID", "99999999") // Unlikely to match our PID
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_PID doesn't match")
		}
		if !strings.Contains(err.Error(), "does not match current PID") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("invalid LISTEN_FDS", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "notanumber")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS is invalid")
		}
		if !strings.Contains(err.Error(), "invalid LISTEN_FDS") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("zero LISTEN_FDS", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "0")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS is 0")
		}
		if !strings.Contains(err.Error(), "expected at least 1") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})
}

func TestSystemdListenerIntegration(t *testing.T) {
	// This test simulates what systemd does: create a listener, get the fd,
	// and pass it to a child process via environment and fd inheritance.
	// Since we can't easily test in-process (fd 3 is likely already in use),
	// we test by spawning a subprocess.

	tempDir := t.TempDir()
	binary := filepath.Join(tempDir, "shelley")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create a listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Get the file descriptor from the listener
	tcpListener := listener.(*net.TCPListener)
	file, err := tcpListener.File()
	if err != nil {
		listener.Close()
		t.Fatalf("Failed to get file from listener: %v", err)
	}
	listener.Close() // Close original listener, file still holds the socket

	// Create a temp database for the test
	dbPath := filepath.Join(tempDir, "test.db")

	// Spawn shelley with the file descriptor as fd 3
	// Note: We don't set LISTEN_PID here because we don't know the child PID yet.
	// The systemdListener function handles missing LISTEN_PID gracefully.
	cmd = exec.Command(binary, "-db", dbPath, "serve", "-systemd-activation")
	// Build environment without LISTEN_PID (will be inherited from parent otherwise)
	// and add LISTEN_FDS=1
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "LISTEN_PID=") {
			env = append(env, e)
		}
	}
	env = append(env, "LISTEN_FDS=1")
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{file} // This makes the file fd 3 in the child
	var stderrBuf, stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Start the process
	if err := cmd.Start(); err != nil {
		file.Close()
		t.Fatalf("Failed to start shelley: %v", err)
	}
	file.Close() // Close our copy after child inherits it

	// Wait a bit for the server to start
	time.Sleep(500 * time.Millisecond)

	// Try to connect to the server
	var resp *http.Response
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 10; i++ {
		resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/version", port))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Kill the server
	cmd.Process.Kill()
	cmd.Wait()

	if err != nil {
		t.Fatalf("Failed to connect to server: %v\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Unexpected status code %d, body: %s", resp.StatusCode, body)
	}
}
