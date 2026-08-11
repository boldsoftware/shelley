package claudetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeExperienceStore struct {
	saved       int
	checkpoints int
	dreams      int
}

func (f *fakeExperienceStore) SearchMemoriesJSON(context.Context, string, string, int) ([]byte, error) {
	return []byte(`[{"title":"known lesson"}]`), nil
}
func (f *fakeExperienceStore) SaveMemoryFields(context.Context, string, string, string, string, string, string, float64) (string, error) {
	f.saved++
	return "memory-1", nil
}
func (f *fakeExperienceStore) AppendCheckpointJSON(context.Context, string, string, string, []byte) (string, error) {
	f.checkpoints++
	return "checkpoint-1", nil
}
func (f *fakeExperienceStore) LatestCheckpointJSON(context.Context, string) ([]byte, error) {
	return []byte(`{"summary":"resume here"}`), nil
}
func (f *fakeExperienceStore) ConsolidateDreamJSON(context.Context, string, string, string, []byte) (int, error) {
	f.dreams++
	return 0, nil
}

func TestExperienceToolsSaveCheckpointAndDream(t *testing.T) {
	store := &fakeExperienceStore{}
	tools := &ExperienceTools{Store: store, ConversationID: "conversation", WorkingDir: NewMutableWorkingDir(t.TempDir())}
	ctx := context.Background()

	memory := tools.MemoryTool().Run(ctx, json.RawMessage(`{"action":"save","scope":"project","kind":"lesson","title":"Build","content":"Run focused tests","confidence":0.9}`))
	if memory.Error != nil || store.saved != 1 {
		t.Fatalf("save memory: error=%v saved=%d", memory.Error, store.saved)
	}
	checkpoint := tools.JournalTool().Run(ctx, json.RawMessage(`{"action":"checkpoint","summary":"implemented","state":{"goal":"ship","next":"test"}}`))
	if checkpoint.Error != nil || store.checkpoints != 1 {
		t.Fatalf("checkpoint: error=%v writes=%d", checkpoint.Error, store.checkpoints)
	}
	dream := tools.DreamTool().Run(ctx, json.RawMessage(`{"summary":"verified","memories":[]}`))
	if dream.Error != nil || store.dreams != 1 {
		t.Fatalf("dream: error=%v runs=%d", dream.Error, store.dreams)
	}
}

func TestExperienceToolsRejectSecrets(t *testing.T) {
	store := &fakeExperienceStore{}
	tools := &ExperienceTools{Store: store, ConversationID: "conversation", WorkingDir: NewMutableWorkingDir(t.TempDir())}
	out := tools.MemoryTool().Run(context.Background(), json.RawMessage(`{"action":"save","scope":"project","kind":"fact","title":"credential","content":"api_key=do-not-store","confidence":1}`))
	if out.Error == nil || !strings.Contains(out.Error.Error(), "credential") || store.saved != 0 {
		t.Fatalf("secret was not rejected: error=%v saved=%d", out.Error, store.saved)
	}
}
