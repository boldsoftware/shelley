package predictable

import (
	"context"
	"errors"
	"strings"
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

// A "stream <total>/<every>: " reply grows evenly, whatever its length:
// every <every> (and at <total>, when <every> doesn't divide it), the
// text due by then, ending at <total>.
func TestDoStreamsATimedReplyEvenly(t *testing.T) {
	for _, text := range []string{"ab", "one two three four five six seven eight", "考え中💭 and then some more"} {
		for _, spec := range []string{"2s/250ms", "1s/300ms"} {
			t.Run(spec+" "+text, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					total, every, _ := strings.Cut(spec, "/")
					totalD, _ := time.ParseDuration(total)
					everyD, _ := time.ParseDuration(every)
					start := time.Now()
					var streamed []rune
					var at []time.Duration
					req := &llm.Request{
						Messages: []llm.Message{llm.UserStringMessage("stream " + spec + ": " + text)},
						OnStream: func(d llm.StreamDelta) {
							streamed = append(streamed, []rune(d.Text)...)
							elapsed := time.Since(start)
							at = append(at, elapsed)
							if elapsed%everyD != 0 && elapsed != totalD {
								t.Errorf("a piece at %v, not on a %v tick", elapsed, everyD)
							}
							if due := len([]rune(text)) * int(elapsed) / int(totalD); len(streamed) != due {
								t.Errorf("at %v, %d characters streamed, want the %d due", elapsed, len(streamed), due)
							}
						},
					}
					resp, err := NewService().Do(t.Context(), req)
					if err != nil {
						t.Fatal(err)
					}
					if string(streamed) != resp.Content[0].Text || string(streamed) != text {
						t.Errorf("streamed %q, returned %q", string(streamed), resp.Content[0].Text)
					}
					if last := at[len(at)-1]; last != totalD {
						t.Errorf("the last piece came at %v, want %v", last, totalD)
					}
				})
			})
		}
	}
}

// A "stream until <gate>: " reply streams its first half, then nothing
// more until another request says "release: <gate>", then the rest: a
// client sees it part way for as long as it needs to.
func TestDoHoldsAGatedReplyHalfWayUntilReleased(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := NewService()
		pieces := make(chan string, 2)
		done := make(chan *llm.Response)
		go func() {
			resp, err := svc.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{llm.UserStringMessage("stream until g1: ping pong")},
				OnStream: func(d llm.StreamDelta) { pieces <- d.Text },
			})
			if err != nil {
				resp = nil
			}
			done <- resp
		}()
		synctest.Wait()
		if got := <-pieces; got != "ping" {
			t.Fatalf("first piece %q, want the first half", got)
		}
		select {
		case p := <-pieces:
			t.Fatalf("streamed %q before the release", p)
		case <-done:
			t.Fatal("answered before the release")
		default:
		}
		resp, err := svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("release: g1")}})
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Content[0].Text; got != "Released g1." {
			t.Errorf("release answered %q", got)
		}
		if got := <-pieces; got != " pong" {
			t.Errorf("second piece %q, want the rest", got)
		}
		if resp := <-done; resp == nil || resp.Content[0].Text != "ping pong" {
			t.Errorf("reply %v, want the whole text", resp)
		}
	})
}

// Released first, a gated reply streams whole at once; cancelled while
// held, it returns the context's error.
func TestDoGatedReplyReleasedEarlyOrCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := NewService()
		if _, err := svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("release: g2")}}); err != nil {
			t.Fatal(err)
		}
		resp, err := svc.Do(t.Context(), &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("stream until g2: early bird")},
			OnStream: func(llm.StreamDelta) {},
		})
		if err != nil || resp.Content[0].Text != "early bird" {
			t.Fatalf("released early: response %v, error %v", resp, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		resp, err = svc.Do(ctx, &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("stream until g3: never released")},
			OnStream: func(llm.StreamDelta) { cancel() },
		})
		if resp != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled: response %v, error %v; want context.Canceled", resp, err)
		}
	})
}

// A gate opens for the reply that waits on it, then closes again: the
// next reply to name it waits for its own release.
func TestDoGateOpensOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := NewService()
		if _, err := svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("release: g4")}}); err != nil {
			t.Fatal(err)
		}
		gated := &llm.Request{
			Messages: []llm.Message{llm.UserStringMessage("stream until g4: once only")},
			OnStream: func(llm.StreamDelta) {},
		}
		if _, err := svc.Do(t.Context(), gated); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() {
			_, err := svc.Do(ctx, gated)
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("the second reply passed an opened gate (error %v)", err)
		default:
		}
		cancel()
		<-done
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
