package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// archiveStoreFixture is an archiving Shelley pushing to a store Shelley through a
// stand-in for the exe.dev proxy, which stamps the source's identity.
type archiveStoreFixture struct {
	archiver, store     *Server
	archiverDB, storeDB *db.DB
	url                 string

	mu       sync.Mutex
	requests []string // "METHOD path" the store got
	fail     int      // fail this many pushes with a 502 first
}

func newArchiveStoreFixture(t *testing.T) *archiveStoreFixture {
	f := &archiveStoreFixture{}
	f.archiver, f.archiverDB, _ = newTestServer(t)
	f.archiver.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
	f.store, f.storeDB, _ = newTestServer(t)
	store := f.store.archiveStoreHandler()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		fail := f.fail > 0
		if fail {
			f.fail--
		}
		f.mu.Unlock()
		if fail {
			http.Error(w, "store down", http.StatusBadGateway)
			return
		}
		r.Header.Set("X-ExeDev-UserID", "usr-alice")
		r.Header.Set("X-ExeDev-Email", "alice@example.com")
		r.Header.Set("X-Exedev-Source-Vm", "alice-vm")
		store.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	f.url = proxy.URL
	return f
}

func (f *archiveStoreFixture) push(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
	if err := f.archiver.archiverPush(t.Context(), f.url); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *archiveStoreFixture) pushOn(t *testing.T) {
	t.Helper()
	if err := f.archiverDB.SetFeatureFlagOverride(t.Context(), FlagArchiver.Name, `"`+f.url+`/"`); err != nil {
		t.Fatal(err)
	}
}

func (f *archiveStoreFixture) conversation(t *testing.T, opts db.ConversationOptions) string {
	t.Helper()
	conv, err := f.archiverDB.CreateConversation(t.Context(), nil, true, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	return conv.ConversationID
}

func (f *archiveStoreFixture) message(t *testing.T, id string, typ db.MessageType, content ...llm.Content) {
	t.Helper()
	role := llm.MessageRoleUser
	if typ == db.MessageTypeAgent {
		role = llm.MessageRoleAssistant
	}
	if _, err := f.archiverDB.CreateMessage(t.Context(), db.CreateMessageParams{
		ConversationID: id, Type: typ, LLMData: llm.Message{Role: role, Content: content},
	}); err != nil {
		t.Fatal(err)
	}
}

func text(s string) llm.Content { return llm.Content{Type: llm.ContentTypeText, Text: s} }

// stored is the store's messages of the source's conversation id.
func (f *archiveStoreFixture) stored(t *testing.T, id string) []generated.ArchiveStoreMessage {
	t.Helper()
	msgs, err := f.storeDB.QueriesRes(t.Context(), func(q *generated.Queries) ([]generated.ArchiveStoreMessage, error) {
		return q.ListArchiveStoreMessages(t.Context(), generated.ListArchiveStoreMessagesParams{SourceVm: "alice-vm", ConversationID: id})
	})
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func (f *archiveStoreFixture) browse(t *testing.T, path string, want int) []byte {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-ExeDev-UserID", "usr-bob")
	r.Header.Set("X-ExeDev-Email", "bob@example.com")
	w := httptest.NewRecorder()
	f.store.archiveStoreHandler().ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

const archiverSecret = "hunter2-archive-store-secret"

// What is pushed: conversations created while push is on and their
// subagents, less temporary, helper, channel and draft ones; their messages
// less system prompts, image bytes and secrets. Pushing again sends nothing
// new; new messages go incrementally.
func TestArchiver(t *testing.T) {
	t.Parallel()
	f := newArchiveStoreFixture(t)
	ctx := t.Context()
	before := f.conversation(t, db.ConversationOptions{})
	f.message(t, before, db.MessageTypeUser, text("from before push"))
	f.pushOn(t)
	if err := f.archiver.secrets.create(ctx, newSecret{ID: "token-k3f9", Name: "Token", Kind: "token", Value: archiverSecret}); err != nil {
		t.Fatal(err)
	}

	shared := f.conversation(t, db.ConversationOptions{})
	f.message(t, shared, db.MessageTypeSystem, text("system prompt with AGENTS.md"))
	f.message(t, shared, db.MessageTypeUser, text("deploy the zebra service"),
		llm.Content{Type: llm.ContentTypeText, MediaType: "image/png", Data: "iVBORimagebytes"})
	f.message(t, shared, db.MessageTypeAgent, text("the token is "+archiverSecret),
		llm.Content{Type: llm.ContentTypeThinking, Thinking: "hmm", Signature: "opaque-signature"})
	sub, err := f.archiverDB.CreateSubagentConversation(ctx, "helper", shared, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.message(t, sub.ConversationID, db.MessageTypeUser, text("subagent task"))

	temporary := f.conversation(t, db.ConversationOptions{Temporary: true})
	f.message(t, temporary, db.MessageTypeUser, text("private"))
	tempSub, err := f.archiverDB.CreateSubagentConversation(ctx, "temp-helper", temporary, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.message(t, tempSub.ConversationID, db.MessageTypeUser, text("private subagent"))
	search := f.conversation(t, db.ConversationOptions{Kind: "search"})
	f.message(t, search, db.MessageTypeUser, text("search helper"))
	// A draft made temporary when its first message is sent.
	draft, err := f.archiverDB.CreateDraftConversation(ctx, nil, nil, db.ConversationOptions{}, "draft text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.archiverDB.PromoteDraft(ctx, draft.ConversationID, nil, nil, &db.ConversationOptions{Temporary: true}); err != nil {
		t.Fatal(err)
	}
	f.message(t, draft.ConversationID, db.MessageTypeUser, text("private draft"))

	got := f.push(t)
	want := []string{
		"PUT /archive-store/v1/projects",
		"POST /archive-store/v1/conversations/" + shared,
		"POST /archive-store/v1/conversations/" + sub.ConversationID,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	msgs := f.stored(t, shared)
	if len(msgs) != 2 || msgs[0].Type != "user" || msgs[1].Type != "agent" {
		t.Fatalf("stored %+v", msgs)
	}
	all := *msgs[0].LlmData + *msgs[1].LlmData
	for _, leak := range []string{archiverSecret, "iVBORimagebytes", "opaque-signature", "AGENTS.md"} {
		if strings.Contains(all, leak) {
			t.Errorf("store got %q: %s", leak, all)
		}
	}
	for _, kept := range []string{"{{secret:token-k3f9}}", "zebra", `"MediaType":"image/png"`} {
		if !strings.Contains(all, kept) {
			t.Errorf("store lacks %q: %s", kept, all)
		}
	}

	// Nothing changed: nothing is sent.
	if got := f.push(t); len(got) != 0 {
		t.Fatalf("second push sent %v", got)
	}
	// A new message goes alone.
	f.message(t, shared, db.MessageTypeUser, text("and the giraffe one"))
	if got := f.push(t); len(got) != 1 {
		t.Fatalf("incremental push sent %v", got)
	}
	if msgs := f.stored(t, shared); len(msgs) != 3 {
		t.Fatalf("stored %d messages", len(msgs))
	}

	// Browsing: the list, search with attribution, a conversation.
	var list []archiveStoreListItem
	json.Unmarshal(f.browse(t, "/archive-store/api/conversations", 200), &list)
	if len(list) != 1 || list[0].ConversationID != shared || list[0].Email != "alice@example.com" {
		t.Fatalf("list %+v", list)
	}
	json.Unmarshal(f.browse(t, "/archive-store/api/conversations?q=giraffe", 200), &list)
	if len(list) != 1 || !strings.Contains(list[0].Snippet, db.SnippetMarkStart+"giraffe") {
		t.Fatalf("search %+v", list)
	}
	json.Unmarshal(f.browse(t, "/archive-store/api/conversations?q=private", 200), &list)
	if len(list) != 0 {
		t.Fatalf("search found temporary conversations: %+v", list)
	}
	var detail struct {
		Subagents []generated.ListArchiveStoreSubagentsRow `json:"subagents"`
		Messages  []APIMessage                             `json:"messages"`
	}
	json.Unmarshal(f.browse(t, "/archive-store/api/conversations/alice-vm/"+shared, 200), &detail)
	if len(detail.Messages) != 3 || len(detail.Subagents) != 1 || detail.Subagents[0].ConversationID != sub.ConversationID {
		t.Fatalf("detail %+v", detail)
	}
	f.browse(t, "/archive-store/api/conversations/alice-vm/"+temporary, 404)
}

// A failed push resumes where it stopped; a store that lost data gets it
// again; deletions and metadata changes propagate.
func TestArchiverResume(t *testing.T) {
	t.Parallel()
	f := newArchiveStoreFixture(t)
	ctx := t.Context()
	f.pushOn(t)
	id := f.conversation(t, db.ConversationOptions{})
	f.message(t, id, db.MessageTypeUser, text("one"))
	f.push(t)

	f.message(t, id, db.MessageTypeAgent, text("two"))
	f.fail = 1
	if err := f.archiver.archiverPush(ctx, f.url); err == nil {
		t.Fatal("push to a failing store succeeded")
	}
	f.push(t)
	if msgs := f.stored(t, id); len(msgs) != 2 {
		t.Fatalf("stored %d messages after resuming", len(msgs))
	}

	// The store is restored from an old backup: it has only the first
	// message. It answers the next push with 409, and gets the rest.
	if err := f.storeDB.QueriesTx(ctx, func(q *generated.Queries) error {
		if err := q.DeleteArchiveStoreMessages(ctx, generated.DeleteArchiveStoreMessagesParams{SourceVm: "alice-vm", ConversationID: id}); err != nil {
			return err
		}
		return q.DeleteArchiveStoreConversation(ctx, generated.DeleteArchiveStoreConversationParams{SourceVm: "alice-vm", ConversationID: id})
	}); err != nil {
		t.Fatal(err)
	}
	f.message(t, id, db.MessageTypeUser, text("three"))
	if got := f.push(t); len(got) != 2 {
		t.Fatalf("push to a restored store sent %v", got)
	}
	if msgs := f.stored(t, id); len(msgs) != 3 {
		t.Fatalf("stored %d messages after the store lost them", len(msgs))
	}

	// Archiving changes only metadata.
	if _, err := f.archiverDB.ArchiveConversation(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.push(t)
	var list []archiveStoreListItem
	json.Unmarshal(f.browse(t, "/archive-store/api/conversations", 200), &list)
	if len(list) != 0 {
		t.Fatalf("archived conversation listed: %+v", list)
	}
	json.Unmarshal(f.browse(t, "/archive-store/api/conversations?archived=true", 200), &list)
	if len(list) != 1 || !list[0].Archived {
		t.Fatalf("archived list %+v", list)
	}

	if err := f.archiverDB.DeleteConversation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := f.push(t); len(got) != 1 || got[0] != "DELETE /archive-store/v1/conversations/"+id {
		t.Fatalf("delete sent %v", got)
	}
	if msgs := f.stored(t, id); len(msgs) != 0 {
		t.Fatalf("store kept %d messages of a deleted conversation", len(msgs))
	}
	if got := f.push(t); len(got) != 0 {
		t.Fatalf("push after delete sent %v", got)
	}
}

// Both halves are off until exe.dev sets their flags: no store listener,
// nothing recorded, so nothing is ever pushed, even after push is turned on.
func TestArchiveStoreOffByDefault(t *testing.T) {
	t.Parallel()
	f := newArchiveStoreFixture(t)
	ctx := t.Context()
	if on, err := f.archiver.flagOn(ctx, FlagArchiveStore); err != nil || on {
		t.Fatalf("archive-store on by default: %v %v", on, err)
	}
	if url, err := f.archiver.archiverURL(ctx); err != nil || url != "" {
		t.Fatalf("archiver on by default: %q %v", url, err)
	}
	if err := f.archiver.syncArchiveStoreListener(ctx); err != nil || f.archiver.archiveStoreServer != nil {
		t.Fatalf("store listener with the flag off: %v", err)
	}
	f.message(t, f.conversation(t, db.ConversationOptions{}), db.MessageTypeUser, text("before push"))
	f.pushOn(t)
	if got := f.push(t); len(got) != 1 || got[0] != "PUT /archive-store/v1/projects" {
		t.Fatalf("pushed %v", got)
	}
}

func TestArchiverNextBackoff(t *testing.T) {
	t.Parallel()
	var got []time.Duration
	for b := time.Duration(0); len(got) < 8; {
		b = archiverNextBackoff(b)
		got = append(got, b)
	}
	want := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("backoffs %v, want %v", got, want)
	}
}
