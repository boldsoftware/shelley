package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

func TestProfileMentionParser(t *testing.T) {
	e := newProfileEnv(t)
	e.want(201, "POST", "/api/profiles", `{"name":"Code"}`)
	e.want(201, "POST", "/api/profiles", `{"name":"Code Review"}`)
	for _, tc := range []struct{ text, name string }{
		{"+code review hello", "Code Review"}, {"+CODE\thi", "Code"},
		{"+Code\nhi", "Code"}, {"+CodeReviewer hi", ""}, {"hi +Code", ""},
		{"+missing hi", ""}, {"+Code", ""},
	} {
		profile, err := e.srv.profileMention(t.Context(), tc.text)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if profile != nil {
			got = profile.Name
		}
		if got != tc.name {
			t.Errorf("%q: got %q, want %q", tc.text, got, tc.name)
		}
	}
}

func TestProfileMentionDoesNotChangeConversation(t *testing.T) {
	for _, first := range []bool{true, false} {
		t.Run(map[bool]string{true: "first message", false: "existing conversation"}[first], func(t *testing.T) {
			e := newProfileEnv(t)
			e.want(201, "POST", "/api/profiles", `{"name":"Probe","model":"model-b","thinking_level":"high","system_prompt":"TURN ONLY","tool_overrides":{"bash":"off"}}`)
			message := "hello"
			if first {
				message = "+probe hello"
			}
			var created struct {
				ConversationID string `json:"conversation_id"`
			}
			body, _ := json.Marshal(map[string]string{"message": message, "cwd": t.TempDir()})
			if err := json.Unmarshal([]byte(e.want(201, "POST", "/api/conversations/new", string(body))), &created); err != nil {
				t.Fatal(err)
			}
			id := created.ConversationID
			e.waitTurn(id, 0)
			path := "/api/conversation/" + id
			beforeSettings := e.want(200, "GET", path+"/settings", "")
			if !first {
				before := len(listMessages(t, e.database, id))
				e.want(202, "POST", path+"/chat", `{"message":"+pRoBe hello"}`)
				e.waitTurn(id, before)
			}
			req := e.lastTurnRequest()
			if systemText(req) != "TURN ONLY\n" || req.ThinkingLevel != llm.ThinkingLevelHigh || hasTool(req, "bash") {
				t.Fatalf("profile wasn't used: system=%q, reasoning=%v, bash=%v", systemText(req), req.ThinkingLevel, hasTool(req, "bash"))
			}
			if !strings.Contains(lastRequestUserText(req), "+") {
				t.Fatal("profile mention was hidden from the model")
			}
			if got := e.want(200, "GET", path+"/settings", ""); got != beforeSettings {
				t.Fatalf("saved settings changed: before=%s after=%s", beforeSettings, got)
			}
			before := len(listMessages(t, e.database, id))
			e.want(202, "POST", path+"/chat", `{"message":"ordinary followup"}`)
			e.waitTurn(id, before)
			req = e.lastTurnRequest()
			if strings.Contains(systemText(req), "TURN ONLY") || !hasTool(req, "bash") || req.ThinkingLevel != llm.ThinkingLevelDefault {
				t.Fatal("profile leaked into the next turn")
			}
			for _, row := range listMessages(t, e.database, id) {
				if row.Type == string(db.MessageTypeSystem) && row.LlmData != nil && strings.Contains(*row.LlmData, "TURN ONLY") {
					t.Fatal("profile prompt was saved as the conversation's system prompt")
				}
			}
		})
	}
}

type profileGate struct {
	llm.Service
	started chan struct{}
	release chan struct{}
}

func (g *profileGate) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if len(req.Tools) > 0 && strings.Contains(lastRequestUserText(req), "+Probe hold") {
		close(g.started)
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.Service.Do(ctx, req)
}

func TestProfileMentionIsolatesQueuedAndImmediateMessages(t *testing.T) {
	e := newProfileEnv(t)
	gate := &profileGate{Service: e.ps, started: make(chan struct{}), release: make(chan struct{})}
	e.srv.llmManager.(*twoModelLLMManager).service = gate
	e.want(201, "POST", "/api/profiles", `{"name":"Probe","system_prompt":"TURN ONLY"}`)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"+Probe hold","cwd":"`+t.TempDir()+`"}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ConversationID
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("profile request did not start")
	}
	e.want(http.StatusAccepted, "POST", "/api/conversation/"+id+"/chat", `{"message":"plain immediate"}`)
	e.want(http.StatusAccepted, "POST", "/api/conversation/"+id+"/chat", `{"message":"+Probe next","queue":true}`)
	e.want(http.StatusAccepted, "POST", "/api/conversation/"+id+"/chat", `{"message":"plain queued","queue":true}`)
	if q := queuedMessages(t, e.database, id); len(q) != 3 {
		t.Fatalf("messages escaped the turn boundary: %+v", q)
	}
	close(gate.release)
	waitFor(t, 5*time.Second, func() bool {
		n := 0
		for _, row := range listMessages(t, e.database, id) {
			if row.Type == "agent" && row.LlmData != nil && strings.Contains(*row.LlmData, `"EndOfTurn":true`) {
				n++
			}
		}
		return n == 4
	})
	var prompts []bool
	for _, req := range e.ps.GetRecentRequests() {
		if len(req.Tools) > 0 {
			prompts = append(prompts, strings.Contains(systemText(req), "TURN ONLY"))
		}
	}
	if len(prompts) != 4 || !prompts[0] || prompts[1] || !prompts[2] || prompts[3] {
		t.Fatalf("turn profiles = %v, want [true false true false]", prompts)
	}
}

func TestProfileMentionRetryAndMachineProvenance(t *testing.T) {
	e := newProfileEnv(t)
	failing := &gatingTestLLM{inner: e.ps, err: fmt.Errorf("503 Service Unavailable")}
	e.srv.llmManager.(*twoModelLLMManager).service = failing
	e.want(201, "POST", "/api/profiles", `{"name":"Probe","system_prompt":"TURN ONLY"}`)
	conv, err := e.database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"+Probe retry"}`)
	waitFor(t, 5*time.Second, func() bool {
		row, err := e.database.GetLatestActionableMessage(t.Context(), id)
		return err == nil && row.Type == "error" && !manager.IsAgentWorking()
	})
	failing.mu.Lock()
	failing.err = nil
	failing.mu.Unlock()
	before := len(listMessages(t, e.database, id))
	if err := manager.RetryLastLLMRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(id, before)
	if !strings.Contains(systemText(e.lastTurnRequest()), "TURN ONLY") {
		t.Fatal("Retry lost the one-turn profile")
	}
	before = len(listMessages(t, e.database, id))
	ctx := contextWithTurnUserData(t.Context(), senderMessageUserData{
		SenderConversationID: "child", SenderSlug: "child", SenderRelationship: senderRelationshipSubagent,
		Text: "+Probe notice",
	})
	if _, err := manager.AcceptUserMessage(ctx, failing, "model-a", llm.UserStringMessage("+Probe notice")); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(id, before)
	if strings.Contains(systemText(e.lastTurnRequest()), "TURN ONLY") {
		t.Fatal("a machine notice selected a profile")
	}
}

func TestProfileMentionQueuedFailureIsVisible(t *testing.T) {
	e := newProfileEnv(t)
	t.Cleanup(func() { stopActiveConversationLoops(e.srv) })
	gate := &profileGate{Service: e.ps, started: make(chan struct{}), release: make(chan struct{})}
	e.srv.llmManager.(*twoModelLLMManager).service = gate
	e.want(201, "POST", "/api/profiles", `{"name":"Probe"}`)
	e.want(201, "POST", "/api/profiles", `{"name":"Unavailable","model":"model-b"}`)
	var created struct {
		ConversationID string `json:"conversation_id"`
	}
	body := e.want(201, "POST", "/api/conversations/new", `{"message":"+Probe hold","cwd":"`+t.TempDir()+`"}`)
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ConversationID
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("profile request did not start")
	}
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.loopLifecycleMu.Lock()
	resolve := manager.profileModelService
	manager.profileModelService = func(model string) (llm.Service, error) {
		if model == "model-b" {
			return nil, fmt.Errorf("model-b is unavailable")
		}
		return resolve(model)
	}
	manager.loopLifecycleMu.Unlock()

	path := "/api/conversation/" + id
	e.want(202, "POST", path+"/chat", `{"message":"+Unavailable hello","queue":true}`)
	e.want(202, "POST", path+"/chat", `{"message":"ordinary followup","queue":true}`)
	close(gate.release)
	waitFor(t, 5*time.Second, func() bool {
		for _, row := range listMessages(t, e.database, id) {
			if row.Type == string(db.MessageTypeWarning) && row.UserData != nil &&
				strings.Contains(*row.UserData, "model-b is unavailable") &&
				strings.Contains(*row.UserData, "cancel the queued message") {
				if !row.ExcludedFromContext || row.LlmData != nil {
					t.Fatal("queue warning must be display-only")
				}
				return true
			}
		}
		return false
	})
	q := queuedMessages(t, e.database, id)
	if len(q) != 2 || userMessageRowExists(t, e.database, id, "+Unavailable hello") ||
		userMessageRowExists(t, e.database, id, "ordinary followup") {
		t.Fatal("failed profile must stay queued without dropping or bypassing messages")
	}
	before := len(listMessages(t, e.database, id))
	e.want(200, "POST", path+"/cancel-queued?queued_id="+q[0].ID, "")
	e.waitTurn(id, before)
	if len(queuedMessages(t, e.database, id)) != 0 ||
		!userMessageRowExists(t, e.database, id, "ordinary followup") {
		t.Fatal("cancelling the failed message must unblock the queue")
	}
}

func TestProfileMentionRefusalContinuesOnConversationModel(t *testing.T) {
	e := newProfileEnv(t)
	t.Cleanup(func() { stopActiveConversationLoops(e.srv) })
	e.srv.llmManager.(*twoModelLLMManager).service = &refuseThenOKService{inner: e.ps}
	e.want(201, "POST", "/api/profiles", `{"name":"Probe","model":"model-b","system_prompt":"TURN ONLY","tool_overrides":{"bash":"off"}}`)
	conv, err := e.database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id := conv.ConversationID
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	e.want(202, "POST", "/api/conversation/"+id+"/chat", `{"message":"+Probe hello"}`)
	waitFor(t, 5*time.Second, func() bool {
		row, err := e.database.GetLatestActionableMessage(t.Context(), id)
		return err == nil && row.Type == "error" && row.UserData != nil &&
			strings.Contains(*row.UserData, `"error_type":"refusal"`) && !manager.IsAgentWorking()
	})
	before := len(listMessages(t, e.database, id))
	err = manager.ContinueAfterRefusal(t.Context(), e.srv.getModelList(),
		func(cur ConversationSettings) (ConversationSettings, error) { return cur, nil },
		e.ps, "model-a")
	if err != nil {
		t.Fatal(err)
	}
	e.waitTurn(id, before)
	req := e.lastTurnRequest()
	if strings.Contains(systemText(req), "TURN ONLY") || !hasTool(req, "bash") {
		t.Fatal("continuing on the conversation model kept the profile override")
	}
}

func TestProfileMentionInterruptedResumeDoesNotFallback(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("upgrade=%v", upgrade), func(t *testing.T) {
			srv, database, service := newTestServer(t)
			t.Cleanup(func() { stopActiveConversationLoops(srv) })
			id := seedInterruptedConversation(t, database, nil)
			if _, err := database.CreateMessage(t.Context(), db.CreateMessageParams{
				ConversationID: id, Type: db.MessageTypeProfileTurn, ExcludedFromContext: true,
				UserData: map[string]string{"profile_to": "Probe", "profile_mention_phase": "start"},
			}); err != nil {
				t.Fatal(err)
			}
			var resume db.UpgradeResume
			if upgrade {
				resume = reserveUpgradeResume(t, database, id)
			} else if _, err := database.ConsumeResumeAfterUpgrade(t.Context()); err != nil {
				t.Fatal(err)
			}
			manager, err := srv.getOrCreateConversationManager(t.Context(), id, "")
			if err != nil {
				t.Fatal(err)
			}
			resolve := func(string) (llm.Service, error) {
				t.Error("interrupted profile must not resolve the conversation model")
				return service, nil
			}
			if upgrade {
				err = manager.ResumeInterruptedTurnAfterUpgrade(t.Context(), resume, "predictable", resolve, resumeWarningText)
			} else {
				err = manager.ContinueInterruptedTurn(t.Context(), "predictable", resolve)
			}
			if !errors.Is(err, errProfileTurnStopped) {
				t.Fatalf("resume = %v, want profile stopped", err)
			}
			conv, err := database.GetConversationByID(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			rows := listMessages(t, database, id)
			if conv.AgentWorking || !conv.TurnInterrupted || manager.IsAgentWorking() || countByType(rows, db.MessageTypeWarning) != 0 ||
				countByType(rows, db.MessageTypeAgent) != 0 {
				t.Fatal("interrupted profile must stay stopped without a warning or fallback turn")
			}
			sendChat(t, srv, id, "echo: ordinary followup", false)
			waitFor(t, 5*time.Second, func() bool {
				return countByType(listMessages(t, database, id), db.MessageTypeAgent) == 1 && !manager.IsAgentWorking()
			})
		})
	}
}

func TestProfileMentionRetryAfterSourceRefreshDoesNotFallback(t *testing.T) {
	e := newProfileEnv(t)
	t.Cleanup(func() { stopActiveConversationLoops(e.srv) })
	failing := &gatingTestLLM{inner: e.ps, err: fmt.Errorf("503 Service Unavailable")}
	e.srv.llmManager.(*twoModelLLMManager).service = failing
	e.want(201, "POST", "/api/profiles", `{"name":"Probe","system_prompt":"TURN ONLY"}`)
	conv, err := e.database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := e.srv.getOrCreateConversationManager(t.Context(), conv.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	manager.resolveModelService = func(string) (llm.Service, string, uint64, error) {
		return failing, "fixture", 1, nil
	}
	e.want(202, "POST", "/api/conversation/"+conv.ConversationID+"/chat", `{"message":"+Probe retry"}`)
	waitFor(t, 5*time.Second, func() bool {
		row, err := e.database.GetLatestActionableMessage(t.Context(), conv.ConversationID)
		return err == nil && row.Type == "error" && !manager.IsAgentWorking()
	})
	manager.loopLifecycleMu.Lock()
	manager.resolveModelService = func(string) (llm.Service, string, uint64, error) {
		return failing, "fixture", 2, nil
	}
	err = manager.ensureLoopLocked(failing, "model-a")
	manager.loopLifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RetryLastLLMRequest(t.Context()); !errors.Is(err, errProfileTurnStopped) {
		t.Fatalf("retry = %v, want profile stopped after losing the override", err)
	}
	if manager.IsAgentWorking() {
		t.Fatal("Retry started a baseline turn after losing the profile override")
	}
}
