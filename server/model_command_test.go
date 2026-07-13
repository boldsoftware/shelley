package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
)

// twoModelLLMManager exposes two ready models ("model-a" and "model-b"), both
// backed by the same PredictableService, so /model switching can be exercised
// end-to-end without real providers.
type twoModelLLMManager struct {
	service llm.Service
}

func (m *twoModelLLMManager) GetService(modelID string) (llm.Service, error) {
	if modelID == "model-a" || modelID == "model-b" {
		return m.service, nil
	}
	return nil, os.ErrNotExist
}

func (m *twoModelLLMManager) GetAvailableModels() []string { return []string{"model-a", "model-b"} }

func (m *twoModelLLMManager) HasModel(modelID string) bool {
	return modelID == "model-a" || modelID == "model-b"
}

func (m *twoModelLLMManager) GetModelInfo(modelID string) *models.ModelInfo {
	switch modelID {
	case "model-a":
		return &models.ModelInfo{DisplayName: "Model A"}
	case "model-b":
		return &models.ModelInfo{DisplayName: "Model B"}
	}
	return nil
}

func (m *twoModelLLMManager) RefreshCustomModels() error { return nil }

// levelNamedModelLLMManager exposes a model literally named "high" (colliding
// with a reasoning level) so the ambiguity-rejection path can be exercised.
type levelNamedModelLLMManager struct {
	service llm.Service
}

func (m *levelNamedModelLLMManager) GetService(modelID string) (llm.Service, error) {
	if modelID == "model-a" || modelID == "high" {
		return m.service, nil
	}
	return nil, os.ErrNotExist
}

func (m *levelNamedModelLLMManager) GetAvailableModels() []string {
	return []string{"model-a", "high"}
}

func (m *levelNamedModelLLMManager) HasModel(modelID string) bool {
	return modelID == "model-a" || modelID == "high"
}

func (m *levelNamedModelLLMManager) GetModelInfo(modelID string) *models.ModelInfo {
	return &models.ModelInfo{}
}

func (m *levelNamedModelLLMManager) RefreshCustomModels() error { return nil }

func newTwoModelTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	svr := NewServer(database, &twoModelLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "model-a", "")
	svr.hooksDir = t.TempDir()
	if svr.terminals != nil {
		svr.terminals.SetSpawner(InProcessSpawner)
	}
	return svr, database
}

func postChat(t *testing.T, srv *Server, conversationID, message string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(ChatRequest{Message: message})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleChatConversation(w, req, conversationID)
	return w
}

// postChatModel is like postChat but sends an explicit request model, mirroring
// what the web UI does (it always attaches its selected model to every send).
func postChatModel(t *testing.T, srv *Server, conversationID, message, model string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(ChatRequest{Message: message, Model: model})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/chat", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleChatConversation(w, req, conversationID)
	return w
}

func listMessages(t *testing.T, database *db.DB, conversationID string) []generated.Message {
	t.Helper()
	var msgs []generated.Message
	err := database.Queries(context.Background(), func(q *generated.Queries) error {
		var err error
		msgs, err = q.ListMessages(context.Background(), conversationID)
		return err
	})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	return msgs
}

func lastModelChange(msgs []generated.Message) *generated.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type == string(db.MessageTypeModelChange) {
			return &msgs[i]
		}
	}
	return nil
}

// TestModelSwitchSticksAgainstStaleRequestModel reproduces the bug where a
// /model switch is silently undone by the very next chat send. The web UI
// hides the model picker on an existing conversation but still attaches its
// (now stale) composer model to every request. That stale req.Model must NOT
// override the model the conversation was switched to via /model.
func TestModelSwitchSticksAgainstStaleRequestModel(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// Run a turn on model-a (the composer's model).
	if w := postChatModel(t, srv, id, "hello", "model-a"); w.Code != http.StatusAccepted {
		t.Fatalf("chat hello: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, m := range listMessages(t, database, id) {
			if m.Type == string(db.MessageTypeAgent) {
				return true
			}
		}
		return false
	})

	// Switch to model-b via /model.
	if w := postChatModel(t, srv, id, "/model model-b", "model-a"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if updated, _ := database.GetConversationByID(ctx, id); updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("expected model-b persisted after /model, got %v", updated.Model)
	}

	// The next real send still carries the STALE composer model (model-a),
	// because the UI never updated it. This must not revert the switch.
	if w := postChatModel(t, srv, id, "and now?", "model-a"); w.Code != http.StatusAccepted {
		t.Fatalf("followup send: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	updated, err := database.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("stale req.Model reverted the switch: got %v, want model-b", updated.Model)
	}
	// And the in-memory loop that actually talks to the LLM must run model-b,
	// not the stale model-a. This is the crux of the bug: the DB could say
	// model-b while the live loop still ran model-a.
	srv.mu.Lock()
	mgr := srv.activeConversations[id]
	srv.mu.Unlock()
	if mgr == nil {
		t.Fatalf("expected an active manager for %s", id)
	}
	if got := mgr.GetModel(); got != "model-b" {
		t.Fatalf("live loop model = %q, want model-b", got)
	}
}

// TestModelCommandSwitch verifies that "/model model-b" persists the new model
// on the conversation, records a user-visible modelchange marker, and does not
// send the command to the LLM.
func TestModelCommandSwitch(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// First a real message so the loop is created and pinned to model-a.
	if w := postChat(t, srv, id, "hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat hello: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, m := range listMessages(t, database, id) {
			if m.Type == string(db.MessageTypeAgent) {
				return true
			}
		}
		return false
	})

	// Now switch models.
	w := postChat(t, srv, id, "/model model-b")
	if w.Code != http.StatusAccepted {
		t.Fatalf("chat /model: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "model-command" {
		t.Fatalf("expected status model-command, got %q", resp["status"])
	}

	// The conversation row must now record model-b.
	updated, err := database.GetConversationByID(ctx, id)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("expected model-b persisted, got %v", updated.Model)
	}

	// A modelchange marker must exist describing the switch.
	msgs := listMessages(t, database, id)
	mc := lastModelChange(msgs)
	if mc == nil {
		t.Fatalf("expected a modelchange marker message")
	}
	var ud ModelChangeUserData
	if mc.UserData == nil {
		t.Fatalf("modelchange marker has no user_data")
	}
	if err := json.Unmarshal([]byte(*mc.UserData), &ud); err != nil {
		t.Fatalf("unmarshal modelchange user_data: %v", err)
	}
	if ud.From != "model-a" || ud.To != "model-b" {
		t.Fatalf("expected from=model-a to=model-b, got from=%q to=%q", ud.From, ud.To)
	}

	// The marker is excluded from LLM context.
	if !mc.ExcludedFromContext {
		t.Fatalf("modelchange marker should be excluded from context")
	}

	// A follow-up message must be accepted under the new model (no mismatch 400).
	if w := postChat(t, srv, id, "hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat after switch: expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

// TestModelCommandSwitchMidTurn verifies that switching models while the agent
// is mid-turn (a long-running tool is executing) cleanly ends the turn and
// clears the persisted agent_working flag, rather than leaving the thinking
// indicator stuck on.
func TestModelCommandSwitchMidTurn(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// Kick off a turn that blocks in a slow bash tool so the agent stays
	// mid-turn (agent_working=true) while we switch.
	if w := postChat(t, srv, id, "bash: sleep 5"); w.Code != http.StatusAccepted {
		t.Fatalf("chat: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(ctx, id)
		return err == nil && c.AgentWorking
	})

	// Switch models mid-turn.
	if w := postChat(t, srv, id, "/model model-b"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// The switch must clear agent_working and persist the new model.
	waitFor(t, 5*time.Second, func() bool {
		c, err := database.GetConversationByID(ctx, id)
		return err == nil && !c.AgentWorking
	})
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("expected model-b persisted, got %v", updated.Model)
	}

	// A modelchange marker recording the switch must exist.
	if mc := lastModelChange(listMessages(t, database, id)); mc == nil {
		t.Fatalf("expected a modelchange marker after mid-turn switch")
	}
}

// getConvOptions reads the persisted conversation options.
func getConvReasoning(t *testing.T, database *db.DB, conversationID string) string {
	t.Helper()
	c, err := database.GetConversationByID(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	return db.ParseConversationOptions(c.ConversationOptions).ThinkingLevel
}

// TestModelCommandReasoningOnly verifies that "/model <level>" changes only the
// reasoning level (leaving the model unchanged), persists it, records a marker,
// and that the next turn's LLM request carries the new thinking level.
func TestModelCommandReasoningOnly(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ps := srv.llmManager.(*twoModelLLMManager).service.(*loop.PredictableService)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// Change reasoning to high.
	if w := postChat(t, srv, id, "/model high"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model high: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Model unchanged; reasoning persisted.
	if r := getConvReasoning(t, database, id); r != "high" {
		t.Fatalf("expected reasoning=high persisted, got %q", r)
	}
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-a" {
		t.Fatalf("reasoning-only change must not touch the model, got %v", updated.Model)
	}

	// Marker records the reasoning transition, not a model one.
	mc := lastModelChange(listMessages(t, database, id))
	if mc == nil {
		t.Fatalf("expected a modelchange marker")
	}
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if ud.To != "" {
		t.Fatalf("reasoning-only change must not record a model switch, got To=%q", ud.To)
	}
	if ud.ReasoningFrom != "default" || ud.ReasoningTo != "high" {
		t.Fatalf("expected reasoning default->high, got %q->%q", ud.ReasoningFrom, ud.ReasoningTo)
	}

	// Next turn must send ThinkingLevelHigh to the model. We check that SOME
	// request carries it rather than the last one: the first real message also
	// kicks off async slug generation, which issues its own LLM request with the
	// default thinking level and can land last.
	ps.ClearRequests()
	if w := postChat(t, srv, id, "hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat hello: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, req := range ps.GetRecentRequests() {
			if req.ThinkingLevel == llm.ThinkingLevelHigh {
				return true
			}
		}
		return false
	})
}

// TestModelCommandModelAndReasoning verifies that "/model <id> <level>" applies
// both changes in one command, and that the level and model id may appear in
// either order.
func TestModelCommandModelAndReasoning(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// Reasoning level first, model second — order must not matter.
	if w := postChat(t, srv, id, "/model low model-b"); w.Code != http.StatusAccepted {
		t.Fatalf("chat: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("expected model-b, got %v", updated.Model)
	}
	if r := getConvReasoning(t, database, id); r != "low" {
		t.Fatalf("expected reasoning=low, got %q", r)
	}

	mc := lastModelChange(listMessages(t, database, id))
	if mc == nil {
		t.Fatalf("expected a modelchange marker")
	}
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if ud.From != "model-a" || ud.To != "model-b" {
		t.Fatalf("expected model-a->model-b, got %q->%q", ud.From, ud.To)
	}
	if ud.ReasoningTo != "low" {
		t.Fatalf("expected reasoning->low, got %q", ud.ReasoningTo)
	}
}

// TestModelCommandBareLevelSwitch verifies that a bare reasoning level like
// "/model medium" changes only reasoning — no "reasoning" keyword required.
func TestModelCommandBareLevelSwitch(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{ThinkingLevel: "high"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	if w := postChat(t, srv, id, "/model medium"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model medium: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if r := getConvReasoning(t, database, id); r != "medium" {
		t.Fatalf("expected reasoning=medium, got %q", r)
	}
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-a" {
		t.Fatalf("bare level must not change the model, got %v", updated.Model)
	}
	mc := lastModelChange(listMessages(t, database, id))
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if ud.ReasoningFrom != "high" || ud.ReasoningTo != "medium" {
		t.Fatalf("expected reasoning high->medium, got %q->%q", ud.ReasoningFrom, ud.ReasoningTo)
	}
}

// TestModelCommandDefaultRejected verifies that "default" is not a special
// token: it is treated as an unknown model and changes nothing.
func TestModelCommandDefaultRejected(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelB := "model-b"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelB, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	if w := postChat(t, srv, id, "/model default"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model default: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	// Model unchanged.
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("/model default must not change the model, got %v", updated.Model)
	}
	// The reply explains the token is unknown.
	mc := lastModelChange(listMessages(t, database, id))
	if mc == nil {
		t.Fatalf("expected an informational marker")
	}
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if !strings.Contains(ud.Text, "Unknown") {
		t.Fatalf("expected unknown-option message, got %q", ud.Text)
	}
}

// TestModelCommandAmbiguous verifies that a token which is both a valid model
// id and a reasoning level (a model literally named "high") is rejected instead
// of guessed.
func TestModelCommandAmbiguous(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := loop.NewPredictableService()
	srv := NewServer(database, &levelNamedModelLLMManager{service: ps},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "model-a", "")
	srv.hooksDir = t.TempDir()
	if srv.terminals != nil {
		srv.terminals.SetSpawner(InProcessSpawner)
	}
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	if w := postChat(t, srv, id, "/model high"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model high: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	// Nothing changed: still model-a, no reasoning set.
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-a" {
		t.Fatalf("ambiguous command must not change the model, got %v", updated.Model)
	}
	if r := getConvReasoning(t, database, id); r != "" {
		t.Fatalf("ambiguous command must not change reasoning, got %q", r)
	}
	// The reply marker explains the ambiguity.
	mc := lastModelChange(listMessages(t, database, id))
	if mc == nil {
		t.Fatalf("expected an informational marker")
	}
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if !strings.Contains(ud.Text, "Ambiguous") {
		t.Fatalf("expected ambiguity message, got %q", ud.Text)
	}
}

// TestModelCommandBareShowsStatus verifies that a bare "/model" reports the
// current model without switching or reaching the LLM.
func TestModelCommandBareShowsStatus(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	w := postChat(t, srv, id, "/model")
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	msgs := listMessages(t, database, id)
	mc := lastModelChange(msgs)
	if mc == nil {
		t.Fatalf("expected a modelchange info marker")
	}
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if ud.From != "" || ud.To != "" {
		t.Fatalf("bare /model must not record a switch, got from=%q to=%q", ud.From, ud.To)
	}
	if !strings.Contains(ud.Text, "model-a") || !strings.Contains(ud.Text, "model-b") {
		t.Fatalf("status should list available models, got: %q", ud.Text)
	}

	// Model unchanged.
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-a" {
		t.Fatalf("bare /model must not change the model, got %v", updated.Model)
	}
}

// TestModelCommandUnknownModel verifies switching to an unknown model reports
// an error and leaves the conversation model unchanged.
func TestModelCommandUnknownModel(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	w := postChat(t, srv, id, "/model does-not-exist")
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	msgs := listMessages(t, database, id)
	mc := lastModelChange(msgs)
	if mc == nil {
		t.Fatalf("expected an info marker for unknown model")
	}
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if !strings.Contains(ud.Text, "does-not-exist") {
		t.Fatalf("expected error to mention the bad model, got: %q", ud.Text)
	}

	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-a" {
		t.Fatalf("unknown /model must not change the model, got %v", updated.Model)
	}
}

// forkAt drives the fork HTTP handler at the given cutoff sequence_id and
// returns the new conversation id.
func forkAt(t *testing.T, srv *Server, conversationID string, cutoff int64) string {
	t.Helper()
	body, _ := json.Marshal(ForkRequest{SequenceID: cutoff})
	req := httptest.NewRequest("POST", "/api/conversation/"+conversationID+"/fork", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleForkConversation(w, req, conversationID)
	if w.Code != http.StatusCreated {
		t.Fatalf("fork: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var conv generated.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &conv); err != nil {
		t.Fatalf("decode fork response: %v", err)
	}
	return conv.ConversationID
}

// TestForkUsesModelStateAtCutoff verifies that forking a conversation which
// switched model and reasoning via /model AFTER the fork point continues from
// the state as of the fork point, not the source's latest state.
func TestForkUsesModelStateAtCutoff(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// Turn on model-a, then note the cutoff (fork here), THEN switch to
	// model-b + high reasoning.
	if w := postChat(t, srv, id, "hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat hello: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, m := range listMessages(t, database, id) {
			if m.Type == string(db.MessageTypeAgent) {
				return true
			}
		}
		return false
	})
	cutoff := listMessages(t, database, id)[len(listMessages(t, database, id))-1].SequenceID

	// Now switch model + reasoning; these markers land AFTER the cutoff.
	if w := postChat(t, srv, id, "/model model-b high"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model: %d %s", w.Code, w.Body.String())
	}
	if updated, _ := database.GetConversationByID(ctx, id); updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("source should now be model-b, got %v", updated.Model)
	}

	// Fork at the cutoff. The fork must reflect model-a and empty reasoning,
	// NOT the source's post-cutoff model-b/high.
	forkID := forkAt(t, srv, id, cutoff)
	fork, err := database.GetConversationByID(ctx, forkID)
	if err != nil {
		t.Fatalf("get fork: %v", err)
	}
	if fork.Model == nil || *fork.Model != "model-a" {
		t.Fatalf("fork model = %v, want model-a (state at cutoff)", fork.Model)
	}
	if r := getConvReasoning(t, database, forkID); r != "" {
		t.Fatalf("fork reasoning = %q, want empty (state at cutoff)", r)
	}
}

// TestForkWithoutModelSwitchKeepsModel verifies the common case: forking a
// conversation that never switched model keeps the source's model.
func TestForkWithoutModelSwitchKeepsModel(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelB := "model-b"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelB, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID
	if w := postChat(t, srv, id, "hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat hello: %d %s", w.Code, w.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, m := range listMessages(t, database, id) {
			if m.Type == string(db.MessageTypeAgent) {
				return true
			}
		}
		return false
	})
	cutoff := listMessages(t, database, id)[len(listMessages(t, database, id))-1].SequenceID

	forkID := forkAt(t, srv, id, cutoff)
	fork, err := database.GetConversationByID(ctx, forkID)
	if err != nil {
		t.Fatalf("get fork: %v", err)
	}
	if fork.Model == nil || *fork.Model != "model-b" {
		t.Fatalf("fork model = %v, want model-b", fork.Model)
	}
}

// TestResolveReasoningArg exercises the lenient reasoning-level matcher: exact
// names, unambiguous prefixes, and ambiguous/unknown tokens.
func TestResolveReasoningArg(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"high", "high", true},
		{"HIGH", "high", true},
		{" medium ", "medium", true},
		{"med", "medium", true},
		{"hi", "high", true},
		{"x", "xhigh", true},
		{"off", "off", true},
		{"m", "", false}, // minimal/medium — ambiguous
		{"z", "", false}, // no match
		{"", "", false},  // empty
		{"lo", "low", true},
	}
	for _, c := range cases {
		got, ok := resolveReasoningArg(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("resolveReasoningArg(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestResolveModelArg exercises the lenient model matcher: exact ids,
// case/dot-insensitive spellings, unique prefixes/substrings, and the
// multi-candidate disambiguation path.
func TestResolveModelArg(t *testing.T) {
	t.Parallel()
	models := []ModelInfo{
		{ID: "claude-opus-4.8", Ready: true},
		{ID: "claude-opus-4.6", Ready: true},
		{ID: "claude-sonnet-5", Ready: true},
		{ID: "gpt-5.5", Ready: true},
		{ID: "glm-5.2-fireworks", Ready: true},
		{ID: "not-ready", Ready: false},
	}
	cases := []struct {
		in       string
		wantID   string
		wantCand []string
	}{
		{"claude-opus-4.8", "claude-opus-4.8", nil}, // exact id
		{"CLAUDE-OPUS-4.8", "claude-opus-4.8", nil}, // case-insensitive
		{"claude-opus-4-8", "claude-opus-4.8", nil}, // dot/dash spelling
		{"sonnet", "claude-sonnet-5", nil},          // unique substring
		{"sonnet-5", "claude-sonnet-5", nil},        // unique substring
		{"glm", "glm-5.2-fireworks", nil},           // unique substring
		{"gpt", "gpt-5.5", nil},                     // unique prefix
		{"opus-4.8", "claude-opus-4.8", nil},        // unique substring
		{"nope", "", nil},                           // no match
		{"not-ready", "", nil},                      // present but not ready
	}
	for _, c := range cases {
		gotID, gotCand, _ := resolveModelArg(c.in, models)
		if gotID != c.wantID {
			t.Errorf("resolveModelArg(%q) id = %q, want %q", c.in, gotID, c.wantID)
		}
		if len(gotCand) != len(c.wantCand) {
			t.Errorf("resolveModelArg(%q) candidates = %v, want %v", c.in, gotCand, c.wantCand)
		}
	}

	// A partial matching several models returns candidates, not a single id.
	gotID, gotCand, _ := resolveModelArg("claude-opus", models)
	if gotID != "" || len(gotCand) != 2 {
		t.Fatalf("resolveModelArg(claude-opus) = (%q,%v), want ambiguous 2 candidates", gotID, gotCand)
	}
	gotID, gotCand, _ = resolveModelArg("claude", models)
	if gotID != "" || len(gotCand) != 3 {
		t.Fatalf("resolveModelArg(claude) = (%q,%v), want 3 candidates", gotID, gotCand)
	}
}

// TestModelCommandLevelPrefixVsSubstringModel guards against a false-ambiguity
// regression: a short unambiguous reasoning-level prefix ("o" -> off) must
// resolve as the level even when it incidentally appears as a substring inside
// exactly one ready model id. A weak substring model match must not override an
// explicit level. Exercised at the classify boundary via a custom catalog
// whose ready model contains "o".
func TestModelCommandLevelPrefixVsSubstringModel(t *testing.T) {
	t.Parallel()
	models := []ModelInfo{
		{ID: "gpt-5.5", Ready: true},
		{ID: "claude-opus-4.8", Ready: true}, // contains "o"
	}
	// "o" is a unique reasoning-level prefix (off) ...
	if level, ok := resolveReasoningArg("o"); !ok || level != "off" {
		t.Fatalf("resolveReasoningArg(o) = (%q,%v), want off,true", level, ok)
	}
	// ... and only a WEAK (substring) match against claude-opus-4.8.
	id, cand, strong := resolveModelArg("o", models)
	if id != "claude-opus-4.8" || strong || cand != nil {
		t.Fatalf("resolveModelArg(o) = (%q, %v, strong=%v), want weak unique claude-opus-4.8", id, cand, strong)
	}
}

// TestModelCommandPartialModel verifies a partial model name in /model switches
// to the uniquely-matching model end-to-end.
func TestModelCommandPartialModel(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// "model-b" via the partial "b" (unique substring among model-a/model-b).
	if w := postChat(t, srv, id, "/model b"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model b: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-b" {
		t.Fatalf("expected model-b via partial, got %v", updated.Model)
	}
}

// TestModelCommandAmbiguousPartial verifies a partial matching several models
// reports the candidates and does not switch.
func TestModelCommandAmbiguousPartial(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	// "model" is a substring of both model-a and model-b.
	if w := postChat(t, srv, id, "/model model"); w.Code != http.StatusAccepted {
		t.Fatalf("chat: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	updated, _ := database.GetConversationByID(ctx, id)
	if updated.Model == nil || *updated.Model != "model-a" {
		t.Fatalf("ambiguous partial must not switch, got %v", updated.Model)
	}
	mc := lastModelChange(listMessages(t, database, id))
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if !strings.Contains(ud.Text, "matches several models") {
		t.Fatalf("expected multi-candidate message, got %q", ud.Text)
	}
}

// TestModelChangeMarkerDisplayNames verifies a switch records the human-facing
// model display names for the UI to show.
func TestModelChangeMarkerDisplayNames(t *testing.T) {
	t.Parallel()
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()

	modelA := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &modelA, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	id := conv.ConversationID

	if w := postChat(t, srv, id, "/model model-b"); w.Code != http.StatusAccepted {
		t.Fatalf("chat /model model-b: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	mc := lastModelChange(listMessages(t, database, id))
	var ud ModelChangeUserData
	json.Unmarshal([]byte(*mc.UserData), &ud)
	if ud.FromDisplay != "Model A" || ud.ToDisplay != "Model B" {
		t.Fatalf("expected display names Model A->Model B, got %q->%q", ud.FromDisplay, ud.ToDisplay)
	}
	if !strings.Contains(ud.Text, "Model A") || !strings.Contains(ud.Text, "Model B") {
		t.Fatalf("summary should use display names, got %q", ud.Text)
	}
}

func TestChatMessageHookReceivesConversationReasoningLevel(t *testing.T) {
	srv, database := newTwoModelTestServer(t)
	ctx := context.Background()
	model := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{ThinkingLevel: "high"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	dumpFile := filepath.Join(t.TempDir(), "chat-message.json")
	hookPath := filepath.Join(srv.hooksDir, "chat-message")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ncat > "+dumpFile+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if w := postChat(t, srv, conv.ConversationID, "echo: hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read hook input: %v", err)
	}
	if !strings.Contains(string(data), `"reasoning_level":"high"`) {
		t.Fatalf("hook input missing reasoning level: %s", data)
	}
}

type defaultReasoningService struct {
	llm.Service
	level string
}

func (s defaultReasoningService) DefaultReasoningLevel() string { return s.level }

func TestChatMessageHookMaterializesDefaultReasoningLevel(t *testing.T) {
	srv, database := newTwoModelTestServer(t)
	srv.llmManager = &twoModelLLMManager{service: defaultReasoningService{
		Service: loop.NewPredictableService(),
		level:   "medium",
	}}
	ctx := context.Background()
	model := "model-a"
	conv, err := database.CreateConversation(ctx, nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	dumpFile := filepath.Join(t.TempDir(), "chat-message.json")
	hookPath := filepath.Join(srv.hooksDir, "chat-message")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ncat > "+dumpFile+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if w := postChat(t, srv, conv.ConversationID, "echo: hello"); w.Code != http.StatusAccepted {
		t.Fatalf("chat: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read hook input: %v", err)
	}
	if !strings.Contains(string(data), `"reasoning_level":"medium"`) {
		t.Fatalf("hook input missing materialized default reasoning level: %s", data)
	}
}
