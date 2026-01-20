// Package db provides database operations for the Shelley AI coding agent.
package db

//go:generate go tool github.com/sqlc-dev/sqlc/cmd/sqlc generate -f ../sqlc.yaml

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"

	_ "modernc.org/sqlite"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// generateConversationID generates a conversation ID in the format "cXXXXXX"
// where X are random alphanumeric characters
func generateConversationID() (string, error) {
	text := rand.Text()
	if len(text) < 6 {
		return "", fmt.Errorf("rand.Text() returned insufficient characters: %d", len(text))
	}
	return "c" + text[:6], nil
}

// DB wraps the database connection pool and provides high-level operations
type DB struct {
	pool *Pool
}

// Config holds database configuration
type Config struct {
	DSN string // Data Source Name for SQLite database
}

// New creates a new database connection with the given configuration
func New(cfg Config) (*DB, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("database DSN cannot be empty")
	}

	if cfg.DSN == ":memory:" {
		return nil, fmt.Errorf(":memory: database not supported (requires multiple connections); use a temp file")
	}

	// Ensure directory exists for file-based SQLite databases
	if cfg.DSN != ":memory:" {
		dir := filepath.Dir(cfg.DSN)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("failed to create database directory: %w", err)
			}
		}
	}

	// Create connection pool with 3 readers
	dsn := cfg.DSN
	if !strings.Contains(dsn, "?") {
		dsn += "?_foreign_keys=on"
	} else if !strings.Contains(dsn, "_foreign_keys") {
		dsn += "&_foreign_keys=on"
	}

	pool, err := NewPool(dsn, 3)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	return &DB{
		pool: pool,
	}, nil
}

// Close closes the database connection pool
func (db *DB) Close() error {
	return db.pool.Close()
}

// Migrate runs the database migrations
func (db *DB) Migrate(ctx context.Context) error {
	// Read all migration files
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	// Filter and validate migration files
	var migrations []string
	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !migrationPattern.MatchString(entry.Name()) {
			continue
		}
		migrations = append(migrations, entry.Name())
	}

	// Sort migrations by number
	sort.Strings(migrations)

	// Get executed migrations
	executedMigrations := make(map[int]bool)
	var tableName string
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row := rx.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='migrations'")
		return row.Scan(&tableName)
	})

	if err == nil {
		// Migrations table exists, load executed migrations
		err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			rows, err := rx.Query("SELECT migration_number FROM migrations")
			if err != nil {
				return fmt.Errorf("failed to query executed migrations: %w", err)
			}
			defer rows.Close()

			for rows.Next() {
				var migrationNumber int
				if err := rows.Scan(&migrationNumber); err != nil {
					return fmt.Errorf("failed to scan migration number: %w", err)
				}
				executedMigrations[migrationNumber] = true
			}
			return rows.Err()
		})
		if err != nil {
			return fmt.Errorf("failed to load executed migrations: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		// Migrations table doesn't exist - executedMigrations remains empty
		slog.Info("migrations table not found, running all migrations")
	}

	// Run any migrations that haven't been executed
	for _, migration := range migrations {
		// Extract migration number from filename (e.g., "001-base.sql" -> 001)
		matches := migrationPattern.FindStringSubmatch(migration)
		if len(matches) != 2 {
			return fmt.Errorf("invalid migration filename format: %s", migration)
		}

		migrationNumber, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("failed to parse migration number from %s: %w", migration, err)
		}

		if !executedMigrations[migrationNumber] {
			slog.Info("running migration", "file", migration, "number", migrationNumber)
			if err := db.executeMigration(ctx, migration); err != nil {
				return fmt.Errorf("failed to execute migration %s: %w", migration, err)
			}

			err = db.pool.Exec(ctx, "INSERT INTO migrations (migration_number, migration_name) VALUES (?, ?)", migrationNumber, migration)
			if err != nil {
				return fmt.Errorf("failed to record migration %s in migrations table: %w", migration, err)
			}
		}
	}

	return nil
}

// executeMigration executes a single migration file
func (db *DB) executeMigration(ctx context.Context, filename string) error {
	content, err := schemaFS.ReadFile("schema/" + filename)
	if err != nil {
		return fmt.Errorf("failed to read migration file %s: %w", filename, err)
	}

	if err := db.pool.Exec(ctx, string(content)); err != nil {
		return fmt.Errorf("failed to execute migration %s: %w", filename, err)
	}

	return nil
}

// Pool returns the underlying connection pool for advanced operations
func (db *DB) Pool() *Pool {
	return db.pool
}

// WithTx runs a function within a database transaction
func (db *DB) WithTx(ctx context.Context, fn func(*generated.Queries) error) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		queries := generated.New(tx.Conn())
		return fn(queries)
	})
}

// WithTxRes runs a function within a database transaction and returns a value
func WithTxRes[T any](db *DB, ctx context.Context, fn func(*generated.Queries) (T, error)) (T, error) {
	var result T
	err := db.WithTx(ctx, func(queries *generated.Queries) error {
		var err error
		result, err = fn(queries)
		return err
	})
	return result, err
}

// Conversation methods (moved from ConversationService)

// CreateConversation creates a new conversation with an optional slug
func (db *DB) CreateConversation(ctx context.Context, slug *string, userInitiated bool, cwd *string) (*generated.Conversation, error) {
	conversationID, err := generateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID: conversationID,
			Slug:           slug,
			UserInitiated:  userInitiated,
			Cwd:            cwd,
		})
		return err
	})
	return &conversation, err
}

// GetConversationByID retrieves a conversation by its ID
func (db *DB) GetConversationByID(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	return &conversation, err
}

// GetConversationBySlug retrieves a conversation by its slug
func (db *DB) GetConversationBySlug(ctx context.Context, slug string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversation, err = q.GetConversationBySlug(ctx, &slug)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found with slug: %s", slug)
	}
	return &conversation, err
}

// ListConversations retrieves conversations with pagination
func (db *DB) ListConversations(ctx context.Context, limit, offset int64) ([]generated.Conversation, error) {
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.ListConversations(ctx, generated.ListConversationsParams{
			Limit:  limit,
			Offset: offset,
		})
		return err
	})
	return conversations, err
}

// SearchConversations searches for conversations containing the given query in their slug
func (db *DB) SearchConversations(ctx context.Context, query string, limit, offset int64) ([]generated.Conversation, error) {
	queryPtr := &query
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.SearchConversations(ctx, generated.SearchConversationsParams{
			Column1: queryPtr,
			Limit:   limit,
			Offset:  offset,
		})
		return err
	})
	return conversations, err
}

// SearchConversationsWithMessages searches for conversations containing the query in slug or message content
func (db *DB) SearchConversationsWithMessages(ctx context.Context, query string, limit, offset int64) ([]generated.Conversation, error) {
	queryPtr := &query
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.SearchConversationsWithMessages(ctx, generated.SearchConversationsWithMessagesParams{
			Column1: queryPtr,
			Column2: queryPtr,
			Column3: queryPtr,
			Limit:   limit,
			Offset:  offset,
		})
		return err
	})
	return conversations, err
}

// UpdateConversationSlug updates the slug of a conversation
func (db *DB) UpdateConversationSlug(ctx context.Context, conversationID, slug string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
			Slug:           &slug,
			ConversationID: conversationID,
		})
		return err
	})
	return &conversation, err
}

// UpdateConversationCwd updates the working directory for a conversation
func (db *DB) UpdateConversationCwd(ctx context.Context, conversationID, cwd string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		_, err := q.UpdateConversationCwd(ctx, generated.UpdateConversationCwdParams{
			Cwd:            &cwd,
			ConversationID: conversationID,
		})
		return err
	})
}

// Message methods (moved from MessageService)

// MessageType represents the type of message
type MessageType string

const (
	MessageTypeUser    MessageType = "user"
	MessageTypeAgent   MessageType = "agent"
	MessageTypeTool    MessageType = "tool"
	MessageTypeSystem  MessageType = "system"
	MessageTypeError   MessageType = "error"
	MessageTypeGitInfo MessageType = "gitinfo" // user-visible only, not sent to LLM
)

// CreateMessageParams contains parameters for creating a message
type CreateMessageParams struct {
	ConversationID string
	Type           MessageType
	LLMData        interface{} // Will be JSON marshalled
	UserData       interface{} // Will be JSON marshalled
	UsageData      interface{} // Will be JSON marshalled
	DisplayData    interface{} // Will be JSON marshalled, tool-specific display content
}

// CreateMessage creates a new message
func (db *DB) CreateMessage(ctx context.Context, params CreateMessageParams) (*generated.Message, error) {
	messageID := uuid.New().String()

	// Marshal JSON fields
	var llmDataJSON, userDataJSON, usageDataJSON, displayDataJSON *string

	if params.LLMData != nil {
		data, err := json.Marshal(params.LLMData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal LLM data: %w", err)
		}
		str := string(data)
		llmDataJSON = &str
	}

	if params.UserData != nil {
		data, err := json.Marshal(params.UserData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal user data: %w", err)
		}
		str := string(data)
		userDataJSON = &str
	}

	if params.UsageData != nil {
		data, err := json.Marshal(params.UsageData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal usage data: %w", err)
		}
		str := string(data)
		usageDataJSON = &str
	}

	if params.DisplayData != nil {
		data, err := json.Marshal(params.DisplayData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal display data: %w", err)
		}
		str := string(data)
		displayDataJSON = &str
	}

	var message generated.Message
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())

		// Get next sequence_id for this conversation
		sequenceID, err := q.GetNextSequenceID(ctx, params.ConversationID)
		if err != nil {
			return fmt.Errorf("failed to get next sequence ID: %w", err)
		}

		message, err = q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:      messageID,
			ConversationID: params.ConversationID,
			SequenceID:     sequenceID,
			Type:           string(params.Type),
			LlmData:        llmDataJSON,
			UserData:       userDataJSON,
			UsageData:      usageDataJSON,
			DisplayData:    displayDataJSON,
		})
		return err
	})
	return &message, err
}

// GetMessageByID retrieves a message by its ID
func (db *DB) GetMessageByID(ctx context.Context, messageID string) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		message, err = q.GetMessage(ctx, messageID)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}
	return &message, err
}

// ListMessagesByConversationPaginated retrieves messages in a conversation with pagination
func (db *DB) ListMessagesByConversationPaginated(ctx context.Context, conversationID string, limit, offset int64) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListMessagesPaginated(ctx, generated.ListMessagesPaginatedParams{
			ConversationID: conversationID,
			Limit:          limit,
			Offset:         offset,
		})
		return err
	})
	return messages, err
}

// ListMessagesByType retrieves messages of a specific type in a conversation
func (db *DB) ListMessagesByType(ctx context.Context, conversationID string, messageType MessageType) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListMessagesByType(ctx, generated.ListMessagesByTypeParams{
			ConversationID: conversationID,
			Type:           string(messageType),
		})
		return err
	})
	return messages, err
}

// GetLatestMessage retrieves the latest message in a conversation
func (db *DB) GetLatestMessage(ctx context.Context, conversationID string) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		message, err = q.GetLatestMessage(ctx, conversationID)
		return err
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no messages found in conversation: %s", conversationID)
	}
	return &message, err
}

// CountMessagesByType returns the number of messages of a specific type in a conversation
func (db *DB) CountMessagesByType(ctx context.Context, conversationID string, messageType MessageType) (int64, error) {
	var count int64
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		count, err = q.CountMessagesByType(ctx, generated.CountMessagesByTypeParams{
			ConversationID: conversationID,
			Type:           string(messageType),
		})
		return err
	})
	return count, err
}

// Queries provides read-only access to generated queries within a read transaction
func (db *DB) Queries(ctx context.Context, fn func(*generated.Queries) error) error {
	return db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		return fn(q)
	})
}

// QueriesTx provides read-write access to generated queries within a write transaction
func (db *DB) QueriesTx(ctx context.Context, fn func(*generated.Queries) error) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return fn(q)
	})
}

// ListArchivedConversations retrieves archived conversations with pagination
func (db *DB) ListArchivedConversations(ctx context.Context, limit, offset int64) ([]generated.Conversation, error) {
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.ListArchivedConversations(ctx, generated.ListArchivedConversationsParams{
			Limit:  limit,
			Offset: offset,
		})
		return err
	})
	return conversations, err
}

// SearchArchivedConversations searches for archived conversations containing the given query in their slug
func (db *DB) SearchArchivedConversations(ctx context.Context, query string, limit, offset int64) ([]generated.Conversation, error) {
	queryPtr := &query
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.SearchArchivedConversations(ctx, generated.SearchArchivedConversationsParams{
			Column1: queryPtr,
			Limit:   limit,
			Offset:  offset,
		})
		return err
	})
	return conversations, err
}

// ArchiveConversation archives a conversation
func (db *DB) ArchiveConversation(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.ArchiveConversation(ctx, conversationID)
		return err
	})
	return &conversation, err
}

// UnarchiveConversation unarchives a conversation
func (db *DB) UnarchiveConversation(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.UnarchiveConversation(ctx, conversationID)
		return err
	})
	return &conversation, err
}

// DeleteConversation deletes a conversation and all its messages
func (db *DB) DeleteConversation(ctx context.Context, conversationID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		// Delete messages first (foreign key constraint)
		if err := q.DeleteConversationMessages(ctx, conversationID); err != nil {
			return fmt.Errorf("failed to delete messages: %w", err)
		}
		return q.DeleteConversation(ctx, conversationID)
	})
}

// CreateSubagentConversation creates a new subagent conversation with a parent
func (db *DB) CreateSubagentConversation(ctx context.Context, slug, parentID string, cwd *string) (*generated.Conversation, error) {
	conversationID, err := generateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateSubagentConversation(ctx, generated.CreateSubagentConversationParams{
			ConversationID:       conversationID,
			Slug:                 &slug,
			Cwd:                  cwd,
			ParentConversationID: &parentID,
		})
		return err
	})
	return &conversation, err
}

// GetSubagents retrieves all subagent conversations for a parent conversation
func (db *DB) GetSubagents(ctx context.Context, parentID string) ([]generated.Conversation, error) {
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversations, err = q.GetSubagents(ctx, &parentID)
		return err
	})
	return conversations, err
}

// GetConversationBySlugAndParent retrieves a subagent conversation by slug and parent ID
func (db *DB) GetConversationBySlugAndParent(ctx context.Context, slug, parentID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		conversation, err = q.GetConversationBySlugAndParent(ctx, generated.GetConversationBySlugAndParentParams{
			Slug:                 &slug,
			ParentConversationID: &parentID,
		})
		return err
	})
	if err == sql.ErrNoRows {
		return nil, nil // Not found, return nil without error
	}
	return &conversation, err
}

// SubagentDBAdapter adapts *DB to the claudetool.SubagentDB interface.
type SubagentDBAdapter struct {
	DB *DB
}

// GetOrCreateSubagentConversation implements claudetool.SubagentDB.
// Returns the conversation ID and the actual slug used (may differ if a suffix was added).
func (a *SubagentDBAdapter) GetOrCreateSubagentConversation(ctx context.Context, slug, parentID, cwd string) (string, string, error) {
	// Try to find existing with exact slug
	existing, err := a.DB.GetConversationBySlugAndParent(ctx, slug, parentID)
	if err != nil {
		return "", "", err
	}
	if existing != nil {
		return existing.ConversationID, *existing.Slug, nil
	}

	// Try to create new, handling unique constraint violations by appending numbers
	baseSlug := slug
	actualSlug := slug
	for attempt := 0; attempt < 100; attempt++ {
		conv, err := a.DB.CreateSubagentConversation(ctx, actualSlug, parentID, &cwd)
		if err == nil {
			return conv.ConversationID, actualSlug, nil
		}

		// Check if this is a unique constraint violation
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unique constraint") ||
			strings.Contains(errLower, "duplicate") {
			// Try with a numeric suffix
			actualSlug = fmt.Sprintf("%s-%d", baseSlug, attempt+1)
			continue
		}

		// Some other error occurred
		return "", "", err
	}

	return "", "", fmt.Errorf("failed to create unique subagent slug after 100 attempts")
}
