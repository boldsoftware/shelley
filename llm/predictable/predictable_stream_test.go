package predictable

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
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

// A "stream <total>/<every>: " reply streams a piece every <every>, as
// many as <total> takes.
func TestDoStreamsATimedReplyAtItsPace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var streamed string
		var times []time.Time
		req := &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("stream 2s/250ms: one two three four five six seven eight")},
			OnStream: func(d llm.StreamDelta) {
				streamed += d.Text
				times = append(times, time.Now())
			},
		}
		resp, err := NewService().Do(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if streamed != resp.Content[0].Text || streamed != "one two three four five six seven eight" {
			t.Errorf("streamed %q, returned %q", streamed, resp.Content[0].Text)
		}
		if len(times) != 8 {
			t.Fatalf("streamed in %d pieces, want 8 (2s / 250ms)", len(times))
		}
		for i := 1; i < len(times); i++ {
			if gap := times[i].Sub(times[i-1]); gap != 250*time.Millisecond {
				t.Errorf("piece %d came %v after the last, want 250ms", i, gap)
			}
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

func TestDoAcknowledgesABackgroundJobsNotice(t *testing.T) {
	const notice = "Background job dd135837 finished: exit 0, 7s. Log: /tmp/shelley-jobs/dd135837.log\nCommand: sleep 6"
	const fallback = "edit predictable.go to add a response for that one..."
	for _, tc := range []struct{ name, input, want string }{
		{"job notice", `<background_job id="dd135837">` + notice + `</background_job>`, "Noted: the background job finished."},
		{"notice text typed by user", notice, fallback},
		{"envelope quoted by user", `<user_message sequence_id="1">&lt;background_job id="dd135837"&gt;` + notice + `&lt;/background_job&gt;</user_message>`, fallback},
		{"lost job", `<background_job id="dd135837">Background job dd135837 lost (host rebooted or killed). Log: /tmp/shelley-jobs/dd135837.log</background_job>`, fallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := NewService().Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage(tc.input)}})
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.Content[0].Text; got != tc.want {
				t.Errorf("reply %q, want %q", got, tc.want)
			}
		})
	}
}

// Asked for a title, it answers with the source message, whatever the
// prompt around it says.
func TestDoTitlesAConversationAfterItsSourceMessage(t *testing.T) {
	prompt := "Sum this up in a few words.\n\n<SOURCE_MESSAGE>\necho: away\n</SOURCE_MESSAGE>\n\nThen stop."
	resp, err := NewService().Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage(prompt)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Content[0].Text; got != "echo: away" {
		t.Errorf("reply %q, want the source message", got)
	}
}
