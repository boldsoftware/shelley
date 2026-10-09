package oai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"shelley.exe.dev/llm"
)

func TestDoSetsResponseTiming(t *testing.T) {
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID:      "chatcmpl-test",
			Model:   "gpt-4.1",
			Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer chat.Close()
	responses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responsesResponse{
			ID:     "resp-test",
			Status: "completed",
			Model:  "test-model",
			Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "ok"}}}},
		})
	}))
	defer responses.Close()

	services := map[string]llm.Service{
		"chat":      &Service{APIKey: "k", Model: GPT41, ModelURL: chat.URL},
		"responses": &ResponsesService{APIKey: "k", Model: GPT41, ModelURL: responses.URL},
	}
	for name, svc := range services {
		t.Run(name, func(t *testing.T) {
			before := time.Now()
			resp, err := svc.Do(t.Context(), &llm.Request{Messages: []llm.Message{{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			if resp.StartTime == nil || resp.EndTime == nil {
				t.Fatalf("StartTime=%v EndTime=%v, want both set", resp.StartTime, resp.EndTime)
			}
			if resp.StartTime.Before(before) || resp.EndTime.Before(*resp.StartTime) || resp.EndTime.After(after) {
				t.Fatalf("bad timing: before=%v start=%v end=%v after=%v", before, resp.StartTime, resp.EndTime, after)
			}
			u := resp.UsageWithMeta()
			if u.StartTime != resp.StartTime || u.EndTime != resp.EndTime {
				t.Fatalf("UsageWithMeta timing = %v/%v, want %v/%v", u.StartTime, u.EndTime, resp.StartTime, resp.EndTime)
			}
		})
	}
}
