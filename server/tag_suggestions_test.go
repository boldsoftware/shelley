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

type tagTestService struct{ namingTestService }

func (s *tagTestService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.request = req
	return &llm.Response{Content: llm.TextContent(`{"tags":[" #OLD ","###","#golang","GOLANG","  ## new  ","third","fourth"]}`)}, nil
}

func TestTagSuggestionsReadOnly(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	id := seedConversation(t, database, 16)
	messages := listMessages(t, database, id)
	if err := database.ForceUpdateConversationModel(ctx, id, "gpt-6-sol"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetConversationThemes(ctx, id, db.ConversationThemes{Chapters: []db.ConversationChapter{{Title: "Go work", MessageID: messages[0].MessageID}}, ThroughMessageID: messages[0].MessageID}); err != nil {
		t.Fatal(err)
	}
	before, err := database.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	mux, server := newTestStreamServer(t, database)
	conversation := &tagTestService{namingTestService{Service: predictable.NewService()}}
	workhorse := &tagTestService{namingTestService{Service: predictable.NewService(), levels: []llm.ThinkingLevel{llm.ThinkingLevelOff}}}
	registry, err := models.NewManager(&models.Config{Models: []models.Built{{ID: "gpt-6-sol", Provider: models.ProviderOpenAI, Service: conversation}, {ID: "gpt-6-luna", Provider: models.ProviderOpenAI, Service: workhorse}}})
	if err != nil {
		t.Fatal(err)
	}
	server.llmManager = registry
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/tag-suggestions", strings.NewReader(`{"existing_tags":["old"],"known_tags":["GoLang"]}`)).WithContext(ctx))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"tags":["GoLang","new"]}` {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	req := workhorse.request
	if req == nil || req.ThinkingLevel != llm.ThinkingLevelOff || len(req.Tools) != 0 || conversation.request != nil {
		t.Fatalf("unexpected requests: %+v", req)
	}
	if prompt := req.Messages[0].Content[0].Text; !strings.Contains(prompt, "Go work") ||
		!strings.Contains(prompt, `"m8"`) || strings.Contains(prompt, `"m0"`) ||
		!strings.Contains(prompt, `"recent_assistant_excerpts":["m;","m=","m?"]`) {
		t.Fatalf("unexpected context: %s", prompt)
	}
	after, err := database.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Tags != after.Tags {
		t.Fatalf("tags changed: %v -> %v", before.Tags, after.Tags)
	}
	if got := listMessages(t, database, id); len(got) != len(messages) {
		t.Fatalf("messages changed: %d -> %d", len(messages), len(got))
	}
}

func TestTagEditsPreserveInterveningChanges(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	id := seedConversation(t, database, 0)
	// Another tab added remote after this editor opened with just WIP.
	if _, err := database.UpdateConversationTags(ctx, id, []string{"WIP", "remote"}); err != nil {
		t.Fatal(err)
	}
	mux, _ := newTestStreamServer(t, database)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/conversation/"+id+"/tags",
		strings.NewReader(`{"edits":[{"tag":"wip","add":false},{"tag":"REMOTE","add":true},{"tag":"new","add":true}]}`)).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	after, err := database.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Tags != `["remote","new"]` {
		t.Fatalf("stored tags: %s", after.Tags)
	}
}
