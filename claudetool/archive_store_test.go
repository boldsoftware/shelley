package claudetool

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeArchiveStore answers every request with reply and records it.
type fakeArchiveStore struct {
	on    bool
	reply any
	got   []string
}

func (f *fakeArchiveStore) ArchiveResearchEnabled(context.Context) bool { return f.on }

func (f *fakeArchiveStore) ArchiveGet(_ context.Context, path string, q url.Values, out any) error {
	f.got = append(f.got, path+"?"+q.Encode())
	b, err := json.Marshal(f.reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func TestArchiveStoreToolOffered(t *testing.T) {
	for _, on := range []bool{false, true} {
		offered := false
		for _, tool := range NewToolSet(t.Context(), ToolSetConfig{ArchiveStore: &fakeArchiveStore{on: on}}).Tools() {
			offered = offered || tool.Name == ArchiveStoreName
		}
		if offered != on {
			t.Errorf("enabled %v: offered %v", on, offered)
		}
	}
}

// The tool's requests, and its output: who wrote each piece, compactly.
func TestArchiveStoreTool(t *testing.T) {
	day := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	conv := ArchiveConversation{Ref: "alice-vm/cA", URL: "https://store.exe.xyz:8000/archive-store/#alice-vm/cA", Title: "webhook-retries", Project: "payments", SourceEmail: "alice@example.com"}
	store := &fakeArchiveStore{on: true, reply: ArchiveSearchResult{Total: 3, OtherTotal: 1, Hits: []ArchiveHit{
		{ArchiveAuthor: ArchiveAuthor{Kind: ArchiveHuman, Email: "alice@example.com", UserID: "usr-alice"}, Conversation: conv, SequenceID: 2, CreatedAt: day, Snippet: "Use exponential\n**backoff**."},
		{ArchiveAuthor: ArchiveAuthor{Kind: ArchiveUnattributed}, Conversation: conv, SequenceID: 1, CreatedAt: day, Snippet: "look into **backoff**"},
	}}}
	tool := ArchiveStoreTool(store)
	out := tool.Run(t.Context(), json.RawMessage(`{"action":"search","query":"backoff","author":" alice ","since":"2026-11-01","limit":2}`))
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	want := `Search "backoff" in what people wrote (and titles, instructions): 3 matches, showing 1-2. agent replies: 1 matches (agent_replies=true).

1. alice@example.com (human, exe.dev user usr-alice) · 2026-11-03 09:30 UTC
   in "webhook-retries" [project payments] · alice@example.com's Shelley · alice-vm/cA · https://store.exe.xyz:8000/archive-store/#alice-vm/cA, seq 2
   Use exponential **backoff**.

2. unattributed (no sign-in recorded on alice@example.com's Shelley: likely an agent or script, not a verified person) · 2026-11-03 09:30 UTC
   in "webhook-retries" [project payments] · alice@example.com's Shelley · alice-vm/cA · https://store.exe.xyz:8000/archive-store/#alice-vm/cA, seq 1
   look into **backoff**

More: offset=2.
`
	if got := out.LLMContent[0].Text; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := strings.Join(store.got, " "); got != "search?author=alice&limit=2&q=backoff&since=2026-11-01" {
		t.Errorf("request: %s", got)
	}

	store.reply = ArchiveThread{Conversation: conv, NextAfterSeq: 9, Entries: []ArchiveEntry{
		{ArchiveAuthor: ArchiveAuthor{Kind: ArchiveHuman, Email: "carol@example.com"}, SequenceID: 2, CreatedAt: day, Text: "Add jitter.\nPlease.", Truncated: 120},
		{ArchiveAuthor: ArchiveAuthor{Kind: ArchiveMachine}, SequenceID: 3, AgentMessages: 3, ToolCalls: 12, Notices: 1, ReplySeq: 8, ReplyWords: 80},
		{ArchiveAuthor: ArchiveAuthor{Kind: ArchiveHuman, Email: "alice@example.com", Via: "text"}, SequenceID: 9, CreatedAt: day, Text: "Ship it."},
	}}
	store.got = nil
	out = tool.Run(t.Context(), json.RawMessage(`{"action":"read_thread","conversation":"alice-vm/cA","after_seq":1}`))
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	want = `Conversation "webhook-retries" [project payments] · alice@example.com's Shelley · alice-vm/cA · https://store.exe.xyz:8000/archive-store/#alice-vm/cA, 0001-01-01 00:00 UTC to 0001-01-01 00:00 UTC.
People's messages in order; machine turns collapsed (expand=SEQ shows one in full).

#2 carol@example.com (human) · 2026-11-03 09:30 UTC
  Add jitter.
  Please.
  [120 more characters: expand=2]

  [agent: 3 messages, 12 tool calls, 1 notice, final reply 80 words: expand=8]

#9 alice@example.com (human, texted) · 2026-11-03 09:30 UTC
  Ship it.

More: after_seq=9.
`
	if got := out.LLMContent[0].Text; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := strings.Join(store.got, " "); got != "conversations/alice-vm/cA?after_seq=1" {
		t.Errorf("request: %s", got)
	}

	for in, want := range map[string]string{
		`{"action":"search"}`:                          "search needs a query",
		`{"action":"read_thread","conversation":"cA"}`: "SOURCE_VM/CONVERSATION_ID",
		`{"action":"delete"}`:                          "unknown action",
	} {
		if out := tool.Run(t.Context(), json.RawMessage(in)); out.Error == nil || !strings.Contains(out.Error.Error(), want) {
			t.Errorf("%s: %v", in, out.Error)
		}
	}
}
