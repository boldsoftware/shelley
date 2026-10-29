package models

import "testing"

func TestBestUpgrade(t *testing.T) {
	ready := map[string]bool{}
	for _, id := range IDs() {
		ready[id] = true
	}
	want := map[string]string{
		"claude-opus-4.5":               "claude-opus-5.5/version/Opus",
		"claude-opus-4.7":               "claude-opus-5.5/version/Opus",
		"claude-opus-4.8":               "claude-opus-5.5/version/Opus",
		"claude-opus-5":                 "claude-opus-5.5/version/Opus",
		"claude-sonnet-4.5":             "claude-sonnet-5.5/version/Sonnet",
		"claude-sonnet-4.6":             "claude-sonnet-5.5/version/Sonnet",
		"claude-sonnet-5":               "claude-sonnet-5.5/version/Sonnet",
		"claude-haiku-4.5":              "claude-haiku-5.5/version/Haiku",
		"claude-fable-5":                "claude-fable-5.1/version/Fable",
		"gpt-5.6-sol":                   "gpt-6.1-sol/version/Sol",
		"gpt-6-sol":                     "gpt-6.1-sol/version/Sol",
		"gpt-5.6-terra":                 "gpt-6.1-terra/version/Terra",
		"gpt-5.5":                       "gpt-6.1-sol/generation/OpenAI",
		"gpt-5.4":                       "gpt-6.1-sol/generation/OpenAI",
		"gpt-5.3-codex":                 "gpt-6.1-sol/generation/OpenAI",
		"gpt-5.4-mini":                  "gpt-6.1-terra/generation/OpenAI",
		"gpt-5.4-nano":                  "gpt-6-luna/generation/OpenAI",
		"deepseek-v4.1-flash-fireworks": "deepseek-v4.1-flash-1017-fireworks/version/DeepSeek Flash",
	}
	for _, id := range IDs() {
		to, kind, line := BestUpgrade(id, ready)
		got := ""
		if to != "" {
			got = to + "/" + string(kind) + "/" + line
			if ByID(to).Provider != ByID(id).Provider {
				t.Errorf("%s -> %s crosses providers", id, to)
			}
		}
		if got != want[id] {
			t.Errorf("%s: got %q, want %q", id, got, want[id])
		}
	}
}

func TestBestUpgradeSkipsUnavailable(t *testing.T) {
	// 5.5 missing: stop at the newest ready model on the way.
	if to, _, _ := BestUpgrade("claude-opus-4.7", map[string]bool{"claude-opus-4.8": true, "claude-opus-5": true}); to != "claude-opus-5" {
		t.Fatalf("got %q", to)
	}
	// Nothing newer ready: no suggestion.
	if to, _, _ := BestUpgrade("claude-opus-4.8", map[string]bool{"claude-opus-4.7": true}); to != "" {
		t.Fatalf("got %q", to)
	}
	// Generation hop whose first target is missing still reaches a newer ready one.
	if to, kind, _ := BestUpgrade("gpt-5.5", map[string]bool{"gpt-6.1-sol": true}); to != "gpt-6.1-sol" || kind != UpgradeGeneration {
		t.Fatalf("got %q %q", to, kind)
	}
}

func TestBestUpgradeFallsBackPastMissingTarget(t *testing.T) {
	// 6.1 Terra is unavailable; the ready 5.6 Terra still beats the mini.
	if to, kind, line := BestUpgrade("gpt-5.4-mini", map[string]bool{"gpt-5.6-terra": true}); to != "gpt-5.6-terra" || kind != UpgradeGeneration || line != "OpenAI" {
		t.Fatalf("got %q %q %q", to, kind, line)
	}
	// Codex reaches the Sol line, not the Luna the tier list hides it behind.
	if to, _, _ := BestUpgrade("gpt-5.3-codex", map[string]bool{"gpt-6-luna": true, "gpt-6.1-sol": true}); to != "gpt-6.1-sol" {
		t.Fatalf("got %q", to)
	}
}
