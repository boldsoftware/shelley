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

// Constants for the "inline image" demo pattern. The service first writes a
// tiny PNG into the conversation working directory via bash, then references it
// with relative-path markdown so the UI renders it through the per-message file
// endpoint (server/message_file.go).
const (
	inlineImagePath     = "shelley-inline-image-demo.png"
	inlineImageSentinel = "SHELLEY_INLINE_IMAGE_DEMO"
	// A small (48x48) four-color PNG so the demo image is actually visible in
	// the UI (a 1x1 pixel would render but be invisible).
	inlineImagePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAADAAAAAwCAIAAADYYG7QAAAAS0lEQVR42u3OMQ0AIAwAsMlBxEQgBznImQiUcGFiH00qoHEyW+Q+LUJISEhISEhISEhISOiz0K3RYtZqISQkJCQkJCQkJCQk9FnoAQiSrlPnJLTeAAAAAElFTkSuQmCC"
)

// requestMentions reports whether any message in the request contains the given
// substring (across text and tool-result content).
func requestMentions(req *llm.Request, needle string) bool {
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if strings.Contains(c.Text, needle) {
				return true
			}
			for _, tr := range c.ToolResult {
				if strings.Contains(tr.Text, needle) {
					return true
				}
			}
		}
	}
	return false
}

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
//   - "fail <error>" - emits a retry warning and returns a failure
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

func (s *PredictableService) Provider() string { return "builtin" }

// SupportsImages reports that the predictable service accepts image inputs
// (it returns image dimensions in its synthetic responses).
func (s *PredictableService) SupportsImages() bool { return true }

// TokenContextWindow returns the maximum token context window size
func (s *PredictableService) TokenContextWindow() int {
	return s.tokenContextWindow
}

// MaxImageDimension returns the maximum allowed image dimension.
func (s *PredictableService) MaxImageDimension() int {
	return 2000
}

// MaxImageBytes returns the maximum allowed encoded image size in bytes.
func (s *PredictableService) MaxImageBytes() int {
	return 5 * 1024 * 1024
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
				switch content.Type {
				case llm.ContentTypeText:
					inputText = strings.TrimSpace(content.Text)
				case llm.ContentTypeToolResult:
					hasToolResult = true
				}
			}
		}
	}

	// If the message is purely a tool result (no text), acknowledge it and end turn.
	if hasToolResult && inputText == "" {
		// Special case: if we previously wrote the inline-image demo file via
		// bash, now reference it with markdown so the UI renders it through the
		// per-message file endpoint. This exercises the full local-image path:
		// model emits ![](relative/path) -> frontend rewrites -> server serves.
		if requestMentions(req, inlineImageSentinel) {
			return s.makeResponse(
				"Here is the generated image:\n\n![demo image]("+inlineImagePath+")\n\nGenerated locally and served from the conversation working directory.",
				inputTokens,
			), nil
		}
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

	case "web search", "citations":
		// Reproduce the Anthropic server-side web-search shape: a server_tool_use
		// block, a web_search_tool_result block, then MANY short text blocks
		// (prose interleaved with cited quotes). Used to exercise the citation
		// coalescing in the UI (adjacent text blocks merge into one paragraph and
		// citations render as inline source markers).
		return s.makeWebSearchCitationsResponse(inputTokens), nil

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

	case "refusal":
		// Simulate a stop_reason=refusal response (model declines mid-turn,
		// often after producing only thinking and no visible content).
		return s.makeRefusalResponse(inputTokens), nil

	default:
		// Handle pattern-based inputs
		if text, ok := strings.CutPrefix(inputText, "echo: "); ok {
			return s.makeResponse(text, inputTokens), nil
		}

		if cmd, ok := strings.CutPrefix(inputText, "bash: "); ok {
			return s.makeBashToolResponse(cmd, inputTokens), nil
		}

		if thoughts, ok := strings.CutPrefix(inputText, "think: "); ok {
			return s.makeThinkingResponse(thoughts, inputTokens), nil
		}

		if filePath, ok := strings.CutPrefix(inputText, "patch: "); ok {
			return s.makePatchToolResponse(filePath, inputTokens), nil
		}

		if rest, ok := strings.CutPrefix(inputText, "fail "); ok {
			errorMsg := strings.TrimSpace(rest)
			if req.OnRetry != nil {
				req.OnRetry(llm.RetryEvent{Attempt: 1, Sleep: time.Second, Err: errorMsg, Provider: "predictable", Model: "predictable-v1"})
			}
			return nil, fmt.Errorf("predictable failure: %s", errorMsg)
		}

		if errorMsg, ok := strings.CutPrefix(inputText, "error: "); ok {
			return nil, fmt.Errorf("predictable error: %s", errorMsg)
		}

		if rest, ok := strings.CutPrefix(inputText, "screenshot: "); ok {
			selector := strings.TrimSpace(rest)
			return s.makeScreenshotToolResponse(selector, inputTokens), nil
		}

		if rest, ok := strings.CutPrefix(inputText, "subagent: "); ok {
			// Format: "subagent: <slug> <prompt>"
			parts := strings.SplitN(rest, " ", 2)
			slug := parts[0]
			prompt := "do the task"
			if len(parts) > 1 {
				prompt = parts[1]
			}
			return s.makeSubagentToolResponse(slug, prompt, inputTokens), nil
		}

		if text, ok := strings.CutPrefix(inputText, "markdown: "); ok {
			return s.makeResponse(text, inputTokens), nil
		}

		if inputText == "inline image" {
			// Write a tiny valid PNG to the working directory, then (next turn)
			// reference it in markdown. The sentinel lets us recognize the
			// follow-up tool result.
			cmd := fmt.Sprintf(
				"printf %%s %q | base64 -d > %s && echo %s",
				inlineImagePNGBase64, inlineImagePath, inlineImageSentinel,
			)
			return s.makeBashToolResponse(cmd, inputTokens), nil
		}

		if path, ok := strings.CutPrefix(inputText, "change_dir: "); ok {
			return s.makeChangeDirToolResponse(path, inputTokens), nil
		}

		if delayStr, ok := strings.CutPrefix(inputText, "delay: "); ok {
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

		// An input mentioning this sentinel anywhere (e.g. embedded in a
		// summarization transcript) yields an empty-text response, used to test
		// handling of models that return no content (e.g. refusals).
		if strings.Contains(inputText, "PREDICTABLE_EMPTY_RESPONSE") {
			return s.makeResponse("", inputTokens), nil
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

// makeRefusalResponse creates a response that simulates the model declining to
// continue (stop_reason=refusal). Mirrors what real providers do: they may emit
// only a thinking block (or nothing) and set stop_reason=refusal, leaving no
// visible content for the user.
func (s *PredictableService) makeRefusalResponse(inputTokens uint64) *llm.Response {
	return &llm.Response{
		ID:    fmt.Sprintf("pred-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "", Signature: "pred-sig"},
		},
		StopReason: llm.StopReasonRefusal,
		RefusalDetails: &llm.RefusalDetails{
			Category: "cyber",
			Explanation: "This request triggered restrictions on violative cyber " +
				"content and was blocked under Anthropic's Usage Policy. To learn " +
				"more, see https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback.",
		},
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: 42,
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

// SetResponseDelay makes every Do() call block for d before responding,
// so tests can keep a conversation "working" for a deterministic window.
func (s *PredictableService) SetResponseDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responseDelay = d
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

// webCite builds an Anthropic-shaped citation array for a single source.
func webCite(citedText, url, title string) json.RawMessage {
	b, _ := json.Marshal([]map[string]string{{
		"type":            "web_search_result_location",
		"cited_text":      citedText,
		"url":             url,
		"title":           title,
		"encrypted_index": "idx",
	}})
	return json.RawMessage(b)
}

// makeWebSearchCitationsResponse reproduces the multi-block message Anthropic
// returns for a server-side web search: a server_tool_use block, the
// web_search_tool_result with sources, then a long run of short text blocks
// where cited quotes carry a Citations array. The UI should coalesce the
// adjacent text blocks into flowing paragraphs and surface inline citation
// markers + a Sources list.
func (s *PredictableService) makeWebSearchCitationsResponse(inputTokens uint64) *llm.Response {
	baseNano := time.Now().UnixNano()
	searchID := fmt.Sprintf("srvtoolu_%d", baseNano%100000)

	searchInput, _ := json.Marshal(map[string]string{"query": "pi coding agent switch models"})

	const (
		urlGit   = "https://github.com/earendil-works/pi"
		urlDocs  = "https://pi.dev/docs/models"
		urlBlog  = "https://pi.dev/blog/model-switching"
		titleGit = "earendil-works/pi: a tiny coding agent"
		titleDoc = "Pi Docs — Switching models mid-session"
		titleBlg = "Model switching workflows with Pi"
	)

	content := []llm.Content{
		{
			ID:        searchID,
			Type:      llm.ContentTypeServerToolUse,
			ToolName:  "web_search",
			ToolInput: json.RawMessage(searchInput),
		},
		{
			Type:      llm.ContentTypeWebSearchToolResult,
			ToolUseID: searchID,
			ToolResult: []llm.Content{
				{Type: llm.ContentTypeWebSearchResult, Title: titleGit, URL: urlGit, PageAge: "3 days ago"},
				{Type: llm.ContentTypeWebSearchResult, Title: titleDoc, URL: urlDocs, PageAge: "1 week ago"},
				{Type: llm.ContentTypeWebSearchResult, Title: titleBlg, URL: urlBlog, PageAge: "2 months ago"},
			},
		},
		// Now the prose, split into many small text blocks the way Anthropic
		// streams it: a sentence is interrupted by a cited quote in its own block.
		{Type: llm.ContentTypeText, Text: "Pi makes mid-session model switching a core feature, so you can change models on an in-progress conversation without losing context.\n\n**Built-in ways to switch:**\n- "},
		{Type: llm.ContentTypeText, Text: "Switch models mid-session with /model or Ctrl+L. Cycle through your favorites with Ctrl+P.", Citations: webCite("Switch models mid-session with /model or Ctrl+L", urlDocs, titleDoc)},
		{Type: llm.ContentTypeText, Text: " The `/model` command is the discoverable way if you don't want to remember the shortcut.\n\n**Why this is seamless:** Pi sits on a unified multi-provider API layer, so "},
		{Type: llm.ContentTypeText, Text: "mid-session model switching across 15+ providers lets you use Claude for exploration, GPT for a second opinion, Gemini for large context.", Citations: webCite("mid-session model switching across 15+ providers", urlGit, titleGit)},
		{Type: llm.ContentTypeText, Text: "\n\n**Typical workflow people use:**\n"},
		{Type: llm.ContentTypeText, Text: "Start with a small model for quick lookups and small edits, switch to a larger model for complex reasoning and multi-file changes, then switch back to the small one for running tests and fixing lint errors.", Citations: webCite("Start with a small model for quick lookups", urlBlog, titleBlg)},
		{Type: llm.ContentTypeText, Text: "\n\nOne nice bonus tied to switching: Pi keeps "},
		{Type: llm.ContentTypeText, Text: "tree-structured sessions — every branch preserved; rewind 10 messages, try something else, never lose work", Citations: webCite("tree-structured sessions — every branch preserved", urlGit, titleGit)},
		{Type: llm.ContentTypeText, Text: ", so model switching pairs well with rewinding to retry a step with a different model."},
	}

	outputTokens := uint64(0)
	for _, c := range content {
		outputTokens += uint64(len(c.Text) / 4)
	}
	if outputTokens == 0 {
		outputTokens = 1
	}
	return &llm.Response{
		ID:         fmt.Sprintf("pred-websearch-%d", baseNano),
		Type:       "message",
		Role:       llm.MessageRoleAssistant,
		Model:      "predictable-v1",
		Content:    content,
		StopReason: llm.StopReasonStopSequence,
		Usage: llm.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CostUSD:      0.003,
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

	// browser: emulate_device action (folded-in emulation family)
	emulateInput, _ := json.Marshal(map[string]string{"action": "emulate_device", "device": "iphone_14"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_emulate_%d", (baseNano+9)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(emulateInput),
	})

	// browser: network_enable action (folded-in network family)
	networkInput, _ := json.Marshal(map[string]string{"action": "network_enable"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_network_%d", (baseNano+10)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(networkInput),
	})

	// browser: accessibility_tree action (folded-in accessibility family)
	accessibilityInput, _ := json.Marshal(map[string]string{"action": "accessibility_tree"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_a11y_%d", (baseNano+11)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(accessibilityInput),
	})

	// browser: profile_metrics action (folded-in profiling family)
	profileInput, _ := json.Marshal(map[string]string{"action": "profile_metrics"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_profile_%d", (baseNano+12)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(profileInput),
	})

	// Backwards-compat: old standalone browser_* tool names from existing DBs.
	// These must still render with their specialized components.
	oldEmulateInput, _ := json.Marshal(map[string]string{"action": "device", "device": "ipad"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_old_emulate_%d", (baseNano+16)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_emulate",
		ToolInput: json.RawMessage(oldEmulateInput),
	})
	oldNetworkInput, _ := json.Marshal(map[string]string{"action": "enable"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_old_network_%d", (baseNano+17)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_network",
		ToolInput: json.RawMessage(oldNetworkInput),
	})
	oldA11yInput, _ := json.Marshal(map[string]string{"action": "tree"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_old_a11y_%d", (baseNano+18)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_accessibility",
		ToolInput: json.RawMessage(oldA11yInput),
	})
	oldProfileInput, _ := json.Marshal(map[string]string{"action": "metrics"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_old_profile_%d", (baseNano+19)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_profile",
		ToolInput: json.RawMessage(oldProfileInput),
	})

	// llm_one_shot tool
	llmInput, _ := json.Marshal(map[string]any{"prompt_files": []string{"/tmp/test-prompt.txt", "/tmp/test-image.png"}})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_llm_%d", (baseNano+13)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "llm_one_shot",
		ToolInput: json.RawMessage(llmInput),
	})

	// browser: screencast_stop action (tests screencast UI widget)
	screencastInput, _ := json.Marshal(map[string]string{"action": "screencast_stop"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_screencast_%d", (baseNano+14)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser",
		ToolInput: json.RawMessage(screencastInput),
	})

	// shell tool (yielding successor to bash; should reuse BashTool widget)
	shellInput, _ := json.Marshal(map[string]string{"command": "echo 'hello from shell'"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_shell_%d", (baseNano+15)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "shell",
		ToolInput: json.RawMessage(shellInput),
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
