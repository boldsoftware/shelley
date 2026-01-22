package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupStartupHookTest creates a test hook and configures the environment.
// Must be called before NewTestHarness.
func setupStartupHookTest(t *testing.T, hookScript string) {
	t.Helper()

	hookDir := t.TempDir()
	configDir := filepath.Join(hookDir, ".config", "shelley")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "on-conversation-start"), []byte(hookScript), 0755); err != nil {
		t.Fatalf("failed to create hook: %v", err)
	}
	t.Setenv("HOME", hookDir)
	// Don't disable startup hook for this test
	t.Setenv("SHELLEY_DISABLE_STARTUP_HOOK", "")
}

func TestStartupHookPrependedToFirstMessage(t *testing.T) {
	setupStartupHookTest(t, "#!/bin/bash\necho 'hook output'\n")

	h := NewTestHarnessWithoutDisablingStartupHook(t)
	defer h.Close()

	h.NewConversation("hello", "/tmp")
	h.WaitResponse() // Wait for LLM to process

	// Check that the LLM received the prepended message
	req := h.llm.GetLastRequest()
	if req == nil {
		t.Fatal("no request sent to LLM")
	}

	// Find the user message
	var userText string
	for _, msg := range req.Messages {
		if msg.Role == 0 { // MessageRoleUser
			for _, content := range msg.Content {
				if content.Type == 2 { // ContentTypeText
					userText = content.Text
					break
				}
			}
		}
	}

	if !strings.Contains(userText, "[Startup hook output]") {
		t.Errorf("expected user message to contain startup hook header, got: %s", userText)
	}
	if !strings.Contains(userText, "hook output") {
		t.Errorf("expected user message to contain hook output, got: %s", userText)
	}
	if !strings.Contains(userText, "[User message]") {
		t.Errorf("expected user message to contain user message header, got: %s", userText)
	}
	if !strings.Contains(userText, "hello") {
		t.Errorf("expected user message to contain original message, got: %s", userText)
	}
}

func TestStartupHookErrorPrependedToFirstMessage(t *testing.T) {
	setupStartupHookTest(t, "#!/bin/bash\nexit 1\n")

	h := NewTestHarnessWithoutDisablingStartupHook(t)
	defer h.Close()

	h.NewConversation("hello", "/tmp")
	h.WaitResponse() // Wait for LLM to process

	// Check that the LLM received the error message
	req := h.llm.GetLastRequest()
	if req == nil {
		t.Fatal("no request sent to LLM")
	}

	var userText string
	for _, msg := range req.Messages {
		if msg.Role == 0 { // MessageRoleUser
			for _, content := range msg.Content {
				if content.Type == 2 { // ContentTypeText
					userText = content.Text
					break
				}
			}
		}
	}

	if !strings.Contains(userText, "Startup hook error") {
		t.Errorf("expected user message to contain startup hook error, got: %s", userText)
	}
	if !strings.Contains(userText, "hello") {
		t.Errorf("expected user message to contain original message, got: %s", userText)
	}
}
