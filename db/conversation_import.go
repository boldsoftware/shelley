package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"shelley.exe.dev/db/generated"
)

// ImportedConversation is a conversation copied from another Shelley's
// database, with its messages and subagents.
type ImportedConversation struct {
	Conversation generated.Conversation
	Messages     []generated.Message
	Subagents    []ImportedConversation
}

// ReadConversationsForImport reads the top-level conversations ids, their
// messages, and their subagents (recursively) from the Shelley database at
// path, opened read-only. Only idle, unqueued, non-draft conversations can
// be imported, so nothing is in flight.
func ReadConversationsForImport(ctx context.Context, path string, ids []string) ([]ImportedConversation, error) {
	source, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer source.Close()
	// A read-only connection to a database in WAL mode needs its -shm file,
	// or a directory it can create one in.
	if _, err := source.ExecContext(ctx, "PRAGMA schema_version"); err != nil {
		return nil, fmt.Errorf("%w: open %s read-only: %v (in WAL mode, it needs its -wal and -shm files or a writable directory)", ErrInvalid, path, err)
	}
	q := generated.New(source)
	var read func(id string, depth int) (ImportedConversation, error)
	read = func(id string, depth int) (ImportedConversation, error) {
		var out ImportedConversation
		if depth > 16 {
			return out, fmt.Errorf("%w: conversation %s: subagents nest too deep", ErrInvalid, id)
		}
		c, err := q.GetConversation(ctx, id)
		if err != nil {
			return out, fmt.Errorf("read conversation %s: %w", id, err)
		}
		if c.AgentWorking || c.IsDraft || c.QueuedMessages != "[]" || c.ExternalConversationID != nil {
			return out, fmt.Errorf("%w: conversation %s is working, a draft, has queued messages, or belongs to a channel", ErrConflict, id)
		}
		if out.Messages, err = q.ListMessages(ctx, id); err != nil {
			return out, fmt.Errorf("read messages of %s: %w", id, err)
		}
		for _, m := range out.Messages {
			if m.ForkedFromMessageID != nil {
				return out, fmt.Errorf("%w: conversation %s has forked messages, which import does not support", ErrInvalid, id)
			}
		}
		children, err := q.GetSubagents(ctx, &id)
		if err != nil {
			return out, fmt.Errorf("read subagents of %s: %w", id, err)
		}
		for _, child := range children {
			sub, err := read(child.ConversationID, depth+1)
			if err != nil {
				return out, err
			}
			out.Subagents = append(out.Subagents, sub)
		}
		out.Conversation = c
		return out, nil
	}
	var out []ImportedConversation
	for _, id := range ids {
		c, err := read(id, 0)
		if err != nil {
			return nil, err
		}
		if !c.Conversation.UserInitiated || c.Conversation.ParentConversationID != nil {
			return nil, fmt.Errorf("%w: %s is not a top-level user conversation", ErrInvalid, id)
		}
		out = append(out, c)
	}
	return out, nil
}

// ImportConversation is an imported top-level conversation and the title (slug) it
// gets here; "" keeps its own.
type ImportConversation struct {
	Title        string
	Conversation ImportedConversation
}

// ImportConversations adds imported conversations into project, all or none,
// with their messages and subagents exactly as they were (ids, sequence,
// generations, timestamps, settings), unarchived and idle, but for the
// profile label, which puts them in the project. Like a moved conversation, one that
// hasn't edited its settings takes the project's at its next turn. Subagents
// keep their slugs, made unique if taken. They (and their subagents) take the
// project's directory, since the source host's may not exist here, and in a VM
// project are bound to the project's VM, as new conversations would be; a VM
// project still setting up its base refuses them. Messages drop their
// channel ids, which belong to the source host's chats.
func (db *DB) ImportConversations(ctx context.Context, name string, imports []ImportConversation) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		project, err := getProject(tx.Rx, "?", name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no project %s", ErrInvalid, name)
		}
		if err != nil {
			return err
		}
		if project.VM != nil && project.VM.NeedsBase() {
			return fmt.Errorf("%w: project %q has no base VM yet; import once its setup is done", ErrConflict, project.Name)
		}
		var cwd *string
		if project.Cwd != "" {
			cwd = &project.Cwd
		}
		for _, imp := range imports {
			if err := importConversation(ctx, tx, imp.Conversation, imp.Title, cwd); err != nil {
				return err
			}
			id := imp.Conversation.Conversation.ConversationID
			if err := setConversationProfile(tx, id, project.Name); err != nil {
				return err
			}
			if project.VM != nil {
				if err := insertConversationVM(tx, id, *project.VM, project.VMDir()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func importConversation(ctx context.Context, tx *Tx, c ImportedConversation, slug string, cwd *string) error {
	conv := c.Conversation
	var exists bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM conversations WHERE conversation_id=?)", conv.ConversationID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: conversation %s already exists here", ErrConflict, conv.ConversationID)
	}
	if slug == "" && conv.Slug != nil {
		slug = *conv.Slug
	}
	var slugArg any
	if slug != "" {
		for attempt := range SlugAttempts {
			candidate := SlugCandidate(slug, attempt)
			var taken bool
			if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM conversations WHERE slug=?)", candidate).Scan(&taken); err != nil {
				return err
			}
			if !taken {
				slugArg = candidate
				break
			}
		}
		if slugArg == nil {
			return fmt.Errorf("could not allocate a unique slug for %s", slug)
		}
	}
	at := func(t time.Time) string { return t.UTC().Format(time.DateTime) }
	_, err := tx.Exec(`INSERT INTO conversations(conversation_id,slug,user_initiated,created_at,updated_at,cwd,archived,
		parent_conversation_id,model,conversation_options,current_generation,agent_working,tags,turn_interrupted)
		VALUES(?,?,?,?,?,?,FALSE,?,?,?,?,FALSE,?,?)`,
		conv.ConversationID, slugArg, conv.UserInitiated, at(conv.CreatedAt), at(conv.UpdatedAt), cwd,
		conv.ParentConversationID, conv.Model, conv.ConversationOptions, conv.CurrentGeneration, conv.Tags, conv.TurnInterrupted)
	if err != nil {
		return fmt.Errorf("import conversation %s: %w", conv.ConversationID, err)
	}
	q := generated.New(tx.Conn())
	for _, m := range c.Messages {
		if m.ConversationID != conv.ConversationID {
			return errors.New("import: message of another conversation")
		}
		_, err := q.CreateMessage(ctx, generated.CreateMessageParams{
			MessageID: m.MessageID, ConversationID: m.ConversationID, SequenceID: m.SequenceID, Generation: m.Generation,
			Type: m.Type, LlmData: m.LlmData, UserData: m.UserData, UsageData: m.UsageData, DisplayData: m.DisplayData,
			ExcludedFromContext: m.ExcludedFromContext, LlmApiUrl: m.LlmApiUrl, ModelName: m.ModelName, UserEmail: m.UserEmail,
			OtherUsageData: m.OtherUsageData, CreatedAt: at(m.CreatedAt),
		})
		if err != nil {
			return fmt.Errorf("import message %s: %w", m.MessageID, err)
		}
	}
	for _, sub := range c.Subagents {
		if err := importConversation(ctx, tx, sub, "", cwd); err != nil {
			return err
		}
	}
	return nil
}
