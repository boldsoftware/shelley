package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func originFor(s *Service, model string) *llm.MessageOrigin {
	origin := s.messageOrigin(model)
	return &origin
}

func thinkingBlocks(w request) int {
	n := 0
	for _, m := range w.Messages {
		for _, c := range m.Content {
			if c.Type == "thinking" || c.Type == "redacted_thinking" {
				n++
			}
		}
	}
	return n
}

func TestThinkingOriginFiltering(t *testing.T) {
	s := &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelLow}
	current := originFor(s, Claude46Opus)
	for _, tt := range []struct {
		name   string
		origin *llm.MessageOrigin
		want   int
	}{
		{"same origin", current, 2},
		{"cross model", originFor(s, Claude5Opus), 0},
		{"cross provider", &llm.MessageOrigin{Provider: "openai", Transport: "openai-responses:https://api.openai.com/v1", Model: "gpt-5.6-sol"}, 0},
		{"cross transport", &llm.MessageOrigin{Provider: "anthropic", Transport: "anthropic-messages:https://gateway.example/v1/messages", Model: Claude46Opus}, 0},
		{"legacy unknown", nil, 2},
		{"incomplete legacy", &llm.MessageOrigin{Provider: "anthropic"}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msgs := thinkingRound("turn")
			msgs[0].Origin = tt.origin
			before := thinkingJSON(t, msgs)
			wire := s.fromLLMRequest(&llm.Request{Messages: msgs})
			if got := thinkingBlocks(*wire); got != tt.want {
				t.Fatalf("thinking blocks = %d, want %d", got, tt.want)
			}
			content := wire.Messages[0].Content
			if len(content) == 0 || content[len(content)-1].Type != "tool_use" {
				t.Fatal("portable tool content was not retained")
			}
			if !bytes.Equal(before, thinkingJSON(t, msgs)) {
				t.Fatal("outgoing origin filtering mutated stored history")
			}
		})
	}
}

func TestThinkingOriginSwitchBackAndActiveToolContinuation(t *testing.T) {
	s := &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelLow}
	msgs := thinkingRound("active")
	msgs[0].Origin = originFor(s, Claude46Opus)
	msgs[0].EndOfTurn = false
	before := thinkingJSON(t, msgs)
	first := s.fromLLMRequest(&llm.Request{Messages: msgs})
	if got := thinkingBlocks(*first); got != 2 || len(first.Messages[0].Content) != 3 {
		t.Fatalf("compatible active tool continuation changed: blocks=%d content=%d", got, len(first.Messages[0].Content))
	}

	s.Model = Claude5Opus
	switched := s.fromLLMRequest(&llm.Request{Messages: msgs})
	if got := thinkingBlocks(*switched); got != 0 || len(switched.Messages[0].Content) != 1 {
		t.Fatalf("switch did not filter opaque state: blocks=%d content=%d", got, len(switched.Messages[0].Content))
	}
	s.Model = Claude46Opus
	back := s.fromLLMRequest(&llm.Request{Messages: msgs})
	if !reflect.DeepEqual(first.Messages, back.Messages) || !bytes.Equal(before, thinkingJSON(t, msgs)) {
		t.Fatal("switching back did not restore the compatible outgoing copy")
	}
}

func TestAnthropicOriginJSONRoundTrip(t *testing.T) {
	s := &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelLow}
	s.HTTPC = &http.Client{Transport: &roundTripFunc{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(mockSSEResponse("msg", Claude46Opus, "answer", 1, 1)))}, nil
	}}}
	resp, err := s.Do(context.Background(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	stored := resp.ToMessage()
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded llm.Message
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	if !stored.Origin.Matches(s.messageOrigin(Claude46Opus)) || !reflect.DeepEqual(stored.Origin, reloaded.Origin) {
		t.Fatalf("origin did not survive response/message JSON: %#v %#v", stored.Origin, reloaded.Origin)
	}
}

func TestThinkingOriginSurvivesCompactedTail(t *testing.T) {
	s := &Service{Model: ClaudeFable51, ThinkingLevel: llm.ThinkingLevelLow}
	tail := thinkingRound("tail")
	tail[0].Origin = originFor(s, s.Model)
	compacted := append([]llm.Message{llm.UserStringMessage("summary")}, tail...)

	raw := thinkingJSON(t, compacted)
	var reloaded []llm.Message
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	wire := s.fromLLMRequest(&llm.Request{Messages: reloaded})
	if len(wire.Messages) != 3 || len(wire.Messages[1].Content) != 3 {
		t.Fatal("compacted tail lost compatible thinking")
	}
	if wire.Messages[1].Content[0].Type != "thinking" ||
		wire.Messages[1].Content[1].Type != "redacted_thinking" {
		t.Fatal("compacted tail thinking changed")
	}
}
