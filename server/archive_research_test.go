package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// archiveResearchFixture is an archive store with research on, and two
// members' Shelleys pushing to it: alice's and bob's.
type archiveResearchFixture struct {
	store        *Server
	storeDB      *db.DB
	alice, bob   *archiveMember
	storeHandler http.Handler
}

type archiveMember struct {
	s   *Server
	db  *db.DB
	url string // the store, through a proxy stamping this member's identity
}

func newArchiveResearchFixture(t *testing.T) *archiveResearchFixture {
	f := &archiveResearchFixture{}
	f.store, f.storeDB, _ = newTestServer(t)
	if err := f.storeDB.SetFeatureFlagOverride(t.Context(), FlagArchiveResearch.Name, "true"); err != nil {
		t.Fatal(err)
	}
	f.storeHandler = f.store.archiveStoreHandler()
	member := func(user, email, vm string) *archiveMember {
		m := &archiveMember{}
		m.s, m.db, _ = newTestServer(t)
		m.s.secrets.keyPath = filepath.Join(t.TempDir(), "secrets.key")
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("X-ExeDev-UserID", user)
			r.Header.Set("X-ExeDev-Email", email)
			r.Header.Set("X-Exedev-Source-Vm", vm)
			f.storeHandler.ServeHTTP(w, r)
		}))
		t.Cleanup(proxy.Close)
		m.url = proxy.URL
		for flag, v := range map[string]string{FlagArchiver.Name: `"` + m.url + `"`, FlagArchiveResearch.Name: "true"} {
			if err := m.db.SetFeatureFlagOverride(t.Context(), flag, v); err != nil {
				t.Fatal(err)
			}
		}
		return m
	}
	f.alice = member("usr-alice", "alice@example.com", "alice-vm")
	f.bob = member("usr-bob", "bob@example.com", "bob-vm")
	return f
}

func (m *archiveMember) push(t *testing.T) {
	t.Helper()
	if err := m.s.archiverPush(t.Context(), m.url); err != nil {
		t.Fatal(err)
	}
}

func (m *archiveMember) conversation(t *testing.T, opts db.ConversationOptions) string {
	t.Helper()
	conv, err := m.db.CreateConversation(t.Context(), nil, true, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	return conv.ConversationID
}

func (m *archiveMember) add(t *testing.T, p db.CreateMessageParams) {
	t.Helper()
	if _, err := m.db.CreateMessage(t.Context(), p); err != nil {
		t.Fatal(err)
	}
}

// typed is a user message a person typed, signed in as email ("" for none).
func typed(id, email, s string) db.CreateMessageParams {
	return db.CreateMessageParams{
		ConversationID: id, Type: db.MessageTypeUser, UserEmail: email,
		LLMData: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{text(s)}},
	}
}

// agentTurn is an agent message: tool calls, or the turn's final reply.
func agentTurn(id, s string, tools int) db.CreateMessageParams {
	msg := llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{text(s)}, EndOfTurn: tools == 0}
	for range tools {
		msg.Content = append(msg.Content, llm.Content{Type: llm.ContentTypeToolUse, ID: "t", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"grep -r backoff"}`)})
	}
	return db.CreateMessageParams{ConversationID: id, Type: db.MessageTypeAgent, LLMData: msg}
}

func toolResult(id, s string) db.CreateMessageParams {
	return db.CreateMessageParams{ConversationID: id, Type: db.MessageTypeUser, LLMData: llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "t", ToolResult: []llm.Content{text(s)}}},
	}}
}

// seed gives alice a project and a conversation in it with three people's
// messages, an unattributed brief, a machine notice and agent turns, plus a
// temporary conversation; and bob a conversation of his own. It returns
// alice's conversation's ref.
func (f *archiveResearchFixture) seed(t *testing.T) string {
	t.Helper()
	if _, err := f.alice.db.CreateProject(t.Context(), db.Project{Name: "payments", Instructions: "Webhook delivery service. Never retry without backoff."}, ""); err != nil {
		t.Fatal(err)
	}
	id := f.alice.conversation(t, db.ConversationOptions{Profile: "payments"})
	if err := f.alice.db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.UpdateConversationSlug(t.Context(), generated.UpdateConversationSlugParams{ConversationID: id, Slug: ptr("webhook-retries")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	texted := typed(id, "", "Also cap webhook retries at ten tries.")
	texted.ExternalMessageID = "0c69f6cd-3da0-4b8e-9d2a-7f1e5c3b2a10"
	notice := typed(id, "", "Background job finished: webhook tests pass.")
	notice.UserData = map[string]any{"background_job_id": "j1", "Text": "Background job finished: webhook tests pass."}
	for _, p := range []db.CreateMessageParams{
		typed(id, "", "Brief from the Assistant: look into webhook retries."),
		typed(id, "alice@example.com", "Webhook retries hammer the merchant. Use exponential backoff."),
		agentTurn(id, "Looking.", 2),
		toolResult(id, "retry.go: backoff := 0"),
		agentTurn(id, "Looking more.", 1),
		toolResult(id, "ok"),
		agentTurn(id, "Done: webhook retries now use exponential backoff with jitter.", 0),
		typed(id, "carol@example.com", "I'd add jitter to the webhook backoff too."),
		texted,
		notice,
		agentTurn(id, "Capped at ten.", 0),
	} {
		f.alice.add(t, p)
	}
	temp := f.alice.conversation(t, db.ConversationOptions{Temporary: true})
	f.alice.add(t, typed(temp, "alice@example.com", "Secret webhook plans, temporary."))

	bobs := f.bob.conversation(t, db.ConversationOptions{})
	f.bob.add(t, typed(bobs, "bob@example.com", "Why do webhook retries back off so slowly?"))
	f.bob.add(t, agentTurn(bobs, "Because of the backoff.", 0))
	f.alice.push(t)
	f.bob.push(t)
	return "alice-vm/" + id
}

func ptr[T any](v T) *T { return &v }

// read GETs a research API path as bob, through the store's handler, and
// decodes its reply.
func (f *archiveResearchFixture) read(t *testing.T, path string, want int, out any) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/archive-store/research/v1/"+path, nil)
	r.Header.Set("X-ExeDev-UserID", "usr-bob")
	r.Header.Set("X-ExeDev-Email", "bob@example.com")
	r.Header.Set("X-Exedev-Source-Vm", "bob-vm")
	w := httptest.NewRecorder()
	f.storeHandler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Body.String()
}

// The index: people's messages with who wrote them (signed in, or texted
// to the source's Assistant), unattributed briefs, agents' final replies,
// titles and instructions; no tool traffic, notices or temporary
// conversations.
func TestArchiveResearchIndex(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	f.seed(t)
	rows, err := f.storeDB.QueriesRes(t.Context(), func(q *generated.Queries) ([]string, error) {
		var out []string
		res, err := q.SearchArchiveResearch(t.Context(), generated.SearchArchiveResearchParams{FtsMatch: ptr(`"webhook"*`), Lim: 50})
		if err != nil {
			return nil, err
		}
		agent, err := q.SearchArchiveResearch(t.Context(), generated.SearchArchiveResearchParams{FtsMatch: ptr(`"webhook"*`), Agent: true, Lim: 50})
		for _, r := range append(res, agent...) {
			out = append(out, strings.Join([]string{r.SourceVm, r.Kind, r.AuthorEmail, r.AuthorUserID, r.Via, r.Project}, "|"))
		}
		return out, err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(rows)
	want := []string{
		"alice-vm|agent||||payments",
		"alice-vm|human|alice@example.com|usr-alice|text|payments",
		"alice-vm|human|alice@example.com|usr-alice||payments",
		"alice-vm|human|carol@example.com|||payments",
		"alice-vm|instructions||||payments",
		"alice-vm|title||||payments",
		"alice-vm|unattributed||||payments",
		"bob-vm|human|bob@example.com|usr-bob||",
	}
	if !slices.Equal(rows, want) {
		t.Errorf("documents:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	docs, err := f.storeDB.QueriesRes(t.Context(), func(q *generated.Queries) ([]generated.ListArchiveResearchThreadRow, error) {
		return q.ListArchiveResearchThread(t.Context(), generated.ListArchiveResearchThreadParams{SourceVm: "alice-vm", ConversationID: strings.TrimPrefix(f.ref(t), "alice-vm/")})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if strings.Contains(d.Text, "temporary") || strings.Contains(d.Text, "Background job") && d.Kind != "" || strings.Contains(d.Text, "retry.go") {
			t.Errorf("indexed %s %q", d.Kind, d.Text)
		}
	}
}

// ref is alice's seeded conversation's ref.
func (f *archiveResearchFixture) ref(t *testing.T) string {
	t.Helper()
	id, err := f.storeDB.QueriesRes(t.Context(), func(q *generated.Queries) (string, error) {
		rows, err := q.ListArchiveResearchConversations(t.Context(), generated.ListArchiveResearchConversationsParams{Project: "payments", Lim: 1})
		if err != nil || len(rows) == 0 {
			return "", err
		}
		return rows[0].ConversationID, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return "alice-vm/" + id
}

// Search ranks people's text first, filters, pages and counts the other
// tier.
func TestArchiveResearchSearch(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	ref := f.seed(t)
	search := func(query string) claudetool.ArchiveSearchResult {
		t.Helper()
		var res claudetool.ArchiveSearchResult
		f.read(t, "search?"+query, http.StatusOK, &res)
		return res
	}
	authors := func(res claudetool.ArchiveSearchResult) string {
		var out []string
		for _, h := range res.Hits {
			out = append(out, h.Kind+":"+h.Email)
		}
		return strings.Join(out, " ")
	}
	res := search("q=backoff")
	if res.Total != 3 || res.OtherTotal != 2 || len(res.Hits) != 3 {
		t.Fatalf("backoff: %+v", res)
	}
	if res.Hits[len(res.Hits)-1].Kind != claudetool.ArchiveInstructions {
		t.Errorf("instructions outranked people: %s", authors(res))
	}
	if h := res.Hits[0]; !strings.Contains(h.Snippet, "**backoff**") || h.Conversation.SourceEmail == "" {
		t.Errorf("hit: %+v", h)
	}
	for query, want := range map[string]string{
		"q=webhook&author=carol":                             "human:carol@example.com",
		"q=webhook+back&author=bob":                          "human:bob@example.com",
		"q=webhook&project=payments&author=alice":            "human:alice@example.com human:alice@example.com",
		"q=jitter&agent=true":                                "agent:",
		"q=webhook&since=2999-01-01":                         "",
		"q=secret+plans":                                     "",
		"q=webhook&author=carol&since=2000-01-01":            "human:carol@example.com",
		"q=webhook&author=carol&until=2999-01-01":            "human:carol@example.com",
		"q=webhook&until=2000-01-01":                         "",
		"q=retries+webhook&conversation=bob-vm/x":            "",
		"q=brief+assistant&conversation=" + ref:              "unattributed:",
		"q=webhook&author=carol&conversation=" + ref + "&x=": "human:carol@example.com",
	} {
		if got := authors(search(query)); got != want {
			t.Errorf("%s: %q, want %q", query, got, want)
		}
	}
	// Of equal matches, the more recent first.
	if got := authors(search("q=backoff")); got != "human:alice@example.com human:carol@example.com instructions:" {
		t.Errorf("backoff: %s", got)
	}
	if err := f.storeDB.Pool().Exec(t.Context(), "UPDATE archive_research_docs SET created_at = datetime('now', '-3 years') WHERE author_email = 'alice@example.com'"); err != nil {
		t.Fatal(err)
	}
	if got := authors(search("q=backoff")); got != "human:carol@example.com instructions: human:alice@example.com" {
		t.Errorf("backoff, alice's old: %s", got)
	}
	page := search("q=webhook&limit=2&offset=2")
	if page.Total != 7 || page.Offset != 2 || len(page.Hits) != 2 {
		t.Errorf("page: total %d offset %d, %d hits", page.Total, page.Offset, len(page.Hits))
	}
	f.read(t, "search?q=", http.StatusBadRequest, nil)
	f.read(t, "search?q=x&since=yesterday", http.StatusBadRequest, nil)
}

// A thread is people's messages, in order, with machine turns collapsed;
// expand shows one message in full.
func TestArchiveResearchThread(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	ref := f.seed(t)
	var th claudetool.ArchiveThread
	f.read(t, "conversations/"+ref, http.StatusOK, &th)
	var got []string
	for _, e := range th.Entries {
		switch e.Kind {
		case claudetool.ArchiveMachine:
			got = append(got, "machine "+claudetool.FormatArchiveThread(claudetool.ArchiveThread{Entries: []claudetool.ArchiveEntry{e}})[strings.Index(claudetool.FormatArchiveThread(claudetool.ArchiveThread{Entries: []claudetool.ArchiveEntry{e}}), "["):])
		default:
			got = append(got, e.Kind+" "+e.Email+" "+e.UserID+" "+e.Via)
		}
	}
	want := []string{
		"unattributed   ",
		"human alice@example.com usr-alice ",
		"machine [agent: 3 messages, 3 tool calls, final reply 9 words: expand=7]\n",
		"human carol@example.com  ",
		"human alice@example.com usr-alice text",
		"machine [agent: 1 message, 1 notice, final reply 3 words: expand=11]\n",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("thread:\n%s\nwant:\n%s", strings.Join(got, "|"), strings.Join(want, "|"))
	}
	if th.Conversation.Title != "webhook-retries" || th.Conversation.Project != "payments" || th.Conversation.SourceEmail != "alice@example.com" {
		t.Errorf("conversation: %+v", th.Conversation)
	}

	th = claudetool.ArchiveThread{}
	f.read(t, "conversations/"+ref+"?limit=2", http.StatusOK, &th)
	if len(th.Entries) != 3 || th.NextAfterSeq != 7 {
		t.Errorf("page: %d entries, next %d", len(th.Entries), th.NextAfterSeq)
	}
	th = claudetool.ArchiveThread{}
	f.read(t, "conversations/"+ref+"?after_seq=7", http.StatusOK, &th)
	if len(th.Entries) != 3 || th.Entries[0].Email != "carol@example.com" {
		t.Errorf("after_seq: %+v", th.Entries)
	}

	th = claudetool.ArchiveThread{}
	f.read(t, "conversations/"+ref+"?expand=7", http.StatusOK, &th)
	if len(th.Entries) != 1 || th.Entries[0].Kind != claudetool.ArchiveAgent || !strings.Contains(th.Entries[0].Text, "jitter") {
		t.Errorf("expand: %+v", th.Entries)
	}
	f.read(t, "conversations/"+ref+"?expand=4", http.StatusBadRequest, nil) // a tool result
	f.read(t, "conversations/alice-vm/nope", http.StatusNotFound, nil)
}

// Lists: conversations with who wrote in them, projects with instructions.
func TestArchiveResearchLists(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	f.seed(t)
	var cs claudetool.ArchiveConversations
	f.read(t, "conversations", http.StatusOK, &cs)
	if cs.Total != 2 {
		t.Fatalf("conversations: %+v", cs)
	}
	var alices claudetool.ArchiveConversation
	for _, c := range cs.Conversations {
		if c.SourceEmail == "alice@example.com" {
			alices = c
		}
	}
	if alices.HumanCount != 3 || strings.Join(alices.HumanAuthors, ",") != "alice@example.com,carol@example.com" || alices.OpeningKind != claudetool.ArchiveUnattributed {
		t.Errorf("alice's: %+v", alices)
	}
	for query, want := range map[string]int64{"author=carol": 1, "since=2000-01-01": 2, "since=2999-01-01": 0, "until=2999-01-01&project=payments": 1} {
		cs = claudetool.ArchiveConversations{}
		f.read(t, "conversations?"+query, http.StatusOK, &cs)
		if cs.Total != want {
			t.Errorf("%s: %d conversations, want %d", query, cs.Total, want)
		}
	}
	var ps claudetool.ArchiveProjects
	f.read(t, "projects", http.StatusOK, &ps)
	if len(ps.Projects) != 1 || ps.Projects[0].Conversations != 1 || !strings.Contains(ps.Projects[0].Instructions, "backoff") {
		t.Errorf("projects: %+v", ps)
	}
}

// The research API needs the proxy's identity and the flag, and logs reads.
func TestArchiveResearchAccess(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	f.seed(t)
	w := httptest.NewRecorder()
	f.storeHandler.ServeHTTP(w, httptest.NewRequest("GET", "/archive-store/research/v1/search?q=webhook", nil))
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("anonymous: %d", w.Code)
	}
	f.read(t, "search?q=webhook", http.StatusOK, nil)
	reads, err := f.storeDB.QueriesRes(t.Context(), func(q *generated.Queries) ([]string, error) {
		rows, err := q.ListArchiveResearchReads(t.Context())
		var out []string
		for _, r := range rows {
			out = append(out, r.Email+" "+r.SourceVm+" "+r.Path+"?"+r.Query)
		}
		return out, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reads, "|") != "bob@example.com bob-vm /archive-store/research/v1/search?q=webhook" {
		t.Errorf("reads: %q", reads)
	}
	r := httptest.NewRequest("POST", "/archive-store/research/v1/search?q=webhook", nil)
	r.Header.Set("X-ExeDev-UserID", "usr-bob")
	r.Header.Set("X-ExeDev-Email", "bob@example.com")
	w = httptest.NewRecorder()
	f.storeHandler.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
	if err := f.storeDB.SetFeatureFlagOverride(t.Context(), FlagArchiveResearch.Name, "false"); err != nil {
		t.Fatal(err)
	}
	f.read(t, "search?q=webhook", http.StatusNotFound, nil)
}

// A member's agent researches through the archiver's URL; the tool is
// offered only with both flags.
func TestArchiveResearchTool(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	ref := f.seed(t)
	client := archiveResearchClient{f.bob.s}
	if !client.ArchiveResearchEnabled(t.Context()) {
		t.Fatal("research off for bob")
	}
	tool := claudetool.ArchiveStoreTool(client)
	run := func(in string) string {
		t.Helper()
		out := tool.Run(t.Context(), json.RawMessage(in))
		if out.Error != nil {
			t.Fatalf("%s: %v", in, out.Error)
		}
		return out.LLMContent[0].Text
	}
	got := run(`{"action":"search","query":"jitter"}`)
	for _, want := range []string{
		`Search "jitter" in what people wrote (and titles, instructions): 1 matches`,
		"agent replies: 1 matches (agent_replies=true)",
		"1. carol@example.com (human) · ",
		`in "webhook-retries" [project payments] · alice@example.com's Shelley · ` + ref + ", seq 8",
		"I'd add **jitter** to the webhook backoff too.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("search: no %q in\n%s", want, got)
		}
	}
	got = run(`{"action":"read_thread","conversation":"` + ref + `"}`)
	for _, want := range []string{
		"#1 unattributed (no sign-in recorded on alice@example.com's Shelley: likely an agent or script, not a verified person)",
		"#2 alice@example.com (human, exe.dev user usr-alice) · ",
		"  Webhook retries hammer the merchant.",
		"[agent: 3 messages, 3 tool calls, final reply 9 words: expand=7]",
		"#9 alice@example.com (human, texted, exe.dev user usr-alice)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("thread: no %q in\n%s", want, got)
		}
	}
	got = run(`{"action":"read_thread","conversation":"` + ref + `","expand":7}`)
	if !strings.Contains(got, "#7 agent (machine-written) in alice@example.com's conversation") {
		t.Errorf("expand:\n%s", got)
	}
	t.Logf("tool output:\n%s", got)
	if out := tool.Run(t.Context(), json.RawMessage(`{"action":"read_thread","conversation":"`+ref+`","expand":4}`)); out.Error == nil || !strings.Contains(out.Error.Error(), "400") {
		t.Errorf("expand a tool result: %v", out.Error)
	}
	if err := f.bob.db.SetFeatureFlagOverride(t.Context(), FlagArchiveResearch.Name, "false"); err != nil {
		t.Fatal(err)
	}
	if client.ArchiveResearchEnabled(t.Context()) {
		t.Error("research on without its flag")
	}
}

// The index follows the store: renames, new instructions, deletions.
func TestArchiveResearchUpdates(t *testing.T) {
	t.Parallel()
	f := newArchiveResearchFixture(t)
	ref := f.seed(t)
	id := strings.TrimPrefix(ref, "alice-vm/")
	count := func(query string) int64 {
		t.Helper()
		var res claudetool.ArchiveSearchResult
		f.read(t, "search?"+query, http.StatusOK, &res)
		return res.Total
	}
	if err := f.alice.db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.UpdateConversationSlug(t.Context(), generated.UpdateConversationSlugParams{ConversationID: id, Slug: ptr("idempotency-keys")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := f.alice.db.GetProject(t.Context(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	p.Instructions = "Ledger service."
	if err := f.alice.db.UpdateProject(t.Context(), "payments", *p); err != nil {
		t.Fatal(err)
	}
	f.alice.push(t)
	if n := count("q=idempotency"); n != 1 {
		t.Errorf("new title: %d", n)
	}
	if n := count("q=retries+hammer") + count("q=ledger"); n != 2 {
		t.Errorf("messages and new instructions: %d", n)
	}
	if n := count("q=delivery+service"); n != 0 {
		t.Errorf("old instructions: %d", n)
	}
	if err := f.alice.db.DeleteConversation(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	f.alice.push(t)
	if n := count("q=webhook&project=payments"); n != 0 {
		t.Errorf("deleted conversation: %d", n)
	}
	if n := count("q=webhook"); n != 1 {
		t.Errorf("bob's: %d", n)
	}
}
