package models

import (
	"errors"
	"testing"

	"shelley.exe.dev/llm"
)

func TestGetWorkhorseServiceNoFallback(t *testing.T) {
	for _, tc := range []struct {
		name         string
		conversation string
		workhorse    string
		requestError error
	}{
		{name: "selected workhorse", conversation: "gpt-6-sol", workhorse: "gpt-6-luna"},
		{name: "workhorse conversation", conversation: "gpt-6-luna", workhorse: "gpt-6-luna"},
		{name: "request failure", conversation: "gpt-6-sol", workhorse: "gpt-6-luna", requestError: errors.New("workhorse unavailable")},
		{name: "missing workhorse", conversation: "gpt-6-sol"},
		{name: "unknown conversation", conversation: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := &recordingService{provider: "openai", err: tc.requestError}
			conversation := &recordingService{provider: "openai"}
			built := []Built{{ID: "gpt-6-sol", Provider: ProviderOpenAI, Service: conversation}}
			if tc.workhorse != "" {
				built = append(built, Built{ID: tc.workhorse, Provider: ProviderOpenAI, Service: primary})
			}
			manager := newWorkhorseManager(t, built...)
			service, err := manager.GetWorkhorseServiceNoFallback(tc.conversation)
			if tc.workhorse == "" {
				if err == nil {
					t.Fatal("accepted missing workhorse")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_, err = service.Do(t.Context(), &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh})
				if !errors.Is(err, tc.requestError) || primary.calls != 1 {
					t.Fatalf("request = %v, calls = %d; want %v, 1", err, primary.calls, tc.requestError)
				}
				if primary.request.ThinkingLevel != llm.ThinkingLevelHigh {
					t.Fatalf("strict selection changed caller effort: %v", primary.request.ThinkingLevel)
				}
			}
			if conversation.calls != 0 {
				t.Fatalf("conversation model called %d times", conversation.calls)
			}
		})
	}
}
