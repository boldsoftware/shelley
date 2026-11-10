package server

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// The rules that turn a Live call's speech into backend tasks. GPT-Live
// emits transcript fragments without turn boundaries; these recover the
// request a delegation is about, the exchange it came from, and what of
// the conversation Live hears.

// liveFragment is one transcript delta, timed in ms from the start of the
// Live session.
type liveFragment struct {
	n              int  // cumulative delta ordinal, unaffected by compaction
	user           bool // the caller; else Live
	text           string
	startMs, endMs float64
}

const (
	// liveUserLeadMs: a delegation counts the user's speech when it follows
	// within this long, of the user or of Live's answer to them.
	liveUserLeadMs = 2500
	// Replies in an ongoing exchange can take longer to start.
	liveResponseGapMs = 5000
	// liveEchoTailMs: echo starts while Live speaks or this soon after.
	liveEchoTailMs         = 1500
	liveContextTurns       = 6
	liveContextChars       = 5000
	liveContentChars       = 1200 // GPT-Live appends accept up to 500 tokens
	liveLatestReplyChars   = 4000
	liveReplyPauseMs       = 3000
	liveUtteranceGapMs     = 5000
	liveUtteranceMaxSpanMs = 60_000
)

// liveSpeech joins fragments and normalizes their whitespace.
func liveSpeech(fragments []liveFragment) string {
	var b strings.Builder
	for _, f := range fragments {
		b.WriteString(f.text)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// liveUtterance is the speech a delegation at offsetMs stands for, and when
// it started; ok is false when none of the user's speech leads up to it.
// user is the user's speech not yet claimed by a delegation, all the
// call's speech.
//
// The user's latest continuous speech counts when the delegation follows it
// within 2.5 s, or follows Live's reply to it: Live often acknowledges first
// ("Got it, handing that off") and delegates as it finishes, seconds after
// the user stopped. A reply that starts more than 2.5 s after the user
// stopped only bridges in an ongoing exchange, not from an old greeting.
func liveUtterance(user []liveFragment, offsetMs float64, all []liveFragment) (text string, startMs float64, ok bool) {
	user = liveRecentUser(user)
	if len(user) == 0 {
		return "", 0, false
	}
	if liveTurnEnd(user, all)+liveUserLeadMs < offsetMs {
		return "", 0, false
	}
	text = liveSpeech(user)
	return text, user[0].startMs, text != ""
}

func liveRecentUser(user []liveFragment) []liveFragment {
	if len(user) == 0 {
		return user
	}
	last := user[len(user)-1]
	first := len(user) - 1
	for first > 0 {
		before, current := user[first-1], user[first]
		if current.startMs-before.endMs > liveUtteranceGapMs || last.endMs-before.startMs > liveUtteranceMaxSpanMs {
			break
		}
		first--
	}
	return user[first:]
}

// liveTurnEnd extends the user's speech through Live's answer. Allow a
// longer response pause when the user was replying to Live, but don't
// revive an isolated greeting with unrelated later output. The user's
// speech can span an intervening clarification question.
func liveTurnEnd(user, all []liveFragment) float64 {
	end := user[len(user)-1].endMs
	gap := float64(liveUserLeadMs)
	for _, u := range user {
		var preceding *liveFragment
		for _, f := range all {
			if f.startMs < u.startMs && f.endMs > f.startMs && strings.TrimSpace(f.text) != "" &&
				(preceding == nil || f.endMs > preceding.endMs) {
				preceding = &f
			}
		}
		if preceding != nil && !preceding.user && u.startMs-preceding.endMs <= liveResponseGapMs {
			gap = liveResponseGapMs
			break
		}
	}
	var live []liveFragment
	for _, f := range all {
		if !f.user && f.endMs > end {
			live = append(live, f)
		}
	}
	slices.SortStableFunc(live, func(a, b liveFragment) int { return cmp.Compare(a.startMs, b.startMs) })
	for _, f := range live {
		if f.startMs-end > gap {
			break
		}
		end = max(end, f.endMs)
		gap = liveUserLeadMs
	}
	return end
}

// liveVoiceContext is the exchange since the previous delegation (fragments
// from index since on), and at least the last few turns, labelled by
// speaker with fragments kept verbatim, under a heading with the captions
// (transcript deltas, as numbered in the saved transcript) it covers. Covering everything since the
// previous delegation carries a request Live acknowledged without
// delegating into the next delegation.
func liveVoiceContext(call int64, all []liveFragment, since int) string {
	type turn struct {
		user bool
		text string
		last int
		from int // the turn's deltas, as numbered in the saved transcript
		to   int
	}
	var turns []turn
	for i, f := range all {
		if n := len(turns); n > 0 && turns[n-1].user == f.user && i != since {
			turns[n-1].text += f.text
			turns[n-1].last, turns[n-1].to = i, f.n
		} else {
			turns = append(turns, turn{f.user, f.text, i, f.n, f.n})
		}
	}
	fresh := len(turns)
	for i, t := range turns {
		if t.last >= since {
			fresh = i
			break
		}
	}
	first := max(0, min(fresh, len(turns)-liveContextTurns))
	lines := make([]string, 0, len(turns))
	for _, t := range turns[first:] {
		label := "LIVE: "
		if t.user {
			label = "USER: "
		}
		lines = append(lines, label+strings.TrimSpace(t.text))
	}
	context := strings.Join(lines, "\n")
	limit := max(liveContextChars, utf8.RuneCountInString(strings.Join(lines[fresh-first:], "\n")))
	if r := []rune(context); len(r) > limit {
		context = string(r[len(r)-limit:])
	}
	if context == "" {
		return ""
	}
	kept := turns[len(turns)-1-strings.Count(context, "\n"):]
	return fmt.Sprintf("%s%d Voice Exchange [Captions %d-%d] (for context only):\n%s", liveVoiceExchange, call, kept[0].from, kept[len(kept)-1].to, context)
}

var liveWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// liveEcho reports whether speech is likely Live's own reply picked up by
// the microphone: a near-verbatim run of it that started while Live spoke,
// or just after (allowing for transcript timing). Speech that begins well
// after Live finished is the user's own, even when it repeats Live's
// words, e.g. choosing an option Live just read out. Echo cancellation
// runs too; this is a second guard.
func liveEcho(speech string, startMs float64, reply liveReply) bool {
	return startMs >= reply.startMs-liveEchoTailMs && startMs <= reply.endMs+liveEchoTailMs && liveRepeats(speech, reply.text)
}

// liveRepeats reports whether one text's words, four or more, appear
// verbatim in the other's.
func liveRepeats(speech, reply string) bool {
	input := liveWord.FindAllString(strings.ToLower(speech), -1)
	output := liveWord.FindAllString(strings.ToLower(reply), -1)
	if len(input) < 4 || len(output) < 4 {
		return false
	}
	short, long := input, output
	if len(input) > len(output) {
		short, long = output, input
	}
	s := strings.Join(short, " ")
	return utf8.RuneCountInString(s) >= 20 && strings.Contains(strings.Join(long, " "), s)
}

type liveReply struct {
	text           string
	startMs, endMs float64
}

// liveLatestReply is Live's latest run of speech, stopping at a pause of
// more than three seconds.
func liveLatestReply(all []liveFragment) liveReply {
	var reply liveReply
	for i := len(all) - 1; i >= 0; i-- {
		f := all[i]
		if f.user {
			if reply.text != "" {
				break
			}
			continue
		}
		if reply.text != "" && reply.startMs-f.endMs > liveReplyPauseMs {
			break
		}
		if reply.text == "" {
			reply.endMs = f.endMs
		}
		reply.text = f.text + reply.text
		reply.startMs = f.startMs
	}
	if r := []rune(reply.text); len(r) > liveLatestReplyChars {
		reply.text = string(r[len(r)-liveLatestReplyChars:])
	}
	return reply
}

// liveTruncate bounds what is appended to Live.
func liveTruncate(text string) string {
	if utf8.RuneCountInString(text) <= liveContentChars {
		return text
	}
	return string([]rune(text)[:liveContentChars]) + "…"
}

// liveMessageText is a message's visible text: its text blocks, joined.
func liveMessageText(message llm.Message) string {
	var text []string
	for _, c := range message.Content {
		if c.Type == llm.ContentTypeText && c.Text != "" {
			text = append(text, c.Text)
		}
	}
	return strings.TrimSpace(strings.Join(text, "\n"))
}

// liveDialogue is what Live hears of a conversation row: the user's typed
// (or delegated) requests, without the voice exchange a delegation carries,
// and the backend's final replies. Tool results, subagent reports,
// background jobs and other machine notices, progress and errors are not
// dialogue (text ""). full is the row's whole visible text.
func liveDialogue(row generated.Message) (user bool, text, full string, err error) {
	if row.LlmData == nil || (row.Type != "user" && row.Type != "agent") {
		return false, "", "", nil
	}
	var message llm.Message
	if err := json.Unmarshal([]byte(*row.LlmData), &message); err != nil {
		return false, "", "", fmt.Errorf("decode message %s: %w", row.MessageID, err)
	}
	full = liveMessageText(message)
	if row.Type == "agent" {
		if !message.EndOfTurn {
			return false, "", full, nil
		}
		return false, full, full, nil
	}
	if !typedUserForLLM(message, []byte(derefString(row.UserData))) {
		return true, "", full, nil
	}
	// The exchange stays visible in chat and in the backend's request, but
	// replaying it to Live would attribute Live's own speech to the user.
	task, _, _ := strings.Cut(full, liveVoiceExchange)
	task, _, _ = strings.Cut(task, liveVoiceExchangeUnnumbered)
	task, _, _ = strings.Cut(task, liveTranscriptMarker)
	return true, strings.TrimSpace(task), full, nil
}
