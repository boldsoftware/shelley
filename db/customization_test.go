package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

// ensureTestWorkspace turns workspace mode on and creates the workspace (the
// Assistant and the Shelley project), returning the Assistant's id.
func ensureTestWorkspace(t *testing.T, database *DB) string {
	t.Helper()
	EnableWorkspaceMode(t, database)
	id, err := database.EnsureWorkspace(t.Context(), t.TempDir(), "predictable", ConversationOptions{ThinkingLevel: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCustomizationReservationAndGeneration(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ensureTestWorkspace(t, database)
	id := newDBTestConversation(t, database, "Shelley", t.TempDir())
	other := newDBTestConversation(t, database, "Shelley", t.TempDir())
	ctx := t.Context()
	plain, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginCustomization(ctx, plain.ConversationID, "commit", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-project conversation: %v", err)
	}
	states, err := database.ListCustomizations(ctx)
	if err != nil || states == nil || len(states) != 0 {
		t.Fatalf("initial states=%v err=%v", states, err)
	}
	state, err := database.BeginCustomization(ctx, id, "commit", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginCustomization(ctx, other, "commit", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("parallel verification: %v", err)
	}
	active, err := database.HasVerifyingCustomization(ctx)
	if err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	updated, err := database.UpdateCustomization(ctx, id, state.Generation, func(current *Customization) {
		current.Status, current.Error = "failed", "test failed"
		current.Output = strings.Repeat("😀", MaxActionOutput) + "\xfftail"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Output) > MaxActionOutput || !utf8.ValidString(updated.Output) || !strings.HasSuffix(updated.Output, "tail") {
		t.Fatalf("invalid bounded output: %d bytes", len(updated.Output))
	}
	next, err := database.BeginCustomization(ctx, id, "new-commit", true)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation == state.Generation || next.Output != "" || next.Error != "" || next.ArtifactSHA256 != "" {
		t.Fatalf("reverification reused previous result: %+v", next)
	}
	if _, err := database.UpdateCustomization(ctx, id, state.Generation, func(current *Customization) {
		current.Status = "ready"
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late worker: %v", err)
	}
}

func TestCustomizationStartupRecovery(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status, artifact, running, installed, want string
	}{
		{"verifying", "", "old", "old", "waiting"},
		{"ready", "new", "old", "old", "ready"},
		{"ready", "new", "new", "new", "running"},
		{"installed", "new", "old", "new", "installed"},
		{"installed", "new", "old", "other", "stale"},
		// An unplanned restart picks up the installed build.
		{"installed", "new", "new", "new", "running"},
		{"restarting", "new", "old", "new", "failed"},
		{"restarting", "new", "new", "new", "running"},
		{"running", "new", "old", "old", "stale"},
		{"running", "new", "new", "new", "running"},
	} {
		t.Run(test.status+"/"+test.running+"/"+test.installed, func(t *testing.T) {
			database, cleanup := NewTestDB(t)
			defer cleanup()
			ensureTestWorkspace(t, database)
			id := newDBTestConversation(t, database, "Shelley", t.TempDir())
			state, err := database.BeginCustomization(t.Context(), id, "commit", true)
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.UpdateCustomization(t.Context(), id, state.Generation, func(current *Customization) {
				current.Status, current.ArtifactSHA256, current.ArtifactPath = test.status, test.artifact, "/private/artifact"
				current.Output = "preserve output"
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.RecoverCustomizations(t.Context(), test.running, test.installed); err != nil {
				t.Fatal(err)
			}
			got, err := database.GetCustomization(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.want || got.Output != "preserve output" || got.ArtifactPath != "/private/artifact" {
				t.Fatalf("recovered=%+v", got)
			}
			// An interrupted verification is queued again, not a failure.
			if test.want != test.status && test.want != "running" && test.want != "waiting" && got.Error == "" {
				t.Fatal("recovery silently lost failure")
			}
			conversation, err := database.GetConversationByID(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if applied := test.status != "running" && test.want == "running"; conversation.Archived != applied {
				t.Fatalf("archived=%v, want %v", conversation.Archived, applied)
			}
		})
	}
}

// Running a build archives only conversations that said their work was finished, were
// not written to since, and are idle; containing a conversation's commits (as after a history rewrite or merge)
// or being installed for it is not enough, and the Assistant is never archived.
func TestCustomizationRunningArchivesOnlyFinishedIdleConversations(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	assistant := ensureTestWorkspace(t, database)
	ctx := t.Context()
	install := func(id string, finished bool) {
		t.Helper()
		state, err := database.BeginCustomization(ctx, id, "commit", finished)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.UpdateCustomization(ctx, id, state.Generation, func(current *Customization) {
			current.Status, current.ArtifactSHA256 = "installed", "new"
		}); err != nil {
			t.Fatal(err)
		}
	}
	message := func(id string, content map[string]any) {
		t.Helper()
		if _, err := database.CreateMessage(ctx, CreateMessageParams{
			ConversationID: id, Type: MessageTypeUser,
			LLMData: map[string]any{"Content": []any{content}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	finished := newDBTestConversation(t, database, "Shelley", t.TempDir())
	message(finished, map[string]any{"Type": 2, "Text": "change Shelley"})
	install(finished, true)
	// The verify call's own result does not reopen the conversation.
	message(finished, map[string]any{"Type": int(llm.ContentTypeToolResult), "ToolUseID": "verify"})
	// The user's follow-up after a finished verification does.
	answered := newDBTestConversation(t, database, "Shelley", t.TempDir())
	install(answered, true)
	message(answered, map[string]any{"Type": 2, "Text": "one more thing"})
	unfinished := newDBTestConversation(t, database, "Shelley", t.TempDir())
	install(unfinished, false)
	queued := newDBTestConversation(t, database, "Shelley", t.TempDir())
	install(queued, true)
	if _, err := database.AppendQueuedMessage(ctx, queued, QueuedMessage{ID: "q", Llm: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// A conversation merely contained in the build adopts it, finished or not.
	adopted := newDBTestConversation(t, database, "Shelley", t.TempDir())
	if _, err := database.WaitForCustomization(ctx, adopted, "old", false); err != nil {
		t.Fatal(err)
	}
	// The Assistant cannot verify, but must survive a record regardless.
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return writeCustomization(tx, &Customization{ConversationID: assistant, Status: "installed", ArtifactSHA256: "new", Finished: true, Generation: "g"})
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.RecoverCustomizations(ctx, "new", "new"); err != nil {
		t.Fatal(err)
	}
	adoptedState, err := database.GetCustomization(ctx, adopted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AdoptCustomization(ctx, adopted, adoptedState.Generation, &Customization{Status: "running", Commit: "commit", ArtifactSHA256: "new"}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{finished: true, answered: false, unfinished: false, queued: false, adopted: false, assistant: false} {
		state, err := database.GetCustomization(ctx, id)
		if err != nil || state.Status != "running" {
			t.Fatalf("%s: %+v %v", id, state, err)
		}
		conversation, err := database.GetConversationByID(ctx, id)
		if err != nil || conversation.Archived != want {
			t.Fatalf("%s archived=%v, want %v (%v)", id, conversation.Archived, want, err)
		}
	}
}

func TestCustomizationInstallKeepsConversationWithPendingWork(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ensureTestWorkspace(t, database)
	id := newDBTestConversation(t, database, "Shelley", t.TempDir())
	state, err := database.BeginCustomization(t.Context(), id, "commit", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateCustomization(t.Context(), id, state.Generation, func(current *Customization) {
		current.Status, current.ArtifactSHA256 = "restarting", "new"
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationAgentWorking(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	if err := database.RecoverCustomizations(t.Context(), "new", "new"); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.GetCustomization(t.Context(), id)
	if err != nil || got.Status != "running" || conversation.Archived {
		t.Fatalf("status=%v archived=%v err=%v", got.Status, conversation.Archived, err)
	}
}

func TestCustomizationWaitQueuesBehindVerification(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ensureTestWorkspace(t, database)
	id := newDBTestConversation(t, database, "Shelley", t.TempDir())
	other := newDBTestConversation(t, database, "Shelley", t.TempDir())
	if _, err := database.BeginCustomization(t.Context(), id, "commit", true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.WaitForCustomization(t.Context(), id, "commit", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("verifying conversation cannot also wait: %v", err)
	}
	waiting, err := database.WaitForCustomization(t.Context(), other, "commit", true)
	if err != nil || waiting.Status != "waiting" || !waiting.Unsettled() {
		t.Fatalf("waiting=%+v err=%v", waiting, err)
	}
	if _, err := database.ArchiveConversation(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetCustomization(t.Context(), other)
	if err != nil || !got.Archived || got.Unsettled() {
		t.Fatalf("archived conversations are settled: %+v %v", got, err)
	}
}
