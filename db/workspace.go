package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"shelley.exe.dev/db/generated"
)

var (
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid input")
)

// WorkspaceConversation is a conversation's place in the workspace: the project its profile
// names, or the Assistant.
type WorkspaceConversation struct {
	ConversationID string `json:"conversation_id"`
	Project        string `json:"project"`
	IsAssistant    bool   `json:"is_assistant"`
	Archived       bool   `json:"archived"`
}

type Workspace struct {
	AssistantID   string                  `json:"assistant_id"`
	Projects      []Project               `json:"projects"`
	Conversations []WorkspaceConversation `json:"conversations"`
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
// project moves atomic even with concurrent requests.
func conversationAvailable(ctx context.Context, tx *Tx, conversationID string) (*generated.Conversation, error) {
	conversation, err := generated.New(tx.Conn()).GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	var assistant bool
	if err := tx.QueryRow("SELECT ? IS "+workspaceAssistant, conversationID).Scan(&assistant); err != nil {
		return nil, err
	}
	pending, err := pendingWork(tx, &conversation)
	if err != nil {
		return nil, err
	}
	if assistant || !conversation.UserInitiated ||
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

func (db *DB) GetWorkspace(ctx context.Context) (*Workspace, error) {
	workspace := &Workspace{Conversations: []WorkspaceConversation{}}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		if err := rx.QueryRow("SELECT COALESCE(" + workspaceAssistant + ",'')").Scan(&workspace.AssistantID); err != nil {
			return err
		}
		var err error
		workspace.Projects, err = listProjects(rx)
		if err != nil {
			return err
		}
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
	})
	return workspace, err
}
