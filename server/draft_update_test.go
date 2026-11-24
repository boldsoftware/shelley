package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// draftPut sends a PUT /draft with the given partial-update fields (nil =
// omit) for conversationID and returns the recorder.
func draftPut(t *testing.T, server *Server, conversationID string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal draft request: %v", err)
	}
	httpReq := httptest.NewRequest("PUT", "/api/conversation/"+conversationID+"/draft", strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.handleUpdateDraft(w, httpReq, conversationID)
	return w
}

// Fields update independently: a model-only PUT (composer picker) and a
// cwd-only PUT (command palette) must each preserve the draft text and
// each other's values. Without the model sync, a draft promoted by a
// client that omits `model` (e.g. the iOS push "Reply" handler) falls
// back to the stale model the draft was created with, and other devices
// reopening the draft seed their picker from it.
func TestUpdateDraftPartialFieldsIndependent(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	origModel := "test-model"
	origCwd := "/tmp/orig"
	draft, err := database.CreateDraftConversation(t.Context(), &origCwd, &origModel, db.ConversationOptions{}, "my unsent draft")
	if err != nil {
		t.Fatalf("failed to create draft conversation: %v", err)
	}
	id := draft.ConversationID

	// Model-only update: text and cwd untouched.
	w := draftPut(t, server, id, map[string]string{"model": "test-model-2"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Model == nil || *got.Model != "test-model-2" {
		t.Fatalf("model not updated: got %v, want %q", got.Model, "test-model-2")
	}
	if got.Draft != "my unsent draft" {
		t.Fatalf("draft text clobbered by model update: got %q", got.Draft)
	}
	if got.Cwd == nil || *got.Cwd != origCwd {
		t.Fatalf("cwd clobbered by model update: got %v", got.Cwd)
	}

	// Cwd-only update: text and model untouched.
	w = draftPut(t, server, id, map[string]string{"cwd": "/tmp/newdir"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, err = database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Cwd == nil || *got.Cwd != "/tmp/newdir" {
		t.Fatalf("cwd not updated: got %v, want %q", got.Cwd, "/tmp/newdir")
	}
	if got.Model == nil || *got.Model != "test-model-2" {
		t.Fatalf("model clobbered by cwd update: got %v", got.Model)
	}
	if got.Draft != "my unsent draft" {
		t.Fatalf("draft text clobbered by cwd update: got %q", got.Draft)
	}

	// Text-only update (the autosave path): model and cwd untouched.
	w = draftPut(t, server, id, map[string]string{"draft": "edited text"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, err = database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Draft != "edited text" {
		t.Fatalf("draft text not updated: got %q", got.Draft)
	}
	if got.Model == nil || *got.Model != "test-model-2" {
		t.Fatalf("model clobbered by text update: got %v", got.Model)
	}
	if got.Cwd == nil || *got.Cwd != "/tmp/newdir" {
		t.Fatalf("cwd clobbered by text update: got %v", got.Cwd)
	}
	if !got.IsDraft {
		t.Fatalf("conversation should still be a draft")
	}

	// The response body carries the updated conversation.
	var resp generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Draft != "edited text" {
		t.Fatalf("response draft text: got %q", resp.Draft)
	}
	if resp.Model == nil || *resp.Model != "test-model-2" {
		t.Fatalf("response model: got %v", resp.Model)
	}
}

// A non-draft conversation is immutable through this endpoint: cwd is
// fixed at promote and the model only changes through the /model loop
// switch. 404 (not 400) so a client racing a concurrent promote can
// treat it as a no-op.
func TestUpdateDraftRejectsNonDraft(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	origModel := "test-model"
	origCwd := "/tmp/orig"
	conv, err := database.CreateConversation(t.Context(), nil, true, &origCwd, &origModel, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}

	for _, fields := range []map[string]string{
		{"model": "test-model-2", "cwd": "/tmp/newdir"},
		{"model": "test-model-2", "draft": "text"},
		{"cwd": "/tmp/newdir", "draft": "text"},
	} {
		w := draftPut(t, server, conv.ConversationID, fields)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for non-draft %v, got %d: %s", fields, w.Code, w.Body.String())
		}
	}

	got, err := database.GetConversationByID(t.Context(), conv.ConversationID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Model == nil || *got.Model != origModel {
		t.Fatalf("non-draft model should be unchanged: got %v", got.Model)
	}
	if got.Cwd == nil || *got.Cwd != origCwd {
		t.Fatalf("non-draft cwd should be unchanged: got %v", got.Cwd)
	}
	if got.Draft != "" || got.DraftUpdatedAt != nil {
		t.Fatalf("rejected update saved text: %q at %v", got.Draft, got.DraftUpdatedAt)
	}
}

// A started conversation's composer text is saved like a draft's, so it
// follows the user between clients, without making the conversation a draft
// or reordering the list: updated_at stays and draft_updated_at versions it.
func TestUpdateDraftTextOnStartedConversation(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	model := "test-model"
	conv, err := database.CreateConversation(t.Context(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	id := conv.ConversationID
	backdated := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := database.Pool().Exec(t.Context(), "UPDATE conversations SET updated_at = ? WHERE conversation_id = ?", backdated, id); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}

	w := draftPut(t, server, id, map[string]string{"draft": "next message"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Draft != "next message" || resp.IsDraft || resp.DraftUpdatedAt == nil {
		t.Fatalf("response: draft=%q is_draft=%v draft_updated_at=%v", resp.Draft, resp.IsDraft, resp.DraftUpdatedAt)
	}
	got, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Draft != "next message" || got.IsDraft {
		t.Fatalf("saved: draft=%q is_draft=%v", got.Draft, got.IsDraft)
	}
	if !got.UpdatedAt.Equal(backdated) {
		t.Fatalf("saving text moved updated_at: %v, want %v", got.UpdatedAt, backdated)
	}

	// The same text again keeps its version; empty text clears it.
	if err := database.Pool().Exec(t.Context(), "UPDATE conversations SET draft_updated_at = ? WHERE conversation_id = ?", backdated, id); err != nil {
		t.Fatalf("backdate draft_updated_at: %v", err)
	}
	if w := draftPut(t, server, id, map[string]string{"draft": "next message"}); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ = database.GetConversationByID(t.Context(), id); !got.DraftUpdatedAt.Equal(backdated) {
		t.Fatalf("unchanged text moved draft_updated_at: %v", got.DraftUpdatedAt)
	}
	if w := draftPut(t, server, id, map[string]string{"draft": ""}); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, _ = database.GetConversationByID(t.Context(), id)
	if got.Draft != "" || !got.DraftUpdatedAt.After(backdated) {
		t.Fatalf("cleared: draft=%q draft_updated_at=%v", got.Draft, got.DraftUpdatedAt)
	}
	if !got.UpdatedAt.Equal(backdated) {
		t.Fatalf("clearing text moved updated_at: %v", got.UpdatedAt)
	}
}

// An unknown model id is a client bug (or a host that doesn't carry the
// model); reject rather than persisting an id the promote would 400 on.
// Uses the two-model manager because the default test manager accepts
// any model id.
func TestUpdateDraftRejectsUnsupportedModel(t *testing.T) {
	t.Parallel()
	server, database := newTwoModelTestServer(t)

	draft, err := database.CreateDraftConversation(t.Context(), nil, nil, db.ConversationOptions{}, "draft")
	if err != nil {
		t.Fatalf("failed to create draft conversation: %v", err)
	}

	w := draftPut(t, server, draft.ConversationID, map[string]string{"model": "no-such-model"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported model, got %d: %s", w.Code, w.Body.String())
	}

	// And a supported id on the same manager works.
	w = draftPut(t, server, draft.ConversationID, map[string]string{"model": "model-b"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for supported model, got %d: %s", w.Code, w.Body.String())
	}
	got, err := database.GetConversationByID(t.Context(), draft.ConversationID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Model == nil || *got.Model != "model-b" {
		t.Fatalf("model not updated: got %v", got.Model)
	}
}

// Empty model or cwd is a client bug: "absent means keep" is spelled by
// omitting the field, not sending "". (Empty draft text is meaningful —
// it clears the composer — so only model/cwd reject empties.)
func TestUpdateDraftRejectsEmptyModelOrCwd(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	draft, err := database.CreateDraftConversation(t.Context(), nil, nil, db.ConversationOptions{}, "draft")
	if err != nil {
		t.Fatalf("failed to create draft conversation: %v", err)
	}

	w := draftPut(t, server, draft.ConversationID, map[string]string{"model": ""})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty model, got %d: %s", w.Code, w.Body.String())
	}

	w = draftPut(t, server, draft.ConversationID, map[string]string{"cwd": ""})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty cwd, got %d: %s", w.Code, w.Body.String())
	}

	// Empty draft text is allowed: it clears the composer.
	w = draftPut(t, server, draft.ConversationID, map[string]string{"draft": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty draft text, got %d: %s", w.Code, w.Body.String())
	}
	got, err := database.GetConversationByID(t.Context(), draft.ConversationID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Draft != "" {
		t.Fatalf("draft text should be cleared: got %q", got.Draft)
	}
}

// The motivating end-to-end flow: the composer picker retargets a draft's
// model via PUT /draft, and a promote that OMITS the model (push "Reply",
// a crashed client's retry) must then run on the picked model — not the
// model the draft was created with, and not the host default.
func TestUpdateDraftModelFlowsToOmittedModelPromote(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	origModel := "stale-model"
	draft, err := database.CreateDraftConversation(t.Context(), nil, &origModel, db.ConversationOptions{}, "echo: draft body")
	if err != nil {
		t.Fatalf("failed to create draft conversation: %v", err)
	}
	id := draft.ConversationID

	// Composer picker changes the model on the live draft.
	if w := draftPut(t, server, id, map[string]string{"model": "picked-model"}); w.Code != http.StatusOK {
		t.Fatalf("model PUT: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Promote with an omitted model. Must pin the loop to the picked model.
	if w := chatPost(t, server, id, ChatRequest{Message: "echo: hi"}); w.Code != http.StatusAccepted {
		t.Fatalf("promote send: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	got, err := database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.IsDraft {
		t.Fatalf("conversation should have been promoted")
	}
	if got.Model == nil || *got.Model != "picked-model" {
		t.Fatalf("promoted model: got %v, want %q", got.Model, "picked-model")
	}

	// A follow-up omitted-model reply must match the pinned model (no 400).
	if w := chatPost(t, server, id, ChatRequest{Message: "echo: reply"}); w.Code != http.StatusAccepted {
		t.Fatalf("reply with empty model: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// And the endpoint is now closed: post-promote PUTs are a no-op 404.
	if w := draftPut(t, server, id, map[string]string{"model": "stale-model"}); w.Code != http.StatusNotFound {
		t.Fatalf("post-promote PUT: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// An empty JSON body ({}) is a valid partial update that changes nothing
// but still bumps updated_at (the conversation-list reorder key and the
// draft-cache arbiter both key off it).
func TestUpdateDraftEmptyBodyBumpsUpdatedAt(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	origModel := "test-model"
	origCwd := "/tmp/orig"
	draft, err := database.CreateDraftConversation(t.Context(), &origCwd, &origModel, db.ConversationOptions{}, "my unsent draft")
	if err != nil {
		t.Fatalf("failed to create draft conversation: %v", err)
	}

	// CURRENT_TIMESTAMP is second-granular; backdate the row so the bump
	// is observable without sleeping.
	backdated := time.Now().Add(-time.Hour).UTC()
	if err := database.Pool().Exec(t.Context(), "UPDATE conversations SET updated_at = ? WHERE conversation_id = ?", backdated, draft.ConversationID); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}

	w := draftPut(t, server, draft.ConversationID, map[string]string{})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty body, got %d: %s", w.Code, w.Body.String())
	}
	got, err := database.GetConversationByID(t.Context(), draft.ConversationID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if got.Draft != "my unsent draft" || got.Model == nil || *got.Model != origModel || got.Cwd == nil || *got.Cwd != origCwd {
		t.Fatalf("empty body changed fields: draft=%q model=%v cwd=%v", got.Draft, got.Model, got.Cwd)
	}
	if !got.UpdatedAt.After(backdated.Add(time.Minute)) {
		t.Fatalf("updated_at not bumped: still %v", got.UpdatedAt)
	}
}
