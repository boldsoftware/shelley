// Package llm provides a unified interface for interacting with LLMs.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Service interface {
	// Do sends a request to an LLM.
	Do(context.Context, *Request) (*Response, error)
	// Provider returns the provider name (e.g., "anthropic", "openai", "fireworks", "gemini").
	Provider() string
	// TokenContextWindow returns the maximum token context window size for this service
	TokenContextWindow() int
	// MaxImageDimension returns the maximum allowed dimension (width or height) for images.
	// For multi-image requests, some providers enforce stricter limits.
	// Returns 0 if there is no limit.
	MaxImageDimension() int
	// MaxImageBytes returns the maximum allowed encoded size in bytes for a single image.
	// Returns 0 if there is no known limit.
	MaxImageBytes() int
	// SupportsImages reports whether the service accepts image inputs.
	SupportsImages() bool
}

type SimplifiedPatcher interface {
	// UseSimplifiedPatch reports whether the service should use the simplified patch input schema.
	UseSimplifiedPatch() bool
}

func UseSimplifiedPatch(svc Service) bool {
	if sp, ok := svc.(SimplifiedPatcher); ok {
		return sp.UseSimplifiedPatch()
	}
	return false
}

// DefaultReasoner is implemented by services that can report the reasoning
// level they apply when a request carries no per-conversation override (i.e.
// ThinkingLevelDefault). This is the level actually sent to the model, so the
// UI can label conversations honestly instead of leaving the badge blank.
type DefaultReasoner interface {
	// DefaultReasoningLevel returns the user-facing name of the reasoning
	// level applied to un-overridden requests ("off", "minimal", "low",
	// "medium", "high", "xhigh", or a provider-verbatim effort string).
	// Empty string means the provider is left to pick its own default and
	// Shelley cannot know the concrete level up front.
	//
	// This reports the configured/effective level by name; it does NOT
	// replicate the per-model effort clamping some request builders apply
	// (e.g. chat backends downgrading "xhigh"->"high"). That only diverges
	// when a service-level default is itself set to a clamped level, which
	// does not happen for the shipped defaults (all "medium").
	DefaultReasoningLevel() string
}

// ServiceDefaultReasoningLevel returns the service's default reasoning level
// name, or "" when the service doesn't implement DefaultReasoner.
func ServiceDefaultReasoningLevel(svc Service) string {
	if dr, ok := svc.(DefaultReasoner); ok {
		return dr.DefaultReasoningLevel()
	}
	return ""
}

// MustSchema validates that schema is a valid JSON schema and returns it as a json.RawMessage.
// It panics if the schema is invalid.
// The schema must have at least type="object" and a properties key.
func MustSchema(schema string) json.RawMessage {
	schema = strings.TrimSpace(schema)
	bytes := []byte(schema)
	var obj map[string]any
	if err := json.Unmarshal(bytes, &obj); err != nil {
		panic("failed to parse JSON schema: " + schema + ": " + err.Error())
	}
	if typ, ok := obj["type"]; !ok || typ != "object" {
		panic("JSON schema must have type='object': " + schema)
	}
	if _, ok := obj["properties"]; !ok {
		panic("JSON schema must have 'properties' key: " + schema)
	}
	return json.RawMessage(bytes)
}

func EmptySchema() json.RawMessage {
	return MustSchema(`{"type": "object", "properties": {}}`)
}

// ErrorType identifies system-generated error messages (not LLM content).
type ErrorType string

const (
	ErrorTypeNone       ErrorType = ""            // Not an error
	ErrorTypeTruncation ErrorType = "truncation"  // Response truncated due to max tokens
	ErrorTypeLLMRequest ErrorType = "llm_request" // LLM request failed
	ErrorTypeRefusal    ErrorType = "refusal"     // Model declined to continue (stop_reason=refusal)
)

// StreamDelta represents a partial content update during streaming.
type StreamDelta struct {
	// Type is the kind of delta: "text", "thinking", "tool_input"
	Type string `json:"type"`
	// Text is the delta text content
	Text string `json:"text"`
	// Index is the content block index in the response
	Index int `json:"index"`
	// Seq is a per-conversation, monotonically increasing sequence number
	// assigned to each partial update broadcast to clients. It lets clients
	// detect dropped or out-of-order deltas. It is assigned by the server
	// when broadcasting (see server.streamFlusher), not by LLM providers.
	Seq int64 `json:"seq"`
}

type Request struct {
	Messages   []Message
	ToolChoice *ToolChoice
	Tools      []*Tool
	System     []SystemContent
	// ThinkingLevel, when non-zero (i.e. not ThinkingLevelDefault), overrides
	// the per-service default thinking level for this request. Use
	// ThinkingLevelOff to explicitly disable reasoning for this turn.
	ThinkingLevel ThinkingLevel
	// OnStream is called with each streaming delta as the LLM generates content.
	// If nil, no streaming callbacks are made. The full response is still returned from Do.
	OnStream func(StreamDelta) `json:"-"`
	// OnRetry is called before sleeping for a retryable LLM request failure.
	OnRetry func(RetryEvent) `json:"-"`
}

type RetryEvent struct {
	Attempt  int
	Sleep    time.Duration
	Err      string
	Status   int
	Provider string
	Model    string
}

func FormatRetryEvent(event RetryEvent) string {
	parts := []string{}
	if event.Provider != "" {
		parts = append(parts, event.Provider)
	}
	if event.Model != "" {
		parts = append(parts, event.Model)
	}
	if event.Status != 0 {
		parts = append(parts, fmt.Sprintf("status %d", event.Status))
	}
	msg := "LLM request failed"
	if len(parts) > 0 {
		msg += ": " + strings.Join(parts, " ")
	}
	msg += fmt.Sprintf("; retrying in %s.", event.Sleep.Round(time.Second))
	if event.Err != "" {
		msg += " " + Truncate(event.Err, 160)
	}
	return msg
}

// Message represents a message in the conversation.
type Message struct {
	Role      MessageRole `json:"Role"`
	Content   []Content   `json:"Content"`
	ToolUse   *ToolUse    `json:"ToolUse,omitempty"` // use to control whether/which tool to use
	EndOfTurn bool        `json:"EndOfTurn"`         // true if this message completes the agent's turn (no tool calls to make)

	// ExcludedFromContext indicates this message should be stored but not sent back to the LLM.
	// Used for truncated responses we want to keep for cost tracking but that would confuse the LLM.
	ExcludedFromContext bool `json:"ExcludedFromContext,omitempty"`

	// ErrorType indicates this is a system-generated error message (not LLM content).
	// Empty string means not an error. Values: "truncation", "llm_request".
	ErrorType ErrorType `json:"ErrorType,omitempty"`

	// ErrorRetryable is set on error messages (ErrorType != "") to indicate
	// that re-running the LLM request with the same history is likely to
	// succeed. The UI exposes a Retry button when this is true.
	ErrorRetryable bool `json:"ErrorRetryable,omitempty"`
}

// ToolUse represents a tool use in the message content.
type ToolUse struct {
	ID   string
	Name string
}

type ToolChoice struct {
	Type ToolChoiceType
	Name string
}

type SystemContent struct {
	Text  string
	Type  string
	Cache bool
}

// Tool represents a tool available to an LLM.
type Tool struct {
	Name string
	// Type is used by the text editor tool; see
	// https://docs.anthropic.com/en/docs/build-with-claude/tool-use/text-editor-tool
	Type        string
	Description string
	InputSchema json.RawMessage
	// EndsTurn indicates that this tool should cause the model to end its turn when used
	EndsTurn bool
	// Cache indicates whether to use prompt caching for this tool
	Cache bool

	// ServerSide marks tools that are executed server-side by the LLM provider
	// (e.g., Anthropic web search). These tools are provider-specific and must
	// be filtered out when sending requests to other providers.
	ServerSide bool

	// The Run function is automatically called when the tool is used.
	// Run functions may be called concurrently with each other and themselves.
	// The input to Run function is the input to the tool, as provided by Claude, in compliance with the input schema.
	// The outputs from Run will be sent back to Claude.
	// If you do not want to respond to the tool call request from Claude, return ErrDoNotRespond.
	// ctx contains extra (rarely used) tool call information; retrieve it with ToolCallInfoFromContext.
	Run func(ctx context.Context, input json.RawMessage) ToolOut `json:"-"`
}

// RunJSON adapts a typed tool handler to Tool.Run by unmarshalling the raw
// JSON tool input before calling run.
func RunJSON[T any](run func(context.Context, T) ToolOut) func(context.Context, json.RawMessage) ToolOut {
	return func(ctx context.Context, raw json.RawMessage) ToolOut {
		var input T
		if err := json.Unmarshal(raw, &input); err != nil {
			return ErrorfToolOut("invalid tool input: %w", err)
		}
		return run(ctx, input)
	}
}

// ToolProgress represents a progress update from a running tool.
type ToolProgress struct {
	// ToolUseID is the tool_use block ID this progress belongs to.
	ToolUseID string `json:"tool_use_id"`
	// ToolName is the name of the tool generating progress.
	ToolName string `json:"tool_name"`
	// Output is the last chunk of output (tail of output, max ~10KB).
	Output string `json:"output"`
}

// ToolProgressFunc is called by tools to report progress during execution.
type ToolProgressFunc func(ToolProgress)

// ToolOut represents the output of a tool run.
type ToolOut struct {
	// LLMContent is the output of the tool to be sent back to the LLM.
	// May be nil on error.
	LLMContent []Content
	// Display is content to be displayed to the user.
	// The type of content is set by the tool and coordinated with the UIs.
	// It should be JSON-serializable.
	Display any
	// Error is the error (if any) that occurred during the tool run.
	// The text contents of the error will be sent back to the LLM.
	// If non-nil, LLMContent will be ignored.
	Error error
}

type Content struct {
	ID   string
	Type ContentType
	Text string

	// Media type for image content
	MediaType string

	// for thinking
	Thinking  string
	Data      string
	Signature string

	// for tool_use
	ToolName  string
	ToolInput json.RawMessage

	// for tool_result
	ToolUseID  string
	ToolError  bool
	ToolResult []Content

	// timing information for tool_result; added externally; not sent to the LLM
	ToolUseStartTime *time.Time
	ToolUseEndTime   *time.Time

	// Display is content to be displayed to the user, copied from ToolOut
	Display any

	// DisplayImageURL is set by the API layer when serving conversation data.
	// It replaces the base64 Data with a URL to the image endpoint.
	DisplayImageURL string

	// DisplayWidth and DisplayHeight are the intrinsic pixel dimensions of
	// an image (when MediaType is set). Populated at the time the image
	// content is created so the UI can reserve layout space without having
	// to download the bytes first. Not sent to the LLM.
	DisplayWidth  int `json:",omitempty"`
	DisplayHeight int `json:",omitempty"`

	Cache bool

	// Server-side tool fields (Anthropic web search).
	// These MUST stay omitempty: a nil json.RawMessage marshals to the JSON
	// token `null`, which on reload unmarshals back to []byte("null") (not
	// nil). Without omitempty, persisting a Content with no Caller and then
	// reloading produces a non-nil Caller holding the bytes `null`, which we
	// would send to Anthropic as `"caller": null`. The API rejects that with
	//   server_tool_use.caller: Input should be an object
	// and because the bad block is part of conversation history, every retry
	// resends the same payload and the conversation is permanently wedged.
	Caller    json.RawMessage `json:",omitempty"` // for server_tool_use blocks
	Citations json.RawMessage `json:",omitempty"` // for text blocks with citations

	// Web search result fields
	Title            string
	URL              string
	EncryptedContent string
	PageAge          string
	EncryptedIndex   string
}

func StringContent(s string) Content {
	return Content{Type: ContentTypeText, Text: s}
}

// ContentsAttr returns contents as a slog.Attr.
// It is meant for logging.
func ContentsAttr(contents []Content) slog.Attr {
	var contentAttrs []any // slog.Attr
	for _, content := range contents {
		var attrs []any // slog.Attr
		switch content.Type {
		case ContentTypeText:
			attrs = append(attrs, slog.String("text", content.Text))
		case ContentTypeToolUse:
			attrs = append(attrs, slog.String("tool_name", content.ToolName))
			attrs = append(attrs, slog.String("tool_input", string(content.ToolInput)))
		case ContentTypeToolResult:
			attrs = append(attrs, slog.Any("tool_result", content.ToolResult))
			attrs = append(attrs, slog.Bool("tool_error", content.ToolError))
		case ContentTypeThinking:
			attrs = append(attrs, slog.String("thinking", content.Thinking))
		default:
			attrs = append(attrs, slog.String("unknown_content_type", content.Type.String()))
			attrs = append(attrs, slog.Any("text", content)) // just log it all raw, better to have too much than not enough
		}
		contentAttrs = append(contentAttrs, slog.Group(content.ID, attrs...))
	}
	return slog.Group("contents", contentAttrs...)
}

type (
	MessageRole    int
	ContentType    int
	ToolChoiceType int
	StopReason     int
	ThinkingLevel  int
)

//go:generate go tool golang.org/x/tools/cmd/stringer -type=MessageRole,ContentType,ToolChoiceType,StopReason,ThinkingLevel -output=llm_string.go

const (
	MessageRoleUser MessageRole = iota
	MessageRoleAssistant

	ContentTypeText ContentType = iota
	ContentTypeThinking
	ContentTypeRedactedThinking
	ContentTypeToolUse
	ContentTypeToolResult
	ContentTypeServerToolUse
	ContentTypeWebSearchToolResult
	ContentTypeWebSearchResult // individual search result inside web_search_tool_result

	ToolChoiceTypeAuto ToolChoiceType = iota // default
	ToolChoiceTypeAny                        // any tool, but must use one
	ToolChoiceTypeNone                       // no tools allowed
	ToolChoiceTypeTool                       // must use the tool specified in the Name field

	StopReasonStopSequence StopReason = iota
	StopReasonMaxTokens
	StopReasonEndTurn
	StopReasonToolUse
	StopReasonRefusal
	StopReasonPause // server-side tool use paused mid-turn (Anthropic pause_turn)
)

// IsServerSideContentType reports whether a content type represents server-side
// tool activity (e.g., Anthropic web search). These content blocks are
// provider-specific and should be filtered out when sending to other providers.
func IsServerSideContentType(ct ContentType) bool {
	switch ct {
	case ContentTypeServerToolUse, ContentTypeWebSearchToolResult, ContentTypeWebSearchResult:
		return true
	default:
		return false
	}
}

// ThinkingLevel controls how much thinking/reasoning the model does.
// ThinkingLevelDefault is the zero value: providers fall back to their
// per-service default (usually medium). To explicitly turn thinking off, use
// ThinkingLevelOff.
const (
	ThinkingLevelDefault ThinkingLevel = iota // Use the service-level default
	ThinkingLevelOff                          // Explicitly disable thinking
	ThinkingLevelMinimal                      // Minimal thinking (~1024 tokens / "minimal")
	ThinkingLevelLow                          // Low thinking (~2048 tokens / "low")
	ThinkingLevelMedium                       // Medium thinking (~8192 tokens / "medium")
	ThinkingLevelHigh                         // High thinking (~16384 tokens / "high")
	ThinkingLevelXHigh                        // Maximum thinking (~32768 tokens / "xhigh")
)

// EffectiveThinkingLevel resolves the level to use for a request: a non-default
// request-level override wins; otherwise the service-level default applies.
func EffectiveThinkingLevel(serviceDefault, requestOverride ThinkingLevel) ThinkingLevel {
	if requestOverride != ThinkingLevelDefault {
		return requestOverride
	}
	return serviceDefault
}

// ParseThinkingLevel parses one of the user-facing level names
// ("default", "off", "minimal", "low", "medium", "high", "xhigh") into a
// ThinkingLevel. Empty string and unknown values return ThinkingLevelDefault.
func ParseThinkingLevel(s string) ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return ThinkingLevelOff
	case "minimal":
		return ThinkingLevelMinimal
	case "low":
		return ThinkingLevelLow
	case "medium":
		return ThinkingLevelMedium
	case "high":
		return ThinkingLevelHigh
	case "xhigh":
		return ThinkingLevelXHigh
	default:
		return ThinkingLevelDefault
	}
}

// Name returns the lower-case user-facing name for the thinking level
// ("off", "minimal", "low", "medium", "high", "xhigh"). Returns "" for
// ThinkingLevelDefault.
func (t ThinkingLevel) Name() string {
	switch t {
	case ThinkingLevelOff:
		return "off"
	case ThinkingLevelMinimal:
		return "minimal"
	case ThinkingLevelLow:
		return "low"
	case ThinkingLevelMedium:
		return "medium"
	case ThinkingLevelHigh:
		return "high"
	case ThinkingLevelXHigh:
		return "xhigh"
	default:
		return ""
	}
}

// ThinkingBudgetTokens returns the recommended budget_tokens for Anthropic's
// extended thinking (used by older non-adaptive Claude models). Returns 0 for
// ThinkingLevelOff/Default. ThinkingLevelXHigh is clamped to the high budget
// since budget-style APIs don't have a separate "xhigh" tier.
func (t ThinkingLevel) ThinkingBudgetTokens() int {
	switch t {
	case ThinkingLevelMinimal:
		return 1024
	case ThinkingLevelLow:
		return 2048
	case ThinkingLevelMedium:
		return 8192
	case ThinkingLevelHigh, ThinkingLevelXHigh:
		return 16384
	default:
		return 0
	}
}

// ThinkingEffort returns the reasoning effort string for OpenAI's reasoning API.
func (t ThinkingLevel) ThinkingEffort() string {
	switch t {
	case ThinkingLevelMinimal:
		return "minimal"
	case ThinkingLevelLow:
		return "low"
	case ThinkingLevelMedium:
		return "medium"
	case ThinkingLevelHigh:
		return "high"
	case ThinkingLevelXHigh:
		return "xhigh"
	default:
		return ""
	}
}

type Response struct {
	ID           string
	Type         string
	Role         MessageRole
	Model        string
	Content      []Content
	StopReason   StopReason
	StopSequence *string
	Usage        Usage
	StartTime    *time.Time
	EndTime      *time.Time
	// URL is the LLM API endpoint this response came from. Providers set it
	// so the loop can record which endpoint produced the usage data.
	URL string
}

func (m *Response) ToMessage() Message {
	return Message{
		Role:    m.Role,
		Content: m.Content,
		// End of turn unless there are client tools to call (ToolUse) or the
		// server paused mid-turn to run a server-side tool (Pause).
		EndOfTurn: m.StopReason != StopReasonToolUse && m.StopReason != StopReasonPause,
	}
}

func CostUSDFromResponse(headers http.Header) float64 {
	h := headers.Get("Exedev-Gateway-Cost")
	if h == "" {
		return 0
	}
	cost, err := strconv.ParseFloat(h, 64)
	if err != nil {
		slog.Warn("failed to parse Exedev-Gateway-Cost header", "header", h)
		return 0
	}
	return cost
}

// Usage represents the billing and rate-limit usage.
// Most LLM structs do not have JSON tags, to avoid accidental direct use in specific providers.
// However, the front-end uses this struct, and it relies on its JSON serialization.
// Do NOT use this struct directly when implementing an llm.Service.
type Usage struct {
	InputTokens              uint64  `json:"input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
	Model                    string  `json:"model,omitempty"`
	// URL is the LLM API endpoint the request was sent to (e.g.
	// https://api.anthropic.com/v1/messages). Set by the loop from the
	// response so it can be recorded alongside the model name.
	URL       string     `json:"url,omitempty"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}

func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.OutputTokens += other.OutputTokens
	u.CostUSD += other.CostUSD
}

func (u *Usage) String() string {
	return fmt.Sprintf("in: %d, out: %d", u.InputTokens, u.OutputTokens)
}

// TotalInputTokens returns the total number of input tokens including cached tokens.
// This represents the full context that was sent to the model:
// - InputTokens: tokens processed (not from cache)
// - CacheCreationInputTokens: tokens written to cache (also part of input)
// - CacheReadInputTokens: tokens read from cache (also part of input)
func (u *Usage) TotalInputTokens() uint64 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// ContextWindowUsed returns the total context window usage after this response.
// This is the size of the conversation that would be sent to the model for the next turn:
// total input tokens + output tokens (which become part of the conversation).
func (u *Usage) ContextWindowUsed() uint64 {
	return u.TotalInputTokens() + u.OutputTokens
}

func (u *Usage) IsZero() bool {
	return *u == Usage{}
}

func (u *Usage) Attr() slog.Attr {
	return slog.Group(
		"usage",
		slog.Uint64("input_tokens", u.InputTokens),
		slog.Uint64("output_tokens", u.OutputTokens),
		slog.Uint64("cache_creation_input_tokens", u.CacheCreationInputTokens),
		slog.Uint64("cache_read_input_tokens", u.CacheReadInputTokens),
		slog.Float64("cost_usd", u.CostUSD),
	)
}

// UserStringMessage creates a user message with a single text content item.
func UserStringMessage(text string) Message {
	return Message{
		Role:    MessageRoleUser,
		Content: []Content{StringContent(text)},
	}
}

// TextContent creates a simple text content for tool results.
// This is a helper function to create the most common type of tool result content.
func TextContent(text string) []Content {
	return []Content{{
		Type: ContentTypeText,
		Text: text,
	}}
}

func ErrorToolOut(err error) ToolOut {
	if err == nil {
		panic("ErrorToolOut called with nil error")
	}
	return ToolOut{
		Error: err,
	}
}

func ErrorfToolOut(format string, args ...any) ToolOut {
	return ErrorToolOut(fmt.Errorf(format, args...))
}

// DumpToFile writes LLM communication content to a timestamped file in ~/.cache/sketch/.
// For requests, it includes the URL followed by the content. For responses, it only includes the content.
// The typ parameter is used as a prefix in the filename ("request", "response").
func DumpToFile(typ, url string, content []byte) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cacheDir := filepath.Join(homeDir, ".cache", "sketch")
	err = os.MkdirAll(cacheDir, 0o700)
	if err != nil {
		return err
	}
	now := time.Now()
	filename := fmt.Sprintf("%s_%d.txt", typ, now.UnixMilli())
	filePath := filepath.Join(cacheDir, filename)

	// For requests, start with the URL; for responses, just write the content
	data := []byte(url)
	if url != "" {
		data = append(data, "\n\n"...)
	}
	data = append(data, content...)

	return os.WriteFile(filePath, data, 0o600)
}
