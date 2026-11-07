package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/siwc"
)

const (
	planAccount   = "0123456789abcdef0123456789abcdef"
	signedOutPlan = "fedcba9876543210fedcba9876543210"
)

// A signed-in plan's models route through its own tokens and survive
// provider refreshes; a signed-out account's are unlisted.
func TestChatGPTPlanModels(t *testing.T) {
	var auth []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":"+
			"[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n")
	}))
	t.Cleanup(api.Close)

	database, closeDB := db.NewTestDB(t)
	t.Cleanup(closeDB)
	store := siwc.NewStore(database)
	models := []siwc.Model{{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"}, {Slug: "gpt-6-luna", DisplayName: "GPT-6 Luna"}}
	for _, a := range []*siwc.Account{
		{
			ID: planAccount, Email: "ada@example.com", ClientID: "app_1", Subject: "u1", IDToken: "id-1", AccessToken: "access-1", RefreshToken: "refresh-1",
			ExpiresAt: time.Now().Add(time.Hour), Scopes: strings.Fields(siwc.Scopes), Models: models,
		},
		{ID: signedOutPlan, Email: "bob@example.com", ClientID: "app_2", Subject: "u2"},
	} {
		if err := store.Put(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	client := siwc.NewClient(api.Client())
	client.API = api.URL + "/v1"

	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	manager, err := NewManager(&Config{DB: database, Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatal(err)
	}
	manager.SetChatGPTPlans(siwc.NewManager(store, client))
	if err := manager.RefreshChatGPTPlans(); err != nil {
		t.Fatal(err)
	}
	// Other sources' refreshes keep the plan's models.
	if err := manager.RefreshProviders(); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshCustomModels(); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshBuiltModels([]Built{predictableBuilt()}); err != nil {
		t.Fatal(err)
	}

	sol := ChatGPTPlanRouteID(planAccount, "gpt-6.1-sol")
	info := manager.GetModelInfo(sol)
	if info == nil || info.Mode != ModeChatGPTPlan || info.SourceID != ChatGPTPlanSourceID(planAccount) || info.DisplayName != "GPT-6.1 Sol" {
		t.Fatalf("model info = %+v", info)
	}
	for _, id := range manager.GetAvailableModels() {
		if strings.Contains(id, signedOutPlan) {
			t.Errorf("signed-out account lists %s", id)
		}
	}
	if got := manager.GetWorkhorseModelID(sol); got != ChatGPTPlanRouteID(planAccount, "gpt-6-luna") {
		t.Errorf("workhorse = %s, want the plan's own luna", got)
	}

	service, source, _, err := manager.GetServiceSnapshot(sol)
	if err != nil || source != ChatGPTPlanSourceID(planAccount) {
		t.Fatalf("snapshot source %q, %v", source, err)
	}
	req := &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}}}}
	if _, err := service.Do(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 1 || auth[0] != "Bearer access-1" {
		t.Fatalf("Authorization = %v", auth)
	}
	if service.(*loggingService).service.(interface{ SupportsServerSideWebSearch() bool }).SupportsServerSideWebSearch() {
		t.Error("plan models must not use hosted web search")
	}

	if _, _, _, err := manager.GetServiceSnapshot(ChatGPTPlanRouteID(signedOutPlan, "gpt-6.1-sol")); err == nil {
		t.Fatal("signed-out account's model resolved")
	}
}
