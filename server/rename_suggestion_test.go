package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

type namingTestService struct {
	llm.Service
	request *llm.Request
	levels  []llm.ThinkingLevel
}

func (s *namingTestService) SupportsReasoning() bool { return true }
func (s *namingTestService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return s.levels
}

func (s *namingTestService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.request = req
	return &llm.Response{Content: llm.TextContent("chapter-based-chat-naming")}, nil
}

func TestRenameSuggestionUsesWorkhorseWithoutReasoning(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	id := seedConversation(t, database, 16)
	messages := listMessages(t, database, id)
	if _, err := database.UpdateConversationSlug(ctx, id, "old-name"); err != nil {
		t.Fatal(err)
	}
	if err := database.ForceUpdateConversationModel(ctx, id, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationThemes(ctx, id, db.ConversationThemes{
		Chapters:         []db.ConversationChapter{{Title: "Conversation navigation", MessageID: messages[0].MessageID}},
		ThroughMessageID: messages[0].MessageID,
	}); err != nil {
		t.Fatal(err)
	}
	mux, server := newTestStreamServer(t, database)
	conversation := &namingTestService{Service: predictable.NewService()}
	workhorse := &namingTestService{Service: predictable.NewService()}
	built := []models.Built{{ID: "gpt-6-sol", Provider: models.ProviderOpenAI, Service: conversation}}
	for _, mode := range []string{"absent", "requires reasoning", "supports off"} {
		candidates := built
		if mode != "absent" {
			workhorse.levels = []llm.ThinkingLevel{llm.ThinkingLevelLow}
			if mode == "supports off" {
				workhorse.levels = append(workhorse.levels, llm.ThinkingLevelOff)
			}
			candidates = append(candidates, models.Built{ID: "gpt-6-luna", Provider: models.ProviderOpenAI, Service: workhorse})
		}
		registry, err := models.NewManager(&models.Config{Models: candidates})
		if err != nil {
			t.Fatal(err)
		}
		server.llmManager = registry
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/conversation/"+id+"/rename-suggestion", nil).WithContext(ctx))
		if mode != "supports off" {
			if w.Code != http.StatusServiceUnavailable || conversation.request != nil || workhorse.request != nil {
				t.Fatalf("workhorse %s: status %d, model called: %v", mode, w.Code, conversation.request != nil || workhorse.request != nil)
			}
			continue
		}
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"slug":"chapter-based-chat-naming"`) {
			t.Fatalf("suggestion response status %d: %s", w.Code, w.Body)
		}
	}
	req := workhorse.request
	if req == nil || req.ThinkingLevel != llm.ThinkingLevelOff || len(req.Tools) != 0 || conversation.request != nil {
		t.Fatalf("expected only a workhorse request without reasoning or tools, got %+v", req)
	}
	prompt := req.Messages[0].Content[0].Text
	for _, want := range []string{"old-name", "Conversation navigation", `"m4","m6","m8"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in naming context: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, `"m0"`) || strings.Contains(prompt, `"m2"`) {
		t.Errorf("naming context contains old messages: %s", prompt)
	}
	conv, err := database.GetConversationByID(ctx, id)
	if err != nil || derefString(conv.Slug) != "old-name" {
		t.Fatalf("suggestion changed the stored name: %v, %v", conv, err)
	}
	if got := listMessages(t, database, id); len(got) != len(messages) {
		t.Fatalf("suggestion added messages: got %d, want %d", len(got), len(messages))
	}
}
