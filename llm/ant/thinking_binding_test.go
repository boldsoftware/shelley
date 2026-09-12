package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func TestThinkingBindingRequest(t *testing.T) {
	for _, tt := range []struct {
		name    string
		model   string
		url     string
		enabled bool
		level   llm.ThinkingLevel
		want    bool
	}{
		{"native", Claude46Opus, "", false, llm.ThinkingLevelMedium, true},
		{"native alias", "claude-future-alias", "", false, llm.ThinkingLevelMedium, true},
		{"gateway", Claude46Sonnet, "https://gateway.example/v1/messages", true, llm.ThinkingLevelLow, true},
		{"third party", Claude46Opus, "https://third.example/v1/messages", false, llm.ThinkingLevelLow, false},
		{"bedrock", "us.anthropic.claude-sonnet-4-5-v1:0", "https://bedrock.example/v1/messages", false, llm.ThinkingLevelLow, false},
		{"thinking off", Claude46Opus, "", false, llm.ThinkingLevelOff, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{
				Model:                 tt.model,
				URL:                   tt.url,
				ThinkingLevel:         tt.level,
				EnableThinkingBinding: tt.enabled,
				HTTPC: &http.Client{Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
					var wire request
					if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
						t.Fatal(err)
					}
					got := wire.Thinking != nil && wire.Thinking.BlockBinding != nil
					if got != tt.want {
						t.Fatalf("binding = %t, want %t", got, tt.want)
					}
					if (req.Header.Get("Anthropic-Beta") == thinkingBindingBeta) != tt.want {
						t.Fatalf("beta header = %q", req.Header.Get("Anthropic-Beta"))
					}
					if got && wire.Thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" {
						t.Fatal("wrong prefix mismatch behavior")
					}
					return httpResponse(http.StatusOK, mockSSEResponse("msg", tt.model, "ok", 1, 1)), nil
				}}},
			}
			if _, err := s.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{llm.UserStringMessage("hello")},
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestThinkingBindingStreamMetadata(t *testing.T) {
	a := inputTransformation{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch"}
	b := inputTransformation{Type: "thinking_dropped", Path: "messages.3.content.0", Reason: "model_binding_mismatch"}
	var stream strings.Builder
	emit := func(v any) { stream.WriteString("data: " + string(thinkingJSON(t, v)) + "\n\n") }
	emit(streamEvent{
		Type:    "message_start",
		Message: &response{ID: "msg", Model: Claude46Opus, Role: "assistant", InputTransformations: []inputTransformation{a, a}},
	})
	emit(streamEvent{Type: "content_block_start", ContentBlock: &content{Type: "text", Text: strp("answer")}})
	emit(streamEvent{
		Type:                 "message_delta",
		Delta:                thinkingJSON(t, streamDelta{StopReason: "end_turn"}),
		InputTransformations: []inputTransformation{a, b, b},
	})
	emit(streamEvent{Type: "message_stop"})

	parsed, err := parseSSEStream(strings.NewReader(stream.String()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.InputTransformations, []inputTransformation{a, b}) {
		t.Fatalf("transformations = %+v", parsed.InputTransformations)
	}
	out := toLLMResponse(parsed)
	if out.Content[0].Text != "answer" || out.StopReason != llm.StopReasonEndTurn {
		t.Fatal("metadata changed model output")
	}
	if bytes.Contains(thinkingJSON(t, out), []byte("binding_mismatch")) {
		t.Fatal("input metadata escaped the adapter")
	}
}

func TestThinkingBindingProviderDiagnostics(t *testing.T) {
	entry := inputTransformation{
		Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch",
	}
	start := streamEvent{
		Type:    "message_start",
		Message: &response{ID: "msg", Model: Claude46Opus, Role: "assistant", InputTransformations: []inputTransformation{entry, entry}},
	}
	stream := "data: " + string(thinkingJSON(t, start)) + "\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"secret-thinking\",\"signature\":\"secret-signature\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	logs := captureLogs(t)
	s := &Service{
		Model:         Claude46Opus,
		ThinkingLevel: llm.ThinkingLevelLow,
		HTTPC: &http.Client{Transport: &roundTripFunc{fn: func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Request-Id": {"req-safe"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}, nil
		}}},
	}
	out, err := s.Do(context.Background(), &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("hello")},
	})
	if err != nil || out == nil {
		t.Fatalf("response=%t err=%v", out != nil, err)
	}
	for _, want := range []string{
		"anthropic_input_transformation", "req-safe", "msg",
		entry.Type, entry.Path, entry.Reason,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log missing %q: %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "secret-") {
		t.Fatal("provider log leaked thinking")
	}
}
