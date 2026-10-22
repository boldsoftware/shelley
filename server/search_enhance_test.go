package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
)

func TestEnhanceSearchWithoutExeDevEmail(t *testing.T) {
	srv, _, ps := newTestServer(t)
	held := newHeldLLMService()
	registry, err := models.NewManager(&models.Config{Models: []models.Built{
		{ID: "other", Service: ps},
		{ID: "local-default", Service: held},
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv.llmManager = registry
	srv.predictableOnly = false
	srv.defaultModel = "local-default"
	t.Cleanup(func() {
		srv.searchChatMu.Lock()
		defer srv.searchChatMu.Unlock()
		stopActiveConversationLoops(srv)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/search/enhance", strings.NewReader(`{"q":"font"}`))
	req.Header.Set("X-Shelley-Request", "1")
	w := httptest.NewRecorder()
	srv.handleEnhanceSearch(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Conversation ConversationWithState `json:"conversation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if model := response.Conversation.Model; model == nil || *model != "local-default" {
		t.Fatalf("search model = %v, want local-default", model)
	}
	message, err := searchInitialMessage("font", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, message)
}

func TestSearchChatReusesConversation(t *testing.T) {
	srv, database, _ := newTestServer(t)
	held := newHeldLLMService()
	srv.llmManager = &testLLMManager{service: held}
	t.Cleanup(func() { stopActiveConversationLoops(srv) })
	ctx := t.Context()
	opts := db.ConversationOptions{Kind: "search", ThinkingLevel: "off", SystemPrompt: "Search instructions"}
	first, err := srv.reuseSearchConversation(ctx, "ask-shelley-fixture", "predictable", "fixture@example.invalid", opts)
	if err != nil {
		t.Fatal(err)
	}
	old, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: first.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "Previous search"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateSubagentConversation(ctx, "old-search-worker", first.ConversationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSubagentRunner(srv).RunSubagent(ctx, child.ConversationID, "previous search task", "predictable", ""); err != nil {
		t.Fatal(err)
	}
	held.waitCall(t, "previous search task")
	if _, err := database.ArchiveConversation(ctx, first.ConversationID); err != nil {
		t.Fatal(err)
	}
	second, err := srv.reuseSearchConversation(ctx, "ask-shelley-fixture", "predictable", "fixture@example.invalid", opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.ConversationID != first.ConversationID || second.CurrentGeneration != first.CurrentGeneration+1 || second.Archived {
		t.Fatalf("expected same unarchived chat with a fresh generation: %+v", second)
	}
	if old.Generation == second.CurrentGeneration {
		t.Fatal("previous search was carried into the new generation")
	}
	waitFor(t, 5*time.Second, func() bool { return !srv.IsAgentWorking(child.ConversationID) })
	if !hasCancelledEndOfTurn(t, database, child.ConversationID) {
		t.Fatal("previous search's child was not cancelled")
	}
	if err := srv.messageParent(ctx, *child, "stale search report"); err == nil {
		t.Fatal("previous search's child can still report into the fresh generation")
	}
	if got := db.ParseConversationOptions(second.ConversationOptions); got.SystemPrompt != opts.SystemPrompt || got.DisableAllTools {
		t.Fatalf("expected custom prompt and ordinary tools: %+v", got)
	}
}

func TestSearchInitialMessageKeepsBroadVerifiedSeeds(t *testing.T) {
	var matches []searchMatch
	for i := range 12 {
		slug := fmt.Sprintf("font-work-%d", i)
		matches = append(matches, searchMatch{
			ID: fmt.Sprintf("c%d", i), Slug: &slug, MessageID: fmt.Sprintf("hit%d", i),
			Snippet: "CSS " + db.SnippetMarkStart + "font" + db.SnippetMarkEnd,
		})
	}
	matches = append(matches, matches[0])
	quote := "The font sample contains literal ``` backticks."
	message, err := searchInitialMessage("font", matches, []db.SearchMessageContext{
		{HitID: "hit0", MessageID: "human", Speaker: "user", Text: quote},
		{HitID: "hit0", MessageID: "assistant", Speaker: "agent", Text: "We changed font rendering."},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, data, ok := strings.Cut(message, "```json\n")
	if !ok {
		t.Fatal("missing inspectable JSON seeds")
	}
	data = strings.TrimSuffix(data, "\n```")
	if strings.Contains(data, "\n") {
		t.Fatal("seed JSON is not one line")
	}
	var seeds []searchSeed
	if err := json.Unmarshal([]byte(data), &seeds); err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 12 {
		t.Fatalf("expected all twelve unique candidates, got %d", len(seeds))
	}
	first := seeds[0]
	if first.MatchContext != "CSS font" || len(first.Sources) != 2 || first.Sources[0].Text != quote {
		t.Fatalf("wrong match context or supporting sources: %+v", first)
	}
	if first.Sources[0].URL != "/c/font-work-0?message_id=human" || first.Sources[0].Speaker != "User" {
		t.Fatalf("wrong exact-message link or speaker: %+v", first.Sources[0])
	}
}
