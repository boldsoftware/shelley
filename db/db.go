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

type ConversationOptions struct {
	Type            string `json:"type,omitempty"`             // "normal" (default) or "orchestrator"
	SubagentBackend string `json:"subagent_backend,omitempty"` // "shelley" (default), "claude-cli", "codex-cli"
	// ToolOverrides maps tool name to "on" or "off". Tools not listed use their default.
	ToolOverrides map[string]string `json:"tool_overrides,omitempty"`
	// DisableAllTools disables every tool by default; ToolOverrides with "on" re-enable individual tools.
	// Useful for API clients that can't enumerate the tool registry.
	DisableAllTools bool `json:"disable_all_tools,omitempty"`
	// EndOfTurnHooks are posted to whenever a top-level agent turn ends.
	EndOfTurnHooks []ConversationHook `json:"end_of_turn_hooks,omitempty"`
}

// IsOrchestrator returns true if the conversation is in orchestrator mode.
func (o ConversationOptions) IsOrchestrator() bool {
	return o.Type == "orchestrator"
}

// ParseConversationOptions parses a JSON string into ConversationOptions.
// Returns zero-value options (type=normal) for empty or invalid input.
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

// RegisterConversationHook atomically adds hook to conversation options if absent.
func (db *DB) RegisterConversationHook(ctx context.Context, conversationID string, hook ConversationHook) (ConversationOptions, error) {
	var opts ConversationOptions
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		raw, err := q.GetConversationOptions(ctx, conversationID)
		if err != nil {
			return err
		}
		opts = ParseConversationOptions(raw)
		for _, existing := range opts.EndOfTurnHooks {
			if existing.URL == hook.URL {
				return nil
			}
		}
		opts.EndOfTurnHooks = append(append([]ConversationHook(nil), opts.EndOfTurnHooks...), hook)
		optsJSON, err := json.Marshal(opts)
		if err != nil {
			return fmt.Errorf("failed to marshal conversation options: %w", err)
		}
		return q.UpdateConversationOptions(ctx, generated.UpdateConversationOptionsParams{
			ConversationID:      conversationID,
			ConversationOptions: string(optsJSON),
		})
	})
	return opts, err
}

// CreateConversation creates a new conversation with an optional slug
func (db *DB) CreateConversation(ctx context.Context, slug *string, userInitiated bool, cwd, model *string, opts ConversationOptions) (*generated.Conversation, error) {
	conversationID, err := generateConversationID()
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

// ConversationListItem is a conversation row plus the three derived fields the
// conversation list needs but that don't live on the conversation row: a
// one-line preview of the trailing agent message, that message's timestamp,
// and the conversation's current max sequence_id. All three are computed by
// correlated subqueries IN the list/search query itself (see
// conversations.sql), scoped to exactly the window of conversations returned,
// so we never scan or JSON-decode messages for conversations off-window.
type ConversationListItem struct {
	generated.Conversation
	Preview          string
	PreviewUpdatedAt string // RFC 3339 (trailing Z), empty if there's no preview message
	MaxSequenceID    int64
}

// previewTimestampLen is the fixed width of the RFC3339 timestamp
// ("2006-01-02T15:04:05Z") that the list/search queries prepend to the
// preview text in the preview_packed column. See the preview_packed note in
// conversations.sql.
const previewTimestampLen = len("2006-01-02T15:04:05Z")

// splitPreviewPacked splits the preview_packed column back into its timestamp
// and (already truncated) preview text. An empty packed value means the
// conversation has no agent-message preview, so both results are empty.
func splitPreviewPacked(packed string) (preview, updatedAt string) {
	if len(packed) < previewTimestampLen {
		return "", ""
	}
	return packed[previewTimestampLen:], packed[:previewTimestampLen]
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
			preview, updatedAt := splitPreviewPacked(r.PreviewPacked)
			items[i] = ConversationListItem{
				Conversation:     r.Conversation,
				Preview:          preview,
				PreviewUpdatedAt: updatedAt,
				MaxSequenceID:    r.MaxSequenceID,
			}
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
			preview, updatedAt := splitPreviewPacked(r.PreviewPacked)
			items[i] = ConversationListItem{
				Conversation:     r.Conversation,
				Preview:          preview,
				PreviewUpdatedAt: updatedAt,
				MaxSequenceID:    r.MaxSequenceID,
			}
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
			preview, updatedAt := splitPreviewPacked(r.PreviewPacked)
			items[i] = ConversationListItem{
				Conversation:     r.Conversation,
				Preview:          preview,
				PreviewUpdatedAt: updatedAt,
				MaxSequenceID:    r.MaxSequenceID,
			}
		}
		return nil
	})
	return items, err
}

// SearchConversationsWithMessages searches for conversations containing the query in slug or message content
func (db *DB) SearchConversationsWithMessages(ctx context.Context, query string, limit, offset int64) ([]ConversationListItem, error) {
	queryPtr := &query
	var items []ConversationListItem
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		rows, err := q.SearchConversationsWithMessages(ctx, generated.SearchConversationsWithMessagesParams{
			Column1: queryPtr,
			Column2: queryPtr,
			Column3: queryPtr,
			Limit:   limit,
			Offset:  offset,
		})
		if err != nil {
			return err
		}
		items = make([]ConversationListItem, len(rows))
		for i, r := range rows {
			preview, updatedAt := splitPreviewPacked(r.PreviewPacked)
			items[i] = ConversationListItem{
				Conversation:     r.Conversation,
				Preview:          preview,
				PreviewUpdatedAt: updatedAt,
				MaxSequenceID:    r.MaxSequenceID,
			}
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
	Conversation generated.Conversation
	Snippet      string // empty if matched only by slug
	// Preview, PreviewUpdatedAt and MaxSequenceID are the same derived
	// conversation-list fields carried by ConversationListItem, computed in
	// the search query itself (see SearchConversationsFTSList).
	Preview          string
	PreviewUpdatedAt string
	MaxSequenceID    int64
}

// SnippetMarkStart and SnippetMarkEnd surround matched terms inside
// Snippet strings produced by SearchConversationsFTS.
const (
	SnippetMarkStart = "\x02"
	SnippetMarkEnd   = "\x03"
)

// SearchConversationsFTS performs a full-text search over user/agent message
// content (via the messages_fts FTS5 virtual table) and slug substring across
// ALL top-level conversations (active and archived). Active conversations are
// returned first, then archived; both buckets are ordered by updated_at DESC.
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
			preview, updatedAt := splitPreviewPacked(c.PreviewPacked)
			results[i] = ConversationSearchResult{
				Conversation:     c.Conversation,
				Preview:          preview,
				PreviewUpdatedAt: updatedAt,
				MaxSequenceID:    c.MaxSequenceID,
			}
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
			if _, ok := snippets[r.ConversationID]; ok {
				continue // first row per conv = best rank
			}
			snippets[r.ConversationID] = centerOnMark(r.Snippet, 120)
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
// Called once during server startup to recover from a previous process that
// exited mid-loop and left stale TRUE values in the table.
func (db *DB) ResetAllAgentWorking(ctx context.Context) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.ResetAllAgentWorking(ctx)
	})
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

// MessageType represents the type of message
type MessageType string

const (
	MessageTypeUser    MessageType = "user"
	MessageTypeAgent   MessageType = "agent"
	MessageTypeTool    MessageType = "tool"
	MessageTypeSystem  MessageType = "system"
	MessageTypeError   MessageType = "error"
	MessageTypeGitInfo MessageType = "gitinfo" // user-visible only, not sent to LLM
	MessageTypeWarning MessageType = "warning" // user-visible only, not sent to LLM
)

// CreateMessageParams contains parameters for creating a message
type CreateMessageParams struct {
	ConversationID      string
	Type                MessageType
	LLMData             interface{} // Will be JSON marshalled
	UserData            interface{} // Will be JSON marshalled
	UsageData           interface{} // Will be JSON marshalled
	DisplayData         interface{} // Will be JSON marshalled, tool-specific display content
	ExcludedFromContext bool        // If true, message is stored but not sent to LLM
	// MarkAgentDone, when true, also writes conversations.agent_working=false
	// inside the same Tx as the message INSERT. The list-patch stream's
	// OnCommit hook then fires exactly one patch carrying both the new
	// message AND working=false, instead of two patches where the first
	// snapshots the stale pre-flip working=true row. Mirrors the
	// SetAgentWorking(true)-before-recordMessage ordering AcceptUserMessage
	// uses on the Send side.
	MarkAgentDone bool
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

		conversation, err := q.GetConversation(ctx, params.ConversationID)
		if err != nil {
			return fmt.Errorf("failed to get conversation generation: %w", err)
		}

		message, err = q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID:           messageID,
			ConversationID:      params.ConversationID,
			SequenceID:          sequenceID,
			Generation:          conversation.CurrentGeneration,
			Type:                string(params.Type),
			LlmData:             llmDataJSON,
			UserData:            userDataJSON,
			UsageData:           usageDataJSON,
			DisplayData:         displayDataJSON,
			ExcludedFromContext: params.ExcludedFromContext,
		})
		if err != nil {
			return err
		}
		if params.MarkAgentDone {
			return q.SetConversationAgentWorking(ctx, generated.SetConversationAgentWorkingParams{
				AgentWorking:   false,
				ConversationID: params.ConversationID,
			})
		}
		return nil
	})
	return &message, err
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

// UpdateMessageUserData updates the user_data JSON field of a message
func (db *DB) UpdateMessageUserData(ctx context.Context, messageID string, userData *string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.UpdateMessageUserData(ctx, generated.UpdateMessageUserDataParams{
			MessageID: messageID,
			UserData:  userData,
		})
	})
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

// GetSubagentCounts returns a map of parent_conversation_id -> subagent count.
func (db *DB) GetSubagentCounts(ctx context.Context) (map[string]int64, error) {
	var rows []generated.GetSubagentCountsRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		q := generated.New(rx.Conn())
		var err error
		rows, err = q.GetSubagentCounts(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		if r.ParentConversationID != nil {
			counts[*r.ParentConversationID] = r.Count
		}
	}
	return counts, nil
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
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoCacheSession
			}
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

// TouchCacheSession bumps last_seen_at. No error if the row is missing.
func (db *DB) TouchCacheSession(ctx context.Context, tokenHash string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		q := generated.New(tx.Conn())
		return q.TouchCacheSession(ctx, tokenHash)
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
