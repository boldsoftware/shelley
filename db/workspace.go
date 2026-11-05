package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"
)

const MaxActionOutput = 64 << 10

var (
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid input")
)

type ProjectAction struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	Command          string `json:"command"`
	ArchiveOnSuccess bool   `json:"archive_on_success"`
	// Silent runs don't report to the conversation's agent; others send it
	// their result as a message.
	Silent bool `json:"silent"`
}

// WorkspaceConversation is a conversation's place in the workspace: the project its profile
// names, or the Assistant. See 060-workspace-views.sql.
type WorkspaceConversation = generated.WorkspaceConversation

// ActionRunSummary is what the workspace lists of a run.
type ActionRunSummary struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	Project        string     `json:"project"`
	ActionID       string     `json:"action_id"`
	Label          string     `json:"label"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	// Report is the state of the run's report to its agent: "pending",
	// "sent", or "skipped".
	Report string `json:"report"`
}

type ActionRun struct {
	ActionRunSummary
	Command string `json:"command"`
	Output  string `json:"output"`
	Error   string `json:"error"`
	// Archived says whether the run's success archived its conversation.
	Archived bool `json:"archived"`
}

type Workspace struct {
	AssistantID   string                  `json:"assistant_id"`
	Projects      []Project               `json:"projects"`
	Conversations []WorkspaceConversation `json:"conversations"`
	Runs          []ActionRunSummary      `json:"runs"`
	VMs           []ConversationVM        `json:"vms"`
	// BaseVMs are the base VMs Shelley created for projects, which deleting
	// the project deletes.
	BaseVMs []ProjectBaseVM `json:"base_vms"`
	// ShelleyProject names the project that customizes Shelley, if any.
	ShelleyProject string `json:"shelley_project"`
}

// The server adds the rules of customizing Shelley to its project's prompt.
const shelleyProjectInstructions = `Customize, maintain, and improve Shelley, this coding agent.`

// SelfProject names the project that customizes Shelley, which workspace
// mode creates.
const SelfProject = "Self"

// AssistantSlug is the workspace Assistant's slug.
const AssistantSlug = "assistant"

// EnsureAssistant returns the Assistant's id. Without one, the most recent
// texted chat's conversation becomes it (see promoteTextedConversation),
// or, with create, a new one is made with model and opts; otherwise the id
// is "". The Assistant and its workspace row are committed together, and
// with them, if shelleyCwd is set, Shelley's own project there, unless a
// profile has its name.
func (db *DB) EnsureAssistant(ctx context.Context, model string, opts ConversationOptions, create bool, shelleyCwd string) (string, error) {
	var assistantID string
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		workspace, err := q.GetWorkspace(ctx)
		if err == nil {
			assistantID = workspace.AssistantID
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if assistantID, err = promoteTextedConversation(ctx, q); err != nil {
			return err
		}
		if assistantID == "" && !create {
			return nil
		}
		if shelleyCwd != "" {
			taken, err := found(q.GetProfile(ctx, SelfProject))
			if err != nil {
				return err
			}
			if !taken {
				if err := createProject(ctx, q, Project{Name: SelfProject, Instructions: shelleyProjectInstructions, Cwd: shelleyCwd, Actions: []ProjectAction{}}, ""); err != nil {
					return err
				}
			}
		}
		if assistantID != "" {
			return q.CreateWorkspace(ctx, assistantID)
		}
		assistantID, err = GenerateConversationID()
		if err != nil {
			return err
		}
		options, err := json.Marshal(opts)
		if err != nil {
			return err
		}
		var modelPtr *string
		if model != "" {
			modelPtr = &model
		}
		slug, err := assistantSlug(ctx, q)
		if err != nil {
			return err
		}
		// No cwd is invented for the assistant; its tools use the normal
		// configured server working directory.
		if _, err := q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: assistantID, Slug: slug, UserInitiated: true, Model: modelPtr, ConversationOptions: string(options),
		}); err != nil {
			return err
		}
		return q.CreateWorkspace(ctx, assistantID)
	})
	return assistantID, err
}

// assistantSlug is the Assistant's slug: it is titled "Assistant", not
// after its first message, so its slug is "assistant", or none while
// another conversation has that slug (see 059-assistant-slug.sql).
func assistantSlug(ctx context.Context, q *generated.Queries) (*string, error) {
	taken, err := found(q.GetConversationBySlug(ctx, ptr(AssistantSlug)))
	if err != nil || taken {
		return nil, err
	}
	return ptr(AssistantSlug), nil
}

// promoteTextedConversation returns the id of the most recent texted
// chat's conversation (see PromotableTextedConversation), or "" without
// one, made the Assistant but for its workspace row: its texted messages
// get routes, so replies to them still reach the chat, it is unbound from
// the chat, and it is titled like a new Assistant.
func promoteTextedConversation(ctx context.Context, q *generated.Queries) (string, error) {
	id, err := q.PromotableTextedConversation(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := q.RouteTextedMessages(ctx, id); err != nil {
		return "", err
	}
	if err := q.UnbindChatConversation(ctx, id); err != nil {
		return "", err
	}
	slug, err := assistantSlug(ctx, q)
	if err != nil {
		return "", err
	}
	if slug != nil {
		if _, err := q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{Slug: slug, ConversationID: id}); err != nil {
			return "", err
		}
	}
	return id, nil
}

// found reports whether a query for one row found it.
func found[T any](_ T, err error) (bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func ptr[T any](v T) *T { return &v }

// rowsChanged turns an update or delete that changed no rows into sql.ErrNoRows.
func rowsChanged(n int64, err error) error {
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// WorkspaceFlag names the feature flag that turns on workspace mode: a
// pinned Assistant that routes work into projects. 060-workspace-views.sql
// and db/query/workspace.sql name it too.
const WorkspaceFlag = "workspace"

// WorkspaceMode reports whether workspace mode is on.
func (db *DB) WorkspaceMode(ctx context.Context) (bool, error) {
	var on bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		on, err = generated.New(rx.Conn()).WorkspaceMode(ctx)
		return err
	})
	return on, err
}

// GetWorkspaceConversation returns sql.ErrNoRows for conversations that are neither
// in a project nor the Assistant.
func (db *DB) GetWorkspaceConversation(ctx context.Context, conversationID string) (*WorkspaceConversation, error) {
	var c WorkspaceConversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		c, err = generated.New(rx.Conn()).GetWorkspaceConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// conversationAvailable is always called within the writing transaction, making
// duplicate actions and project moves atomic even with concurrent requests.
func conversationAvailable(ctx context.Context, q *generated.Queries, conversationID string) (*generated.Conversation, error) {
	conversation, err := q.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	assistantID, err := q.GetAssistantID(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	running, err := q.HasRunningAction(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	pending, err := pendingWork(ctx, q, &conversation)
	if err != nil {
		return nil, err
	}
	if assistantID == conversationID || running || !conversation.UserInitiated ||
		conversation.ParentConversationID != nil || conversation.ExternalConversationID != nil || pending {
		return nil, ErrConflict
	}
	return &conversation, nil
}

func pendingWork(ctx context.Context, q *generated.Queries, conversation *generated.Conversation) (bool, error) {
	queued, err := ParseQueuedMessagesStrict(conversation.QueuedMessages)
	if err != nil {
		return false, err
	}
	dependentWork, err := q.HasDependentWork(ctx, conversation.ConversationID)
	if err != nil {
		return false, err
	}
	return conversation.AgentWorking || len(queued) != 0 || conversation.TurnInterrupted || dependentWork, nil
}

// MoveConversation puts an idle conversation in project by relabeling its profile. Like any
// profile switch by label, a conversation that hasn't edited its settings takes the
// project's at its next turn; one that has keeps its own.
func (db *DB) MoveConversation(ctx context.Context, conversationID, project string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		if _, err := conversationAvailable(ctx, q, conversationID); err != nil {
			return err
		}
		if _, err := q.GetProject(ctx, project); err != nil {
			return err
		}
		return q.SetConversationProfile(ctx, generated.SetConversationProfileParams{Profile: project, ConversationID: conversationID})
	})
}

// GetAssistantID returns sql.ErrNoRows before there is an Assistant.
func (db *DB) GetAssistantID(ctx context.Context) (string, error) {
	var id string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		id, err = generated.New(rx.Conn()).GetAssistantID(ctx)
		return err
	})
	return id, err
}

func (db *DB) HasRunningAction(ctx context.Context, conversationID string) (bool, error) {
	var running bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		running, err = generated.New(rx.Conn()).HasRunningAction(ctx, conversationID)
		return err
	})
	return running, err
}

func (db *DB) HasAnyRunningAction(ctx context.Context) (bool, error) {
	var running bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		running, err = generated.New(rx.Conn()).HasAnyRunningAction(ctx)
		return err
	})
	return running, err
}

// BeginAction snapshots the configured action, while retaining the conversation's
// actual working directory. A project edit cannot silently move existing conversations.
func (db *DB) BeginAction(ctx context.Context, conversationID, actionID string) (*ActionRun, *ProjectAction, string, error) {
	var run ActionRun
	var action ProjectAction
	var cwd string
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err := conversationAvailable(ctx, q, conversationID)
		if err != nil {
			return err
		}
		if conversation.Archived {
			return fmt.Errorf("%w: the conversation is archived", ErrConflict)
		}
		project, err := getConversationProject(ctx, q, conversationID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(project.Actions, func(a ProjectAction) bool { return a.ID == actionID })
		if i < 0 {
			return sql.ErrNoRows
		}
		action = project.Actions[i]
		if conversation.Cwd != nil {
			cwd = *conversation.Cwd
		}
		report := "pending"
		if action.Silent {
			report = "skipped"
		}
		run = ActionRun{ActionRunSummary: ActionRunSummary{
			ID: uuid.NewString(), ConversationID: conversationID, Project: project.Name,
			ActionID: action.ID, Label: action.Label, Status: "running", StartedAt: tx.Now.UTC(), Report: report,
		}, Command: action.Command}
		return q.CreateActionRun(ctx, generated.CreateActionRunParams{
			ID: run.ID, ConversationID: run.ConversationID, Profile: run.Project, ActionID: run.ActionID, Label: run.Label,
			Command: run.Command, StartedAt: formatTime(run.StartedAt), ArchiveOnSuccess: action.ArchiveOnSuccess, Report: report,
		})
	})
	return &run, &action, cwd, err
}

// Times are stored as RFC 3339 text.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

func parseOptionalTime(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := parseTime(*s)
	return &t, err
}

func newActionRunSummary(row generated.ListWorkspaceActionRunsRow) (ActionRunSummary, error) {
	run := ActionRunSummary{
		ID: row.ID, ConversationID: row.ConversationID, Project: row.Profile, ActionID: row.ActionID,
		Label: row.Label, Status: row.Status, Report: row.Report,
	}
	if row.ExitCode != nil {
		run.ExitCode = ptr(int(*row.ExitCode))
	}
	var err1, err2 error
	run.StartedAt, err1 = parseTime(row.StartedAt)
	run.FinishedAt, err2 = parseOptionalTime(row.FinishedAt)
	return run, errors.Join(err1, err2)
}

func newActionRun(row generated.ActionRun) (*ActionRun, error) {
	summary, err := newActionRunSummary(generated.ListWorkspaceActionRunsRow{
		ID: row.ID, ConversationID: row.ConversationID, Profile: row.Profile, ActionID: row.ActionID, Label: row.Label,
		Status: row.Status, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, ExitCode: row.ExitCode, Report: row.Report,
	})
	if err != nil {
		return nil, err
	}
	return &ActionRun{ActionRunSummary: summary, Command: row.Command, Output: row.Output, Error: row.Error, Archived: row.Archived}, nil
}

func (db *DB) GetActionRun(ctx context.Context, id string) (*ActionRun, error) {
	var run *ActionRun
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row, err := generated.New(rx.Conn()).GetActionRun(ctx, id)
		if err != nil {
			return err
		}
		run, err = newActionRun(row)
		return err
	})
	return run, err
}

// BoundedOutput retains the recent tail, keeping valid UTF-8 and a strict
// byte bound even when a subprocess emits binary data or enormous chunks.
func BoundedOutput(output string) string {
	if len(output) > MaxActionOutput {
		output = output[len(output)-MaxActionOutput:]
	}
	output = strings.ToValidUTF8(output, "\uFFFD")
	if len(output) > MaxActionOutput {
		output = output[len(output)-MaxActionOutput:]
		for len(output) > 0 && !utf8.RuneStart(output[0]) {
			output = output[1:]
		}
	}
	return output
}

func (db *DB) AppendActionOutput(ctx context.Context, id, output string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		run, err := q.GetActionRun(ctx, id)
		if err != nil {
			return err
		}
		if run.Status != "running" {
			return ErrConflict
		}
		return q.SetActionRunOutput(ctx, generated.SetActionRunOutputParams{Output: BoundedOutput(run.Output + output), ID: id})
	})
}

// FinishAction commits success and archive together. A previously
// interrupted run cannot later archive its conversation, even if an old process exits.
func (db *DB) FinishAction(ctx context.Context, id string, exitCode *int, runError string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		run, err := q.GetActionRun(ctx, id)
		if err != nil {
			return err
		}
		if run.Status != "running" {
			return ErrConflict
		}
		status := "failed"
		if exitCode != nil && *exitCode == 0 && runError == "" {
			status = "succeeded"
		}
		var exit *int64
		if exitCode != nil {
			exit = ptr(int64(*exitCode))
		}
		if err := q.FinishActionRun(ctx, generated.FinishActionRunParams{
			Status: status, Error: BoundedOutput(runError), FinishedAt: ptr(formatTime(tx.Now)), ExitCode: exit, ID: id,
		}); err != nil {
			return err
		}
		if status != "succeeded" || !run.ArchiveOnSuccess {
			return nil
		}
		conversation, err := q.GetConversation(ctx, run.ConversationID)
		if err != nil {
			return err
		}
		pending, err := pendingWork(ctx, q, &conversation)
		if err != nil {
			return err
		}
		if pending {
			return q.SetActionRunOutput(ctx, generated.SetActionRunOutputParams{
				Output: BoundedOutput(run.Output + "\nCommand succeeded; not archived because new work is pending.\n"), ID: id,
			})
		}
		if _, err := q.ArchiveConversation(ctx, run.ConversationID); err != nil {
			return err
		}
		return q.MarkActionRunArchived(ctx, id)
	})
}

func (db *DB) InterruptActionRuns(ctx context.Context) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return generated.New(tx.Conn()).InterruptRunningActionRuns(ctx, ptr(formatTime(tx.Now)))
	})
}

func (db *DB) InterruptActionRun(ctx context.Context, id, reason string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return rowsChanged(generated.New(tx.Conn()).InterruptActionRun(ctx, generated.InterruptActionRunParams{
			Error: BoundedOutput(reason), FinishedAt: ptr(formatTime(tx.Now)), ID: id,
		}))
	})
}

func (db *DB) GetWorkspace(ctx context.Context) (*Workspace, error) {
	workspace := &Workspace{Runs: []ActionRunSummary{}}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		workspace.AssistantID, err = q.GetAssistantID(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if workspace.Projects, err = listProjects(ctx, q); err != nil {
			return err
		}
		if workspace.VMs, err = listConversationVMs(ctx, q, true); err != nil {
			return err
		}
		if workspace.BaseVMs, err = listProjectBaseVMs(ctx, q, ""); err != nil {
			return err
		}
		if workspace.Conversations, err = q.ListWorkspaceConversations(ctx); err != nil {
			return err
		}
		rows, err := q.ListWorkspaceActionRuns(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			run, err := newActionRunSummary(row)
			if err != nil {
				return err
			}
			workspace.Runs = append(workspace.Runs, run)
		}
		return nil
	})
	return workspace, err
}
