package db

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
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

func TestWorkspaceAtomicSingletonAndAdoption(t *testing.T) {
	t.Parallel()
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := t.Context()
	if on, err := database.WorkspaceMode(ctx); err != nil || on {
		t.Fatalf("a new database in workspace mode: %v %v", on, err)
	}
	EnableWorkspaceMode(t, database)
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
	channel, err := database.CreateChannelConversation(ctx, "external", "https://chat.invalid", model, ConversationOptions{})
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
			id, err := database.EnsureWorkspace(ctx, cwd, model, ConversationOptions{ThinkingLevel: "medium"})
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
	if assistant.Model == nil || *assistant.Model != model || assistant.Cwd != nil ||
		!assistant.UserInitiated || assistant.ParentConversationID != nil ||
		ParseConversationOptions(assistant.ConversationOptions).ThinkingLevel != "medium" {
		t.Fatalf("unexpected assistant: %+v", assistant)
	}
	meta, err := database.GetWorkspaceConversation(ctx, assistantID)
	if err != nil || !meta.IsAssistant || meta.Project != "" {
		t.Fatalf("assistant metadata: %+v %v", meta, err)
	}
	// Outside workspace mode there is no Assistant; it's an ordinary
	// conversation, which a project can take.
	if err := database.SetFeatureFlagOverride(ctx, WorkspaceFlag, "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetAssistantID(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("assistant outside workspace mode: %v", err)
	}
	if _, err := database.GetWorkspaceConversation(ctx, assistantID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("assistant metadata outside workspace mode: %v", err)
	}
	if w, err := database.GetWorkspace(ctx); err != nil || w.AssistantID != "" || len(w.Projects) != 1 {
		t.Fatalf("workspace outside workspace mode: %+v %v", w, err)
	}
	EnableWorkspaceMode(t, database)
	unchanged, err := database.GetConversationByID(ctx, active.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if *unchanged.Slug != slug || *unchanged.Cwd != cwd || *unchanged.Model != model ||
		unchanged.ConversationOptions != active.ConversationOptions || unchanged.Archived {
		t.Fatalf("workspace setup changed original conversation: %+v", unchanged)
	}
	shelley, err := database.GetProject(ctx, "Shelley")
	if err != nil {
		t.Fatal(err)
	}
	shelley.Instructions = "Keep agent edits"
	shelley.Actions = []ProjectAction{{ID: "custom", Label: "Configured action", Command: "make custom"}}
	if err := database.UpdateProject(ctx, "Shelley", *shelley); err != nil {
		t.Fatal(err)
	}
	newConversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureWorkspace(ctx, "not-a-repository", "different-model", ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	shelley, _ = database.GetProject(ctx, "Shelley")
	if shelley.Instructions != "Keep agent edits" || !reflect.DeepEqual(shelley.Actions, []ProjectAction{{ID: "custom", Label: "Configured action", Command: "make custom"}}) {
		t.Fatal("workspace ensure overwrote project edits")
	}
	if _, err := database.GetWorkspaceConversation(ctx, newConversation.ConversationID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new conversation in a project: %v", err)
	}
	var count int
	if err := database.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT count(*) FROM conversations WHERE conversation_id NOT IN (?,?,?,?,?,?)",
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
	if _, err := database.AppendQueuedMessage(ctx, id, QueuedMessage{ID: "pending"}); err != nil {
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
		_, err := tx.Exec(`INSERT INTO background_jobs(job_id,conversation_id,tool_use_id,command,pid,process_start_time,log_path,exit_path,started_at)
			VALUES('job','active-child','tool','true',1,1,'log','exit',CURRENT_TIMESTAMP)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.BeginAction(ctx, id, "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("descendant job allowed action: %v", err)
	}
	if err := database.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("UPDATE background_jobs SET exited=TRUE,notified=TRUE WHERE job_id='job'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	run, _, _, err := database.BeginAction(ctx, id, "release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendQueuedMessage(ctx, id, QueuedMessage{ID: "arrived-during-run"}); err != nil {
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
