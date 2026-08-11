package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MemoryItem is a durable, source-attributed fact or lesson available to later
// conversations. Content is always treated as untrusted reference material.
type MemoryItem struct {
	ID                   string    `json:"id"`
	Scope                string    `json:"scope"`
	ProjectPath          string    `json:"project_path"`
	Kind                 string    `json:"kind"`
	Title                string    `json:"title"`
	Content              string    `json:"content"`
	Confidence           float64   `json:"confidence"`
	SourceConversationID string    `json:"source_conversation_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// MemoryDraft is the validated input used by memory and Dream writes.
type MemoryDraft struct {
	Scope       string  `json:"scope"`
	ProjectPath string  `json:"project_path"`
	Kind        string  `json:"kind"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Confidence  float64 `json:"confidence"`
}

// TaskCheckpoint is an append-only recovery snapshot.
type TaskCheckpoint struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	EventType      string          `json:"event_type"`
	Summary        string          `json:"summary"`
	State          json.RawMessage `json:"state"`
	CreatedAt      time.Time       `json:"created_at"`
}

func (db *DB) SaveMemory(ctx context.Context, conversationID string, draft MemoryDraft) (MemoryItem, error) {
	id := uuid.NewString()
	now := time.Now().UTC()
	if draft.Confidence == 0 {
		draft.Confidence = 1
	}
	var item MemoryItem
	var source sql.NullString
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.QueryRow(`INSERT INTO memory_items
			(id, scope, project_path, kind, title, content, confidence, source_conversation_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
			ON CONFLICT(scope, project_path, kind, title) DO UPDATE SET
				content = excluded.content,
				confidence = excluded.confidence,
				source_conversation_id = excluded.source_conversation_id,
				updated_at = excluded.updated_at
			RETURNING id, scope, project_path, kind, title, content, confidence,
				source_conversation_id, created_at, updated_at`,
			id, draft.Scope, draft.ProjectPath, draft.Kind, draft.Title, draft.Content,
			draft.Confidence, conversationID, now, now).Scan(&item.ID, &item.Scope,
			&item.ProjectPath, &item.Kind, &item.Title, &item.Content, &item.Confidence,
			&source, &item.CreatedAt, &item.UpdatedAt)
	})
	if err != nil {
		return MemoryItem{}, fmt.Errorf("save memory: %w", err)
	}
	item.SourceConversationID = source.String
	return item, nil
}

func memoryFTSQuery(query string) string {
	words := strings.Fields(query)
	if len(words) > 12 {
		words = words[:12]
	}
	for i, word := range words {
		words[i] = `"` + strings.ReplaceAll(word, `"`, `""`) + `"`
	}
	return strings.Join(words, " OR ")
}

func scanMemory(rows *sql.Rows) ([]MemoryItem, error) {
	var out []MemoryItem
	for rows.Next() {
		var m MemoryItem
		var source sql.NullString
		if err := rows.Scan(&m.ID, &m.Scope, &m.ProjectPath, &m.Kind, &m.Title,
			&m.Content, &m.Confidence, &source, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.SourceConversationID = source.String
		out = append(out, m)
	}
	return out, rows.Err()
}

// SearchMemories searches global memories and memories for the exact project root.
func (db *DB) SearchMemories(ctx context.Context, projectPath, query string, limit int) ([]MemoryItem, error) {
	if limit < 1 || limit > 20 {
		limit = 8
	}
	var out []MemoryItem
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var rows *sql.Rows
		var err error
		if fts := memoryFTSQuery(query); fts != "" {
			rows, err = rx.Query(`SELECT m.id, m.scope, m.project_path, m.kind, m.title,
				m.content, m.confidence, m.source_conversation_id, m.created_at, m.updated_at
				FROM memory_items_fts f JOIN memory_items m ON m.rowid = f.rowid
				WHERE memory_items_fts MATCH ? AND (m.scope = 'global' OR (m.scope = 'project' AND m.project_path = ?))
				ORDER BY rank, m.updated_at DESC LIMIT ?`, fts, projectPath, limit)
		} else {
			rows, err = rx.Query(`SELECT id, scope, project_path, kind, title, content,
				confidence, source_conversation_id, created_at, updated_at FROM memory_items
				WHERE scope = 'global' OR (scope = 'project' AND project_path = ?)
				ORDER BY updated_at DESC LIMIT ?`, projectPath, limit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		out, err = scanMemory(rows)
		return err
	})
	return out, err
}

func (db *DB) AppendTaskCheckpoint(ctx context.Context, conversationID, eventType, summary string, state json.RawMessage) (TaskCheckpoint, error) {
	if len(eventType) == 0 || len(eventType) > 40 || len(summary) == 0 || len(summary) > 1000 || len(state) > 16*1024 {
		return TaskCheckpoint{}, fmt.Errorf("checkpoint exceeds size limit or is incomplete")
	}
	id := uuid.NewString()
	now := time.Now().UTC()
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}
	if !json.Valid(state) {
		return TaskCheckpoint{}, fmt.Errorf("checkpoint state must be valid JSON")
	}
	err := db.pool.Exec(ctx, `INSERT INTO task_journal
		(id, conversation_id, event_type, summary, state_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, conversationID, eventType, summary, string(state), now)
	if err != nil {
		return TaskCheckpoint{}, fmt.Errorf("append task checkpoint: %w", err)
	}
	return TaskCheckpoint{ID: id, ConversationID: conversationID, EventType: eventType,
		Summary: summary, State: state, CreatedAt: now}, nil
}

func (db *DB) LatestTaskCheckpoint(ctx context.Context, conversationID string) (*TaskCheckpoint, error) {
	var cp TaskCheckpoint
	var state string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`SELECT id, conversation_id, event_type, summary, state_json, created_at
			FROM task_journal WHERE conversation_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`,
			conversationID).Scan(&cp.ID, &cp.ConversationID, &cp.EventType, &cp.Summary, &state, &cp.CreatedAt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	cp.State = json.RawMessage(state)
	return &cp, nil
}

func (db *DB) ConsolidateDreamJSON(ctx context.Context, conversationID, projectPath, summary string, raw []byte) (int, error) {
	var memories []MemoryDraft
	if err := json.Unmarshal(raw, &memories); err != nil {
		return 0, fmt.Errorf("decode dream memories: %w", err)
	}
	if len(memories) > 8 || len(summary) > 2000 {
		return 0, fmt.Errorf("dream exceeds size limit")
	}
	now := time.Now().UTC()
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		for _, memory := range memories {
			validKind := memory.Kind == "fact" || memory.Kind == "decision" || memory.Kind == "preference" || memory.Kind == "lesson"
			if memory.Scope != "project" || (memory.ProjectPath != "" && memory.ProjectPath != projectPath) ||
				!validKind || memory.Title == "" || memory.Content == "" ||
				len(memory.Title) > 200 || len(memory.Content) > 4000 ||
				memory.Confidence < 0 || memory.Confidence > 1 {
				return fmt.Errorf("invalid dream memory")
			}
			if _, err := tx.Exec(`INSERT INTO memory_items
				(id, scope, project_path, kind, title, content, confidence, source_conversation_id, created_at, updated_at)
				VALUES (?, 'project', ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(scope, project_path, kind, title) DO UPDATE SET
					content = excluded.content, confidence = excluded.confidence,
					source_conversation_id = excluded.source_conversation_id, updated_at = excluded.updated_at`,
				uuid.NewString(), projectPath, memory.Kind, memory.Title, memory.Content,
				memory.Confidence, conversationID, now, now); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO dream_runs
			(id, conversation_id, project_path, summary, memory_count) VALUES (?, ?, ?, ?, ?)`,
			uuid.NewString(), conversationID, projectPath, summary, len(memories))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("consolidate dream: %w", err)
	}
	return len(memories), nil
}

// The JSON/primitive wrappers implement claudetool.ExperienceStore without
// making the tool package depend on database types.
func (db *DB) SearchMemoriesJSON(ctx context.Context, projectPath, query string, limit int) ([]byte, error) {
	items, err := db.SearchMemories(ctx, projectPath, query, limit)
	if err != nil {
		return nil, err
	}
	return json.Marshal(items)
}

func (db *DB) SaveMemoryFields(ctx context.Context, conversationID, scope, projectPath, kind, title, content string, confidence float64) (string, error) {
	item, err := db.SaveMemory(ctx, conversationID, MemoryDraft{Scope: scope, ProjectPath: projectPath,
		Kind: kind, Title: title, Content: content, Confidence: confidence})
	return item.ID, err
}

func (db *DB) AppendCheckpointJSON(ctx context.Context, conversationID, eventType, summary string, state []byte) (string, error) {
	checkpoint, err := db.AppendTaskCheckpoint(ctx, conversationID, eventType, summary, json.RawMessage(state))
	return checkpoint.ID, err
}

func (db *DB) LatestCheckpointJSON(ctx context.Context, conversationID string) ([]byte, error) {
	checkpoint, err := db.LatestTaskCheckpoint(ctx, conversationID)
	if err != nil || checkpoint == nil {
		return nil, err
	}
	return json.Marshal(checkpoint)
}
