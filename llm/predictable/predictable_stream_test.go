package predictable

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

// A "stream: " reply streams in more than one piece, never splitting a
// character, and the pieces add up to what Do returns.
func TestDoStreamsAStreamReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var streamed string
		pieces := 0
		req := &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("stream: 考え中💭 and then some")},
			OnStream: func(d llm.StreamDelta) {
				if d.Type != "text" || d.Index != 0 {
					t.Errorf("piece %+v, want text of block 0", d)
				}
				if !utf8.ValidString(d.Text) {
					t.Errorf("piece %q splits a character", d.Text)
				}
				streamed += d.Text
				pieces++
			},
		}
		resp, err := NewService().Do(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if streamed != resp.Content[0].Text {
			t.Errorf("streamed %q, returned %q", streamed, resp.Content[0].Text)
		}
		if pieces < 2 {
			t.Errorf("streamed in %d pieces, want several", pieces)
		}
	})
}

// Any other reply comes back whole: nothing streams.
func TestDoReturnsOtherRepliesWhole(t *testing.T) {
	for _, message := range []string{"echo: a reply of some length", "think: about it"} {
		req := &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage(message)},
			OnStream: func(d llm.StreamDelta) { t.Errorf("%q streamed %+v", message, d) },
		}
		if _, err := NewService().Do(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
}

// Cancelled part way through a reply, Do stops streaming and returns the
// context's error, as a provider whose request was cancelled does.
func TestDoStopsStreamingWhenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		req := &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("stream: partial reply")},
			OnStream: func(llm.StreamDelta) { calls++; cancel() },
		}
		resp, err := NewService().Do(ctx, req)
		if calls != 1 || resp != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("got %d pieces, response %v, error %v; want one piece and context.Canceled", calls, resp, err)
		}
	})
}
