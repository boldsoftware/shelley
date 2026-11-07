package llmhttp

import (
	"strings"
	"testing"
)

func TestReadSSEComments(t *testing.T) {
	// Comments (lines starting with ':') should be ignored.
	stream := ": this is a comment\nevent: ping\ndata: {}\n\n"
	var events []SSEEvent
	err := ReadSSE(strings.NewReader(stream), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadSSE() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].EventType != "ping" {
		t.Errorf("event type = %q, want %q", events[0].EventType, "ping")
	}
}

func TestReadSSENoTrailingNewline(t *testing.T) {
	// Stream that ends without a trailing blank line should still dispatch.
	stream := "event: ping\ndata: {}"
	var events []SSEEvent
	err := ReadSSE(strings.NewReader(stream), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadSSE() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
}
