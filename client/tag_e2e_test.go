package client

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
	"shelley.exe.dev/server"
)

// newRealServer boots a real Shelley server over httptest so the tag
// commands are exercised against the actual handlers (not a stub), which
// is what proves the wire shape of tags round-trips.
func newRealServer(t *testing.T) (*db.DB, *clientConfig, *http.Client, string) {
	t.Helper()
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(&server.LLMConfig{
		Models: modelsources.Build(models.All(), []modelsources.Source{modelsources.Predictable()}, nil, logger),
		Logger: logger,
	})
	svr := server.NewServer(database, llmManager,
		claudetool.ToolSetConfig{EnableBrowser: false}, logger, true, "predictable", "")
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cc := &clientConfig{serverURL: ts.URL}
	httpClient, baseURL, err := cc.newHTTPClient()
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	return database, cc, httpClient, baseURL
}

func TestTagRoundTripAgainstRealServer(t *testing.T) {
	ctx := context.Background()
	database, cc, httpClient, baseURL := newRealServer(t)

	conv, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	id := conv.ConversationID

	// A fresh conversation has no tags.
	got, err := fetchConversationTags(cc, httpClient, baseURL, id)
	if err != nil {
		t.Fatalf("fetchConversationTags: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("new conversation tags = %v, want none", got)
	}

	// Add two tags (one brand new, one that will later be reused).
	if _, err := setConversationTags(cc, httpClient, baseURL, id, mergeTags(got, []string{"ios", "#bug"})); err != nil {
		t.Fatalf("setConversationTags: %v", err)
	}
	got, err = fetchConversationTags(cc, httpClient, baseURL, id)
	if err != nil {
		t.Fatalf("fetchConversationTags: %v", err)
	}
	if want := []string{"ios", "bug"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}

	// A second conversation reuses an existing tag.
	conv2, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := setConversationTags(cc, httpClient, baseURL, conv2.ConversationID, []string{"ios"}); err != nil {
		t.Fatalf("setConversationTags: %v", err)
	}

	// "tags" now offers both, most-used first.
	all, err := listAllTags(cc, httpClient, baseURL, 100)
	if err != nil {
		t.Fatalf("listAllTags: %v", err)
	}
	want := []tagCount{{Tag: "ios", Count: 2}, {Tag: "bug", Count: 1}}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("listAllTags = %+v, want %+v", all, want)
	}

	// Removal.
	next := removeTags(got, []string{"bug"})
	if _, err := setConversationTags(cc, httpClient, baseURL, id, next); err != nil {
		t.Fatalf("setConversationTags: %v", err)
	}
	got, err = fetchConversationTags(cc, httpClient, baseURL, id)
	if err != nil {
		t.Fatalf("fetchConversationTags: %v", err)
	}
	if want := []string{"ios"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tags after removal = %v, want %v", got, want)
	}
}
