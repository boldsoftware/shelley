package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/featureflags"
)

// Archive research: agents research prior work in the Shelley archive store
// (archive_store.go), reading what people wrote first, with who wrote it.
//
// On the store, the research index (db/schema/081-archive-research.sql)
// keeps one document per piece of text worth reading, by kind: human (typed
// by a person signed in through exe.dev, or texted by the source's owner),
// unattributed (a typed user message with no sign-in: usually an agent or
// script using the local API), agent (final replies and message_user texts:
// they say what was done and decided, so they're searchable, but as a
// separate tier), title and instructions. Tool calls and results,
// intermediate agent text and notices aren't documents: they're the bulk
// of the store and drown out what people said.
//
// The research API, GET under /archive-store/research/v1/ on the store's
// listener (so with the proxy's identity, like the rest of it), is
// read-only and logs every read in archive_research_reads:
//   - search?q=&agent=&author=&project=&conversation=VM/ID&since=&until=&offset=&limit=
//   - conversations?author=&project=&since=&until=&offset=&limit=
//   - conversations/{vm}/{id}?after_seq=&limit=&expand=SEQ
//   - projects
// Replies are claudetool's Archive* types.
//
// On an archiving Shelley, the archive_store tool (claudetool) reads it
// through the archiver's URL.

// FlagArchiveResearch serves the research API on an archive store, and on
// an archiving Shelley gives agents the archive_store tool.
var FlagArchiveResearch = featureflags.Register(featureflags.Flag{
	Name:        "archive-research",
	Description: "Research in the Shelley archive store: an archive store serves its read API for agents, and an archiving Shelley's agents get the archive_store tool.",
	Default:     false,
})

const (
	// archiveResearchHumanChars is how much of a message a thread shows
	// unexpanded.
	archiveResearchHumanChars = 4000
	// archiveResearchMaxChars bounds an expanded message.
	archiveResearchMaxChars = 20000
	archiveResearchMaxLimit = 50
)

func (s *Server) registerArchiveResearch(mux *http.ServeMux) {
	for path, h := range map[string]func(http.ResponseWriter, *http.Request) (any, error){
		"search":                  s.archiveResearchSearch,
		"conversations":           s.archiveResearchConversations,
		"conversations/{vm}/{id}": s.archiveResearchThread,
		"projects":                s.archiveResearchProjects,
	} {
		mux.HandleFunc("GET /archive-store/research/v1/"+path, func(w http.ResponseWriter, r *http.Request) {
			on, err := s.flagOn(r.Context(), FlagArchiveResearch)
			if err != nil {
				s.internalError(w, "Failed to read flags", err)
				return
			}
			if !on {
				http.Error(w, "archive research is off on this store", http.StatusNotFound)
				return
			}
			userID, email, vm := r.Header.Get("X-ExeDev-UserID"), r.Header.Get("X-ExeDev-Email"), r.Header.Get("X-Exedev-Source-Vm")
			s.logger.Info("Archive research read", "email", email, "source_vm", vm, "path", r.URL.Path, "query", r.URL.RawQuery)
			if err := s.db.QueriesTx(r.Context(), func(q *generated.Queries) error {
				return q.InsertArchiveResearchRead(r.Context(), generated.InsertArchiveResearchReadParams{
					UserID: userID, Email: email, SourceVm: vm, Path: r.URL.Path, Query: r.URL.RawQuery,
				})
			}); err != nil {
				s.internalError(w, "Failed to log read", err)
				return
			}
			res, err := h(w, r)
			var bad archiveResearchBadRequest
			switch {
			case errors.As(err, &bad):
				http.Error(w, err.Error(), http.StatusBadRequest)
			case errors.Is(err, sql.ErrNoRows):
				http.NotFound(w, r)
			case err != nil:
				s.internalError(w, "Archive research failed", err)
			default:
				writeJSON(w, http.StatusOK, res)
			}
		})
	}
}

type archiveResearchBadRequest struct{ error }

func badRequestf(format string, args ...any) error {
	return archiveResearchBadRequest{fmt.Errorf(format, args...)}
}

// archiveResearchParams are a request's common parameters.
type archiveResearchParams struct {
	author, project, since, until string
	offset, limit                 int64
}

func parseArchiveResearchParams(r *http.Request, defaultLimit int64) (archiveResearchParams, error) {
	q := r.URL.Query()
	p := archiveResearchParams{author: q.Get("author"), project: q.Get("project"), limit: defaultLimit}
	for _, d := range []struct {
		name string
		to   *string
	}{{"since", &p.since}, {"until", &p.until}} {
		if v := q.Get(d.name); v != "" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return p, badRequestf("%s: want YYYY-MM-DD, not %q", d.name, v)
			}
			*d.to = v
		}
	}
	for _, n := range []struct {
		name string
		to   *int64
	}{{"offset", &p.offset}, {"limit", &p.limit}} {
		if v := q.Get(n.name); v != "" {
			i, err := strconv.ParseInt(v, 10, 64)
			if err != nil || i < 0 {
				return p, badRequestf("%s: want a number, not %q", n.name, v)
			}
			*n.to = i
		}
	}
	if p.limit < 1 {
		p.limit = 1
	}
	if p.limit > archiveResearchMaxLimit {
		p.limit = archiveResearchMaxLimit
	}
	return p, nil
}

// archiveConversationRef names a conversation as the tool does.
func archiveConversationRef(vm, id string) string { return vm + "/" + id }

// archiveStorePageURL is the store page's link to a conversation, or ""
// off exe.dev. People open it through the proxy, at the store VM's port.
func (s *Server) archiveStorePageURL(vm, id string) string {
	env, err := exeenv.Current()
	if err != nil || env.VMName() == "" {
		return ""
	}
	scheme := "https"
	if !env.Production() {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:%d/archive-store/#%s", scheme, env.VMHost(env.VMName()), s.ArchiveStorePort, archiveConversationRef(vm, id))
}

// archiveUserIDs maps sources' emails, lowercased, to their exe.dev user ids.
func archiveUserIDs(sources []generated.ListArchiveResearchSourcesRow) map[string]string {
	ids := map[string]string{}
	for _, src := range sources {
		ids[strings.ToLower(src.Email)] = src.UserID
	}
	return ids
}

func (s *Server) archiveResearchSearch(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := parseArchiveResearchParams(r, 10)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		return nil, badRequestf("q is required")
	}
	var vm, id string
	if ref := r.URL.Query().Get("conversation"); ref != "" {
		var ok bool
		if vm, id, ok = strings.Cut(ref, "/"); !ok {
			return nil, badRequestf("conversation: want SOURCE_VM/CONVERSATION_ID, not %q", ref)
		}
	}
	agent := r.URL.Query().Get("agent") == "true"
	match := db.FTSMatch(query)
	search := func(q *generated.Queries, agent bool, offset, limit int64) ([]generated.SearchArchiveResearchRow, error) {
		return q.SearchArchiveResearch(r.Context(), generated.SearchArchiveResearchParams{
			MarkStart: "**", MarkEnd: "**", FtsMatch: &match, Agent: agent, Author: p.author, Project: p.project,
			SourceVm: vm, ConversationID: id, Since: p.since, Until: p.until, Off: offset, Lim: limit,
		})
	}
	res := claudetool.ArchiveSearchResult{Agent: agent, Offset: int(p.offset), Hits: []claudetool.ArchiveHit{}}
	err = s.db.Queries(r.Context(), func(q *generated.Queries) error {
		rows, err := search(q, agent, p.offset, p.limit)
		if err != nil {
			return err
		}
		other, err := search(q, !agent, 0, 1)
		if err != nil {
			return err
		}
		if len(other) > 0 {
			res.OtherTotal = other[0].Total
		}
		for _, row := range rows {
			res.Total = row.Total
			res.Hits = append(res.Hits, claudetool.ArchiveHit{
				ArchiveAuthor: claudetool.ArchiveAuthor{Kind: row.Kind, Email: row.AuthorEmail, UserID: row.AuthorUserID, Via: row.Via},
				Conversation: claudetool.ArchiveConversation{
					Ref: archiveConversationRef(row.SourceVm, row.ConversationID), URL: s.archiveStorePageURL(row.SourceVm, row.ConversationID),
					Title: row.Slug, Project: row.Project, SourceEmail: row.SourceEmail, Archived: row.Archived, Subagent: row.Subagent,
				},
				SequenceID: row.SequenceID, CreatedAt: row.CreatedAt, Snippet: row.Snippet,
			})
			if row.Kind == claudetool.ArchiveInstructions {
				h := &res.Hits[len(res.Hits)-1]
				h.Conversation = claudetool.ArchiveConversation{Project: row.Project, SourceEmail: row.SourceEmail}
			}
		}
		if len(rows) == 0 && p.offset > 0 {
			// Past the end: still say how many there are.
			first, err := search(q, agent, 0, 1)
			if err != nil || len(first) == 0 {
				return err
			}
			res.Total = first[0].Total
		}
		return nil
	})
	return res, err
}

func (s *Server) archiveResearchConversations(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := parseArchiveResearchParams(r, 20)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueriesRes(r.Context(), func(q *generated.Queries) ([]generated.ListArchiveResearchConversationsRow, error) {
		return q.ListArchiveResearchConversations(r.Context(), generated.ListArchiveResearchConversationsParams{
			Project: p.project, Author: p.author, Since: p.since, Until: p.until, Off: p.offset, Lim: p.limit,
		})
	})
	if err != nil {
		return nil, err
	}
	res := claudetool.ArchiveConversations{Offset: int(p.offset), Conversations: []claudetool.ArchiveConversation{}}
	for _, row := range rows {
		res.Total = row.Total
		c := claudetool.ArchiveConversation{
			Ref: archiveConversationRef(row.SourceVm, row.ConversationID), URL: s.archiveStorePageURL(row.SourceVm, row.ConversationID),
			Title: row.Slug, Project: row.Project, SourceEmail: row.SourceEmail, Archived: row.Archived,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, HumanCount: row.HumanMessages,
			Opening: row.Opening, OpeningKind: row.OpeningKind, OpeningAuthor: row.OpeningAuthor,
		}
		if row.Authors != "" {
			c.HumanAuthors = strings.Split(row.Authors, ",")
		}
		res.Conversations = append(res.Conversations, c)
	}
	return res, nil
}

func (s *Server) archiveResearchThread(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := parseArchiveResearchParams(r, 30)
	if err != nil {
		return nil, err
	}
	var afterSeq, expand int64
	for _, n := range []struct {
		name string
		to   *int64
	}{{"after_seq", &afterSeq}, {"expand", &expand}} {
		if v := r.URL.Query().Get(n.name); v != "" {
			if *n.to, err = strconv.ParseInt(v, 10, 64); err != nil {
				return nil, badRequestf("%s: want a number, not %q", n.name, v)
			}
		}
	}
	vm, id := r.PathValue("vm"), r.PathValue("id")
	var (
		conv    generated.GetArchiveResearchConversationRow
		rows    []generated.ListArchiveResearchThreadRow
		sources []generated.ListArchiveResearchSourcesRow
	)
	if err := s.db.Queries(r.Context(), func(q *generated.Queries) error {
		var err error
		if conv, err = q.GetArchiveResearchConversation(r.Context(), generated.GetArchiveResearchConversationParams{SourceVm: vm, ConversationID: id}); err != nil {
			return err
		}
		if sources, err = q.ListArchiveResearchSources(r.Context()); err != nil {
			return err
		}
		from := afterSeq
		if expand > 0 {
			from = expand - 1
		}
		rows, err = q.ListArchiveResearchThread(r.Context(), generated.ListArchiveResearchThreadParams{SourceVm: vm, ConversationID: id, SequenceID: from})
		return err
	}); err != nil {
		return nil, err
	}
	t := claudetool.ArchiveThread{Entries: []claudetool.ArchiveEntry{}, Conversation: claudetool.ArchiveConversation{
		Ref: archiveConversationRef(vm, id), URL: s.archiveStorePageURL(vm, id), Project: conv.Project,
		SourceEmail: conv.SourceEmail, Archived: conv.Archived, Subagent: conv.ParentConversationID != nil,
		CreatedAt: conv.CreatedAt, UpdatedAt: conv.UpdatedAt,
	}}
	if conv.Slug != nil {
		t.Conversation.Title = *conv.Slug
	}
	t.Entries, t.NextAfterSeq, err = archiveThreadEntries(rows, archiveUserIDs(sources), expand, p.limit)
	return t, err
}

// archiveThreadEntries renders a thread's rows: what people wrote in full
// (to a point), and each run of machine turns between as one entry; at most
// limit people's messages, and where to continue if there are more. With
// expand, it is only that message's text, whatever its kind.
func archiveThreadEntries(rows []generated.ListArchiveResearchThreadRow, userIDs map[string]string, expand, limit int64) ([]claudetool.ArchiveEntry, int64, error) {
	entry := func(row generated.ListArchiveResearchThreadRow, maxChars int) claudetool.ArchiveEntry {
		e := claudetool.ArchiveEntry{
			ArchiveAuthor: claudetool.ArchiveAuthor{Kind: row.Kind, Email: row.AuthorEmail, Via: row.Via},
			SequenceID:    row.SequenceID, CreatedAt: row.CreatedAt, Text: row.Text,
		}
		if row.Kind == claudetool.ArchiveHuman {
			e.UserID = userIDs[strings.ToLower(row.AuthorEmail)]
		}
		if text := []rune(row.Text); len(text) > maxChars {
			e.Text, e.Truncated = string(text[:maxChars]), len(text)-maxChars
		}
		return e
	}
	if expand > 0 {
		if len(rows) == 0 || rows[0].SequenceID != expand || rows[0].Kind == "" {
			return nil, 0, badRequestf("message %d has no text to expand: only people's messages and agents' final replies do", expand)
		}
		return []claudetool.ArchiveEntry{entry(rows[0], archiveResearchMaxChars)}, 0, nil
	}
	entries := []claudetool.ArchiveEntry{}
	var run *claudetool.ArchiveEntry
	flush := func() {
		if run != nil {
			entries = append(entries, *run)
			run = nil
		}
	}
	var people int64
	for i, row := range rows {
		switch {
		case row.Kind == claudetool.ArchiveHuman || row.Kind == claudetool.ArchiveUnattributed:
			if people == limit {
				flush()
				return entries, rows[i-1].SequenceID, nil
			}
			flush()
			entries = append(entries, entry(row, archiveResearchHumanChars))
			people++
			continue
		case row.Type != string(db.MessageTypeAgent) && (row.Type != string(db.MessageTypeUser) || row.ToolResult):
			// Tool results, git and model notes, errors: part of a turn.
			continue
		}
		if run == nil {
			run = &claudetool.ArchiveEntry{ArchiveAuthor: claudetool.ArchiveAuthor{Kind: claudetool.ArchiveMachine}, SequenceID: row.SequenceID, CreatedAt: row.CreatedAt}
		}
		if row.Type == string(db.MessageTypeUser) {
			run.Notices++
			continue
		}
		run.AgentMessages++
		run.ToolCalls += int(row.ToolCalls)
		if row.Kind == claudetool.ArchiveAgent {
			run.ReplySeq, run.ReplyWords = row.SequenceID, len(strings.Fields(row.Text))
		}
	}
	flush()
	return entries, 0, nil
}

func (s *Server) archiveResearchProjects(w http.ResponseWriter, r *http.Request) (any, error) {
	var (
		sources []generated.ListArchiveResearchSourcesRow
		counts  []generated.CountArchiveResearchProjectConversationsRow
	)
	if err := s.db.Queries(r.Context(), func(q *generated.Queries) error {
		var err error
		if sources, err = q.ListArchiveResearchSources(r.Context()); err != nil {
			return err
		}
		counts, err = q.CountArchiveResearchProjectConversations(r.Context())
		return err
	}); err != nil {
		return nil, err
	}
	n := map[[2]string]int64{}
	for _, c := range counts {
		n[[2]string{c.SourceVm, c.Project}] = c.Conversations
	}
	res := claudetool.ArchiveProjects{Projects: []claudetool.ArchiveProject{}}
	for _, src := range sources {
		var projects []struct {
			Name         string `json:"name"`
			Instructions string `json:"instructions"`
		}
		if err := json.Unmarshal([]byte(src.Projects), &projects); err != nil {
			return nil, fmt.Errorf("source %s's projects: %w", src.SourceVm, err)
		}
		for _, p := range projects {
			res.Projects = append(res.Projects, claudetool.ArchiveProject{
				Name: p.Name, SourceEmail: src.Email, SourceVM: src.SourceVm, Instructions: p.Instructions,
				Conversations: n[[2]string{src.SourceVm, p.Name}],
			})
		}
	}
	return res, nil
}

// archiveResearchClient is the archive store as this Shelley's archive_store
// tool reaches it: through the archiver's URL.
type archiveResearchClient struct{ s *Server }

func (c archiveResearchClient) ArchiveResearchEnabled(ctx context.Context) bool {
	on, err := c.s.flagOn(ctx, FlagArchiveResearch)
	if err != nil || !on {
		return false
	}
	base, err := c.s.archiverURL(ctx)
	return err == nil && base != ""
}

func (c archiveResearchClient) ArchiveGet(ctx context.Context, path string, query url.Values, out any) error {
	base, err := c.s.archiverURL(ctx)
	if err != nil {
		return err
	}
	if base == "" {
		return fmt.Errorf("this Shelley has no archive store (the archiver flag is off)")
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	u := base + "/archive-store/research/v1/" + strings.Join(segments, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := archiverHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("archive store: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}
