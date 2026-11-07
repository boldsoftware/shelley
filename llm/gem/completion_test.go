package gem

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

func TestGeminiCompletionStatus(t *testing.T) {
	type testCase struct {
		name        string
		body        string
		wantStop    llm.StopReason
		wantRefusal string
		wantErr     string
	}
	tests := []testCase{
		{
			name:     "normal text",
			body:     `"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"Done"}]}}]`,
			wantStop: llm.StopReasonEndTurn,
		},
		{
			name:     "parallel tool calls",
			body:     `"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"call_one","name":"bash","args":{"command":"pwd"}}},{"functionCall":{"id":"call_two","name":"bash","args":{"command":"ls"}}}]}}]`,
			wantStop: llm.StopReasonToolUse,
		},
		{
			name:     "truncated text",
			body:     `"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"Partial"}]}}]`,
			wantStop: llm.StopReasonMaxTokens,
		},
		{
			name:     "truncated thinking without visible output",
			body:     `"candidates":[{"finishReason":"MAX_TOKENS"}]`,
			wantStop: llm.StopReasonMaxTokens,
		},
		{
			name:     "truncation overrides tool calls",
			body:     `"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"functionCall":{"id":"partial","name":"bash","args":{}}}]}}]`,
			wantStop: llm.StopReasonMaxTokens,
		},
		{
			name:     "continuation is not completion",
			body:     `"candidates":[{"finishReason":"CONTINUATION","content":{"parts":[{"functionCall":{"id":"partial","name":"bash","args":{}}}]}}]`,
			wantStop: llm.StopReasonMaxTokens,
		},
		{
			name:        "refusal overrides tool calls",
			body:        `"candidates":[{"finishReason":"SAFETY","finishMessage":"Filtered output","content":{"parts":[{"functionCall":{"id":"blocked","name":"bash","args":{}}}]}}]`,
			wantStop:    llm.StopReasonRefusal,
			wantRefusal: "SAFETY",
		},
		{
			name:        "recitation",
			body:        `"candidates":[{"finishReason":"RECITATION"}]`,
			wantStop:    llm.StopReasonRefusal,
			wantRefusal: "RECITATION",
		},
		{
			name:        "blocked prompt without candidates",
			body:        `"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"Filtered prompt"}`,
			wantStop:    llm.StopReasonRefusal,
			wantRefusal: "SAFETY",
		},
		{
			name:        "prompt block overrides tool calls",
			body:        `"promptFeedback":{"blockReason":"BLOCKLIST"},"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"blocked","name":"bash","args":{}}}]}}]`,
			wantStop:    llm.StopReasonRefusal,
			wantRefusal: "BLOCKLIST",
		},
		{
			name:    "no candidates",
			body:    `"candidates":[]`,
			wantErr: "no candidates",
		},
		{
			name:    "unspecified prompt feedback is not a refusal",
			body:    `"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}`,
			wantErr: "no candidates",
		},
		{
			name:    "missing finish reason",
			body:    `"candidates":[{"content":{"parts":[{"text":"Not necessarily done"}]}}]`,
			wantErr: "finish reason",
		},
		{
			name:    "unspecified finish reason",
			body:    `"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}]`,
			wantErr: "FINISH_REASON_UNSPECIFIED",
		},
		{
			name:    "malformed tool call",
			body:    `"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Invalid arguments","content":{"parts":[{"functionCall":{"id":"invalid","name":"bash","args":{}}}]}}]`,
			wantErr: "MALFORMED_FUNCTION_CALL",
		},
		{
			name:    "unexpected tool call",
			body:    `"candidates":[{"finishReason":"UNEXPECTED_TOOL_CALL"}]`,
			wantErr: "UNEXPECTED_TOOL_CALL",
		},
		{
			name:    "unknown finish reason",
			body:    `"candidates":[{"finishReason":"NEW_PROVIDER_FAILURE"}]`,
			wantErr: "NEW_PROVIDER_FAILURE",
		},
	}
	for _, reason := range []string{
		"BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION",
	} {
		tests = append(tests, testCase{
			name: "refusal " + reason, body: fmt.Sprintf(`"candidates":[{"finishReason":%q}]`, reason),
			wantStop: llm.StopReasonRefusal, wantRefusal: reason,
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{%s,"usageMetadata":{"promptTokenCount":120,"cachedContentTokenCount":80,"candidatesTokenCount":9,"thoughtsTokenCount":4,"toolUsePromptTokenCount":3,"totalTokenCount":136}}`, tt.body)
			service := &Service{
				Model:  "gemini-test",
				APIKey: "test-key",
				URL:    "https://test.googleapis.com",
				HTTPC: &http.Client{Transport: &mockRoundTripper{response: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}, "Exedev-Gateway-Cost": {"0.0005"}},
					Body:       io.NopCloser(strings.NewReader(geminiSSE(body))),
				}}},
			}
			response, err := service.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{llm.UserStringMessage("Hello")},
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Do error = %v, want %q", err, tt.wantErr)
				}
				if response != nil {
					t.Fatalf("unexpected successful response: %#v", response)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.StopReason != tt.wantStop {
				t.Errorf("stop reason = %v, want %v", response.StopReason, tt.wantStop)
			}
			if got, want := response.Usage, (llm.Usage{
				InputTokens: 43, CacheReadInputTokens: 80, OutputTokens: 13, CostUSD: 0.0005,
			}); got != want {
				t.Errorf("usage = %#v, want %#v", got, want)
			}
			if tt.wantRefusal != "" {
				if response.RefusalDetails == nil || response.RefusalDetails.Category != tt.wantRefusal {
					t.Fatalf("refusal details = %#v, want category %q", response.RefusalDetails, tt.wantRefusal)
				}
				if tt.name == "refusal overrides tool calls" && response.RefusalDetails.Explanation != "Filtered output" {
					t.Errorf("lost candidate finish message: %#v", response.RefusalDetails)
				}
				if tt.name == "blocked prompt without candidates" && response.RefusalDetails.Explanation != "Filtered prompt" {
					t.Errorf("lost prompt block message: %#v", response.RefusalDetails)
				}
			} else if response.RefusalDetails != nil {
				t.Errorf("unexpected refusal details: %#v", response.RefusalDetails)
			}
			if tt.name == "parallel tool calls" {
				if len(response.Content) != 2 || response.Content[0].ID != "call_one" || response.Content[1].ID != "call_two" {
					t.Errorf("lost parallel call IDs: %#v", response.Content)
				}
			}
		})
	}
}

func TestGeminiUsageMetadata(t *testing.T) {
	type testCase struct {
		name     string
		metadata string
		want     llm.Usage
		wantErr  string
	}
	tests := []testCase{
		{
			name:     "uncached prompt and output",
			metadata: `{"promptTokenCount":100,"candidatesTokenCount":20}`,
			want:     llm.Usage{InputTokens: 100, OutputTokens: 20},
		},
		{
			name:     "cache thinking and tool prompt",
			metadata: `{"promptTokenCount":100,"cachedContentTokenCount":60,"candidatesTokenCount":20,"thoughtsTokenCount":30,"toolUsePromptTokenCount":5,"totalTokenCount":155}`,
			want:     llm.Usage{InputTokens: 45, CacheReadInputTokens: 60, OutputTokens: 50},
		},
		{
			name:     "fully cached prompt",
			metadata: `{"promptTokenCount":100,"cachedContentTokenCount":100}`,
			want:     llm.Usage{CacheReadInputTokens: 100},
		},
		{
			name:     "thinking without visible output",
			metadata: `{"promptTokenCount":100,"thoughtsTokenCount":30}`,
			want:     llm.Usage{InputTokens: 100, OutputTokens: 30},
		},
		{
			name:     "reported zero does not estimate text",
			metadata: `{}`,
		},
		{
			name:    "missing metadata",
			wantErr: "missing usage metadata",
		},
		{
			name:     "null metadata",
			metadata: `null`,
			wantErr:  "missing usage metadata",
		},
		{
			name:     "cached tokens exceed prompt",
			metadata: `{"promptTokenCount":10,"cachedContentTokenCount":11}`,
			wantErr:  "cached token count exceeds prompt",
		},
	}
	for _, field := range []string{
		"promptTokenCount", "cachedContentTokenCount", "candidatesTokenCount",
		"thoughtsTokenCount", "toolUsePromptTokenCount",
	} {
		tests = append(tests, testCase{
			name: "negative " + field, metadata: fmt.Sprintf(`{%q:-1}`, field), wantErr: "negative token count",
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := ""
			if tt.metadata != "" {
				metadata = `,"usageMetadata":` + tt.metadata
			}
			body := `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"Visible text must not determine reported token counts."}]}}]` + metadata + `}`
			service := &Service{
				Model: "gemini-test", APIKey: "test-key", URL: "https://test.googleapis.com",
				HTTPC: &http.Client{Transport: &mockRoundTripper{response: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(geminiSSE(body))),
				}}},
			}
			response, err := service.Do(t.Context(), &llm.Request{
				Messages: []llm.Message{llm.UserStringMessage("Input text must not determine reported token counts.")},
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Do error = %v, want %q", err, tt.wantErr)
				}
				if response != nil {
					t.Fatalf("unexpected successful response: %#v", response)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Usage != tt.want {
				t.Errorf("usage = %#v, want %#v", response.Usage, tt.want)
			}
		})
	}
}

func TestGeminiBlockedPromptWithoutUsage(t *testing.T) {
	response, err := convertGeminiResponse(&gemini.Response{
		PromptFeedback: &gemini.PromptFeedback{BlockReason: "SAFETY"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing usage metadata") {
		t.Fatalf("conversion error = %v, want missing usage metadata", err)
	}
	if response != nil {
		t.Fatalf("unexpected successful response: %#v", response)
	}
}
