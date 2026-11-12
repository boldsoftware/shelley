package loop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/streamtest"
)

// streamingService streams each reply's text before returning it: first a
// reply with a tool call, then a final one.
type streamingService struct {
	requests int
	first    func(*llm.Request) (*llm.Response, error)
}

func (s *streamingService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.requests++
	if s.requests == 1 && s.first != nil {
		return s.first(req)
	}
	if s.requests == 1 {
		req.OnStream(llm.StreamDelta{Type: "text", Text: "wor"})
		req.OnStream(llm.StreamDelta{Type: "text", Text: "king"})
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "working"},
			{Type: llm.ContentTypeToolUse, ID: "job", ToolName: "work", ToolInput: json.RawMessage(`{}`)},
		}}, nil
	}
	req.OnStream(llm.StreamDelta{Type: "text", Text: "done"})
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("done")}, nil
}

func (*streamingService) Provider() string       { return "test" }
func (*streamingService) MaxImageDimension() int { return 0 }
func (*streamingService) MaxImageBytes() int     { return 0 }
func (*streamingService) SupportsImages() bool   { return false }

type recordedRow struct {
	id   string // the message_id the row is saved under ("" means fresh)
	text string
}

func runStreaming(t *testing.T, service *streamingService) (deltas []llm.StreamDelta, rows []recordedRow) {
	t.Helper()
	record := func(id string, m llm.Message) {
		text := ""
		if len(m.Content) > 0 {
			text = m.Content[0].Text
		}
		rows = append(rows, recordedRow{id: id, text: text})
	}
	_ = Run(t.Context(), RunConfig{
		LLM:      service,
		Messages: []llm.Message{llm.UserStringMessage("start")},
		Tools: []*llm.Tool{{Name: "work", Run: func(context.Context, json.RawMessage) llm.ToolOut {
			return llm.ToolOut{LLMContent: llm.TextContent("worked")}
		}}},
		Hooks: Hooks{
			OnStreamingResponse: func(_ context.Context, d llm.StreamDelta) { deltas = append(deltas, d) },
			OnResponse: func(_ context.Context, r Response) error {
				record(r.MessageID, r.Message)
				return nil
			},
			OnToolResponse: func(_ context.Context, r ToolResponse) error {
				record("", r.Message)
				return nil
			},
		},
	})
	return deltas, rows
}

// TestStreamDeltasNameTheirReplyMessage checks that every stream delta carries
// the message_id its reply is then recorded under, and that no other row takes
// a reply's id.
func TestStreamDeltasNameTheirReplyMessage(t *testing.T) {
	deltas, rows := runStreaming(t, &streamingService{})

	if len(deltas) != 3 || len(rows) != 3 {
		t.Fatalf("deltas=%+v rows=%+v; want 3 deltas and 3 rows", deltas, rows)
	}
	first, second := deltas[0].MessageID, deltas[2].MessageID
	if first == "" || second == "" {
		t.Fatalf("deltas without a message id: %+v", deltas)
	}
	if deltas[1].MessageID != first {
		t.Errorf("delta %+v, want id %q", deltas[1], first)
	}
	if first == second {
		t.Errorf("two model requests share id %q", first)
	}
	if want := (recordedRow{id: first, text: "working"}); rows[0] != want {
		t.Errorf("tool-use reply = %+v, want %+v", rows[0], want)
	}
	if rows[1].id != "" {
		t.Errorf("tool result took a reply id: %+v", rows[1])
	}
	if want := (recordedRow{id: second, text: "done"}); rows[2] != want {
		t.Errorf("final reply = %+v, want %+v", rows[2], want)
	}
}

// TestStreamedReplyIDOnFailedReplies checks the replies that end a turn with an
// error: a truncated reply keeps its stream's id (it holds the streamed text)
// and the error row after it gets its own; a failed request records only an
// error row, which does not take the abandoned stream's id.
func TestStreamedReplyIDOnFailedReplies(t *testing.T) {
	t.Run("truncated", func(t *testing.T) {
		deltas, rows := runStreaming(t, &streamingService{first: func(req *llm.Request) (*llm.Response, error) {
			req.OnStream(llm.StreamDelta{Type: "text", Text: "long"})
			return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonMaxTokens, Content: llm.TextContent("long")}, nil
		}})
		if len(deltas) != 1 || len(rows) != 2 {
			t.Fatalf("deltas=%+v rows=%+v", deltas, rows)
		}
		if rows[0].id != deltas[0].MessageID || rows[0].text != "long" {
			t.Errorf("truncated reply = %+v, want id %q", rows[0], deltas[0].MessageID)
		}
		if rows[1].id != "" {
			t.Errorf("truncation error row took a reply id: %+v", rows[1])
		}
	})
	t.Run("refused", func(t *testing.T) {
		deltas, rows := runStreaming(t, &streamingService{first: func(req *llm.Request) (*llm.Response, error) {
			req.OnStream(llm.StreamDelta{Type: "thinking", Text: "hmm"})
			return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonRefusal, Content: llm.TextContent("")}, nil
		}})
		if len(deltas) != 1 || len(rows) != 2 {
			t.Fatalf("deltas=%+v rows=%+v", deltas, rows)
		}
		if rows[0].id != deltas[0].MessageID {
			t.Errorf("refused reply = %+v, want id %q", rows[0], deltas[0].MessageID)
		}
		if rows[1].id != "" {
			t.Errorf("refusal notice row took a reply id: %+v", rows[1])
		}
	})
	t.Run("request error", func(t *testing.T) {
		deltas, rows := runStreaming(t, &streamingService{first: func(req *llm.Request) (*llm.Response, error) {
			req.OnStream(llm.StreamDelta{Type: "text", Text: "part"})
			return nil, errors.New("bad request")
		}})
		if len(deltas) != 1 || len(rows) != 1 {
			t.Fatalf("deltas=%+v rows=%+v", deltas, rows)
		}
		if rows[0].id != "" {
			t.Errorf("error row took the abandoned stream's id: %+v", rows[0])
		}
	})
}

// eofOnceService streams part of a reply and fails with a retryable error the
// loop itself retries (not the provider), then streams the whole reply.
type eofOnceService struct{ streamingService }

func (s *eofOnceService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.requests++
	if s.requests == 1 {
		req.OnStream(llm.StreamDelta{Type: "text", Text: "par"})
		return nil, io.ErrUnexpectedEOF
	}
	req.OnStream(llm.StreamDelta{Type: "text", Text: "done"})
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("done")}, nil
}

// TestLoopRetryKeepsReplyID checks that the loop's own retry (doWithRetry)
// also streams again under the reply's id.
func TestLoopRetryKeepsReplyID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var deltas []llm.StreamDelta
		var rows []recordedRow
		err := Run(t.Context(), RunConfig{
			LLM:      &eofOnceService{},
			Messages: []llm.Message{llm.UserStringMessage("start")},
			Hooks: Hooks{
				OnStreamingResponse: func(_ context.Context, d llm.StreamDelta) { deltas = append(deltas, d) },
				OnResponse: func(_ context.Context, r Response) error {
					rows = append(rows, recordedRow{id: r.MessageID, text: r.Message.Content[0].Text})
					return nil
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(deltas) != 2 {
			t.Fatalf("deltas = %+v, want both attempts' text", deltas)
		}
		id := deltas[0].MessageID
		for _, d := range deltas {
			if d.MessageID != id {
				t.Fatalf("deltas changed id across the retry: %+v", deltas)
			}
		}
		if want := (recordedRow{id: id, text: "done"}); len(rows) != 1 || rows[0] != want {
			t.Fatalf("rows = %+v, want %+v", rows, want)
		}
	})
}

// pausedService answers in two parts, as Anthropic does when it pauses a
// reply to run a server-side tool: the first response ends with
// stop_reason=pause_turn, and the continuation numbers its blocks from 0
// again.
type pausedService struct{ streamingService }

func (s *pausedService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.requests++
	if s.requests == 1 {
		req.OnStream(llm.StreamDelta{Type: "thinking", Text: "Hm.", Index: 0})
		req.OnStream(llm.StreamDelta{Type: "text", Text: "Let me search. ", Index: 1})
		req.OnStream(llm.StreamDelta{Type: "text", Text: "Searching.", Index: 3})
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonPause, Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "Hm."},
			{Type: llm.ContentTypeText, Text: "Let me search. "},
			{Type: llm.ContentTypeServerToolUse, ID: "search", ToolName: "web_search"},
			{Type: llm.ContentTypeText, Text: "Searching."},
		}}, nil
	}
	req.OnStream(llm.StreamDelta{Type: "text", Text: "Found it.", Index: 0})
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("Found it.")}, nil
}

// TestPausedReplyStreamsUnderOneMessage checks that both parts of a paused
// reply stream under one message id and are saved as one message under it.
func TestPausedReplyStreamsUnderOneMessage(t *testing.T) {
	var deltas []llm.StreamDelta
	var saved []Response
	err := Run(t.Context(), RunConfig{
		LLM:      &pausedService{},
		Messages: []llm.Message{llm.UserStringMessage("start")},
		Hooks: Hooks{
			OnStreamingResponse: func(_ context.Context, d llm.StreamDelta) { deltas = append(deltas, d) },
			OnResponse: func(_ context.Context, r Response) error {
				saved = append(saved, r)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || len(saved[0].Message.Content) != 5 {
		t.Fatalf("saved %+v, want one message with both parts' five blocks", saved)
	}
	if len(deltas) != 4 {
		t.Fatalf("deltas = %+v, want both parts' four deltas", deltas)
	}
	for _, d := range deltas {
		if d.MessageID != saved[0].MessageID {
			t.Errorf("delta %+v, want message %s", d, saved[0].MessageID)
		}
	}
}

// providerStream records what the provider streamed, before the loop sees it.
type providerStream struct {
	llm.Service
	deltas []llm.StreamDelta
}

func (s *providerStream) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	recorded := *req
	recorded.OnStream = func(d llm.StreamDelta) {
		s.deltas = append(s.deltas, d)
		req.OnStream(d)
	}
	return s.Service.Do(ctx, &recorded)
}

// TestExistingClientsSeeWhatTheProviderStreamed checks that the loop passes
// on each provider delta's type and text unchanged and in order, adding and
// dropping none, so a client that only appends text shows what it showed
// before deltas carried message ids.
func TestExistingClientsSeeWhatTheProviderStreamed(t *testing.T) {
	provider := &providerStream{Service: &pausedService{}}
	var got, want streamtest.LegacyClient
	n := 0
	err := Run(t.Context(), RunConfig{
		LLM:      provider,
		Messages: []llm.Message{llm.UserStringMessage("start")},
		Hooks: Hooks{OnStreamingResponse: func(_ context.Context, d llm.StreamDelta) {
			n++
			got.Apply(d)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range provider.deltas {
		want.Apply(d)
	}
	if n != len(provider.deltas) || got != want {
		t.Errorf("%d deltas reached clients, which show %+v; the provider streamed %d, which show %+v", n, got, len(provider.deltas), want)
	}
}
