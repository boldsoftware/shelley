package browse

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAccessibilityNodeHonorsTimeout guards against accessibility_node waiting
// forever for an element that never appears, which wedged the sequential
// browser tool for the rest of the conversation.
func TestAccessibilityNodeHonorsTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(tools.Close)
	browser := tools.CombinedTool()

	out := browser.Run(ctx, json.RawMessage(`{"action":"navigate","url":"data:text/html,<button>Go</button>"}`))
	if out.Error != nil {
		if strings.Contains(out.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("navigate: %v", out.Error)
	}

	out = browser.Run(ctx, json.RawMessage(`{"action":"accessibility_node","selector":"#missing","timeout":"200ms"}`))
	if !errors.Is(out.Error, context.DeadlineExceeded) {
		t.Fatalf("got error %v, want a deadline exceeded error", out.Error)
	}
}
