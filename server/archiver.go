package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/featureflags"
	"shelley.exe.dev/llm"
)

// The archiver: this Shelley pushes its projects and the conversations
// recorded in archiver_pushes to a Shelley archive store (see
// archive_store.go).

// FlagArchiver is the archive store's base URL, through this VM's
// VM-to-VM integration; null or "" is off.
var FlagArchiver = featureflags.Register(featureflags.Flag{
	Name:        "archiver",
	Description: `Push conversations started from now on, and projects, to the Shelley archive store at this URL (e.g. "https://archive-store.int.exe.xyz/"); "" is off.`,
	Default:     "",
})

const (
	archiverBatchBytes   = 4 << 20
	archiverPollInterval = 10 * time.Second
	archiverMinBackoff   = 15 * time.Second
	archiverMaxBackoff   = 10 * time.Minute
)

// archiverURL is the store's base URL, or "" while push is off.
func (s *Server) archiverURL(ctx context.Context) (string, error) {
	overrides, err := s.db.GetAllFeatureFlagOverrides(ctx)
	if err != nil {
		return "", err
	}
	raw, ok := overrides[FlagArchiver.Name]
	if !ok {
		return "", nil
	}
	var url *string
	if err := json.Unmarshal([]byte(raw), &url); err != nil {
		return "", fmt.Errorf("feature flag %s: %w", FlagArchiver.Name, err)
	}
	if url == nil {
		return "", nil
	}
	return strings.TrimSuffix(*url, "/"), nil
}

func (s *Server) wakeArchiver() {
	select {
	case s.archiverWake <- struct{}{}:
	default:
	}
}

// archiverLoop pushes while push is on: every poll interval, at once
// when the flag changes, and after a failure with backoff.
func (s *Server) archiverLoop() {
	ctx := context.Background()
	var backoff time.Duration
	for {
		url, err := s.archiverURL(ctx)
		if err == nil && url != "" {
			err = s.archiverPush(ctx, url)
		}
		var wait <-chan time.Time
		switch {
		case err != nil:
			backoff = archiverNextBackoff(backoff)
			s.logger.Warn("Archiver push failed", "error", err, "retry_in", backoff)
			wait = time.After(backoff)
		case url != "":
			backoff = 0
			wait = time.After(archiverPollInterval)
		}
		select {
		case <-s.shutdownCh:
			return
		case <-s.archiverWake:
		case <-wait:
		}
	}
}

// archiverPush pushes everything that changed since the store last acked
// it. Each step's progress is recorded as soon as the store acks it, so an
// error or a restart resumes where it stopped.
func (s *Server) archiverPush(ctx context.Context, base string) error {
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return err
	}
	if err := s.archiveProjects(ctx, base, projects); err != nil {
		return err
	}
	deleted, err := s.db.QueriesRes(ctx, func(q *generated.Queries) ([]generated.ListArchiverDeletedRow, error) {
		return q.ListArchiverDeleted(ctx)
	})
	if err != nil {
		return err
	}
	for _, d := range deleted {
		if d.Pushed {
			if _, err := s.archiverRequest(ctx, http.MethodDelete, base+"/archive-store/v1/conversations/"+d.ConversationID, nil, nil); err != nil {
				return err
			}
		}
		if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error { return q.DeleteArchiverPush(ctx, d.ConversationID) }); err != nil {
			return err
		}
	}
	rows, err := s.db.QueriesRes(ctx, func(q *generated.Queries) ([]generated.ListArchiverPushesRow, error) {
		return q.ListArchiverPushes(ctx)
	})
	if err != nil {
		return err
	}
	projectNames := map[string]bool{}
	for _, p := range projects {
		projectNames[p.Name] = true
	}
	for _, row := range rows {
		if !archiverIncludes(row) {
			continue
		}
		if err := s.archiveConversation(ctx, base, row, projectNames); err != nil {
			return fmt.Errorf("conversation %s: %w", row.ConversationID, err)
		}
	}
	return nil
}

// archiverIncludes reports whether a recorded conversation goes to the store
// (see archiveConversation for what of it).
func archiverIncludes(row generated.ListArchiverPushesRow) bool {
	if row.IsDraft || row.RootChannel {
		return false
	}
	for _, raw := range []string{row.ConversationOptions, row.RootOptions} {
		opts := db.ParseConversationOptions(raw)
		if opts.Temporary || opts.Kind != "" {
			return false
		}
	}
	return true
}

// archiveStoreProject is what the store gets of a project.
type archiveStoreProject struct {
	Name         string               `json:"name"`
	Instructions string               `json:"instructions"`
	Cwd          string               `json:"cwd"`
	Actions      []archiveStoreAction `json:"actions"`
	Repos        []db.ProjectRepo     `json:"repos"`
	PageURL      string               `json:"page_url"`
	Settings     Settings             `json:"settings"`
}

type archiveStoreAction struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

func (s *Server) archiveProjects(ctx context.Context, base string, projects []db.Project) error {
	out := make([]archiveStoreProject, len(projects))
	for i, p := range projects {
		profile, err := s.getProfile(ctx, p.Name)
		if err != nil {
			return err
		}
		actions := make([]archiveStoreAction, len(p.Actions))
		for j, a := range p.Actions {
			actions[j] = archiveStoreAction{Label: a.Label, Command: a.Command}
		}
		out[i] = archiveStoreProject{
			Name: p.Name, Instructions: p.Instructions, Cwd: p.Cwd, Actions: actions,
			Repos: p.Repos, Settings: profile.Settings,
		}
		if p.PageURL != nil {
			out[i].PageURL = *p.PageURL
		}
	}
	body, err := json.Marshal(map[string]any{"projects": out})
	if err != nil {
		return err
	}
	redacted, err := s.secrets.Redact(ctx, string(body))
	if err != nil {
		return err
	}
	pushed, err := s.db.QueriesRes(ctx, func(q *generated.Queries) (string, error) {
		return q.GetArchiverProjects(ctx)
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if pushed == redacted {
		return nil
	}
	if _, err := s.archiverRequest(ctx, http.MethodPut, base+"/archive-store/v1/projects", []byte(redacted), nil); err != nil {
		return err
	}
	return s.db.QueriesTx(ctx, func(q *generated.Queries) error { return q.SetArchiverProjects(ctx, redacted) })
}

// archiveConversation pushes a conversation's metadata, if it
// changed, and its messages after the store's cursor, in batches.
func (s *Server) archiveConversation(ctx context.Context, base string, row generated.ListArchiverPushesRow, projects map[string]bool) error {
	meta := db.ArchiveStoreConversation{
		ConversationID: row.ConversationID, ParentConversationID: row.ParentConversationID, Slug: row.Slug,
		Model: row.Model, Cwd: row.Cwd, Archived: row.Archived, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if p := db.ParseConversationOptions(row.ConversationOptions).Profile; projects[p] {
		meta.Project = p
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	redactedMeta, err := s.secrets.Redact(ctx, string(metaJSON))
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(redactedMeta), &meta); err != nil {
		return err
	}
	seq, reset := row.PushedSeq, false
	if row.MaxSeq < seq {
		// Its messages went backwards: the store starts over.
		seq, reset = 0, true
	}
	if !reset && seq == row.MaxSeq && redactedMeta == row.PushedMeta {
		return nil
	}
	behind := false
	for {
		push := db.ArchiveStorePush{Conversation: meta, AfterSeq: seq, ThroughSeq: seq, Reset: reset, Messages: []db.ArchiveStoreMessage{}}
		msgs, err := s.db.QueriesRes(ctx, func(q *generated.Queries) ([]generated.Message, error) {
			return q.ListArchiverMessagesAfter(ctx, generated.ListArchiverMessagesAfterParams{ConversationID: row.ConversationID, SequenceID: seq, Limit: 500})
		})
		if err != nil {
			return err
		}
		size := 0
		for _, m := range msgs {
			push.ThroughSeq = m.SequenceID
			if m.Type == string(db.MessageTypeSystem) {
				continue
			}
			tm, err := s.archiveStoreMessage(ctx, m)
			if err != nil {
				return err
			}
			push.Messages = append(push.Messages, tm)
			if size += archiverMessageSize(tm); size >= archiverBatchBytes {
				break
			}
		}
		body, err := json.Marshal(push)
		if err != nil {
			return err
		}
		var ack struct {
			HaveSeq int64 `json:"have_seq"`
		}
		status, err := s.archiverRequest(ctx, http.MethodPost, base+"/archive-store/v1/conversations/"+row.ConversationID, body, &ack)
		if err != nil {
			return err
		}
		if status == http.StatusConflict {
			// The store lost some of it: resend from what it has.
			if behind || ack.HaveSeq >= seq {
				return fmt.Errorf("store has %d after asking to resend from it", ack.HaveSeq)
			}
			behind, seq = true, ack.HaveSeq
			continue
		}
		if ack.HaveSeq != push.ThroughSeq {
			return fmt.Errorf("store acked %d, not %d", ack.HaveSeq, push.ThroughSeq)
		}
		if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
			return q.SetArchiverPushed(ctx, generated.SetArchiverPushedParams{PushedSeq: ack.HaveSeq, PushedMeta: redactedMeta, ConversationID: row.ConversationID})
		}); err != nil {
			return err
		}
		seq, reset, behind = ack.HaveSeq, false, false
		if len(msgs) == 0 || seq >= row.MaxSeq {
			return nil
		}
	}
}

func archiverMessageSize(m db.ArchiveStoreMessage) int {
	n := 512
	for _, p := range []*string{m.LlmData, m.UserData, m.UsageData, m.DisplayData} {
		if p != nil {
			n += len(*p)
		}
	}
	return n
}

// archiveStoreMessage is m as the store gets it: no image bytes or opaque
// provider state, and stored secrets' values replaced by their placeholders.
func (s *Server) archiveStoreMessage(ctx context.Context, m generated.Message) (db.ArchiveStoreMessage, error) {
	llmData, err := archiverLLMData(m.LlmData)
	if err != nil {
		return db.ArchiveStoreMessage{}, err
	}
	out := db.ArchiveStoreMessage{
		MessageID: m.MessageID, SequenceID: m.SequenceID, Type: m.Type, LlmData: llmData, UserData: m.UserData,
		UsageData: m.UsageData, DisplayData: m.DisplayData, CreatedAt: m.CreatedAt, Generation: m.Generation,
		ExcludedFromContext: m.ExcludedFromContext, ModelName: m.ModelName, UserEmail: m.UserEmail,
	}
	for _, p := range []**string{&out.LlmData, &out.UserData, &out.DisplayData} {
		if *p == nil {
			continue
		}
		redacted, err := s.secrets.Redact(ctx, **p)
		if err != nil {
			return db.ArchiveStoreMessage{}, err
		}
		*p = &redacted
	}
	return out, nil
}

func archiverLLMData(data *string) (*string, error) {
	if data == nil {
		return nil, nil
	}
	var msg llm.Message
	if err := json.Unmarshal([]byte(*data), &msg); err != nil {
		return nil, err
	}
	var strip func([]llm.Content)
	strip = func(cs []llm.Content) {
		for i := range cs {
			c := &cs[i]
			c.Data, c.Signature, c.OpenAIResponsesReasoning, c.GeminiServerTool = "", "", nil, nil
			strip(c.ToolResult)
		}
	}
	strip(msg.Content)
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	str := string(out)
	return &str, nil
}

var archiverHTTP = &http.Client{Timeout: 2 * time.Minute}

// archiverRequest sends a request to the store, decoding a JSON reply into
// ack. A 409 is returned as a status for the caller; other failures are
// errors.
func (s *Server) archiverRequest(ctx context.Context, method, url string, body []byte, ack any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := archiverHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusConflict:
		return resp.StatusCode, json.Unmarshal(reply, ack)
	case http.StatusNoContent:
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(reply)))
}

// archiverNextBackoff doubles backoff, from archiverMinBackoff up to
// archiverMaxBackoff.
func archiverNextBackoff(backoff time.Duration) time.Duration {
	switch {
	case backoff < archiverMinBackoff:
		return archiverMinBackoff
	case 2*backoff > archiverMaxBackoff:
		return archiverMaxBackoff
	}
	return 2 * backoff
}
