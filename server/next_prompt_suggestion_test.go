package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func newSuggestionHarness(t *testing.T, on bool) *TestHarness {
	t.Helper()
	h := NewTestHarness(t)
	h.convOpts = &db.ConversationOptions{DisableNotifications: true}
	// setupTestDB turned the flag off; with on, the default applies.
	if on {
		if err := h.db.DeleteFeatureFlagOverride(t.Context(), FlagNextPromptSuggestions.Name); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// suggestionState returns the conversation's suggestion columns.
func suggestionState(t *testing.T, h *TestHarness) (string, int64) {
	t.Helper()
	conv, err := h.db.GetConversationByID(t.Context(), h.convID)
	if err != nil {
		t.Fatal(err)
	}
	return conv.NextPromptSuggestion, conv.NextPromptSuggestionSeq
}

// lastTurnEnd returns the sequence id of the newest end-of-turn agent message.
func lastTurnEnd(t *testing.T, h *TestHarness) int64 {
	t.Helper()
	var seq int64
	for _, row := range listMessages(t, h.db, h.convID) {
		if row.Type != string(db.MessageTypeAgent) || row.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.EndOfTurn {
			seq = row.SequenceID
		}
	}
	return seq
}

func suggestionUsage(t *testing.T, h *TestHarness) []generated.Message {
	t.Helper()
	var markers []generated.Message
	for _, row := range listMessages(t, h.db, h.convID) {
		if row.OtherUsageData != nil && strings.Contains(*row.OtherUsageData, `"purpose":"`+nextPromptSuggestionPurpose+`"`) {
			markers = append(markers, row)
		}
	}
	return markers
}

func suggestionRequests(h *TestHarness) []*llm.Request {
	var out []*llm.Request
	for _, req := range h.llm.GetRecentRequests() {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == llm.MessageRoleUser && len(last.Content) == 1 && last.Content[0].Text == nextPromptSuggestionPrompt {
			out = append(out, req)
		}
	}
	return out
}

// The suggestion request is the next turn's prefix, the last request of the
// turn exactly as sent and its reply, plus the prompt, so it reads the cache.
func TestNextPromptSuggestionFollowsEachTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		defer stopActiveConversationLoops(h.server)
		h.NewConversation("echo: water the basil", "")
		synctest.Wait()

		if text, seq := suggestionState(t, h); text != "echo: More about water the basil" || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want it after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
		reqs := suggestionRequests(h)
		if len(reqs) != 1 {
			t.Fatalf("suggestion requests = %d, want 1", len(reqs))
		}
		suggestion := reqs[0]
		prefix := suggestion.Messages[:len(suggestion.Messages)-2]
		var turn *llm.Request
		for _, req := range h.llm.GetRecentRequests() {
			if req != suggestion && reflect.DeepEqual(req.Messages, prefix) {
				turn = req
			}
		}
		if turn == nil || !reflect.DeepEqual(turn.System, suggestion.System) || !reflect.DeepEqual(turn.Tools, suggestion.Tools) ||
			turn.ThinkingLevel != suggestion.ThinkingLevel {
			t.Fatal("suggestion request does not extend the turn's last request")
		}
		if reply := suggestion.Messages[len(suggestion.Messages)-2]; reply.Role != llm.MessageRoleAssistant || reply.Content[0].Text != "water the basil" {
			t.Fatalf("suggestion request does not carry the turn's reply: %+v", reply)
		}
		markers := suggestionUsage(t, h)
		if len(markers) != 1 || markers[0].Type != string(db.MessageTypeSlug) {
			t.Fatalf("suggestion usage markers = %+v, want 1", markers)
		}

		h.Chat("echo: no suggestion")
		synctest.Wait()
		if text, seq := suggestionState(t, h); text != "" || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want none after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}

		h.Chat("echo: prune the rosemary")
		synctest.Wait()
		if text, seq := suggestionState(t, h); text != "echo: More about prune the rosemary" || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want it after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
		if got := len(suggestionUsage(t, h)); got != 3 {
			t.Fatalf("suggestion usage markers = %d, want 3", got)
		}
	})
}

func TestNextPromptSuggestionNeedsFlag(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, false)
		defer stopActiveConversationLoops(h.server)
		h.NewConversation("echo: water the basil", "")
		synctest.Wait()
		if text, seq := suggestionState(t, h); text != "" || seq != 0 {
			t.Fatalf("suggestion = %q at %d with the flag off", text, seq)
		}
		if n := len(suggestionRequests(h)); n != 0 {
			t.Fatalf("suggestion requests = %d with the flag off", n)
		}
	})
}

// A new message cancels the suggestion for the turn before it.
func TestNextPromptSuggestionCancelledByActivity(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		defer stopActiveConversationLoops(h.server)
		h.NewConversation("echo: water the basil", "")
		synctest.Wait()

		h.llm.SetResponseDelay(20 * time.Second)
		h.Chat("echo: prune the rosemary")
		<-time.After(25 * time.Second)
		synctest.Wait()
		if len(suggestionRequests(h)) != 2 {
			t.Fatal("expected the second suggestion request in flight")
		}
		h.Chat("echo: repot the mint")
		<-time.After(time.Minute)
		synctest.Wait()

		if text, seq := suggestionState(t, h); text != "echo: More about repot the mint" || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want it after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
		if got := len(suggestionUsage(t, h)); got != 2 {
			t.Fatalf("suggestion usage markers = %d, want 2: the cancelled one records nothing", got)
		}
	})
}

// A turn that message_user ends has no final reply; the suggestion request
// carries the tool call and its result instead, as the next turn would.
func TestNextPromptSuggestionAfterToolEndedTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		h.convOpts.ToolOverrides = map[string]string{claudetool.MessageUserName: "on"}
		defer stopActiveConversationLoops(h.server)
		h.NewConversation(`message_user: {"text":"Watered the basil.","end_turn":true}`, "")
		synctest.Wait()

		if text, seq := suggestionState(t, h); text == "" || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want one after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
		reqs := suggestionRequests(h)
		if len(reqs) != 1 {
			t.Fatalf("suggestion requests = %d, want 1", len(reqs))
		}
		msgs := reqs[0].Messages
		call, result := msgs[len(msgs)-3], msgs[len(msgs)-2]
		if call.Content[0].Type != llm.ContentTypeToolUse || call.Content[0].ToolName != claudetool.MessageUserName ||
			result.Content[0].Type != llm.ContentTypeToolResult || result.Content[0].ToolUseID != call.Content[0].ID {
			t.Fatalf("suggestion request does not end with the tool call and its result: %+v, %+v", call, result)
		}
	})
}

// Queued work starts another turn as soon as this one ends, so a suggestion
// for this one would be wasted.
func TestNextPromptSuggestionSkipsQueuedWork(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		defer stopActiveConversationLoops(h.server)
		h.NewConversation("echo: water the basil", "")
		synctest.Wait()
		cm, err := h.server.getOrCreateConversationManager(t.Context(), h.convID, "")
		if err != nil {
			t.Fatal(err)
		}

		h.llm.SetResponseDelay(10 * time.Second)
		h.Chat("echo: prune the rosemary")
		synctest.Wait()
		if err := cm.QueueMessage(t.Context(), "predictable", llm.UserStringMessage("echo: repot the mint")); err != nil {
			t.Fatal(err)
		}
		<-time.After(time.Minute)
		synctest.Wait()

		if n := len(suggestionRequests(h)); n != 2 {
			t.Fatalf("suggestion requests = %d, want 2: none for the turn the queued message followed", n)
		}
		if text, seq := suggestionState(t, h); text != "echo: More about repot the mint" || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want it after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
	})
}

// A chat's messages, web chats' included, are answered in their chat, whose
// composer offers no suggestion.
func TestNextPromptSuggestionSkipsChannels(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		defer stopActiveConversationLoops(h.server)
		mux := http.NewServeMux()
		h.server.RegisterRoutes(mux)
		ev := channelEvent{Type: "message", ID: "m1", ChatID: "web-usr1", Web: true, Text: "echo: water the basil"}
		if w := postJSON(t, mux, "/api/channels/messages", ev); w.Code != http.StatusNoContent {
			t.Fatalf("deliver: status %d: %s", w.Code, w.Body.String())
		}
		synctest.Wait()

		if route, err := h.db.LatestChannelRoute(t.Context(), "web-usr1"); err != nil || route.ConversationID != currentAssistant(t, h.server) {
			t.Fatalf("web chat route: %+v %v", route, err)
		}
		if n := len(suggestionRequests(h)); n != 0 {
			t.Fatalf("suggestion requests = %d, want 0", n)
		}
	})
}

// The Assistant's texted messages are answered by text, and typed ones in
// its composer.
func TestNextPromptSuggestionSkipsTextedAssistantTurns(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		defer stopActiveConversationLoops(h.server)
		id, err := h.server.ensureAssistant(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		h.convID = id
		mux := http.NewServeMux()
		h.server.RegisterRoutes(mux)
		// The debug chat's gateway is on mux. Reach it in process: dialing
		// its http://localhost endpoint would resolve localhost in the
		// bubble, which can kill the test binary (golang/go#80903).
		h.server.channelClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			return w.Result(), nil
		})}
		if w := postJSON(t, mux, "/api/debug/channels/debug-a/receive", map[string]string{"text": "echo: water the basil"}); w.Code != http.StatusOK {
			t.Fatalf("receive: status %d: %s", w.Code, w.Body.String())
		}
		synctest.Wait()
		h.server.channelDebug.mu.Lock()
		typing := h.server.channelDebug.chats["debug-a"].Typing
		h.server.channelDebug.mu.Unlock()
		if !typing {
			t.Fatal("the texted turn showed no typing indicator in its chat")
		}
		if n := len(suggestionRequests(h)); n != 0 {
			t.Fatalf("after a texted message, suggestion requests = %d, want 0", n)
		}

		h.Chat("echo: prune the rosemary")
		synctest.Wait()
		if text, seq := suggestionState(t, h); text != "echo: More about prune the rosemary" || seq != lastTurnEnd(t, h) {
			t.Fatalf("after a typed message, suggestion = %q at %d, want one after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
	})
}

func TestParseNextPromptSuggestion(t *testing.T) {
	t.Parallel()
	for reply, want := range map[string]string{
		`{"suggestion":"  Add a regression test  "}`:                    "Add a regression test",
		`{"suggestion":null}`:                                           "",
		`{"suggestion":"` + strings.Repeat("é", 240) + `"}`:             strings.Repeat("é", 240),
		`{"suggestion":"` + strings.Repeat("é", 241) + `"}`:             "error",
		`{"suggestion":"one\ntwo"}`:                                     "error",
		`{"suggestion":"one` + "\u2028" + `two"}`:                       "error",
		`{"suggestion":3}`:                                              "error",
		"```json\n{\"suggestion\":\"Add a regression test\"}\n```":      "Add a regression test",
		"\n```\n{\"suggestion\":\"Add a regression test\"}\n```\n":      "Add a regression test",
		"```JSON\n{\"suggestion\":\"Add a regression test\"}```":        "Add a regression test",
		"``` json\r\n{\"suggestion\":\"Add a regression test\"}\r\n```": "Add a regression test",
		"```json {\"suggestion\":\"Add a regression test\"} ```":        "Add a regression test",
		"```{\"suggestion\":\"Add a regression test\"}```":              "Add a regression test",
		"~~~json\n{\"suggestion\":\"Add a regression test\"}\n~~~":      "Add a regression test",
		"````json\n{\"suggestion\":\"Add a regression test\"}\n````":    "Add a regression test",
		"```json\n{\"suggestion\":null}\n```\nDone.":                    "error",
		"```json\n{\"suggestion\":\"Add a regression test\"}":           "error",
		"Here:\n```json\n{\"suggestion\":null}\n```":                    "error",
		`Sure! Try asking for tests.`:                                   "error",
	} {
		got, _, err := parseNextPromptSuggestion(&llm.Response{Content: []llm.Content{{Type: llm.ContentTypeText, Text: reply}}})
		if err != nil {
			got = "error"
		}
		if got != want {
			t.Errorf("parse(%q) = %q, want %q", reply, got, want)
		}
	}
}

// Quick replies are kept up to the first unusable one: dropping only that
// one would promote worse replies into its place. A tap sends a reply as
// typed, so one that would run a command is unusable. Replies past the
// fourth are ignored, and quick replies survive an unusable suggestion.
func TestParseNextPromptQuickReplies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reply string
		want  []string
	}{
		{`{"suggestion":null}`, nil},
		{`{"suggestion":null,"quick_replies":null}`, nil},
		{`{"suggestion":null,"quick_replies":[]}`, nil},
		{`{"suggestion":"Commit it","quick_replies":[" Yes ","Option B"]}`, []string{"Yes", "Option B"}},
		{`{"suggestion":null,"quick_replies":["A","B","C","D","E"]}`, []string{"A", "B", "C", "D"}},
		{`{"suggestion":null,"quick_replies":["A","B","C","D","","/archive"]}`, []string{"A", "B", "C", "D"}},
		{`{"suggestion":null,"quick_replies":["Yes","/archive"]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":["/archive","Yes"]}`, nil},
		{`{"suggestion":null,"quick_replies":["Yes","Push first","!ls","Commit"]}`, []string{"Yes", "Push first"}},
		{`{"suggestion":null,"quick_replies":["Yes"," !rm -rf ~"]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":["yes","Yes","YES","` + strings.Repeat("é", 24) + `"]}`, []string{"yes", strings.Repeat("é", 24)}},
		{`{"suggestion":null,"quick_replies":["Yes","` + strings.Repeat("é", 25) + `"]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":["Yes","one\ntwo"]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":["Yes","  "]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":["Yes",3]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":["Yes",null]}`, []string{"Yes"}},
		{"```json\n{\"suggestion\":null,\"quick_replies\":[\"Yes\"]}\n```", []string{"Yes"}},
		{`{"suggestion":"` + strings.Repeat("x", 241) + `","quick_replies":["Yes"]}`, []string{"Yes"}},
		{`{"suggestion":null,"quick_replies":"Yes"}`, nil},
		{`{"suggestion":null,"quick_replies":{"Yes":1}}`, nil},
	} {
		_, got, _ := parseNextPromptSuggestion(&llm.Response{Content: []llm.Content{{Type: llm.ContentTypeText, Text: tc.reply}}})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parse(%q) quick replies = %q, want %q", tc.reply, got, tc.want)
		}
	}
}

// Quick replies are an extra: a malformed list costs only them, never the
// suggestion beside it.
func TestParseNextPromptMalformedQuickRepliesKeepSuggestion(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{
		`{"suggestion":"Commit it","quick_replies":"Yes"}`,
		`{"suggestion":"Commit it","quick_replies":{"Yes":1}}`,
		`{"suggestion":"Commit it","quick_replies":7}`,
	} {
		got, quickReplies, err := parseNextPromptSuggestion(&llm.Response{Content: []llm.Content{{Type: llm.ContentTypeText, Text: reply}}})
		if got != "Commit it" || quickReplies != nil || err != nil {
			t.Errorf("parse(%q) = %q, %q, %v; want \"Commit it\", none, no error", reply, got, quickReplies, err)
		}
	}
}

// Quick replies are stored with the suggestion, under its sequence id, and
// reach clients wherever the conversation does: its stream, its fetch, and
// the conversation list, as a JSON array in a string, like tags.
func TestNextPromptQuickRepliesReachClients(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newSuggestionHarness(t, true)
		defer stopActiveConversationLoops(h.server)
		mux := http.NewServeMux()
		h.server.RegisterRoutes(mux)
		h.NewConversation("echo: water the basil", "")
		synctest.Wait()
		manager, err := h.server.getOrCreateConversationManager(t.Context(), h.convID, "")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		next := manager.subpub.Subscribe(ctx, -1)

		h.Chat("echo: Prune the rosemary?")
		synctest.Wait()
		conv, err := h.db.GetConversationByID(t.Context(), h.convID)
		if err != nil {
			t.Fatal(err)
		}
		if want := `["Yes","No"]`; conv.NextPromptQuickReplies != want || conv.NextPromptSuggestionSeq != lastTurnEnd(t, h) {
			t.Fatalf("quick replies = %q at %d, want %q after the turn ending at %d", conv.NextPromptQuickReplies, conv.NextPromptSuggestionSeq, want, lastTurnEnd(t, h))
		}
		const field = `"next_prompt_quick_replies":"[\"Yes\",\"No\"]"`
		streamed := false
		for !streamed {
			event, ok := next()
			if !ok {
				t.Fatal("the stream never carried the quick replies")
			}
			b, _ := json.Marshal(event)
			streamed = strings.Contains(string(b), field)
		}
		if body := call(t, mux, http.StatusOK, "GET", "/api/conversation/"+h.convID, "").Body.String(); !strings.Contains(body, field) {
			t.Fatalf("conversation fetch lacks %s: %s", field, body)
		}
		if body := call(t, mux, http.StatusOK, "GET", "/api/conversations", "").Body.String(); !strings.Contains(body, field) {
			t.Fatalf("conversation list lacks %s: %s", field, body)
		}

		h.Chat("echo: Pruned.")
		synctest.Wait()
		if text, seq := suggestionState(t, h); text != "echo: More about Pruned." || seq != lastTurnEnd(t, h) {
			t.Fatalf("suggestion = %q at %d, want it after the turn ending at %d", text, seq, lastTurnEnd(t, h))
		}
		if body := call(t, mux, http.StatusOK, "GET", "/api/conversation/"+h.convID, "").Body.String(); !strings.Contains(body, `"next_prompt_quick_replies":"[]"`) {
			t.Fatalf("conversation fetch has quick replies after a turn with none: %s", body)
		}
	})
}
