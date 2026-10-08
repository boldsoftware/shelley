package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// compactTestConversation is a conversation on a predictable model, driven
// through the chat endpoint.
type compactTestConversation struct {
	t        *testing.T
	srv      *Server
	database *db.DB
	ps       *predictable.Service
	id       string
}

func newCompactTestConversation(t *testing.T, opts db.ConversationOptions) *compactTestConversation {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	srv := NewServer(database, &twoModelLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "model-a", "")
	srv.hooksDir = t.TempDir()
	srv.piDistillKeepRecentTokens = 1
	modelA := "model-a"
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, &modelA, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &compactTestConversation{t: t, srv: srv, database: database, ps: ps, id: conv.ConversationID}
}

func (c *compactTestConversation) endTurns() int {
	n := 0
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeAgent) && m.LlmData != nil && strings.Contains(*m.LlmData, `"EndOfTurn":true`) {
			n++
		}
	}
	return n
}

func (c *compactTestConversation) turn(msg string) {
	c.t.Helper()
	before := c.endTurns()
	if w := postChat(c.t, c.srv, c.id, msg); w.Code != http.StatusAccepted {
		c.t.Fatalf("%q: got %d: %s", msg, w.Code, w.Body.String())
	}
	waitFor(c.t, 10*time.Second, func() bool { return c.endTurns() > before })
}

// rows returns the conversation's rows decoded as LLM messages, by sequence id.
func (c *compactTestConversation) rows() map[int64]llm.Message {
	out := map[int64]llm.Message{}
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
			c.t.Fatal(err)
		}
		out[m.SequenceID] = msg
	}
	return out
}

// seqWith returns the sequence id of the first row whose LLM data contains s.
func (c *compactTestConversation) seqWith(s string) int64 {
	c.t.Helper()
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.LlmData != nil && strings.Contains(*m.LlmData, s) {
			return m.SequenceID
		}
	}
	c.t.Fatalf("no row containing %q", s)
	return 0
}

// callSeq returns the sequence id of the message making the tool call
// useID: the id of its row in the index.
func (c *compactTestConversation) callSeq(useID string) int64 {
	c.t.Helper()
	for seq, m := range c.rows() {
		for _, ct := range m.Content {
			if ct.Type == llm.ContentTypeToolUse && ct.ID == useID {
				return seq
			}
		}
	}
	c.t.Fatalf("no call %s", useID)
	return 0
}

// lastToolOutput returns the text of the newest tool result.
func (c *compactTestConversation) lastToolOutput() (string, bool) {
	c.t.Helper()
	var text string
	var isErr bool
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
			c.t.Fatal(err)
		}
		for _, ct := range msg.Content {
			if ct.Type == llm.ContentTypeToolResult {
				text, isErr = ct.ToolResult[0].Text, ct.ToolError
			}
		}
	}
	return text, isErr
}

func (c *compactTestConversation) records() int {
	n := 0
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeInPlaceCompaction) {
			n++
		}
	}
	return n
}

func requestDump(req *llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		j, _ := json.Marshal(m)
		b.Write(j)
		b.WriteString("\n")
	}
	return b.String()
}

// systemCardHasTool reports whether the system prompt card lists name.
func (c *compactTestConversation) systemCardHasTool(name string) bool {
	c.t.Helper()
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeSystem) && m.DisplayData != nil {
			return strings.Contains(*m.DisplayData, `"name":"`+name+`"`)
		}
	}
	c.t.Fatal("no system prompt")
	return false
}

func hasTool(req *llm.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestCompactInPlaceToolIsOptIn(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{})
	c.turn("hello")
	if hasTool(c.ps.GetLastRequest(), claudetool.CompactInPlaceName) || c.systemCardHasTool(claudetool.CompactInPlaceName) {
		t.Fatal("compact_in_place offered without being enabled")
	}
}

// TestCompactInPlaceEndToEnd drives the tool through a conversation: the
// index lists the view, invalid requests come back as tool errors, and a valid
// one compacts the history the rest of the same turn, and later turns, see.
func TestCompactInPlaceEndToEnd(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{ToolOverrides: map[string]string{claudetool.CompactInPlaceName: "on"}})
	c.turn("hello")
	c.turn("bash: echo FIRST_OUTPUT")
	c.turn("bash: echo SECOND_OUTPUT")
	if !hasTool(c.ps.GetLastRequest(), claudetool.CompactInPlaceName) || !c.systemCardHasTool(claudetool.CompactInPlaceName) {
		t.Fatal("compact_in_place not offered, or missing from the system prompt card")
	}

	helloSeq := c.seqWith(`"Text":"hello"`)
	hiSeq := c.seqWith("Well, hi there!")
	firstResultSeq := c.seqWith(`FIRST_OUTPUT\n`)
	firstUseID := c.rows()[firstResultSeq].Content[0].ToolUseID
	firstCallSeq := c.callSeq(firstUseID)

	c.turn(`compact_in_place: {"action":"index"}`)
	index, isErr := c.lastToolOutput()
	if isErr {
		t.Fatalf("index failed: %s", index)
	}
	for _, want := range []string{
		"## Index",
		"dead ends",
		fmt.Sprintf("%d  ", helloSeq),
		"is kept as is and not listed",
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("index missing %q:\n%s", want, index)
		}
	}
	// The call and its output share a row, under the call's id.
	if want := regexp.MustCompile(fmt.Sprintf(`(?m)^%d +assistant +\d+ .*bash "echo FIRST_OUTPUT" → \d+$`, firstCallSeq)); !want.MatchString(index) {
		t.Fatalf("index lacks the row of the first call and its output:\n%s", index)
	}
	if want := regexp.MustCompile(fmt.Sprintf(`(?m)^%d +user\* `, helloSeq)); !want.MatchString(index) {
		t.Fatalf("index does not mark the user's message with *:\n%s", index)
	}
	// The recent part (the index request) is not listed.
	if strings.Contains(index, `"compact_in_place: `) {
		t.Fatalf("index lists the recent part:\n%s", index)
	}

	compact := func(trim []claudetool.IndexID, collapse ...claudetool.CompactCollapse) {
		t.Helper()
		in, _ := json.Marshal(claudetool.CompactInPlaceInput{Action: "compact", Trim: trim, Collapse: collapse})
		c.turn("compact_in_place: " + string(in))
	}
	seq := func(n int64) claudetool.IndexID { return claudetool.IndexID(fmt.Sprint(n)) }
	for want, collapse := range map[string]claudetool.CompactCollapse{
		"unknown id":           {From: "999999", To: seq(hiSeq), Note: "x"},
		"from comes after to":  {From: seq(hiSeq), To: seq(helloSeq), Note: "x"},
		"note is not shorter":  {From: seq(hiSeq), To: seq(hiSeq), Note: strings.Repeat("long ", 100)},
		"only be collapsed on": {From: seq(helloSeq), To: seq(hiSeq), Note: "x"},
	} {
		compact(nil, collapse)
		if out, isErr := c.lastToolOutput(); !isErr || !strings.Contains(out, want) {
			t.Fatalf("want error %q, got %q (error %v)", want, out, isErr)
		}
	}
	compact([]claudetool.IndexID{"nope"})
	if out, isErr := c.lastToolOutput(); !isErr || !strings.Contains(out, "trim nope: unknown id") {
		t.Fatalf("bad trim: %q", out)
	}
	if n := c.records(); n != 0 {
		t.Fatalf("invalid requests recorded %d compactions", n)
	}

	compact([]claudetool.IndexID{seq(firstCallSeq)}, claudetool.CompactCollapse{From: seq(hiSeq), To: seq(hiSeq), Note: "GREETED"})
	if out, isErr := c.lastToolOutput(); isErr || !strings.Contains(out, "Compacted: 1 collapsed, 1 trimmed") {
		t.Fatalf("compact: %q", out)
	}
	if n := c.records(); n != 1 {
		t.Fatalf("records = %d", n)
	}

	// The request that followed the tool call, in the same turn, already
	// sees the compacted history, ending with the compact call's output.
	check := func(req *llm.Request) {
		t.Helper()
		got := requestDump(req)
		if second := req.Messages[1].Content[0].Text; second != compactionNoteText(c.id, hiSeq, hiSeq, "GREETED") {
			t.Fatalf("second message is not the note:\n%s", got)
		}
		for _, m := range req.Messages {
			for _, ct := range m.Content {
				if ct.Text == "Well, hi there!" || ct.ToolUseID == firstUseID && ct.ToolResult[0].Text == "FIRST_OUTPUT\n" {
					t.Fatalf("compacted content leaked:\n%s", got)
				}
			}
		}
		if !strings.Contains(got, trimmedToolOutputText(c.id, firstResultSeq)) || !strings.Contains(got, `SECOND_OUTPUT\n`) {
			t.Fatalf("want the first output trimmed and the second intact:\n%s", got)
		}
		if n := strings.Count(got, "Compacted: 1 collapsed"); n != 1 {
			t.Fatalf("compact output appears %d times:\n%s", n, got)
		}
		// Earlier compact_in_place calls (the index, failed attempts) are
		// hidden; only the successful call remains, as a receipt.
		if strings.Contains(got, "## Index") || strings.Contains(got, "unknown id") {
			t.Fatalf("earlier compact_in_place calls not hidden:\n%s", got)
		}
		if n := strings.Count(got, `"ToolName":"compact_in_place"`); n != 1 {
			t.Fatalf("%d compact_in_place calls in context, want 1:\n%s", n, got)
		}
	}
	check(c.ps.GetLastRequest())

	// A later turn on the same loop sees the same history.
	c.turn("echo: AFTER")
	check(c.ps.GetLastRequest())

	// And so does a loop rebuilt from the database.
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.ResetLoop()
	c.turn("echo: REBUILT")
	check(c.ps.GetLastRequest())

	// The next index shows the note and the trimmed output.
	c.turn(`compact_in_place: {"action":"index"}`)
	index, _ = c.lastToolOutput()
	if !strings.Contains(index, `note "GREETED"`) || !strings.Contains(index, `bash "echo FIRST_OUTPUT" → trimmed`) {
		t.Fatalf("index after compaction:\n%s", index)
	}
}

// TestCompactInPlaceNudge: with the tool enabled, the agent is told the
// context size once it reaches the threshold, and not again until it grows a
// step further.
func TestCompactInPlaceNudge(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{
		ToolOverrides:      map[string]string{claudetool.CompactInPlaceName: "on"},
		CompactNudgeTokens: 1,
	})
	nudges := func() []llm.Message {
		var out []llm.Message
		for _, m := range listMessages(t, c.database, c.id) {
			if m.UserData != nil && strings.Contains(*m.UserData, `"context_nudge":true`) {
				var msg llm.Message
				if err := json.Unmarshal([]byte(*m.LlmData), &msg); err != nil {
					t.Fatal(err)
				}
				out = append(out, msg)
			}
		}
		return out
	}
	c.turn("hello")
	if n := len(nudges()); n != 0 {
		t.Fatalf("nudged before any usage: %d", n)
	}
	c.turn("echo: two")
	got := nudges()
	// The nudge comes with the index, so the agent can compact right away.
	if len(got) != 1 || len(got[0].Content) != 2 || got[0].Content[0].Text != "Context is 0k." ||
		!strings.HasPrefix(got[0].Content[1].Text, "## Index") || !strings.Contains(got[0].Content[1].Text, `text "hello"`) {
		t.Fatalf("nudges = %+v", got)
	}
	if req := requestDump(c.ps.GetLastRequest()); !strings.Contains(req, "Context is 0k.") || !strings.Contains(req, "## Index") {
		t.Fatalf("nudge not sent to the model:\n%s", req)
	}
	c.turn("echo: three")
	if n := len(nudges()); n != 1 {
		t.Fatalf("nudged again below the next step: %d", n)
	}

	// The agent can compact from the nudge's index, without asking for one,
	// and the compaction hides the nudges from the model.
	var id string
	for _, line := range strings.Split(got[0].Content[1].Text, "\n") {
		if strings.Contains(line, `text "Well, hi there!"`) {
			id = strings.Fields(line)[0]
		}
	}
	c.turn(`compact_in_place: {"action":"compact","collapse":[{"from":"` + id + `","to":"` + id + `","note":"hi"}]}`)
	if out, isErr := c.lastToolOutput(); isErr {
		t.Fatalf("compact: %s", out)
	}
	if got := requestDump(c.ps.GetLastRequest()); strings.Contains(got, "Context is") {
		t.Fatalf("nudge still in context after compaction:\n%s", got)
	}

	// The response that makes a compact call crosses the next nudge level
	// (here, as its input carries a long message). Right after the
	// compaction that size is stale: no nudge.
	c.turn("bash: echo AGAIN")
	callSeq := c.callSeq(c.rows()[c.seqWith(`AGAIN\n`)].Content[0].ToolUseID)
	size := lastContextWindowSize(listMessages(t, c.database, c.id))
	if err := c.database.UpdateConversationOptions(t.Context(), c.id, db.ConversationOptions{
		ToolOverrides:      map[string]string{claudetool.CompactInPlaceName: "on"},
		CompactNudgeTokens: int(size) + 100,
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.ResetLoop()
	before := len(nudges())
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":[%d]}`, callSeq) + strings.Repeat(" ", 2000))
	if out, isErr := c.lastToolOutput(); isErr {
		t.Fatalf("compact: %s", out)
	}
	if got := nudges(); len(got) != before {
		t.Fatalf("nudged with the size from before the compaction: %v", got[before:])
	}
}

func TestContextNudger(t *testing.T) {
	n := newContextNudger(0, 260_000)
	if _, ok := n.take(); ok {
		t.Fatal("a fresh nudger repeats the level the conversation already had")
	}
	observe := func(tokens uint64) { n.observe(llm.Usage{InputTokens: tokens}) }
	for _, step := range []struct {
		tokens uint64
		want   string
	}{
		{290_000, ""},
		{302_000, "Context is 300k."},
		{320_000, ""},
		{90_000, ""}, // compacted: re-arms
		{249_000, ""},
		{253_000, "Context is 250k."},
		{410_000, "Context is 410k."},
	} {
		observe(step.tokens)
		if got, _ := n.take(); got != step.want {
			t.Fatalf("at %d: got %q, want %q", step.tokens, got, step.want)
		}
	}
}

func callItem(seq int64, id string) contextItem {
	return contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq, Type: string(db.MessageTypeAgent)}, message: llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
		{Type: llm.ContentTypeToolUse, ID: id, ToolName: "bash"},
	}}}
}

func outputItem(seq int64, id, text string) contextItem {
	// Like a stored tool output, which is a user message that carries only results.
	return contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq, Type: string(db.MessageTypeUser)}, message: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{
		{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: text}}},
	}}}
}

// TestBuildCompactionKeepsCallsWithOutputs: a range covers whole rows, so it
// cannot split a tool call from its output; the output's own sequence id is
// not an id in the index. Other problems of the request are reported with it.
func TestBuildCompactionKeepsCallsWithOutputs(t *testing.T) {
	out := strings.Repeat("x", 400)
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"), outputItem(3, "a", out),
		callItem(4, "b"), outputItem(5, "b", out),
		textItem(6, llm.MessageRoleAssistant, "done"),
		textItem(7, llm.MessageRoleAssistant, strings.Repeat("x", 4*25_000)),
	}
	col := func(from, to claudetool.IndexID, note string) claudetool.CompactCollapse {
		return claudetool.CompactCollapse{From: from, To: to, Note: note}
	}
	_, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{
		col("3", "6", "n"), col("2", "3", "n"), col("4", "4", ""),
	}})
	want := "3 problems. Nothing was compacted; fix them and send the whole request again:\n" +
		"- collapse 3-6: unknown id; use ids from the index\n" +
		"- collapse 2-3: unknown id; use ids from the index\n" +
		"- collapse 4-4: note is empty"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	c, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{col("2", "2", "n"), col("4", "6", "n")}})
	if err != nil || len(c.Squishes) != 2 || c.Squishes[0].ToSequenceID != 3 || c.Squishes[1].FromSequenceID != 4 || c.Squishes[1].ToSequenceID != 6 {
		t.Fatalf("%+v, %v", c, err)
	}
}

// TestBuildCompactionOrphans: a call can lack its output (the server stopped
// while it ran), or an output its call. Such a row gets a row of its own, and
// a range may cover it, but not end at the call or start at the output: that
// would leave the output, or the call, behind.
func TestBuildCompactionOrphans(t *testing.T) {
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"), outputItem(3, "a", "ok"),
		callItem(4, "lost"),
		textItem(5, llm.MessageRoleAssistant, strings.Repeat("x", 400)),
		outputItem(6, "late", "late"),
		textItem(7, llm.MessageRoleAssistant, "done"),
		textItem(8, llm.MessageRoleAssistant, strings.Repeat("x", 4*25_000)),
	}
	index, err := compactIndex(items, 20_000)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^4 .*bash → no output$`).MatchString(index) || !regexp.MustCompile(`(?m)^6 .*tool output → 1$`).MatchString(index) {
		t.Fatalf("index does not show the orphans:\n%s", index)
	}
	col := func(from, to claudetool.IndexID) claudetool.CompactCollapse {
		return claudetool.CompactCollapse{From: from, To: to, Note: "n"}
	}
	_, err = buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{col("2", "4"), col("6", "7")}})
	want := "2 problems. Nothing was compacted; fix them and send the whole request again:\n" +
		"- collapse 2-4: 4 is a tool call without its output; end the range before it\n" +
		"- collapse 6-7: 6 is a tool output without its call; start the range after it"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	// Suggestions around a row that can only be collapsed on its own leave
	// them out at the ends.
	withUser := slices.Insert(slices.Clone(items), 4, textItem(10, llm.MessageRoleUser, "back"))
	_, err = buildCompaction(withUser, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{col("2", "5")}})
	if want := "covers 10, which can only be collapsed on its own; collapse e.g. 2, 5 instead"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
	// Otherwise they go with the range.
	for _, r := range []claudetool.CompactCollapse{col("2", "5"), col("4", "6"), col("5", "7")} {
		c, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{r}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyInPlaceCompaction(items, c, "c1", 9); err != nil {
			t.Fatalf("collapse %s-%s: %v", r.From, r.To, err)
		}
	}
}

func TestReadableToolInput(t *testing.T) {
	for in, want := range map[string]string{
		`{"command":"cd /x \u0026\u0026 ls","slow_ok":false}`: "cd /x && ls slow_ok=false",
		`{"a":{"b":1},"c":[1],"d":null,"e":"","n":2.5}`:       "n=2.5",
		`"text"`:    `"text"`,
		`null`:      "null",
		``:          "",
		`{"a":"x",`: `{"a":"x",`,
	} {
		if got := readableToolInput(json.RawMessage(in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func compactCallItem(seq int64, id string) contextItem {
	it := callItem(seq, id)
	it.message.Content[0].ToolName = claudetool.CompactInPlaceName
	return it
}

func nudgeItem(seq int64) contextItem {
	it := textItem(seq, llm.MessageRoleUser, "Context is 160k.")
	nudge := `{"context_nudge":true}`
	it.source.UserData = &nudge
	return it
}

// recentID returns the id of the first row of the recent part of history.
func recentID(t *testing.T, history []contextItem, keepRecentTokens int) string {
	t.Helper()
	view, err := compactionView(history)
	if err != nil {
		t.Fatal(err)
	}
	r := recentStart(view, keepRecentTokens)
	if r >= len(view) {
		t.Fatalf("recent start %d of %d rows", r, len(view))
	}
	return itemID(view[r])
}

// TestRecentPartIgnoresCompacting: the recent part starts where it did before
// the agent began compacting, however much the index output, the nudge and the
// compact call that is running add.
func TestRecentPartIgnoresCompacting(t *testing.T) {
	big := strings.Repeat("x", 4*15_000) // ~15k tokens
	history := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"),
		outputItem(3, "a", big),
		callItem(4, "b"),
		outputItem(5, "b", big),
		textItem(6, llm.MessageRoleAssistant, "done"),
		textItem(7, llm.MessageRoleUser, big+big),
		callItem(8, "c"),
		outputItem(9, "c", "out"),
		textItem(10, llm.MessageRoleAssistant, "ok"),
	}
	if got := recentID(t, history, 20_000); got != "7" {
		t.Fatalf("recent part starts at %s, want 7", got)
	}
	running := compactCallItem(14, "compact")
	running.message.Content[0].ToolInput = json.RawMessage(`"` + big + `"`)
	compacting := append(slices.Clone(history),
		nudgeItem(11), compactCallItem(12, "index"), outputItem(13, "index", big), running)
	if got := recentID(t, compacting, 20_000); got != "7" {
		t.Fatalf("recent part starts at %s while compacting, want 7", got)
	}
	// A single tool output that fills the budget stays with its call.
	huge := append(slices.Clone(history[:6]), callItem(7, "d"), outputItem(8, "d", strings.Repeat("x", 4*25_000)), compactCallItem(9, "compact"))
	if got := recentID(t, huge, 20_000); got != "7" {
		t.Fatalf("recent part starts at %s after a huge output, want 7 (its call)", got)
	}
}

// TestRecentPartHoldsWithParallelCalls: the call that is running shares its
// message with another call; when the calls finish, that message is still
// there, so the recent part must not start later than it did while they ran.
func TestRecentPartHoldsWithParallelCalls(t *testing.T) {
	big := strings.Repeat("x", 4*15_000)
	parallel := callItem(3, "a")
	parallel.message.Content[0].ToolInput = json.RawMessage(`"` + big + `"`)
	parallel.message.Content = append(parallel.message.Content, compactCallItem(3, "index").message.Content[0])
	running := []contextItem{textItem(1, llm.MessageRoleUser, "do it"), textItem(2, llm.MessageRoleAssistant, "ok"), parallel}
	done := append(slices.Clone(running), outputItem(4, "a", "out"), outputItem(5, "index", "index"))
	if got, want := recentID(t, running, 10_000), recentID(t, done, 10_000); got != "3" || want != "3" {
		t.Fatalf("recent part starts at %s while the calls run, at %s after: want 3 both times", got, want)
	}
}

// TestIndexSkipsEvidenceOfEarlierAttempts: an earlier index call, its output
// and a nudge are hidden by the next compaction, so the index does not list
// them.
func TestIndexSkipsEvidenceOfEarlierAttempts(t *testing.T) {
	history := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"),
		outputItem(3, "a", "small"),
		nudgeItem(4),
		compactCallItem(5, "index"),
		outputItem(6, "index", "index"),
		textItem(7, llm.MessageRoleUser, "go on"),
		textItem(8, llm.MessageRoleAssistant, strings.Repeat("x", 4*15_000)),
		textItem(9, llm.MessageRoleUser, "and on"),
	}
	index, err := compactIndex(history, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, table, _ := strings.Cut(index, "```\n")
	rows := regexp.MustCompile(`(?m)^(\d+) `).FindAllStringSubmatch(table, -1)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r[1])
	}
	if got := strings.Join(ids, " "); got != "1 2 7 8" {
		t.Errorf("index lists rows %s, want 1 2 7 8 (the work, then not the recent part from 9):\n%s", got, index)
	}
}

// TestCompactIndexRows: a tool call shares a row with its output, under the
// call's id. Inputs read like commands, and outputs show their size or that
// they are trimmed.
func TestCompactIndexRows(t *testing.T) {
	use := func(id, name, input string) llm.Content {
		return llm.Content{Type: llm.ContentTypeToolUse, ID: id, ToolName: name, ToolInput: json.RawMessage(input)}
	}
	result := func(id, text string) llm.Content {
		return llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: text}}}
	}
	msg := func(seq int64, role llm.MessageRole, content ...llm.Content) contextItem {
		typ := string(db.MessageTypeAgent)
		if role == llm.MessageRoleUser {
			typ = string(db.MessageTypeUser)
		}
		return contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq, Type: typ}, message: llm.Message{Role: role, Content: content}}
	}
	history := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		msg(2, llm.MessageRoleAssistant, llm.Content{Type: llm.ContentTypeThinking, Thinking: "hmm"},
			use("a", "bash", `{"command":"cd /x \u0026\u0026 ls\n  -la","slow_ok":false}`)),
		// Output that merely starts like a trimmed one is not trimmed.
		msg(3, llm.MessageRoleUser, result("a", "[Tool output compacted, says the log]"+strings.Repeat("x", 363))),
		// Parallel calls; one output is trimmed already.
		msg(4, llm.MessageRoleAssistant, use("b", "bash", `{"command":"make"}`),
			use("c", "browser", `{"action":"navigate","url":"http://localhost/","expression":"","await":false,"timeout":0,"tabs":[1]}`)),
		msg(5, llm.MessageRoleUser, result("b", trimmedToolOutputText("c1", 5)), result("c", "page")),
		textItem(6, llm.MessageRoleAssistant, "done"),
		textItem(7, llm.MessageRoleUser, strings.Repeat("x", 4*15_000)),
	}
	index, err := compactIndex(history, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, table, _ := strings.Cut(index, "```\n")
	want := "id  role       tokens  content\n" +
		"1   user*      2       text \"do it\"\n" +
		"2   assistant  117     thinking, bash \"cd /x && ls -la slow_ok=false\" → 100\n" +
		"4   assistant  49      bash \"make\" → trimmed, browser \"navigate http://localhost/ await=false timeout=0\" → 1\n" +
		"6   assistant  1       text \"done\"\n" +
		"```\n"
	if table != want {
		t.Fatalf("got:\n%s\nwant:\n%s", table, want)
	}
}

func TestBuildCompactionTrimsOnlyOlderOutputs(t *testing.T) {
	history := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"), outputItem(3, "a", "old"),
		callItem(4, "t"), outputItem(5, "t", trimmedToolOutputText("c1", 5)),
		callItem(6, "b"), outputItem(7, "b", "new"),
	}
	// The recent part is the last call and its output.
	// Rows are trimmed by id: the call's.
	c, err := buildCompaction(history, 1, claudetool.CompactInPlaceInput{Trim: []claudetool.IndexID{"2"}})
	if err != nil || len(c.Trims) != 1 || c.Trims[0] != (db.CompactionTrim{SequenceID: 3, ToolUseID: "a"}) {
		t.Fatalf("trim 2: %+v, %v", c, err)
	}
	for id, want := range map[claudetool.IndexID]string{
		"6": "in the recent part, which is kept as is",
		"4": "already trimmed",
		"1": "has no tool output",
		"3": "unknown id",
	} {
		_, err = buildCompaction(history, 1, claudetool.CompactInPlaceInput{Trim: []claudetool.IndexID{id}})
		if want := fmt.Sprintf("trim %s: %s", id, want); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func TestBuildCompaction(t *testing.T) {
	big := strings.Repeat("x", 4*15_000) // ~15k tokens
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "do it"),
		callItem(2, "a"),
		outputItem(3, "a", strings.Repeat("x", 4*25_000)),
		textItem(4, llm.MessageRoleAssistant, big),
		textItem(5, llm.MessageRoleAssistant, "a"),
		textItem(6, llm.MessageRoleUser, "also this"),
		textItem(7, llm.MessageRoleUser, "and that"),
		textItem(8, llm.MessageRoleAssistant, "b"),
		textItem(9, llm.MessageRoleAssistant, "c"),
		textItem(10, llm.MessageRoleAssistant, big+big),
	}
	// The recent part is the last message.
	if r := recentStart(items, 20_000); r != 9 {
		t.Fatalf("recent start = %d", r)
	}
	col := func(from, to claudetool.IndexID) claudetool.CompactCollapse {
		return claudetool.CompactCollapse{From: from, To: to, Note: "n"}
	}
	for want, in := range map[string]claudetool.CompactInPlaceInput{
		"nothing to compact": {},
		"recent part":        {Collapse: []claudetool.CompactCollapse{col("9", "10")}},
		"at most":            {Collapse: []claudetool.CompactCollapse{col("2", "4")}},
		"overlaps":           {Collapse: []claudetool.CompactCollapse{col("8", "9"), col("9", "9")}},
		"note is empty":      {Collapse: []claudetool.CompactCollapse{{From: "8", To: "9"}}},
		// The user's messages may only be collapsed on their own.
		"collapse 5-8: covers 6, which can only be collapsed on its own; collapse e.g. 5, 8 instead":             {Collapse: []claudetool.CompactCollapse{col("5", "8")}},
		"collapse 6-7: covers 6, which can only be collapsed on its own; collapse each of those rows on its own": {Collapse: []claudetool.CompactCollapse{col("6", "7")}},
	} {
		if _, err := buildCompaction(items, 20_000, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error %q, got %v", want, err)
		}
	}
	// Every problem is reported at once, and nothing is compacted.
	_, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{
		Trim:     []claudetool.IndexID{"nope"},
		Collapse: []claudetool.CompactCollapse{col("99", "2"), col("2", "4")},
	})
	want := "3 problems. Nothing was compacted; fix them and send the whole request again:\n" +
		"- trim nope: unknown id; use ids from the index\n" +
		"- collapse 99-2: unknown id; use ids from the index\n" +
		"- collapse 2-4: covers ~"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("want error starting %q, got %v", want, err)
	}
	// Over the limit is fine for one row, here a tool call and its output,
	// and a user message may be collapsed on its own.
	c, err := buildCompaction(items, 20_000, claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{col("8", "9"), col("2", "2"), col("6", "6"), col("1", "1")}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range c.Squishes {
		got = append(got, fmt.Sprintf("%d-%d", s.FromSequenceID, s.ToSequenceID))
	}
	if g := strings.Join(got, " "); g != "1-1 2-3 6-6 8-9" {
		t.Fatalf("squishes %s", g)
	}
}

func TestCollapsesAlone(t *testing.T) {
	withUserData := func(it contextItem, ud string) contextItem {
		it.source.UserData = &ud
		return it
	}
	for _, tc := range []struct {
		it   contextItem
		want bool
	}{
		{textItem(1, llm.MessageRoleUser, "fix it"), true},
		{withUserData(textItem(1, llm.MessageRoleUser, "summary"), `{"distilled":"true","distillation_content":"summary"}`), true},
		{withUserData(textItem(1, llm.MessageRoleUser, "do this"), `{"sender_conversation_id":"c1","sender_relationship":"parent"}`), true},
		{withUserData(textItem(1, llm.MessageRoleUser, "found it"), `{"sender_conversation_id":"c2","sender_relationship":"subagent"}`), false},
		{contextItem{noteID: "s5.0", note: "earlier"}, true},
		{textItem(1, llm.MessageRoleAssistant, "done"), false},
		{outputItem(1, "a", "out"), false},
	} {
		got, err := collapsesAlone(tc.it)
		if err != nil || got != tc.want {
			t.Errorf("%+v: got %v, %v", tc.it, got, err)
		}
	}
	if _, err := collapsesAlone(withUserData(textItem(1, llm.MessageRoleUser, "x"), `{`)); err == nil {
		t.Error("malformed user data: want an error")
	}
}

func TestHideItems(t *testing.T) {
	use := func(seq int64, ids ...string) contextItem {
		it := textItem(seq, llm.MessageRoleAssistant, "calling")
		for _, id := range ids {
			it.message.Content = append(it.message.Content, llm.Content{Type: llm.ContentTypeToolUse, ID: id})
		}
		return it
	}
	result := func(seq int64, ids ...string) contextItem {
		it := contextItem{from: seq, to: seq, source: &generated.Message{SequenceID: seq}, message: llm.Message{Role: llm.MessageRoleUser}}
		for _, id := range ids {
			it.message.Content = append(it.message.Content, llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: id})
		}
		return it
	}
	items := []contextItem{
		textItem(1, llm.MessageRoleUser, "go"),
		use(2, "compact"),
		result(3, "compact"),
		textItem(4, llm.MessageRoleUser, "Context is 160k."),
		use(5, "compact2", "bash"),
		result(6, "compact2", "bash"),
	}
	got, err := hideItems(slices.Clone(items), []int64{4}, []string{"compact", "compact2"})
	if err != nil {
		t.Fatal(err)
	}
	var spans []string
	for _, it := range got {
		spans = append(spans, fmt.Sprintf("%d:%d", it.from, len(it.message.Content)))
	}
	// The parallel bash call and its result stay; so does the text beside it.
	if s := strings.Join(spans, " "); s != "1:1 5:2 6:1" {
		t.Fatalf("got %s", s)
	}
	if len(items[4].message.Content) != 3 {
		t.Fatal("input message modified")
	}
	for _, bad := range [][]string{{"missing"}, {"compact", "compact"}} {
		if _, err := hideItems(slices.Clone(items), nil, bad); err == nil {
			t.Errorf("hide %v: want error", bad)
		}
	}
}

// postEnableCompactInPlace presses the Compact in Place button's first half.
func postEnableCompactInPlace(t *testing.T, s *Server, conversationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversationID+"/enable-compact-in-place", nil)
	w := httptest.NewRecorder()
	s.handleEnableCompactInPlace(w, req, conversationID)
	return w
}

// toolEnabledMarkers counts the log rows recording compact_in_place being
// enabled.
func (c *compactTestConversation) toolEnabledMarkers() int {
	n := 0
	for _, m := range listMessages(c.t, c.database, c.id) {
		if m.Type == string(db.MessageTypeModelChange) && m.UserData != nil &&
			strings.Contains(*m.UserData, `"tools_on":["compact_in_place"]`) {
			n++
		}
	}
	return n
}

func (c *compactTestConversation) options() db.ConversationOptions {
	c.t.Helper()
	conv, err := c.database.GetConversationByID(c.t.Context(), c.id)
	if err != nil {
		c.t.Fatal(err)
	}
	return db.ParseConversationOptions(conv.ConversationOptions)
}

// TestEnableCompactInPlaceMidConversation: a conversation started without
// compact_in_place gets it on demand. Later turns offer the tool, the log
// says where it appeared, and the agent is not nudged: the user asked for a
// compaction, not for an agent that watches its context.
func TestEnableCompactInPlaceMidConversation(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{})
	c.turn("hello")
	c.turn("bash: echo OUTPUT")
	if hasTool(c.ps.GetLastRequest(), claudetool.CompactInPlaceName) {
		t.Fatal("compact_in_place offered before being enabled")
	}
	// Were nudges on, the next turns would cross the first threshold.
	size := lastContextWindowSize(listMessages(t, c.database, c.id))
	if err := c.database.UpdateConversationOptions(t.Context(), c.id, db.ConversationOptions{CompactNudgeTokens: int(size) + 100}); err != nil {
		t.Fatal(err)
	}

	if w := postEnableCompactInPlace(t, c.srv, c.id); w.Code != http.StatusOK {
		t.Fatalf("enable: got %d: %s", w.Code, w.Body.String())
	}
	opts := c.options()
	if !claudetool.IsToolEnabled(claudetool.CompactInPlaceName, opts.ToolOverrides, opts.DisableAllTools) || !opts.DisableCompactNudges {
		t.Fatalf("options after enabling: %+v", opts)
	}
	if n := c.toolEnabledMarkers(); n != 1 {
		t.Fatalf("%d markers, want 1", n)
	}

	c.turn("echo: two" + strings.Repeat(" ", 2000))
	c.turn("echo: three")
	for _, m := range listMessages(t, c.database, c.id) {
		if m.UserData != nil && strings.Contains(*m.UserData, `"context_nudge":true`) {
			t.Fatalf("nudged: %s", *m.LlmData)
		}
	}
	req := c.ps.GetLastRequest()
	if !hasTool(req, claudetool.CompactInPlaceName) {
		t.Fatal("compact_in_place not offered after enabling")
	}
	if strings.Contains(requestDump(req), "Enabled the compact_in_place tool") {
		t.Fatal("the marker reached the model")
	}
	callSeq := c.callSeq(c.rows()[c.seqWith(`OUTPUT\n`)].Content[0].ToolUseID)
	c.turn(fmt.Sprintf(`compact_in_place: {"action":"compact","trim":["%d"]}`, callSeq))
	if out, isErr := c.lastToolOutput(); isErr {
		t.Fatalf("compact: %s", out)
	}
	if c.records() != 1 {
		t.Fatalf("%d compaction records, want 1", c.records())
	}

	// Pressing the button again changes nothing.
	if w := postEnableCompactInPlace(t, c.srv, c.id); w.Code != http.StatusOK {
		t.Fatalf("enable again: got %d: %s", w.Code, w.Body.String())
	}
	if n := c.toolEnabledMarkers(); n != 1 {
		t.Fatalf("%d markers after enabling twice, want 1", n)
	}

	// Choosing the nudge yourself brings nudges back.
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	nudge := 50_000
	if _, err := c.srv.changeSettings(t.Context(), manager, SettingsChange{CompactNudgeTokens: &nudge}); err != nil {
		t.Fatal(err)
	}
	if opts := c.options(); opts.DisableCompactNudges || opts.CompactNudgeTokens != nudge {
		t.Fatalf("options after choosing the nudge: %+v", opts)
	}
}

// Enabling rebuilds the loop, which would drop a running turn's tools out
// from under it, so it waits for the turn to end.
func TestEnableCompactInPlaceRefusesWhileWorking(t *testing.T) {
	t.Parallel()
	c := newCompactTestConversation(t, db.ConversationOptions{})
	c.turn("hello")
	if w := postChat(t, c.srv, c.id, "bash: sleep 5"); w.Code != http.StatusAccepted {
		t.Fatalf("chat: got %d: %s", w.Code, w.Body.String())
	}
	manager, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, manager.IsAgentWorking)

	if w := postEnableCompactInPlace(t, c.srv, c.id); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 while the agent works, got %d: %s", w.Code, w.Body.String())
	}
	if opts := c.options(); opts.ToolOverrides[claudetool.CompactInPlaceName] != "" || c.toolEnabledMarkers() != 0 {
		t.Fatalf("enabled while the agent was working: %+v", opts)
	}
}
