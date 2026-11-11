package claudetool

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

// The archive_store tool researches prior work in the Shelley archive store
// (server/archive_research.go): what people wrote first, each piece with
// who wrote it. These are the store's research API's wire types, and the
// tool's compact rendering of them.

const ArchiveStoreName = "archive_store"

// ArchiveStore is the archive store as the tool reaches it. It is
// implemented by the server package.
type ArchiveStore interface {
	// ArchiveResearchEnabled reports whether the tool is offered: the
	// archive-research flag is on and the archiver has a store.
	ArchiveResearchEnabled(ctx context.Context) bool
	// ArchiveGet GETs the research API's path (e.g. "search") with query,
	// decoding its JSON reply into out.
	ArchiveGet(ctx context.Context, path string, query url.Values, out any) error
}

// Kinds of archived text, by who wrote it.
const (
	// ArchiveHuman is what a person typed, signed in through exe.dev as
	// AuthorEmail.
	ArchiveHuman = "human"
	// ArchiveUnattributed is a user-role message no sign-in was recorded
	// for: usually an agent or script posting through Shelley's API. Never
	// a verified person's statement.
	ArchiveUnattributed = "unattributed"
	// ArchiveAgent is an agent's final reply (machine-written).
	ArchiveAgent = "agent"
	// ArchiveTitle is a conversation's title, often machine-generated.
	ArchiveTitle = "title"
	// ArchiveInstructions is a project's instructions; their author isn't
	// recorded.
	ArchiveInstructions = "instructions"
)

// ArchiveConversation is an archived conversation: Ref is how the tool
// names it ("SOURCE_VM/CONVERSATION_ID").
type ArchiveConversation struct {
	Ref          string    `json:"ref"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Project      string    `json:"project"`
	SourceEmail  string    `json:"source_email"`
	Archived     bool      `json:"archived"`
	Subagent     bool      `json:"subagent"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	HumanAuthors []string  `json:"human_authors,omitempty"`
	HumanCount   int64     `json:"human_count"`
	// Opening is the start of its first user message, by OpeningKind and
	// OpeningAuthor.
	Opening       string `json:"opening,omitempty"`
	OpeningKind   string `json:"opening_kind,omitempty"`
	OpeningAuthor string `json:"opening_author,omitempty"`
}

// ArchiveAuthor is who wrote a piece of text: Kind, and for human text
// the exe.dev account, and Via "text" for a text message to the source's
// Assistant (its owner's).
type ArchiveAuthor struct {
	Kind   string `json:"kind"`
	Email  string `json:"email,omitempty"`
	UserID string `json:"user_id,omitempty"`
	Via    string `json:"via,omitempty"`
}

// ArchiveHit is a search match.
type ArchiveHit struct {
	ArchiveAuthor
	Conversation ArchiveConversation `json:"conversation"`
	SequenceID   int64               `json:"sequence_id"`
	CreatedAt    time.Time           `json:"created_at"`
	// Snippet marks matches **like this**.
	Snippet string `json:"snippet"`
}

// ArchiveSearchResult is a page of one tier's matches: agent replies
// (Agent), or else what people wrote, titles and instructions.
type ArchiveSearchResult struct {
	Agent  bool         `json:"agent"`
	Hits   []ArchiveHit `json:"hits"`
	Offset int          `json:"offset"`
	Total  int64        `json:"total"`
	// OtherTotal is the other tier's number of matches.
	OtherTotal int64 `json:"other_total"`
}

// ArchiveEntry is a thread entry: a message's text by its author, or
// (Kind "machine") a collapsed run of machine turns.
type ArchiveEntry struct {
	ArchiveAuthor
	SequenceID int64     `json:"sequence_id"`
	CreatedAt  time.Time `json:"created_at"`
	Text       string    `json:"text,omitempty"`
	// Truncated is how many characters of Text were left out.
	Truncated int `json:"truncated,omitempty"`
	// A machine run's agent messages, tool calls, and notices (tool
	// results aside: subagent and job reports, unattributed-less machine
	// messages), and its last final reply's sequence id and words.
	AgentMessages int   `json:"agent_messages,omitempty"`
	ToolCalls     int   `json:"tool_calls,omitempty"`
	Notices       int   `json:"notices,omitempty"`
	ReplySeq      int64 `json:"reply_seq,omitempty"`
	ReplyWords    int   `json:"reply_words,omitempty"`
}

// ArchiveMachine is the Kind of a collapsed run of machine turns.
const ArchiveMachine = "machine"

// ArchiveThread is a page of a conversation's thread.
type ArchiveThread struct {
	Conversation ArchiveConversation `json:"conversation"`
	Entries      []ArchiveEntry      `json:"entries"`
	// NextAfterSeq continues the thread; 0 at its end.
	NextAfterSeq int64 `json:"next_after_seq"`
}

// ArchiveConversations is a page of conversations.
type ArchiveConversations struct {
	Conversations []ArchiveConversation `json:"conversations"`
	Offset        int                   `json:"offset"`
	Total         int64                 `json:"total"`
}

// ArchiveProject is a project as a source pushed it.
type ArchiveProject struct {
	Name          string `json:"name"`
	SourceEmail   string `json:"source_email"`
	SourceVM      string `json:"source_vm"`
	Instructions  string `json:"instructions"`
	Conversations int64  `json:"conversations"`
}

// ArchiveProjects is every source's projects.
type ArchiveProjects struct {
	Projects []ArchiveProject `json:"projects"`
}

const archiveStoreDescription = `Research prior work in the team's Shelley archive store: conversations and projects that Shelleys pushed to it.
Use it to find earlier discussion, decisions and requests relevant to the current task.

Every result says who wrote what:
- "human": a person wrote it, signed in through exe.dev as the email shown (or "texted": sent from their phone to their Assistant). Only this is a person's statement.
- "unattributed": a user-role message with no sign-in recorded: usually another agent or a script posting through Shelley's API (task briefs, notes between tasks). Don't attribute it to a person.
- "agent": an agent's final reply, machine-written. Never present it as what a person said or decided.
- "title" (often machine-generated) and "instructions" (a project's, author not recorded).
Cite as author + date + conversation link, e.g. "bob@example.com, 2026-11-03, <link>".

Actions:
- search: full-text search (every word must match; words of 3+ letters match as prefixes) over what people wrote, titles and instructions, ranked toward human text and recent text. agent_replies=true searches agent replies instead.
- read_thread: a conversation's human messages in order, machine turns collapsed to one line each; expand=SEQ shows one message in full.
- list_conversations: recent conversations with who wrote in them.
- list_projects: the projects and their instructions.
Filters: author (part of an email), project, since/until (YYYY-MM-DD), conversation (a ref, SOURCE_VM/CONVERSATION_ID). Page with offset or after_seq as results say.`

const archiveStoreSchema = `{
  "type": "object",
  "required": ["action"],
  "properties": {
    "action": {"type": "string", "enum": ["search", "read_thread", "list_conversations", "list_projects"]},
    "query": {"type": "string", "description": "search: the words to find"},
    "agent_replies": {"type": "boolean", "description": "search: agent replies instead of human text"},
    "conversation": {"type": "string", "description": "a conversation ref, SOURCE_VM/CONVERSATION_ID: read_thread's, or search within it"},
    "author": {"type": "string", "description": "part of an author's email"},
    "project": {"type": "string", "description": "a project name"},
    "since": {"type": "string", "description": "YYYY-MM-DD, inclusive"},
    "until": {"type": "string", "description": "YYYY-MM-DD, exclusive"},
    "after_seq": {"type": "integer", "description": "read_thread: continue after this sequence id"},
    "expand": {"type": "integer", "description": "read_thread: show this sequence id's message in full"},
    "offset": {"type": "integer", "description": "search, list_conversations: skip this many results"},
    "limit": {"type": "integer", "description": "results per page (search 10, threads 30, lists 20 by default; at most 50)"}
  }
}`

type archiveStoreInput struct {
	Action       string `json:"action"`
	Query        string `json:"query"`
	AgentReplies bool   `json:"agent_replies"`
	Conversation string `json:"conversation"`
	Author       string `json:"author"`
	Project      string `json:"project"`
	Since        string `json:"since"`
	Until        string `json:"until"`
	AfterSeq     int64  `json:"after_seq"`
	Expand       int64  `json:"expand"`
	Offset       int    `json:"offset"`
	Limit        int    `json:"limit"`
}

// ArchiveStorePromptNote is the system prompt's note on the tool.
const ArchiveStorePromptNote = `
<archive_research>
The archive_store tool searches the team's Shelley archive store. Before substantial work, use it to research prior team discussion relevant to the task: earlier requests, decisions and attempts. Cite what you use as author + date + conversation link. Only text marked "human" is a person's statement; never treat agent-written or unattributed text as something a person said.
</archive_research>
`

// ArchiveStoreTool returns the archive_store tool, backed by store.
func ArchiveStoreTool(store ArchiveStore) *llm.Tool {
	return &llm.Tool{
		Name:        ArchiveStoreName,
		Description: archiveStoreDescription,
		InputSchema: llm.MustSchema(archiveStoreSchema),
		Run: llm.RunJSON(func(ctx context.Context, in archiveStoreInput) llm.ToolOut {
			text, err := runArchiveStore(ctx, store, in)
			if err != nil {
				return llm.ErrorToolOut(err)
			}
			return llm.ToolOut{LLMContent: llm.TextContent(text)}
		}),
	}
}

func runArchiveStore(ctx context.Context, store ArchiveStore, in archiveStoreInput) (string, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			q.Set(k, v)
		}
	}
	setInt := func(k string, v int64) {
		if v != 0 {
			q.Set(k, strconv.FormatInt(v, 10))
		}
	}
	set("author", in.Author)
	set("project", in.Project)
	set("since", in.Since)
	set("until", in.Until)
	setInt("offset", int64(in.Offset))
	setInt("limit", int64(in.Limit))
	switch in.Action {
	case "search":
		if strings.TrimSpace(in.Query) == "" {
			return "", fmt.Errorf("search needs a query")
		}
		set("q", in.Query)
		set("conversation", in.Conversation)
		if in.AgentReplies {
			q.Set("agent", "true")
		}
		var res ArchiveSearchResult
		if err := store.ArchiveGet(ctx, "search", q, &res); err != nil {
			return "", err
		}
		return FormatArchiveSearch(in.Query, res), nil
	case "read_thread":
		if strings.Count(in.Conversation, "/") != 1 {
			return "", fmt.Errorf("read_thread needs conversation, a ref like SOURCE_VM/CONVERSATION_ID")
		}
		setInt("after_seq", in.AfterSeq)
		setInt("expand", in.Expand)
		var res ArchiveThread
		if err := store.ArchiveGet(ctx, "conversations/"+in.Conversation, q, &res); err != nil {
			return "", err
		}
		return FormatArchiveThread(res), nil
	case "list_conversations":
		var res ArchiveConversations
		if err := store.ArchiveGet(ctx, "conversations", q, &res); err != nil {
			return "", err
		}
		return FormatArchiveConversations(res), nil
	case "list_projects":
		var res ArchiveProjects
		if err := store.ArchiveGet(ctx, "projects", q, &res); err != nil {
			return "", err
		}
		return FormatArchiveProjects(res), nil
	}
	return "", fmt.Errorf("unknown action %q", in.Action)
}

// ArchiveAuthorLabel says who wrote a piece of text in conversation c (or,
// for instructions, on the Shelley of sourceEmail), unambiguously.
func ArchiveAuthorLabel(a ArchiveAuthor, sourceEmail string) string {
	switch a.Kind {
	case ArchiveHuman:
		how := "human"
		if a.Via == "text" {
			how = "human, texted"
		}
		if a.UserID != "" {
			how += ", exe.dev user " + a.UserID
		}
		return fmt.Sprintf("%s (%s)", a.Email, how)
	case ArchiveUnattributed:
		return fmt.Sprintf("unattributed (no sign-in recorded on %s's Shelley: likely an agent or script, not a verified person)", sourceEmail)
	case ArchiveAgent:
		return fmt.Sprintf("agent (machine-written) in %s's conversation", sourceEmail)
	case ArchiveTitle:
		return fmt.Sprintf("title of %s's conversation (often machine-generated)", sourceEmail)
	case ArchiveInstructions:
		return fmt.Sprintf("project instructions on %s's Shelley (author not recorded)", sourceEmail)
	}
	return a.Kind
}

func archiveTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// archiveWhere names a conversation: title, project, source and link.
func archiveWhere(c ArchiveConversation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q", c.Title)
	if c.Project != "" {
		fmt.Fprintf(&b, " [project %s]", c.Project)
	}
	if c.Subagent {
		b.WriteString(" (subagent)")
	}
	if c.Archived {
		b.WriteString(" (archived)")
	}
	fmt.Fprintf(&b, " · %s's Shelley · %s", c.SourceEmail, c.Ref)
	if c.URL != "" {
		b.WriteString(" · " + c.URL)
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// FormatArchiveSearch renders search results for the agent.
func FormatArchiveSearch(query string, r ArchiveSearchResult) string {
	var b strings.Builder
	tier, other, otherHow := "what people wrote (and titles, instructions)", "agent replies", "agent_replies=true"
	if r.Agent {
		tier, other, otherHow = "agent replies (machine-written)", "what people wrote", "agent_replies=false"
	}
	fmt.Fprintf(&b, "Search %q in %s: %d matches", query, tier, r.Total)
	if len(r.Hits) > 0 {
		fmt.Fprintf(&b, ", showing %d-%d", r.Offset+1, r.Offset+len(r.Hits))
	}
	fmt.Fprintf(&b, ". %s: %d matches (%s).\n", other, r.OtherTotal, otherHow)
	for i, h := range r.Hits {
		fmt.Fprintf(&b, "\n%d. %s · %s\n", r.Offset+i+1, ArchiveAuthorLabel(h.ArchiveAuthor, h.Conversation.SourceEmail), archiveTime(h.CreatedAt))
		if h.Kind == ArchiveInstructions {
			fmt.Fprintf(&b, "   project %s\n", h.Conversation.Project)
		} else {
			fmt.Fprintf(&b, "   in %s, seq %d\n", archiveWhere(h.Conversation), h.SequenceID)
		}
		fmt.Fprintf(&b, "   %s\n", oneLine(h.Snippet))
	}
	if int64(r.Offset+len(r.Hits)) < r.Total {
		fmt.Fprintf(&b, "\nMore: offset=%d.\n", r.Offset+len(r.Hits))
	}
	return b.String()
}

// FormatArchiveThread renders a page of a thread for the agent.
func FormatArchiveThread(t ArchiveThread) string {
	var b strings.Builder
	c := t.Conversation
	fmt.Fprintf(&b, "Conversation %s, %s to %s.\n", archiveWhere(c), archiveTime(c.CreatedAt), archiveTime(c.UpdatedAt))
	b.WriteString("People's messages in order; machine turns collapsed (expand=SEQ shows one in full).\n")
	for _, e := range t.Entries {
		if e.Kind == ArchiveMachine {
			fmt.Fprintf(&b, "\n  [%s]\n", archiveMachineRun(e))
			continue
		}
		fmt.Fprintf(&b, "\n#%d %s · %s\n", e.SequenceID, ArchiveAuthorLabel(e.ArchiveAuthor, c.SourceEmail), archiveTime(e.CreatedAt))
		b.WriteString(indent(e.Text))
		if e.Truncated > 0 {
			fmt.Fprintf(&b, "\n  [%d more characters: expand=%d]", e.Truncated, e.SequenceID)
		}
		b.WriteByte('\n')
	}
	if t.NextAfterSeq != 0 {
		fmt.Fprintf(&b, "\nMore: after_seq=%d.\n", t.NextAfterSeq)
	}
	return b.String()
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

func archiveMachineRun(e ArchiveEntry) string {
	parts := []string{plural(e.AgentMessages, "message")}
	if e.ToolCalls > 0 {
		parts = append(parts, plural(e.ToolCalls, "tool call"))
	}
	if e.Notices > 0 {
		parts = append(parts, plural(e.Notices, "notice"))
	}
	if e.ReplySeq != 0 {
		parts = append(parts, fmt.Sprintf("final reply %s: expand=%d", plural(e.ReplyWords, "word"), e.ReplySeq))
	}
	return "agent: " + strings.Join(parts, ", ")
}

func plural(n int, s string) string {
	if n == 1 {
		return "1 " + s
	}
	return fmt.Sprintf("%d %ss", n, s)
}

// FormatArchiveConversations renders a page of conversations for the agent.
func FormatArchiveConversations(r ArchiveConversations) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d conversations", r.Total)
	if len(r.Conversations) > 0 {
		fmt.Fprintf(&b, ", showing %d-%d, most recently updated first", r.Offset+1, r.Offset+len(r.Conversations))
	}
	b.WriteString(".\n")
	for i, c := range r.Conversations {
		fmt.Fprintf(&b, "\n%d. %s\n   %s to %s", r.Offset+i+1, archiveWhere(c), archiveTime(c.CreatedAt), archiveTime(c.UpdatedAt))
		if len(c.HumanAuthors) > 0 {
			fmt.Fprintf(&b, "; %s by %s", plural(int(c.HumanCount), "human message"), strings.Join(c.HumanAuthors, ", "))
		} else {
			b.WriteString("; no signed-in human messages")
		}
		b.WriteByte('\n')
		if c.Opening != "" {
			fmt.Fprintf(&b, "   opens, by %s: %s\n", ArchiveAuthorLabel(ArchiveAuthor{Kind: c.OpeningKind, Email: c.OpeningAuthor}, c.SourceEmail), oneLine(c.Opening))
		}
	}
	if int64(r.Offset+len(r.Conversations)) < r.Total {
		fmt.Fprintf(&b, "\nMore: offset=%d.\n", r.Offset+len(r.Conversations))
	}
	return b.String()
}

// archiveInstructionsShown is how much of a project's instructions a list
// shows; search finds the rest.
const archiveInstructionsShown = 400

// FormatArchiveProjects renders the projects for the agent.
func FormatArchiveProjects(r ArchiveProjects) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d projects. Instructions' authors aren't recorded.\n", len(r.Projects))
	for _, p := range r.Projects {
		fmt.Fprintf(&b, "\n- %s on %s's Shelley (%s), %s\n", p.Name, p.SourceEmail, p.SourceVM, plural(int(p.Conversations), "conversation"))
		if ins := oneLine(p.Instructions); ins != "" {
			if r := []rune(ins); len(r) > archiveInstructionsShown {
				ins = string(r[:archiveInstructionsShown]) + "…"
			}
			fmt.Fprintf(&b, "  instructions: %s\n", ins)
		}
	}
	return b.String()
}
