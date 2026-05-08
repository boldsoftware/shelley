package server

import (
	"encoding/json"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

func mkConv(id, slug string, working bool) ConversationWithState {
	s := slug
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return ConversationWithState{
		Conversation: generated.Conversation{
			ConversationID:      id,
			Slug:                &s,
			UserInitiated:       true,
			CreatedAt:           now,
			UpdatedAt:           now,
			ConversationOptions: "{}",
		},
		Working: working,
	}
}

func TestComputeListPatchAdd(t *testing.T) {
	ops, err := computeListPatch(nil, []ConversationWithState{mkConv("a", "alpha", false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "add" || ops[0].Path != "/0" {
		t.Fatalf("unexpected ops: %+v", ops)
	}
	roundTrip(t, nil, []ConversationWithState{mkConv("a", "alpha", false)}, ops)
}

func TestComputeListPatchRemove(t *testing.T) {
	old := []ConversationWithState{mkConv("a", "alpha", false), mkConv("b", "beta", false)}
	ops, err := computeListPatch(old, []ConversationWithState{mkConv("a", "alpha", false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "remove" || ops[0].Path != "/1" {
		t.Fatalf("unexpected ops: %+v", ops)
	}
	roundTrip(t, old, []ConversationWithState{mkConv("a", "alpha", false)}, ops)
}

func TestComputeListPatchReplaceField(t *testing.T) {
	old := []ConversationWithState{mkConv("a", "alpha", false)}
	new := []ConversationWithState{mkConv("a", "alpha", true)}
	ops, err := computeListPatch(old, new)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "replace" || ops[0].Path != "/0/working" {
		t.Fatalf("unexpected ops: %+v", ops)
	}
	roundTrip(t, old, new, ops)
}

func TestComputeListPatchReorder(t *testing.T) {
	old := []ConversationWithState{
		mkConv("a", "alpha", false),
		mkConv("b", "beta", false),
		mkConv("c", "gamma", false),
	}
	new := []ConversationWithState{
		mkConv("b", "beta", false),
		mkConv("a", "alpha", false),
		mkConv("c", "gamma", false),
	}
	ops, err := computeListPatch(old, new)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, old, new, ops)
}

func TestComputeListPatchIdentity(t *testing.T) {
	old := []ConversationWithState{mkConv("a", "alpha", false)}
	ops, err := computeListPatch(old, old)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("expected no ops, got %+v", ops)
	}
}

func TestComputeListPatchAddRemoveReplaceCombo(t *testing.T) {
	old := []ConversationWithState{
		mkConv("a", "alpha", false),
		mkConv("b", "beta", false),
		mkConv("c", "gamma", false),
	}
	new := []ConversationWithState{
		mkConv("d", "delta", false),
		mkConv("c", "gamma-renamed", false),
		mkConv("a", "alpha", true),
	}
	ops, err := computeListPatch(old, new)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, old, new, ops)
}

func roundTrip(t *testing.T, old, new []ConversationWithState, ops []conversationListPatchOp) {
	t.Helper()
	got, err := applyTestPatch(old, ops)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(new)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("apply mismatch:\n got=%s\nwant=%s\n ops=%+v", gotJSON, wantJSON, ops)
	}
}
