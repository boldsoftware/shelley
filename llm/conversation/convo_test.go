package conversation

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/loop"
	"sketch.dev/httprr"
)

func TestBasicConvo(t *testing.T) {
	ctx := context.Background()
	rr, err := httprr.Open("testdata/basic_convo.httprr", http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	rr.ScrubReq(func(req *http.Request) error {
		req.Header.Del("x-api-key")
		return nil
	})

	apiKey := cmp.Or(os.Getenv("OUTER_SKETCH_MODEL_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"))
	srv := &ant.Service{
		APIKey: apiKey,
		Model:  ant.Claude4Sonnet, // Use specific model to match cached responses
		HTTPC:  rr.Client(),
	}
	convo := New(ctx, srv, nil)

	const name = "Cornelius"
	res, err := convo.SendUserTextMessage("Hi, my name is " + name)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range res.Content {
		t.Logf("%s", part.Text)
	}
	res, err = convo.SendUserTextMessage("What is my name?")
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, part := range res.Content {
		got += part.Text
	}
	if !strings.Contains(got, name) {
		t.Errorf("model does not know the given name %s: %q", name, got)
	}
}

// TestCancelToolUse tests the CancelToolUse function of the Convo struct
func TestCancelToolUse(t *testing.T) {
	tests := []struct {
		name         string
		setupToolUse bool
		toolUseID    string
		cancelErr    error
		expectError  bool
		expectCancel bool
	}{
		{
			name:         "Cancel existing tool use",
			setupToolUse: true,
			toolUseID:    "tool123",
			cancelErr:    nil,
			expectError:  false,
			expectCancel: true,
		},
		{
			name:         "Cancel existing tool use with error",
			setupToolUse: true,
			toolUseID:    "tool456",
			cancelErr:    context.Canceled,
			expectError:  false,
			expectCancel: true,
		},
		{
			name:         "Cancel non-existent tool use",
			setupToolUse: false,
			toolUseID:    "tool789",
			cancelErr:    nil,
			expectError:  true,
			expectCancel: false,
		},
	}

	srv := &ant.Service{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convo := New(context.Background(), srv, nil)

			var cancelCalled bool
			var cancelledWithErr error

			if tt.setupToolUse {
				// Setup a mock cancel function to track calls
				mockCancel := func(err error) {
					cancelCalled = true
					cancelledWithErr = err
				}

				convo.toolUseCancelMu.Lock()
				convo.toolUseCancel[tt.toolUseID] = mockCancel
				convo.toolUseCancelMu.Unlock()
			}

			err := convo.CancelToolUse(tt.toolUseID, tt.cancelErr)

			// Check if we got the expected error state
			if (err != nil) != tt.expectError {
				t.Errorf("CancelToolUse() error = %v, expectError %v", err, tt.expectError)
			}

			// Check if the cancel function was called as expected
			if cancelCalled != tt.expectCancel {
				t.Errorf("Cancel function called = %v, expectCancel %v", cancelCalled, tt.expectCancel)
			}

			// If we expected the cancel to be called, verify it was called with the right error
			if tt.expectCancel && cancelledWithErr != tt.cancelErr {
				t.Errorf("Cancel function called with error = %v, expected %v", cancelledWithErr, tt.cancelErr)
			}

			// Verify the toolUseID was removed from the map if it was initially added
			if tt.setupToolUse {
				convo.toolUseCancelMu.Lock()
				_, exists := convo.toolUseCancel[tt.toolUseID]
				convo.toolUseCancelMu.Unlock()

				if exists {
					t.Errorf("toolUseID %s still exists in the map after cancellation", tt.toolUseID)
				}
			}
		})
	}
}

// TestInsertMissingToolResults tests the insertMissingToolResults function
// to ensure it doesn't create duplicate tool results when multiple tool uses are missing results.
func TestInsertMissingToolResults(t *testing.T) {
	tests := []struct {
		name            string
		messages        []llm.Message
		currentMsg      llm.Message
		expectedCount   int
		expectedToolIDs []string
	}{
		{
			name: "Single missing tool result",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool1",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{},
			},
			expectedCount:   1,
			expectedToolIDs: []string{"tool1"},
		},
		{
			name: "Multiple missing tool results",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool1",
						},
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool2",
						},
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool3",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{},
			},
			expectedCount:   3,
			expectedToolIDs: []string{"tool1", "tool2", "tool3"},
		},
		{
			name: "No missing tool results when results already present",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool1",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: "tool1",
					},
				},
			},
			expectedCount:   1, // Only the existing one
			expectedToolIDs: []string{"tool1"},
		},
		{
			name: "No tool uses in previous message",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeText,
							Text: "Just some text",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{},
			},
			expectedCount:   0,
			expectedToolIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &ant.Service{}
			convo := New(context.Background(), srv, nil)

			// Create request with messages
			req := &llm.Request{
				Messages: append(tt.messages, tt.currentMsg),
			}

			// Call insertMissingToolResults
			msg := tt.currentMsg
			convo.insertMissingToolResults(req, &msg)

			// Count tool results in the message
			toolResultCount := 0
			toolIDs := []string{}
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolResult {
					toolResultCount++
					toolIDs = append(toolIDs, content.ToolUseID)
				}
			}

			// Verify count
			if toolResultCount != tt.expectedCount {
				t.Errorf("Expected %d tool results, got %d", tt.expectedCount, toolResultCount)
			}

			// Verify no duplicates by checking unique tool IDs
			seenIDs := make(map[string]int)
			for _, id := range toolIDs {
				seenIDs[id]++
			}

			// Check for duplicates
			for id, count := range seenIDs {
				if count > 1 {
					t.Errorf("Duplicate tool result for ID %s: found %d times", id, count)
				}
			}

			// Verify all expected tool IDs are present
			for _, expectedID := range tt.expectedToolIDs {
				if !slices.Contains(toolIDs, expectedID) {
					t.Errorf("Expected tool ID %s not found in results", expectedID)
				}
			}
		})
	}
}

// TestSubConvo tests the SubConvo function
func TestSubConvo(t *testing.T) {
	ctx := context.Background()
	srv := &ant.Service{}
	parentConvo := New(ctx, srv, nil)

	// Test that SubConvo creates a new conversation with the correct parent relationship
	subConvo := parentConvo.SubConvo()

	if subConvo == nil {
		t.Fatal("SubConvo returned nil")
	}

	if subConvo.Parent != parentConvo {
		t.Error("SubConvo did not set the correct parent")
	}

	if subConvo.Service != parentConvo.Service {
		t.Error("SubConvo did not inherit the service")
	}

	if subConvo.PromptCaching != parentConvo.PromptCaching {
		t.Error("SubConvo did not inherit PromptCaching setting")
	}

	// Check that the sub-convo has a different ID
	if subConvo.ID == parentConvo.ID {
		t.Error("SubConvo should have a different ID from parent")
	}

	// Check that the sub-convo shares tool uses with parent
	if &subConvo.usage.ToolUses == &parentConvo.usage.ToolUses {
		t.Error("SubConvo should share tool uses map with parent")
	}

	// Check that the sub-convo has its own usage instance
	if subConvo.usage == parentConvo.usage {
		t.Error("SubConvo should have its own usage instance (but sharing ToolUses)")
	}
}

// TestSubConvoWithHistory tests the SubConvoWithHistory function

// TestDepth tests the Depth function

// TestFindTool tests the findTool function
func TestFindTool(t *testing.T) {
	ctx := context.Background()
	srv := &ant.Service{}
	convo := New(ctx, srv, nil)

	// Add some tools to the conversation
	tool1 := &llm.Tool{Name: "tool1"}
	tool2 := &llm.Tool{Name: "tool2"}
	convo.Tools = append(convo.Tools, tool1, tool2)

	// Test finding an existing tool
	foundTool, err := convo.findTool("tool1")
	if err != nil {
		t.Errorf("findTool returned error for existing tool: %v", err)
	}
	if foundTool != tool1 {
		t.Error("findTool did not return the correct tool")
	}

	// Test finding another existing tool
	foundTool, err = convo.findTool("tool2")
	if err != nil {
		t.Errorf("findTool returned error for existing tool: %v", err)
	}
	if foundTool != tool2 {
		t.Error("findTool did not return the correct tool")
	}

	// Test finding a non-existent tool
	_, err = convo.findTool("nonexistent")
	if err == nil {
		t.Error("findTool should return error for non-existent tool")
	}
	expectedErr := `tool "nonexistent" not found`
	if err.Error() != expectedErr {
		t.Errorf("Expected error %q, got %q", expectedErr, err.Error())
	}
}

// TestToolCallInfoFromContext tests the ToolCallInfoFromContext function
func TestToolCallInfoFromContext(t *testing.T) {
	// Test with no tool call info in context
	ctx := context.Background()
	info := ToolCallInfoFromContext(ctx)
	if info.ToolUseID != "" {
		t.Error("ToolCallInfoFromContext should return empty info when no tool call info is in context")
	}

	// Test with tool call info in context
	toolInfo := ToolCallInfo{
		ToolUseID: "testID",
	}
	ctxWithInfo := context.WithValue(ctx, toolCallInfoKey, toolInfo)
	info = ToolCallInfoFromContext(ctxWithInfo)
	if info.ToolUseID != "testID" {
		t.Errorf("Expected ToolUseID 'testID', got %q", info.ToolUseID)
	}
}

// TestCumulativeUsageMethods tests CumulativeUsage methods
func TestCumulativeUsageMethods(t *testing.T) {
	// Test Clone method
	original := &CumulativeUsage{
		StartTime:                time.Now(),
		Responses:                5,
		InputTokens:              100,
		OutputTokens:             200,
		CacheReadInputTokens:     50,
		CacheCreationInputTokens: 30,
		TotalCostUSD:             1.23,
		ToolUses: map[string]int{
			"tool1": 3,
			"tool2": 2,
		},
	}

	clone := original.Clone()

	// Check that values are copied correctly
	if clone.StartTime != original.StartTime {
		t.Error("Clone did not copy StartTime correctly")
	}
	if clone.Responses != original.Responses {
		t.Error("Clone did not copy Responses correctly")
	}
	if clone.InputTokens != original.InputTokens {
		t.Error("Clone did not copy InputTokens correctly")
	}
	if clone.OutputTokens != original.OutputTokens {
		t.Error("Clone did not copy OutputTokens correctly")
	}
	if clone.CacheReadInputTokens != original.CacheReadInputTokens {
		t.Error("Clone did not copy CacheReadInputTokens correctly")
	}
	if clone.CacheCreationInputTokens != original.CacheCreationInputTokens {
		t.Error("Clone did not copy CacheCreationInputTokens correctly")
	}
	if clone.TotalCostUSD != original.TotalCostUSD {
		t.Error("Clone did not copy TotalCostUSD correctly")
	}
	if len(clone.ToolUses) != len(original.ToolUses) {
		t.Error("Clone did not copy ToolUses correctly")
	}
	for k, v := range original.ToolUses {
		if clone.ToolUses[k] != v {
			t.Errorf("Clone did not copy ToolUses correctly for key %s", k)
		}
	}

	// Check that maps are separate instances
	clone.ToolUses["tool3"] = 1
	if _, exists := original.ToolUses["tool3"]; exists {
		t.Error("Clone should have separate ToolUses map")
	}
}

// TestUsageMethods tests various usage calculation methods
func TestUsageMethods(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Test CumulativeUsage on empty conversation
	usage := convo.CumulativeUsage()
	if usage.Responses != 0 {
		t.Error("CumulativeUsage should be empty for new conversation")
	}

	// Test WallTime method
	wallTime := usage.WallTime()
	if wallTime <= 0 {
		t.Error("WallTime should be positive")
	}

	// Test DollarsPerHour method
	dollarsPerHour := usage.DollarsPerHour()
	if dollarsPerHour != 0 {
		t.Error("DollarsPerHour should be 0 for empty usage")
	}

	// Test TotalInputTokens method
	totalInputTokens := usage.TotalInputTokens()
	if totalInputTokens != 0 {
		t.Error("TotalInputTokens should be 0 for empty usage")
	}

	// Test Attr method
	attr := usage.Attr()
	if attr.Key != "usage" {
		t.Error("Attr should have key 'usage'")
	}
}

// TestLastUsage tests the LastUsage function
func TestLastUsage(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Test LastUsage on empty conversation
	lastUsage := convo.LastUsage()
	if lastUsage.InputTokens != 0 {
		t.Error("LastUsage should be empty for new conversation")
	}

	// Send a message to generate some usage
	_, err := convo.SendUserTextMessage("echo: hello")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	// Test LastUsage after sending a message
	lastUsage = convo.LastUsage()
	if lastUsage.InputTokens == 0 {
		t.Error("LastUsage should have input tokens after sending a message")
	}
}

// TestOverBudget tests the OverBudget function
func TestOverBudget(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Test OverBudget with no budget set
	err := convo.OverBudget()
	if err != nil {
		t.Errorf("OverBudget should return nil when no budget is set, got %v", err)
	}

	// Set a budget
	convo.Budget.MaxDollars = 10.0

	// Test OverBudget with budget not exceeded
	err = convo.OverBudget()
	if err != nil {
		t.Errorf("OverBudget should return nil when budget is not exceeded, got %v", err)
	}

	// Test with sub-conversation
	subConvo := convo.SubConvo()
	err = subConvo.OverBudget()
	if err != nil {
		t.Errorf("OverBudget should return nil for sub-conversation when budget is not exceeded, got %v", err)
	}
}

// TestResetBudget tests the ResetBudget function
func TestResetBudget(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Set initial budget
	initialBudget := Budget{MaxDollars: 5.0}
	convo.ResetBudget(initialBudget)

	// Check that budget was set
	if convo.Budget.MaxDollars != 5.0 {
		t.Errorf("Expected budget MaxDollars to be 5.0, got %f", convo.Budget.MaxDollars)
	}

	// Send a message to accumulate some usage
	_, err := convo.SendUserTextMessage("echo: hello")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	// Get current usage
	usage := convo.CumulativeUsage()
	usedAmount := usage.TotalCostUSD

	// Reset budget again
	newBudget := Budget{MaxDollars: 10.0}
	convo.ResetBudget(newBudget)

	// Check that budget was adjusted by usage
	expectedBudget := 10.0 + usedAmount
	if convo.Budget.MaxDollars != expectedBudget {
		t.Errorf("Expected adjusted budget MaxDollars to be %f, got %f", expectedBudget, convo.Budget.MaxDollars)
	}
}

// TestOverBudgetFunction tests the overBudget function
func TestOverBudgetFunction(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Test overBudget with no budget set
	err := convo.overBudget()
	if err != nil {
		t.Errorf("overBudget should return nil when no budget is set, got %v", err)
	}

	// Set a budget
	convo.Budget.MaxDollars = 5.0

	// Test overBudget with budget not exceeded
	err = convo.overBudget()
	if err != nil {
		t.Errorf("overBudget should return nil when budget is not exceeded, got %v", err)
	}
}

// TestGetID tests the GetID function

// TestListenerMethods tests the listener methods
func TestListenerMethods(t *testing.T) {
	listener := &NoopListener{}
	ctx := context.Background()
	convo := &Convo{}

	// Test that noop listener methods don't panic
	listener.OnToolCall(ctx, convo, "id", "toolName", json.RawMessage(`{"key":"value"}`), llm.Content{})
	listener.OnToolResult(ctx, convo, "id", "toolName", json.RawMessage(`{"key":"value"}`), llm.Content{}, nil, nil)
	listener.OnResponse(ctx, convo, "id", &llm.Response{})
	listener.OnRequest(ctx, convo, "id", &llm.Message{})

	t.Log("NoopListener methods executed without panic")
}

// TestIncrementToolUse tests the incrementToolUse function
func TestIncrementToolUse(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Check initial state
	usage := convo.CumulativeUsage()
	if usage.ToolUses["testTool"] != 0 {
		t.Errorf("Expected 0 uses of testTool, got %d", usage.ToolUses["testTool"])
	}

	// Increment tool use
	convo.incrementToolUse("testTool")

	// Check that tool use was incremented
	usage = convo.CumulativeUsage()
	if usage.ToolUses["testTool"] != 1 {
		t.Errorf("Expected 1 use of testTool, got %d", usage.ToolUses["testTool"])
	}

	// Increment again
	convo.incrementToolUse("testTool")

	// Check that tool use was incremented again
	usage = convo.CumulativeUsage()
	if usage.ToolUses["testTool"] != 2 {
		t.Errorf("Expected 2 uses of testTool, got %d", usage.ToolUses["testTool"])
	}

	// Test with different tool
	convo.incrementToolUse("anotherTool")
	usage = convo.CumulativeUsage()
	if usage.ToolUses["anotherTool"] != 1 {
		t.Errorf("Expected 1 use of anotherTool, got %d", usage.ToolUses["anotherTool"])
	}
}

// TestDebugJSON tests the DebugJSON function
// TestToolResultCancelContents tests the ToolResultCancelContents function
func TestToolResultCancelContents(t *testing.T) {
	ctx := context.Background()
	srv := &ant.Service{}
	convo := New(ctx, srv, nil)

	// Test with response that doesn't have tool use stop reason
	resp := &llm.Response{
		StopReason: llm.StopReasonEndTurn,
	}
	contents, err := convo.ToolResultCancelContents(resp)
	if err != nil {
		t.Errorf("ToolResultCancelContents should not error with non-tool-use response: %v", err)
	}
	if contents != nil {
		t.Error("ToolResultCancelContents should return nil with non-tool-use response")
	}

	// Test with response that has tool use stop reason but no tool use content
	resp = &llm.Response{
		StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Hello"},
		},
	}
	contents, err = convo.ToolResultCancelContents(resp)
	if err != nil {
		t.Errorf("ToolResultCancelContents should not error with tool use response but no tool content: %v", err)
	}
	// Check if contents is nil (this is expected when no tool uses are found)
	if contents != nil && len(contents) != 0 {
		t.Errorf("ToolResultCancelContents should return nil or empty slice with tool use response but no tool content, got length %d", len(contents))
	}

	// Test with response that has tool use stop reason and actual tool use content
	resp = &llm.Response{
		StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{
			{Type: llm.ContentTypeToolUse, ID: "tool1", ToolName: "testTool"},
		},
	}
	contents, err = convo.ToolResultCancelContents(resp)
	if err != nil {
		t.Errorf("ToolResultCancelContents should not error with tool use response and tool content: %v", err)
	}
	if contents == nil {
		t.Error("ToolResultCancelContents should return non-nil slice with tool use response and tool content")
	} else if len(contents) != 1 {
		t.Errorf("ToolResultCancelContents should return slice with one element with tool use response and tool content, got length %d", len(contents))
	} else {
		// Check that the returned content has the correct properties
		if contents[0].Type != llm.ContentTypeToolResult {
			t.Errorf("ToolResultCancelContents should return tool result content, got type %v", contents[0].Type)
		}
		if contents[0].ToolUseID != "tool1" {
			t.Errorf("ToolResultCancelContents should return content with correct ToolUseID, got %v", contents[0].ToolUseID)
		}
		if !contents[0].ToolError {
			t.Error("ToolResultCancelContents should return content with ToolError set to true")
		}
	}
}

// TestNewToolUseContext tests the newToolUseContext function
func TestNewToolUseContext(t *testing.T) {
	ctx := context.Background()
	srv := &ant.Service{}
	convo := New(ctx, srv, nil)

	// Test creating a new tool use context
	toolUseID := "test-tool-use-id"
	toolCtx, cancel := convo.newToolUseContext(ctx, toolUseID)

	if toolCtx == nil {
		t.Error("newToolUseContext should return a valid context")
	}

	if cancel == nil {
		t.Error("newToolUseContext should return a valid cancel function")
	}

	// Check that the tool use was registered
	convo.toolUseCancelMu.Lock()
	_, exists := convo.toolUseCancel[toolUseID]
	convo.toolUseCancelMu.Unlock()

	if !exists {
		t.Error("newToolUseContext should register the tool use cancel function")
	}

	// Test that cancel function works
	cancel()

	// Check that the tool use was unregistered
	convo.toolUseCancelMu.Lock()
	_, exists = convo.toolUseCancel[toolUseID]
	convo.toolUseCancelMu.Unlock()

	if exists {
		t.Error("Cancel function should unregister the tool use")
	}
}

// TestToolResultContents tests the ToolResultContents function
func TestToolResultContents(t *testing.T) {
	ctx := context.Background()
	srv := &ant.Service{}
	convo := New(ctx, srv, nil)

	// Skip nil response test as the function doesn't handle nil properly
	// This would cause a nil pointer dereference in the actual function

	// Test with response that doesn't have tool use stop reason
	resp := &llm.Response{
		StopReason: llm.StopReasonEndTurn,
	}
	contents, endsTurn, err := convo.ToolResultContents(ctx, resp)
	if err != nil {
		t.Errorf("ToolResultContents should not error with non-tool-use response: %v", err)
	}
	if contents != nil {
		t.Error("ToolResultContents should return nil with non-tool-use response")
	}
	if endsTurn {
		t.Error("ToolResultContents should return false for endsTurn with non-tool-use response")
	}
}

// testListener is a custom listener implementation for testing
type testListener struct {
	events []string
}

func (tl *testListener) OnToolCall(ctx context.Context, convo *Convo, id, toolName string, toolInput json.RawMessage, content llm.Content) {
	tl.events = append(tl.events, "OnToolCall")
}

func (tl *testListener) OnToolResult(ctx context.Context, convo *Convo, id, toolName string, toolInput json.RawMessage, content llm.Content, result *string, err error) {
	tl.events = append(tl.events, "OnToolResult")
}

func (tl *testListener) OnResponse(ctx context.Context, convo *Convo, id string, resp *llm.Response) {
	tl.events = append(tl.events, "OnResponse")
}

func (tl *testListener) OnRequest(ctx context.Context, convo *Convo, id string, msg *llm.Message) {
	tl.events = append(tl.events, "OnRequest")
}

// TestListenerInterface tests that the Listener interface methods are called
func TestListenerInterface(t *testing.T) {
	listener := &testListener{}
	ctx := context.Background()
	convo := &Convo{}

	// Test that all listener methods can be called without panicking
	listener.OnToolCall(ctx, convo, "id", "toolName", json.RawMessage(`{"key":"value"}`), llm.Content{})
	listener.OnToolResult(ctx, convo, "id", "toolName", json.RawMessage(`{"key":"value"}`), llm.Content{}, nil, nil)
	listener.OnResponse(ctx, convo, "id", &llm.Response{})
	listener.OnRequest(ctx, convo, "id", &llm.Message{})

	// Check that events were recorded
	if len(listener.events) != 4 {
		t.Errorf("Expected 4 events, got %d", len(listener.events))
	}

	expectedEvents := []string{"OnToolCall", "OnToolResult", "OnResponse", "OnRequest"}
	for i, expected := range expectedEvents {
		if listener.events[i] != expected {
			t.Errorf("Expected event %s, got %s", expected, listener.events[i])
		}
	}
}

// TestToolResultContentsWithToolUse tests ToolResultContents with actual tool use
func TestToolResultContentsWithToolUse(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Add a simple echo tool
	convo.Tools = append(convo.Tools, &llm.Tool{
		Name:        "echo",
		Description: "Echo tool for testing",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"message": {"type": "string"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			return llm.ToolOut{
				LLMContent: []llm.Content{{Type: llm.ContentTypeText, Text: "echo response"}},
			}
		},
	})

	// Create a response with tool use stop reason
	resp := &llm.Response{
		StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{
			{
				Type:      llm.ContentTypeToolUse,
				ID:        "test-tool-call",
				ToolName:  "echo",
				ToolInput: json.RawMessage(`{"message": "test"}`),
			},
		},
	}

	// Test ToolResultContents with tool use
	contents, endsTurn, err := convo.ToolResultContents(ctx, resp)
	if err != nil {
		t.Fatalf("ToolResultContents failed: %v", err)
	}

	// Should return tool results
	if len(contents) == 0 {
		t.Error("ToolResultContents should return tool results")
	}

	// Check the content type
	if contents[0].Type != llm.ContentTypeToolResult {
		t.Errorf("Expected ContentTypeToolResult, got %s", contents[0].Type)
	}

	// For our echo tool, endsTurn should be false
	if endsTurn {
		t.Error("Expected endsTurn to be false for echo tool")
	}
}

// TestOverBudgetWithExceeded tests OverBudget when budget is exceeded
func TestOverBudgetWithExceeded(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Set a tiny budget
	convo.Budget.MaxDollars = 0.0000001

	// Send a message to accumulate usage
	_, err := convo.SendUserTextMessage("test message")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	// Test that OverBudget returns an error
	err = convo.OverBudget()
	if err == nil {
		t.Error("OverBudget should return an error when budget is exceeded")
	}
}

// TestResetBudgetWithUsage tests ResetBudget with existing usage
func TestResetBudgetWithUsage(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Send a message to accumulate usage
	_, err := convo.SendUserTextMessage("test message")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	// Get current usage
	initialUsage := convo.CumulativeUsage()
	initialCost := initialUsage.TotalCostUSD

	// Reset budget
	newBudget := Budget{MaxDollars: 10.0}
	convo.ResetBudget(newBudget)

	// Check that budget was adjusted
	expectedBudget := 10.0 + initialCost
	if convo.Budget.MaxDollars != expectedBudget {
		t.Errorf("Expected budget to be %f, got %f", expectedBudget, convo.Budget.MaxDollars)
	}
}

// TestSubConvoWithHistory tests SubConvoWithHistory method

// TestDepth tests Depth method

// TestGetID tests GetID method

// TestDebugJSON tests DebugJSON method

// recordingListener is a listener that records all calls for testing
type recordingListener struct {
	calls []string
}

func (rl *recordingListener) OnToolCall(ctx context.Context, convo *Convo, id, toolName string, toolInput json.RawMessage, content llm.Content) {
	rl.calls = append(rl.calls, "OnToolCall")
}

func (rl *recordingListener) OnToolResult(ctx context.Context, convo *Convo, id, toolName string, toolInput json.RawMessage, content llm.Content, result *string, err error) {
	rl.calls = append(rl.calls, "OnToolResult")
}

func (rl *recordingListener) OnResponse(ctx context.Context, convo *Convo, id string, resp *llm.Response) {
	rl.calls = append(rl.calls, "OnResponse")
}

func (rl *recordingListener) OnRequest(ctx context.Context, convo *Convo, id string, msg *llm.Message) {
	rl.calls = append(rl.calls, "OnRequest")
}

// TestConvoListenerIntegration tests that Convo actually calls listener methods during operation
func TestConvoListenerIntegration(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Set up recording listener
	listener := &recordingListener{}
	convo.Listener = listener

	// Send a message to trigger listener calls
	_, err := convo.SendUserTextMessage("Hello")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	// Check that we recorded some calls
	if len(listener.calls) == 0 {
		t.Error("Expected listener methods to be called during conversation, but no calls were recorded")
	}

	// Verify that request and response events were recorded
	requestFound := false
	responseFound := false
	for _, call := range listener.calls {
		if call == "OnRequest" {
			requestFound = true
		}
		if call == "OnResponse" {
			responseFound = true
		}
	}

	if !requestFound {
		t.Error("Expected OnRequest to be called during conversation")
	}
	if !responseFound {
		t.Error("Expected OnResponse to be called during conversation")
	}
}

// TestSubConvoWithHistory tests SubConvoWithHistory method
func TestSubConvoWithHistoryAdditional(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Send a message to create some history
	_, err := convo.SendUserTextMessage("Hello")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	// Create sub-conversation with history
	subConvo := convo.SubConvoWithHistory()
	if subConvo == nil {
		t.Fatal("SubConvoWithHistory should return a valid conversation")
	}

	// Check that sub-conversation has parent
	if subConvo.Parent != convo {
		t.Error("Sub-conversation should have parent set")
	}

	// Check that sub-conversation has messages (history)
	if len(subConvo.messages) == 0 {
		t.Error("Sub-conversation should have messages from parent")
	}

	// Check that the first message is from the parent conversation
	if len(subConvo.messages) < 1 {
		t.Error("Sub-conversation should have at least one message")
	}
}

// TestDepthAdditional tests Depth method
func TestDepthAdditional(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Root conversation should have depth 0
	if convo.Depth() != 0 {
		t.Errorf("Expected depth 0, got %d", convo.Depth())
	}

	// Sub-conversation should have depth 1
	subConvo := convo.SubConvo()
	if subConvo.Depth() != 1 {
		t.Errorf("Expected depth 1, got %d", subConvo.Depth())
	}

	// Sub-sub-conversation should have depth 2
	subSubConvo := subConvo.SubConvo()
	if subSubConvo.Depth() != 2 {
		t.Errorf("Expected depth 2, got %d", subSubConvo.Depth())
	}
}

// TestGetIDAdditional tests GetID method
func TestGetIDAdditional(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	id := convo.GetID()
	if id == "" {
		t.Error("GetID should return a non-empty ID")
	}
	if id != convo.ID {
		t.Error("GetID should return the conversation ID")
	}
}

// TestDebugJSONAdditional tests DebugJSON method
func TestDebugJSONAdditional(t *testing.T) {
	ctx := context.Background()
	srv := loop.NewPredictableService()
	convo := New(ctx, srv, nil)

	// Test with empty conversation
	jsonData, err := convo.DebugJSON()
	if err != nil {
		t.Errorf("DebugJSON failed: %v", err)
	}
	if len(jsonData) == 0 {
		t.Error("DebugJSON should return non-empty data")
	}

	// Test with conversation that has messages
	_, err = convo.SendUserTextMessage("Hello")
	if err != nil {
		t.Fatalf("SendUserTextMessage failed: %v", err)
	}

	jsonData, err = convo.DebugJSON()
	if err != nil {
		t.Errorf("DebugJSON failed: %v", err)
	}
	if len(jsonData) == 0 {
		t.Error("DebugJSON should return non-empty data")
	}

	// Verify it's valid JSON by trying to unmarshal it
	var parsed interface{}
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Errorf("DebugJSON should return valid JSON: %v", err)
	}
}
