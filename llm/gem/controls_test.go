package gem

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestModelAwareThinkingControls(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		service    llm.ThinkingLevel
		serviceRaw string
		level      llm.ThinkingLevel
		raw        string
		wantLevel  string
		wantBudget *int
		wantErr    string
	}{
		{name: "Argon level", model: "gemini-4-argon", level: llm.ThinkingLevelHigh, wantLevel: "high"},
		{name: "Argon resource name", model: "models/gemini-4-argon", level: llm.ThinkingLevelLow, wantLevel: "low"},
		{name: "Argon snapshot", model: "gemini-4-argon-2026-11-06", level: llm.ThinkingLevelMedium, wantLevel: "medium"},
		{name: "Flash alias", model: "gemini-flash-latest", level: llm.ThinkingLevelHigh, wantLevel: "high"},
		{name: "Flash Lite alias", model: "gemini-flash-lite-latest", level: llm.ThinkingLevelMinimal, wantLevel: "minimal"},
		{name: "request raw wins", model: "gemini-4-argon", service: llm.ThinkingLevelHigh, serviceRaw: "medium", level: llm.ThinkingLevelHigh, raw: "low", wantLevel: "low"},
		{name: "request generic beats service raw", model: "gemini-4-argon", serviceRaw: "low", level: llm.ThinkingLevelHigh, wantLevel: "high"},
		{name: "service raw beats generic", model: "gemini-4-argon", service: llm.ThinkingLevelHigh, serviceRaw: "low", wantLevel: "low"},
		{name: "generic extreme clamps", model: "gemini-4-argon", level: llm.ThinkingLevelXHigh, wantLevel: "high"},
		{name: "generic minimal clamps", model: "gemini-3.8-flash", level: llm.ThinkingLevelMinimal, wantLevel: "low"},
		{name: "Argon raw minimal rejected", model: "gemini-4-argon", raw: "minimal", wantErr: "not supported"},
		{name: "Flash raw minimal rejected", model: "gemini-3.8-flash", raw: "minimal", wantErr: "not supported"},
		{name: "unknown effort rejected", model: "gemini-4-argon", raw: "ultra", wantErr: "not supported"},
		{name: "Argon off rejected", model: "gemini-4-argon", level: llm.ThinkingLevelOff, wantErr: "cannot disable"},
		{name: "Flash off rejected", model: "gemini-3-flash-preview", level: llm.ThinkingLevelOff, wantErr: "cannot disable"},
		{name: "raw off rejected", model: "gemini-4-argon", raw: "off", wantErr: "cannot disable"},
		{name: "2.5 raw effort maps to budget", model: "gemini-2.5-pro", raw: "high", wantBudget: ptr(16384)},
		{name: "2.5 request overrides raw service", model: "gemini-2.5-pro", serviceRaw: "high", level: llm.ThinkingLevelLow, wantBudget: ptr(2048)},
		{name: "2.5 Pro off rejected", model: "gemini-2.5-pro", level: llm.ThinkingLevelOff, wantErr: "cannot disable"},
		{name: "2.5 Flash off allowed", model: "gemini-2.5-flash", level: llm.ThinkingLevelOff, wantBudget: ptr(0)},
		{name: "2.5 resource name", model: "models/gemini-2.5-pro", level: llm.ThinkingLevelMedium, wantBudget: ptr(8192)},
		{name: "unknown generic controls rejected", model: "custom-unknown", level: llm.ThinkingLevelHigh, wantErr: "unknown thinking controls"},
		{name: "other provider controls not inherited", model: "gpt-5.6-sol", level: llm.ThinkingLevelHigh, wantErr: "unknown thinking controls"},
		{name: "unknown explicit wire effort", model: "custom-unknown", raw: "future", wantLevel: "future"},
		{name: "unlisted controls reject generic guesses", model: "gemini-2.5-flash-image", level: llm.ThinkingLevelHigh, wantErr: "unknown thinking controls"},
		{name: "unlisted controls explicit standard effort", model: "gemini-2.5-flash-image", raw: "high", wantLevel: "high"},
		{name: "unlisted controls explicit custom effort", model: "gemini-2.5-flash-image", raw: "future", wantLevel: "future"},
		{name: "nonreasoning model rejects thinking", model: "gemini-2.5-flash-preview-tts", level: llm.ThinkingLevelHigh, wantErr: "does not support thinking"},
		{name: "nonreasoning model rejects raw effort", model: "gemini-2.5-flash-preview-tts", raw: "high", wantErr: "does not support thinking"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{Model: tt.model, ThinkingLevel: tt.service, ReasoningEffort: tt.serviceRaw}
			request, err := service.buildGeminiRequest(&llm.Request{
				Messages: []llm.Message{llm.UserStringMessage("Hello")}, ThinkingLevel: tt.level, ReasoningEffort: tt.raw,
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc := request.GenerationConfig.ThinkingConfig
			if tc == nil {
				t.Fatal("thinking config omitted")
			}
			if tc.ThinkingLevel != tt.wantLevel {
				t.Errorf("thinking level = %q, want %q", tc.ThinkingLevel, tt.wantLevel)
			}
			if tt.wantBudget == nil {
				if tc.ThinkingBudget != nil {
					t.Errorf("unexpected legacy budget: %d", *tc.ThinkingBudget)
				}
			} else if tc.ThinkingBudget == nil || *tc.ThinkingBudget != *tt.wantBudget {
				t.Errorf("budget = %v, want %d", tc.ThinkingBudget, *tt.wantBudget)
			}
			if want := tt.wantBudget == nil || *tt.wantBudget != 0; tc.IncludeThoughts != want {
				t.Errorf("includeThoughts = %v, want %v", tc.IncludeThoughts, want)
			}
			body, err := json.Marshal(tc)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "thinkingLevel") && strings.Contains(string(body), "thinkingBudget") {
				t.Fatalf("mutually exclusive controls sent together: %s", body)
			}
		})
	}
}

func TestArgonStreamingOutputLimit(t *testing.T) {
	for _, model := range []string{"gemini-4-argon", "models/gemini-4-argon", "gemini-4-argon-2026-11-06"} {
		for _, endpoint := range []string{"", "https://generativelanguage.googleapis.com/v1beta", "https://gateway.example/v1beta", "https://openrouter.ai/api/v1"} {
			for _, maxTokens := range []int{0, 4096, 200000} {
				t.Run(model+"/"+endpoint+"/"+strconv.Itoa(maxTokens), func(t *testing.T) {
					service := &Service{Model: model, URL: endpoint, MaxTokens: maxTokens}
					request, err := service.buildGeminiRequest(&llm.Request{Messages: []llm.Message{llm.UserStringMessage("Hello")}})
					if err != nil {
						t.Fatal(err)
					}
					want := 65536
					if maxTokens > 0 && maxTokens < want {
						want = maxTokens
					}
					if got := request.GenerationConfig.MaxOutputTokens; got != want {
						t.Errorf("maxOutputTokens = %d, want streaming per-request cap %d", got, want)
					}
				})
			}
		}
	}
}

func TestNegativeOutputLimitRejected(t *testing.T) {
	_, err := (&Service{MaxTokens: -1}).buildGeminiRequest(&llm.Request{Messages: []llm.Message{llm.UserStringMessage("Hello")}})
	if err == nil || !strings.Contains(err.Error(), "max output tokens") {
		t.Fatalf("negative output limit error = %v", err)
	}
}

func TestUnknownThinkingControlsNotAdvertised(t *testing.T) {
	for _, model := range []string{"custom-unknown", "gemini-2.5-flash-image"} {
		service := &Service{Model: model}
		if service.SupportsReasoning() || len(service.SupportedReasoningLevels()) != 0 {
			t.Errorf("%s advertises unknown generic thinking controls", model)
		}
	}
}
