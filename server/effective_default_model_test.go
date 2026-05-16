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
		{ID: "claude-opus-4.7", Ready: true},
		{ID: "claude-sonnet-4.6", Ready: true},
	})
	if got != "claude-opus-4.7" {
		t.Errorf("got %q, want claude-opus-4.7", got)
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

func TestEffectiveDefaultModelEmptyConfiguredUsesProcessDefault(t *testing.T) {
	s := &Server{defaultModel: ""}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: models.Default().ID, Ready: true},
		{ID: "some-other-model", Ready: true},
	})
	if got != models.Default().ID {
		t.Errorf("got %q, want %q", got, models.Default().ID)
	}
}

func TestEffectiveDefaultModelEmptyConfiguredProcessDefaultNotInCatalog(t *testing.T) {
	// When s.defaultModel is "" and models.Default().ID isn't in
	// the catalog, fall through to first ready.
	s := &Server{defaultModel: ""}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "some-fake-id", Ready: true},
	})
	if got != "some-fake-id" {
		t.Errorf("got %q, want some-fake-id", got)
	}
}

func TestEffectiveDefaultModelConfiguredNotReadyFallsBackToProcessDefault(t *testing.T) {
	// Configured default isn't ready, but the process default is —
	// prefer the process default over arbitrary first-ready.
	s := &Server{defaultModel: "configured-not-ready"}
	got := s.effectiveDefaultModel([]ModelInfo{
		{ID: "configured-not-ready", Ready: false},
		{ID: "first-ready", Ready: true},
		{ID: models.Default().ID, Ready: true},
	})
	if got != models.Default().ID {
		t.Errorf("got %q, want %q", got, models.Default().ID)
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
