package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"shelley.exe.dev/db"
)

func TestConversationThemesEndpoint(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	if err := database.SetFeatureFlagOverride(t.Context(), FlagConversationChapters.Name, "true"); err != nil {
		t.Fatal(err)
	}
	mux, _ := newTestStreamServer(t, database)
	emptyIndexResponse := httptest.NewRecorder()
	mux.ServeHTTP(emptyIndexResponse, httptest.NewRequest("GET", "/api/conversations/themes", nil))
	if emptyIndexResponse.Code != http.StatusOK {
		t.Fatalf("empty index status = %d, want %d: %s", emptyIndexResponse.Code, http.StatusOK, emptyIndexResponse.Body)
	}
	var emptyIndex map[string][]db.ConversationChapter
	if err := json.Unmarshal(emptyIndexResponse.Body.Bytes(), &emptyIndex); err != nil {
		t.Fatal(err)
	}
	if emptyIndex == nil || len(emptyIndex) != 0 {
		t.Fatalf("empty index = %#v, want empty object", emptyIndex)
	}

	id := seedConversation(t, database, 3)
	get := func(id string, status int) db.ConversationThemes {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/conversation/"+id+"/themes", nil))
		if w.Code != status {
			t.Fatalf("status = %d, want %d: %s", w.Code, status, w.Body)
		}
		var result db.ConversationThemes
		if status == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	if empty := get(id, http.StatusOK); empty.Chapters == nil || len(empty.Chapters) != 0 {
		t.Fatalf("unseeded themes = %+v", empty)
	}
	messages, err := database.ListMessages(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	themes := db.ConversationThemes{
		Chapters: []db.ConversationChapter{
			{Title: "First idea", MessageID: messages[0].MessageID},
			{Title: "Second idea", MessageID: messages[1].MessageID},
		},
		ThroughMessageID: messages[2].MessageID,
	}
	if err := database.SetConversationThemes(t.Context(), id, themes); err != nil {
		t.Fatal(err)
	}
	if got := get(id, http.StatusOK); !reflect.DeepEqual(got, themes) {
		t.Fatalf("themes = %+v; want %+v", got, themes)
	}
	get("missing-conversation", http.StatusNotFound)

	indexRequest := httptest.NewRequest("GET", "/api/conversations/themes", nil)
	indexResponse := httptest.NewRecorder()
	mux.ServeHTTP(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("index status = %d, want %d: %s", indexResponse.Code, http.StatusOK, indexResponse.Body)
	}
	var index map[string][]db.ConversationChapter
	if err := json.Unmarshal(indexResponse.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(index, map[string][]db.ConversationChapter{id: themes.Chapters}) {
		t.Fatalf("index = %#v", index)
	}

	if err := database.SetFeatureFlagOverride(t.Context(), FlagConversationChapters.Name, "false"); err != nil {
		t.Fatal(err)
	}
	offResponse := httptest.NewRecorder()
	mux.ServeHTTP(offResponse, httptest.NewRequest("GET", "/api/conversations/themes", nil))
	if offResponse.Code != http.StatusForbidden {
		t.Fatalf("flag-off index status = %d, want %d", offResponse.Code, http.StatusForbidden)
	}
}
