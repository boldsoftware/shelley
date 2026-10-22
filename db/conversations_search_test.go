package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

func TestSearchConversationsPreferSlugMatches(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := t.Context()

	fixtures := []struct {
		slug     *string
		archived bool
		message  bool
		updated  string
	}{
		{stringPtr("pelican-older"), false, false, "2024-01-01 00:00:00"},
		{stringPtr("project-pelican-notes"), false, true, "2024-01-02 00:00:00"},
		{stringPtr("archived-pelican"), true, false, "2024-01-03 00:00:00"},
		{stringPtr("active-notes"), false, true, "2024-01-04 00:00:00"},
		{nil, false, true, "2024-01-05 00:00:00"},
		{stringPtr("active-latest"), false, true, "2024-01-06 00:00:00"},
		{stringPtr("archived-notes"), true, true, "2024-01-07 00:00:00"},
		{stringPtr("unrelated"), false, false, "2024-01-08 00:00:00"},
	}
	ids := make([]string, len(fixtures))
	for i, fixture := range fixtures {
		conv, err := db.CreateConversation(ctx, fixture.slug, true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = conv.ConversationID
		if fixture.message {
			for range 2 {
				if _, err := db.CreateMessage(ctx, CreateMessageParams{
					ConversationID: conv.ConversationID,
					Type:           MessageTypeAgent,
					LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "A pelican by the bay"}}},
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if fixture.archived {
			if _, err := db.ArchiveConversation(ctx, conv.ConversationID); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = ? WHERE conversation_id = ?", fixture.updated, conv.ConversationID); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		search func(context.Context, string, int64, int64) ([]ConversationListItem, error)
		want   []string
	}{
		{
			name: "FTS",
			search: func(ctx context.Context, query string, limit, offset int64) ([]ConversationListItem, error) {
				hits, err := db.SearchConversationsFTS(ctx, query, false, limit, offset)
				items := make([]ConversationListItem, len(hits))
				for i, hit := range hits {
					items[i] = hit.ConversationListItem
				}
				return items, err
			},
			want: []string{ids[1], ids[0], ids[5], ids[4], ids[3]},
		},
		{
			name:   "WithMessages",
			search: db.SearchConversationsWithMessages,
			want:   []string{ids[1], ids[0], ids[5], ids[4], ids[3]},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := func(limit, offset int64, want []string) {
				t.Helper()
				results, err := tc.search(ctx, "PELICAN", limit, offset)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]string, len(results))
				for i, result := range results {
					got[i] = result.ConversationID
				}
				if !slices.Equal(got, want) {
					t.Fatalf("limit=%d offset=%d: got %v, want %v", limit, offset, got, want)
				}
			}
			check(50, 0, tc.want)
			for i, id := range tc.want {
				check(1, int64(i), []string{id})
			}
			check(1, int64(len(tc.want)), nil)
		})
	}
}

func TestSearchConversationsFTSShortQueries(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := t.Context()

	active, err := db.CreateConversation(ctx, stringPtr("pelican-猫咪-jq-r"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := db.CreateConversation(ctx, stringPtr("pelican-猫咪-jq-r-archived"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ArchiveConversation(ctx, archived.ConversationID); err != nil {
		t.Fatal(err)
	}
	messageOnly, err := db.CreateConversation(ctx, stringPtr("notes"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: messageOnly.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "A pelican 猫咪 nearby; use jq and R"}}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		query       string
		matchesBody bool
	}{
		{"p", false},
		{"pe", false},
		{"猫", false},
		{"猫咪", true},
		{"j", false},
		{"jq", true},
		{"JQ", true},
		{"r", true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			query := tc.query
			results, err := db.SearchConversationsFTS(ctx, query, false, 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(results))
			for i, result := range results {
				got[i] = result.ConversationID
				if result.ConversationID == active.ConversationID && result.Snippet != "" {
					t.Errorf("slug-only match returned a message snippet: %q", result.Snippet)
				}
			}
			want := []string{active.ConversationID}
			if tc.matchesBody {
				want = append(want, messageOnly.ConversationID)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			if tc.matchesBody && (!strings.Contains(results[1].Snippet, SnippetMarkStart) || results[1].SnippetMessageID == "") {
				t.Fatalf("message match missing highlighted snippet or message ID: %#v", results[1])
			}
			page, err := db.SearchConversationsFTS(ctx, query, false, 1, int64(len(want)))
			if err != nil {
				t.Fatal(err)
			}
			if len(page) != 0 {
				t.Fatalf("archived result leaked onto second active page: %#v", page)
			}
			archivedResults, err := db.SearchConversationsFTS(ctx, query, true, 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(archivedResults) != 1 || archivedResults[0].ConversationID != archived.ConversationID || archivedResults[0].Snippet != "" {
				t.Fatalf("unexpected archived slug results: %#v", archivedResults)
			}
		})
	}

	results, err := db.SearchConversationsFTS(ctx, "pel", false, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].ConversationID != messageOnly.ConversationID || results[1].Snippet == "" {
		t.Fatalf("three-character prefix did not search messages: %#v", results)
	}

	for _, tc := range []struct {
		query string
		match bool
	}{
		{"jq r", true},
		{"JQ peli", true},
		{"j peli", false},
		{"j q", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			results, err := db.SearchConversationsFTS(ctx, tc.query, false, 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			if tc.match {
				if len(results) != 1 || results[0].ConversationID != messageOnly.ConversationID || results[0].Snippet == "" {
					t.Fatalf("expected message-only match: %#v", results)
				}
			} else if len(results) != 0 {
				t.Fatalf("short terms matched message prefixes: %#v", results)
			}
		})
	}
}

func TestSearchConversationsFTS(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Active conversation with a message mentioning "pelican"
	active, err := db.CreateConversation(ctx, stringPtr("active-bird"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: active.ConversationID,
		Type:           MessageTypeUser,
		UserData:       map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "I saw a pelican by the bay"}}},
	}); err != nil {
		t.Fatalf("create user msg: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: active.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "That pelican had a silver beak"}}},
	}); err != nil {
		t.Fatalf("create second matching msg: %v", err)
	}

	// Archived conversation with a message mentioning "pelican" too
	archived, err := db.CreateConversation(ctx, stringPtr("old-notes"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: archived.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "A pelican is a large waterbird"}}},
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}
	if _, err := db.ArchiveConversation(ctx, archived.ConversationID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Decoy conversation with no matching content.
	if _, err := db.CreateConversation(ctx, stringPtr("decoy"), true, nil, nil, ConversationOptions{}); err != nil {
		t.Fatalf("create decoy: %v", err)
	}

	results, err := db.SearchConversationsFTS(ctx, "pelican", false, 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 active result, got %d: %#v", len(results), results)
	}
	gotActive := false
	gotArchived := false
	for _, r := range results {
		if r.Conversation.ConversationID == active.ConversationID {
			gotActive = true
		}
		if r.Conversation.ConversationID == archived.ConversationID {
			gotArchived = true
		}
		// Every FTS hit should come with a snippet that contains both
		// the matched term and our mark sentinels.
		if r.Snippet == "" {
			t.Errorf("missing snippet for %s", r.Conversation.ConversationID)
		}
		if !strings.Contains(r.Snippet, SnippetMarkStart) || !strings.Contains(r.Snippet, SnippetMarkEnd) {
			t.Errorf("snippet missing mark sentinels: %q", r.Snippet)
		}
		if !strings.Contains(strings.ToLower(r.Snippet), "pelican") {
			t.Errorf("snippet does not contain match term: %q", r.Snippet)
		}
	}
	if !gotActive || gotArchived {
		t.Errorf("unexpected active search results: active=%v archived=%v", gotActive, gotArchived)
	}
	archivedResults, err := db.SearchConversationsFTS(ctx, "pelican", true, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(archivedResults) != 1 || archivedResults[0].ConversationID != archived.ConversationID || !strings.Contains(archivedResults[0].Snippet, SnippetMarkStart) {
		t.Fatalf("unexpected archived FTS results: %#v", archivedResults)
	}
	var snippets []generated.SearchConversationsFTSSnippetsRow
	if err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		match := `"pelican"*`
		var err error
		snippets, err = generated.New(rx.Conn()).SearchConversationsFTSSnippets(ctx, generated.SearchConversationsFTSSnippetsParams{
			MarkStart: SnippetMarkStart,
			MarkEnd:   SnippetMarkEnd,
			FtsMatch:  &match,
			ConvIds:   []string{active.ConversationID, archived.ConversationID},
		})
		return err
	}); err != nil {
		t.Fatalf("SearchConversationsFTSSnippets: %v", err)
	}
	if len(snippets) != 2 {
		t.Fatalf("SearchConversationsFTSSnippets returned %d rows, want one per conversation", len(snippets))
	}

	// Slug match should still work even with no message hits.
	results, err = db.SearchConversationsFTS(ctx, "decoy", false, 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS slug: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 slug result, got %d", len(results))
	}
	// Slug-only hits have no snippet.
	if results[0].Snippet != "" {
		t.Errorf("expected empty snippet for slug-only match, got %q", results[0].Snippet)
	}

	// Prefix matching: typing "peli" should still find the pelican messages.
	results, err = db.SearchConversationsFTS(ctx, "peli", false, 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS prefix: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 active prefix result, got %d", len(results))
	}

	// Empty query returns no results without erroring.
	emptyResults, err := db.SearchConversationsFTS(ctx, "   ", false, 50, 0)
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(emptyResults) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(emptyResults))
	}

	// FTS5 syntax characters in user input must not break the query.
	if _, err := db.SearchConversationsFTS(ctx, `pel"ican AND foo*`, false, 50, 0); err != nil {
		t.Fatalf("escaped query: %v", err)
	}

	// A literal % in the search query must not match every slug via LIKE.
	noise, err := db.SearchConversationsFTS(ctx, "%", false, 50, 0)
	if err != nil {
		t.Fatalf("percent query: %v", err)
	}
	if len(noise) != 0 {
		t.Errorf("expected 0 results for bare %% query, got %d", len(noise))
	}
}

// TestSearchConversationsFTSStripsCitationMarkers checks the search snippet,
// which is built from raw message JSON and is client-facing.
func TestSearchConversationsFTSStripsCitationMarkers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conv, err := db.CreateConversation(ctx, stringPtr("cited"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	text := "A pelican is a large waterbird\ue200cite\ue202turn1search0\ue201 that fishes in flocks."
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": text}}},
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}

	results, err := db.SearchConversationsFTS(ctx, "pelican", false, 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	snippet := results[0].Snippet
	if strings.ContainsAny(snippet, "\ue200\ue201\ue202\ue203") {
		t.Errorf("snippet contains citation markers: %q", snippet)
	}
	if strings.Contains(snippet, "citeturn1search0") {
		t.Errorf("snippet contains citation payload: %q", snippet)
	}
	if !strings.Contains(snippet, "that fishes in flocks.") {
		t.Errorf("snippet lost text following the citation: %q", snippet)
	}
}

func TestSearchConversationsFTSBodyPaginationUsesUpdatedOrder(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	var ids []string
	for _, name := range []string{"first", "second"} {
		slug := name
		c, err := database.CreateConversation(t.Context(), &slug, true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = database.CreateMessage(t.Context(), CreateMessageParams{ConversationID: c.ConversationID, Type: MessageTypeUser, LLMData: map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "needle"}}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ConversationID)
	}
	if err := database.Pool().Exec(t.Context(), "UPDATE conversations SET updated_at='2099-01-01T00:00:00Z' WHERE conversation_id=?", ids[0]); err != nil {
		t.Fatal(err)
	}
	for offset, want := range ids {
		hits, err := database.SearchConversationsFTS(t.Context(), "needle", false, 1, int64(offset))
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ConversationID != want {
			t.Fatalf("page %d: %#v, want %s", offset, hits, want)
		}
	}
}
