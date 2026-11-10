package server

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func userSaid(text string, start, end float64) liveFragment {
	return liveFragment{user: true, text: text, startMs: start, endMs: end}
}

func liveSaid(text string, start, end float64) liveFragment {
	return liveFragment{text: text, startMs: start, endMs: end}
}

// utterance is liveUtterance over a whole call's speech, nothing claimed.
func utterance(all []liveFragment, offsetMs float64) string {
	var user []liveFragment
	for _, f := range all {
		if f.user {
			user = append(user, f)
		}
	}
	text, _, _ := liveUtterance(user, offsetMs, all)
	return text
}

func TestLiveSpeechJoinsTheUtteranceAndNormalizesSpace(t *testing.T) {
	for _, tt := range []struct {
		in   []liveFragment
		want string
	}{
		{[]liveFragment{userSaid("Please ", 1000, 1200), userSaid("fix the ", 1200, 1500), userSaid("tests", 1500, 1900)}, "Please fix the tests"},
		{[]liveFragment{userSaid("  one ", 0, 100), userSaid(" two  ", 100, 200)}, "one two"},
		{nil, ""},
	} {
		if got := liveSpeech(tt.in); got != tt.want {
			t.Errorf("liveSpeech = %q, want %q", got, tt.want)
		}
	}
}

func TestLiveUtterance(t *testing.T) {
	for _, tt := range []struct {
		name   string
		speech []liveFragment
		offset float64
		want   string
	}{
		{
			"the latest request and its late continuation",
			[]liveFragment{
				userSaid("Hi there.", 100, 900), liveSaid("Hello!", 1000, 1500),
				userSaid("Please inspect README", 6000, 6800), userSaid(" and summarize it.", 6900, 7800),
			},
			6800, "Please inspect README and summarize it.",
		},
		{"old small talk is not a task", []liveFragment{userSaid("Hi there.", 100, 900)}, 11_000, ""},
		{"no speech", nil, 0, ""},
		{
			"continuous speech is bounded to a minute",
			[]liveFragment{userSaid("one", 0, 1000), userSaid(" two", 4000, 59_000), userSaid(" three", 62_000, 63_000)},
			63_000, "two three",
		},
		// A real call: Live read out the backend's questions, the user
		// answered "New chats", then "Hello"; Live said it was handing that
		// off and delegated as it finished, seconds after the user stopped.
		{
			"Live acknowledges before delegating",
			[]liveFragment{
				liveSaid("Should they also start from a new or draft chat?", 0, 4000),
				userSaid("New chats", 9000, 9800), userSaid(" Hello", 12000, 12500),
				liveSaid("I'm here, handing those two decisions off to get built.", 13500, 17500),
			},
			17_600, "New chats Hello",
		},
		{
			"a reply that starts late doesn't bridge to an old greeting",
			[]liveFragment{userSaid("Hi.", 0, 500), liveSaid("Hello! What can I do?", 4000, 8000)},
			8100, "",
		},
		{
			"a clarification survives a delayed acknowledgement",
			[]liveFragment{
				liveSaid("Which bit's misbehaving?", 78000, 79600),
				userSaid("Yeah, it often uses ", 82600, 83600),
				userSaid("find double ", 83600, 84200),
				userSaid("dot", 84200, 84400),
				liveSaid("Got it, I'll pass that along.", 87800, 89800),
			},
			89900, "Yeah, it often uses find double dot",
		},
		{
			"a quick clarification keeps its request through a delayed acknowledgement",
			[]liveFragment{
				userSaid("Find the files that use parent dirs.", 74000, 77000),
				liveSaid("Which bit's misbehaving?", 77600, 79000),
				userSaid(" Yeah, it often uses find double dot", 81500, 83300),
				liveSaid("Okay, I'm looking at that now.", 86000, 88000),
			},
			88100, "Find the files that use parent dirs. Yeah, it often uses find double dot",
		},
		{
			"a clarification cannot bridge an indefinitely late reply",
			[]liveFragment{
				liveSaid("Which bit's misbehaving?", 78000, 79600),
				userSaid("Yeah, it often uses find double dot", 82600, 84400),
				liveSaid("What else can I help with?", 100000, 102000),
			},
			102100, "",
		},
		{
			"old Live speech does not make a greeting a clarification",
			[]liveFragment{
				liveSaid("Hello!", 0, 1000),
				userSaid("Hi.", 30000, 30500),
				liveSaid("What can I do?", 34000, 35000),
			},
			35100, "",
		},
		{
			"earlier user speech prevents treating a greeting as a reply to Live",
			[]liveFragment{
				liveSaid("Hello!", 0, 1000),
				userSaid("One moment.", 1500, 2000),
				userSaid("Hi.", 7100, 7500),
				liveSaid("What can I do?", 11000, 12000),
			},
			12100, "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := utterance(tt.speech, tt.offset); got != tt.want {
				t.Errorf("utterance = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLiveVoiceContext(t *testing.T) {
	sentences := make([]liveFragment, 8)
	var want []string
	for i := range sentences {
		sentences[i] = liveFragment{user: i%2 == 0, text: fmt.Sprintf(" sentence %d.", i), startMs: float64(i * 4000), endMs: float64(i*4000 + 500)}
		if i >= 2 {
			want = append(want, map[bool]string{true: "USER", false: "LIVE"}[i%2 == 0]+fmt.Sprintf(": sentence %d.", i))
		}
	}
	missed := []liveFragment{
		userSaid("Keep the message button on the left.", 0, 900),
		liveSaid("Sure, I'll keep that.", 1000, 1500),
		userSaid("Okay.", 2000, 2200),
		liveSaid("Anything else?", 2300, 2600),
		userSaid("Hmm.", 3000, 3200),
		liveSaid("Take your time.", 3300, 3600),
		userSaid("Can you delegate that?", 4000, 4600),
	}
	for _, tt := range []struct {
		name   string
		speech []liveFragment
		since  int
		want   string
	}{
		{
			"verbatim turns labelled by speaker, newest last",
			[]liveFragment{userSaid("there's a typo. ", 0, 500), userSaid(" in README", 500, 900), liveSaid("Which file?", 1000, 1500), userSaid("yes, that one", 2000, 2500)},
			4, exchange(1, 4, "USER: there's a typo.  in README\nLIVE: Which file?\nUSER: yes, that one"),
		},
		{
			"one line per turn, not per fragment",
			[]liveFragment{userSaid("Hello", 100, 400), userSaid(" there", 400, 700), liveSaid("Hi!", 900, 1200), userSaid("Check", 5000, 5300), userSaid(" README", 5300, 5700)},
			5, exchange(1, 5, "USER: Hello there\nLIVE: Hi!\nUSER: Check README"),
		},
		{"the last six turns", sentences, 8, exchange(3, 8, strings.Join(want, "\n"))},
		{
			"file names stay whole",
			[]liveFragment{userSaid("Wait.  I meant README.md, not readme.txt.", 0, 1000), liveSaid("Got it!", 1200, 1400)},
			2, exchange(1, 2, "USER: Wait.  I meant README.md, not readme.txt.\nLIVE: Got it!"),
		},
		{
			// A request Live acknowledged without delegating reaches the
			// next delegation; already delegated speech is left out.
			"everything since the previous delegation",
			append([]liveFragment{userSaid("Earlier task.", -2000, -1000)}, missed...),
			1, exchange(2, 8, "USER: Keep the message button on the left.\nLIVE: Sure, I'll keep that.\nUSER: Okay.\nLIVE: Anything else?\nUSER: Hmm.\nLIVE: Take your time.\nUSER: Can you delegate that?"),
		},
		{"at least the recent turns when nothing is new", missed[5:], 2, exchange(1, 2, "LIVE: Take your time.\nUSER: Can you delegate that?")},
		{"a delegation splits a turn", []liveFragment{userSaid("Run tests.", 0, 500), userSaid("Lint it.", 600, 900)}, 1, exchange(1, 2, "USER: Run tests.\nUSER: Lint it.")},
		{
			"a long fresh response stays whole",
			[]liveFragment{userSaid("Run tests.", 0, 500), liveSaid(strings.Repeat("界", 6000), 600, 7000)},
			0, exchange(1, 2, "USER: Run tests.\nLIVE: "+strings.Repeat("界", 6000)),
		},
		{
			"older context gives way to a long fresh exchange",
			[]liveFragment{liveSaid(strings.Repeat("old ", 2000), 0, 1000), userSaid("Run tests.", 2000, 2500), liveSaid(strings.Repeat("界", 6000), 2600, 7000)},
			1, exchange(2, 3, "USER: Run tests.\nLIVE: "+strings.Repeat("界", 6000)),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := liveVoiceContext(1, numbered(tt.speech...), tt.since); got != tt.want {
				t.Errorf("context =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
	long := liveVoiceContext(1, numbered(userSaid(strings.Repeat("a", 6000), 0, 1)), 1)
	if _, long, _ = strings.Cut(long, ":\n"); len(long) != liveContextChars || !strings.HasSuffix(long, "aaa") {
		t.Errorf("context is not bounded to its last %d chars: %d", liveContextChars, len(long))
	}
	mixed := liveVoiceContext(1, numbered(
		liveSaid(strings.Repeat("old ", 2000), 0, 1000),
		userSaid("Run tests.", 2000, 2500),
		liveSaid("I'll run them.", 2600, 3000),
	), 1)
	head, mixed, _ := strings.Cut(mixed, ":\n")
	if len(mixed) != liveContextChars || !strings.HasSuffix(mixed, "\nUSER: Run tests.\nLIVE: I'll run them.") || head != liveVoiceExchange+"1 Voice Exchange [Captions 1-3] (for context only)" {
		t.Error("older context must stay bounded without trimming the fresh exchange")
	}
}

func TestLiveEcho(t *testing.T) {
	const reply = "Shelley is working on that request now"
	for _, tt := range []struct {
		speech string
		start  float64
		want   bool
	}{
		{"Shelley is working on that request", 1000, true},
		{"Shelley is working on that request", 2000 - liveEchoTailMs, true},
		{"Shelley is working on that request", 2000 - liveEchoTailMs - 1, false},
		{"No, use the other file", 1000, false},
		{"Yes", 1000, false},
		// Echo is Live's playback picked up while it speaks, not speech
		// after it stopped: choosing an option by repeating Live's words
		// is the user's own.
		{"Shelley is working on that request", 4000 + liveEchoTailMs, true},
		{"Shelley is working on that request", 4000 + liveEchoTailMs + 1, false},
	} {
		if got := liveEcho(tt.speech, tt.start, liveReply{text: reply, startMs: 2000, endMs: 4000}); got != tt.want {
			t.Errorf("liveEcho(%q at %v) = %v", tt.speech, tt.start, got)
		}
	}
	if !liveRepeats("Yes, I can help with that today", "yes I can help with that today and more") {
		t.Error("a verbatim repeat is not echo")
	}
	if liveRepeats("Yes", "Yes, I can help with that") {
		t.Error("a short answer is echo")
	}
}

func TestLiveLatestReply(t *testing.T) {
	got := liveLatestReply([]liveFragment{
		liveSaid("Old.", 0, 500), userSaid("Hi", 1000, 1200),
		liveSaid("Earlier part.", 2000, 2500), liveSaid(" Hello", 6000, 6500), liveSaid(" there.", 6500, 7000),
		userSaid("um", 7100, 7200),
	})
	if got != (liveReply{text: " Hello there.", startMs: 6000, endMs: 7000}) {
		t.Errorf("latest reply = %+v", got)
	}
}

func TestLiveTruncate(t *testing.T) {
	if got := liveTruncate(strings.Repeat("é", 5000)); len([]rune(got)) != liveContentChars+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("not bounded: %d runes", len([]rune(got)))
	}
	if liveTruncate("short") != "short" {
		t.Error("short text changed")
	}
}

func dialogueRow(typ, text string, final bool, userData string) generated.Message {
	role := llm.MessageRoleUser
	if typ == "agent" {
		role = llm.MessageRoleAssistant
	}
	data, _ := json.Marshal(llm.Message{Role: role, Content: []llm.Content{llm.StringContent(text)}, EndOfTurn: final})
	raw := string(data)
	row := generated.Message{Type: typ, LlmData: &raw}
	if userData != "" {
		row.UserData = &userData
	}
	return row
}

func TestLiveDialogue(t *testing.T) {
	toolResult, _ := json.Marshal(llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolUseID: "tool", ToolResult: []llm.Content{llm.StringContent("TOOL OUTPUT")}}}})
	toolRaw := string(toolResult)
	spoken := dialogueRow("user", "Spoken request:\nlint it"+exchange(1, 2, "USER: lint it\nLIVE: Sure."), false, "")
	rows := []generated.Message{
		dialogueRow("user", "Please fix the tests", false, ""),
		dialogueRow("user", "carried request", false, `{"compaction_carried":"true"}`),
		dialogueRow("user", "SUBAGENT OUTPUT", false, `{"sender_conversation_id":"child","sender_relationship":"subagent"}`),
		dialogueRow("user", "BACKGROUND OUTPUT", false, `{"background_job_id":"job"}`),
		dialogueRow("user", "MCP NOTICE", false, `{"mcp_server_change":"added"}`),
		dialogueRow("agent", "internal progress", false, ""),
		{Type: "user", LlmData: &toolRaw},
		dialogueRow("agent", "Tests are fixed", true, ""),
		dialogueRow("error", "INTERNAL ERROR", false, ""),
		dialogueRow("user", "UNKNOWN MACHINE NOTICE", false, `{"future_notice":true}`),
		dialogueRow("user", "MALFORMED METADATA", false, `{broken`),
		dialogueRow("user", "NONOBJECT METADATA", false, `"unexpected"`),
		dialogueRow("user", "distilled", false, `{"distilled":"true"}`),
		spoken,
		dialogueRow("user", "Spoken request:\nqueued lint", false, `{"live_delivery_id":"delivery"}`),
	}
	var heard []string
	for _, row := range rows {
		user, text, _, err := liveDialogue(row)
		if err != nil {
			t.Fatal(err)
		}
		if text != "" {
			heard = append(heard, map[bool]string{true: "user", false: "assistant"}[user]+": "+text)
		}
	}
	want := "user: Please fix the tests|user: carried request|assistant: Tests are fixed|user: Spoken request:\nlint it|user: Spoken request:\nqueued lint"
	if got := strings.Join(heard, "|"); got != want {
		t.Errorf("Live hears %q, want %q", got, want)
	}
	if _, _, full, _ := liveDialogue(spoken); !strings.HasSuffix(full, "LIVE: Sure.") {
		t.Errorf("full text lost the exchange: %q", full)
	}
	bad := "not JSON"
	if _, _, _, err := liveDialogue(generated.Message{Type: "agent", LlmData: &bad}); err == nil {
		t.Error("undecodable message is not an error")
	}
}

// numbered numbers fragments as a call would, from 1.
func numbered(fragments ...liveFragment) []liveFragment {
	out := slices.Clone(fragments)
	for i := range out {
		out[i].n = i + 1
	}
	return out
}

// exchange is the voice context covering deltas from through to.
func exchange(from, to int, lines string) string {
	return fmt.Sprintf("%s1 Voice Exchange [Captions %d-%d] (for context only):\n%s", liveVoiceExchange, from, to, lines)
}
