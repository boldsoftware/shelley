package db

import (
	"context"
	"slices"
	"testing"
)

// TestConsumeResumeAfterUpgrade covers the one-shot resume flag: absent (normal
// restart, stale flags cleared), present (upgrade restart, flags preserved and
// IDs returned), and the second call after a consume (back to the normal path).
func TestConsumeResumeAfterUpgrade(t *testing.T) {
	tests := []struct {
		name        string
		setFlag     bool
		consumeCall int // number of times to call, result of the last call is asserted
		wantIDs     bool
		wantWorking bool
	}{
		{name: "flag absent clears stale flags", setFlag: false, consumeCall: 1},
		{name: "flag present returns ids and preserves flags", setFlag: true, consumeCall: 1, wantIDs: true, wantWorking: true},
		{name: "second call after consume takes normal path", setFlag: true, consumeCall: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := NewTestDB(t)
			defer cleanup()
			ctx := context.Background()

			working, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
			if err != nil {
				t.Fatalf("CreateConversation: %v", err)
			}
			idle, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
			if err != nil {
				t.Fatalf("CreateConversation: %v", err)
			}
			if err := database.SetConversationAgentWorking(ctx, working.ConversationID, true); err != nil {
				t.Fatalf("SetConversationAgentWorking: %v", err)
			}

			if tt.setFlag {
				if err := database.SetSetting(ctx, ResumeAfterUpgradeSettingKey, "1"); err != nil {
					t.Fatalf("SetSetting: %v", err)
				}
			}

			var ids []string
			for range tt.consumeCall {
				ids, err = database.ConsumeResumeAfterUpgrade(ctx)
				if err != nil {
					t.Fatalf("ConsumeResumeAfterUpgrade: %v", err)
				}
			}

			wantIDs := []string(nil)
			if tt.wantIDs {
				wantIDs = []string{working.ConversationID}
			}
			if !slices.Equal(ids, wantIDs) {
				t.Errorf("ids = %v, want %v", ids, wantIDs)
			}

			// The flag row must always be gone after a consume.
			if v, err := database.GetSetting(ctx, ResumeAfterUpgradeSettingKey); err != nil || v != "" {
				t.Errorf("flag row after consume = %q, %v; want empty", v, err)
			}

			got, err := database.GetConversationByID(ctx, working.ConversationID)
			if err != nil {
				t.Fatalf("GetConversationByID: %v", err)
			}
			if got.AgentWorking != tt.wantWorking {
				t.Errorf("agent_working = %v, want %v", got.AgentWorking, tt.wantWorking)
			}
			idleGot, err := database.GetConversationByID(ctx, idle.ConversationID)
			if err != nil {
				t.Fatalf("GetConversationByID: %v", err)
			}
			if idleGot.AgentWorking {
				t.Error("idle conversation should never be marked working")
			}
		})
	}
}
