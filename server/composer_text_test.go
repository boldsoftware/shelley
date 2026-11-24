package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

// listClient is a client following the conversation list over /api/stream2,
// as the web UI and the apps do.
type listClient struct {
	rec   *flusherRecorder
	state []ConversationWithState
	hash  string
}

func followList(t *testing.T, mux *http.ServeMux) *listClient {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	c := &listClient{rec: newFlusherRecorder()}
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(c.rec, httptest.NewRequest(http.MethodGet, "/api/stream2", nil).WithContext(ctx))
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	ev := waitForPatchEventAfter(t, c.rec, "")
	c.state = mustApplyPatch(t, nil, ev.Patch)
	c.hash = ev.NewHash
	return c
}

// until applies the list's patches until ok holds for the row id.
func (c *listClient) until(t *testing.T, id string, ok func(ConversationWithState) bool) ConversationWithState {
	t.Helper()
	for {
		if i := slices.IndexFunc(c.state, func(r ConversationWithState) bool { return r.ConversationID == id }); i >= 0 && ok(c.state[i]) {
			return c.state[i]
		}
		ev := waitForPatchEventAfter(t, c.rec, c.hash)
		c.state = mustApplyPatch(t, c.state, ev.Patch)
		c.hash = ev.NewHash
	}
}

func (c *listClient) order() []string {
	var ids []string
	for _, r := range c.state {
		ids = append(ids, r.ConversationID)
	}
	return ids
}

func serve(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// Text typed into a started conversation's composer on one client shows up
// on another without moving the conversation in the list, and sending it from
// the other clears it everywhere.
func TestComposerTextFollowsAcrossClients(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	model := "test-model"
	older, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	laptop := followList(t, mux)
	phone := followList(t, mux)
	before := phone.until(t, older.ConversationID, func(ConversationWithState) bool { return true })
	order := phone.order()

	if w := serve(t, mux, "PUT", "/api/conversation/"+older.ConversationID+"/draft", map[string]string{"draft": "half a thought"}); w.Code != http.StatusOK {
		t.Fatalf("save text: %d %s", w.Code, w.Body.String())
	}
	row := phone.until(t, older.ConversationID, func(r ConversationWithState) bool { return r.Draft == "half a thought" })
	if row.IsDraft {
		t.Fatal("saving text made the conversation a draft")
	}
	if row.DraftUpdatedAt == nil {
		t.Fatal("saved text has no draft_updated_at")
	}
	if !row.UpdatedAt.Equal(before.UpdatedAt) || !slices.Equal(phone.order(), order) {
		t.Fatalf("saving text moved the conversation: updated_at %v -> %v, order %v -> %v", before.UpdatedAt, row.UpdatedAt, order, phone.order())
	}

	laptop.until(t, older.ConversationID, func(r ConversationWithState) bool { return r.Draft == "half a thought" })

	// The phone finishes the thought and sends it, naming the version of
	// the text it was composed from, as it got it from the list.
	stamp, err := json.Marshal(row.DraftUpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Message        string          `json:"message"`
		DraftUpdatedAt json.RawMessage `json:"draft_updated_at"`
	}
	sent.Message, sent.DraftUpdatedAt = "echo: half a thought, finished", stamp
	if w := serve(t, mux, "POST", "/api/conversation/"+older.ConversationID+"/chat", sent); w.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	laptop.until(t, older.ConversationID, func(r ConversationWithState) bool { return r.Draft == "" })
	if !hasUserMessage(t, database, older.ConversationID) {
		t.Fatal("the send recorded no message")
	}
}
