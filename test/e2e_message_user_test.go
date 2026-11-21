package test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Ported from ui/e2e/message-user.spec.ts. The message_user tool:
// conversations with it show only the chat by default (the user's
// messages, the agent's message_user messages with their reply quotes and
// attachments, and the agent's reactions on the user's messages).

// messageUserConversation starts a conversation with message_user on in
// dir and returns it with its first user message's sequence ID.
func (s *shelley) messageUserConversation(dir string) (convo, int) {
	c := s.conversationWith(map[string]any{
		"message":              "Please check the build",
		"model":                "predictable",
		"cwd":                  dir,
		"conversation_options": map[string]any{"tool_overrides": map[string]string{"message_user": "on"}},
	}, "")
	var got struct {
		Messages []struct {
			Type       string `json:"type"`
			SequenceID int    `json:"sequence_id"`
		} `json:"messages"`
	}
	s.api("GET", "/api/conversation/"+c.ID, nil, &got)
	for _, m := range got.Messages {
		if m.Type == "user" {
			return c, m.SequenceID
		}
	}
	s.fail("conversation %s has no user message", c.ID)
	return c, 0
}

// messageUserChat sends message to a conversation and waits for the
// predictable model's turn to end.
func (s *shelley) messageUserChat(id, message string) {
	turns := func() int {
		var got struct {
			Messages []struct {
				EndOfTurn bool `json:"end_of_turn"`
			} `json:"messages"`
		}
		s.api("GET", "/api/conversation/"+id, nil, &got)
		n := 0
		for _, m := range got.Messages {
			if m.EndOfTurn {
				n++
			}
		}
		return n
	}
	before := turns()
	s.api("POST", "/api/conversation/"+id+"/chat", map[string]string{"message": message, "model": "predictable"}, nil)
	s.poll(30*time.Second, func() error {
		if turns() <= before {
			return errors.New("turn not finished yet")
		}
		return nil
	})
}

// messageUserCall is the message that makes the predictable model call
// message_user with input.
func messageUserCall(input map[string]any) string {
	return "message_user: " + mustJSON(input)
}

// messageUserFork forks a conversation and returns the fork.
func (s *shelley) messageUserFork(id string) convo {
	var fork struct {
		ID   string `json:"conversation_id"`
		Slug string `json:"slug"`
	}
	s.api("POST", "/api/conversation/"+id+"/fork", map[string]any{}, &fork)
	return convo{ID: fork.ID, Slug: fork.Slug}
}

// messageUserSeedCompaction writes a finished legacy compaction of a
// conversation straight into the server's database, as an old Shelley left
// it: a new generation that carries copies of every message.
func (s *shelley) messageUserSeedCompaction(id string) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.dir, "test.db"))
	s.check(err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA busy_timeout = 10000")
	s.check(err)
	type row struct {
		id, typ                                 string
		seq                                     int
		llm, user, display, usage, model, email sql.NullString
	}
	rs, err := db.Query(`SELECT message_id, sequence_id, type, llm_data, user_data, display_data, usage_data, model_name, user_email
		FROM messages WHERE conversation_id = ? AND generation = 1 ORDER BY sequence_id`, id)
	s.check(err)
	var rows []row
	for rs.Next() {
		var r row
		s.check(rs.Scan(&r.id, &r.seq, &r.typ, &r.llm, &r.user, &r.display, &r.usage, &r.model, &r.email))
		rows = append(rows, r)
	}
	s.check(rs.Err())
	rs.Close()
	if len(rows) == 0 {
		s.fail("conversation %s has no messages", id)
	}
	seq := rows[len(rows)-1].seq
	tx, err := db.Begin()
	s.check(err)
	defer tx.Rollback()
	add := func(r row, userData any, excluded int) {
		seq++
		var ud sql.NullString
		if userData != nil {
			ud = sql.NullString{String: mustJSON(userData), Valid: true}
		}
		_, err := tx.Exec(`INSERT INTO messages (message_id, conversation_id, sequence_id, type, llm_data, user_data,
			display_data, usage_data, excluded_from_context, generation, model_name, user_email)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 2, ?, ?)`,
			uuid.NewString(), id, seq, r.typ, r.llm, ud, r.display, r.usage, excluded, r.model, r.email)
		s.check(err)
	}
	text := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	agentText := func(t string) row {
		return row{typ: "agent", llm: text(mustJSON(map[string]any{"Role": 1, "Content": []any{map[string]any{"Type": 2, "Text": t}}}))}
	}
	status := func(st string) map[string]string {
		return map[string]string{"distill_method": "compact", "distill_status": st, "new_generation": "true"}
	}
	const summary = "Earlier, the user asked for a build check."
	add(agentText("Distilling conversation…"), status("in_progress"), 1)
	for _, r := range rows {
		if r.typ == "system" {
			add(r, nil, 0)
		}
	}
	add(row{typ: "user", llm: text(mustJSON(map[string]any{"Role": 0, "Content": []any{map[string]any{"Type": 2, "Text": summary}}}))},
		map[string]string{"distilled": "true", "distill_method": "compact", "distillation_content": summary}, 0)
	for _, r := range rows {
		if r.typ == "system" {
			continue
		}
		ud := map[string]any{}
		if r.user.Valid && r.user.String != "" {
			s.check(json.Unmarshal([]byte(r.user.String), &ud))
		}
		ud["carried_from_sequence_id"] = strconv.Itoa(r.seq)
		ud["compaction_carried"] = "true"
		add(r, ud, 0)
	}
	add(agentText("Distillation complete"), status("complete"), 1)
	_, err = tx.Exec("UPDATE conversations SET current_generation = 2 WHERE conversation_id = ?", id)
	s.check(err)
	s.check(tx.Commit())
}

// messageUserView names the conversation view options the tests check.
const messageUserView = `"Open the view options" means: click ".chat-overflow-menu-wrapper .btn-icon"; "the view options" is the element with data-testid "conversation-view-toggle", and its buttons are picked by role "button" and name (with "within" it). `

func TestE2EMessageUserShowsChat(t *testing.T) {
	e2e(t, `Conversations with the message_user tool show the chat: the user's messages, the agent's message_user messages with their reply quotes and attachments, and its reactions; "See All" shows everything and sticks for such conversations, other conversations keep their own setting. The conversation /c/{{SLUG}} has message_user on and runs in a directory holding chart.png (a 40x30 PNG) and report.txt ("the report"). The user asked "Please check the build" (the predictable model's own text reply to it begins "edit predictable.go"); the agent then called message_user three times: "Looking now." replying to that message with the reaction 👀; "Here are the results." with the attachments chart.png and report.txt; and a refused call replying to a message that does not exist (its failure reads "message_user failed"). Call "fork" forks the conversation as /c/message-user-fork and has the agent react 🍴 there to the copy of the first message, through message_user. /c/{{PLAIN}} is an ordinary conversation ("Hello"). "The bubbles" are the elements with data-testid "message-user-bubble". "The chat area" is ".messages-container" (not the page: the drawer previews other conversations); "the chat area's text T" is the element inside it holding T (selector "*" with has_text T, "within" ".messages-container"). "The asked message" is the first element with data-testid "message" whose text contains "Please check the build". `+messageUserView+`1. Navigate to /c/{{SLUG}}. 2. There should be exactly 2 bubbles. 3. The "[data-testid=\"message-user-text\"]" elements inside the bubbles should read, in order, exactly ["Looking now.", "Here are the results."] (assert_texts). 4. The chat area must hold exactly 0 elements with the text "edit predictable.go" and exactly 0 with the text "message_user failed": the agent's own text and the refused call stay out of the chat. 5. The asked message's element with data-testid "message-reactions" should read exactly "👀". 6. The image attachment previews: eval (() => { const b = document.querySelectorAll('[data-testid="message-user-bubble"]'); const img = b[b.length - 1].querySelector('.message-user-image img'); return img ? img.naturalWidth : -1; })() until it returns exactly "40". 7. The bubble containing "Here are the results." has an element with data-testid "message-user-file" that should contain "report.txt". 8. That file link downloads the file: eval (async () => { const b = document.querySelectorAll('[data-testid="message-user-bubble"]'); const a = b[b.length - 1].querySelector('[data-testid="message-user-file"]'); const r = await fetch(a.getAttribute('href')); return r.ok + ' ' + await r.text(); })(), which must return exactly "true the report". 9. Under reduced motion the highlight animation ends at once and takes its class along, so watch for the class: in one eval without "expect", on the asked message element, start a MutationObserver on its class attribute that sets el.dataset.highlighted = "1" whenever el.classList contains "message-highlight". 10. Click the element with data-testid "message-user-quote" in the bubble containing "Looking now.". 11. The asked message should get the attribute data-highlighted exactly "1": the quote jumps to the message it replies to. 12. Open the view options. 13. The view options' button named "See Chat Only" should have aria-pressed exactly "true". 14. The view options must hold exactly 0 buttons named "See End of Turn Messages Only". 15. Click the view options' button named "See All". 16. It should have aria-pressed exactly "true". 17. Press "Escape". 18. The chat area's text "message_user failed" should be visible, and so should its text "edit predictable.go". 19. There should still be exactly 2 bubbles. 20. Reload. 21. The chat area's text "message_user failed" should be visible: See All stuck. 22. Call "fork"; it returns "forked". 23. Navigate to /c/message-user-fork. A fork copies the messages with new IDs: reactions and quotes still find their targets, and new calls target the fork's copies. 24. Eval (() => { const m = [...document.querySelectorAll('[data-testid="message"]')].find((el) => el.textContent.includes('Please check the build')); const r = m && m.querySelector('[data-testid="message-reactions"]'); return !!r && /👀.*🍴/s.test(r.textContent); })() until it returns true. 25. The first element with data-testid "message-user-quote" should be enabled. 26. Navigate to /c/{{PLAIN}}. 27. Open the view options. 28. The view options' button named "See All" should have aria-pressed exactly "true". 29. The view options must hold exactly 0 buttons named "See Chat Only".`, func(s *shelley) {
		dir := s.tempDir("shelley-message-user-")
		s.write(filepath.Join(dir, "chart.png"), string(makePNG(40, 30)))
		s.write(filepath.Join(dir, "report.txt"), "the report")
		c, first := s.messageUserConversation(dir)
		s.messageUserChat(c.ID, messageUserCall(map[string]any{"text": "Looking now.", "reply_to": first, "reaction": "👀"}))
		s.messageUserChat(c.ID, messageUserCall(map[string]any{"text": "Here are the results.", "attachments": []string{"chart.png", "report.txt"}, "end_turn": true}))
		s.messageUserChat(c.ID, messageUserCall(map[string]any{"text": "x", "reply_to": 99999999}))
		s.v("SLUG", c.Slug)
		s.v("PLAIN", s.conversation("Hello", "").Slug)
		s.call("fork", func() string {
			fork := s.messageUserFork(c.ID)
			s.messageUserChat(fork.ID, messageUserCall(map[string]any{"reply_to": first, "reaction": "🍴"}))
			s.api("POST", "/api/conversation/"+fork.ID+"/rename", map[string]string{"slug": "message-user-fork"}, nil)
			return "forked"
		})
	})
}

// After a (legacy) compaction, a fork has only the carried copies of the
// user's messages: calls made before still find them, new calls can target
// them, and jumping to one expands the band it is collapsed behind.
func TestE2EMessageUserTargetsSurviveCompaction(t *testing.T) {
	e2e(t, `message_user targets survive compaction and forks. A conversation with message_user on asked "Please check the build"; the agent replied "Looking now." to it with the reaction 👀, through message_user. Then the conversation was compacted (the old way: a new generation carrying copies of its messages) and forked twice, as /c/{{FORK}} and as the conversation with ID {{OTHER}} and slug {{OTHERSLUG}}. In the first fork the agent then replied "Still on it." to the original first message, through message_user. "The band" is the element with data-testid "carried-band" (the carried messages, collapsed). 1. Navigate to /c/{{FORK}}. 2. There should be exactly 1 band. 3. It must hold exactly 0 elements with data-testid "message" (collapsed). 4. In one eval without "expect", watch for highlights: new MutationObserver((records) => { for (const r of records) { const el = r.target; if (el.classList && el.classList.contains('message-highlight')) document.body.dataset.highlighted = el.innerText; } }).observe(document.body, { subtree: true, attributes: true, attributeFilter: ['class'] }). 5. Click the element with data-testid "message-user-quote" inside the element with data-testid "message-user-bubble" containing "Still on it.". 6. The band's element with data-testid "message" containing "Please check the build" (within the band, has_text) should be visible: jumping expanded the band. 7. Eval (document.body.dataset.highlighted || '').includes('Please check the build') until it returns true. 8. That message's element with data-testid "message-reactions" should read exactly "👀". 9. Click the conversation list's '.conversation-item[data-conversation-id="{{OTHER}}"]': the band expanded here does not expand the same band in another conversation. 10. The URL should contain "{{OTHERSLUG}}". 11. The band's button (role "button", within the band) should have aria-expanded exactly "false".`, func(s *shelley) {
		// Wide enough for the conversation list.
		s.viewport(1280, 800)
		c, first := s.messageUserConversation(s.cwd)
		s.messageUserChat(c.ID, messageUserCall(map[string]any{"text": "Looking now.", "reply_to": first, "reaction": "👀"}))
		s.messageUserSeedCompaction(c.ID)
		fork := s.messageUserFork(c.ID)
		other := s.messageUserFork(c.ID)
		s.messageUserChat(fork.ID, messageUserCall(map[string]any{"text": "Still on it.", "reply_to": first}))
		s.v("FORK", fork.Slug)
		s.v("OTHER", other.ID)
		s.v("OTHERSLUG", other.Slug)
	})
}
