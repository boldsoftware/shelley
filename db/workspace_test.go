package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db/generated"
)

// newDBTestConversation creates a conversation in project: one labeled with its profile.
func newDBTestConversation(t *testing.T, database *DB, project, cwd string) string {
	t.Helper()
	AddTestProject(t, database, project)
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, ConversationOptions{Profile: project})
	if err != nil {
		t.Fatal(err)
	}
	return conversation.ConversationID
}

func TestAssistantAtomicSingleton(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	if on, err := database.WorkspaceMode(ctx); err != nil || on {
		t.Fatalf("a new database in workspace mode: %v %v", on, err)
	}
	// Without create, there is no Assistant until one is needed.
	if id, err := database.EnsureAssistant(ctx, "predictable", ConversationOptions{}, false, ""); err != nil || id != "" {
		t.Fatalf("assistant without create: %q %v", id, err)
	}
	cwd := t.TempDir()
	slug, model := "keep-this-task", "predictable"
	opts := ConversationOptions{SystemPrompt: "Preserve me", ThinkingLevel: "high"}
	active, err := database.CreateConversation(ctx, &slug, true, &cwd, &model, opts)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ArchiveConversation(ctx, archived.ConversationID); err != nil {
		t.Fatal(err)
	}
	internal, err := database.CreateConversation(ctx, nil, false, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A web chat's conversation, with no endpoint, does not become the
	// Assistant (see TestPromotesTextedConversation).
	channel, err := database.CreateChannelConversation(ctx, "external", "", model, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var child generated.Conversation
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		child, err = generated.New(tx.Conn()).CreateSubagentConversation(ctx, generated.CreateSubagentConversationParams{
			ConversationID: "child-task", ParentConversationID: &active.ConversationID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			id, err := database.EnsureAssistant(ctx, model, ConversationOptions{ThinkingLevel: "medium"}, true, cwd)
			results <- result{id, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var assistantID string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if assistantID == "" {
			assistantID = result.id
		} else if assistantID != result.id {
			t.Fatalf("different assistants: %q and %q", assistantID, result.id)
		}
	}
	workspace, err := database.GetWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.AssistantID != assistantID || len(workspace.Projects) != 1 || len(workspace.Conversations) != 1 || workspace.Runs == nil {
		t.Fatalf("unexpected workspace: %+v", workspace)
	}
	// Existing conversations are in no project.
	for _, id := range []string{active.ConversationID, archived.ConversationID, internal.ConversationID, child.ConversationID, channel.ConversationID} {
		if _, err := database.GetWorkspaceConversation(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("conversation %s in a project: %v", id, err)
		}
	}
	assistant, err := database.GetConversationByID(ctx, assistantID)
	if err != nil {
		t.Fatal(err)
	}
	// It is labelled with the Assistant profile, whose settings it didn't
	// take.
	options := ParseConversationOptions(assistant.ConversationOptions)
	if assistant.Slug == nil || *assistant.Slug != AssistantSlug || assistant.Model == nil || *assistant.Model != model || assistant.Cwd != nil ||
		!assistant.UserInitiated || assistant.ParentConversationID != nil ||
		options.ThinkingLevel != "medium" || options.Profile != AssistantProfile || !options.SettingsEdited {
		t.Fatalf("unexpected assistant: %+v", assistant)
	}
	meta, err := database.GetWorkspaceConversation(ctx, assistantID)
	if err != nil || !meta.IsAssistant || meta.Project != "" {
		t.Fatalf("assistant metadata: %+v %v", meta, err)
	}
	// The Assistant does not depend on workspace mode, which only shows it
	// differently.
	if id, err := database.GetAssistantID(ctx); err != nil || id != assistantID {
		t.Fatalf("assistant outside workspace mode: %q %v", id, err)
	}
	unchanged, err := database.GetConversationByID(ctx, active.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if *unchanged.Slug != slug || *unchanged.Cwd != cwd || *unchanged.Model != model ||
		unchanged.ConversationOptions != active.ConversationOptions || unchanged.Archived {
		t.Fatalf("workspace setup changed original conversation: %+v", unchanged)
	}
	shelley, err := database.GetProject(ctx, SelfProject)
	if err != nil {
		t.Fatal(err)
	}
	shelley.Instructions = "Keep agent edits"
	shelley.Actions = []ProjectAction{{ID: "custom", Label: "Configured action", Command: "make custom"}}
	if err := database.UpdateProject(ctx, SelfProject, *shelley); err != nil {
		t.Fatal(err)
	}
	newConversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := database.EnsureAssistant(ctx, "different-model", ConversationOptions{}, true, "not-a-repository"); err != nil || id != assistantID {
		t.Fatalf("second ensure: %q %v", id, err)
	}
	shelley, _ = database.GetProject(ctx, SelfProject)
	if shelley.Instructions != "Keep agent edits" || !reflect.DeepEqual(shelley.Actions, []ProjectAction{{ID: "custom", Label: "Configured action", Command: "make custom"}}) {
		t.Fatal("workspace ensure overwrote project edits")
	}
	if _, err := database.GetWorkspaceConversation(ctx, newConversation.ConversationID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new conversation in a project: %v", err)
	}
	var count int
	if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.Conn().QueryRowContext(ctx, "SELECT count(*) FROM conversations WHERE conversation_id NOT IN (?,?,?,?,?,?)",
			active.ConversationID, archived.ConversationID, internal.ConversationID, child.ConversationID, channel.ConversationID, newConversation.ConversationID).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("orphan assistants: count=%d err=%v", count, err)
	}
	AddTestProject(t, database, "Docs")
	if err := database.MoveConversation(ctx, assistantID, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("assistant move: %v", err)
	}
	if _, _, _, err := database.BeginAction(ctx, assistantID, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("assistant action: %v", err)
	}
}

// Shelley's own project comes with the Assistant only when it is made with
// a Shelley cwd (see TestAssistantAtomicSingleton).
func TestAssistantWithoutShelleyProject(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	if _, err := database.EnsureAssistant(t.Context(), "predictable", ConversationOptions{}, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetProject(t.Context(), "Shelley"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project without a Shelley cwd: %v", err)
	}
}

func TestProjectLifecycleAndValidation(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	project, err := database.CreateProject(ctx, Project{Name: " Custom "}, "")
	if err != nil || project.Name != "Custom" || project.Actions == nil {
		t.Fatalf("create project: %+v %v", project, err)
	}
	// A project is a profile, with a copy of the default profile's settings.
	var profile generated.Profile
	err = database.Queries(ctx, func(q *generated.Queries) (err error) {
		profile, err = q.GetProfile(ctx, "Custom")
		return err
	})
	if err != nil || profile.IsDefault || profile.Builtin {
		t.Fatalf("project profile: %+v %v", profile, err)
	}
	id := newDBTestConversation(t, database, project.Name, t.TempDir())
	if err := database.DeleteProject(ctx, project.Name); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted project with an active conversation: %v", err)
	}
	if _, err := database.ArchiveConversation(ctx, id); err != nil {
		t.Fatal(err)
	}
	// A recorded base VM must be deleted first.
	vm := ProjectBaseVM{Project: project.Name, Name: "custom-base", Integration: "i", Status: ProjectBaseVMCreated, RequestedAt: time.Now()}
	if err := database.RecordProjectBaseVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteProject(ctx, project.Name); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted project with a base VM: %v", err)
	}
	if err := database.ForgetProjectBaseVM(ctx, "i", "custom-base"); err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(ctx, id, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project: %v", err)
	}
	// Renaming relabels its conversations.
	project.Name = "Renamed"
	if err := database.UpdateProject(ctx, "Custom", *project); err != nil {
		t.Fatal(err)
	}
	if c, err := database.GetWorkspaceConversation(ctx, id); err != nil || c.Project != "Renamed" || !c.Archived {
		t.Fatalf("renamed project's conversation: %+v %v", c, err)
	}
	AddTestProject(t, database, "my-project")
	for _, name := range []string{"Default", "Assistant", "my-project"} {
		if err := database.UpdateProject(ctx, "Renamed", Project{Name: name}); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
			t.Fatalf("renamed onto %s: %v", name, err)
		}
	}
	if err := database.MoveConversation(ctx, id, "my-project"); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteProject(ctx, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := database.Queries(ctx, func(q *generated.Queries) error {
		_, err := q.GetProfile(ctx, "Renamed")
		return err
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project's profile survived: %v", err)
	}
	conversation, err := database.GetConversationByID(ctx, id)
	if err != nil || !conversation.Archived {
		t.Fatalf("deletion changed archived conversation: %+v %v", conversation, err)
	}
	if err := database.UpdateProject(ctx, "missing", Project{Name: "Missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing update: %v", err)
	}
	for _, project := range []Project{
		{Name: " "},
		{Name: "a/b"},
		{Name: "Default"},
		{Name: "Bad command", Actions: []ProjectAction{{ID: "a", Label: "Run"}}},
		{Name: "Bad ID", Actions: []ProjectAction{{ID: "../a", Label: "Run", Command: "true"}}},
		{Name: "Dot ID", Actions: []ProjectAction{{ID: ".", Label: "Run", Command: "true"}}},
		{Name: "Parent ID", Actions: []ProjectAction{{ID: "..", Label: "Run", Command: "true"}}},
		{Name: "Control ID", Actions: []ProjectAction{{ID: "push\narchive", Label: "Run", Command: "true"}}},
		{Name: "Duplicate", Actions: []ProjectAction{{ID: "a", Label: "Run", Command: "true"}, {ID: "a", Label: "Again", Command: "true"}}},
	} {
		if _, err := database.CreateProject(ctx, project, ""); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) {
			t.Fatalf("accepted invalid project %+v: %v", project, err)
		}
	}
}

func TestActionsAtomicReservationAndCompletion(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	cwd := t.TempDir()
	id := newDBTestConversation(t, database, "my-project", cwd)
	if err := database.SetConversationAgentWorking(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	AddTestProject(t, database, "Docs")
	if _, _, _, err := database.BeginAction(ctx, id, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("working agent action: %v", err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("working agent assignment: %v", err)
	}
	if err := database.SetConversationAgentWorking(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	runs := make(chan *ActionRun, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			<-start
			run, action, actualCwd, err := database.BeginAction(ctx, id, "release")
			if err != nil {
				errs <- err
				return
			}
			if action.Command != "make release" || actualCwd != cwd {
				errs <- errors.New("incorrect action snapshot/cwd")
				return
			}
			runs <- run
		})
	}
	close(start)
	wg.Wait()
	close(runs)
	close(errs)
	if len(runs) != 1 || len(errs) != 11 {
		t.Fatalf("reservation successes=%d errors=%d", len(runs), len(errs))
	}
	for err := range errs {
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("unexpected reservation error: %v", err)
		}
	}
	run := <-runs
	if running, err := database.HasRunningAction(ctx, id); err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
	if running, err := database.HasAnyRunningAction(ctx); err != nil || !running {
		t.Fatalf("global running=%v err=%v", running, err)
	}
	if err := database.MoveConversation(ctx, id, "Docs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("assignment during action: %v", err)
	}
	output := strings.Repeat("x", MaxActionOutput*2) + "\xff\x00tail🙂"
	if err := database.AppendActionOutput(ctx, run.ID, output); err != nil {
		t.Fatal(err)
	}
	if stored, err := database.GetActionRun(ctx, run.ID); err != nil || len(stored.Output) > MaxActionOutput ||
		!utf8.ValidString(stored.Output) || !strings.HasSuffix(stored.Output, "tail🙂") {
		t.Fatalf("bounded output: %+v %v", stored, err)
	}
	code := 7
	if err := database.FinishAction(ctx, run.ID, &code, "exit status 7"); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetActionRun(ctx, run.ID)
	if err != nil || stored.Status != "failed" || stored.FinishedAt == nil || stored.ExitCode == nil || *stored.ExitCode != code {
		t.Fatalf("failed run: %+v %v", stored, err)
	}
	conversation, _ := database.GetConversationByID(ctx, id)
	if conversation.Archived {
		t.Fatal("failed action archived conversation")
	}
	run, _, _, err = database.BeginAction(ctx, id, "release")
	if err != nil {
		t.Fatal(err)
	}
	code = 0
	if err := database.FinishAction(ctx, run.ID, &code, ""); err != nil {
		t.Fatal(err)
	}
	stored, _ = database.GetActionRun(ctx, run.ID)
	conversation, _ = database.GetConversationByID(ctx, id)
	if stored.Status != "succeeded" || !conversation.Archived {
		t.Fatalf("success/archive not committed together: %+v %+v", stored, conversation)
	}
	if _, _, _, err := database.BeginAction(ctx, id, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("action on archived conversation: %v", err)
	}
	if running, err := database.HasAnyRunningAction(ctx); err != nil || running {
		t.Fatalf("completed action remained globally running=%v err=%v", running, err)
	}
}

func TestActionRestartNeverArchivesOrReruns(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	id := newDBTestConversation(t, database, "my-project", t.TempDir())
	run, _, _, err := database.BeginAction(ctx, id, "release")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AppendActionOutput(ctx, run.ID, "working\n"); err != nil {
		t.Fatal(err)
	}
	if err := database.InterruptActionRuns(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetActionRun(ctx, run.ID)
	if err != nil || stored.Status != "interrupted" || stored.Output != "working\n" || stored.FinishedAt == nil || stored.ExitCode != nil {
		t.Fatalf("interrupted run: %+v %v", stored, err)
	}
	code := 0
	if err := database.FinishAction(ctx, run.ID, &code, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("late completion accepted: %v", err)
	}
	if err := database.AppendActionOutput(ctx, run.ID, "late"); !errors.Is(err, ErrConflict) {
		t.Fatalf("late output accepted: %v", err)
	}
	conversation, _ := database.GetConversationByID(ctx, id)
	if conversation.Archived {
		t.Fatal("interrupted action archived conversation")
	}
	workspace, err := database.GetWorkspace(ctx)
	if err != nil || len(workspace.Runs) != 1 {
		t.Fatalf("interrupted action rerun: %+v %v", workspace, err)
	}
}

func TestActionPendingWorkGuardsAndSuccessRetention(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	id := newDBTestConversation(t, database, "my-project", t.TempDir())
	if _, err := database.AppendQueuedMessage(ctx, id, QueuedMessage{ID: "pending"}, SentDraft{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.BeginAction(ctx, id, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending queue allowed action: %v", err)
	}
	if _, err := database.ClearQueuedMessages(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		if _, err := q.CreateSubagentConversation(ctx, generated.CreateSubagentConversationParams{
			ConversationID: "active-child", ParentConversationID: &id,
		}); err != nil {
			return err
		}
		return q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{ConversationID: "active-child", AgentWorking: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.BeginAction(ctx, id, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("active child allowed action: %v", err)
	}
	if err := database.SetConversationAgentWorking(ctx, "active-child", false); err != nil {
		t.Fatal(err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `INSERT INTO background_jobs(job_id,conversation_id,tool_use_id,command,pid,process_start_time,log_path,exit_path,started_at)
			VALUES('job','active-child','tool','true',1,1,'log','exit',CURRENT_TIMESTAMP)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.BeginAction(ctx, id, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("descendant job allowed action: %v", err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, "UPDATE background_jobs SET exited=TRUE,notified=TRUE WHERE job_id='job'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	run, _, _, err := database.BeginAction(ctx, id, "release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendQueuedMessage(ctx, id, QueuedMessage{ID: "arrived-during-run"}, SentDraft{}); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := database.FinishAction(ctx, run.ID, &code, ""); err != nil {
		t.Fatal(err)
	}
	run, err = database.GetActionRun(ctx, run.ID)
	if err != nil || run.Status != "succeeded" || !strings.Contains(run.Output, "not archived because new work is pending") {
		t.Fatalf("missing successful retention notice: %+v %v", run, err)
	}
	conversation, err := database.GetConversationByID(ctx, id)
	if err != nil || conversation.Archived {
		t.Fatalf("new work was archived: %+v %v", conversation, err)
	}
}

func TestWorkspaceBoundsCompletedRunHistoryButKeepsRunning(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	quietID := newDBTestConversation(t, database, "my-project", t.TempDir())
	quietRun, _, _, err := database.BeginAction(ctx, quietID, "release")
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := database.FinishAction(ctx, quietRun.ID, &code, ""); err != nil {
		t.Fatal(err)
	}
	id := newDBTestConversation(t, database, "my-project", t.TempDir())
	var oldestID string
	for range 25 {
		run, _, _, err := database.BeginAction(ctx, id, "release")
		if err != nil {
			t.Fatal(err)
		}
		if oldestID == "" {
			oldestID = run.ID
		}
		code := 1
		if err := database.FinishAction(ctx, run.ID, &code, "failed"); err != nil {
			t.Fatal(err)
		}
	}
	runningIDs := map[string]bool{}
	for range 2 {
		projectConversationID := newDBTestConversation(t, database, "my-project", t.TempDir())
		run, _, _, err := database.BeginAction(ctx, projectConversationID, "release")
		if err != nil {
			t.Fatal(err)
		}
		runningIDs[run.ID] = true
	}
	workspace, err := database.GetWorkspace(ctx)
	if err != nil || len(workspace.Runs) != 23 {
		t.Fatalf("bounded workspace: runs=%d err=%v", len(workspace.Runs), err)
	}
	quietFound := false
	for _, run := range workspace.Runs {
		if run.ID == quietRun.ID {
			quietFound = true
		}
		if run.Status == "running" {
			delete(runningIDs, run.ID)
		}
		if run.ID == oldestID {
			t.Fatal("old completed run remained in workspace snapshot")
		}
	}
	if len(runningIDs) != 0 {
		t.Fatalf("running runs omitted: %+v", runningIDs)
	}
	if !quietFound {
		t.Fatal("recent conversation feedback disappeared behind other conversations' history")
	}
	if run, err := database.GetActionRun(ctx, oldestID); err != nil || run.Status != "failed" {
		t.Fatalf("old run not retained durably: %+v %v", run, err)
	}
}

// An Assistant created while another conversation is called "assistant"
// stays unnamed.
func TestWorkspaceAssistantSlugTaken(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	EnableWorkspaceMode(t, database)
	slug := AssistantSlug
	if _, err := database.CreateConversation(ctx, &slug, true, nil, nil, ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	id, err := database.EnsureAssistant(ctx, "", ConversationOptions{}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if assistant, err := database.GetConversationByID(ctx, id); err != nil || assistant.Slug != nil {
		t.Fatalf("assistant: %+v %v", assistant, err)
	}
}

// Migration 059 renames an existing Assistant "assistant", or unnames it
// while another conversation has that slug.
func TestAssistantSlugMigration(t *testing.T) {
	t.Parallel()
	for _, taken := range []bool{false, true} {
		database := setupDBMigratedThrough(t, 58)
		ctx := t.Context()
		if err := database.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, slug, user_initiated) VALUES('a', 'reduce-nav-category-padding', 1), ('b', 'other', 1);
			INSERT INTO workspace(singleton, assistant_id) VALUES(1, 'a')`); err != nil {
			t.Fatal(err)
		}
		if taken {
			if err := database.pool.Exec(ctx, "UPDATE conversations SET slug='assistant' WHERE conversation_id='b'"); err != nil {
				t.Fatal(err)
			}
		}
		if err := database.runMigration(ctx, "059-assistant-slug.sql", 59); err != nil {
			t.Fatal(err)
		}
		// The generated queries expect the latest schema.
		if err := database.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"a": "assistant", "b": "other"}
		if taken {
			want = map[string]string{"a": "", "b": "assistant"}
		}
		for id, want := range want {
			c, err := database.GetConversationByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got := ptrValue(c.Slug); got != want {
				t.Fatalf("taken=%v: %s has slug %q, want %q", taken, id, got, want)
			}
		}
		database.Close()
	}
}

func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// The most recent texted chat's conversation that is unarchived and in no
// project becomes the Assistant, unbound from its chat, even outside
// workspace mode.
func TestPromotesTextedConversation(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	AddTestProject(t, database, "P")
	texted := func(chat, profile string) string {
		c, err := database.CreateChannelConversation(ctx, chat, "https://chat.invalid/"+chat, "predictable", ConversationOptions{Profile: profile, ToolOverrides: map[string]string{"add_reaction": "off"}})
		if err != nil {
			t.Fatal(err)
		}
		return c.ConversationID
	}
	want := texted("eligible", "")
	archived := texted("archived", "")
	if _, err := database.ArchiveConversation(ctx, archived); err != nil {
		t.Fatal(err)
	}
	texted("in-project", "P")
	id, err := database.EnsureAssistant(ctx, "predictable", ConversationOptions{}, false, "")
	if err != nil || id != want {
		t.Fatalf("Assistant = %q, %v; want %q", id, err, want)
	}
	assistant, err := database.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// It is labelled with the Assistant profile, whose settings it didn't
	// take.
	options := ParseConversationOptions(assistant.ConversationOptions)
	if assistant.ExternalConversationID != nil || assistant.ExternalEndpoint != nil || assistant.Slug == nil || *assistant.Slug != AssistantSlug ||
		options.Profile != AssistantProfile || !options.SettingsEdited {
		t.Fatalf("unexpected Assistant: %+v", assistant)
	}
	// Its turn ends go unannounced, as they were in the chat.
	if !options.DisableNotifications {
		t.Fatalf("notifications on: %+v", options)
	}
	// It answers with the Assistant's tools, unless it chose otherwise.
	for _, name := range AssistantTools {
		want := "on"
		if name == "add_reaction" {
			want = "off"
		}
		if options.ToolOverrides[name] != want {
			t.Fatalf("%s is %q, want %s: %+v", name, options.ToolOverrides[name], want, options)
		}
	}
}

// The Assistant is the most recently created top-level, unarchived,
// non-draft conversation with the Assistant profile, so creating one with
// that profile starts a new Assistant.
func TestAssistantIsNewestWithProfile(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	assistant := func() string {
		t.Helper()
		id, err := database.GetAssistantID(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	create := func(profile string) string {
		t.Helper()
		c, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		return c.ConversationID
	}
	if got := assistant(); got != "" {
		t.Fatalf("Assistant before any conversation: %q", got)
	}
	first := create(AssistantProfile)
	create("Default")
	if got := assistant(); got != first {
		t.Fatalf("Assistant = %q, want %q", got, first)
	}
	// Within created_at's second, the later wins.
	second := create(AssistantProfile)
	if got := assistant(); got != second {
		t.Fatalf("Assistant = %q, want the newer %q", got, second)
	}
	// Neither a draft, nor a subagent, nor a conversation bound to a chat
	// is the Assistant.
	if _, err := database.CreateDraftConversation(ctx, nil, nil, ConversationOptions{Profile: AssistantProfile}, "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateChannelConversation(ctx, "chat", "http://chat", "", ConversationOptions{Profile: AssistantProfile}); err != nil {
		t.Fatal(err)
	}
	sub, err := database.CreateSubagentConversation(ctx, "sub", second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateConversationOptions(ctx, sub.ConversationID, ConversationOptions{Profile: AssistantProfile}); err != nil {
		t.Fatal(err)
	}
	if got := assistant(); got != second {
		t.Fatalf("Assistant = %q, want %q", got, second)
	}
	// It goes by created_at.
	if err := database.pool.Exec(ctx, "UPDATE conversations SET created_at = datetime(created_at, '+1 minute') WHERE conversation_id = ?", first); err != nil {
		t.Fatal(err)
	}
	if got := assistant(); got != first {
		t.Fatalf("Assistant = %q, want the later created %q", got, first)
	}
	// Archiving it gives the place back to the one before.
	if _, err := database.ArchiveConversation(ctx, first); err != nil {
		t.Fatal(err)
	}
	if got := assistant(); got != second {
		t.Fatalf("Assistant = %q after archiving %q, want %q", got, first, second)
	}
}

// Migration 088 labels the conversation the workspace row named with the
// Assistant profile, keeping its own settings, so it stays the Assistant,
// even with newer texted chats, which had the profile.
func TestAssistantProfileMigration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		options  string
		archived bool
	}{
		{options: `{"profile":"Default","thinking_level":"high"}`},
		{options: `{"profile":"Assistant","thinking_level":"high"}`},
		{options: `{"thinking_level":"high"}`},
		{options: `null`},
		{options: ``},
		{options: `[]`},
		{options: `{"profile":"Assistant","tool_overrides":{"add_reaction":"off","bash":"off"}}`},
		// Labelled, but no longer the Assistant.
		{options: `{"profile":"Default","thinking_level":"high"}`, archived: true},
	} {
		database := setupDBMigratedThrough(t, 87)
		ctx := t.Context()
		if err := database.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, user_initiated, conversation_options, created_at, archived)
			VALUES('older', 1, '{"profile":"Assistant"}', '2026-01-01 00:00:00', 0), ('a', 1, ?, '2026-01-02 00:00:00', ?);
			INSERT INTO conversations(conversation_id, user_initiated, conversation_options, created_at, external_conversation_id, external_endpoint)
			VALUES('chat', 1, '{"profile":"Assistant"}', '2026-01-03 00:00:00', 'chat', 'http://chat');
			INSERT INTO workspace(singleton, assistant_id) VALUES(1, 'a');
			INSERT INTO profiles(name, settings, user_edited) VALUES('Assistant', '{"tool_overrides":{"send_to_thread":"off"}}', 1)
			ON CONFLICT(name) DO UPDATE SET settings = excluded.settings, user_edited = 1`, tc.options, tc.archived); err != nil {
			t.Fatal(err)
		}
		if err := database.runMigration(ctx, "088-assistant-profile.sql", 88); err != nil {
			t.Fatal(err)
		}
		if err := database.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		want := "a"
		if tc.archived {
			want = "older"
		}
		if id, err := database.GetAssistantID(ctx); err != nil || id != want {
			t.Fatalf("%+v: Assistant = %q, %v; want %q", tc, id, err, want)
		}
		c, err := database.GetConversationByID(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		got := ParseConversationOptions(c.ConversationOptions)
		wantOpts := ConversationOptions{Profile: AssistantProfile, SettingsEdited: !strings.Contains(tc.options, `"Assistant"`)}
		if strings.Contains(tc.options, "high") {
			wantOpts.ThinkingLevel = "high"
		}
		if got.Profile != wantOpts.Profile || got.SettingsEdited != wantOpts.SettingsEdited || got.ThinkingLevel != wantOpts.ThinkingLevel {
			t.Fatalf("%+v: options = %+v, want %+v", tc, got, wantOpts)
		}
		// Conversations and the profile with its name turn on the tools the
		// Assistant answers with, which are no longer on by default, unless
		// they chose otherwise.
		var profile string
		if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			return rx.Conn().QueryRowContext(ctx, "SELECT settings FROM profiles WHERE name = 'Assistant'").Scan(&profile)
		}); err != nil {
			t.Fatal(err)
		}
		profileOpts := ParseConversationOptions(profile)
		chosen := func(settings, name string) string {
			if strings.Contains(settings, `"`+name+`":"off"`) {
				return "off"
			}
			return "on"
		}
		for _, name := range AssistantTools {
			if got.ToolOverrides[name] != chosen(tc.options, name) || profileOpts.ToolOverrides[name] != chosen(`"send_to_thread":"off"`, name) {
				t.Fatalf("%+v: %s is %q in the Assistant, %q in its profile", tc, name, got.ToolOverrides[name], profileOpts.ToolOverrides[name])
			}
		}
		// So do they turn notifications off, now a setting.
		if !got.DisableNotifications || !profileOpts.DisableNotifications {
			t.Fatalf("%+v: notifications on in the Assistant (%v) or its profile (%v)", tc, !got.DisableNotifications, !profileOpts.DisableNotifications)
		}
		database.Close()
	}
}

// A fork from before a settings marker goes back to the settings the
// marker saved (server.applyForkPointModelState), so migration 088 turns
// the tools on in those of Assistant conversations too, unless they chose
// otherwise: a fork from before the upgrade would otherwise be an
// Assistant that can't answer Slack. Notifications, now a setting too, are
// off where they were: for the Assistant, or where the conversation turned
// them off.
func TestAssistantProfileMigrationMarkers(t *testing.T) {
	t.Parallel()
	database := setupDBMigratedThrough(t, 87)
	ctx := t.Context()
	markers := map[string]string{
		"assistant": `{"text":"x","previous":{"profile":"Assistant","model":"m","tool_overrides":{"add_reaction":"off","bash":"off"}}}`,
		"bare":      `{"text":"x","previous":{"profile":"Assistant"}}`,
		"null":      `{"text":"x","previous":{"profile":"Assistant","tool_overrides":null}}`,
		"default":   `{"text":"x","previous":{"profile":"Default","tool_overrides":{"bash":"off"}}}`,
		"none":      `{"text":"x"}`,
		"invalid":   `{`,
		"quiet":     `{"text":"x","previous":{"profile":"Default"}}`,
	}
	if err := database.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, user_initiated, conversation_options)
		VALUES('a', 1, '{"profile":"Assistant"}'), ('quiet', 1, '{"profile":"Default","disable_notifications":true}')`); err != nil {
		t.Fatal(err)
	}
	i := 0
	for id, data := range markers {
		i++
		conv := "a"
		if id == "quiet" {
			conv = "quiet"
		}
		if err := database.pool.Exec(ctx, `INSERT INTO messages(message_id, conversation_id, sequence_id, type, user_data) VALUES(?, ?, ?, 'modelchange', ?)`, id, conv, i, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.pool.Exec(ctx, `INSERT INTO messages(message_id, conversation_id, sequence_id, type, user_data) VALUES('user', 'a', 99, 'user', ?)`, markers["bare"]); err != nil {
		t.Fatal(err)
	}
	if err := database.runMigration(ctx, "088-assistant-profile.sql", 88); err != nil {
		t.Fatal(err)
	}
	const tools = `"tool_overrides":{"message_user":"on","send_to_thread":"on","send_to_channel":"on","add_reaction":"on"}`
	want := map[string]string{
		"assistant": `{"text":"x","previous":{"profile":"Assistant","model":"m","tool_overrides":{"message_user":"on","send_to_thread":"on","send_to_channel":"on","add_reaction":"off","bash":"off"},"disable_notifications":true}}`,
		"bare":      `{"text":"x","previous":{"profile":"Assistant",` + tools + `,"disable_notifications":true}}`,
		"null":      `{"text":"x","previous":{"profile":"Assistant",` + tools + `,"disable_notifications":true}}`,
		"quiet":     `{"text":"x","previous":{"profile":"Default","disable_notifications":true}}`,
		"user":      markers["bare"],
	}
	for id := range markers {
		if want[id] == "" {
			want[id] = markers[id]
		}
	}
	parse := func(s string) any {
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return s
		}
		return v
	}
	for id, w := range want {
		var got string
		if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			return rx.Conn().QueryRowContext(ctx, "SELECT user_data FROM messages WHERE message_id = ?", id).Scan(&got)
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(parse(got), parse(w)) {
			t.Errorf("%s: user_data = %s, want %s", id, got, w)
		}
	}
}

// Notifications are now a setting, which a conversation following its
// profile would take from it, so migration 088 keeps a conversation that
// turned them off from following.
func TestAssistantProfileMigrationNotifications(t *testing.T) {
	t.Parallel()
	database := setupDBMigratedThrough(t, 87)
	ctx := t.Context()
	options := map[string]string{
		"quiet":     `{"profile":"Default","disable_notifications":true}`,
		"loud":      `{"profile":"Default"}`,
		"unlabeled": `{"disable_notifications":true}`,
		"assistant": `{"profile":"Assistant"}`,
	}
	for id, o := range options {
		if err := database.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, user_initiated, conversation_options) VALUES(?, 1, ?)`, id, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]ConversationOptions{
		"quiet":     {Profile: "Default", DisableNotifications: true, SettingsEdited: true},
		"loud":      {Profile: "Default"},
		"unlabeled": {DisableNotifications: true},
		"assistant": {Profile: AssistantProfile, DisableNotifications: true},
	} {
		c, err := database.GetConversationByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got := ParseConversationOptions(c.ConversationOptions)
		if got.Profile != want.Profile || got.DisableNotifications != want.DisableNotifications || got.SettingsEdited != want.SettingsEdited {
			t.Errorf("%s: options %s, want %+v", id, c.ConversationOptions, want)
		}
	}
}

// Were the Assistant profile the default, every new conversation would
// become the Assistant, so migration 088 makes Default the default
// instead, restoring it if it was deleted. The label said nothing about
// conversations made with it, so only the stored Assistant keeps it; a
// newer conversation with it otherwise becomes the Assistant.
func TestAssistantProfileMigrationDefault(t *testing.T) {
	t.Parallel()
	const assistantDefault = `UPDATE profiles SET is_default = FALSE; UPDATE profiles SET is_default = TRUE WHERE name = 'Assistant';`
	for _, tc := range []struct{ setup, want, assistant, project string }{
		{setup: assistantDefault, want: "Default (built-in)", assistant: "a"},
		{setup: assistantDefault + `DELETE FROM profiles WHERE name = 'Default'`, want: "Default (built-in)", assistant: "a"},
		{setup: `INSERT INTO profiles(name) VALUES('Mine'); UPDATE profiles SET is_default = FALSE; UPDATE profiles SET is_default = TRUE WHERE name = 'Mine'`, want: "Mine", assistant: "b"},
		// A project's profile can't be the default.
		{setup: assistantDefault + `INSERT INTO profile_projects(profile, position) VALUES('Default', 0)`, want: "Default 2", assistant: "a", project: "Default"},
		// Nor can a project be named Assistant; b was its conversation.
		{setup: `INSERT INTO profiles(name) VALUES('Assistant project'); INSERT INTO profile_projects(profile, position) VALUES('Assistant', 0)`, want: "Default (built-in)", assistant: "a", project: "Assistant project 2"},
	} {
		database := setupDBMigratedThrough(t, 87)
		ctx := t.Context()
		if err := database.pool.Exec(ctx, `INSERT OR IGNORE INTO profiles(name) VALUES('Assistant');
			INSERT INTO conversations(conversation_id, user_initiated, conversation_options, created_at)
			VALUES('a', 1, '{"profile":"Assistant"}', '2026-01-01 00:00:00'), ('b', 1, '{"profile":"Assistant","model":"m"}', '2026-01-02 00:00:00');
			INSERT INTO messages(message_id, conversation_id, sequence_id, type, user_data)
			VALUES('a1', 'a', 1, 'modelchange', '{"previous":{"profile":"Assistant"}}'), ('b1', 'b', 1, 'modelchange', '{"previous":{"profile":"Assistant"}}');
			INSERT INTO workspace(singleton, assistant_id) VALUES(1, 'a');`+tc.setup); err != nil {
			t.Fatal(err)
		}
		if err := database.runMigration(ctx, "088-assistant-profile.sql", 88); err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			// A restored Default is the built-in one.
			rows, err := rx.Conn().QueryContext(ctx, "SELECT name || CASE WHEN builtin AND NOT user_edited THEN ' (built-in)' ELSE '' END FROM profiles WHERE is_default")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return err
				}
				got = append(got, name)
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: default profiles %q, want %q", tc.setup, got, tc.want)
		}
		if id, err := database.GetAssistantID(ctx); err != nil || id != tc.assistant {
			t.Errorf("%s: Assistant = %q, %v; want %q", tc.setup, id, err, tc.assistant)
		}
		var project, bProfile, aSaved, bSaved string
		if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			return rx.Conn().QueryRowContext(ctx, `SELECT COALESCE((SELECT group_concat(profile) FROM profile_projects), ''),
				COALESCE((SELECT json_extract(conversation_options, '$.profile') FROM conversations WHERE conversation_id = 'b'), ''),
				COALESCE((SELECT json_extract(user_data, '$.previous.profile') FROM messages WHERE message_id = 'a1'), ''),
				COALESCE((SELECT json_extract(user_data, '$.previous.profile') FROM messages WHERE message_id = 'b1'), '')`).Scan(&project, &bProfile, &aSaved, &bSaved)
		}); err != nil {
			t.Fatal(err)
		}
		if project != tc.project || tc.project == "Assistant project 2" && bProfile != tc.project {
			t.Errorf("%s: projects %q, b's profile %q; want project %q", tc.setup, project, bProfile, tc.project)
		}
		// The profile a marker saved for forks from before it changes with
		// its conversation's, so such a fork of b isn't an Assistant if b
		// isn't one.
		if aSaved != "Assistant" || bSaved != bProfile {
			t.Errorf("%s: saved profiles a %q, b %q; want Assistant, %q", tc.setup, aSaved, bSaved, bProfile)
		}
		database.Close()
	}
}

// Startup drains texted messages queued in any conversation, an earlier
// Assistant's too, but not in an archived one, such as the Slack
// Assistant's, which migration 083 retired.
func TestQueuedTextsToRecover(t *testing.T) {
	t.Parallel()
	database := setupDBMigratedThrough(t, 82)
	ctx := t.Context()
	if err := database.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, user_initiated, queued_messages, external_conversation_id, external_endpoint)
		VALUES('slack', 1, '[{"external_message_id":"slack:1"}]', 'slack:assistant', 'http://slack');
		INSERT INTO conversations(conversation_id, user_initiated, queued_messages)
		VALUES('old', 1, '[{"external_message_id":"m1"}]'), ('typed', 1, '[{"id":"q"}]')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := database.Queries(ctx, func(q *generated.Queries) error {
		convs, err := q.ListConversationsWithUnwatchedQueues(ctx)
		for _, c := range convs {
			got = append(got, c.ConversationID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"old"}) {
		t.Fatalf("conversations to drain = %q, want [old]", got)
	}
}
