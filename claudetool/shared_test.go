package claudetool

import (
	"context"
	"testing"
)

func TestWithWorkingDir(t *testing.T) {
	ctx := context.Background()
	wd := "/test/working/dir"

	newCtx := WithWorkingDir(ctx, wd)
	if newCtx == nil {
		t.Fatal("WithWorkingDir returned nil context")
	}
}

func TestWorkingDir(t *testing.T) {
	ctx := context.Background()
	wd := "/test/working/dir"

	// Test with working dir set
	ctxWithWd := WithWorkingDir(ctx, wd)
	result := WorkingDir(ctxWithWd)

	if result != wd {
		t.Errorf("expected %q, got %q", wd, result)
	}

	// Test without working dir set
	result = WorkingDir(ctx)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestWithSessionID(t *testing.T) {
	ctx := context.Background()
	sessionID := "test-session-id"

	newCtx := WithSessionID(ctx, sessionID)
	if newCtx == nil {
		t.Fatal("WithSessionID returned nil context")
	}
}

func TestSessionID(t *testing.T) {
	ctx := context.Background()
	sessionID := "test-session-id"

	// Test with session ID set
	ctxWithSession := WithSessionID(ctx, sessionID)
	result := SessionID(ctxWithSession)

	if result != sessionID {
		t.Errorf("expected %q, got %q", sessionID, result)
	}

	// Test without session ID set
	result = SessionID(ctx)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}
