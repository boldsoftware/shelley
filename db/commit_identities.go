package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"shelley.exe.dev/db/generated"
)

// CommitIdentity is the name and email git commits are made as. The zero
// value is none: git's own configuration applies.
type CommitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (c CommitIdentity) IsZero() bool { return c == CommitIdentity{} }

// String is c as git writes an author: "Name <email>".
func (c CommitIdentity) String() string { return c.Name + " <" + c.Email + ">" }

// Environ is the GIT_AUTHOR_* and GIT_COMMITTER_* assignments that make git
// commit as c whatever its configuration says, or none for the zero value.
func (c CommitIdentity) Environ() []string {
	if c.IsZero() {
		return nil
	}
	return []string{
		"GIT_AUTHOR_NAME=" + c.Name, "GIT_AUTHOR_EMAIL=" + c.Email,
		"GIT_COMMITTER_NAME=" + c.Name, "GIT_COMMITTER_EMAIL=" + c.Email,
	}
}

// defaultCommitIdentityKey is the setting holding the default commit
// identity, as JSON; every conversation not in a project with its own uses it.
const defaultCommitIdentityKey = "commit_identity"

// ValidateCommitIdentity trims c, which needs both a name and an email, or
// neither.
func ValidateCommitIdentity(c *CommitIdentity) error {
	c.Name, c.Email = strings.TrimSpace(c.Name), strings.TrimSpace(c.Email)
	bad := func(s string) bool {
		return len(s) > 254 || strings.ContainsAny(s, "<>") || strings.ContainsFunc(s, unicode.IsControl)
	}
	switch {
	case c.IsZero():
		return nil
	case c.Name == "" || c.Email == "":
		return fmt.Errorf("%w: a commit identity needs both a name and an email", ErrInvalid)
	case bad(c.Name):
		return fmt.Errorf("%w: a commit name can't contain < or > or control characters", ErrInvalid)
	case bad(c.Email) || strings.ContainsFunc(c.Email, unicode.IsSpace) || !strings.Contains(c.Email[1:], "@"):
		return fmt.Errorf("%w: %q is not an email address", ErrInvalid, c.Email)
	}
	return nil
}

func (db *DB) DefaultCommitIdentity(ctx context.Context) (CommitIdentity, error) {
	var c CommitIdentity
	value, err := db.GetSetting(ctx, defaultCommitIdentityKey)
	if err != nil || value == "" {
		return c, err
	}
	return c, json.Unmarshal([]byte(value), &c)
}

// SetDefaultCommitIdentity sets the default commit identity; the zero value
// removes it.
func (db *DB) SetDefaultCommitIdentity(ctx context.Context, c CommitIdentity) error {
	if err := ValidateCommitIdentity(&c); err != nil {
		return err
	}
	if c.IsZero() {
		return db.DeleteSetting(ctx, defaultCommitIdentityKey)
	}
	value, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return db.SetSetting(ctx, defaultCommitIdentityKey, string(value))
}

// ConversationCommitIdentity is the identity conversationID commits as: its
// project's (a subagent's top-level conversation's), else the default, else
// none.
func (db *DB) ConversationCommitIdentity(ctx context.Context, conversationID string) (CommitIdentity, error) {
	var row generated.GetConversationCommitIdentityRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) (err error) {
		row, err = generated.New(rx.Conn()).GetConversationCommitIdentity(ctx, conversationID)
		return err
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return db.DefaultCommitIdentity(ctx)
	case err != nil:
		return CommitIdentity{}, err
	}
	return CommitIdentity{Name: row.Name, Email: row.Email}, nil
}

// setProjectCommitIdentity sets the project's own commit identity: nil
// keeps it, the zero value removes it (the project uses the default).
func setProjectCommitIdentity(ctx context.Context, q *generated.Queries, project string, c *CommitIdentity) error {
	switch {
	case c == nil:
		return nil
	case c.IsZero():
		return q.DeleteProjectCommitIdentity(ctx, project)
	}
	return q.UpsertProjectCommitIdentity(ctx, generated.UpsertProjectCommitIdentityParams{Profile: project, Name: c.Name, Email: c.Email})
}

func attachProjectCommitIdentities(ctx context.Context, q *generated.Queries, projects []Project) error {
	rows, err := q.ListProjectCommitIdentities(ctx)
	if err != nil {
		return err
	}
	identities := map[string]CommitIdentity{}
	for _, row := range rows {
		identities[row.Profile] = CommitIdentity{Name: row.Name, Email: row.Email}
	}
	for i := range projects {
		c := identities[projects[i].Name]
		projects[i].CommitIdentity = &c
	}
	return nil
}
