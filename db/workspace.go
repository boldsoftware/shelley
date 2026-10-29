package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
// names, or the Assistant.
type WorkspaceConversation struct {
	ConversationID string `json:"conversation_id"`
	Project        string `json:"project"`
	IsAssistant    bool   `json:"is_assistant"`
	Archived       bool   `json:"archived"`
}

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
}

// EnsureWorkspace serializes singleton creation with all other DB writes.
// Both the assistant row and its identity are committed together, never as an
// orphan created outside the transaction.
func (db *DB) EnsureWorkspace(ctx context.Context, model string, opts ConversationOptions) (string, error) {
	var assistantID string
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		err := tx.QueryRow("SELECT assistant_id FROM workspace WHERE singleton = 1").Scan(&assistantID)
		if errors.Is(err, sql.ErrNoRows) {
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
			// No cwd is invented for the assistant; its tools use the normal
			// configured server working directory.
			if _, err := generated.New(tx.Conn()).CreateConversation(ctx, generated.CreateConversationParams{
				ConversationID: assistantID, UserInitiated: true, Model: modelPtr, ConversationOptions: string(options),
			}); err != nil {
				return err
			}
			_, err = tx.Exec("INSERT INTO workspace(singleton,assistant_id) VALUES(1,?)", assistantID)
		}
		return err
	})
	return assistantID, err
}

type rowScanner interface{ Scan(...any) error }

func rowsChanged(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// WorkspaceFlag names the feature flag that turns on workspace mode: a
// pinned Assistant that routes work into projects.
const WorkspaceFlag = "workspace"

const workspaceOn = `EXISTS(SELECT 1 FROM feature_flags WHERE name='` + WorkspaceFlag + `' AND trim(value)='true')`

// workspaceAssistant is the Assistant's conversation id, or NULL: there is
// none until workspace mode creates it, and none while the mode is off.
const workspaceAssistant = `(SELECT assistant_id FROM workspace WHERE singleton=1 AND ` + workspaceOn + `)`

// WorkspaceMode reports whether workspace mode is on.
func (db *DB) WorkspaceMode(ctx context.Context) (bool, error) {
	var on bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT " + workspaceOn).Scan(&on)
	})
	return on, err
}

// workspaceConversationQuery lists the workspace's conversations: top-level conversations
// whose profile is a project, and the Assistant. Callers filter with AND.
const workspaceConversationQuery = `SELECT conversation_id,project,is_assistant,archived FROM (
	SELECT c.conversation_id,
		CASE WHEN c.conversation_id IS ` + workspaceAssistant + ` THEN '' ELSE COALESCE(p.profile,'') END AS project,
		c.conversation_id IS ` + workspaceAssistant + ` AS is_assistant, c.archived
	FROM conversations c
	LEFT JOIN profile_projects p ON p.profile=` + conversationProfile + `
	WHERE c.parent_conversation_id IS NULL
) WHERE (project!='' OR is_assistant)`

// conversationProfile is the profile label of conversation c.
const conversationProfile = `(CASE WHEN json_valid(c.conversation_options) THEN json_extract(c.conversation_options,'$.profile') END)`

func scanWorkspaceConversation(scanner rowScanner) (*WorkspaceConversation, error) {
	var c WorkspaceConversation
	if err := scanner.Scan(&c.ConversationID, &c.Project, &c.IsAssistant, &c.Archived); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetWorkspaceConversation returns sql.ErrNoRows for conversations that are neither
// in a project nor the Assistant.
func (db *DB) GetWorkspaceConversation(ctx context.Context, conversationID string) (*WorkspaceConversation, error) {
	var c *WorkspaceConversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		c, err = scanWorkspaceConversation(rx.QueryRow(workspaceConversationQuery+" AND conversation_id=?", conversationID))
		return err
	})
	return c, err
}

// conversationAvailable is always called within the writing transaction, making
// duplicate actions and project moves atomic even with concurrent requests.
func conversationAvailable(ctx context.Context, tx *Tx, conversationID string) (*generated.Conversation, error) {
	conversation, err := generated.New(tx.Conn()).GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	var assistant bool
	if err := tx.QueryRow("SELECT ? IS "+workspaceAssistant, conversationID).Scan(&assistant); err != nil {
		return nil, err
	}
	running, err := hasRunningAction(tx.Rx, conversationID)
	if err != nil {
		return nil, err
	}
	pending, err := pendingWork(tx, &conversation)
	if err != nil {
		return nil, err
	}
	if assistant || running || !conversation.UserInitiated ||
		conversation.ParentConversationID != nil || conversation.ExternalConversationID != nil || pending {
		return nil, ErrConflict
	}
	return &conversation, nil
}

func pendingWork(tx *Tx, conversation *generated.Conversation) (bool, error) {
	queued, err := ParseQueuedMessagesStrict(conversation.QueuedMessages)
	if err != nil {
		return false, err
	}
	var dependentWork bool
	if err := tx.QueryRow(`WITH RECURSIVE family(id) AS (
		SELECT ? UNION SELECT c.conversation_id FROM conversations c JOIN family f ON c.parent_conversation_id=f.id
	) SELECT EXISTS(
		SELECT 1 FROM conversations c JOIN family f ON c.conversation_id=f.id
		WHERE c.conversation_id != ? AND (c.agent_working OR c.turn_interrupted OR c.queued_messages NOT IN ('[]','','null'))
		UNION ALL
		SELECT 1 FROM background_jobs b JOIN family f ON b.conversation_id=f.id WHERE NOT b.exited OR NOT b.notified
	)`, conversation.ConversationID, conversation.ConversationID).Scan(&dependentWork); err != nil {
		return false, err
	}
	return conversation.AgentWorking || len(queued) != 0 || conversation.TurnInterrupted || dependentWork, nil
}

// MoveConversation puts an idle conversation in project by relabeling its profile. Like any
// profile switch by label, a conversation that hasn't edited its settings takes the
// project's at its next turn; one that has keeps its own.
func (db *DB) MoveConversation(ctx context.Context, conversationID, project string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := conversationAvailable(ctx, tx, conversationID); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRow("SELECT 1 FROM profile_projects WHERE profile=?", project).Scan(&exists); err != nil {
			return err
		}
		return setConversationProfile(tx, conversationID, project)
	})
}

func setConversationProfile(tx *Tx, conversationID, profile string) error {
	_, err := tx.Exec(`UPDATE conversations SET conversation_options=json_set(
		CASE WHEN json_valid(conversation_options) THEN conversation_options ELSE '{}' END,'$.profile',?)
		WHERE conversation_id=?`, profile, conversationID)
	return err
}

// GetAssistantID returns sql.ErrNoRows without an Assistant: before
// workspace mode creates it, or while the mode is off.
func (db *DB) GetAssistantID(ctx context.Context) (string, error) {
	var id string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT assistant_id FROM workspace WHERE singleton=1 AND " + workspaceOn).Scan(&id)
	})
	return id, err
}

func hasRunningAction(rx *Rx, conversationID string) (bool, error) {
	var running bool
	err := rx.QueryRow("SELECT EXISTS(SELECT 1 FROM action_runs WHERE conversation_id=? AND status='running')", conversationID).Scan(&running)
	return running, err
}

func (db *DB) HasRunningAction(ctx context.Context, conversationID string) (bool, error) {
	var running bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		running, err = hasRunningAction(rx, conversationID)
		return err
	})
	return running, err
}

func (db *DB) HasAnyRunningAction(ctx context.Context) (bool, error) {
	var running bool
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT EXISTS(SELECT 1 FROM action_runs WHERE status='running')").Scan(&running)
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
		conversation, err := conversationAvailable(ctx, tx, conversationID)
		if err != nil {
			return err
		}
		if conversation.Archived {
			return fmt.Errorf("%w: the conversation is archived", ErrConflict)
		}
		project, err := getProject(tx.Rx, "(SELECT project FROM ("+workspaceConversationQuery+" AND conversation_id=?))", conversationID)
		if err != nil {
			return err
		}
		found := false
		for _, candidate := range project.Actions {
			if candidate.ID == actionID {
				action = candidate
				found = true
				break
			}
		}
		if !found {
			return sql.ErrNoRows
		}
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
		_, err = tx.Exec(`INSERT INTO action_runs
			(id,conversation_id,profile,action_id,label,command,status,started_at,archive_on_success,report)
			VALUES(?,?,?,?,?,?,'running',?,?,?)`, run.ID, run.ConversationID, run.Project, run.ActionID, run.Label, run.Command,
			run.StartedAt.Format(time.RFC3339Nano), action.ArchiveOnSuccess, report)
		return err
	})
	return &run, &action, cwd, err
}

const (
	actionRunSummaryColumns = `id,conversation_id,profile,action_id,label,status,started_at,finished_at,exit_code,report`
	actionRunColumns        = actionRunSummaryColumns + `,command,output,error,archived`
)

func scanActionRunSummary(run *ActionRunSummary, scanner rowScanner, more ...any) error {
	var started string
	var finished sql.NullString
	if err := scanner.Scan(append([]any{
		&run.ID, &run.ConversationID, &run.Project, &run.ActionID, &run.Label, &run.Status,
		&started, &finished, &run.ExitCode, &run.Report,
	}, more...)...); err != nil {
		return err
	}
	var err error
	run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return err
	}
	if finished.Valid {
		at, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return err
		}
		run.FinishedAt = &at
	}
	return nil
}

func scanActionRun(scanner rowScanner) (*ActionRun, error) {
	var run ActionRun
	if err := scanActionRunSummary(&run.ActionRunSummary, scanner, &run.Command, &run.Output, &run.Error, &run.Archived); err != nil {
		return nil, err
	}
	return &run, nil
}

func (db *DB) GetActionRun(ctx context.Context, id string) (*ActionRun, error) {
	var run *ActionRun
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		run, err = scanActionRun(rx.QueryRow("SELECT "+actionRunColumns+" FROM action_runs WHERE id=?", id))
		return err
	})
	return run, err
}

func (db *DB) listActionRuns(ctx context.Context, where string, args ...any) ([]ActionRun, error) {
	var runs []ActionRun
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query("SELECT "+actionRunColumns+" FROM action_runs "+where+" ORDER BY started_at,id", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanActionRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, *run)
		}
		return rows.Err()
	})
	return runs, err
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
		var existing string
		var status string
		if err := tx.QueryRow("SELECT output,status FROM action_runs WHERE id=?", id).Scan(&existing, &status); err != nil {
			return err
		}
		if status != "running" {
			return ErrConflict
		}
		_, err := tx.Exec("UPDATE action_runs SET output=? WHERE id=?", BoundedOutput(existing+output), id)
		return err
	})
}

// FinishAction commits success and archive together. A previously
// interrupted run cannot later archive its conversation, even if an old process exits.
func (db *DB) FinishAction(ctx context.Context, id string, exitCode *int, runError string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var conversationID, status string
		var archive bool
		if err := tx.QueryRow("SELECT conversation_id,status,archive_on_success FROM action_runs WHERE id=?", id).Scan(&conversationID, &status, &archive); err != nil {
			return err
		}
		if status != "running" {
			return ErrConflict
		}
		status = "failed"
		if exitCode != nil && *exitCode == 0 && runError == "" {
			status = "succeeded"
		}
		if _, err := tx.Exec("UPDATE action_runs SET status=?,error=?,finished_at=?,exit_code=? WHERE id=?",
			status, BoundedOutput(runError), tx.Now.UTC().Format(time.RFC3339Nano), exitCode, id); err != nil {
			return err
		}
		if status == "succeeded" && archive {
			conversation, err := generated.New(tx.Conn()).GetConversation(ctx, conversationID)
			if err != nil {
				return err
			}
			pending, err := pendingWork(tx, &conversation)
			if err != nil {
				return err
			}
			if pending {
				var output string
				if err := tx.QueryRow("SELECT output FROM action_runs WHERE id=?", id).Scan(&output); err != nil {
					return err
				}
				_, err := tx.Exec("UPDATE action_runs SET output=? WHERE id=?",
					BoundedOutput(output+"\nCommand succeeded; not archived because new work is pending.\n"), id)
				return err
			}
			if _, err := generated.New(tx.Conn()).ArchiveConversation(ctx, conversationID); err != nil {
				return err
			}
			_, err = tx.Exec("UPDATE action_runs SET archived=TRUE WHERE id=?", id)
			return err
		}
		return nil
	})
}

func (db *DB) InterruptActionRuns(ctx context.Context) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`UPDATE action_runs SET status='interrupted',error='Server restarted; command outcome is unknown. The conversation was not archived.',finished_at=?
			WHERE status='running'`, tx.Now.UTC().Format(time.RFC3339Nano))
		return err
	})
}

func (db *DB) InterruptActionRun(ctx context.Context, id, reason string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		result, err := tx.Exec(`UPDATE action_runs SET status='interrupted',error=?,finished_at=?
			WHERE id=? AND status='running'`, BoundedOutput(reason), tx.Now.UTC().Format(time.RFC3339Nano), id)
		return rowsChanged(result, err)
	})
}

func (db *DB) GetWorkspace(ctx context.Context) (*Workspace, error) {
	workspace := &Workspace{Conversations: []WorkspaceConversation{}, Runs: []ActionRunSummary{}}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		if err := rx.QueryRow("SELECT COALESCE(" + workspaceAssistant + ",'')").Scan(&workspace.AssistantID); err != nil {
			return err
		}
		var err error
		workspace.Projects, err = listProjects(rx)
		if err != nil {
			return err
		}
		if err := func() error {
			rows, err := rx.Query(workspaceConversationQuery + " ORDER BY conversation_id")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				c, err := scanWorkspaceConversation(rows)
				if err != nil {
					return err
				}
				workspace.Conversations = append(workspace.Conversations, *c)
			}
			return rows.Err()
		}(); err != nil {
			return err
		}
		// Running runs, recent ones, and each conversation's latest, which its
		// transcript shows until it reports. GET /api/action-runs/{id} has the rest.
		rows, err := rx.Query("SELECT " + actionRunSummaryColumns + ` FROM action_runs
			WHERE status='running' OR id IN (
				SELECT id FROM action_runs WHERE status!='running' ORDER BY started_at DESC,id DESC LIMIT 20
			) OR id=(
				SELECT latest.id FROM action_runs latest
				WHERE latest.conversation_id=action_runs.conversation_id
				ORDER BY latest.started_at DESC,latest.id DESC LIMIT 1
			) ORDER BY started_at,id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var run ActionRunSummary
			if err := scanActionRunSummary(&run, rows); err != nil {
				return err
			}
			workspace.Runs = append(workspace.Runs, run)
		}
		return rows.Err()
	})
	return workspace, err
}
