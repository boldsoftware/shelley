package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/featureflags"
	"shelley.exe.dev/ui"
)

// The Shelley archive store: Shelleys' archivers (archiver.go) push their
// projects and conversations to a Shelley in archive store mode, which
// serves them read-only. Both are off by default.
//
// Archive store mode (FlagArchiveStore) opens a listener of its own serving
// only the store, never the rest of Shelley. It trusts the exe.dev proxy's
// identity headers: pushes need X-ExeDev-UserID, X-ExeDev-Email and the
// attested X-Exedev-Source-Vm (the source's VM-to-VM integration), and are
// stored under that VM; browsing needs X-ExeDev-UserID and X-ExeDev-Email
// (a share of the store VM).
//
// Push protocol (v1), JSON:
//   - PUT /archive-store/v1/projects {projects}: the source's complete set. 204.
//   - POST /archive-store/v1/conversations/{id} {conversation, after_seq,
//     through_seq, reset, messages}: metadata and the messages after
//     after_seq, covering through through_seq (skipped messages included).
//     200 {have_seq}; 409 {have_seq} if the store has less than after_seq,
//     and the archiver resends from there. reset drops the store's messages
//     first. Idempotent.
//   - DELETE /archive-store/v1/conversations/{id}. 204.
//
// Browse API, GET: /archive-store/api/sources, /archive-store/api/conversations
// (?q=, vm=, archived=), /archive-store/api/conversations/{vm}/{id}; the page
// is /archive-store/.

// FlagArchiveStore serves the archive store on ArchiveStorePort.
var FlagArchiveStore = featureflags.Register(featureflags.Flag{
	Name:        "archive-store",
	Description: "Serve a Shelley archive store (archivers' pushes, and a read-only browse page) on port 8000.",
	Default:     false,
})

// archiveStoreBodyLimit bounds a push; the push client sends ~4 MB batches.
const archiveStoreBodyLimit = 64 << 20

// syncArchiveStoreListener opens or closes the store's listener to match its
// flag.
func (s *Server) syncArchiveStoreListener(ctx context.Context) error {
	on, err := s.flagOn(ctx, FlagArchiveStore)
	if err != nil {
		return err
	}
	s.archiveStoreMu.Lock()
	defer s.archiveStoreMu.Unlock()
	switch {
	case on && s.archiveStoreServer == nil:
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.ArchiveStorePort))
		if err != nil {
			return fmt.Errorf("archive store listener: %w", err)
		}
		srv := &http.Server{Handler: s.archiveStoreHandler()}
		s.archiveStoreServer = srv
		s.logger.Info("Archive store serving", "addr", ln.Addr().String())
		go func() {
			if err := srv.Serve(ln); err != http.ErrServerClosed {
				s.logger.Error("Archive store listener failed", "error", err)
			}
		}()
	case !on && s.archiveStoreServer != nil:
		err := s.archiveStoreServer.Close()
		s.archiveStoreServer = nil
		return err
	}
	return nil
}

// archiveStoreHandler serves only the store. Everything on it needs the
// identity the exe.dev proxy stamps; a push also needs the attested
// source VM, which a browser's request never has.
func (s *Server) archiveStoreHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /archive-store/v1/projects", s.handleArchiveStoreProjects)
	mux.HandleFunc("POST /archive-store/v1/conversations/{id}", s.handleArchiveStorePush)
	mux.HandleFunc("DELETE /archive-store/v1/conversations/{id}", s.handleArchiveStoreDelete)
	mux.HandleFunc("GET /archive-store/api/sources", s.handleArchiveStoreSources)
	mux.HandleFunc("GET /archive-store/api/conversations", s.handleArchiveStoreConversations)
	mux.HandleFunc("GET /archive-store/api/conversations/{vm}/{id}", s.handleArchiveStoreConversation)
	mux.Handle("GET /{$}", http.RedirectHandler("/archive-store/", http.StatusFound))
	assets := ui.Assets()
	mux.HandleFunc("GET /archive-store/", func(w http.ResponseWriter, r *http.Request) {
		// The bare page: none of the app's init data.
		index, err := readUIFile(assets, "/index.html")
		if err != nil {
			s.internalError(w, "Failed to read index.html", err)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
	static := s.staticHandler(assets)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// The UI's files, never its pages.
		if path.Ext(r.URL.Path) == "" || r.URL.Path == "/index.html" {
			http.NotFound(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
	return LoggerMiddleware(s.logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-ExeDev-Email") == "" || r.Header.Get("X-ExeDev-UserID") == "" {
			http.Error(w, "archive store: sign in through exe.dev", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

// archiveStoreSource is who pushes, or false (and a 403 written) if r isn't
// a push from a source VM.
func archiveStoreSource(w http.ResponseWriter, r *http.Request) (db.ArchiveStoreSource, bool) {
	m := db.ArchiveStoreSource{
		SourceVM: r.Header.Get("X-Exedev-Source-Vm"),
		UserID:   r.Header.Get("X-ExeDev-UserID"),
		Email:    r.Header.Get("X-ExeDev-Email"),
	}
	if m.SourceVM == "" {
		http.Error(w, "archive store: pushes come from a VM, through a VM-to-VM integration", http.StatusForbidden)
		return m, false
	}
	return m, true
}

func (s *Server) handleArchiveStoreProjects(w http.ResponseWriter, r *http.Request) {
	m, ok := archiveStoreSource(w, r)
	if !ok {
		return
	}
	var req struct {
		Projects []json.RawMessage `json:"projects"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, archiveStoreBodyLimit)
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Projects == nil {
		req.Projects = []json.RawMessage{}
	}
	projects, err := json.Marshal(req.Projects)
	if err != nil {
		s.internalError(w, "Failed to encode projects", err)
		return
	}
	if err := s.db.ReceiveArchiveStoreProjects(r.Context(), m, string(projects)); err != nil {
		s.internalError(w, "Failed to store projects", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleArchiveStorePush(w http.ResponseWriter, r *http.Request) {
	m, ok := archiveStoreSource(w, r)
	if !ok {
		return
	}
	var req db.ArchiveStorePush
	r.Body = http.MaxBytesReader(w, r.Body, archiveStoreBodyLimit)
	if !decodeJSON(w, r, &req) {
		return
	}
	have, err := s.db.ReceiveArchiveStorePush(r.Context(), m, r.PathValue("id"), req)
	switch {
	case errors.Is(err, db.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, db.ErrArchiveStoreBehind):
		writeJSON(w, http.StatusConflict, map[string]int64{"have_seq": have})
	case err != nil:
		s.internalError(w, "Failed to store push", err)
	default:
		writeJSON(w, http.StatusOK, map[string]int64{"have_seq": have})
	}
}

func (s *Server) handleArchiveStoreDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := archiveStoreSource(w, r)
	if !ok {
		return
	}
	if err := s.db.DeleteArchiveStoreConversation(r.Context(), m, r.PathValue("id")); err != nil {
		s.internalError(w, "Failed to delete conversation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleArchiveStoreSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueriesRes(r.Context(), func(q *generated.Queries) ([]generated.ListArchiveStoreSourcesRow, error) {
		return q.ListArchiveStoreSources(r.Context())
	})
	if err != nil {
		s.internalError(w, "Failed to list sources", err)
		return
	}
	type source struct {
		generated.ListArchiveStoreSourcesRow
		Projects json.RawMessage `json:"projects"`
	}
	out := make([]source, len(rows))
	for i, row := range rows {
		out[i] = source{row, json.RawMessage(row.Projects)}
	}
	writeJSON(w, http.StatusOK, out)
}

// archiveStoreListItem is a conversation in the browse list.
type archiveStoreListItem struct {
	generated.ListArchiveStoreConversationsRow
	// Snippet is the best matching message's, when searching.
	Snippet string `json:"snippet,omitempty"`
}

// handleArchiveStoreConversations lists top-level conversations, newest first,
// or with q, every conversation whose title or messages match. vm keeps
// one source's; archived=true includes archived ones.
func (s *Server) handleArchiveStoreConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q, vm, archived := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("vm"), r.URL.Query().Get("archived") == "true"
	rows, err := s.db.QueriesRes(ctx, func(g *generated.Queries) ([]generated.ListArchiveStoreConversationsRow, error) {
		return g.ListArchiveStoreConversations(ctx)
	})
	if err != nil {
		s.internalError(w, "Failed to list conversations", err)
		return
	}
	var hits map[[2]string]string
	if q != "" {
		if hits, err = s.db.SearchArchiveStore(ctx, q); err != nil {
			s.internalError(w, "Failed to search", err)
			return
		}
	}
	out := []archiveStoreListItem{}
	for _, row := range rows {
		if (vm != "" && row.SourceVm != vm) || (row.Archived && !archived) {
			continue
		}
		item := archiveStoreListItem{ListArchiveStoreConversationsRow: row}
		if q == "" {
			if row.ParentConversationID != nil {
				continue
			}
		} else {
			snippet, hit := hits[[2]string{row.SourceVm, row.ConversationID}]
			titleHit := row.Slug != nil && strings.Contains(strings.ToLower(*row.Slug), strings.ToLower(q))
			if !hit && !titleHit {
				continue
			}
			item.Snippet = snippet
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleArchiveStoreConversation is one conversation: its metadata, subagents
// and messages, in the shape of Shelley's conversation API.
func (s *Server) handleArchiveStoreConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vm, id := r.PathValue("vm"), r.PathValue("id")
	type result struct {
		Conversation generated.GetArchiveStoreConversationRow `json:"conversation"`
		Subagents    []generated.ListArchiveStoreSubagentsRow `json:"subagents"`
		Messages     []APIMessage                             `json:"messages"`
	}
	res, err := s.db.QueriesRes(ctx, func(q *generated.Queries) (result, error) {
		var res result
		var err error
		if res.Conversation, err = q.GetArchiveStoreConversation(ctx, generated.GetArchiveStoreConversationParams{SourceVm: vm, ConversationID: id}); err != nil {
			return res, err
		}
		if res.Subagents, err = q.ListArchiveStoreSubagents(ctx, generated.ListArchiveStoreSubagentsParams{SourceVm: vm, ParentConversationID: &id}); err != nil {
			return res, err
		}
		rows, err := q.ListArchiveStoreMessages(ctx, generated.ListArchiveStoreMessagesParams{SourceVm: vm, ConversationID: id})
		if err != nil {
			return res, err
		}
		msgs := make([]generated.Message, len(rows))
		for i, m := range rows {
			msgs[i] = generated.Message{
				MessageID: m.MessageID, ConversationID: id, SequenceID: m.SequenceID, Type: m.Type,
				LlmData: m.LlmData, UserData: m.UserData, UsageData: m.UsageData, DisplayData: m.DisplayData,
				CreatedAt: m.CreatedAt, Generation: m.Generation, ExcludedFromContext: m.ExcludedFromContext,
				ModelName: m.ModelName, UserEmail: m.UserEmail,
			}
		}
		res.Messages = toAPIMessages(msgs)
		return res, nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, "Failed to get conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
