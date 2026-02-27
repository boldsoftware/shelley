package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/llm"
)

// PredictableService is an LLM service that returns predictable responses for testing.
//
// To add new test patterns, update the Do() method directly by adding cases to the switch
// statement or new prefix checks. Do not extend or wrap this service - modify it in place.
// Available patterns include:
//   - "echo: <text>" - echoes the text back
//   - "bash: <command>" - triggers bash tool with command
//   - "think: <thoughts>" - returns response with extended thinking content
//   - "subagent: <slug> <prompt>" - triggers subagent tool
//   - "change_dir: <path>" - triggers change_dir tool
//   - "delay: <seconds>" - delays response by specified seconds
//   - See Do() method for complete list of supported patterns
type PredictableService struct {
	// TokenContextWindow size
	tokenContextWindow int
	mu                 sync.Mutex
	// Recent requests for testing inspection
	recentRequests []*llm.Request
	responseDelay  time.Duration
}

// NewPredictableService creates a new predictable LLM service
func NewPredictableService() *PredictableService {
	svc := &PredictableService{
		tokenContextWindow: 200000,
	}

	if delayEnv := os.Getenv("PREDICTABLE_DELAY_MS"); delayEnv != "" {
		if ms, err := strconv.Atoi(delayEnv); err == nil && ms > 0 {
			svc.responseDelay = time.Duration(ms) * time.Millisecond
		}
	}

	return svc
}

// TokenContextWindow returns the maximum token context window size
func (s *PredictableService) TokenContextWindow() int {
	return s.tokenContextWindow
}

// MaxImageDimension returns the maximum allowed image dimension.
func (s *PredictableService) MaxImageDimension() int {
	return 2000
}

// Do processes a request and returns a predictable response based on the input text
func (s *PredictableService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	// Store request for testing inspection
	s.mu.Lock()
	delay := s.responseDelay
	s.recentRequests = append(s.recentRequests, req)
	// Keep only last 10 requests
	if len(s.recentRequests) > 10 {
		s.recentRequests = s.recentRequests[len(s.recentRequests)-10:]
	}
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Calculate input token count based on the request content
	inputTokens := s.countRequestTokens(req)

	// Extract the text content from the last user message
	var inputText string
	var hasToolResult bool
	if len(req.Messages) > 0 {
		lastMessage := req.Messages[len(req.Messages)-1]
		if lastMessage.Role == llm.MessageRoleUser {
			for _, content := range lastMessage.Content {
				if content.Type == llm.ContentTypeText {
					inputText = strings.TrimSpace(content.Text)
				} else if content.Type == llm.ContentTypeToolResult {
					hasToolResult = true
				}
			}
		}
	}

	// If the message is purely a tool result (no text), acknowledge it and end turn
	if hasToolResult && inputText == "" {
		return s.makeResponse("Done.", inputTokens), nil
	}

	// Handle input using case statements
	switch inputText {
	case "hello":
		return s.makeResponse("Well, hi there!", inputTokens), nil

	case "Hello":
		return s.makeResponse("Hello! I'm Shelley, your AI assistant. How can I help you today?", inputTokens), nil

	case "Create an example":
		return s.makeThinkingResponse("I'll create a simple example for you.", inputTokens), nil

	case "screenshot":
		// Trigger a screenshot of the current page
		return s.makeScreenshotToolResponse("", inputTokens), nil

	case "wide tables":
		return s.makeResponse(wideTablesMarkdown, inputTokens), nil

	case "tool smorgasbord":
		// Return a response with all tool types for testing
		return s.makeToolSmorgasbordResponse(inputTokens), nil

	case "echo: foo":
		return s.makeResponse("foo", inputTokens), nil

	case "patch fail":
		// Trigger a patch that will fail (file doesn't exist)
		return s.makePatchToolResponse("/nonexistent/file/that/does/not/exist.txt", inputTokens), nil

	case "patch success":
		// Trigger a patch that will succeed (using overwrite, which creates the file)
		return s.makePatchToolResponseOverwrite("/tmp/test-patch-success.txt", inputTokens), nil

	case "patch bad json":
		// Trigger a patch with malformed JSON (simulates Anthropic sending invalid JSON)
		return s.makeMalformedPatchToolResponse(inputTokens), nil

	case "maxTokens":
		// Simulate a max_tokens truncation
		return s.makeMaxTokensResponse("This is a truncated response that was cut off mid-sentence because the output token limit was", inputTokens), nil

	default:
		// Handle pattern-based inputs
		if strings.HasPrefix(inputText, "echo: ") {
			text := strings.TrimPrefix(inputText, "echo: ")
			return s.makeResponse(text, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "bash: ") {
			cmd := strings.TrimPrefix(inputText, "bash: ")
			return s.makeBashToolResponse(cmd, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "think: ") {
			thoughts := strings.TrimPrefix(inputText, "think: ")
			return s.makeThinkingResponse(thoughts, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "patch: ") {
			filePath := strings.TrimPrefix(inputText, "patch: ")
			return s.makePatchToolResponse(filePath, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "error: ") {
			errorMsg := strings.TrimPrefix(inputText, "error: ")
			return nil, fmt.Errorf("predictable error: %s", errorMsg)
		}

		if strings.HasPrefix(inputText, "screenshot: ") {
			selector := strings.TrimSpace(strings.TrimPrefix(inputText, "screenshot: "))
			return s.makeScreenshotToolResponse(selector, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "subagent: ") {
			// Format: "subagent: <slug> <prompt>"
			parts := strings.SplitN(strings.TrimPrefix(inputText, "subagent: "), " ", 2)
			slug := parts[0]
			prompt := "do the task"
			if len(parts) > 1 {
				prompt = parts[1]
			}
			return s.makeSubagentToolResponse(slug, prompt, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "markdown: ") {
			text := strings.TrimPrefix(inputText, "markdown: ")
			return s.makeResponse(text, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "change_dir: ") {
			path := strings.TrimPrefix(inputText, "change_dir: ")
			return s.makeChangeDirToolResponse(path, inputTokens), nil
		}

		if strings.HasPrefix(inputText, "delay: ") {
			delayStr := strings.TrimPrefix(inputText, "delay: ")
			delaySeconds, err := strconv.ParseFloat(delayStr, 64)
			if err == nil && delaySeconds > 0 {
				delayDuration := time.Duration(delaySeconds * float64(time.Second))
				select {
				case <-time.After(delayDuration):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return s.makeResponse(fmt.Sprintf("Delayed for %s seconds", delayStr), inputTokens), nil
		}

		// Default response for undefined inputs
		return s.makeResponse("edit predictable.go to add a response for that one...", inputTokens), nil
	}
}

// makeMaxTokensResponse creates a response that simulates hitting max_tokens limit
func (s *PredictableService) makeMaxTokensResponse(text string, inputTokens uint64) *llm.Response {
	outputTokens := uint64(len(text) / 4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: text},
		},
		StopReason: llm.StopReasonMaxTokens,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.001,
		},
	}
}

// makeResponse creates a simple text response
func (s *PredictableService) makeResponse(text string, inputTokens uint64) *llm.Response {
	outputTokens := uint64(len(text) / 4) // ~4 chars per token
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: text},
		},
		StopReason: llm.StopReasonStopSequence,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.001,
		},
	}
}

// makeBashToolResponse creates a response that calls the bash tool
func (s *PredictableService) makeBashToolResponse(command string, inputTokens uint64) *llm.Response {
	// Properly marshal the command to avoid JSON escaping issues
	toolInputData := map[string]string{"command": command}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	responseText := fmt.Sprintf("I'll run the command: %s", command)
	outputTokens := uint64(len(responseText)/4 + len(toolInputBytes)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-bash-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: responseText},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "bash",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.002,
		},
	}
}

// makeThinkingResponse creates a response with extended thinking content
func (s *PredictableService) makeThinkingResponse(thoughts string, inputTokens uint64) *llm.Response {
	responseText := "I've considered my approach."
	outputTokens := uint64(len(responseText)/4 + len(thoughts)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-thinking-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: thoughts},
			{Type: llm.ContentTypeText, Text: responseText},
		},
		StopReason: llm.StopReasonEndTurn,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.002,
		},
	}
}

// makePatchToolResponse creates a response that calls the patch tool
func (s *PredictableService) makePatchToolResponse(filePath string, inputTokens uint64) *llm.Response {
	// Properly marshal the patch data to avoid JSON escaping issues
	toolInputData := map[string]interface{}{
		"path": filePath,
		"patches": []map[string]string{
			{
				"operation": "replace",
				"oldText":   "example",
				"newText":   "updated example",
			},
		},
	}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	responseText := fmt.Sprintf("I'll patch the file: %s", filePath)
	outputTokens := uint64(len(responseText)/4 + len(toolInputBytes)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-patch-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: responseText},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "patch",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.003,
		},
	}
}

// makePatchToolResponseOverwrite creates a response that uses overwrite operation (always succeeds)
func (s *PredictableService) makePatchToolResponseOverwrite(filePath string, inputTokens uint64) *llm.Response {
	toolInputData := map[string]interface{}{
		"path": filePath,
		"patches": []map[string]string{
			{
				"operation": "overwrite",
				"newText":   "This is the new content of the file.\nLine 2\nLine 3\n",
			},
		},
	}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	responseText := fmt.Sprintf("I'll create/overwrite the file: %s", filePath)
	outputTokens := uint64(len(responseText)/4 + len(toolInputBytes)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-patch-overwrite-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: responseText},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "patch",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.0,
		},
	}
}

// makeMalformedPatchToolResponse creates a response with malformed JSON that will fail to parse
// This simulates when Anthropic sends back invalid JSON in the tool input
func (s *PredictableService) makeMalformedPatchToolResponse(inputTokens uint64) *llm.Response {
	// This malformed JSON has a string where an object is expected (patch field)
	// Mimics the error: "cannot unmarshal string into Go struct field PatchInputOneSingular.patch"
	malformedJSON := `{"path":"/home/agent/example.css","patch":"<parameter name=\"operation\">replace","oldText":".example {\n  color: red;\n}","newText":".example {\n  color: blue;\n}"}`
	toolInput := json.RawMessage(malformedJSON)
	return &llm.Response{
		ID:    fmt.Sprintf("pred-patch-malformed-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "I'll patch the file with the changes."},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "patch",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: 50,
			CostUSD:      0.003,
		},
	}
}

// GetRecentRequests returns the recent requests made to this service
func (s *PredictableService) GetRecentRequests() []*llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.recentRequests) == 0 {
		return nil
	}

	requests := make([]*llm.Request, len(s.recentRequests))
	copy(requests, s.recentRequests)
	return requests
}

// GetLastRequest returns the most recent request, or nil if none
func (s *PredictableService) GetLastRequest() *llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.recentRequests) == 0 {
		return nil
	}
	return s.recentRequests[len(s.recentRequests)-1]
}

// ClearRequests clears the request history
func (s *PredictableService) ClearRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recentRequests = nil
}

// countRequestTokens estimates token count based on character count.
// Uses a simple ~4 chars per token approximation.
func (s *PredictableService) countRequestTokens(req *llm.Request) uint64 {
	var totalChars int

	// Count system prompt characters
	for _, sys := range req.System {
		totalChars += len(sys.Text)
	}

	// Count message characters
	for _, msg := range req.Messages {
		for _, content := range msg.Content {
			switch content.Type {
			case llm.ContentTypeText:
				totalChars += len(content.Text)
			case llm.ContentTypeToolUse:
				totalChars += len(content.ToolName)
				totalChars += len(content.ToolInput)
			case llm.ContentTypeToolResult:
				for _, result := range content.ToolResult {
					if result.Type == llm.ContentTypeText {
						totalChars += len(result.Text)
					}
				}
			}
		}
	}

	// Count tool definitions
	for _, tool := range req.Tools {
		totalChars += len(tool.Name)
		totalChars += len(tool.Description)
		totalChars += len(tool.InputSchema)
	}

	// ~4 chars per token is a rough approximation
	return uint64(totalChars / 4)
}

// makeScreenshotToolResponse creates a response that calls the screenshot tool
func (s *PredictableService) makeScreenshotToolResponse(selector string, inputTokens uint64) *llm.Response {
	toolInputData := map[string]any{}
	if selector != "" {
		toolInputData["selector"] = selector
	}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	responseText := "Taking a screenshot..."
	outputTokens := uint64(len(responseText)/4 + len(toolInputBytes)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-screenshot-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: responseText},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "browser",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.0,
		},
	}
}

// makeChangeDirToolResponse creates a response that calls the change_dir tool
func (s *PredictableService) makeChangeDirToolResponse(path string, inputTokens uint64) *llm.Response {
	toolInputData := map[string]string{"path": path}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	responseText := fmt.Sprintf("I'll change to directory: %s", path)
	outputTokens := uint64(len(responseText)/4 + len(toolInputBytes)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-change_dir-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: responseText},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "change_dir",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.001,
		},
	}
}

func (s *PredictableService) makeSubagentToolResponse(slug, prompt string, inputTokens uint64) *llm.Response {
	toolInputData := map[string]any{
		"slug":   slug,
		"prompt": prompt,
	}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	responseText := fmt.Sprintf("Delegating to subagent '%s'...", slug)
	outputTokens := uint64(len(responseText)/4 + len(toolInputBytes)/4)
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:    fmt.Sprintf("pred-subagent-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: responseText},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "subagent",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.0,
		},
	}
}

// makeToolSmorgasbordResponse creates a response that uses all available tool types
func (s *PredictableService) makeToolSmorgasbordResponse(inputTokens uint64) *llm.Response {
	baseNano := time.Now().UnixNano()
	content := []llm.Content{
		{Type: llm.ContentTypeText, Text: "Here's a sample of all the tools:"},
	}

	// bash tool
	bashInput, _ := json.Marshal(map[string]string{"command": "echo 'hello from bash'"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_bash_%d", baseNano%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "bash",
		ToolInput: json.RawMessage(bashInput),
	})

	// extended thinking content (not a tool)
	content = append(content, llm.Content{
		Type:     llm.ContentTypeThinking,
		Thinking: "I'm thinking about the best approach for this task. Let me consider all the options available.",
	})

	// patch tool
	patchInput, _ := json.Marshal(map[string]interface{}{
		"path": "/tmp/example.txt",
		"patches": []map[string]string{
			{"operation": "replace", "oldText": "foo", "newText": "bar"},
		},
	})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_patch_%d", (baseNano+2)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "patch",
		ToolInput: json.RawMessage(patchInput),
	})

	// browser: screenshot action
	screenshotInput, _ := json.Marshal(map[string]string{"action": "screenshot"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_screenshot_%d", (baseNano+3)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(screenshotInput),
	})

	// keyword_search tool
	keywordInput, _ := json.Marshal(map[string]interface{}{
		"query":        "find all references",
		"search_terms": []string{"reference", "example"},
	})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_keyword_%d", (baseNano+4)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "keyword_search",
		ToolInput: json.RawMessage(keywordInput),
	})

	// browser: navigate action
	navigateInput, _ := json.Marshal(map[string]string{"action": "navigate", "url": "https://example.com"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_navigate_%d", (baseNano+5)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(navigateInput),
	})

	// browser: eval action
	evalInput, _ := json.Marshal(map[string]string{"action": "eval", "expression": "document.title"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_eval_%d", (baseNano+6)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(evalInput),
	})

	// read_image tool (separate from browser)
	readImageInput, _ := json.Marshal(map[string]string{"path": "/tmp/image.png"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_readimg_%d", (baseNano+7)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "read_image",
		ToolInput: json.RawMessage(readImageInput),
	})

	// browser: console_logs action
	consoleInput, _ := json.Marshal(map[string]string{"action": "console_logs"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_console_%d", (baseNano+8)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(consoleInput),
	})

	return &llm.Response{
		ID:         fmt.Sprintf("pred-smorgasbord-%d", baseNano),
		Type:       "message",
		Role:       llm.MessageRoleAssistant,
		Model:      "predictable-v1",
		Content:    content,
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: 200,
			CostUSD:      0.01,
		},
	}
}

const wideTablesMarkdown = `Here are some wide tables to test rendering:

## Narrow Table (should look fine)

| Name | Age | City |
|------|-----|------|
| Alice | 30 | NYC |
| Bob | 25 | LA |

## Wide Table (many columns)

| ID | First Name | Last Name | Email Address | Phone Number | Street Address | City | State | Zip Code | Country | Department | Job Title | Start Date | Salary | Manager |
|----|-----------|-----------|--------------|-------------|---------------|------|-------|----------|---------|-----------|-----------|-----------|--------|--------|
| 1 | Alexander | Montgomery | alexander.montgomery@longcompanyname.com | +1-555-0123 | 1234 Willowbrook Lane | San Francisco | California | 94102 | United States | Engineering | Senior Staff Engineer | 2019-03-15 | $185,000 | Sarah Johnson |
| 2 | Elizabeth | Fitzgerald | elizabeth.fitzgerald@longcompanyname.com | +1-555-0456 | 5678 Meadowridge Drive | New York | New York | 10001 | United States | Product Management | Director of Product | 2018-07-22 | $210,000 | Michael Chen |
| 3 | Christopher | Worthington | christopher.worthington@longcompanyname.com | +1-555-0789 | 9012 Thunderbird Road | Chicago | Illinois | 60601 | United States | Data Science | Principal Data Scientist | 2020-01-10 | $195,000 | Sarah Johnson |

## Table with Code and Long Content

| Function | Signature | Description | Example Usage | Return Type |
|----------|-----------|-------------|---------------|-------------|
| ` + "`processDataPipeline`" + ` | ` + "`func processDataPipeline(ctx context.Context, input []DataRecord, opts ...ProcessOption) (*PipelineResult, error)`" + ` | Processes a batch of data records through the configured pipeline stages | ` + "`result, err := processDataPipeline(ctx, records, WithParallelism(4), WithTimeout(30*time.Second))`" + ` | ` + "`*PipelineResult`" + ` |
| ` + "`validateConfiguration`" + ` | ` + "`func validateConfiguration(cfg *Config, validators ...ConfigValidator) ([]ValidationError, error)`" + ` | Validates the configuration against all registered validators | ` + "`errs, err := validateConfiguration(cfg, RequiredFieldsValidator{}, RangeValidator{})`" + ` | ` + "`[]ValidationError`" + ` |

## Table with Long Headers

| Configuration Parameter Name | Default Value | Minimum Allowed Value | Maximum Allowed Value | Environment Variable Override | Description of Behavior |
|------------------------------|---------------|----------------------|----------------------|------------------------------|-------------------------|
| max_concurrent_connections | 100 | 1 | 10000 | APP_MAX_CONNECTIONS | Limits simultaneous connections |
| request_timeout_seconds | 30 | 1 | 300 | APP_REQUEST_TIMEOUT | Per-request timeout |
| background_worker_pool_size | 4 | 1 | 64 | APP_WORKER_POOL | Number of background workers |

## Numeric Data Table

| Metric | Q1 2024 | Q2 2024 | Q3 2024 | Q4 2024 | YoY Change | Trend |
|--------|---------|---------|---------|---------|------------|-------|
| Revenue ($M) | 12.45 | 13.82 | 15.01 | 16.73 | +34.4% | 📈 |
| Active Users | 1,234,567 | 1,456,789 | 1,678,901 | 1,890,123 | +53.2% | 📈 |
| Churn Rate | 4.2% | 3.8% | 3.5% | 3.1% | -26.2% | 📉 |
| NPS Score | 42 | 45 | 48 | 52 | +23.8% | 📈 |

That's a variety of table widths for testing!`
