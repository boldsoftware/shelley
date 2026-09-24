package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// mockService implements Service interface for testing
type mockService struct {
	maxImageDimension int
}

func (m *mockService) Do(ctx context.Context, req *Request) (*Response, error) {
	return &Response{}, nil
}

func (m *mockService) Provider() string { return "" }

func (m *mockService) MaxImageDimension() int {
	return m.maxImageDimension
}

func (m *mockService) MaxImageBytes() int {
	return 0
}

func TestMustSchema(t *testing.T) {
	tests := []struct {
		name        string
		schema      string
		expectPanic bool
	}{
		{
			name:        "valid schema",
			schema:      `{"type": "object", "properties": {}}`,
			expectPanic: false,
		},
		{
			name:        "valid schema with properties",
			schema:      `{"type": "object", "properties": {"name": {"type": "string"}}}`,
			expectPanic: false,
		},
		{
			name:        "invalid json",
			schema:      `{"type": "object", "properties": }`,
			expectPanic: true,
		},
		{
			name:        "missing type",
			schema:      `{"properties": {}}`,
			expectPanic: true,
		},
		{
			name:        "wrong type",
			schema:      `{"type": "string", "properties": {}}`,
			expectPanic: true,
		},
		{
			name:        "missing properties",
			schema:      `{"type": "object"}`,
			expectPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("Expected panic for schema: %s", tt.schema)
					}
				}()
			}
			result := MustSchema(tt.schema)
			if !tt.expectPanic {
				if string(result) != tt.schema {
					t.Errorf("MustSchema() = %s, want %s", string(result), tt.schema)
				}
			}
		})
	}
}

func TestRunJSON(t *testing.T) {
	type request struct {
		Name string `json:"name"`
	}

	var gotCtx context.Context
	var gotReq request
	run := RunJSON(func(ctx context.Context, req request) ToolOut {
		gotCtx = ctx
		gotReq = req
		return ToolOut{LLMContent: TextContent("hello " + req.Name)}
	})

	ctx := context.WithValue(t.Context(), struct{}{}, "ctx-value")
	out := run(ctx, json.RawMessage(`{"name":"Ada"}`))
	if out.Error != nil {
		t.Fatalf("RunJSON returned error: %v", out.Error)
	}
	if gotCtx != ctx {
		t.Fatal("RunJSON did not pass through context")
	}
	if gotReq.Name != "Ada" {
		t.Fatalf("RunJSON decoded request %+v, want name Ada", gotReq)
	}
	if len(out.LLMContent) != 1 || out.LLMContent[0].Text != "hello Ada" {
		t.Fatalf("RunJSON output = %+v", out.LLMContent)
	}
}

func TestRunJSONInvalidJSON(t *testing.T) {
	type request struct {
		Name string `json:"name"`
	}

	called := false
	run := RunJSON(func(ctx context.Context, req request) ToolOut {
		called = true
		return ToolOut{}
	})

	out := run(t.Context(), json.RawMessage(`{"name":123}`))
	if out.Error == nil {
		t.Fatal("RunJSON returned nil error for invalid input")
	}
	if called {
		t.Fatal("RunJSON called handler after invalid input")
	}
}

func TestUsageAdd(t *testing.T) {
	u1 := Usage{
		InputTokens:              100,
		CacheCreationInputTokens: 50,
		CacheReadInputTokens:     25,
		OutputTokens:             200,
		CostUSD:                  0.01,
	}

	u2 := Usage{
		InputTokens:              150,
		CacheCreationInputTokens: 75,
		CacheReadInputTokens:     30,
		OutputTokens:             100,
		CostUSD:                  0.02,
	}

	u1.Add(u2)

	expected := Usage{
		InputTokens:              250,  // 100 + 150
		CacheCreationInputTokens: 125,  // 50 + 75
		CacheReadInputTokens:     55,   // 25 + 30
		OutputTokens:             300,  // 200 + 100
		CostUSD:                  0.03, // 0.01 + 0.02
	}

	if !reflect.DeepEqual(u1, expected) {
		t.Errorf("Usage.Add() resulted in %v, want %v", u1, expected)
	}
}

func TestUsageRequestBreakdown(t *testing.T) {
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Second)
	single := Usage{
		InputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 30,
		OutputTokens: 40, CostUSD: 0.25, StartTime: &start, EndTime: &end,
	}
	want := []RequestUsage{{
		InputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 30,
		OutputTokens: 40, CostUSD: 0.25, StartTime: &start, EndTime: &end,
	}}
	for _, requests := range [][]RequestUsage{nil, {}, want} {
		u := single
		u.Requests = requests
		if got := u.RequestBreakdown(); !reflect.DeepEqual(got, want) {
			t.Errorf("RequestBreakdown(%+v) = %+v, want %+v", u, got, want)
		}
	}
	// The request list is authoritative, not an addition to the totals.
	single.Requests = append(want, RequestUsage{InputTokens: 50, CostUSD: 0.5})
	if got := single.RequestBreakdown(); !reflect.DeepEqual(got, single.Requests) {
		t.Errorf("RequestBreakdown() = %+v, want %+v", got, single.Requests)
	}
}

func TestUsageIsZero(t *testing.T) {
	empty := Usage{Requests: []RequestUsage{}}
	if !empty.IsZero() {
		t.Fatal("empty usage is not zero")
	}
	for _, u := range []Usage{
		{InputTokens: 1}, {CacheCreationInputTokens: 1}, {CacheReadInputTokens: 1},
		{OutputTokens: 1}, {CostUSD: 1}, {Model: "m"}, {URL: "u"},
		{StartTime: new(time.Time)}, {EndTime: new(time.Time)},
		{Requests: []RequestUsage{{}}},
	} {
		if u.IsZero() {
			t.Errorf("IsZero(%+v) = true", u)
		}
	}
}

func TestResponseToMessage(t *testing.T) {
	tests := []struct {
		name          string
		response      Response
		wantRole      MessageRole
		wantEndOfTurn bool
		wantOrigin    *MessageOrigin
	}{
		{
			name: "tool use stop reason",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonToolUse,
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: false,
		},
		{
			name: "end turn stop reason",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonEndTurn,
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: true,
		},
		{
			name: "max tokens stop reason",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonMaxTokens,
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: true,
		},
		{
			name: "origin",
			response: Response{
				Role:       MessageRoleAssistant,
				StopReason: StopReasonEndTurn,
				Origin: &MessageOrigin{
					Provider:  "anthropic",
					Transport: "anthropic-messages:https://api.anthropic.com/v1/messages",
					Model:     "claude-opus-5",
				},
			},
			wantRole:      MessageRoleAssistant,
			wantEndOfTurn: true,
			wantOrigin: &MessageOrigin{
				Provider:  "anthropic",
				Transport: "anthropic-messages:https://api.anthropic.com/v1/messages",
				Model:     "claude-opus-5",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.response.ToMessage()

			if message.Role != tt.wantRole {
				t.Errorf("ToMessage().Role = %v, want %v", message.Role, tt.wantRole)
			}

			if message.EndOfTurn != tt.wantEndOfTurn {
				t.Errorf("ToMessage().EndOfTurn = %v, want %v", message.EndOfTurn, tt.wantEndOfTurn)
			}

			if !reflect.DeepEqual(message.Origin, tt.wantOrigin) {
				t.Errorf("ToMessage().Origin = %+v, want %+v", message.Origin, tt.wantOrigin)
			}
		})
	}
}

func TestCostUSDFromResponse(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		wantCost float64
	}{
		{
			name: "valid cost header",
			headers: map[string]string{
				"Exedev-Gateway-Cost": "0.050000",
			},
			wantCost: 0.05,
		},
		{
			name: "invalid cost header",
			headers: map[string]string{
				"Exedev-Gateway-Cost": "invalid",
			},
			wantCost: 0,
		},
		{
			name:     "missing cost header",
			headers:  map[string]string{},
			wantCost: 0,
		},
		{
			name: "empty cost header",
			headers: map[string]string{
				"Exedev-Gateway-Cost": "",
			},
			wantCost: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			for k, v := range tt.headers {
				headers.Set(k, v)
			}

			cost := CostUSDFromResponse(headers)
			if cost != tt.wantCost {
				t.Errorf("CostUSDFromResponse() = %f, want %f", cost, tt.wantCost)
			}
		})
	}
}

func TestIsServerSideContentType(t *testing.T) {
	serverSide := []ContentType{
		ContentTypeServerToolUse,
		ContentTypeWebSearchToolResult,
		ContentTypeWebSearchResult,
	}
	for _, ct := range serverSide {
		if !IsServerSideContentType(ct) {
			t.Errorf("IsServerSideContentType(%v) = false, want true", ct)
		}
	}

	notServerSide := []ContentType{
		ContentTypeText,
		ContentTypeToolUse,
		ContentTypeToolResult,
		ContentTypeThinking,
		ContentTypeRedactedThinking,
	}
	for _, ct := range notServerSide {
		if IsServerSideContentType(ct) {
			t.Errorf("IsServerSideContentType(%v) = true, want false", ct)
		}
	}
}

// TestContentCallerCitationsOmitEmpty pins the wire-format-safety contract of
// llm.Content.Caller and llm.Content.Citations.
//
// These fields are persisted to the messages table as part of llm_data JSON
// and later reloaded to be sent back to the LLM. Without `omitempty`, a nil
// json.RawMessage marshals to the JSON token `null`, which on reload
// unmarshals back to []byte("null") (not nil). We would then forward
// `"caller": null` to Anthropic, which the API rejects with
//
//	server_tool_use.caller: Input should be an object
//
// and the bad block sits in conversation history forever, wedging every
// retry. With omitempty, nil values are dropped on marshal and never come
// back to bite us.
func TestContentCallerCitationsOmitEmpty(t *testing.T) {
	original := Content{
		Type:     ContentTypeServerToolUse,
		ID:       "srvtoolu_x",
		ToolName: "web_search",
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, `"Caller"`) || strings.Contains(s, `"Citations"`) {
		t.Fatalf("nil Caller/Citations must not appear in marshaled JSON; got: %s", s)
	}
	var reloaded Content
	if err := json.Unmarshal(b, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.Caller != nil {
		t.Errorf("reloaded Caller = %q, want nil", reloaded.Caller)
	}
	if reloaded.Citations != nil {
		t.Errorf("reloaded Citations = %q, want nil", reloaded.Citations)
	}
}

func (m *mockService) SupportsImages() bool { return true }

func TestClampThinkingLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     ThinkingLevel
		supported []ThinkingLevel
		want      ThinkingLevel
	}{
		{name: "supported unchanged", level: ThinkingLevelHigh, supported: []ThinkingLevel{ThinkingLevelLow, ThinkingLevelHigh}, want: ThinkingLevelHigh},
		{name: "unknown levels unchanged", level: ThinkingLevelMax, want: ThinkingLevelMax},
		{name: "max rounds down", level: ThinkingLevelMax, supported: []ThinkingLevel{ThinkingLevelOff, ThinkingLevelHigh, ThinkingLevelXHigh}, want: ThinkingLevelXHigh},
		{name: "tie rounds lower", level: ThinkingLevelXHigh, supported: []ThinkingLevel{ThinkingLevelHigh, ThinkingLevelMax}, want: ThinkingLevelHigh},
		{name: "non-off never rounds to off", level: ThinkingLevelMinimal, supported: []ThinkingLevel{ThinkingLevelOff, ThinkingLevelLow}, want: ThinkingLevelLow},
		{name: "unsupported off uses lowest tier", level: ThinkingLevelOff, supported: []ThinkingLevel{ThinkingLevelLow, ThinkingLevelHigh}, want: ThinkingLevelLow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampThinkingLevel(tt.level, tt.supported); got != tt.want {
				t.Fatalf("ClampThinkingLevel(%s) = %s, want %s", tt.level, got, tt.want)
			}
		})
	}
}

type profiledService struct{ mockService }

func (profiledService) PatchProfile() string { return "codex_apply_patch" }

func TestPatchProfile(t *testing.T) {
	if got := PatchProfile(&mockService{}); got != "flat" {
		t.Fatalf("plain service profile = %q, want flat", got)
	}
	if got := PatchProfile(&profiledService{}); got != "codex_apply_patch" {
		t.Fatalf("profiled service profile = %q, want codex_apply_patch", got)
	}
}
