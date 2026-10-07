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
	"time"

	"github.com/google/uuid"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"

	_ "modernc.org/sqlite"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// GenerateConversationID generates a conversation ID in the format "cXXXXXX"
// where X are random alphanumeric characters
func GenerateConversationID() (string, error) {
	text := rand.Text()
	if len(text) < 6 {
		return "", fmt.Errorf("rand.Text() returned insufficient characters: %d", len(text))
	}
	return "c" + text[:6], nil
}

// DB wraps the database connection pool and provides high-level operations
type DB struct {
	pool *Pool
	path string
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

	// Plain filenames may include driver options. Leave file: URI handling
	// (including its directory requirements) to SQLite.
	filename, _, _ := strings.Cut(cfg.DSN, "?")
	if !strings.HasPrefix(filename, "file:") {
		dir := filepath.Dir(filename)
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

	// Ask the opened connection, not the DSN: SQLite resolves relative paths,
	// URI escaping/options and symlinks. Keep this instance-local and stable
	// even if the process changes working directory later.
	var path string
	if err := pool.Rx(context.Background(), func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&path)
	}); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database path: %w", err)
	}
	if path == "" {
		pool.Close()
		return nil, fmt.Errorf("database must be backed by a filesystem file")
	}

	return &DB{
		pool: pool,
		path: path,
	}, nil
}

// Path returns the filesystem path of this instance's main SQLite database.
func (db *DB) Path() string { return db.path }

// Close closes the database connection pool
func (db *DB) Close() error {
	return db.pool.Close()
}

// Migrate runs the database migrations.
//
// Migrations are tracked by filename. Two migration files may share a
// numeric prefix as long as their full names differ; this keeps
// concurrent feature branches that both grab "the next number" from
// silently skipping each other's migrations after merge. (The earlier
// implementation keyed on number, and a renumber-on-rebase could leave
// production databases with a column missing because the runner thought
// it had already executed something with that number.)
func (db *DB) Migrate(ctx context.Context) error {
	// Read and validate migration files.
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	type migration struct {
		name   string
		number int
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationPattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		num, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("failed to parse migration number from %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{name: entry.Name(), number: num})
	}

	// Sort by (number, name) so renumbered/sibling migrations have a stable order.
	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].number != migrations[j].number {
			return migrations[i].number < migrations[j].number
		}
		return migrations[i].name < migrations[j].name
	})

	// Load the names of already-applied migrations.
	executed := make(map[string]bool)
	var tableName string
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		row := rx.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='migrations'")
		return row.Scan(&tableName)
	})
	if err == nil {
		err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			rows, err := rx.Query("SELECT migration_name FROM migrations")
			if err != nil {
				return fmt.Errorf("failed to query executed migrations: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return fmt.Errorf("failed to scan migration name: %w", err)
				}
				executed[name] = true
			}
			return rows.Err()
		})
		if err != nil {
			return fmt.Errorf("failed to load executed migrations: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Info("migrations table not found, running all migrations")
	}

	for _, m := range migrations {
		if executed[m.name] {
			continue
		}
		slog.Info("running migration", "file", m.name, "number", m.number)
		if err := db.runMigration(ctx, m.name, m.number); err != nil {
			return err
		}
	}

	return nil
}

// runMigration executes a single migration file within a transaction,
// including recording it in the migrations table.
func (db *DB) runMigration(ctx context.Context, filename string, migrationNumber int) error {
	content, err := schemaFS.ReadFile("schema/" + filename)
	if err != nil {
		return fmt.Errorf("failed to read migration file %s: %w", filename, err)
	}

	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.Exec(string(content)); err != nil {
			return fmt.Errorf("failed to execute migration %s: %w", filename, err)
		}

		if _, err := tx.Exec("INSERT INTO migrations (migration_number, migration_name) VALUES (?, ?)", migrationNumber, filename); err != nil {
			return fmt.Errorf("failed to record migration %s in migrations table: %w", filename, err)
		}

		return nil
	})
}

// Pool returns the underlying connection pool for advanced operations
func (db *DB) Pool() *Pool {
	return db.pool
}

// Checkpoint runs a truncating WAL checkpoint, flushing the write-ahead log
// back into the main database file and shrinking the -wal file on disk.
//
// In WAL mode SQLite's default auto-checkpoint is PASSIVE: it copies committed
// frames into the main db but never shrinks the -wal file, which therefore
// grows to (and stays at) its high-water mark. Run this periodically and at
// startup to keep the -wal file bounded.
func (db *DB) Checkpoint(ctx context.Context) error {
	if err := db.pool.Exec(ctx, "PRAGMA wal_checkpoint(TRUNCATE);"); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
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

// ConversationOptions holds extensible conversation settings stored as JSON.
type ConversationHook struct {
	URL string `json:"url"`
}

type BtwParentPointer struct {
	Generation int64 `json:"generation"`
	SequenceID int64 `json:"sequence_id"`
}

type CommitTourRequest struct {
	Repository  string    `json:"repository"`
	Worktree    string    `json:"worktree"`
	Commit      string    `json:"commit"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
}

type ConversationOptions struct {
	// Kind identifies specialized child conversations. Empty is a normal chat.
	Kind          string             `json:"kind,omitempty"`
	ParentPointer *BtwParentPointer  `json:"parent_pointer,omitempty"`
	CommitTour    *CommitTourRequest `json:"commit_tour,omitempty"`
	// ToolOverrides maps tool name to "on" or "off". Tools not listed use their default.
	ToolOverrides map[string]string `json:"tool_overrides,omitempty"`
	// DisableAllTools disables every tool by default; ToolOverrides with "on" re-enable individual tools.
	// Useful for API clients that can't enumerate the tool registry.
	DisableAllTools bool `json:"disable_all_tools,omitempty"`
	// EndOfTurnHooks are posted to whenever a top-level agent turn ends.
	EndOfTurnHooks []ConversationHook `json:"end_of_turn_hooks,omitempty"`
	// ThinkingLevel is the user-facing reasoning level for this conversation.
	// One of "off", "minimal", "low", "medium", "high", "xhigh". Empty string
	// means "use the service default". See llm.ParseThinkingLevel.
	ThinkingLevel string `json:"thinking_level,omitempty"`
	// DisableNotifications suppresses end-of-turn notifications (push, email,
	// discord, ntfy) for this conversation. Useful for cron-style or
	// self-invoked conversations that shouldn't ping the user.
	DisableNotifications bool `json:"disable_notifications,omitempty"`
	// SystemPrompt, if set, is the conversation's system prompt in place of
	// the generated one.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// CompactNudgeTokens is the context size at which the agent is first told
	// the size of its context while the compact_in_place tool is enabled.
	// Zero means the default (160k).
	CompactNudgeTokens int `json:"compact_nudge_tokens,omitempty"`
	// DisableCompactNudges keeps the agent from being told its context size
	// even though compact_in_place is enabled. Set when the user enables the
	// tool mid-conversation (the Compact in Place button): they asked for a
	// compaction, not for an agent that watches its context.
	DisableCompactNudges bool `json:"disable_compact_nudges,omitempty"`
}

// ParseConversationOptions parses a JSON string into ConversationOptions.
// Returns zero-value options for empty or invalid input.
func ParseConversationOptions(s string) ConversationOptions {
	var opts ConversationOptions
	if s != "" {
		_ = json.Unmarshal([]byte(s), &opts)
	}
	return opts
}

// UpdateConversationOptions replaces a conversation's stored options JSON.
func (db *DB) UpdateConversationOptions(ctx context.Context, conversationID string, opts ConversationOptions) error {
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return fmt.Errorf("failed to marshal conversation options: %w", err)
	}
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID:      conversationID,
			ConversationOptions: string(optsJSON),
		})
	})
}

// ModifyConversationOptions atomically applies modify to a conversation's
// stored options, writing them back if modify reports a change. It returns the
// resulting options and whether they changed.
func (db *DB) ModifyConversationOptions(ctx context.Context, conversationID string, modify func(*ConversationOptions) bool) (ConversationOptions, bool, error) {
	var opts ConversationOptions
	var changed bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		raw, err := q.GetConversationOptions(ctx, conversationID)
		if err != nil {
			return err
		}
		opts = ParseConversationOptions(raw)
		if changed = modify(&opts); !changed {
			return nil
		}
		optsJSON, err := json.Marshal(opts)
		if err != nil {
			return fmt.Errorf("failed to marshal conversation options: %w", err)
		}
		return q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID:      conversationID,
			ConversationOptions: string(optsJSON),
		})
	})
	return opts, changed, err
}

// RegisterConversationHook atomically adds hook to conversation options if absent.
func (db *DB) RegisterConversationHook(ctx context.Context, conversationID string, hook ConversationHook) (ConversationOptions, error) {
	opts, _, err := db.ModifyConversationOptions(ctx, conversationID, func(o *ConversationOptions) bool {
		for _, existing := range o.EndOfTurnHooks {
			if existing.URL == hook.URL {
				return false
			}
		}
		o.EndOfTurnHooks = append(append([]ConversationHook(nil), o.EndOfTurnHooks...), hook)
		return true
	})
	return opts, err
}

// SetConversationThinkingLevel atomically updates the reasoning/thinking level
// in a conversation's stored options, preserving all other option fields. It
// returns the resulting options. reasoning is a user-facing level name
// ("off", "minimal", "low", "medium", "high", "xhigh").
func (db *DB) SetConversationThinkingLevel(ctx context.Context, conversationID, reasoning string) (ConversationOptions, error) {
	opts, _, err := db.ModifyConversationOptions(ctx, conversationID, func(o *ConversationOptions) bool {
		if o.ThinkingLevel == reasoning {
			return false
		}
		o.ThinkingLevel = reasoning
		return true
	})
	return opts, err
}

// CreateConversation creates a new conversation with an optional slug.
func (db *DB) CreateConversation(ctx context.Context, slug *string, userInitiated bool, cwd, model *string, opts ConversationOptions) (*generated.Conversation, error) {
	conversationID, err := GenerateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal conversation options: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID:      conversationID,
			Slug:                slug,
			UserInitiated:       userInitiated,
			Cwd:                 cwd,
			Model:               model,
			ConversationOptions: string(optsJSON),
		})
		return err
	})
	return &conversation, err
}

// CreateChannelConversation creates the conversation for an external chat:
// externalID is the channel's chat id, endpoint the base URL its replies
// are sent to ("" for none), and opts its options.
func (db *DB) CreateChannelConversation(ctx context.Context, externalID, endpoint, model string, opts ConversationOptions) (*generated.Conversation, error) {
	conversationID, err := GenerateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal conversation options: %w", err)
	}
	var ep *string
	if endpoint != "" {
		ep = &endpoint
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		conversation, err = generated.New(tx.Conn()).CreateChannelConversation(ctx, generated.CreateChannelConversationParams{
			ConversationID:         conversationID,
			Model:                  &model,
			ConversationOptions:    string(optsJSON),
			ExternalConversationID: &externalID,
			ExternalEndpoint:       ep,
		})
		return err
	})
	return &conversation, err
}

// CreateDraftConversation creates a new draft conversation. Drafts have
// no messages; their body lives in the draft column until promoted by
// the chat handler. They appear in the normal conversation list and can
// be deleted like any other conversation.
func (db *DB) CreateDraftConversation(ctx context.Context, cwd, model *string, opts ConversationOptions, draft string) (*generated.Conversation, error) {
	conversationID, err := GenerateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal conversation options: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err = q.CreateDraftConversation(ctx, generated.CreateDraftConversationParams{
			ConversationID:      conversationID,
			Slug:                nil,
			Cwd:                 cwd,
			Model:               model,
			ConversationOptions: string(optsJSON),
			Draft:               draft,
		})
		return err
	})
	return &conversation, err
}

// UpdateDraft partially updates a draft conversation in place: nil fields
// keep their current value, so draft text, model, and cwd each update
// independently without racing each other. Returns ErrConversationNotDraft
// if the conversation no longer exists as a draft (deleted, or promoted by
// a concurrent chat post) so the caller doesn't mutate an active
// conversation.
func (db *DB) UpdateDraft(ctx context.Context, conversationID string, draft, model, cwd *string) (*generated.Conversation, error) {
	var conv generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conv, err = q.UpdateConversationDraft(ctx, generated.UpdateConversationDraftParams{
			ConversationID: conversationID,
			Draft:          draft,
			Model:          model,
			Cwd:            cwd,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConversationNotDraft
		}
		return err
	})
	return &conv, err
}

// PromoteDraft applies send-time overrides (any non-nil of cwd, model,
// opts) and clears is_draft and draft, all in one transaction, returning
// the promoted row. The single Tx matters: a concurrent PUT /draft can no
// longer interleave between an override and the promote, so the returned
// row's model is exactly what the loop must pin to. Returns
// ErrConversationNotDraft (and applies nothing) when the conversation is
// no longer a draft — a concurrent send won the promote race — so the
// caller can decide whether to retry or fail.
func (db *DB) PromoteDraft(ctx context.Context, conversationID string, cwd, model *string, opts *ConversationOptions) (*generated.Conversation, error) {
	var requestedOptions *string
	if opts != nil {
		effective := *opts
		effective.Kind = ""
		effective.ParentPointer = nil
		optsJSON, err := json.Marshal(effective)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal conversation options: %w", err)
		}
		value := string(optsJSON)
		requestedOptions = &value
	}
	var conv generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		// Gate the overrides on is_draft inside the Tx (not just on the
		// caller's earlier read): if the promote below is going to lose the
		// race, the overrides must not touch the now-active conversation.
		if _, err := q.UpdateConversationDraft(ctx, generated.UpdateConversationDraftParams{
			ConversationID: conversationID,
			Model:          model,
			Cwd:            cwd,
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConversationNotDraft
			}
			return err
		}
		optionsJSON := requestedOptions
		if optionsJSON == nil {
			stored, err := q.GetConversationOptions(ctx, conversationID)
			if err != nil {
				return err
			}
			scrubbed, changed, err := scrubManagedBtwOptions(stored)
			if err != nil {
				return err
			}
			if changed {
				optionsJSON = &scrubbed
			}
		}
		if optionsJSON != nil {
			if err := q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
				ConversationID:      conversationID,
				ConversationOptions: *optionsJSON,
			}); err != nil {
				return err
			}
		}
		promoted, err := q.PromoteDraftConversation(ctx, conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConversationNotDraft
		}
		conv = promoted
		return err
	})
	if err != nil {
		return nil, err
	}
	return &conv, nil
}

// QueuedMessageKind identifies specialized queued work. Empty is an ordinary
// user message.
type QueuedMessageKind string

const QueuedMessageKindTranscription QueuedMessageKind = "transcription"

// QueuedMessageState is the durable lifecycle of specialized queued work.
type QueuedMessageState string

const (
	QueuedMessageStateWorking QueuedMessageState = "working"
	QueuedMessageStateReady   QueuedMessageState = "ready"
	QueuedMessageStateFailed  QueuedMessageState = "failed"
)

// QueuedTranscription contains the durable inputs and audit for a
// transcription queue item.
type QueuedTranscription struct {
	MediaPath        string          `json:"media_path"`
	ContactSheetPath string          `json:"contact_sheet_path,omitempty"`
	Context          string          `json:"context,omitempty"`
	Audit            json.RawMessage `json:"audit,omitempty"`
}

// QueuedMessage is one user message held in a conversation's queued_messages
// JSON array while the agent is busy or distilling. The array is the single
// source of truth for queued user input (there is no `messages` row until the
// item drains). On drain the Llm payload is fed to the loop verbatim and a
// normal, immutable user message is inserted with a fresh sequence_id.
type QueuedMessage struct {
	// ID is a stable identifier so clients can cancel a single queued item
	// and so drain can remove exactly the item it consumed.
	ID string `json:"id"`
	// Llm is the raw JSON of the llm.Message to feed to the loop on drain.
	// Working and failed specialized items omit it until their result is ready.
	// Stored as RawMessage so package db stays decoupled from package llm.
	Llm json.RawMessage `json:"llm,omitempty"`
	// CreatedAt is when the message was queued (RFC3339).
	CreatedAt time.Time `json:"created_at"`
	// Model is the model id chosen at queue time, used to start/continue the
	// loop when the queue drains.
	Model string `json:"model"`
	// UserEmail is the exe.dev account (from the X-ExeDev-Email header) that
	// authored the queued message, captured at queue time. It is persisted
	// here (rather than read at drain) because drain runs on a background
	// context with no request/header available. Stamped onto the messages row
	// when the message drains. Empty when the request carried no header.
	UserEmail string `json:"user_email,omitempty"`
	// UserData is message provenance and other presentation metadata captured at
	// queue time. It is copied to messages.user_data when the item drains.
	UserData json.RawMessage `json:"user_data,omitempty"`
	// ExternalMessageID is the channel's id for a message that arrived from
	// an external chat; it is stamped onto the messages row on drain.
	ExternalMessageID string `json:"external_message_id,omitempty"`
	// Inject lets a running turn take the message at its next LLM round
	// instead of waiting for the turn to end.
	Inject bool `json:"inject,omitempty"`
	// ID, CreatedAt, Model, UserEmail, UserData, and ExternalMessageID are
	// shared queue metadata.
	// Kind, State, Transcription, Error, and the optional ready Llm payload form
	// the specialized-work variant.
	Kind          QueuedMessageKind    `json:"kind,omitempty"`
	State         QueuedMessageState   `json:"state,omitempty"`
	Transcription *QueuedTranscription `json:"transcription,omitempty"`
	Error         string               `json:"error,omitempty"`
}

// ParseQueuedMessagesStrict parses the queued_messages JSON array, returning an
// error if a NON-EMPTY column fails to unmarshal. MUTATION paths (append /
// remove, and the atomic drain removal in insertMessageTx) MUST use this so a
// corrupt-but-nonempty column is never silently overwritten with '[]', which
// would lose queued user input. An empty string (legacy rows predating the
// NOT NULL DEFAULT) parses to nil without error.
func ParseQueuedMessagesStrict(s string) ([]QueuedMessage, error) {
	if s == "" {
		return nil, nil
	}
	var out []QueuedMessage
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("failed to parse queued_messages JSON (%d bytes): %w", len(s), err)
	}
	return out, nil
}

// ParseQueuedMessages parses the queued_messages JSON array leniently for
// READ / render / Hydrate paths: invalid input yields an empty slice (callers
// that care about corruption should log; see Hydrate). Returns an empty slice
// for empty or invalid input (the column defaults to '[]'). MUTATION paths must
// use ParseQueuedMessagesStrict instead.
func ParseQueuedMessages(s string) []QueuedMessage {
	out, _ := ParseQueuedMessagesStrict(s)
	return out
}

// MarshalQueuedMessages serializes a queued-message slice for storage. A nil
// or empty slice serializes to "[]" so the column never holds null.
func MarshalQueuedMessages(msgs []QueuedMessage) (string, error) {
	if len(msgs) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		return "", fmt.Errorf("failed to marshal queued messages: %w", err)
	}
	return string(b), nil
}

// mutateQueuedMessages runs mutate against the parsed queued_messages array
// inside one transaction, persists the result (bumping updated_at, which
// re-sorts the conversation and triggers a list-patch recompute), and returns
// the conversation row after the update so callers can broadcast it.
func (db *DB) mutateQueuedMessages(ctx context.Context, conversationID string, mutate func(msgs []QueuedMessage) ([]QueuedMessage, error)) (*generated.Conversation, error) {
	var conv generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		raw, err := q.GetConversationQueuedMessages(ctx, conversationID)
		if err != nil {
			return err
		}
		msgs, err := ParseQueuedMessagesStrict(raw)
		if err != nil {
			return err
		}
		if msgs, err = mutate(msgs); err != nil {
			return err
		}
		jsonStr, err := MarshalQueuedMessages(msgs)
		if err != nil {
			return err
		}
		if err := q.UpdateConversationQueuedMessages(ctx, generated.UpdateConversationQueuedMessagesParams{
			QueuedMessages: jsonStr,
			ConversationID: conversationID,
		}); err != nil {
			return err
		}
		conv, err = q.GetConversation(ctx, conversationID)
		return err
	})
	return &conv, err
}

// AppendQueuedMessage atomically appends qm to a conversation's
// queued_messages array.
func (db *DB) AppendQueuedMessage(ctx context.Context, conversationID string, qm QueuedMessage) (*generated.Conversation, error) {
	return db.mutateQueuedMessages(ctx, conversationID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
		return append(msgs, qm), nil
	})
}

var (
	ErrQueuedMessageNotFound     = errors.New("queued message not found")
	ErrQueuedMessageNotRetryable = errors.New("queued message is not a failed transcription")
)

func findQueuedMessage(msgs []QueuedMessage, queuedID string) (*QueuedMessage, error) {
	for i := range msgs {
		if msgs[i].ID == queuedID {
			return &msgs[i], nil
		}
	}
	return nil, ErrQueuedMessageNotFound
}

// GetQueuedMessages strictly reads the durable queued-message array.
func (db *DB) GetQueuedMessages(ctx context.Context, conversationID string) ([]QueuedMessage, error) {
	var msgs []QueuedMessage
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		raw, err := generated.New(rx.Conn()).GetConversationQueuedMessages(ctx, conversationID)
		if err != nil {
			return err
		}
		msgs, err = ParseQueuedMessagesStrict(raw)
		return err
	})
	return msgs, err
}

// GetQueuedMessage returns one durable queue item by id.
func (db *DB) GetQueuedMessage(ctx context.Context, conversationID, queuedID string) (QueuedMessage, error) {
	msgs, err := db.GetQueuedMessages(ctx, conversationID)
	if err != nil {
		return QueuedMessage{}, err
	}
	qm, err := findQueuedMessage(msgs, queuedID)
	if err != nil {
		return QueuedMessage{}, err
	}
	return *qm, nil
}

// UpdateQueuedMessage atomically mutates one queue item and returns the
// updated conversation and item. The callback runs inside the transaction and
// must not perform database work.
func (db *DB) UpdateQueuedMessage(ctx context.Context, conversationID, queuedID string, update func(*QueuedMessage) error) (*generated.Conversation, QueuedMessage, error) {
	var updated QueuedMessage
	conv, err := db.mutateQueuedMessages(ctx, conversationID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
		qm, err := findQueuedMessage(msgs, queuedID)
		if err != nil {
			return nil, err
		}
		if err := update(qm); err != nil {
			return nil, err
		}
		updated = *qm
		return msgs, nil
	})
	return conv, updated, err
}

// CreateQueuedTranscription atomically appends a working transcription item
// to the parent queue.
func (db *DB) CreateQueuedTranscription(ctx context.Context, parentID string, qm QueuedMessage) (*generated.Conversation, QueuedMessage, error) {
	if qm.Kind != QueuedMessageKindTranscription || qm.State != QueuedMessageStateWorking || qm.Transcription == nil {
		return nil, QueuedMessage{}, fmt.Errorf("invalid queued transcription")
	}
	parent, err := db.mutateQueuedMessages(ctx, parentID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
		return append(msgs, qm), nil
	})
	return parent, qm, err
}

// RetryQueuedTranscription atomically moves a failed item back to working.
// Concurrent retries serialize on state.
func (db *DB) RetryQueuedTranscription(ctx context.Context, parentID, queuedID string) (*generated.Conversation, QueuedMessage, error) {
	var updated QueuedMessage
	parent, err := db.mutateQueuedMessages(ctx, parentID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
		qm, err := findQueuedMessage(msgs, queuedID)
		if err != nil {
			return nil, err
		}
		if qm.Kind != QueuedMessageKindTranscription || qm.State != QueuedMessageStateFailed || qm.Transcription == nil {
			return nil, ErrQueuedMessageNotRetryable
		}
		qm.State = QueuedMessageStateWorking
		qm.Error = ""
		qm.Transcription.ContactSheetPath = ""
		qm.Transcription.Audit = nil
		updated = *qm
		return msgs, nil
	})
	return parent, updated, err
}

// RemoveQueuedMessages atomically removes the queued messages whose IDs are in
// the given set. IDs not present are ignored.
func (db *DB) RemoveQueuedMessages(ctx context.Context, conversationID string, ids ...string) (*generated.Conversation, error) {
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	return db.mutateQueuedMessages(ctx, conversationID, func(msgs []QueuedMessage) ([]QueuedMessage, error) {
		kept := msgs[:0]
		for _, m := range msgs {
			if !remove[m.ID] {
				kept = append(kept, m)
			}
		}
		return kept, nil
	})
}

// ClearQueuedMessages resets a conversation's queued_messages array to '[]'
// and bumps updated_at. Returns the conversation row after the update.
func (db *DB) ClearQueuedMessages(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	var conv generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		if err := q.UpdateConversationQueuedMessages(ctx, generated.UpdateConversationQueuedMessagesParams{
			QueuedMessages: "[]",
			ConversationID: conversationID,
		}); err != nil {
			return err
		}
		var err error
		conv, err = q.GetConversation(ctx, conversationID)
		return err
	})
	return &conv, err
}

// ErrConversationNotDraft is returned by PromoteDraft when the
// conversation is not (or is no longer) a draft. Callers that gate on
// IsDraft should treat this as a race condition / 4xx, not a 500.
var ErrConversationNotDraft = errors.New("conversation is not a draft")

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

// ConversationListItem is a conversation row plus the derived fields the
// conversation list needs but that don't live on the conversation row. All
// are computed by correlated subqueries IN the list/search query itself (see
// conversations.sql), scoped to exactly the window of conversations returned,
// so we never scan or JSON-decode messages for conversations off-window.
type ConversationListItem struct {
	generated.Conversation
	// Preview is the trailing text of the most recent agent message, so the
	// list can show a one-line summary without a separate fetch.
	Preview string `json:"preview,omitempty"`
	// PreviewUpdatedAt is that message's CreatedAt, RFC 3339 (trailing Z).
	PreviewUpdatedAt string `json:"preview_updated_at,omitempty"`
	// MaxSequenceID is the highest message sequence_id stored for this
	// conversation. Clients use it to decide whether their cached snapshot
	// is up to date without a separate /api/conversation/<id> roundtrip.
	MaxSequenceID int64 `json:"max_sequence_id"`
	// Participants are the exe.dev accounts (from the X-ExeDev-Email header)
	// that authored messages in this conversation, with authored-message
	// counts, sorted by email. Clients use it to filter the list down to
	// their own conversations.
	Participants  []ConversationParticipant `json:"participants,omitempty"`
	SubagentCount int64                     `json:"subagent_count"`
	// RunningBackgroundJobs counts this conversation's backgrounded bash
	// commands that have not exited.
	RunningBackgroundJobs int64 `json:"running_background_jobs"`
}

type ConversationParticipant struct {
	Email        string `json:"email"`
	MessageCount int64  `json:"message_count"`
}

// ConversationParticipants returns known authenticated message authors and
// authored-message counts for the requested conversations.
func (db *DB) ConversationParticipants(ctx context.Context, conversationIDs []string) (map[string][]ConversationParticipant, error) {
	out := make(map[string][]ConversationParticipant)
	if len(conversationIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(conversationIDs)), ",")
	args := make([]any, len(conversationIDs))
	for i, id := range conversationIDs {
		args[i] = id
	}
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Conn().QueryContext(ctx, `
			SELECT conversation_id, user_email, COUNT(*)
			FROM messages
			WHERE user_email IS NOT NULL AND user_email <> ''
			  AND conversation_id IN (`+placeholders+`)
			GROUP BY conversation_id, user_email
			ORDER BY conversation_id, user_email`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var participant ConversationParticipant
			if err := rows.Scan(&id, &participant.Email, &participant.MessageCount); err != nil {
				return err
			}
			out[id] = append(out[id], participant)
		}
		return rows.Err()
	})
	return out, err
}

// decodeParticipants decodes and stably sorts participant summaries.
func decodeParticipants(raw string) ([]ConversationParticipant, error) {
	var participants []ConversationParticipant
	if err := json.Unmarshal([]byte(raw), &participants); err != nil {
		return nil, fmt.Errorf("decoding participants %q: %w", raw, err)
	}
	if len(participants) == 0 {
		return nil, nil
	}
	sort.Slice(participants, func(i, j int) bool {
		return participants[i].Email < participants[j].Email
	})
	return participants, nil
}

// previewTimestampLen is the fixed width of the RFC3339 timestamp
// ("2006-01-02T15:04:05Z") that the list/search queries prepend to the
// preview text in the preview_packed column. See the preview_packed note in
// conversations.sql.
const previewTimestampLen = len("2006-01-02T15:04:05Z")

// splitPreviewPacked splits the preview_packed column back into its timestamp
// and (already truncated) preview text. An empty packed value means the
// conversation has no agent-message preview, so both results are empty.
//
// The preview is client-facing, so inline citation markup is stripped here.
// Truncation can cut a citation group in half; the strip fails open on the
// remnant, which shows a bare "citeturn1search0" rather than the stray
// glyphs the raw markers render as.
func splitPreviewPacked(packed string) (preview, updatedAt string) {
	if len(packed) < previewTimestampLen {
		return "", ""
	}
	return llm.StripInlineCitationMarkers(packed[previewTimestampLen:]), packed[:previewTimestampLen]
}

// newConversationListItem assembles a list item from a conversation row and
// the derived columns the list/search queries compute alongside it.
func newConversationListItem(conv generated.Conversation, previewPacked string, maxSequenceID int64, participantsJSON string, subagentCount, runningBackgroundJobs int64) (ConversationListItem, error) {
	participants, err := decodeParticipants(participantsJSON)
	if err != nil {
		return ConversationListItem{}, err
	}
	preview, updatedAt := splitPreviewPacked(previewPacked)
	return ConversationListItem{
		Conversation:          conv,
		Preview:               preview,
		PreviewUpdatedAt:      updatedAt,
		MaxSequenceID:         maxSequenceID,
		Participants:          participants,
		SubagentCount:         subagentCount,
		RunningBackgroundJobs: runningBackgroundJobs,
	}, nil
}

// ListConversations retrieves conversations with pagination
func (db *DB) ListConversations(ctx context.Context, limit, offset int64) ([]ConversationListItem, error) {
	var items []ConversationListItem
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		rows, err := q.ListConversations(ctx, generated.ListConversationsParams{
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return err
		}
		items = make([]ConversationListItem, len(rows))
		for i, r := range rows {
			item, err := newConversationListItem(r.Conversation, r.PreviewPacked, r.MaxSequenceID, r.ParticipantsJson, r.SubagentCount, r.RunningBackgroundJobs)
			if err != nil {
				return err
			}
			items[i] = item
		}
		return nil
	})
	return items, err
}

// ListAllConversations retrieves all conversations (including subagents) with pagination.
func (db *DB) ListAllConversations(ctx context.Context, limit, offset int64) ([]ConversationListItem, error) {
	var items []ConversationListItem
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		rows, err := q.ListAllConversations(ctx, generated.ListAllConversationsParams{
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return err
		}
		items = make([]ConversationListItem, len(rows))
		for i, r := range rows {
			item, err := newConversationListItem(r.Conversation, r.PreviewPacked, r.MaxSequenceID, r.ParticipantsJson, r.SubagentCount, r.RunningBackgroundJobs)
			if err != nil {
				return err
			}
			items[i] = item
		}
		return nil
	})
	return items, err
}

// SearchConversations searches for conversations containing the given query in their slug
func (db *DB) SearchConversations(ctx context.Context, query string, limit, offset int64) ([]ConversationListItem, error) {
	queryPtr := &query
	var items []ConversationListItem
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		rows, err := q.SearchConversations(ctx, generated.SearchConversationsParams{
			Column1: queryPtr,
			Limit:   limit,
			Offset:  offset,
		})
		if err != nil {
			return err
		}
		items = make([]ConversationListItem, len(rows))
		for i, r := range rows {
			item, err := newConversationListItem(r.Conversation, r.PreviewPacked, r.MaxSequenceID, r.ParticipantsJson, r.SubagentCount, r.RunningBackgroundJobs)
			if err != nil {
				return err
			}
			items[i] = item
		}
		return nil
	})
	return items, err
}

// SearchConversationsWithMessages searches slug or message content, with slug matches first.
func (db *DB) SearchConversationsWithMessages(ctx context.Context, query string, limit, offset int64) ([]ConversationListItem, error) {
	queryPtr := &query
	var items []ConversationListItem
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		rows, err := q.SearchConversationsWithMessages(ctx, generated.SearchConversationsWithMessagesParams{
			Query:  queryPtr,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return err
		}
		items = make([]ConversationListItem, len(rows))
		for i, r := range rows {
			item, err := newConversationListItem(r.Conversation, r.PreviewPacked, r.MaxSequenceID, r.ParticipantsJson, r.SubagentCount, r.RunningBackgroundJobs)
			if err != nil {
				return err
			}
			items[i] = item
		}
		return nil
	})
	return items, err
}

// ConversationSearchResult is a conversation with an optional snippet showing
// the matched text. Snippets use the sentinel markers "\x02" and "\x03"
// around hit terms so callers can safely substitute spans without worrying
// about HTML in message bodies.
type ConversationSearchResult struct {
	// ConversationListItem carries the same derived conversation-list fields
	// as the regular list, computed in the search query itself (see
	// SearchConversationsFTSList).
	ConversationListItem
	Snippet string // empty if matched only by slug
}

// SnippetMarkStart and SnippetMarkEnd surround matched terms inside
// Snippet strings produced by SearchConversationsFTS.
const (
	SnippetMarkStart = "\x02"
	SnippetMarkEnd   = "\x03"
)

// SearchConversationsFTS performs a full-text search over user/agent message
// content (via the messages_fts FTS5 virtual table) and slug substring across
// ALL top-level conversations (active and archived). Slug matches come first;
// within each match tier, active conversations precede archived conversations,
// then results are ordered by updated_at DESC.
// Each FTS hit comes with a Snippet drawn from the best-ranking message;
// slug-only matches have an empty snippet.
// The query is the raw user input; this function handles tokenisation and
// escaping for both the FTS5 MATCH branch and the LIKE branch.
func (db *DB) SearchConversationsFTS(ctx context.Context, query string, limit, offset int64) ([]ConversationSearchResult, error) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return nil, nil
	}

	// Build an FTS5 MATCH expression: each token becomes a quoted prefix
	// term, all AND'd together. Escape embedded double quotes by doubling.
	ftsParts := make([]string, 0, len(fields))
	for _, f := range fields {
		ftsParts = append(ftsParts, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	ftsMatch := strings.Join(ftsParts, " AND ")

	// Escape LIKE wildcards (%, _) and the escape char itself in the slug
	// pattern so typing a literal % doesn't match every conversation.
	slugEscaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	slugLike := "%" + slugEscaped + "%"

	var results []ConversationSearchResult
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		convs, err := q.SearchConversationsFTSList(ctx, generated.SearchConversationsFTSListParams{
			SlugLike: &slugLike,
			FtsMatch: &ftsMatch,
			Limit:    limit,
			Offset:   offset,
		})
		if err != nil {
			return err
		}
		results = make([]ConversationSearchResult, len(convs))
		convIDs := make([]string, len(convs))
		for i, c := range convs {
			item, err := newConversationListItem(c.Conversation, c.PreviewPacked, c.MaxSequenceID, c.ParticipantsJson, c.SubagentCount, c.RunningBackgroundJobs)
			if err != nil {
				return err
			}
			results[i] = ConversationSearchResult{ConversationListItem: item}
			convIDs[i] = c.Conversation.ConversationID
		}
		if len(convIDs) == 0 {
			return nil
		}
		snipRows, err := q.SearchConversationsFTSSnippets(ctx, generated.SearchConversationsFTSSnippetsParams{
			MarkStart: SnippetMarkStart,
			MarkEnd:   SnippetMarkEnd,
			FtsMatch:  &ftsMatch,
			ConvIds:   convIDs,
		})
		if err != nil {
			return err
		}
		snippets := make(map[string]string, len(convIDs))
		for _, r := range snipRows {
			// The FTS source column is built from raw message JSON, so
			// snippets carry citation markup. Strip before centering: the
			// mark sentinels are outside the marker range, and stripping
			// first keeps centerOnMark's byte slicing off a marker's
			// 3-byte sequence.
			snippets[r.ConversationID] = centerOnMark(llm.StripInlineCitationMarkers(r.Snippet), 120)
		}
		for i := range results {
			results[i].Snippet = snippets[results[i].Conversation.ConversationID]
		}
		return nil
	})
	return results, err
}

// centerOnMark trims a snippet so the first SnippetMarkStart lands roughly
// in the middle, keeping at most budget bytes total. FTS5's snippet() uses
// a token budget, which collapses on long opaque runs (e.g. base64) and can
// push the actual match off the visible end of the truncated UI line.
// Centering on the mark guarantees the matched term is in the leading window
// the UI displays. An ellipsis prefix marks a trimmed-left snippet.
func centerOnMark(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	mark := strings.Index(s, SnippetMarkStart)
	if mark < 0 {
		return s
	}
	left := budget / 4
	start := mark - left
	if start <= 0 {
		return s
	}
	// Snap to the next space so we don't slice through a word, but only if
	// one is close by; long opaque runs (e.g. base64) have no spaces and we
	// must just cut.
	if sp := strings.IndexByte(s[start:], ' '); sp >= 0 && sp < 16 {
		start += sp + 1
	}
	return "..." + s[start:]
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

// SetConversationSlugIfUnset installs slug only while the conversation remains
// unnamed. The read and write share one write transaction so a manual rename or
// hook assignment that wins while async slug generation is in flight cannot be
// overwritten afterward.
func (db *DB) SetConversationSlugIfUnset(ctx context.Context, conversationID, slug string) (*generated.Conversation, bool, error) {
	var conversation generated.Conversation
	updated := false
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		current, err := q.GetConversation(ctx, conversationID)
		if err != nil {
			return err
		}
		if current.Slug != nil && *current.Slug != "" {
			conversation = current
			return nil
		}
		conversation, err = q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
			Slug:           &slug,
			ConversationID: conversationID,
		})
		if err == nil {
			updated = true
		}
		return err
	})
	return &conversation, updated, err
}

// UpdateConversationTags replaces a conversation's tag list. Tags are stored
// as a JSON array of strings; callers are responsible for normalizing/
// deduplicating entries.
func (db *DB) UpdateConversationTags(ctx context.Context, conversationID string, tags []string) (*generated.Conversation, error) {
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tags: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.UpdateConversationTags(ctx, generated.UpdateConversationTagsParams{
			Tags:           string(tagsJSON),
			ConversationID: conversationID,
		})
		return err
	})
	return &conversation, err
}

// ClearConversationSlug removes the slug from a conversation.
func (db *DB) ClearConversationSlug(ctx context.Context, conversationID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
			Slug:           nil,
			ConversationID: conversationID,
		})
		return err
	})
	return &conversation, err
}

// SetConversationAgentWorking persists the in-memory agent_working flag for
// a conversation. Writes are wrapped in a Tx so the conversation list patch
// stream's Pool.OnCommit hook fires and SSE clients see the change. The
// query intentionally does not bump updated_at — working state changes are
// frequent and must not reorder the conversation list.
func (db *DB) SetConversationAgentWorking(ctx context.Context, conversationID string, working bool) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{
			AgentWorking:   working,
			ConversationID: conversationID,
		})
	})
}

// ResetAllAgentWorking clears agent_working = TRUE for every conversation.
// Ordinary startup calls it in the same transaction that records durable
// interruption state for top-level conversations left mid-turn.
func (db *DB) ResetAllAgentWorking(ctx context.Context) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.ResetAllAgentWorking(ctx)
	})
}

// ClaimInterruptedTurn atomically changes an interrupted, idle, top-level
// conversation into a working conversation. A false result means another
// request already claimed it or it is no longer eligible.
func (db *DB) ClaimInterruptedTurn(ctx context.Context, conversationID string) (bool, error) {
	var claimed bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := generated.New(tx.Conn()).ClaimInterruptedTurn(ctx, conversationID)
		claimed = rows == 1
		return err
	})
	return claimed, err
}

// UpgradeResume is a one-process claim token for a turn that was working when
// an upgrade restart began. Its generation and latest user-message sequence
// change whenever newer durable user work supersedes that turn.
type UpgradeResume struct {
	ConversationID    string
	CurrentGeneration int64
	MaxUserSequenceID int64
}

// ClaimUpgradeInterruptedTurn atomically validates that a startup resume token
// still names the current stale working turn. A fresh turn changes its durable
// version before a late worker can claim it.
func (db *DB) ClaimUpgradeInterruptedTurn(ctx context.Context, resume UpgradeResume) (bool, error) {
	var claimed bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := generated.New(tx.Conn()).ClaimUpgradeInterruptedTurn(ctx, generated.ClaimUpgradeInterruptedTurnParams{
			ConversationID:    resume.ConversationID,
			CurrentGeneration: resume.CurrentGeneration,
			MaxUserSequenceID: resume.MaxUserSequenceID,
		})
		claimed = rows == 1
		return err
	})
	return claimed, err
}

// FinishUpgradeInterruptedTurn clears the hidden claim immediately before the
// automatic retry. A false result means the claimed state changed unexpectedly.
func (db *DB) FinishUpgradeInterruptedTurn(ctx context.Context, conversationID string) (bool, error) {
	var finished bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := generated.New(tx.Conn()).FinishUpgradeInterruptedTurn(ctx, conversationID)
		finished = rows == 1
		return err
	})
	return finished, err
}

// MarkUpgradeResumeInterrupted converts a failed automatic resume into the
// ordinary manual-recovery state only if its startup token still names the same
// durable turn. A false result means newer work or cancellation won the race.
func (db *DB) MarkUpgradeResumeInterrupted(ctx context.Context, resume UpgradeResume) (bool, error) {
	var marked bool
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := generated.New(tx.Conn()).MarkUpgradeResumeInterrupted(ctx, generated.MarkUpgradeResumeInterruptedParams{
			ConversationID:    resume.ConversationID,
			CurrentGeneration: resume.CurrentGeneration,
			MaxUserSequenceID: resume.MaxUserSequenceID,
		})
		marked = rows == 1
		return err
	})
	return marked, err
}

// ClearConversationRuntimeState cancels work that has no live loop, including
// an automatic upgrade resume that has not claimed its token yet.
func (db *DB) ClearConversationRuntimeState(ctx context.Context, conversationID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		if err := q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{
			AgentWorking:   false,
			ConversationID: conversationID,
		}); err != nil {
			return err
		}
		return q.SetConversationTurnInterrupted(ctx, generated.SetConversationTurnInterruptedParams{
			TurnInterrupted: false,
			ConversationID:  conversationID,
		})
	})
}

// install an upgraded or customized binary and that the next process should
// resume the conversations that were mid-turn instead of clearing their
// agent_working flags. Written by restart paths that promise continuation,
// consumed exactly once by ConsumeResumeAfterUpgrade on the next startup.
const ResumeAfterUpgradeSettingKey = "resume_after_upgrade_restart"

func interruptionMetadataFlag(value any) bool {
	return value == true || value == "true"
}

func isInterruptionBookkeeping(message generated.Message) bool {
	if message.UserData == nil || *message.UserData == "" {
		return false
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(*message.UserData), &data); err != nil {
		return false
	}
	return data["distill_status"] != nil || interruptionMetadataFlag(data["distilled"]) || interruptionMetadataFlag(data["cwd_change"])
}

// LatestTurnEndedWithAgent reports whether the current generation's latest
// real turn message is a completed agent response. UI-only and synthetic
// bookkeeping rows are ignored. An EOT error deliberately returns false:
// Retry adds no new message, so working=true may mean that retry was interrupted.
func LatestTurnEndedWithAgent(messages []generated.Message, currentGeneration int64) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Generation != currentGeneration || isInterruptionBookkeeping(message) {
			continue
		}
		switch MessageType(message.Type) {
		case MessageTypeAgent:
			if message.LlmData == nil {
				return false
			}
			var data struct {
				EndOfTurn bool `json:"EndOfTurn"`
			}
			return json.Unmarshal([]byte(*message.LlmData), &data) == nil && data.EndOfTurn
		case MessageTypeUser, MessageTypeTool, MessageTypeError:
			// An EOT error may be followed by Retry(), which deliberately adds
			// no new message. working=true + terminal error is therefore valid
			// evidence of an interrupted retry and must still set the bit.
			return false
		}
	}
	return false
}

// upgradeResumable reports whether an upgrade restart resumes the
// conversation's interrupted turn: top-level conversations and delegated
// subagents. Other children are owned by their parent (BTW readers) or by
// their own recovery (transcription and commit-tour workers).
func upgradeResumable(conversation generated.Conversation) bool {
	if conversation.ParentConversationID == nil {
		return true
	}
	return !conversation.UserInitiated && ParseConversationOptions(conversation.ConversationOptions).Kind == ""
}

// ConsumeResumeAfterUpgrade decides, in a single transaction, what startup does
// with the agent_working flags left behind by the previous process:
//
//   - No ResumeAfterUpgradeSettingKey row: an ordinary restart or a crash. Set
//     turn_interrupted on every eligible top-level conversation still marked
//     working, then clear all stale agent_working flags.
//   - Row present: the previous process exited to install an upgrade. Delete the
//     row, capture a durable version token for each eligible top-level or
//     delegated-subagent stale turn (see upgradeResumable) while leaving
//     agent_working true, and return those tokens. Ineligible stale rows are
//     cleared before listeners open.
//
// The interrupted/working updates share this transaction, so a crash can
// neither lose the interruption nor expose an interrupted conversation as
// still working. The upgrade flag delete is likewise one-shot: once it commits,
// a crash mid-resume leaves the next boot on the ordinary interruption path.
func (db *DB) ConsumeResumeAfterUpgrade(ctx context.Context) ([]UpgradeResume, error) {
	var resumes []UpgradeResume
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		upgradeResume, err := q.DeleteSetting(ctx, ResumeAfterUpgradeSettingKey)
		if err != nil {
			return err
		}
		workingIDs, err := q.ListAgentWorkingConversationIDs(ctx)
		if err != nil {
			return err
		}

		if upgradeResume != 0 {
			for _, conversationID := range workingIDs {
				conversation, err := q.GetConversation(ctx, conversationID)
				if err != nil {
					return err
				}
				resumable := upgradeResumable(conversation)
				var messages []generated.Message
				if resumable {
					messages, err = q.ListMessages(ctx, conversationID)
					if err != nil {
						return err
					}
					resumable = !LatestTurnEndedWithAgent(messages, conversation.CurrentGeneration)
				}
				if resumable {
					// A prior upgrade process may have exited after claiming this
					// turn but before Retry. Re-arm the hidden claim before listeners
					// open so the new worker can claim the same durable version.
					if err := q.SetConversationTurnInterrupted(ctx, generated.SetConversationTurnInterruptedParams{
						TurnInterrupted: false,
						ConversationID:  conversationID,
					}); err != nil {
						return err
					}
					var maxUserSequenceID int64
					for _, message := range messages {
						if message.Type == string(MessageTypeUser) {
							maxUserSequenceID = message.SequenceID
						}
					}
					resumes = append(resumes, UpgradeResume{
						ConversationID:    conversationID,
						CurrentGeneration: conversation.CurrentGeneration,
						MaxUserSequenceID: maxUserSequenceID,
					})
					continue
				}
				if err := q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{
					AgentWorking:   false,
					ConversationID: conversationID,
				}); err != nil {
					return err
				}
				if err := q.SetConversationTurnInterrupted(ctx, generated.SetConversationTurnInterruptedParams{
					TurnInterrupted: false,
					ConversationID:  conversationID,
				}); err != nil {
					return err
				}
			}
			return nil
		}

		for _, conversationID := range workingIDs {
			conversation, err := q.GetConversation(ctx, conversationID)
			if err != nil {
				return err
			}
			// Managed children already project an idle unfinished turn as
			// interrupted in the BTW UI. Their parent owns their lifecycle.
			// Clear a hidden upgrade-resume claim a crashed process left.
			if conversation.ParentConversationID != nil {
				if err := q.SetConversationTurnInterrupted(ctx, generated.SetConversationTurnInterruptedParams{
					TurnInterrupted: false,
					ConversationID:  conversationID,
				}); err != nil {
					return err
				}
				continue
			}
			messages, err := q.ListMessages(ctx, conversationID)
			if err != nil {
				return err
			}
			// Current terminal agent writes clear agent_working atomically, so
			// this can only be legacy stale state. Do not turn a completed turn
			// into a resumable interruption. EOT errors are intentionally not
			// skipped: Retry() adds no row, so they can precede a real in-flight
			// retry left behind by the stopped process.
			if LatestTurnEndedWithAgent(messages, conversation.CurrentGeneration) {
				continue
			}
			if err := q.SetConversationTurnInterrupted(ctx, generated.SetConversationTurnInterruptedParams{
				TurnInterrupted: true,
				ConversationID:  conversationID,
			}); err != nil {
				return err
			}
		}
		return q.ResetAllAgentWorking(ctx)
	})
	if err != nil {
		return nil, err
	}
	return resumes, nil
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

// UpdateConversationModel sets the model for a conversation that doesn't have one yet.
// This is used to backfill the model for conversations created before the model column existed.
func (db *DB) UpdateConversationModel(ctx context.Context, conversationID, model string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.UpdateConversationModel(ctx, generated.UpdateConversationModelParams{
			Model:          &model,
			ConversationID: conversationID,
		})
	})
}

// ForceUpdateConversationModel updates the model on a conversation, even if already set.
func (db *DB) ForceUpdateConversationModel(ctx context.Context, conversationID, model string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.ForceUpdateConversationModel(ctx, generated.ForceUpdateConversationModelParams{
			Model:          &model,
			ConversationID: conversationID,
		})
	})
}

// Message methods (moved from MessageService)

// MessageType represents the type of message.
//
// Adding a type? Message rows are an APPEND-ONLY log, and a lot depends on that
// silently: the browser caches rows keyed by (conversation_id, sequence_id) and
// refreshes only by fetching the tail, forks copy rows wholesale, and the stream
// contract is that a sequence_id is delivered exactly once. So record data that
// arrives late by appending a row, never by rewriting one — and if the new row is
// invisible to users, check the places that ask "what is the last message" or
// "how many messages are there" (GetLatestActionableMessage, ListMessagesTail,
// CountConsecutiveMessagesByType, coalesceMessages) before assuming an unseen row
// is harmless.
type MessageType string

const (
	MessageTypeUser    MessageType = "user"
	MessageTypeAgent   MessageType = "agent"
	MessageTypeTool    MessageType = "tool"
	MessageTypeSystem  MessageType = "system"
	MessageTypeError   MessageType = "error"
	MessageTypeGitInfo MessageType = "gitinfo" // user-visible only, not sent to LLM
	MessageTypeWarning MessageType = "warning" // user-visible only, not sent to LLM
	// MessageTypeModelChange marks where the conversation switched models via
	// the /model command, or otherwise changed what its loop is built with (a
	// tool enabled mid-conversation). User-visible only, never sent to the LLM.
	MessageTypeModelChange MessageType = "modelchange"
	// MessageTypeSlug records the LLM call that generated the conversation's
	// slug. Rendered as nothing and never sent to the LLM: it exists only to
	// carry that call's usage, so the cost shows up in the conversation's
	// accounting via the same path as every other message's usage.
	//
	// It is an appended message rather than a mutable column because of the
	// append-only invariant described on MessageType above.
	MessageTypeSlug MessageType = "slug"
	// MessageTypeInPlaceCompaction rewrites how EARLIER messages of the same
	// generation are presented to the LLM: it squishes sequence-id ranges into
	// summaries and/or trims tool results. user_data holds the operations
	// (server.InPlaceCompaction); llm_data is NULL, so it is never itself sent.
	// It is a record rather than an edit because of the append-only invariant
	// described on MessageType above: the original rows stay intact for the UI.
	MessageTypeInPlaceCompaction MessageType = "inplacecompaction"
)

// CreateMessageParams contains parameters for creating a message
type CreateMessageParams struct {
	ConversationID string
	Type           MessageType
	LLMData        interface{} // Will be JSON marshalled
	UserData       interface{} // Will be JSON marshalled
	UsageData      interface{} // Will be JSON marshalled
	// LLMAPIURL and ModelName promote the LLM endpoint URL and the
	// provider-reported model name out of UsageData into first-class
	// columns so they can be queried directly. Empty strings are stored as
	// NULL (only assistant/agent messages with usage carry them).
	LLMAPIURL string
	ModelName string
	// UserEmail is the exe.dev account (from the X-ExeDev-Email header the
	// HTTPS proxy stamps) that authored a user message. Empty strings are
	// stored as NULL: only user messages carry it, and requests without the
	// header (direct/local access) leave it unset.
	UserEmail string
	// ExternalMessageID is the channel's id for a user message that arrived
	// from an external chat. Empty strings are stored as NULL.
	ExternalMessageID   string
	DisplayData         interface{} // Will be JSON marshalled, tool-specific display content
	ExcludedFromContext bool        // If true, message is stored but not sent to LLM
	// OtherUsageData is the usage of indirect LLM calls affiliated with this
	// message (compaction summarization for a summary message, LLM-backed
	// tools for a tool-result message), as []llm.PurposedUsage. Marshalled to
	// a JSON array; NULL when empty.
	OtherUsageData interface{}
	// MarkAgentDone, when true, also writes conversations.agent_working=false
	// inside the same Tx as the message INSERT. The list-patch stream's
	// OnCommit hook then fires exactly one patch carrying both the new
	// message AND working=false, instead of two patches where the first
	// snapshots the stale pre-flip working=true row. Mirrors the
	// SetAgentWorking(true)-before-recordMessage ordering AcceptUserMessage
	// uses on the Send side.
	MarkAgentDone bool
	// MarkAgentStart is the symmetric counterpart of MarkAgentDone: when true,
	// it writes conversations.agent_working=true inside the message-INSERT Tx.
	// AcceptUserMessage uses it on the user message that starts a turn so the
	// working flip and the message land in one commit (one list-patch), instead
	// of a separate SetAgentWorking(true) Tx fired right before.
	MarkAgentStart bool
	// BumpTimestamp, when true, updates conversations.updated_at inside the same
	// Tx as the INSERT so the conversation re-sorts to the top without a second
	// commit. Most message writes want this; system-prompt and other internal
	// metadata inserts deliberately leave it false (see the callers in convo.go).
	BumpTimestamp bool
	// RemoveQueuedID, when non-empty, drops the matching entry from the
	// conversation's queued_messages array inside the SAME Tx as the INSERT.
	// Drain uses this so creating the real (immutable) user message and
	// removing its queued mirror entry are atomic: if the Tx aborts, neither
	// the row nor the array change persists, so a crash can't leave an array
	// entry that Hydrate would re-feed as a duplicate.
	RemoveQueuedID string
	// CreatedAt overrides the row's created_at (default CURRENT_TIMESTAMP).
	// Every real caller leaves this nil so messages get true wall-clock
	// insertion time. The loremipsum debug generator is the one caller that
	// sets it, stamping created_at from its own synthetic clock so a
	// message's created_at and the tool/usage timestamps embedded in it
	// stay on one timeline instead of disagreeing about "now".
	CreatedAt *time.Time
}

// nullableString returns nil for an empty string so the column is stored as
// SQL NULL, and a pointer to the value otherwise.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// sqliteDateTimeFormat matches SQLite's native datetime string format
// (millisecond precision, UTC "Z" suffix), which strftime() and SQLite's
// date/time functions parse directly. Millisecond precision is enough to
// order rows the lorem generator writes 200ms apart; nanosecond-precision
// Go formats (e.g. time.RFC3339Nano) are NOT parsed by strftime(), which
// would leave conversations.sql's preview/timestamp queries unable to read
// an explicitly-stamped created_at back out.
const sqliteDateTimeFormat = "2006-01-02T15:04:05.000Z"

// sqliteTimeArg formats an explicit CreatedAt override for the generated
// query's created_at arg, or nil to let SQL's COALESCE(..., CURRENT_TIMESTAMP)
// apply the default.
func sqliteTimeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(sqliteDateTimeFormat)
}

// marshalMessageJSON marshals the JSON columns of a message into the
// nullable strings the generated query expects.
func marshalMessageJSON(params CreateMessageParams) (llmDataJSON, userDataJSON, usageDataJSON, displayDataJSON, otherUsageDataJSON *string, err error) {
	marshal := func(v interface{}, what string) (*string, error) {
		if v == nil {
			return nil, nil
		}
		data, merr := json.Marshal(v)
		if merr != nil {
			return nil, fmt.Errorf("failed to marshal %s: %w", what, merr)
		}
		str := string(data)
		return &str, nil
	}
	if llmDataJSON, err = marshal(params.LLMData, "LLM data"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if userDataJSON, err = marshal(params.UserData, "user data"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if usageDataJSON, err = marshal(params.UsageData, "usage data"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if displayDataJSON, err = marshal(params.DisplayData, "display data"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if otherUsageDataJSON, err = marshal(params.OtherUsageData, "other usage data"); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return llmDataJSON, userDataJSON, usageDataJSON, displayDataJSON, otherUsageDataJSON, nil
}

// insertMessageTx inserts one message within an open Tx, allocating its
// sequence_id and stamping the conversation's current generation. Shared by
// CreateMessage and CreateMessages so single and bulk inserts are identical.
func insertMessageTx(ctx context.Context, q *generated.Queries, params CreateMessageParams) (generated.Message, error) {
	if params.MarkAgentDone && params.MarkAgentStart {
		return generated.Message{}, fmt.Errorf("insertMessageTx: MarkAgentDone and MarkAgentStart are mutually exclusive")
	}
	llmDataJSON, userDataJSON, usageDataJSON, displayDataJSON, otherUsageDataJSON, err := marshalMessageJSON(params)
	if err != nil {
		return generated.Message{}, err
	}
	sequenceID, err := q.GetNextSequenceID(ctx, params.ConversationID)
	if err != nil {
		return generated.Message{}, fmt.Errorf("failed to get next sequence ID: %w", err)
	}
	conversation, err := q.GetConversation(ctx, params.ConversationID)
	if err != nil {
		return generated.Message{}, fmt.Errorf("failed to get conversation generation: %w", err)
	}
	message, err := q.CreateMessage(ctx, generated.CreateMessageParams{
		MessageID:           uuid.New().String(),
		ConversationID:      params.ConversationID,
		SequenceID:          sequenceID,
		Generation:          conversation.CurrentGeneration,
		Type:                string(params.Type),
		LlmData:             llmDataJSON,
		UserData:            userDataJSON,
		UsageData:           usageDataJSON,
		DisplayData:         displayDataJSON,
		ExcludedFromContext: params.ExcludedFromContext,
		LlmApiUrl:           nullableString(params.LLMAPIURL),
		ModelName:           nullableString(params.ModelName),
		UserEmail:           nullableString(params.UserEmail),
		OtherUsageData:      otherUsageDataJSON,
		ExternalMessageID:   nullableString(params.ExternalMessageID),
		CreatedAt:           sqliteTimeArg(params.CreatedAt),
	})
	if err != nil {
		return generated.Message{}, err
	}
	if params.MarkAgentDone || params.MarkAgentStart {
		if err := q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{
			AgentWorking:   params.MarkAgentStart,
			ConversationID: params.ConversationID,
		}); err != nil {
			return generated.Message{}, err
		}
	}
	if params.RemoveQueuedID != "" {
		raw, err := q.GetConversationQueuedMessages(ctx, params.ConversationID)
		if err != nil {
			return generated.Message{}, err
		}
		msgs, err := ParseQueuedMessagesStrict(raw)
		if err != nil {
			return generated.Message{}, err
		}
		kept := msgs[:0]
		found := false
		for _, m := range msgs {
			if m.ID == params.RemoveQueuedID {
				found = true
				continue
			}
			kept = append(kept, m)
		}
		if !found {
			return generated.Message{}, ErrQueuedMessageNotFound
		}
		jsonStr, err := MarshalQueuedMessages(kept)
		if err != nil {
			return generated.Message{}, err
		}
		// UpdateConversationQueuedMessages also bumps updated_at, so this
		// satisfies the re-sort even when BumpTimestamp wasn't set.
		if err := q.UpdateConversationQueuedMessages(ctx, generated.UpdateConversationQueuedMessagesParams{
			QueuedMessages: jsonStr,
			ConversationID: params.ConversationID,
		}); err != nil {
			return generated.Message{}, err
		}
	}
	if params.BumpTimestamp {
		if err := q.UpdateConversationTimestamp(ctx, params.ConversationID); err != nil {
			return generated.Message{}, err
		}
	}
	return message, nil
}

// CreateMessage creates a new message
func (db *DB) CreateMessage(ctx context.Context, params CreateMessageParams) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		message, err = insertMessageTx(ctx, generated.New(tx.Conn()), params)
		return err
	})
	return &message, err
}

// CreateMessages inserts several messages and bumps the conversation timestamp
// in a SINGLE transaction, so exactly one commit hook fires (one
// conversation-list recompute, one SSE notification) regardless of how many
// messages are written. Compaction copies a whole tail of messages forward;
// doing each in its own Tx triggered a full-list recompute per message, which
// is visibly slow on a large conversation list. Returns the created rows in
// input order. All messages must target the same conversation.
func (db *DB) CreateMessages(ctx context.Context, paramsList []CreateMessageParams) ([]generated.Message, error) {
	if len(paramsList) == 0 {
		return nil, nil
	}
	conversationID := paramsList[0].ConversationID
	out := make([]generated.Message, 0, len(paramsList))
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		for _, params := range paramsList {
			if params.ConversationID != conversationID {
				return fmt.Errorf("CreateMessages: all messages must share a conversation (%q != %q)", params.ConversationID, conversationID)
			}
			msg, err := insertMessageTx(ctx, q, params)
			if err != nil {
				return err
			}
			out = append(out, msg)
		}
		return q.UpdateConversationTimestamp(ctx, conversationID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateSlugMessage appends a slug marker carrying the usage of the LLM call
// that generated the conversation's slug. The message has no content and
// renders as nothing; it exists so the cost is accounted for through the same
// path as every other message's usage, without mutating an existing row.
//
// Excluded from context so it never reaches the LLM even if a future context
// builder forgets to filter the type. llm_data stays NULL, which is a second
// belt: convertToLLMMessage rejects NULL, so paths that build context without
// checking the type (broadcastEstimatedContextSize, buildConversationSummary)
// skip the marker too.
//
// Returns (nil, nil) when there is nothing to record; callers use that to decide
// whether there is a marker to publish.
func (db *DB) CreateSlugMessage(ctx context.Context, conversationID string, otherUsage []llm.PurposedUsage) (*generated.Message, error) {
	if len(otherUsage) == 0 {
		return nil, nil
	}
	otherUsageJSON, err := marshalJSON(otherUsage)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal slug usage: %w", err)
	}
	var message generated.Message
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		conversation, err := q.GetConversation(ctx, conversationID)
		if err != nil {
			return fmt.Errorf("failed to get conversation: %w", err)
		}
		sequenceID, err := q.GetNextSequenceID(ctx, conversationID)
		if err != nil {
			return fmt.Errorf("failed to get next sequence ID: %w", err)
		}
		message, err = q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:           uuid.New().String(),
			ConversationID:      conversationID,
			SequenceID:          sequenceID,
			Generation:          conversation.CurrentGeneration,
			Type:                string(MessageTypeSlug),
			OtherUsageData:      otherUsageJSON,
			ExcludedFromContext: true,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &message, nil
}

type CreateWarningMessageResult struct {
	Message      *generated.Message
	Conversation generated.Conversation
	Suppressed   bool
}

// CreateWarningMessage creates a user-visible warning that is never sent to the LLM.
// Consecutive warnings are capped so provider retry storms don't fill the DB.
func (db *DB) CreateWarningMessage(ctx context.Context, conversationID, text string, maxConsecutive int64, suppressedText string) (*CreateWarningMessageResult, error) {
	if maxConsecutive < 1 {
		return nil, fmt.Errorf("maxConsecutive must be positive")
	}

	var result CreateWarningMessageResult
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())

		conversation, err := q.GetConversation(ctx, conversationID)
		if err != nil {
			return fmt.Errorf("failed to get conversation generation: %w", err)
		}
		result.Conversation = conversation

		count, err := q.CountConsecutiveMessagesByType(ctx, generated.CountConsecutiveMessagesByTypeParams{
			ConversationID: conversationID,
			Generation:     conversation.CurrentGeneration,
			Type:           string(MessageTypeWarning),
		})
		if err != nil {
			return fmt.Errorf("failed to count consecutive warnings: %w", err)
		}
		if count >= maxConsecutive {
			result.Suppressed = true
			return nil
		}

		userData := map[string]interface{}{"text": text}
		if count == maxConsecutive-1 {
			userData["suppression_text"] = suppressedText
			userData["suppressed"] = true
		}
		userDataJSON, err := marshalJSON(userData)
		if err != nil {
			return fmt.Errorf("failed to marshal warning data: %w", err)
		}

		sequenceID, err := q.GetNextSequenceID(ctx, conversationID)
		if err != nil {
			return fmt.Errorf("failed to get next sequence ID: %w", err)
		}

		message, err := q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:           uuid.New().String(),
			ConversationID:      conversationID,
			SequenceID:          sequenceID,
			Generation:          conversation.CurrentGeneration,
			Type:                string(MessageTypeWarning),
			UserData:            userDataJSON,
			ExcludedFromContext: true,
		})
		if err != nil {
			return err
		}
		result.Message = &message

		if err := q.UpdateConversationTimestamp(ctx, conversationID); err != nil {
			return fmt.Errorf("failed to update conversation timestamp: %w", err)
		}
		conversation, err = q.GetConversation(ctx, conversationID)
		if err != nil {
			return fmt.Errorf("failed to get updated conversation: %w", err)
		}
		result.Conversation = conversation
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func marshalJSON(v interface{}) (*string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	str := string(data)
	return &str, nil
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

// ListMessages retrieves all messages in a conversation ordered by sequence
func (db *DB) ListMessages(ctx context.Context, conversationID string) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListMessages(ctx, conversationID)
		return err
	})
	return messages, err
}

// ListMessagesForContext retrieves messages that should be sent to the LLM (excludes excluded_from_context=true)
func (db *DB) ListMessagesForContext(ctx context.Context, conversationID string) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListMessagesForContext(ctx, conversationID)
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

// ListTypedUserMessages returns the messages the user typed in a
// conversation, newest first.
func (db *DB) ListTypedUserMessages(ctx context.Context, conversationID string) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		messages, err = generated.New(rx.Conn()).ListTypedUserMessages(ctx, conversationID)
		return err
	})
	return messages, err
}

// GetAgentMessageBefore returns the newest agent message of a conversation
// before sequenceID.
func (db *DB) GetAgentMessageBefore(ctx context.Context, conversationID string, sequenceID int64) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		message, err = generated.New(rx.Conn()).GetAgentMessageBefore(ctx, generated.GetAgentMessageBeforeParams{
			ConversationID: conversationID,
			SequenceID:     sequenceID,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &message, nil
}

// ListAgentMessagesSinceLastUser returns the agent messages produced since
// the most recent user message in a conversation, newest first (or all
// agent messages if there is no user message). Useful for picking a
// notification body that walks back through a tail of tool-only turns up
// to the previous user turn boundary.
func (db *DB) ListAgentMessagesSinceLastUser(ctx context.Context, conversationID string) ([]generated.Message, error) {
	var messages []generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		messages, err = q.ListAgentMessagesSinceLastUser(ctx, generated.ListAgentMessagesSinceLastUserParams{
			ConversationID:   conversationID,
			ConversationID_2: conversationID,
		})
		return err
	})
	return messages, err
}

// GetLatestActionableMessage retrieves the latest message a user could act on.
// Slug markers are skipped; see the query comment for why.
func (db *DB) GetLatestActionableMessage(ctx context.Context, conversationID string) (*generated.Message, error) {
	var message generated.Message
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		message, err = q.GetLatestActionableMessage(ctx, conversationID)
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

// ListConversationsWithQueuedTranscriptions returns every conversation, archived
// or not, whose queued_messages array holds a transcription item.
func (db *DB) ListConversationsWithQueuedTranscriptions(ctx context.Context) ([]generated.Conversation, error) {
	var conversations []generated.Conversation
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		conversations, err = generated.New(rx.Conn()).ListConversationsWithQueuedTranscriptions(ctx)
		return err
	})
	return conversations, err
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

// ForkConversation creates a new top-level conversation that copies the source
// conversation's messages up to and including cutoffSequenceID. It copies the
// generation that was ACTIVE AT THE FORK POINT (the generation of the last
// message at or before the cutoff), not the source's current generation —
// forking at a message from an older generation must copy that older
// generation, or the fork would be empty. The copies are renumbered to
// generation 1 so the fork starts a fresh generation history (compaction etc.
// begin anew). The new conversation inherits the source's cwd, model, and
// options. Its slug starts nil; the caller assigns one. Returns the new
// conversation.
// ErrInvalidForkPoint is returned by ForkConversation when no message exists
// at or before the requested cutoff sequence.
var (
	ErrInvalidForkPoint    = errors.New("no message at or before fork point")
	ErrCannotForkBtwReader = errors.New("BTW readers cannot be forked")
)

func (db *DB) ForkConversation(ctx context.Context, sourceConversationID string, cutoffSequenceID int64) (*generated.Conversation, error) {
	conversationID, err := GenerateConversationID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	var conversation generated.Conversation
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		source, err := q.GetConversation(ctx, sourceConversationID)
		if err != nil {
			return fmt.Errorf("failed to load source conversation: %w", err)
		}
		if _, ok := ManagedBtwReaderIdentity(source); ok {
			return ErrCannotForkBtwReader
		}
		optionsJSON, _, err := scrubManagedBtwOptions(source.ConversationOptions)
		if err != nil {
			return fmt.Errorf("failed to scrub fork options: %w", err)
		}
		conversation, err = q.CreateConversation(ctx, generated.CreateConversationParams{
			ConversationID:      conversationID,
			Slug:                nil,
			UserInitiated:       true,
			Cwd:                 source.Cwd,
			Model:               source.Model,
			ConversationOptions: optionsJSON,
		})
		if err != nil {
			return fmt.Errorf("failed to create forked conversation: %w", err)
		}
		conversation, err = q.UpdateConversationTags(ctx, generated.UpdateConversationTagsParams{
			Tags:           source.Tags,
			ConversationID: conversationID,
		})
		if err != nil {
			return fmt.Errorf("failed to copy conversation tags: %w", err)
		}
		// Copy the generation active at the fork point, renumbered to
		// generation 1 in the fork. The new conversation keeps CreateConversation's
		// default current_generation of 1.
		forkGeneration, err := q.GetGenerationAtOrBeforeSequence(ctx, generated.GetGenerationAtOrBeforeSequenceParams{
			ConversationID: sourceConversationID,
			SequenceID:     cutoffSequenceID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidForkPoint
		}
		if err != nil {
			return fmt.Errorf("failed to resolve fork-point generation: %w", err)
		}
		if err := q.CopyMessagesForFork(ctx, generated.CopyMessagesForForkParams{
			DestConversationID:   conversationID,
			SourceConversationID: sourceConversationID,
			CutoffSequenceID:     cutoffSequenceID,
			SourceGeneration:     forkGeneration,
		}); err != nil {
			return fmt.Errorf("failed to copy messages: %w", err)
		}
		return nil
	})
	return &conversation, err
}

// CreateSubagentConversation creates a new subagent conversation with a parent
func (db *DB) CreateSubagentConversation(ctx context.Context, slug, parentID string, cwd *string) (*generated.Conversation, error) {
	conversationID, err := GenerateConversationID()
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

// GetSubagentUsage aggregates LLM usage across all descendant conversations
// of parentID (recursively), grouped by model.
func (db *DB) GetSubagentUsage(ctx context.Context, parentID string) ([]generated.GetSubagentUsageRow, error) {
	var rows []generated.GetSubagentUsageRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		rows, err = q.GetSubagentUsage(ctx, &parentID)
		return err
	})
	return rows, err
}

// GetSubagentOtherUsage aggregates indirect LLM usage (other_usage_data
// entries) across all descendant conversations of parentID (recursively),
// grouped by model.
func (db *DB) GetSubagentOtherUsage(ctx context.Context, parentID string) ([]generated.GetSubagentOtherUsageRow, error) {
	var rows []generated.GetSubagentOtherUsageRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		rows, err = q.GetSubagentOtherUsage(ctx, &parentID)
		return err
	})
	return rows, err
}

// GetMaxSequenceIDsForAllConversations returns a map of conversation_id -> max sequence_id.
func (db *DB) GetMaxSequenceIDsForAllConversations(ctx context.Context) (map[string]int64, error) {
	var rows []generated.GetMaxSequenceIDsForAllConversationsRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		rows, err = q.GetMaxSequenceIDsForAllConversations(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(rows))
	for _, r := range rows {
		result[r.ConversationID] = r.MaxSequenceID
	}
	return result, nil
}

// UpdateConversationParent sets the parent_conversation_id for a conversation
func (db *DB) UpdateConversationParent(ctx context.Context, conversationID, parentID string) (*generated.Conversation, error) {
	var conversation generated.Conversation
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		conversation, err = q.UpdateConversationParent(ctx, generated.UpdateConversationParentParams{
			ParentConversationID: &parentID,
			ConversationID:       conversationID,
		})
		return err
	})
	return &conversation, err
}

// DistillReplaceSwap atomically renames the source conversation's slug, assigns the
// original slug to the new conversation, sets the source as a child of the new
// conversation, and archives the source. All within a single transaction.
func (db *DB) DistillReplaceSwap(ctx context.Context, sourceConvID, newConvID, newSourceSlug, originalSlug string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		// 1. Rename source slug
		if _, err := q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
			Slug:           &newSourceSlug,
			ConversationID: sourceConvID,
		}); err != nil {
			return fmt.Errorf("rename source slug: %w", err)
		}
		// 2. Assign original slug to new conversation
		if _, err := q.UpdateConversationSlug(ctx, generated.UpdateConversationSlugParams{
			Slug:           &originalSlug,
			ConversationID: newConvID,
		}); err != nil {
			return fmt.Errorf("assign original slug to new conv: %w", err)
		}
		// 3. Set source as child of new conversation
		if _, err := q.UpdateConversationParent(ctx, generated.UpdateConversationParentParams{
			ParentConversationID: &newConvID,
			ConversationID:       sourceConvID,
		}); err != nil {
			return fmt.Errorf("set parent: %w", err)
		}
		// 4. Archive source
		if _, err := q.ArchiveConversation(ctx, sourceConvID); err != nil {
			return fmt.Errorf("archive source: %w", err)
		}
		return nil
	})
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

// GetModels returns all models from the database
func (db *DB) GetModels(ctx context.Context) ([]generated.Model, error) {
	var models []generated.Model
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		models, err = q.GetModels(ctx)
		return err
	})
	return models, err
}

// GetModel returns a model by ID
func (db *DB) GetModel(ctx context.Context, modelID string) (*generated.Model, error) {
	var model generated.Model
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		model, err = q.GetModel(ctx, modelID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// CreateModel creates a new model
func (db *DB) CreateModel(ctx context.Context, params generated.CreateModelParams) (*generated.Model, error) {
	if params.ReasoningReplay == "" {
		params.ReasoningReplay = "auto"
	}
	var model generated.Model
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		model, err = q.CreateModel(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// UpdateModel updates a model
func (db *DB) UpdateModel(ctx context.Context, params generated.UpdateModelParams) (*generated.Model, error) {
	if params.ReasoningReplay == "" {
		params.ReasoningReplay = "auto"
	}
	var model generated.Model
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		model, err = q.UpdateModel(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// DeleteModel deletes a model
func (db *DB) DeleteModel(ctx context.Context, modelID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.DeleteModel(ctx, modelID)
	})
}

func (db *DB) GetNotificationChannels(ctx context.Context) ([]generated.NotificationChannel, error) {
	var channels []generated.NotificationChannel
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		channels, err = q.GetNotificationChannels(ctx)
		return err
	})
	return channels, err
}

func (db *DB) GetEnabledNotificationChannels(ctx context.Context) ([]generated.NotificationChannel, error) {
	var channels []generated.NotificationChannel
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		channels, err = q.GetEnabledNotificationChannels(ctx)
		return err
	})
	return channels, err
}

func (db *DB) GetNotificationChannel(ctx context.Context, channelID string) (*generated.NotificationChannel, error) {
	var ch generated.NotificationChannel
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		ch, err = q.GetNotificationChannel(ctx, channelID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (db *DB) CreateNotificationChannel(ctx context.Context, params generated.CreateNotificationChannelParams) (*generated.NotificationChannel, error) {
	var ch generated.NotificationChannel
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		ch, err = q.CreateNotificationChannel(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (db *DB) UpdateNotificationChannel(ctx context.Context, params generated.UpdateNotificationChannelParams) (*generated.NotificationChannel, error) {
	var ch generated.NotificationChannel
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		var err error
		ch, err = q.UpdateNotificationChannel(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (db *DB) DeleteNotificationChannel(ctx context.Context, channelID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.DeleteNotificationChannel(ctx, channelID)
	})
}

// GetSetting retrieves a setting value by key
// Returns empty string and nil error if the setting doesn't exist
func (db *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		value, err = q.GetSetting(ctx, key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return value, err
}

// SetSetting sets a setting value by key
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.SetSetting(ctx, generated.SetSettingParams{
			Key:   key,
			Value: value,
		})
	})
}

// InitSetting returns the value stored for key, first storing value if the
// key is unset. Concurrent callers all see the same winning value.
func (db *DB) InitSetting(ctx context.Context, key, value string) (string, error) {
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		stored, err := q.GetSetting(ctx, key)
		if err == nil {
			value = stored
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return q.SetSetting(ctx, generated.SetSettingParams{Key: key, Value: value})
	})
	return value, err
}

// GetAllSettings retrieves all settings
func (db *DB) GetAllSettings(ctx context.Context) (map[string]string, error) {
	var rows []generated.GetAllSettingsRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		rows, err = q.GetAllSettings(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}

	settings := make(map[string]string)
	for _, row := range rows {
		settings[row.Key] = row.Value
	}
	return settings, nil
}

// ── Cache sessions (browser IDB encryption keys) ─────────────────────────────

// ErrNoCacheSession is returned by GetCacheSession when no row exists.
var ErrNoCacheSession = errors.New("cache session not found")

// CacheSession is the public projection of a cache_sessions row.
type CacheSession struct {
	TokenHash  string
	UserID     string
	CreatedAt  string
	LastSeenAt string
}

// GetCacheSession returns the row keyed by token_hash, or ErrNoCacheSession.
func (db *DB) GetCacheSession(ctx context.Context, tokenHash string) (CacheSession, error) {
	var out CacheSession
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		row, err := q.GetCacheSession(ctx, tokenHash)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNoCacheSession
		case err != nil:
			return err
		}
		out = CacheSession{
			TokenHash:  row.TokenHash,
			UserID:     row.UserID,
			CreatedAt:  row.CreatedAt.Format(time.RFC3339),
			LastSeenAt: row.LastSeenAt.Format(time.RFC3339),
		}
		return nil
	})
	return out, err
}

// UpsertCacheSession creates or updates the row (refreshing last_seen_at).
func (db *DB) UpsertCacheSession(ctx context.Context, tokenHash, userID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.UpsertCacheSession(ctx, generated.UpsertCacheSessionParams{
			TokenHash: tokenHash,
			UserID:    userID,
		})
	})
}

// DeleteCacheSession removes the row, effectively logging that browser out
// of the IDB cache.
func (db *DB) DeleteCacheSession(ctx context.Context, tokenHash string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.DeleteCacheSession(ctx, tokenHash)
	})
}

// ── Feature flag overrides ───────────────────────────────────────────────────

// GetAllFeatureFlagOverrides returns every persisted override as raw JSON text
// keyed by flag name. Callers are responsible for filtering out names not
// recognized by the in-code registry.
func (db *DB) GetAllFeatureFlagOverrides(ctx context.Context) (map[string]string, error) {
	var rows []generated.GetAllFeatureFlagsRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		rows, err = q.GetAllFeatureFlags(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Name] = r.Value
	}
	return out, nil
}

// SetFeatureFlagOverride upserts a JSON-encoded override.
func (db *DB) SetFeatureFlagOverride(ctx context.Context, name, jsonValue string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.SetFeatureFlag(ctx, generated.SetFeatureFlagParams{
			Name:  name,
			Value: jsonValue,
		})
	})
}

// DeleteFeatureFlagOverride removes a stored override, reverting to the
// code-defined default.
func (db *DB) DeleteFeatureFlagOverride(ctx context.Context, name string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.DeleteFeatureFlag(ctx, name)
	})
}
