package models

import (
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models/modelsdev"
)

func addWorkhorseRegressionRoute(t *testing.T, manager *Manager, protocol, wire string, saved bool) string {
	t.Helper()
	const endpoint = "https://openrouter.int.exe.xyz/api/v1"
	var entry serviceEntry
	if saved {
		var err error
		entry, err = manager.providerEntry(db.ModelProvider{
			ProviderID: "gateway", ProviderType: protocol, Endpoint: endpoint,
		}, wire)
		if err != nil {
			t.Fatal(err)
		}
		if entry.provider != ProviderOpenAI || entry.sourceID != "gateway" {
			t.Fatalf("saved gateway metadata = (%q, %q), want (openai, gateway)", entry.provider, entry.sourceID)
		}
	} else {
		entry = manager.customModelEntry(generated.Model{
			ModelID: wire, ModelName: wire, ProviderType: protocol, Endpoint: endpoint,
		})
	}
	if entry.service == nil {
		t.Fatal("route has no service")
	}
	manager.services[entry.modelID] = entry
	manager.modelOrder = append(manager.modelOrder, entry.modelID)
	return entry.modelID
}

func assertWorkhorseRegressionSelection(t *testing.T, manager *Manager, conversation, want string) {
	t.Helper()
	if got := manager.GetWorkhorseModelID(conversation); got != want {
		t.Errorf("GetWorkhorseModelID(%q) = %q, want %q", conversation, got, want)
	}
	service, err := manager.GetWorkhorseServiceNoFallback(conversation)
	if err != nil {
		t.Fatal(err)
	}
	logged, ok := service.(*loggingService)
	if !ok || logged.modelID != want {
		t.Fatalf("strict service = %#v, want route %q", service, want)
	}
}

func addWorkhorseSavedRegressionRoute(t *testing.T, manager *Manager, provider db.ModelProvider, wire string) string {
	t.Helper()
	entry, err := manager.providerEntry(provider, wire)
	if err != nil {
		t.Fatal(err)
	}
	if entry.service == nil || entry.provider != ProviderOpenAI || entry.sourceID != provider.ProviderID ||
		entry.baseURL != provider.Endpoint || entry.apiModelName != wire {
		t.Fatalf("saved route metadata does not preserve connection and wire name: %+v", entry)
	}
	manager.services[entry.modelID] = entry
	manager.modelOrder = append(manager.modelOrder, entry.modelID)
	return entry.modelID
}

func TestWorkhorseSavedRouteFamilyCompetition(t *testing.T) {
	const builtIn = "claude-haiku-5.5"
	for _, protocol := range []string{"openai", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			for _, tc := range []struct {
				name, sourceID, endpoint, conversation, want string
				candidates                                   []string
				builtInWins                                  bool
			}{
				{
					name: "OpenRouter o3 matches OpenAI family", sourceID: "openrouter",
					endpoint:     "https://openrouter.ai/api/v1",
					conversation: "openai/o3",
					candidates:   []string{"openai/gpt-6-luna"},
					want:         "openai/gpt-6-luna",
				},
				{
					name: "OpenRouter o1 matches OpenAI family", sourceID: "openrouter",
					endpoint:     "https://openrouter.ai/api/v1",
					conversation: "openai/o1",
					candidates:   []string{"openai/gpt-6-luna"},
					want:         "openai/gpt-6-luna",
				},
				{
					name: "Requesty o4-mini preserves region", sourceID: "requesty",
					endpoint:     "https://router.requesty.ai/v1",
					conversation: "o4-mini@eu",
					candidates:   []string{"gpt-6-luna@eu"},
					want:         "gpt-6-luna@eu",
				},
				{
					name: "Azure codex-mini matches OpenAI family", sourceID: "azure",
					endpoint:     "https://example.openai.azure.com/openai/v1",
					conversation: "codex-mini",
					candidates:   []string{"gpt-6-luna"},
					want:         "gpt-6-luna",
				},
				{
					name: "Fireworks namespace is not a family", sourceID: "fireworks",
					endpoint:     "https://api.fireworks.ai/inference/v1",
					conversation: "accounts/fireworks/models/kimi-k3",
					candidates:   []string{"accounts/fireworks/models/deepseek-v4p1-flash-1017"},
					want:         "accounts/fireworks/models/deepseek-v4p1-flash-1017",
					builtInWins:  true,
				},
				{
					name: "Requesty same family beats protocol", sourceID: "requesty",
					endpoint:     "https://router.requesty.ai/v1",
					conversation: "claude-opus-5.5@eu",
					candidates:   []string{"gpt-6-luna@eu", "claude-haiku-5.5@eu"},
					want:         "claude-haiku-5.5@eu",
				},
				{
					name: "Requesty unrelated family follows builtin", sourceID: "requesty",
					endpoint:     "https://router.requesty.ai/v1",
					conversation: "claude-opus-5.5@eu",
					candidates:   []string{"gpt-6-luna@eu"},
					want:         "gpt-6-luna@eu",
					builtInWins:  true,
				},
				{
					name: "Bedrock family matches unqualified mixed case", sourceID: "requesty",
					endpoint:     "https://router.requesty.ai/v1",
					conversation: "us.anthropic.claude-opus-5-5",
					candidates:   []string{"gpt-6-luna@eu", "CLAUDE-HAIKU-5.5@eu"},
					want:         "CLAUDE-HAIKU-5.5@eu",
				},
				{
					name: "Mixed case family matches Bedrock", sourceID: "requesty",
					endpoint:     "https://router.requesty.ai/v1",
					conversation: "CLAUDE-OPUS-5.5@eu",
					candidates:   []string{"gpt-6-luna@eu", "us.anthropic.claude-haiku-5-5"},
					want:         "us.anthropic.claude-haiku-5-5",
				},
				{
					name: "SiliconFlow preserves mixed case", sourceID: "siliconflow",
					endpoint:     "https://api.siliconflow.cn/v1",
					conversation: "deepseek-ai/DeepSeek-V4-Pro",
					candidates:   []string{"deepseek-ai/DeepSeek-V4-Flash"},
					want:         "deepseek-ai/DeepSeek-V4-Flash",
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, withBuiltIn := range []bool{true, false} {
						name := "without builtin"
						var built []Built
						if withBuiltIn {
							name = "with builtin"
							built = []Built{{
								ID: builtIn, Provider: ProviderAnthropic,
								Service: &recordingService{provider: "anthropic"},
							}}
						}
						t.Run(name, func(t *testing.T) {
							manager := newWorkhorseManager(t, built...)
							provider := db.ModelProvider{
								ProviderID: tc.sourceID, ProviderType: protocol, Endpoint: tc.endpoint,
							}
							conversation := addWorkhorseSavedRegressionRoute(t, manager, provider, tc.conversation)
							for _, wire := range tc.candidates {
								addWorkhorseSavedRegressionRoute(t, manager, provider, wire)
							}
							want := ProviderRouteID(tc.sourceID, tc.want)
							if withBuiltIn && tc.builtInWins {
								want = builtIn
							}
							assertWorkhorseRegressionSelection(t, manager, conversation, want)
						})
					}
				})
			}
		})
	}
}

func TestWorkhorseRealGatewayRoutes(t *testing.T) {
	for _, route := range []struct {
		name, protocol string
		saved          bool
	}{
		{"saved chat", "openai", true},
		{"saved responses", "openai-responses", true},
		{"legacy custom chat", "openai", false},
		{"legacy custom responses", "openai-responses", false},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, conversation, want string
				candidates               []string
			}{
				{
					name: "same vendor hydrogen", conversation: "google/gemini-4-neon",
					want:       "google/gemini-4-hydrogen",
					candidates: []string{"anthropic/claude-haiku-5.5", "google/gemini-4-hydrogen"},
				},
				{
					name: "gateway only fallback", conversation: "moonshotai/kimi-k3.1-pro",
					want:       "google/gemini-4-hydrogen",
					candidates: []string{"google/gemini-4-hydrogen"},
				},
				{
					name: "same vendor haiku", conversation: "anthropic/claude-opus-5",
					want:       "anthropic/claude-haiku-5.5",
					candidates: []string{"google/gemini-4-hydrogen", "anthropic/claude-haiku-5.5"},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					manager := newWorkhorseManager(t)
					conversation := addWorkhorseRegressionRoute(t, manager, route.protocol, tc.conversation, route.saved)
					var want string
					for _, wire := range tc.candidates {
						id := addWorkhorseRegressionRoute(t, manager, route.protocol, wire, route.saved)
						if wire == tc.want {
							want = id
						}
					}
					assertWorkhorseRegressionSelection(t, manager, conversation, want)
				})
			}
		})
	}
}

func TestWorkhorseLegacyCustomDoesNotOutrankBuiltIn(t *testing.T) {
	manager := newWorkhorseManager(t,
		Built{ID: "grok-4.5", Provider: ProviderXAI},
		Built{ID: "gpt-6-luna", Provider: ProviderOpenAI, Service: &recordingService{provider: "openai"}},
	)
	entry := manager.customModelEntry(generated.Model{
		ModelID: "custom-haiku", ModelName: "claude-haiku-5.5",
		ProviderType: "anthropic", Endpoint: "https://gateway.example/v1/messages",
	})
	if entry.sourceID != "" || entry.source != SourceCustomLabel || entry.service == nil {
		t.Fatalf("unexpected legacy custom metadata: %+v", entry)
	}
	manager.services[entry.modelID] = entry
	manager.modelOrder = append(manager.modelOrder, entry.modelID)
	assertWorkhorseRegressionSelection(t, manager, "grok-4.5", "gpt-6-luna")
}

func TestWorkhorseUnlistedSavedConversationQualifies(t *testing.T) {
	manager := newWorkhorseManager(t)
	entry, err := manager.providerEntry(db.ModelProvider{
		ProviderID: "anthropic", ProviderType: "anthropic", Endpoint: "https://api.anthropic.com/v1",
	}, "claude-haiku-5.5")
	if err != nil {
		t.Fatal(err)
	}
	manager.services[entry.modelID] = entry
	manager.modelOrder = append(manager.modelOrder, entry.modelID)
	manager.modelOrder = nil
	if len(manager.GetAvailableModels()) != 0 {
		t.Fatal("conversation route is still in the picker")
	}
	assertWorkhorseRegressionSelection(t, manager, entry.modelID, entry.modelID)
}

func TestWorkhorseGatewayLunarisIsNotLuna(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			manager := newWorkhorseManager(t)
			conversation := addWorkhorseRegressionRoute(t, manager, protocol, "moonshotai/kimi-k3.1-pro", true)
			unqualified := addWorkhorseRegressionRoute(t, manager, protocol, "gpt-6-sol", true)
			lunaris := addWorkhorseRegressionRoute(t, manager, protocol, "sao10k/l3-lunaris-8b", true)
			for _, id := range []string{conversation, unqualified, lunaris} {
				if got := manager.GetWorkhorseModelID(id); got != id {
					t.Errorf("GetWorkhorseModelID(%q) = %q, want conversation unchanged", id, got)
				}
				if _, err := manager.GetWorkhorseServiceNoFallback(id); err == nil {
					t.Errorf("strict selection accepted Lunaris as a workhorse for %q", id)
				}
			}
		})
	}
}

func TestWorkhorseLegacyResponsesNames(t *testing.T) {
	t.Run("unqualified Lunaris is not Luna", func(t *testing.T) {
		manager := newWorkhorseManager(t)
		id := addWorkhorseRegressionRoute(t, manager, "openai-responses", "l3-lunaris-8b", false)
		if got := manager.GetWorkhorseModelID(id); got != id {
			t.Errorf("GetWorkhorseModelID(%q) = %q, want conversation unchanged", id, got)
		}
		if _, err := manager.GetWorkhorseServiceNoFallback(id); err == nil {
			t.Fatal("strict selection accepted unqualified Lunaris as a workhorse")
		}
	})
	t.Run("Fireworks DeepSeek path qualifies", func(t *testing.T) {
		manager := newWorkhorseManager(t)
		id := addWorkhorseRegressionRoute(t, manager, "openai-responses", "accounts/fireworks/models/deepseek-v4p1-flash-1017", false)
		assertWorkhorseRegressionSelection(t, manager, id, id)
	})
}

func TestWorkhorseGatewayQualifiedNames(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, wire string
		saved                bool
	}{
		{"Requesty chat region", "openai", "gpt-6-luna@eu", true},
		{"Requesty responses region", "openai-responses", "gpt-6-luna@eu", true},
		{"DigitalOcean legacy responses", "openai-responses", "anthropic-claude-haiku-4.5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newWorkhorseManager(t)
			id := addWorkhorseRegressionRoute(t, manager, tc.protocol, tc.wire, tc.saved)
			assertWorkhorseRegressionSelection(t, manager, id, id)
		})
	}
}

func TestWorkhorseSavedQwenASRDoesNotQualify(t *testing.T) {
	for _, protocol := range []string{"openai", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			manager := newWorkhorseManager(t)
			conversation := addWorkhorseRegressionRoute(t, manager, protocol, "qwen3-max", true)
			asr := addWorkhorseRegressionRoute(t, manager, protocol, "qwen3-asr-flash", true)
			for _, id := range []string{conversation, asr} {
				if got := manager.GetWorkhorseModelID(id); got != id {
					t.Errorf("GetWorkhorseModelID(%q) = %q, want conversation unchanged", id, got)
				}
				if _, err := manager.GetWorkhorseServiceNoFallback(id); err == nil {
					t.Errorf("strict selection accepted Qwen ASR as a workhorse for %q", id)
				}
			}
		})
	}
}

func TestWorkhorseGatewayVendorAliasesPreferSameEndpoint(t *testing.T) {
	for _, route := range []struct {
		name, protocol string
		saved          bool
	}{
		{"saved chat", "openai", true},
		{"saved responses", "openai-responses", true},
		{"legacy custom responses", "openai-responses", false},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, conversation, workhorse string
			}{
				{"DeepSeek", "deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4-flash"},
				{"Gemini", "gemini/gemini-2.5-pro", "gemini/gemini-2.5-flash"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					manager := newWorkhorseManager(t, Built{
						ID: "claude-haiku-5.5", Provider: ProviderAnthropic,
						Service: &recordingService{provider: "anthropic"},
					})
					conversation := addWorkhorseRegressionRoute(t, manager, route.protocol, tc.conversation, route.saved)
					workhorse := addWorkhorseRegressionRoute(t, manager, route.protocol, tc.workhorse, route.saved)
					assertWorkhorseRegressionSelection(t, manager, conversation, workhorse)
				})
			}
		})
	}
}

func TestClassifyWorkhorseModelGatewayNames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider Provider
		want     bool
	}{
		{"openai/gpt-6-luna", ProviderOpenAI, true},
		{"o1", ProviderOpenAI, false},
		{"o1-2024-12-17", ProviderOpenAI, false},
		{"o1@eu", ProviderOpenAI, false},
		{"o3", ProviderOpenAI, false},
		{"o3-2025-04-16", ProviderOpenAI, false},
		{"o3@eu", ProviderOpenAI, false},
		{"openai/o3", ProviderOpenAI, false},
		{"o4-mini", ProviderOpenAI, false},
		{"o4-mini-2025-04-16", ProviderOpenAI, false},
		{"o4-mini@eu", ProviderOpenAI, false},
		{"codex-mini", ProviderOpenAI, false},
		{"codex-mini-latest", ProviderOpenAI, false},
		{"codex-mini-2025-05-16", ProviderOpenAI, false},
		{"codex-mini@eu", ProviderOpenAI, false},
		{"azure/CODEX-MINI@eu", ProviderOpenAI, false},
		{"oracle", "", false},
		{"olmo-3", "", false},
		{"o3ish", "", false},
		{"codex-minimum", "", false},
		{"openai/oracle", "", false},
		{"anthropic/claude-haiku-5.5", ProviderAnthropic, true},
		{"google/gemini-4-hydrogen", ProviderGemini, true},
		{"deepseek-ai/deepseek-v4-flash", ProviderFireworks, true},
		{"deepseek-ai/DeepSeek-V4-Flash", ProviderFireworks, true},
		{"gemini/gemini-2.5-flash", ProviderGemini, true},
		{"accounts/fireworks/models/deepseek-v4p1-flash-1017", ProviderFireworks, true},
		{"gpt-6-luna@eu", ProviderOpenAI, true},
		{"anthropic-claude-haiku-4.5", ProviderAnthropic, true},
		{"us.anthropic.claude-haiku-5-5", ProviderAnthropic, true},
		{"CLAUDE-HAIKU-5.5@eu", ProviderAnthropic, true},
		{"claude-opus-5.5@eu", ProviderAnthropic, false},
		{"CLAUDE-OPUS-5.5@eu", ProviderAnthropic, false},
		{"us.anthropic.claude-opus-5-5", ProviderAnthropic, false},
		{"anthropic/gpt-6-sol", ProviderOpenAI, false},
		{"google/gemini-4-neon", ProviderGemini, false},
		{"accounts/fireworks/models/kimi-k3", "", false},
		{"qwen3-asr-flash", "", false},
		{"qwen3-flash", "", false},
		{"sao10k/l3-lunaris-8b", "", false},
		{"lunaris", "", false},
		{"deepseek-ai/deepseek-v4-pro", ProviderFireworks, false},
		{"deepseek-ai/DeepSeek-V4-Pro", ProviderFireworks, false},
		{"gemini/gemini-2.5-flash-lite", ProviderGemini, false},
		{"gemini/gemini-2.5-flash-image", ProviderGemini, false},
		{"gemini/gemini-2.5-flash-tts", ProviderGemini, false},
		{"gemini/gemini-2.5-flash-live", ProviderGemini, false},
		{"gemini-proxy/qwen3-asr-flash", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, got := classifyWorkhorseModel(tc.name)
			if provider != tc.provider || got != tc.want {
				t.Errorf("classifyWorkhorseModel(%q) = (%q, %v), want (%q, %v)", tc.name, provider, got, tc.provider, tc.want)
			}
		})
	}
}

func TestWorkhorseBedrockNamedAnthropicGateway(t *testing.T) {
	manager := newWorkhorseManager(t)
	var workhorse string
	for _, wire := range []string{"us.anthropic.claude-opus-5-5", "us.anthropic.claude-haiku-5-5"} {
		entry, err := manager.providerEntry(db.ModelProvider{
			ProviderID: "anthropic-gateway", ProviderType: "anthropic", Endpoint: "https://gateway.example/v1",
		}, wire)
		if err != nil {
			t.Fatal(err)
		}
		manager.services[entry.modelID] = entry
		manager.modelOrder = append(manager.modelOrder, entry.modelID)
		workhorse = entry.modelID
	}
	assertWorkhorseRegressionSelection(t, manager,
		ProviderRouteID("anthropic-gateway", "us.anthropic.claude-opus-5-5"), workhorse)
	assertWorkhorseRegressionSelection(t, manager, workhorse, workhorse)
}

func TestWorkhorseTextModalities(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, wire string
		known, allowed       bool
	}{
		{"audio input", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "qwen3-asr-flash", true, false},
		{"audio output", DefaultGeminiBaseURL, "gemini-3.1-flash-tts-preview", true, false},
		{"image output", "https://api.qnaigc.com/v1", "gemini-2.5-flash-image", true, false},
		{"text model", DefaultGeminiBaseURL, "gemini-2.5-flash", true, true},
		{"unknown model", "https://private.example/v1", "private-workhorse", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, known := modelsdev.LookupModalities(tc.endpoint, tc.wire); known != tc.known {
				t.Fatalf("catalog fixture known = %v, want %v", known, tc.known)
			}
			manager := newWorkhorseManager(t,
				Built{
					ID: "opaque-candidate", Provider: ProviderOpenAI, Source: SourceCustomLabel,
					BaseURL: tc.endpoint, APIModelName: tc.wire, ReleaseDate: "2026-10-01",
				},
				Built{ID: "private-fallback", Provider: ProviderOpenAI, ReleaseDate: "2026-01-01"},
			)
			want := "private-fallback"
			if tc.allowed {
				want = "opaque-candidate"
			}
			got := manager.bestWorkhorse(manager.GetAvailableModels(), func(string, *ModelInfo) bool { return true })
			if got != want {
				t.Errorf("bestWorkhorse = %q, want %q", got, want)
			}
		})
	}
}
