// Package streamtest has clients for stream deltas, for tests: one that
// groups streamed text by message id, and one that does what clients did
// before deltas carried message ids.
package streamtest

import (
	"testing"

	"shelley.exe.dev/llm"
)

// Client holds the text streamed for each reply of one conversation, by
// message id. It applies only the first rule in API.md: it does not handle
// saved messages or the end of a turn.
type Client map[string]*LegacyClient

// Apply appends one delta's text to what its message id has streamed.
func (c Client) Apply(t testing.TB, d llm.StreamDelta) {
	t.Helper()
	if d.MessageID == "" {
		t.Fatalf("delta %+v has no message id", d)
	}
	if c[d.MessageID] == nil {
		c[d.MessageID] = &LegacyClient{}
	}
	c[d.MessageID].Apply(d)
}

// LegacyClient is how clients that predate message ids show a streaming
// reply: they append each delta's text, by type, in the order it arrives,
// and read nothing else. Web keeps both buffers; iOS and Android only Text.
type LegacyClient struct{ Text, Thinking string }

// Apply appends one delta's text.
func (c *LegacyClient) Apply(d llm.StreamDelta) {
	switch d.Type {
	case "text":
		c.Text += d.Text
	case "thinking":
		c.Thinking += d.Text
	}
}
