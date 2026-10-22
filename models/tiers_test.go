package models

import "testing"

func TestAssignTiers(t *testing.T) {
	t.Run("haiku 5.5 supersedes haiku 4.5", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-haiku-5.5", "claude-haiku-4.5"})
		if tiers["claude-haiku-5.5"] != Tier1 || tiers["claude-haiku-4.5"] != Tier2 {
			t.Fatalf("tiers = %v", tiers)
		}
	})
	t.Run("opus 5.5 shadows older Opus and Sonnet 5", func(t *testing.T) {
		ids := []string{"claude-opus-5.5", "claude-opus-5", "claude-opus-4.8", "claude-opus-4.7", "claude-sonnet-5"}
		tiers := AssignTiers(ids)
		if tiers["claude-opus-5.5"] != Tier1 {
			t.Errorf("opus-5.5 tier = %d, want %d", tiers["claude-opus-5.5"], Tier1)
		}
		for _, id := range ids[1:] {
			if tiers[id] != Tier2 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier2)
			}
		}
	})
	t.Run("opus 4.7 shadows opus 4.5", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-4.7", "claude-opus-4.5"})
		if tiers["claude-opus-4.7"] != Tier1 || tiers["claude-opus-4.5"] != Tier2 {
			t.Fatalf("tiers = %v", tiers)
		}
	})

	t.Run("sonnet 5.5 supersedes sonnet 5", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-sonnet-5.5", "claude-sonnet-5", "claude-sonnet-4.6"})
		if tiers["claude-sonnet-5.5"] != Tier1 || tiers["claude-sonnet-5"] != Tier2 || tiers["claude-sonnet-4.6"] != Tier2 {
			t.Fatalf("tiers = %v", tiers)
		}
	})

	t.Run("GPT-6.1 Sol shadows Sol 6 and older Sol releases", func(t *testing.T) {
		ids := []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-5.6-sol", "gpt-5.5", "gpt-5.4"}
		tiers := AssignTiers(ids)
		if tiers["gpt-6.1-sol"] != Tier1 {
			t.Errorf("gpt-6.1-sol tier = %d, want %d", tiers["gpt-6.1-sol"], Tier1)
		}
		for _, id := range ids[1:] {
			if tiers[id] != Tier2 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier2)
			}
		}
	})

	t.Run("GPT-6 Sol shadows its superseded lineage", func(t *testing.T) {
		ids := []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}
		tiers := AssignTiers(ids)
		for _, id := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-terra"} {
			if tiers[id] != Tier1 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier1)
			}
		}
		if tiers["gpt-5.6-sol"] != Tier2 {
			t.Errorf("gpt-5.6-sol tier = %d, want %d", tiers["gpt-5.6-sol"], Tier2)
		}
	})

	t.Run("Luna generations remain peers while subscription access lags", func(t *testing.T) {
		tiers := AssignTiers([]string{"gpt-6-luna", "gpt-5.6-luna"})
		for _, id := range []string{"gpt-6-luna", "gpt-5.6-luna"} {
			if tiers[id] != Tier1 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier1)
			}
		}
	})

	t.Run("GPT-6 variants directly shadow older models without intermediates", func(t *testing.T) {
		ids := []string{"gpt-6-sol", "gpt-5.5", "gpt-5.4", "gpt-6-luna", "gpt-5.4-nano", "gpt-5.3-codex", "claude-haiku-4.5"}
		tiers := AssignTiers(ids)
		for _, id := range []string{"gpt-6-sol", "gpt-6-luna"} {
			if tiers[id] != Tier1 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier1)
			}
		}
		for _, id := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-nano", "gpt-5.3-codex", "claude-haiku-4.5"} {
			if tiers[id] != Tier2 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier2)
			}
		}
	})

	t.Run("shadowed model drops to tier 2 when both present", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-4.8", "claude-opus-4.7"})
		if tiers["claude-opus-4.8"] != Tier1 {
			t.Errorf("opus-4.8 tier = %d, want %d", tiers["claude-opus-4.8"], Tier1)
		}
		if tiers["claude-opus-4.7"] != Tier2 {
			t.Errorf("opus-4.7 tier = %d, want %d", tiers["claude-opus-4.7"], Tier2)
		}
	})

	t.Run("opus 5 shadows opus 4.8", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-5", "claude-opus-4.8"})
		if tiers["claude-opus-5"] != Tier1 {
			t.Errorf("opus-5 tier = %d, want %d", tiers["claude-opus-5"], Tier1)
		}
		if tiers["claude-opus-4.8"] != Tier2 {
			t.Errorf("opus-4.8 tier = %d, want %d", tiers["claude-opus-4.8"], Tier2)
		}
	})

	t.Run("fable 5.1 shadows fable 5", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-fable-5.1", "claude-fable-5"})
		if tiers["claude-fable-5.1"] != Tier1 {
			t.Errorf("fable-5.1 tier = %d, want %d", tiers["claude-fable-5.1"], Tier1)
		}
		if tiers["claude-fable-5"] != Tier2 {
			t.Errorf("fable-5 tier = %d, want %d", tiers["claude-fable-5"], Tier2)
		}
	})

	t.Run("worse model stays tier 1 when better absent", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-4.7"})
		if tiers["claude-opus-4.7"] != Tier1 {
			t.Errorf("opus-4.7 tier = %d, want %d (no shadowing model present)", tiers["claude-opus-4.7"], Tier1)
		}
	})

	t.Run("unknown model defaults to tier 2", func(t *testing.T) {
		tiers := AssignTiers([]string{"some-brand-new-model"})
		if tiers["some-brand-new-model"] != Tier2 {
			t.Errorf("unknown tier = %d, want %d", tiers["some-brand-new-model"], Tier2)
		}
	})

	t.Run("known unshadowed model stays tier 1", func(t *testing.T) {
		tiers := AssignTiers([]string{"gpt-5.6-sol"})
		if tiers["gpt-5.6-sol"] != Tier1 {
			t.Errorf("known tier = %d, want %d", tiers["gpt-5.6-sol"], Tier1)
		}
	})

	t.Run("multiple shadows demote several models", func(t *testing.T) {
		avail := []string{"gpt-5.6-luna", "gpt-5.3-codex", "claude-haiku-4.5"}
		tiers := AssignTiers(avail)
		if tiers["gpt-5.6-luna"] != Tier1 {
			t.Errorf("luna tier = %d, want %d", tiers["gpt-5.6-luna"], Tier1)
		}
		for _, worse := range []string{"gpt-5.3-codex", "claude-haiku-4.5"} {
			if tiers[worse] != Tier2 {
				t.Errorf("%s tier = %d, want %d", worse, tiers[worse], Tier2)
			}
		}
	})

	t.Run("glm 5.3 shadows glm 5.2", func(t *testing.T) {
		tiers := AssignTiers([]string{"glm-5.3-fireworks", "glm-5.2-fireworks"})
		if tiers["glm-5.3-fireworks"] != Tier1 {
			t.Errorf("glm 5.3 tier = %d, want %d", tiers["glm-5.3-fireworks"], Tier1)
		}
		if tiers["glm-5.2-fireworks"] != Tier2 {
			t.Errorf("glm 5.2 tier = %d, want %d", tiers["glm-5.2-fireworks"], Tier2)
		}
	})

	t.Run("every input id is assigned a tier", func(t *testing.T) {
		avail := IDs()
		tiers := AssignTiers(avail)
		for _, id := range avail {
			if tiers[id] != Tier1 && tiers[id] != Tier2 {
				t.Errorf("%s tier = %d, want 1 or 2", id, tiers[id])
			}
		}
	})
}
