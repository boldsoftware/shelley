package ant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestIsClaudeModel(t *testing.T) {
	tests := []struct {
		name     string
		userName string
		want     bool
	}{
		{"claude model", "claude", true},
		{"sonnet model", "sonnet", true},
		{"opus model", "opus", true},
		{"unknown model", "gpt-4", false},
		{"empty string", "", false},
		{"random string", "random", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeModel(tt.userName); got != tt.want {
				t.Errorf("IsClaudeModel(%q) = %v, want %v", tt.userName, got, tt.want)
			}
		})
	}
}

func TestClaudeModelName(t *testing.T) {
	tests := []struct {
		name     string
		userName string
		want     string
	}{
		{"claude model", "claude", Claude46Sonnet},
		{"sonnet model", "sonnet", Claude46Sonnet},
		{"opus model", "opus", Claude46Opus},
		{"unknown model", "gpt-4", ""},
		{"empty string", "", ""},
		{"random string", "random", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudeModelName(tt.userName); got != tt.want {
				t.Errorf("ClaudeModelName(%q) = %v, want %v", tt.userName, got, tt.want)
			}
		})
	}
}

func TestTokenContextWindow(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  int
	}{
		{"default model", "", 200000},
		{"Claude46Sonnet", Claude46Sonnet, 200000},
		{"Claude45Haiku", Claude45Haiku, 200000},
		{"Claude45Opus", Claude45Opus, 200000},
		{"unknown model", "unknown-model", 200000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{Model: tt.model}
			if got := s.TokenContextWindow(); got != tt.want {
				t.Errorf("TokenContextWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaxImageDimension(t *testing.T) {
	s := &Service{}
	want := 2000
	if got := s.MaxImageDimension(); got != want {
		t.Errorf("MaxImageDimension() = %v, want %v", got, want)
	}
}

func TestToLLMUsage(t *testing.T) {
	tests := []struct {
		name string
		u    usage
		want llm.Usage
	}{
		{
			name: "empty usage",
			u:    usage{},
			want: llm.Usage{},
		},
		{
			name: "full usage",
			u: usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     25,
				OutputTokens:             200,
				CostUSD:                  0.05,
			},
			want: llm.Usage{
				InputTokens:              100,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     25,
				OutputTokens:             200,
				CostUSD:                  0.05,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toLLMUsage(tt.u)
			if got != tt.want {
				t.Errorf("toLLMUsage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToLLMContent(t *testing.T) {
	text := "hello world"
	tests := []struct {
		name string
		c    content
		want llm.Content
	}{
		{
			name: "text content",
			c: content{
				Type: "text",
				Text: &text,
			},
			want: llm.Content{
				Type: llm.ContentTypeText,
				Text: "hello world",
			},
		},
		{
			name: "thinking content",
			c: content{
				Type:      "thinking",
				Thinking:  strp("thinking content"),
				Signature: "signature",
			},
			want: llm.Content{
				Type:      llm.ContentTypeThinking,
				Thinking:  "thinking content",
				Signature: "signature",
			},
		},
		{
			name: "redacted thinking content",
			c: content{
				Type:      "redacted_thinking",
				Data:      "redacted data",
				Signature: "signature",
			},
			want: llm.Content{
				Type:      llm.ContentTypeRedactedThinking,
				Data:      "redacted data",
				Signature: "signature",
			},
		},
		{
			name: "tool use content",
			c: content{
				Type:      "tool_use",
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command":"ls"}`),
			},
			want: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command":"ls"}`),
			},
		},
		{
			name: "tool result content",
			c: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
			want: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toLLMContent(tt.c)
			if got.Type != tt.want.Type {
				t.Errorf("toLLMContent().Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.Text != tt.want.Text {
				t.Errorf("toLLMContent().Text = %v, want %v", got.Text, tt.want.Text)
			}
			if got.Thinking != tt.want.Thinking {
				t.Errorf("toLLMContent().Thinking = %v, want %v", got.Thinking, tt.want.Thinking)
			}
			if got.Signature != tt.want.Signature {
				t.Errorf("toLLMContent().Signature = %v, want %v", got.Signature, tt.want.Signature)
			}
			if got.Data != tt.want.Data {
				t.Errorf("toLLMContent().Data = %v, want %v", got.Data, tt.want.Data)
			}
			if got.ID != tt.want.ID {
				t.Errorf("toLLMContent().ID = %v, want %v", got.ID, tt.want.ID)
			}
			if got.ToolName != tt.want.ToolName {
				t.Errorf("toLLMContent().ToolName = %v, want %v", got.ToolName, tt.want.ToolName)
			}
			if string(got.ToolInput) != string(tt.want.ToolInput) {
				t.Errorf("toLLMContent().ToolInput = %v, want %v", string(got.ToolInput), string(tt.want.ToolInput))
			}
			if got.ToolUseID != tt.want.ToolUseID {
				t.Errorf("toLLMContent().ToolUseID = %v, want %v", got.ToolUseID, tt.want.ToolUseID)
			}
			if got.ToolError != tt.want.ToolError {
				t.Errorf("toLLMContent().ToolError = %v, want %v", got.ToolError, tt.want.ToolError)
			}
		})
	}
}

func TestToLLMResponse(t *testing.T) {
	text := "Hello, world!"
	resp := &response{
		ID:         "msg_123",
		Type:       "message",
		Role:       "assistant",
		Model:      Claude46Sonnet,
		Content:    []content{{Type: "text", Text: &text}},
		StopReason: "end_turn",
		Usage: usage{
			InputTokens:  100,
			OutputTokens: 50,
			CostUSD:      0.01,
		},
	}

	got := toLLMResponse(resp)
	if got.ID != "msg_123" {
		t.Errorf("toLLMResponse().ID = %v, want %v", got.ID, "msg_123")
	}
	if got.Type != "message" {
		t.Errorf("toLLMResponse().Type = %v, want %v", got.Type, "message")
	}
	if got.Role != llm.MessageRoleAssistant {
		t.Errorf("toLLMResponse().Role = %v, want %v", got.Role, llm.MessageRoleAssistant)
	}
	if got.Model != Claude46Sonnet {
		t.Errorf("toLLMResponse().Model = %v, want %v", got.Model, Claude46Sonnet)
	}
	if len(got.Content) != 1 {
		t.Errorf("toLLMResponse().Content length = %v, want %v", len(got.Content), 1)
	}
	if got.Content[0].Type != llm.ContentTypeText {
		t.Errorf("toLLMResponse().Content[0].Type = %v, want %v", got.Content[0].Type, llm.ContentTypeText)
	}
	if got.Content[0].Text != "Hello, world!" {
		t.Errorf("toLLMResponse().Content[0].Text = %v, want %v", got.Content[0].Text, "Hello, world!")
	}
	if got.StopReason != llm.StopReasonEndTurn {
		t.Errorf("toLLMResponse().StopReason = %v, want %v", got.StopReason, llm.StopReasonEndTurn)
	}
	if got.Usage.InputTokens != 100 {
		t.Errorf("toLLMResponse().Usage.InputTokens = %v, want %v", got.Usage.InputTokens, 100)
	}
	if got.Usage.OutputTokens != 50 {
		t.Errorf("toLLMResponse().Usage.OutputTokens = %v, want %v", got.Usage.OutputTokens, 50)
	}
	if got.Usage.CostUSD != 0.01 {
		t.Errorf("toLLMResponse().Usage.CostUSD = %v, want %v", got.Usage.CostUSD, 0.01)
	}
}

func TestFromLLMToolUse(t *testing.T) {
	tests := []struct {
		name string
		tu   *llm.ToolUse
		want *toolUse
	}{
		{
			name: "nil tool use",
			tu:   nil,
			want: nil,
		},
		{
			name: "valid tool use",
			tu: &llm.ToolUse{
				ID:   "tool-id",
				Name: "bash",
			},
			want: &toolUse{
				ID:   "tool-id",
				Name: "bash",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromLLMToolUse(tt.tu)
			if tt.want == nil && got != nil {
				t.Errorf("fromLLMToolUse() = %v, want nil", got)
			} else if tt.want != nil && got == nil {
				t.Errorf("fromLLMToolUse() = nil, want %v", tt.want)
			} else if tt.want != nil && got != nil {
				if got.ID != tt.want.ID || got.Name != tt.want.Name {
					t.Errorf("fromLLMToolUse() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestFromLLMMessage(t *testing.T) {
	text := "Hello, world!"
	msg := llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{
				Type: llm.ContentTypeText,
				Text: text,
			},
		},
		ToolUse: &llm.ToolUse{
			ID:   "tool-id",
			Name: "bash",
		},
	}

	got := fromLLMMessage(msg)
	if got.Role != "assistant" {
		t.Errorf("fromLLMMessage().Role = %v, want %v", got.Role, "assistant")
	}
	if len(got.Content) != 1 {
		t.Errorf("fromLLMMessage().Content length = %v, want %v", len(got.Content), 1)
	}
	if got.Content[0].Type != "text" {
		t.Errorf("fromLLMMessage().Content[0].Type = %v, want %v", got.Content[0].Type, "text")
	}
	if *got.Content[0].Text != text {
		t.Errorf("fromLLMMessage().Content[0].Text = %v, want %v", *got.Content[0].Text, text)
	}
	if got.ToolUse == nil {
		t.Errorf("fromLLMMessage().ToolUse = nil, want not nil")
	} else {
		if got.ToolUse.ID != "tool-id" {
			t.Errorf("fromLLMMessage().ToolUse.ID = %v, want %v", got.ToolUse.ID, "tool-id")
		}
		if got.ToolUse.Name != "bash" {
			t.Errorf("fromLLMMessage().ToolUse.Name = %v, want %v", got.ToolUse.Name, "bash")
		}
	}
}

func TestFromLLMMessageSkipsCorruptThinking(t *testing.T) {
	// A thinking block with no signature is corrupt and should be skipped.
	msg := fromLLMMessage(llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "", Signature: ""},
		},
	})
	if len(msg.Content) != 0 {
		t.Errorf("expected corrupt thinking block to be skipped, got %d content blocks", len(msg.Content))
	}

	// A thinking block WITH a signature should be kept.
	msg = fromLLMMessage(llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "", Signature: "sig"},
			{Type: llm.ContentTypeText, Text: "hello"},
		},
	})
	if len(msg.Content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(msg.Content))
	}
}

func TestFromLLMRequestSkipsEmptyMessages(t *testing.T) {
	s := &Service{Model: "claude-sonnet-4-6"}
	req := s.fromLLMRequest(&llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "", Signature: ""},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "hello"},
			}},
		},
	})
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message after filtering, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("expected remaining message to be user, got %s", req.Messages[0].Role)
	}
}

func TestFromLLMToolChoice(t *testing.T) {
	tests := []struct {
		name string
		tc   *llm.ToolChoice
		want *toolChoice
	}{
		{
			name: "nil tool choice",
			tc:   nil,
			want: nil,
		},
		{
			name: "auto tool choice",
			tc: &llm.ToolChoice{
				Type: llm.ToolChoiceTypeAuto,
			},
			want: &toolChoice{
				Type: "auto",
			},
		},
		{
			name: "tool tool choice",
			tc: &llm.ToolChoice{
				Type: llm.ToolChoiceTypeTool,
				Name: "bash",
			},
			want: &toolChoice{
				Type: "tool",
				Name: "bash",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromLLMToolChoice(tt.tc)
			if tt.want == nil && got != nil {
				t.Errorf("fromLLMToolChoice() = %v, want nil", got)
			} else if tt.want != nil && got == nil {
				t.Errorf("fromLLMToolChoice() = nil, want %v", tt.want)
			} else if tt.want != nil && got != nil {
				if got.Type != tt.want.Type {
					t.Errorf("fromLLMToolChoice().Type = %v, want %v", got.Type, tt.want.Type)
				}
				if got.Name != tt.want.Name {
					t.Errorf("fromLLMToolChoice().Name = %v, want %v", got.Name, tt.want.Name)
				}
			}
		})
	}
}

func TestFromLLMTool(t *testing.T) {
	tool := &llm.Tool{
		Name:        "bash",
		Description: "Execute bash commands",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Cache:       true,
	}

	got := fromLLMTool(tool)
	if got.Name != "bash" {
		t.Errorf("fromLLMTool().Name = %v, want %v", got.Name, "bash")
	}
	if got.Description != "Execute bash commands" {
		t.Errorf("fromLLMTool().Description = %v, want %v", got.Description, "Execute bash commands")
	}
	if string(got.InputSchema) != `{"type":"object"}` {
		t.Errorf("fromLLMTool().InputSchema = %v, want %v", string(got.InputSchema), `{"type":"object"}`)
	}
	if string(got.CacheControl) != `{"type":"ephemeral"}` {
		t.Errorf("fromLLMTool().CacheControl = %v, want %v", string(got.CacheControl), `{"type":"ephemeral"}`)
	}
}

func TestFromLLMSystem(t *testing.T) {
	sys := llm.SystemContent{
		Text:  "You are a helpful assistant",
		Type:  "text",
		Cache: true,
	}

	got := fromLLMSystem(sys)
	if got.Text != "You are a helpful assistant" {
		t.Errorf("fromLLMSystem().Text = %v, want %v", got.Text, "You are a helpful assistant")
	}
	if got.Type != "text" {
		t.Errorf("fromLLMSystem().Type = %v, want %v", got.Type, "text")
	}
	if string(got.CacheControl) != `{"type":"ephemeral"}` {
		t.Errorf("fromLLMSystem().CacheControl = %v, want %v", string(got.CacheControl), `{"type":"ephemeral"}`)
	}
}

func TestMapped(t *testing.T) {
	// Test the mapped function with a simple example
	input := []int{1, 2, 3, 4, 5}
	expected := []int{2, 4, 6, 8, 10}

	got := mapped(input, func(x int) int { return x * 2 })

	if len(got) != len(expected) {
		t.Errorf("mapped() length = %v, want %v", len(got), len(expected))
	}

	for i, v := range got {
		if v != expected[i] {
			t.Errorf("mapped()[%d] = %v, want %v", i, v, expected[i])
		}
	}
}

func TestUsageAdd(t *testing.T) {
	u1 := usage{
		InputTokens:              100,
		CacheCreationInputTokens: 50,
		CacheReadInputTokens:     25,
		OutputTokens:             200,
		CostUSD:                  0.05,
	}

	u2 := usage{
		InputTokens:              150,
		CacheCreationInputTokens: 75,
		CacheReadInputTokens:     30,
		OutputTokens:             300,
		CostUSD:                  0.07,
	}

	u1.Add(u2)

	if u1.InputTokens != 250 {
		t.Errorf("usage.Add() InputTokens = %v, want %v", u1.InputTokens, 250)
	}
	if u1.CacheCreationInputTokens != 125 {
		t.Errorf("usage.Add() CacheCreationInputTokens = %v, want %v", u1.CacheCreationInputTokens, 125)
	}
	if u1.CacheReadInputTokens != 55 {
		t.Errorf("usage.Add() CacheReadInputTokens = %v, want %v", u1.CacheReadInputTokens, 55)
	}
	if u1.OutputTokens != 500 {
		t.Errorf("usage.Add() OutputTokens = %v, want %v", u1.OutputTokens, 500)
	}

	// Use a small epsilon for floating point comparison
	const epsilon = 1e-10
	expectedCost := 0.12
	if abs(u1.CostUSD-expectedCost) > epsilon {
		t.Errorf("usage.Add() CostUSD = %v, want %v", u1.CostUSD, expectedCost)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestFromLLMRequestStripsOldThinkingBlocks(t *testing.T) {
	s := &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelMedium}

	// Simulate a conversation with multiple assistant turns containing thinking blocks.
	// Only the last assistant turn's thinking should be preserved.
	req := s.fromLLMRequest(&llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "first question"},
			}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "old thinking", Signature: "old-sig-1"},
				{Type: llm.ContentTypeText, Text: "first answer"},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "second question"},
			}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "old thinking 2", Signature: "old-sig-2"},
				{Type: llm.ContentTypeRedactedThinking, Data: "redacted", Signature: "old-sig-3"},
				{Type: llm.ContentTypeText, Text: "second answer"},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "third question"},
			}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "latest thinking", Signature: "valid-sig"},
				{Type: llm.ContentTypeText, Text: "third answer"},
				{Type: llm.ContentTypeToolUse, ID: "tool1", ToolName: "bash", ToolInput: json.RawMessage(`{}`)},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeToolResult, ToolUseID: "tool1", ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "output"}}},
			}},
		},
	})

	// Should have 7 messages (no messages dropped)
	if len(req.Messages) != 7 {
		t.Fatalf("expected 7 messages, got %d", len(req.Messages))
	}

	// First assistant (index 1): thinking should be stripped, only text remains
	firstAssistant := req.Messages[1]
	if len(firstAssistant.Content) != 1 {
		t.Errorf("first assistant: expected 1 content block, got %d", len(firstAssistant.Content))
	}
	if firstAssistant.Content[0].Type != "text" {
		t.Errorf("first assistant content[0]: expected text, got %s", firstAssistant.Content[0].Type)
	}

	// Second assistant (index 3): thinking + redacted_thinking stripped, only text remains
	secondAssistant := req.Messages[3]
	if len(secondAssistant.Content) != 1 {
		t.Errorf("second assistant: expected 1 content block, got %d", len(secondAssistant.Content))
	}

	// Last assistant (index 5): thinking preserved
	lastAssistant := req.Messages[5]
	if len(lastAssistant.Content) != 3 {
		t.Errorf("last assistant: expected 3 content blocks, got %d", len(lastAssistant.Content))
	}
	if lastAssistant.Content[0].Type != "thinking" {
		t.Errorf("last assistant content[0]: expected thinking, got %s", lastAssistant.Content[0].Type)
	}
	if lastAssistant.Content[0].Signature != "valid-sig" {
		t.Errorf("last assistant thinking signature not preserved")
	}
}

func TestFromLLMRequest(t *testing.T) {
	s := &Service{
		Model:     Claude46Sonnet,
		MaxTokens: 1000,
	}

	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, world!",
					},
				},
			},
		},
		ToolChoice: &llm.ToolChoice{
			Type: llm.ToolChoiceTypeAuto,
		},
		Tools: []*llm.Tool{
			{
				Name:        "bash",
				Description: "Execute bash commands",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		System: []llm.SystemContent{
			{
				Text: "You are a helpful assistant",
			},
		},
	}

	got := s.fromLLMRequest(req)

	if got.Model != Claude46Sonnet {
		t.Errorf("fromLLMRequest().Model = %v, want %v", got.Model, Claude46Sonnet)
	}
	if got.MaxTokens != 1000 {
		t.Errorf("fromLLMRequest().MaxTokens = %v, want %v", got.MaxTokens, 1000)
	}
	if len(got.Messages) != 1 {
		t.Errorf("fromLLMRequest().Messages length = %v, want %v", len(got.Messages), 1)
	}
	if got.ToolChoice == nil {
		t.Errorf("fromLLMRequest().ToolChoice = nil, want not nil")
	} else if got.ToolChoice.Type != "auto" {
		t.Errorf("fromLLMRequest().ToolChoice.Type = %v, want %v", got.ToolChoice.Type, "auto")
	}
	if len(got.Tools) != 1 {
		t.Errorf("fromLLMRequest().Tools length = %v, want %v", len(got.Tools), 1)
	} else if got.Tools[0].Name != "bash" {
		t.Errorf("fromLLMRequest().Tools[0].Name = %v, want %v", got.Tools[0].Name, "bash")
	}
	if len(got.System) != 1 {
		t.Errorf("fromLLMRequest().System length = %v, want %v", len(got.System), 1)
	} else if got.System[0].Text != "You are a helpful assistant" {
		t.Errorf("fromLLMRequest().System[0].Text = %v, want %v", got.System[0].Text, "You are a helpful assistant")
	}
}

func TestMaxOutputTokensCapping(t *testing.T) {
	simpleReq := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
		}},
	}

	// Opus 4.5 has a 64k limit — setting MaxTokens above must be capped
	s := &Service{Model: Claude45Opus, MaxTokens: 100000, ThinkingLevel: llm.ThinkingLevelMedium}
	got := s.fromLLMRequest(simpleReq)
	if got.MaxTokens != 64000 {
		t.Errorf("Opus 4.5: MaxTokens = %d, want 64000", got.MaxTokens)
	}
	if got.Thinking != nil && got.Thinking.BudgetTokens >= got.MaxTokens {
		t.Errorf("Opus 4.5: BudgetTokens (%d) >= MaxTokens (%d)", got.Thinking.BudgetTokens, got.MaxTokens)
	}

	// Opus 4.6 has a 128k limit — 100000 should pass through
	s2 := &Service{Model: Claude46Opus, MaxTokens: 100000}
	got2 := s2.fromLLMRequest(simpleReq)
	if got2.MaxTokens != 100000 {
		t.Errorf("Opus 4.6: MaxTokens = %d, want 100000", got2.MaxTokens)
	}

	// Sonnet 4.6 has a 64k limit — 50000 should pass through
	s3 := &Service{Model: Claude46Sonnet, MaxTokens: 50000}
	got3 := s3.fromLLMRequest(simpleReq)
	if got3.MaxTokens != 50000 {
		t.Errorf("Sonnet 4.6: MaxTokens = %d, want 50000", got3.MaxTokens)
	}

	// Sonnet 4.6 with MaxTokens above 64k must be capped
	s4 := &Service{Model: Claude46Sonnet, MaxTokens: 200000}
	got4 := s4.fromLLMRequest(simpleReq)
	if got4.MaxTokens != 64000 {
		t.Errorf("Sonnet 4.6 capped: MaxTokens = %d, want 64000", got4.MaxTokens)
	}
}

// TestMaxOutputTokensMatchModelsDevAPI validates our maxOutputTokens() values against
// the live models.dev API (same pattern as llmpricing.TestPricingMatchesModelsDev).
func TestMaxOutputTokensMatchModelsDevAPI(t *testing.T) {
	resp, err := http.Get("https://models.dev/api.json")
	if err != nil {
		t.Skipf("Failed to fetch models.dev API: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Skipf("models.dev API returned status %d", resp.StatusCode)
	}

	type ModelInfo struct {
		Limit struct {
			Output int `json:"output"`
		} `json:"limit"`
	}
	type ProviderInfo struct {
		Models map[string]ModelInfo `json:"models"`
	}
	var apiData map[string]ProviderInfo
	if err := json.NewDecoder(resp.Body).Decode(&apiData); err != nil {
		t.Fatalf("Failed to decode models.dev API: %v", err)
	}

	anthropic, ok := apiData["anthropic"]
	if !ok {
		t.Fatal("anthropic provider not found in models.dev API")
	}

	// Every model constant we define must match models.dev
	for _, model := range []string{
		Claude45Haiku,
		Claude45Opus,
		Claude46Opus,
		Claude46Sonnet,
	} {
		apiModel, ok := anthropic.Models[model]
		if !ok {
			t.Errorf("%s: not found in models.dev data", model)
			continue
		}
		svc := &Service{Model: model}
		got := svc.maxOutputTokens()
		if got != apiModel.Limit.Output {
			t.Errorf("%s: maxOutputTokens() = %d, models.dev says %d", model, got, apiModel.Limit.Output)
		}
	}
}

func TestConfigDetails(t *testing.T) {
	tests := []struct {
		name    string
		service *Service
		want    map[string]string
	}{
		{
			name: "default values",
			service: &Service{
				APIKey: "test-key",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "true",
			},
		},
		{
			name: "custom values",
			service: &Service{
				URL:    "https://custom.anthropic.com/v1/messages",
				Model:  Claude45Opus,
				APIKey: "test-key",
			},
			want: map[string]string{
				"url":             "https://custom.anthropic.com/v1/messages",
				"model":           Claude45Opus,
				"has_api_key_set": "true",
			},
		},
		{
			name: "no api key",
			service: &Service{
				APIKey: "",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.service.ConfigDetails()
			for key, wantValue := range tt.want {
				if gotValue, ok := got[key]; !ok {
					t.Errorf("ConfigDetails() missing key %q", key)
				} else if gotValue != wantValue {
					t.Errorf("ConfigDetails()[%q] = %v, want %v", key, gotValue, wantValue)
				}
			}
		})
	}
}

// mockSSEResponse builds an SSE stream body for a simple text response.
func mockSSEResponse(id, model, text string, inputTokens, outputTokens uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"%s\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"%s\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%d,\"output_tokens\":0}}}\n\n", id, model, inputTokens)
	fmt.Fprintf(&b, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	// Send text in one delta
	textJSON, _ := json.Marshal(text)
	fmt.Fprintf(&b, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", textJSON)
	fmt.Fprintf(&b, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	fmt.Fprintf(&b, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":%d}}\n\n", outputTokens)
	fmt.Fprintf(&b, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return b.String()
}

func TestDo(t *testing.T) {
	// Create a mock SSE streaming response
	mockBody := mockSSEResponse("msg_123", Claude46Sonnet, "Hello, world!", 100, 50)

	// Create a service with a mock HTTP client
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockBody, statusCode: 200},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do
	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	// Check the response
	if resp == nil {
		t.Fatalf("Do() response = nil, want not nil")
	}
	if resp.ID != "msg_123" {
		t.Errorf("Do() response ID = %v, want %v", resp.ID, "msg_123")
	}
	if resp.Role != llm.MessageRoleAssistant {
		t.Errorf("Do() response Role = %v, want %v", resp.Role, llm.MessageRoleAssistant)
	}
	if len(resp.Content) != 1 {
		t.Errorf("Do() response Content length = %v, want %v", len(resp.Content), 1)
	} else if resp.Content[0].Text != "Hello, world!" {
		t.Errorf("Do() response Content[0].Text = %v, want %v", resp.Content[0].Text, "Hello, world!")
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("Do() response Usage.InputTokens = %v, want %v", resp.Usage.InputTokens, 100)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("Do() response Usage.OutputTokens = %v, want %v", resp.Usage.OutputTokens, 50)
	}
}

// mockHTTPTransport is a mock HTTP transport for testing
type mockHTTPTransport struct {
	responseBody string
	statusCode   int
}

func (m *mockHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(m.responseBody)),
		Header:     make(http.Header),
	}
	if m.statusCode == 200 {
		resp.Header.Set("content-type", "text/event-stream")
	} else {
		resp.Header.Set("content-type", "application/json")
	}
	return resp, nil
}

func TestFromLLMContent(t *testing.T) {
	text := "hello world"
	toolInput := json.RawMessage(`{"command":"ls"}`)

	tests := []struct {
		name string
		c    llm.Content
		want content
	}{
		{
			name: "text content",
			c: llm.Content{
				Type: llm.ContentTypeText,
				Text: "hello world",
			},
			want: content{
				Type: "text",
				Text: &text,
			},
		},
		{
			name: "thinking content",
			c: llm.Content{
				Type:      llm.ContentTypeThinking,
				Thinking:  "thinking content",
				Signature: "signature",
			},
			want: content{
				Type:      "thinking",
				Thinking:  strp("thinking content"),
				Signature: "signature",
			},
		},
		{
			name: "redacted thinking content",
			c: llm.Content{
				Type:      llm.ContentTypeRedactedThinking,
				Data:      "redacted data",
				Signature: "signature",
			},
			want: content{
				Type:      "redacted_thinking",
				Data:      "redacted data",
				Signature: "signature",
			},
		},
		{
			name: "tool use content",
			c: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: toolInput,
			},
			want: content{
				Type:      "tool_use",
				ID:        "tool-id",
				ToolName:  "bash",
				ToolInput: toolInput,
			},
		},
		{
			name: "tool use with nil input gets empty object",
			c: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-id",
				ToolName:  "browser_take_screenshot",
				ToolInput: nil,
			},
			want: content{
				Type:      "tool_use",
				ID:        "tool-id",
				ToolName:  "browser_take_screenshot",
				ToolInput: json.RawMessage("{}"),
			},
		},
		{
			name: "tool use with JSON null input gets empty object",
			c: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "tool-id",
				ToolName:  "browser_take_screenshot",
				ToolInput: json.RawMessage("null"), // DB stores "null" which unmarshals as []byte("null")
			},
			want: content{
				Type:      "tool_use",
				ID:        "tool-id",
				ToolName:  "browser_take_screenshot",
				ToolInput: json.RawMessage("{}"),
			},
		},
		{
			name: "tool result content",
			c: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
			want: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolError: true,
			},
		},
		{
			name: "image content as text",
			c: llm.Content{
				Type:      llm.ContentTypeText,
				MediaType: "image/jpeg",
				Data:      "base64image",
			},
			want: content{
				Type:   "image",
				Source: json.RawMessage(`{"type":"base64","media_type":"image/jpeg","data":"base64image"}`),
			},
		},
		{
			name: "tool result with nested content",
			c: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolResult: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "nested text",
					},
				},
			},
			want: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolResult: []content{
					{
						Type: "text",
						Text: &[]string{"nested text"}[0],
					},
				},
			},
		},
		{
			name: "tool result with nested image content",
			c: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "tool-use-id",
				ToolResult: []llm.Content{
					{
						Type:      llm.ContentTypeText,
						MediaType: "image/png",
						Data:      "base64image",
					},
				},
			},
			want: content{
				Type:      "tool_result",
				ToolUseID: "tool-use-id",
				ToolResult: []content{
					{
						Type:   "image",
						Source: json.RawMessage(`{"type":"base64","media_type":"image/png","data":"base64image"}`),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromLLMContent(tt.c)

			// Compare basic fields
			if got.Type != tt.want.Type {
				t.Errorf("fromLLMContent().Type = %v, want %v", got.Type, tt.want.Type)
			}

			if got.ID != tt.want.ID {
				t.Errorf("fromLLMContent().ID = %v, want %v", got.ID, tt.want.ID)
			}

			gotThinking, wantThinking := "", ""
			if got.Thinking != nil {
				gotThinking = *got.Thinking
			}
			if tt.want.Thinking != nil {
				wantThinking = *tt.want.Thinking
			}
			if gotThinking != wantThinking {
				t.Errorf("fromLLMContent().Thinking = %q, want %q", gotThinking, wantThinking)
			}

			if got.Signature != tt.want.Signature {
				t.Errorf("fromLLMContent().Signature = %v, want %v", got.Signature, tt.want.Signature)
			}

			if got.Data != tt.want.Data {
				t.Errorf("fromLLMContent().Data = %v, want %v", got.Data, tt.want.Data)
			}

			if got.ToolName != tt.want.ToolName {
				t.Errorf("fromLLMContent().ToolName = %v, want %v", got.ToolName, tt.want.ToolName)
			}

			if string(got.ToolInput) != string(tt.want.ToolInput) {
				t.Errorf("fromLLMContent().ToolInput = %v, want %v", string(got.ToolInput), string(tt.want.ToolInput))
			}

			if got.ToolUseID != tt.want.ToolUseID {
				t.Errorf("fromLLMContent().ToolUseID = %v, want %v", got.ToolUseID, tt.want.ToolUseID)
			}

			if got.ToolError != tt.want.ToolError {
				t.Errorf("fromLLMContent().ToolError = %v, want %v", got.ToolError, tt.want.ToolError)
			}

			// Compare text field
			if tt.want.Text != nil {
				if got.Text == nil {
					t.Errorf("fromLLMContent().Text = nil, want %v", *tt.want.Text)
				} else if *got.Text != *tt.want.Text {
					t.Errorf("fromLLMContent().Text = %v, want %v", *got.Text, *tt.want.Text)
				}
			} else if got.Text != nil {
				t.Errorf("fromLLMContent().Text = %v, want nil", *got.Text)
			}

			// Compare source field (for image content)
			if len(tt.want.Source) > 0 {
				if string(got.Source) != string(tt.want.Source) {
					t.Errorf("fromLLMContent().Source = %v, want %v", string(got.Source), string(tt.want.Source))
				}
			}

			// Compare tool result length
			if len(got.ToolResult) != len(tt.want.ToolResult) {
				t.Errorf("fromLLMContent().ToolResult length = %v, want %v", len(got.ToolResult), len(tt.want.ToolResult))
			} else if len(tt.want.ToolResult) > 0 {
				// Compare each tool result item
				for i, tr := range tt.want.ToolResult {
					if got.ToolResult[i].Type != tr.Type {
						t.Errorf("fromLLMContent().ToolResult[%d].Type = %v, want %v", i, got.ToolResult[i].Type, tr.Type)
					}
					if tr.Text != nil {
						if got.ToolResult[i].Text == nil {
							t.Errorf("fromLLMContent().ToolResult[%d].Text = nil, want %v", i, *tr.Text)
						} else if *got.ToolResult[i].Text != *tr.Text {
							t.Errorf("fromLLMContent().ToolResult[%d].Text = %v, want %v", i, *got.ToolResult[i].Text, *tr.Text)
						}
					}
					if len(tr.Source) > 0 {
						if string(got.ToolResult[i].Source) != string(tr.Source) {
							t.Errorf("fromLLMContent().ToolResult[%d].Source = %v, want %v", i, string(got.ToolResult[i].Source), string(tr.Source))
						}
					}
				}
			}
		})
	}
}

func TestInverted(t *testing.T) {
	// Test normal case
	m := map[string]int{
		"a": 1,
		"b": 2,
		"c": 3,
	}

	want := map[int]string{
		1: "a",
		2: "b",
		3: "c",
	}

	got := inverted(m)

	if len(got) != len(want) {
		t.Errorf("inverted() length = %v, want %v", len(got), len(want))
	}

	for k, v := range want {
		if gotV, ok := got[k]; !ok {
			t.Errorf("inverted() missing key %v", k)
		} else if gotV != v {
			t.Errorf("inverted()[%v] = %v, want %v", k, gotV, v)
		}
	}

	// Test panic case with duplicate values
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("inverted() should panic with duplicate values")
		}
	}()

	m2 := map[string]int{
		"a": 1,
		"b": 1, // duplicate value
	}

	inverted(m2)
}

func TestToLLMContentWithNestedToolResults(t *testing.T) {
	text := "nested text"
	nestedContent := content{
		Type: "text",
		Text: &text,
	}

	c := content{
		Type:      "tool_result",
		ToolUseID: "tool-use-id",
		ToolResult: []content{
			nestedContent,
		},
	}

	got := toLLMContent(c)

	if got.Type != llm.ContentTypeToolResult {
		t.Errorf("toLLMContent().Type = %v, want %v", got.Type, llm.ContentTypeToolResult)
	}

	if got.ToolUseID != "tool-use-id" {
		t.Errorf("toLLMContent().ToolUseID = %v, want %v", got.ToolUseID, "tool-use-id")
	}

	if len(got.ToolResult) != 1 {
		t.Errorf("toLLMContent().ToolResult length = %v, want %v", len(got.ToolResult), 1)
	} else {
		if got.ToolResult[0].Type != llm.ContentTypeText {
			t.Errorf("toLLMContent().ToolResult[0].Type = %v, want %v", got.ToolResult[0].Type, llm.ContentTypeText)
		}
		if got.ToolResult[0].Text != "nested text" {
			t.Errorf("toLLMContent().ToolResult[0].Text = %v, want %v", got.ToolResult[0].Text, "nested text")
		}
	}
}

func TestParseSSEStreamText(t *testing.T) {
	stream := mockSSEResponse("msg_abc", Claude46Sonnet, "Hello!", 10, 5)
	resp, err := parseSSEStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if resp.ID != "msg_abc" {
		t.Errorf("ID = %q, want %q", resp.ID, "msg_abc")
	}
	if resp.Role != "assistant" {
		t.Errorf("Role = %q, want %q", resp.Role, "assistant")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end_turn")
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", resp.Usage.OutputTokens)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "text")
	}
	if resp.Content[0].Text == nil || *resp.Content[0].Text != "Hello!" {
		t.Errorf("Content[0].Text = %v, want %q", resp.Content[0].Text, "Hello!")
	}
}

func TestParseSSEStreamMultipleDeltas(t *testing.T) {
	// Build a stream with text split across multiple deltas
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_multi\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello, \"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"world!\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if *resp.Content[0].Text != "Hello, world!" {
		t.Errorf("Content[0].Text = %q, want %q", *resp.Content[0].Text, "Hello, world!")
	}
}

func TestParseSSEStreamToolUse(t *testing.T) {
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tool\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":50,\"output_tokens\":0}}}\n\n")
	// Text block first
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Let me run that.\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	// Tool use block
	b.WriteString(`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"bash","input":{}}}` + "\n\n")
	b.WriteString(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"comma"}}` + "\n\n")
	b.WriteString(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"nd\":\"ls\"}"}}` + "\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":25}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "tool_use")
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content length = %d, want 2", len(resp.Content))
	}
	// Text block
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "text")
	}
	if *resp.Content[0].Text != "Let me run that." {
		t.Errorf("Content[0].Text = %q, want %q", *resp.Content[0].Text, "Let me run that.")
	}
	// Tool use block
	if resp.Content[1].Type != "tool_use" {
		t.Errorf("Content[1].Type = %q, want %q", resp.Content[1].Type, "tool_use")
	}
	if resp.Content[1].ID != "toolu_123" {
		t.Errorf("Content[1].ID = %q, want %q", resp.Content[1].ID, "toolu_123")
	}
	if resp.Content[1].ToolName != "bash" {
		t.Errorf("Content[1].ToolName = %q, want %q", resp.Content[1].ToolName, "bash")
	}
	// The accumulated JSON should be the concatenation of partials
	var input map[string]string
	if err := json.Unmarshal(resp.Content[1].ToolInput, &input); err != nil {
		t.Fatalf("failed to parse tool input JSON: %v (raw: %q)", err, string(resp.Content[1].ToolInput))
	}
	if input["command"] != "ls" {
		t.Errorf("tool input command = %q, want %q", input["command"], "ls")
	}
}

func TestParseSSEStreamToolUseEmptyInput(t *testing.T) {
	// Reproduces a bug where tool_use with empty input {} gets ToolInput=nil
	// after SSE parsing. Anthropic sends input_json_delta with partial_json:""
	// for tools with no parameters, and append(nil, []byte("")...) stays nil.
	// This causes the "input" field to be omitted via omitempty, leading to
	// a 400 "tool_use.input: Field required" error on the next API call.
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_empty\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":50,\"output_tokens\":0}}}\n\n")
	b.WriteString(`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_empty","name":"browser_take_screenshot","input":{}}}` + "\n\n")
	b.WriteString(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}` + "\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":10}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != "tool_use" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "tool_use")
	}
	if resp.Content[0].ToolInput == nil {
		t.Fatal("Content[0].ToolInput is nil, want non-nil (at least {})")
	}
	// Verify it serializes correctly with the "input" field present
	out, err := json.Marshal(resp.Content[0])
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if !strings.Contains(string(out), `"input"`) {
		t.Errorf("serialized tool_use missing 'input' field: %s", out)
	}
}

func TestParseSSEStreamThinking(t *testing.T) {
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_think\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":20,\"output_tokens\":0}}}\n\n")
	// Thinking block
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Let me think...\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig123\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	// Text block
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"The answer is 42.\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content length = %d, want 2", len(resp.Content))
	}
	// Thinking block
	if resp.Content[0].Type != "thinking" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "thinking")
	}
	if resp.Content[0].Thinking == nil || *resp.Content[0].Thinking != "Let me think..." {
		t.Errorf("Content[0].Thinking = %v, want %q", resp.Content[0].Thinking, "Let me think...")
	}
	if resp.Content[0].Signature != "sig123" {
		t.Errorf("Content[0].Signature = %q, want %q", resp.Content[0].Signature, "sig123")
	}
	// Text block
	if *resp.Content[1].Text != "The answer is 42." {
		t.Errorf("Content[1].Text = %q, want %q", *resp.Content[1].Text, "The answer is 42.")
	}
}

func TestParseSSEStreamPing(t *testing.T) {
	// Stream with ping events interspersed
	var b strings.Builder
	b.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n")
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_p\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
	b.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n")
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if *resp.Content[0].Text != "ok" {
		t.Errorf("Text = %q, want %q", *resp.Content[0].Text, "ok")
	}
}

func TestParseSSEStreamNoMessageStart(t *testing.T) {
	stream := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	_, err := parseSSEStream(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatal("expected error for missing message_start")
	}
	if !strings.Contains(err.Error(), "no message_start") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "no message_start")
	}
}

func TestParseSSEStreamIncomplete(t *testing.T) {
	// Stream has message_start and content but no message_stop
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_inc\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
	b.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n")

	_, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err == nil {
		t.Fatal("expected error for incomplete stream (no message_stop)")
	}
	if !strings.Contains(err.Error(), "incomplete stream") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "incomplete stream")
	}
}

func TestParseSSEStreamError(t *testing.T) {
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_err\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
	b.WriteString(`event: error` + "\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n")

	_, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err == nil {
		t.Fatal("expected error for stream error event")
	}
	if !strings.Contains(err.Error(), "stream error event") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "stream error event")
	}
}

func TestDoRetriesOnInvalidThinkingSignature(t *testing.T) {
	// When the API returns "Invalid `signature` in `thinking` block",
	// Do should retry with all thinking blocks stripped.
	invalidSigResponse := `{"type":"error","error":{"type":"invalid_request_error","message":"messages.11.content.0: Invalid ` + "`" + `signature` + "`" + ` in ` + "`" + `thinking` + "`" + ` block"}}`
	successResponse := mockSSEResponse("msg_retry", Claude46Opus, "It works!", 100, 50)

	callCount := 0
	transport := &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			// First call: return invalid signature error
			return &http.Response{
				StatusCode: 400,
				Body:       io.NopCloser(strings.NewReader(invalidSigResponse)),
				Header:     http.Header{"Content-Type": {"application/json"}},
			}, nil
		}
		// Second call: check that thinking content blocks were stripped, return success.
		// The request-level "thinking" config (budget_tokens) is fine; we check for
		// signature which only appears in thinking content blocks.
		body, _ := io.ReadAll(req.Body)
		if strings.Contains(string(body), `"signature"`) {
			t.Errorf("retry request still contains thinking signature")
		}
		if strings.Contains(string(body), `"old thinking"`) {
			t.Errorf("retry request still contains thinking text")
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(successResponse)),
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
		}, nil
	}}

	s := &Service{
		APIKey:        "test-key",
		Model:         Claude46Opus,
		ThinkingLevel: llm.ThinkingLevelMedium,
		HTTPC:         &http.Client{Transport: transport},
		Backoff:       []time.Duration{time.Millisecond}, // fast backoff for tests
	}

	req := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "What is 2+2?"},
			}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "old thinking", Signature: "bad-sig"},
				{Type: llm.ContentTypeText, Text: "4"},
			}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "And 3+3?"},
			}},
		},
	}

	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("Do() response = nil")
	}
	if callCount != 2 {
		t.Errorf("expected 2 HTTP calls, got %d", callCount)
	}
	if resp.Content[0].Text != "It works!" {
		t.Errorf("unexpected response text: %q", resp.Content[0].Text)
	}
}

// roundTripFunc implements http.RoundTripper using a function.
type roundTripFunc struct {
	fn func(*http.Request) (*http.Response, error)
}

func (f *roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.fn(req)
}

func TestDoClientError(t *testing.T) {
	// Create a mock HTTP client that returns a client error
	mockResponse := `{"error": "bad request"}`

	// Create a service with a mock HTTP client
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockResponse, statusCode: 400},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do - should fail immediately
	resp, err := s.Do(context.Background(), req)
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}

	if resp != nil {
		t.Errorf("Do() response = %v, want nil", resp)
	}
}

func TestServiceConfigDetails(t *testing.T) {
	tests := []struct {
		name    string
		service *Service
		want    map[string]string
	}{
		{
			name: "default values",
			service: &Service{
				APIKey: "test-key",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "true",
			},
		},
		{
			name: "custom values",
			service: &Service{
				APIKey: "test-key",
				URL:    "https://custom-url.com",
				Model:  "custom-model",
			},
			want: map[string]string{
				"url":             "https://custom-url.com",
				"model":           "custom-model",
				"has_api_key_set": "true",
			},
		},
		{
			name: "empty api key",
			service: &Service{
				APIKey: "",
			},
			want: map[string]string{
				"url":             DefaultURL,
				"model":           DefaultModel,
				"has_api_key_set": "false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.service.ConfigDetails()

			for key, wantValue := range tt.want {
				if gotValue, ok := got[key]; !ok {
					t.Errorf("ConfigDetails() missing key %v", key)
				} else if gotValue != wantValue {
					t.Errorf("ConfigDetails()[%v] = %v, want %v", key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestDoStartTimeEndTime(t *testing.T) {
	// Create a mock SSE streaming response
	mockBody := mockSSEResponse("msg_123", Claude46Sonnet, "Hello, world!", 100, 50)

	// Create a service with a mock HTTP client
	client := &http.Client{
		Transport: &mockHTTPTransport{responseBody: mockBody, statusCode: 200},
	}

	s := &Service{
		APIKey: "test-key",
		HTTPC:  client,
	}

	// Create a request
	req := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type: llm.ContentTypeText,
						Text: "Hello, Claude!",
					},
				},
			},
		},
	}

	// Call Do
	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	// Check the response
	if resp == nil {
		t.Fatalf("Do() response = nil, want not nil")
	}

	// Check that StartTime and EndTime are set
	if resp.StartTime == nil {
		t.Error("Do() response StartTime = nil, want not nil")
	}

	if resp.EndTime == nil {
		t.Error("Do() response EndTime = nil, want not nil")
	}

	// Check that EndTime is after StartTime
	if resp.StartTime != nil && resp.EndTime != nil {
		if resp.EndTime.Before(*resp.StartTime) {
			t.Error("Do() response EndTime should be after StartTime")
		}
	}
}

// TestLiveAnthropicModels sends a real request to every Anthropic model we support
// and verifies we get a valid response. Skipped if ANTHROPIC_API_KEY is not set.
func TestLiveAnthropicModels(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	models := []struct {
		name  string
		model string
	}{
		{"Haiku 4.5", Claude45Haiku},
		{"Sonnet 4.6", Claude46Sonnet},
		{"Opus 4.5", Claude45Opus},
		{"Opus 4.6", Claude46Opus},
	}

	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Say hello in exactly 3 words."}},
		}},
		System: []llm.SystemContent{{Text: "Be brief.", Type: "text"}},
	}

	for _, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			svc := &Service{
				APIKey:        apiKey,
				Model:         m.model,
				ThinkingLevel: llm.ThinkingLevelMedium,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			resp, err := svc.Do(ctx, req)
			if err != nil {
				t.Fatalf("%s: %v", m.model, err)
			}

			var text string
			for _, c := range resp.Content {
				if c.Type == llm.ContentTypeText {
					text = c.Text
					break
				}
			}
			if text == "" {
				t.Fatalf("%s: got empty text response", m.model)
			}
			t.Logf("%s: %q", m.model, text)
		})
	}
}

func strp(s string) *string { return &s }

func TestParseSSEStreamRecordedResponse(t *testing.T) {
	// Test with a real recorded SSE response from Anthropic's API.
	// This ensures our parser handles the exact format the API sends,
	// including extra whitespace in data fields.
	recorded := `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-6","id":"msg_01PU4SWJR43AoiPx2RA7hfcf","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":16,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":1,"service_tier":"standard","inference_geo":"not_available"}} }

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}       }

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}      }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" to"}       }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" you."}      }

event: content_block_stop
data: {"type":"content_block_stop","index":0    }

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":16,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":7}  }

event: message_stop
data: {"type":"message_stop"}
`
	resp, err := parseSSEStream(strings.NewReader(recorded), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if resp.ID != "msg_01PU4SWJR43AoiPx2RA7hfcf" {
		t.Errorf("ID = %q, want %q", resp.ID, "msg_01PU4SWJR43AoiPx2RA7hfcf")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end_turn")
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Text == nil || *resp.Content[0].Text != "Hello to you." {
		t.Errorf("Text = %v, want %q", resp.Content[0].Text, "Hello to you.")
	}
	if resp.Usage.OutputTokens != 7 {
		t.Errorf("OutputTokens = %d, want 7", resp.Usage.OutputTokens)
	}
}

func TestParseSSEStreamConnectionReset(t *testing.T) {
	// Simulate a connection reset mid-read by using an io.Reader that returns
	// some valid data followed by an error.
	partial := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_reset\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n"

	r := &errorAfterReader{data: []byte(partial), err: fmt.Errorf("connection reset by peer")}
	_, err := parseSSEStream(r, nil)
	if err == nil {
		t.Fatal("expected error for connection reset")
	}
	// Should get either the read error or the incomplete stream error
	if !strings.Contains(err.Error(), "connection reset") && !strings.Contains(err.Error(), "incomplete stream") {
		t.Errorf("error = %q, want to contain 'connection reset' or 'incomplete stream'", err.Error())
	}
}

// errorAfterReader returns data, then an error.
type errorAfterReader struct {
	data []byte
	err  error
	pos  int
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// mockTruncatedSSEResponse builds an SSE stream that cuts off before message_delta/message_stop.
// This simulates a connection drop mid-stream.
func mockTruncatedSSEResponse(id, model, text string, inputTokens uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"%s\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"%s\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%d,\"output_tokens\":0}}}\n\n", id, model, inputTokens)
	fmt.Fprintf(&b, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	textJSON, _ := json.Marshal(text)
	fmt.Fprintf(&b, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", textJSON)
	fmt.Fprintf(&b, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	// No message_delta or message_stop — stream was truncated!
	return b.String()
}

func TestParseSSEStreamTruncated(t *testing.T) {
	// A stream that cuts off before message_delta (no stop_reason) should be an error.
	stream := mockTruncatedSSEResponse("msg_trunc", Claude46Sonnet, "partial response", 100)
	_, err := parseSSEStream(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatal("expected error for truncated stream, got nil")
	}
	if !strings.Contains(err.Error(), "incomplete stream") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "incomplete stream")
	}
}

func TestParseSSEStreamTruncatedMidContentBlock(t *testing.T) {
	// Stream that cuts off in the middle of a content block (no content_block_stop, no message_delta).
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_mid\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n")
	// Cut off here — no content_block_stop, no message_delta, no message_stop

	_, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err == nil {
		t.Fatal("expected error for truncated stream, got nil")
	}
	if !strings.Contains(err.Error(), "incomplete stream") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "incomplete stream")
	}
}

// retryCountTransport tracks how many requests are made and can return truncated
// responses for the first N attempts, then a complete response.
type retryCountTransport struct {
	truncatedCount int // how many truncated responses to return first
	completeBody   string
	truncatedBody  string
	calls          int
}

func (m *retryCountTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls++
	body := m.truncatedBody
	if m.calls > m.truncatedCount {
		body = m.completeBody
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}, nil
}

func TestDoRetriesOnTruncatedStream(t *testing.T) {
	truncated := mockTruncatedSSEResponse("msg_trunc", Claude46Sonnet, "partial", 100)
	complete := mockSSEResponse("msg_ok", Claude46Sonnet, "Hello, world!", 100, 50)

	transport := &retryCountTransport{
		truncatedCount: 2, // first 2 attempts return truncated stream
		completeBody:   complete,
		truncatedBody:  truncated,
	}

	s := &Service{
		APIKey:  "test-key",
		HTTPC:   &http.Client{Transport: transport},
		Backoff: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}

	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
		}},
	}

	resp, err := s.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil (expected retry to succeed)", err)
	}
	if resp.Content[0].Text != "Hello, world!" {
		t.Errorf("resp text = %q, want %q", resp.Content[0].Text, "Hello, world!")
	}
	if transport.calls != 3 {
		t.Errorf("expected 3 attempts (2 truncated + 1 success), got %d", transport.calls)
	}
}

func TestDoStopsRetryingOnContextCancel(t *testing.T) {
	// If the context is cancelled during retries, Do should stop immediately
	// instead of sleeping through all 11 attempts.
	truncated := mockTruncatedSSEResponse("msg_trunc", Claude46Sonnet, "partial", 100)

	transport := &retryCountTransport{
		truncatedCount: 999, // always truncated
		truncatedBody:  truncated,
		completeBody:   truncated,
	}

	s := &Service{
		APIKey:  "test-key",
		HTTPC:   &http.Client{Transport: transport},
		Backoff: []time.Duration{10 * time.Second}, // long backoff to prove we don't sleep through it
	}

	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
		}},
	}

	// Cancel context after first attempt completes
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := s.Do(ctx, req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Do() expected error")
	}
	if !strings.Contains(err.Error(), "context cancelled") && !strings.Contains(err.Error(), "context deadline") {
		// Accept either "context cancelled" from our check or "context deadline exceeded" from http
		if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "deadline") {
			t.Errorf("error = %q, want context-related error", err.Error())
		}
	}
	// Should have stopped well before 11 * 10s = 110s
	if elapsed > 5*time.Second {
		t.Errorf("Do() took %v, expected it to bail out quickly on context cancellation", elapsed)
	}
	// Should have made at most a few attempts, not all 11
	if transport.calls > 3 {
		t.Errorf("expected at most 3 attempts, got %d (should stop retrying on cancelled context)", transport.calls)
	}
}

func TestDoNoRetryOnContextDeadlineDuringStream(t *testing.T) {
	// Simulate the exact scenario from the bug report: the context deadline fires
	// during SSE stream reading. The provider should NOT retry — it should return
	// immediately with a clear error.

	// Use a transport that blocks during stream reading until the context is cancelled
	transport := &contextDeadlineTransport{}

	s := &Service{
		APIKey:  "test-key",
		HTTPC:   &http.Client{Transport: transport},
		Backoff: []time.Duration{10 * time.Second}, // long backoff to prove we don't sleep
	}

	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := s.Do(ctx, req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Do() expected error")
	}
	// Should fail immediately (within 1s), not wait for backoff
	if elapsed > 2*time.Second {
		t.Errorf("Do() took %v, should have returned immediately on context deadline", elapsed)
	}
	// Should have made exactly 1 attempt
	if transport.calls != 1 {
		t.Errorf("expected 1 attempt, got %d (should not retry on context deadline)", transport.calls)
	}
	// Error should mention the failure clearly
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error = %q, want to contain 'context deadline exceeded'", err.Error())
	}
	if !strings.Contains(err.Error(), "1 attempts") {
		t.Errorf("error = %q, want to contain '1 attempts'", err.Error())
	}
}

// contextDeadlineTransport returns a 200 response with an SSE stream that
// blocks until the request context is cancelled, simulating a stream that
// gets cut off by a context deadline.
type contextDeadlineTransport struct {
	calls int
}

func (t *contextDeadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	// Return a 200 with a body that sends the start of an SSE stream,
	// then blocks until the context is cancelled.
	pr, pw := io.Pipe()
	go func() {
		// Write the beginning of a valid SSE stream
		pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5-20250929\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":100,\"output_tokens\":0}}}\n\n"))
		// Block until context is cancelled
		<-req.Context().Done()
		pw.CloseWithError(req.Context().Err())
	}()
	return &http.Response{
		StatusCode: 200,
		Body:       pr,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}, nil
}

func TestDoFailsAfterMaxRetriesOnTruncatedStream(t *testing.T) {
	// All attempts return truncated stream — should fail after max retries
	truncated := mockTruncatedSSEResponse("msg_trunc", Claude46Sonnet, "partial", 100)

	transport := &retryCountTransport{
		truncatedCount: 999, // always truncated
		truncatedBody:  truncated,
		completeBody:   truncated, // doesn't matter
	}

	s := &Service{
		APIKey:  "test-key",
		HTTPC:   &http.Client{Transport: transport},
		Backoff: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}

	req := &llm.Request{
		Messages: []llm.Message{{
			Role:    llm.MessageRoleUser,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hello"}},
		}},
	}

	_, err := s.Do(context.Background(), req)
	if err == nil {
		t.Fatal("Do() expected error after max retries on truncated stream")
	}
	if !strings.Contains(err.Error(), "incomplete stream") {
		t.Errorf("error = %q, want to contain 'incomplete stream'", err.Error())
	}
	if !strings.Contains(err.Error(), "failed after") {
		t.Errorf("error = %q, want to contain 'failed after'", err.Error())
	}
	// Should have made 11 attempts (0-10, then fails at attempts > 10)
	if transport.calls != 11 {
		t.Errorf("expected 11 attempts, got %d", transport.calls)
	}
}

func TestParseSSEStreamMultiLineData(t *testing.T) {
	// SSE spec allows multiple "data:" lines which should be joined with "\n".
	// This tests that our parser correctly handles this case.
	var b strings.Builder
	// Use multi-line data format for message_start
	b.WriteString("event: message_start\n")
	b.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_multi\",\n")
	b.WriteString("data: \"type\":\"message\",\"role\":\"assistant\",\"model\":\"test\",\n")
	b.WriteString("data: \"content\":[],\"stop_reason\":null,\n")
	b.WriteString("data: \"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n")
	b.WriteString("\n") // blank line dispatches event

	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if resp.ID != "msg_multi" {
		t.Errorf("resp.ID = %q, want %q", resp.ID, "msg_multi")
	}
	if len(resp.Content) != 1 || *resp.Content[0].Text != "hello" {
		t.Errorf("unexpected content: %+v", resp.Content)
	}
}

func TestParseSSEStreamErrorIncludesData(t *testing.T) {
	// When JSON parsing fails, the error should include the raw data for debugging.
	var b strings.Builder
	b.WriteString("event: message_start\n")
	b.WriteString("data: {\"type\": \"message_start\" \"broken json}\n")
	b.WriteString("\n")

	_, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	// Error should contain the event type and the raw data
	if !strings.Contains(err.Error(), "message_start") {
		t.Errorf("error should contain event type, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "broken json") {
		t.Errorf("error should contain raw data, got: %q", err.Error())
	}
}

func TestParseSSEStreamTruncatedJSON(t *testing.T) {
	// Simulate what happens when the connection drops mid-JSON (the actual bug we're debugging).
	// The data line contains incomplete JSON that would cause "unexpected end of JSON input".
	var b strings.Builder
	b.WriteString("event: content_block_delta\n")
	b.WriteString("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_del\n")
	b.WriteString("\n")

	_, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err == nil {
		t.Fatal("expected error for truncated JSON")
	}
	// Should include the event type for context
	if !strings.Contains(err.Error(), "content_block_delta") {
		t.Errorf("error should contain event type, got: %q", err.Error())
	}
	// Should include the truncated data
	if !strings.Contains(err.Error(), "text_del") {
		t.Errorf("error should contain the truncated data, got: %q", err.Error())
	}
}

func TestIterSSEEventsComments(t *testing.T) {
	// Comments (lines starting with ':') should be ignored.
	stream := ": this is a comment\nevent: ping\ndata: {}\n\n"
	var events []sseEvent
	err := iterSSEEvents(strings.NewReader(stream), func(ev sseEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("iterSSEEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].EventType != "ping" {
		t.Errorf("event type = %q, want %q", events[0].EventType, "ping")
	}
}

func TestIterSSEEventsNoTrailingNewline(t *testing.T) {
	// Stream that ends without a trailing blank line should still dispatch.
	stream := "event: ping\ndata: {}"
	var events []sseEvent
	err := iterSSEEvents(strings.NewReader(stream), func(ev sseEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("iterSSEEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
}

func TestParseSSEStreamInvalidCharInJSON(t *testing.T) {
	// Simulate the "invalid character '\"' after object key:value pair" error
	// mentioned in the bug report. This can happen when JSON is split across
	// what the old parser thought were separate lines.
	var b strings.Builder
	b.WriteString("event: message_start\n")
	b.WriteString(`data: {"type":"message_start","message":{"id":"msg_ok","type":"message","role":"assistant","model":"test","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n")
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp, err := parseSSEStream(strings.NewReader(b.String()), nil)
	if err != nil {
		t.Fatalf("parseSSEStream() error = %v", err)
	}
	if resp.ID != "msg_ok" {
		t.Errorf("resp.ID = %q, want %q", resp.ID, "msg_ok")
	}
}
