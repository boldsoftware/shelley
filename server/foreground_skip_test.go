package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

type foregroundSkipService struct {
	mu       sync.Mutex
	batches  [][]llm.Content
	requests []*llm.Request
	block    int
	seen     chan *llm.Request
}

func (*foregroundSkipService) Provider() string       { return "builtin" }
func (*foregroundSkipService) SupportsImages() bool   { return false }
func (*foregroundSkipService) MaxImageDimension() int { return 0 }
func (*foregroundSkipService) MaxImageBytes() int     { return 0 }
func (s *foregroundSkipService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if len(req.Tools) == 0 {
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("Done.")}, nil
	}
	s.mu.Lock()
	n := len(s.requests)
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.seen != nil {
		s.seen <- req
	}
	if n == s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if n < len(s.batches) && len(s.batches[n]) != 0 {
		return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse, Content: s.batches[n]}, nil
	}
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("Done.")}, nil
}

func foregroundCall(id, action string) llm.Content {
	return llm.Content{Type: llm.ContentTypeToolUse, ID: id, ToolName: claudetool.CompactInPlaceName, ToolInput: json.RawMessage(fmt.Sprintf(`{"action":%q}`, action))}
}

func foregroundCheck(t *testing.T, c *compactTestConversation, messages []llm.Message, expected ...llm.Content) {
	t.Helper()
	calls, results, stored := map[string]llm.Content{}, map[string]llm.Content{}, map[string]llm.Content{}
	for _, m := range c.rows() {
		for _, ct := range m.Content {
			if ct.Type == llm.ContentTypeToolResult {
				stored[ct.ToolUseID] = ct
			}
		}
	}
	for _, m := range messages {
		for _, ct := range m.Content {
			if ct.Type == llm.ContentTypeToolUse {
				calls[ct.ID] = ct
			}
			if ct.Type == llm.ContentTypeToolResult {
				results[ct.ToolUseID] = ct
			}
		}
	}
	for _, want := range expected {
		if got, ok := calls[want.ID]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("call %s changed or missing: %+v", want.ID, got)
		}
		got, ok := results[want.ID]
		original, exists := stored[want.ID]
		if !ok || !exists || got.ToolError || original.ToolError || len(got.ToolResult) != 1 || len(original.ToolResult) != 1 ||
			got.ToolResult[0].Type != original.ToolResult[0].Type || got.ToolResult[0].Text != original.ToolResult[0].Text {
			t.Errorf("result %s is missing, failed, or has a changed payload", want.ID)
			continue
		}
		if string(want.ToolInput) == `{"action":"index"}` && (len(got.ToolResult) != 1 || !strings.Contains(got.ToolResult[0].Text, "## Index")) {
			t.Errorf("index %s is not full: %+v", want.ID, got)
		}
	}
	if n := c.records(); n != 0 {
		t.Errorf("foreground skip wrote %d compaction records", n)
	}
}

func foregroundReplay(t *testing.T, cm *ConversationManager) []llm.Message {
	t.Helper()
	items, _, err := cm.loadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var msgs []llm.Message
	for _, it := range items {
		msgs = append(msgs, it.message)
	}
	return msgs
}

func TestForegroundSkipEvidence(t *testing.T) {
	index, skip1, skip2 := foregroundCall("index", "index"), foregroundCall("skip1", "skip"), foregroundCall("skip2", "skip")
	bash := llm.Content{Type: llm.ContentTypeToolUse, ID: "bash", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"printf BATCH_SIBLING","background":false}`)}
	for _, tc := range []struct {
		name     string
		batches  [][]llm.Content
		expected []llm.Content
	}{
		{"sequential", [][]llm.Content{{index}, {skip1}, {skip2}}, []llm.Content{index, skip1, skip2}},
		{"batch-sibling", [][]llm.Content{{index, skip1, bash}}, []llm.Content{index, skip1, bash}},
		{"prior-index-reverse-batch", [][]llm.Content{{index}, {skip1, foregroundCall("index2", "index")}}, []llm.Content{index, skip1, foregroundCall("index2", "index")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCompactTestConversation(t, db.ConversationOptions{})
			c.turn("echo: " + strings.Repeat("COMPACT_ME ", 150))
			svc := &foregroundSkipService{batches: tc.batches, block: -1}
			c.srv.llmManager.(*twoModelLLMManager).service = svc
			cm, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
			if err != nil {
				t.Fatal(err)
			}
			cm.ResetLoop()
			defer cm.ResetLoop()
			c.turn("Index and skip.")
			foregroundCheck(t, c, svc.requests[len(tc.batches)].Messages, tc.expected...)
			foregroundCheck(t, c, foregroundReplay(t, cm), tc.expected...)
		})
	}
}

func TestForegroundSkipResume(t *testing.T) {
	for _, block := range []int{1, 2} {
		t.Run(fmt.Sprint(block), func(t *testing.T) {
			c := newCompactTestConversation(t, db.ConversationOptions{})
			c.turn("echo: " + strings.Repeat("COMPACT_ME ", 150))
			index, first, second := foregroundCall("index", "index"), foregroundCall("first", "skip"), foregroundCall("second", "skip")
			batches := make([][]llm.Content, block+2)
			batches[0] = []llm.Content{index}
			if block == 2 {
				batches[1] = []llm.Content{first}
			}
			batches[block+1] = []llm.Content{second}
			svc := &foregroundSkipService{batches: batches, block: block, seen: make(chan *llm.Request, 8)}
			c.srv.llmManager.(*twoModelLLMManager).service = svc
			cm, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
			if err != nil {
				t.Fatal(err)
			}
			cm.ResetLoop()
			defer cm.ResetLoop()
			if w := postChat(t, c.srv, c.id, "Index and decide."); w.Code != http.StatusAccepted {
				t.Fatalf("chat: %d %s", w.Code, w.Body.String())
			}
			<-svc.seen
			blocked := <-svc.seen
			if block == 2 {
				blocked = <-svc.seen
			}
			want := []llm.Content{index}
			if block == 2 {
				want = append(want, first)
			}
			foregroundCheck(t, c, blocked.Messages, want...)
			cm.ResetLoop()
			if err := cm.ResumeInterruptedTurn(t.Context(), svc, "model-a"); err != nil {
				t.Fatal(err)
			}
			resumed := <-svc.seen
			foregroundCheck(t, c, resumed.Messages, want...)
			after := <-svc.seen
			want = append(want, second)
			foregroundCheck(t, c, after.Messages, want...)
			foregroundCheck(t, c, foregroundReplay(t, cm), want...)
		})
	}
}
