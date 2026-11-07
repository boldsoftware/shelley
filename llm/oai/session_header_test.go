package oai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

func TestOpenAIAdapterSessionHeaders(t *testing.T) {
	for _, adapter := range []string{"chat", "chat-stream", "responses", "prewarm"} {
		for _, tc := range []struct {
			name, provider, conversation, cacheKey, sessionID, affinity string
		}{
			{"custom-provider", "zen-go", "conversation-123", "", "conversation-123", ""},
			{"cache-override", "zen-go", "conversation-123", "shared-prefix", "shared-prefix", ""},
			{"openai", "openai", "conversation-123", "", "conversation-123", ""},
			{"unnamed", "", "conversation-123", "", "conversation-123", ""},
			{"no-key", "zen-go", "", "", "", ""},
			{"fireworks", "fireworks", "conversation-123", "shared-prefix", "", "shared-prefix"},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				var headers []http.Header
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					headers = append(headers, r.Header.Clone())
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					switch adapter {
					case "chat":
						fmt.Fprint(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
					case "chat-stream":
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, `data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}

data: [DONE]

`)
					default:
						fmt.Fprint(w, prewarmReply)
					}
				}))
				defer server.Close()

				ctx := llmhttp.WithProvider(t.Context(), tc.provider)
				ctx = llmhttp.WithConversationID(ctx, tc.conversation)
				if tc.cacheKey != "" {
					ctx = llmhttp.WithPromptCacheKey(ctx, tc.cacheKey)
				}
				client := llmhttp.NewClient(server.Client())
				defer client.CloseIdleConnections()
				request := &llm.Request{Messages: []llm.Message{{
					Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
				}}}
				if adapter == "chat-stream" {
					request.OnStream = func(llm.StreamDelta) {}
				}
				// Reusing the context must keep the routing key stable across turns.
				for range 2 {
					switch adapter {
					case "chat", "chat-stream":
						s := &Service{APIKey: "k", Model: modelForTest("test-model"), ModelURL: server.URL, ProviderName: tc.provider, HTTPC: client}
						if _, err := s.Do(ctx, request); err != nil {
							t.Fatal(err)
						}
					default:
						s := &ResponsesService{APIKey: "k", Model: GPT61Sol, ModelURL: server.URL, ProviderName: tc.provider, HTTPC: client}
						if adapter == "prewarm" {
							// Prewarm capability is OpenAI-only; routing uses the context provider.
							s.ProviderName = "openai"
							if _, err := s.WarmCache(ctx, request); err != nil {
								t.Fatal(err)
							}
						} else if _, err := s.Do(ctx, request); err != nil {
							t.Fatal(err)
						}
					}
				}
				if len(headers) != 2 {
					t.Fatalf("requests = %d, want 2", len(headers))
				}
				for i, h := range headers {
					for name, want := range map[string]string{
						"session-id": tc.sessionID, "x-session-affinity": tc.affinity,
						"Shelley-Conversation-Id": tc.conversation,
					} {
						if got := h.Get(name); got != want {
							t.Errorf("request %d: %s = %q, want %q", i, name, got, want)
						}
					}
				}
			})
		}
	}
}
