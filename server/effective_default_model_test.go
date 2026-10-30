package server

import (
	"testing"

	"shelley.exe.dev/models"
)

// ifactory regression: the configured defaultModel isn't ready on
// this host (no API key), so falling back to it blindly returned
// 400 "Unsupported model" for every empty-model send. The web UI
// dodged it by precomputing a ready id at page load; iOS/CLI
// clients hit it. effectiveDefaultModel centralizes the fallback.
func TestEffectiveDefaultModelPrefersConfiguredWhenReady(t *testing.T) {
	s := &Server{defaultModel: "claude-opus-4.7"}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "claude-sonnet-4.6", Ready: true, Tier: models.Tier1},
		{ID: "claude-opus-4.7", Ready: true, Tier: models.Tier2},
	})
	if got != "claude-opus-4.7" {
		t.Errorf("got %q, want claude-opus-4.7", got)
	}
}

func TestEffectiveDefaultModelPrefersFirstReadyTier1(t *testing.T) {
	for _, configured := range []string{"", "unavailable"} {
		t.Run("configured="+configured, func(t *testing.T) {
			s := &Server{defaultModel: configured}
			list := []ModelInfo{
				{ID: "gpt-5.6-sol", Ready: true},
				{ID: "gpt-6-astra", Ready: false},
				{ID: "gpt-6.1-sol", Ready: true},
				{ID: "gpt-6.1-terra", Ready: true},
			}
			assignModelTiers(list)
			if got := s.effectiveDefaultModel(list); got != "gpt-6.1-sol" {
				t.Errorf("got %q, want gpt-6.1-sol", got)
			}
		})
	}
}

func TestEffectiveDefaultModelUsesFirstReadyWhenOnlyTier2Available(t *testing.T) {
	s := &Server{}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "unavailable", Ready: false, Tier: models.Tier1},
		{ID: "first-ready", Ready: true, Tier: models.Tier2},
		{ID: "second-ready", Ready: true, Tier: models.Tier2},
	})
	if got != "first-ready" {
		t.Errorf("got %q, want first-ready", got)
	}
}

func TestEffectiveDefaultModelFallsBackWhenConfiguredNotReady(t *testing.T) {
	// The ifactory scenario.
	s := &Server{defaultModel: "claude-opus-4.7"}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "claude-opus-4.7", Ready: false},
		{ID: "claude-sonnet-4.6", Ready: true},
		{ID: "gpt-5.3", Ready: true},
	})
	if got != "claude-sonnet-4.6" {
		t.Errorf("got %q, want claude-sonnet-4.6 (first ready)", got)
	}
}

func TestEffectiveDefaultModelUsesFirstReadyWhenNotConfigured(t *testing.T) {
	s := &Server{defaultModel: ""}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "some-other-model", Ready: true},
		{ID: models.Default().ID, Ready: true},
	})
	if got != "some-other-model" {
		t.Errorf("got %q, want some-other-model", got)
	}
}

func TestEffectiveDefaultModelSkipsFirstModelWhenNotReady(t *testing.T) {
	s := &Server{defaultModel: ""}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "not-ready", Ready: false},
		{ID: "some-fake-id", Ready: true},
	})
	if got != "some-fake-id" {
		t.Errorf("got %q, want some-fake-id", got)
	}
}

func TestEffectiveDefaultModelConfiguredNotReadyUsesFirstReady(t *testing.T) {
	s := &Server{defaultModel: "configured-not-ready"}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "configured-not-ready", Ready: false},
		{ID: "first-ready", Ready: true},
		{ID: models.Default().ID, Ready: true},
	})
	if got != "first-ready" {
		t.Errorf("got %q, want first-ready", got)
	}
}

func TestEffectiveDefaultModelEmptyListReturnsEmpty(t *testing.T) {
	s := &Server{defaultModel: "claude-opus-4.7"}
	if got := s.effectiveDefaultModel(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestEffectiveDefaultModelNoReadyReturnsEmpty(t *testing.T) {
	s := &Server{defaultModel: "claude-opus-4.7"}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "claude-opus-4.7", Ready: false},
		{ID: "claude-sonnet-4.6", Ready: false},
	})
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMarkDefaultModel(t *testing.T) {
	list := []ModelInfo{
		{ID: "a", Ready: true},
		{ID: "b", Ready: true},
	}
	markDefaultModel(list, "b")
	if list[0].IsDefault {
		t.Error("a should not be default")
	}
	if !list[1].IsDefault {
		t.Error("b should be default")
	}
}

func TestMarkDefaultModelEmptyID(t *testing.T) {
	list := []ModelInfo{{ID: "a", Ready: true}}
	markDefaultModel(list, "")
	if list[0].IsDefault {
		t.Error("no model should be marked default when defaultID is empty")
	}
}

func TestMarkDefaultModelUnknownID(t *testing.T) {
	list := []ModelInfo{{ID: "a", Ready: true}}
	markDefaultModel(list, "z")
	if list[0].IsDefault {
		t.Error("no model should be marked default when defaultID is unknown")
	}
}
