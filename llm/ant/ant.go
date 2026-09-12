package ant

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/models/modelsdev"
)

const (
	DefaultModel = Claude46Sonnet
	APIKeyEnv    = "ANTHROPIC_API_KEY"
	DefaultURL   = "https://api.anthropic.com/v1/messages"
)

const (
	Claude45Haiku  = "claude-haiku-4-5-20251001"
	Claude4Sonnet  = "claude-sonnet-4-20250514"
	Claude45Sonnet = "claude-sonnet-4-5-20250929"
	Claude45Opus   = "claude-opus-4-5-20251101"
	Claude46Opus   = "claude-opus-4-6"
	Claude46Sonnet = "claude-sonnet-4-6"
	Claude5Sonnet  = "claude-sonnet-5"
	Claude47Opus   = "claude-opus-4-7"
	Claude48Opus   = "claude-opus-4-8"
	Claude5Opus    = "claude-opus-5"
	ClaudeFable51  = "claude-fable-5-1"
	ClaudeFable5   = "claude-fable-5"
)

// unknownAnthropicMaxOutputTokens preserves the established Anthropic
// allowance when no catalog entry is available. Anthropic requires
// max_tokens, so unknown models cannot omit it.
const unknownAnthropicMaxOutputTokens = 64000

// IsClaudeModel reports whether userName is a user-friendly Claude model.
// It uses ClaudeModelName under the hood.
func IsClaudeModel(userName string) bool {
	return ClaudeModelName(userName) != ""
}

// ClaudeModelName returns the Anthropic Claude model name for userName.
// It returns an empty string if userName is not a recognized Claude model.
func ClaudeModelName(userName string) string {
	switch userName {
	case "claude", "sonnet":
		return Claude46Sonnet
	case "opus":
		return Claude48Opus
	default:
		return ""
	}
}

func (s *Service) Provider() string { return "anthropic" }

func (s *Service) SupportsReasoning() bool {
	caps, found := modelsdev.LookupReasoningCapabilities(s.URL, cmp.Or(s.Model, DefaultModel))
	return !found || caps.Supported
}

// SupportedReasoningLevels advertises exact effort levels from models.dev.
// Budget-token and unknown models return nil and retain the historical
// standard-level fallback.
func (s *Service) SupportedReasoningLevels() []llm.ThinkingLevel {
	caps, found := modelsdev.LookupReasoningCapabilities(s.URL, cmp.Or(s.Model, DefaultModel))
	if !found {
		return nil
	}
	return append([]llm.ThinkingLevel(nil), caps.Levels...)
}

// SupportsServerSideWebSearch reports whether this service can run the
// Anthropic server-side `web_search_20250305` tool. Only genuine Claude
// models understand it. The Anthropic Messages wire protocol is also used to
// reach non-Anthropic models (e.g. when an LLM integration advertises
// anthropic_messages for a third-party model); those reject the tool, so gate
// on the model name.
func (s *Service) SupportsServerSideWebSearch() bool {
	return strings.HasPrefix(strings.ToLower(cmp.Or(s.Model, DefaultModel)), "claude")
}

// DefaultReasoningLevel reports the reasoning level applied to requests that
// carry no per-conversation override. applyAnthropicThinking treats both
// ThinkingLevelDefault and ThinkingLevelOff as "send no thinking", so those
// surface as "off"; any other configured level surfaces by name.
func (s *Service) DefaultReasoningLevel() string {
	eff := s.ThinkingLevel
	if eff == llm.ThinkingLevelDefault || eff == llm.ThinkingLevelOff {
		return "off"
	}
	return eff.Name()
}

// SupportsImages reports whether this service accepts image inputs.
// Anthropic models support images by default; set SupportsImages=false to opt out
// (e.g. for a custom endpoint that proxies a text-only model).
func (s *Service) SupportsImages() bool { return s.SupportsImages_ }

// maxOutputTokens reads the embedded models.dev output limit. Unknown models
// retain the historical named conservative default because Anthropic requires
// max_tokens on every request.
func (s *Service) maxOutputTokens(model string) int {
	if limit, found := modelsdev.LookupAnthropicOutputLimit(s.URL, model); found {
		return limit
	}
	return unknownAnthropicMaxOutputTokens
}

// requestMaxTokens returns the max_tokens to send for model.
//
// s.MaxTokens may only lower the limit, never raise it. For custom models it
// comes from the models.max_tokens column, which used to mean context tokens
// and defaulted to 200000; those rows were not migrated. Anthropic rejects
// max_tokens above the model's output limit with a non-retryable 400, so the
// value is capped at the catalog limit, or at unknownAnthropicMaxOutputTokens
// when the model is not in the catalog (Bedrock IDs, proxy aliases,
// Anthropic-compatible endpoints). Zero means use that limit.
func (s *Service) requestMaxTokens(model string) int {
	limit := s.maxOutputTokens(model)
	return min(cmp.Or(s.MaxTokens, limit), limit)
}

// MaxImageDimension returns the maximum allowed image dimension for multi-image requests.
// Anthropic enforces a 2000 pixel limit when multiple images are in a conversation.
func (s *Service) MaxImageDimension() int {
	return 2000
}

// MaxImageBytes returns the maximum allowed encoded size in bytes for a single image.
// Anthropic enforces a 5 MB per-image limit on the API.
// See https://platform.claude.com/docs/en/build-with-claude/vision.
func (s *Service) MaxImageBytes() int {
	return 5 * 1024 * 1024
}

// Service provides Claude completions.
// Fields should not be altered concurrently with calling any method on Service.
type Service struct {
	HTTPC                 *http.Client      // defaults to http.DefaultClient if nil
	URL                   string            // defaults to DefaultURL if empty
	APIKey                string            // must be non-empty
	Model                 string            // defaults to DefaultModel if empty
	MaxTokens             int               // 0 uses the catalog limit, or the named unknown-model default
	ThinkingLevel         llm.ThinkingLevel // service-level default; ThinkingLevelDefault (zero) means "none configured"
	Backoff               []time.Duration   // retry backoff durations; defaults to {15s, 30s, 60s} if nil
	SupportsImages_       bool              // whether this service accepts image inputs
	EnableThinkingBinding bool              // send drop_block binding controls; native api.anthropic.com always does
}

var _ llm.Service = (*Service)(nil)

type content struct {
	// https://docs.anthropic.com/en/api/messages
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`

	// Subtly, an empty string appears in tool results often, so we have
	// to distinguish between empty string and no string.
	// Underlying error looks like one of:
	//   "messages.46.content.0.tool_result.content.0.text.text: Field required""
	//   "messages.1.content.1.tool_use.text: Extra inputs are not permitted"
	//
	// I haven't found a super great source for the API, but
	// https://github.com/anthropics/anthropic-sdk-typescript/blob/main/src/resources/messages/messages.ts
	// is somewhat acceptable but hard to read.
	Text      *string         `json:"text,omitempty"`
	MediaType string          `json:"media_type,omitempty"` // for image
	Source    json.RawMessage `json:"source,omitempty"`     // for image

	// for thinking
	Thinking  *string `json:"thinking,omitempty"`
	Data      string  `json:"data,omitempty"`      // for redacted_thinking or image
	Signature string  `json:"signature,omitempty"` // for thinking

	// for tool_use
	ToolName  string          `json:"name,omitempty"`
	ToolInput json.RawMessage `json:"input,omitempty"`

	// for tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	ToolError bool   `json:"is_error,omitempty"`
	// note the recursive nature here; message looks like:
	// {
	//  "role": "user",
	//  "content": [
	//    {
	//      "type": "tool_result",
	//      "tool_use_id": "toolu_01A09q90qw90lq917835lq9",
	//      "content": [
	//        {"type": "text", "text": "15 degrees"},
	//        {
	//          "type": "image",
	//          "source": {
	//            "type": "base64",
	//            "media_type": "image/jpeg",
	//            "data": "/9j/4AAQSkZJRg...",
	//          }
	//        }
	//      ]
	//    }
	//  ]
	//}
	ToolResult []content `json:"content,omitempty"`

	// timing information for tool_result; not sent to Claude
	StartTime *time.Time `json:"-"`
	EndTime   *time.Time `json:"-"`

	CacheControl json.RawMessage `json:"cache_control,omitempty"`

	// Server-side tool fields (Anthropic web search)
	Caller    json.RawMessage `json:"caller,omitempty"`
	Citations json.RawMessage `json:"citations,omitempty"`

	// Web search result fields
	Title            string `json:"title,omitempty"`
	URL              string `json:"url,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	PageAge          string `json:"page_age,omitempty"`
	EncryptedIndex   string `json:"encrypted_index,omitempty"`
}

// message represents a message in the conversation.
type message struct {
	Role    string    `json:"role"`
	Content []content `json:"content"`
	ToolUse *toolUse  `json:"tool_use,omitempty"` // use to control whether/which tool to use
}

// toolUse represents a tool use in the message content.
type toolUse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// tool represents a tool available to Claude.
type tool struct {
	Name string `json:"name"`
	// Type is used by the text editor tool; see
	// https://docs.anthropic.com/en/docs/build-with-claude/tool-use/text-editor-tool
	Type         string          `json:"type,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// usage represents the billing and rate-limit usage.
type usage struct {
	InputTokens              uint64  `json:"input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

func (u *usage) Add(other usage) {
	u.InputTokens += other.InputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.OutputTokens += other.OutputTokens
	u.CostUSD += other.CostUSD
}

// response represents the response from the message API.
type response struct {
	ID                   string                `json:"id"`
	Type                 string                `json:"type"`
	Role                 string                `json:"role"`
	Model                string                `json:"model"`
	Content              []content             `json:"content"`
	StopReason           string                `json:"stop_reason"`
	StopSequence         *string               `json:"stop_sequence,omitempty"`
	StopDetails          *stopDetails          `json:"stop_details,omitempty"`
	InputTransformations []inputTransformation `json:"input_transformations,omitempty"`
	Usage                usage                 `json:"usage"`
}

type inputTransformation struct {
	Type   string `json:"type"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// stopDetails is Anthropic's structured stop explanation. On a refusal it
// carries a coarse category (e.g. "cyber") and a human-readable explanation.
type stopDetails struct {
	Type        string `json:"type"`
	Category    string `json:"category,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// https://docs.anthropic.com/en/api/messages#body-system
type systemContent struct {
	Text         string          `json:"text,omitempty"`
	Type         string          `json:"type,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// adaptiveThinkingMinVersion maps Claude family names to the first version
// (major, minor) that requires adaptive thinking. Models at or above the
// threshold use thinking.type=adaptive + output_config.effort; older ones use
// the legacy thinking.type=enabled + budget_tokens. Version thresholds (rather
// than an exact-name allowlist) make new releases (opus-6, sonnet-5.5, ...)
// work without a code change.
var adaptiveThinkingMinVersion = map[string][2]int{
	"opus":   {4, 7},
	"sonnet": {5, 0},
	"haiku":  {5, 0}, // no adaptive haiku yet; assume the next one is
	"fable":  {0, 0}, // every Fable release is adaptive
}

// useAdaptiveThinking reports whether the model requires adaptive thinking
// (thinking: {type: "adaptive"} + output_config: {effort: "..."}) instead of
// the legacy manual thinking (thinking: {type: "enabled", budget_tokens: N}).
func useAdaptiveThinking(model string) bool {
	family, major, minor, ok := parseClaudeModel(model)
	if !ok {
		return false
	}
	minVer, ok := adaptiveThinkingMinVersion[family]
	if !ok {
		return false
	}
	return major > minVer[0] || (major == minVer[0] && minor >= minVer[1])
}

func (s *Service) messageOrigin(model string) llm.MessageOrigin {
	return llm.MessageOrigin{
		Provider:  "anthropic",
		Transport: "anthropic-messages:" + transportIdentity(cmp.Or(s.URL, DefaultURL)),
		Model:     model,
	}
}

func transportIdentity(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String()
}

func (s *Service) supportsThinkingBinding() bool {
	if s.EnableThinkingBinding {
		return true
	}
	u, err := url.Parse(cmp.Or(s.URL, DefaultURL))
	return err == nil && u.Scheme == "https" &&
		strings.EqualFold(u.Hostname(), "api.anthropic.com") &&
		strings.TrimSuffix(u.Path, "/") == "/v1/messages"
}

// parseClaudeModel extracts the family ("opus", "sonnet", "haiku", "fable")
// and version from a Claude model identifier. It handles bare names
// ("claude-opus-5"), dotted versions ("claude-opus-4.8"), dated snapshots
// ("claude-opus-4-8-20260115"), and provider-qualified names
// ("us.anthropic.claude-opus-4-8-v1:0", "claude-opus-5@20260724"). Returns
// ok=false for strings that don't follow the claude-<family>-<version> shape,
// such as the legacy version-first names ("claude-3-opus-20240229"), which
// all predate adaptive thinking anyway.
func parseClaudeModel(model string) (family string, major, minor int, ok bool) {
	tokens := strings.FieldsFunc(strings.ToLower(model), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	seenClaude := false
	for i, tok := range tokens {
		if tok == "claude" {
			seenClaude = true
			continue
		}
		if !seenClaude {
			continue
		}
		if tok != "opus" && tok != "sonnet" && tok != "haiku" && tok != "fable" {
			continue
		}
		// Parse up to two numeric tokens after the family as major/minor.
		// Date snapshots (20260115) are large; version components are small.
		var nums []int
		for _, t := range tokens[i+1:] {
			n, err := strconv.Atoi(t)
			if err != nil || n >= 1000 {
				break
			}
			nums = append(nums, n)
			if len(nums) == 2 {
				break
			}
		}
		if len(nums) == 0 {
			return "", 0, 0, false
		}
		if len(nums) > 1 {
			minor = nums[1]
		}
		return tok, nums[0], minor, true
	}
	return "", 0, 0, false
}

// request represents the request payload for creating a message.
// thinking configures extended thinking for Claude models.
type thinking struct {
	BlockBinding *thinkingBlockBinding `json:"block_binding,omitempty"`
	Type         string                `json:"type"`                    // "enabled" or "adaptive"
	BudgetTokens int                   `json:"budget_tokens,omitempty"` // Max tokens for thinking (legacy, not used with adaptive)
	Display      string                `json:"display,omitempty"`       // "summarized": return thinking text. Opus 4.7+ defaults to "omitted" (empty thinking blocks).
}

// thinkingBindingBeta enables input transformation reports and binding controls.
const thinkingBindingBeta = "thinking-binding-controls-2026-08-01"

type thinkingBlockBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
}

// outputConfig controls output behavior (effort level for adaptive thinking).
type outputConfig struct {
	Effort string `json:"effort,omitempty"` // "minimal", "low", "medium", "high"
}

type request struct {
	// Field order matters for JSON serialization - stable fields should come first
	// to maximize prefix deduplication when storing LLM requests.
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	Stream        bool            `json:"stream,omitempty"`
	System        []systemContent `json:"system,omitempty"`
	Tools         []*tool         `json:"tools,omitempty"`
	ToolChoice    *toolChoice     `json:"tool_choice,omitempty"`
	Thinking      *thinking       `json:"thinking,omitempty"`
	OutputConfig  *outputConfig   `json:"output_config,omitempty"`
	Temperature   float64         `json:"temperature,omitempty"`
	TopK          int             `json:"top_k,omitempty"`
	TopP          float64         `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	// Messages comes last since it grows with each request in a conversation
	Messages []message `json:"messages"`
}

func mapped[Slice ~[]E, E, T any](s Slice, f func(E) T) []T {
	out := make([]T, len(s))
	for i, v := range s {
		out[i] = f(v)
	}
	return out
}

func inverted[K, V cmp.Ordered](m map[K]V) map[V]K {
	inv := make(map[V]K)
	for k, v := range m {
		if _, ok := inv[v]; ok {
			panic(fmt.Errorf("inverted map has multiple keys for value %v", v))
		}
		inv[v] = k
	}
	return inv
}

var (
	fromLLMRole = map[llm.MessageRole]string{
		llm.MessageRoleAssistant: "assistant",
		llm.MessageRoleUser:      "user",
	}
	toLLMRole = inverted(fromLLMRole)

	fromLLMContentType = map[llm.ContentType]string{
		llm.ContentTypeText:                "text",
		llm.ContentTypeThinking:            "thinking",
		llm.ContentTypeRedactedThinking:    "redacted_thinking",
		llm.ContentTypeToolUse:             "tool_use",
		llm.ContentTypeToolResult:          "tool_result",
		llm.ContentTypeServerToolUse:       "server_tool_use",
		llm.ContentTypeWebSearchToolResult: "web_search_tool_result",
		llm.ContentTypeWebSearchResult:     "web_search_result",
	}
	toLLMContentType = inverted(fromLLMContentType)

	fromLLMToolChoiceType = map[llm.ToolChoiceType]string{
		llm.ToolChoiceTypeAuto: "auto",
		llm.ToolChoiceTypeAny:  "any",
		llm.ToolChoiceTypeNone: "none",
		llm.ToolChoiceTypeTool: "tool",
	}

	toLLMStopReason = map[string]llm.StopReason{
		"stop_sequence": llm.StopReasonStopSequence,
		"max_tokens":    llm.StopReasonMaxTokens,
		"end_turn":      llm.StopReasonEndTurn,
		"tool_use":      llm.StopReasonToolUse,
		"refusal":       llm.StopReasonRefusal,
		"pause_turn":    llm.StopReasonPause, // server-side tool execution, model will continue
		// Claude 4.5+ accepts input + max_tokens > context window and stops
		// with this reason instead of a 400 when generation hits the window.
		// Treat it as truncation; unmapped it would fall to the zero value
		// StopReasonStopSequence and look like a normal completion.
		"model_context_window_exceeded": llm.StopReasonMaxTokens,
	}
)

func fromLLMCache(c bool) json.RawMessage {
	if !c {
		return nil
	}
	return json.RawMessage(`{"type":"ephemeral"}`)
}

// nonNullRawMessage returns nil if m is empty or the JSON literal `null`.
// json.RawMessage fields persisted through encoding/json may come back as
// the 4-byte slice []byte("null") rather than nil. Anthropic's API rejects
// fields like `caller: null` on server_tool_use blocks, which wedges the
// conversation forever.
func nonNullRawMessage(m json.RawMessage) json.RawMessage {
	if len(m) == 0 || string(m) == "null" {
		return nil
	}
	return m
}

func fromLLMContent(c llm.Content) content {
	var toolResult []content
	if len(c.ToolResult) > 0 {
		toolResult = make([]content, len(c.ToolResult))
		for i, tr := range c.ToolResult {
			// For image content inside a tool_result, we need to map it to "image" type
			if tr.MediaType != "" && tr.MediaType == "image/jpeg" || tr.MediaType == "image/png" {
				// Format as an image for Claude
				toolResult[i] = content{
					Type: "image",
					Source: json.RawMessage(fmt.Sprintf(`{"type":"base64","media_type":"%s","data":"%s"}`,
						tr.MediaType, tr.Data)),
				}
			} else {
				toolResult[i] = fromLLMContent(tr)
			}
		}
	}

	d := content{
		Type:         fromLLMContentType[c.Type],
		CacheControl: fromLLMCache(c.Cache),
	}

	// Set fields based on content type to avoid sending invalid fields
	switch c.Type {
	case llm.ContentTypeText:
		// Images are represented as text with MediaType and Data
		if c.MediaType != "" {
			d.Type = "image"
			d.Source = json.RawMessage(fmt.Sprintf(`{"type":"base64","media_type":"%s","data":"%s"}`,
				c.MediaType, c.Data))
		} else {
			d.Text = &c.Text
		}
	case llm.ContentTypeThinking:
		d.Thinking = &c.Thinking
		d.Signature = c.Signature
	case llm.ContentTypeRedactedThinking:
		d.Data = c.Data
		d.Signature = c.Signature
	case llm.ContentTypeToolUse:
		d.ID = c.ID
		d.ToolName = c.ToolName
		d.ToolInput = c.ToolInput
		// Handle both nil and JSON "null" (which unmarshals as []byte("null"))
		if d.ToolInput == nil || string(d.ToolInput) == "null" {
			d.ToolInput = json.RawMessage("{}")
		}
	case llm.ContentTypeToolResult:
		d.ToolUseID = c.ToolUseID
		d.ToolError = c.ToolError
		d.ToolResult = toolResult
	case llm.ContentTypeServerToolUse:
		d.ID = c.ID
		d.ToolName = c.ToolName
		d.ToolInput = c.ToolInput
		d.Caller = nonNullRawMessage(c.Caller)
	case llm.ContentTypeWebSearchToolResult:
		d.ToolUseID = c.ToolUseID
		d.ToolResult = toolResult
	case llm.ContentTypeWebSearchResult:
		d.Title = c.Title
		d.URL = c.URL
		d.EncryptedContent = c.EncryptedContent
		d.PageAge = c.PageAge
		d.EncryptedIndex = c.EncryptedIndex
	}

	// Citations live on text blocks (per Anthropic's wire format).
	if c.Type == llm.ContentTypeText {
		if cit := nonNullRawMessage(c.Citations); len(cit) > 0 {
			d.Citations = cit
		}
	}

	return d
}

func fromLLMToolUse(tu *llm.ToolUse) *toolUse {
	if tu == nil {
		return nil
	}
	return &toolUse{
		ID:   tu.ID,
		Name: tu.Name,
	}
}

// stripThinkingBlocks returns a copy of the message with thinking and
// redacted_thinking content blocks removed. Used to strip stale thinking
// from older assistant turns before sending to the API.
func stripThinkingBlocks(msg llm.Message) llm.Message {
	var filtered []llm.Content
	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeThinking || c.Type == llm.ContentTypeRedactedThinking {
			continue
		}
		filtered = append(filtered, c)
	}
	msg.Content = filtered
	return msg
}

// filterThinkingForOrigin strips thinking from a known different origin.
func filterThinkingForOrigin(msg llm.Message, origin llm.MessageOrigin) llm.Message {
	if msg.Role != llm.MessageRoleAssistant {
		return msg
	}
	if msg.Origin.Known() && !msg.Origin.Matches(origin) {
		return stripThinkingBlocks(msg)
	}
	return msg
}

// sanitizeServerToolBlocks removes orphaned server-side tool blocks that would
// cause Anthropic to reject the request with errors like:
//
//	`web_search` tool use with id `srvtoolu_...` was found without a
//	corresponding `web_search_tool_result` block
//
// Anthropic requires a server_tool_use block and its web_search_tool_result to
// live in the SAME message. This pairing can be broken when a response stops
// with `pause_turn` while it also contains a client-side tool_use: the loop runs
// the client tool and injects a user tool_result message, after which the model
// emits the web_search_tool_result in a *later* assistant message — permanently
// splitting the server_tool_use from its result. Such a history is accepted by
// the prompt cache for a while, then rejected once the cache expires, wedging
// the conversation forever.
//
// Rules (per Anthropic's wire validation, verified empirically):
//   - A server_tool_use is valid only if a web_search_tool_result with the same
//     tool_use_id appears in the same message.
//   - Exception: an orphan server_tool_use in the final message is allowed — it
//     is the pause_turn continuation point, where the server will produce the
//     result on the next turn.
//   - A web_search_tool_result with no matching server_tool_use in the same
//     message is always dropped.
//   - web_search_result blocks only appear nested inside a web_search_tool_result,
//     so they are never touched at the top level here.
//
// It returns a copy of msgs with offending blocks removed; messages whose
// content becomes empty are dropped by the caller (fromLLMRequest skips empty
// messages).
func sanitizeServerToolBlocks(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, msg := range msgs {
		isLast := i == len(msgs)-1

		// Collect ids fulfilled by a web_search_tool_result in THIS message.
		resultIDs := make(map[string]bool)
		serverUseIDs := make(map[string]bool)
		hasServerBlocks := false
		for _, c := range msg.Content {
			switch c.Type {
			case llm.ContentTypeServerToolUse:
				hasServerBlocks = true
				serverUseIDs[c.ID] = true
			case llm.ContentTypeWebSearchToolResult:
				hasServerBlocks = true
				resultIDs[c.ToolUseID] = true
			}
		}

		// Fast path: no server-side blocks, nothing to sanitize.
		if !hasServerBlocks {
			out[i] = msg
			continue
		}

		filtered := make([]llm.Content, 0, len(msg.Content))
		for _, c := range msg.Content {
			switch c.Type {
			case llm.ContentTypeServerToolUse:
				// Keep if paired in this message, or if it's an orphan in the
				// final message (a valid pause_turn continuation point).
				if resultIDs[c.ID] || isLast {
					filtered = append(filtered, c)
				}
			case llm.ContentTypeWebSearchToolResult:
				// Keep only if a matching server_tool_use exists in this message.
				if serverUseIDs[c.ToolUseID] {
					filtered = append(filtered, c)
				}
			default:
				filtered = append(filtered, c)
			}
		}
		msg.Content = filtered
		out[i] = msg
	}
	return out
}

func fromLLMMessage(msg llm.Message) message {
	var contents []content
	for _, c := range msg.Content {
		// Skip thinking blocks with no signature — they're corrupt/incomplete
		// and the API rejects them.
		if c.Type == llm.ContentTypeThinking && c.Signature == "" {
			continue
		}
		// Skip empty text blocks. Anthropic rejects requests whose history
		// contains a text block with empty text ("text content blocks must be
		// non-empty"), which permanently wedges the conversation on every
		// subsequent turn. Such blocks can arise when a streamed response opens
		// a text content block that never receives any text_delta (e.g. an
		// empty text block emitted alongside web-search citations). Note a text
		// block carrying image data (MediaType set) is not "empty".
		if c.Type == llm.ContentTypeText && c.Text == "" && c.MediaType == "" {
			continue
		}
		contents = append(contents, fromLLMContent(c))
	}
	return message{
		Role:    fromLLMRole[msg.Role],
		Content: contents,
		ToolUse: fromLLMToolUse(msg.ToolUse),
	}
}

func fromLLMToolChoice(tc *llm.ToolChoice) *toolChoice {
	if tc == nil {
		return nil
	}
	return &toolChoice{
		Type: fromLLMToolChoiceType[tc.Type],
		Name: tc.Name,
	}
}

func fromLLMTool(t *llm.Tool) *tool {
	return &tool{
		Name:         t.Name,
		Type:         t.Type,
		Description:  t.Description,
		InputSchema:  t.InputSchema,
		CacheControl: fromLLMCache(t.Cache),
	}
}

func fromLLMSystem(s llm.SystemContent) systemContent {
	return systemContent{
		Text: s.Text,
		// Anthropic requires a type on every system block and rejects the
		// request with 400 "system.0.type: Field required" when it is absent.
		// "text" is the only value the API accepts here, so filling it in for
		// callers that left it blank cannot change the meaning of a request
		// that would otherwise have succeeded.
		Type:         cmp.Or(s.Type, "text"),
		CacheControl: fromLLMCache(s.Cache),
	}
}

func (s *Service) fromLLMRequest(r *llm.Request) *request {
	return s.buildRequest(r, false)
}

func (s *Service) buildRequest(r *llm.Request, stripThinking bool) *request {
	model := cmp.Or(s.Model, DefaultModel)
	maxTokens := s.requestMaxTokens(model)

	// Drop orphaned server-side tool blocks (e.g. a web_search server_tool_use
	// whose web_search_tool_result ended up in a different message). Anthropic
	// rejects such histories. See sanitizeServerToolBlocks.
	srcMessages := sanitizeServerToolBlocks(r.Messages)

	var messages []message
	origin := s.messageOrigin(model)
	for _, m := range srcMessages {
		m = filterThinkingForOrigin(m, origin)
		if stripThinking && m.Role == llm.MessageRoleAssistant {
			m = stripThinkingBlocks(m)
		}
		msg := fromLLMMessage(m)
		if len(msg.Content) > 0 {
			messages = append(messages, msg)
		}
	}
	req := &request{
		Model:      model,
		Messages:   messages,
		MaxTokens:  maxTokens,
		ToolChoice: fromLLMToolChoice(r.ToolChoice),
		Tools:      mapped(r.Tools, fromLLMTool),
		System:     mapped(r.System, fromLLMSystem),
	}

	applyAnthropicThinking(req, model, llm.EffectiveThinkingLevel(s.ThinkingLevel, r.ThinkingLevel), maxTokens, s.supportsThinkingBinding())
	return req
}

const minAnthropicThinkingBudget = 1024

// applyAnthropicThinking sets the Thinking / OutputConfig fields without
// increasing the configured output allowance. Budget-style thinking shares
// max_tokens with the final answer, so leave 1024 tokens for that answer.
func applyAnthropicThinking(req *request, model string, level llm.ThinkingLevel, maxTokens int, supportsBinding bool) {
	adaptive := useAdaptiveThinking(model)
	if adaptive {
		caps, found := modelsdev.LookupReasoningCapabilities("", model)
		if found && len(caps.Levels) > 0 {
			level = llm.ClampThinkingLevel(level, caps.Levels)
		} else if level == llm.ThinkingLevelMinimal {
			// Historical fallback for adaptive models without exact metadata.
			level = llm.ThinkingLevelLow
		}
	}
	if level == llm.ThinkingLevelOff || level == llm.ThinkingLevelDefault {
		return
	}
	if adaptive {
		effort := level.ThinkingEffort()
		// Adaptive-thinking models default thinking.display to "omitted", which
		// returns thinking blocks with an empty thinking field. Request summarized
		// thinking so the UI can show the model's reasoning.
		req.Thinking = &thinking{Type: "adaptive", Display: "summarized"}
		req.OutputConfig = &outputConfig{Effort: effort}
	} else {
		budget := level.ThinkingBudgetTokens()
		if budget == 0 {
			return
		}
		budget = min(budget, maxTokens-minAnthropicThinkingBudget)
		if budget < minAnthropicThinkingBudget {
			return
		}
		req.Thinking = &thinking{Type: "enabled", BudgetTokens: budget}
	}
	if supportsBinding {
		req.Thinking.BlockBinding = &thinkingBlockBinding{PrefixMismatchBehavior: "drop_block"}
	}
}

func toLLMUsage(u usage) llm.Usage {
	return llm.Usage{
		InputTokens:              u.InputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		OutputTokens:             u.OutputTokens,
		CostUSD:                  u.CostUSD,
	}
}

func toLLMContent(c content) llm.Content {
	// Convert toolResult from []content to []llm.Content
	var toolResultContents []llm.Content
	if len(c.ToolResult) > 0 {
		toolResultContents = make([]llm.Content, len(c.ToolResult))
		for i, tr := range c.ToolResult {
			toolResultContents[i] = toLLMContent(tr)
		}
	}

	ret := llm.Content{
		ID:               c.ID,
		Type:             toLLMContentType[c.Type],
		MediaType:        c.MediaType,
		Data:             c.Data,
		Signature:        c.Signature,
		ToolName:         c.ToolName,
		ToolInput:        c.ToolInput,
		ToolUseID:        c.ToolUseID,
		ToolError:        c.ToolError,
		ToolResult:       toolResultContents,
		Caller:           c.Caller,
		Citations:        c.Citations,
		Title:            c.Title,
		URL:              c.URL,
		EncryptedContent: c.EncryptedContent,
		PageAge:          c.PageAge,
		EncryptedIndex:   c.EncryptedIndex,
	}
	if c.Text != nil {
		ret.Text = *c.Text
	}
	if c.Thinking != nil {
		ret.Thinking = *c.Thinking
	}
	return ret
}

func toLLMResponse(r *response) *llm.Response {
	resp := &llm.Response{
		ID:           r.ID,
		Type:         r.Type,
		Role:         toLLMRole[r.Role],
		Model:        r.Model,
		Content:      mapped(r.Content, toLLMContent),
		StopReason:   toLLMStopReason[r.StopReason],
		StopSequence: r.StopSequence,
		Usage:        toLLMUsage(r.Usage),
	}
	if r.StopDetails != nil && r.StopDetails.Type == "refusal" {
		resp.RefusalDetails = &llm.RefusalDetails{
			Category:    r.StopDetails.Category,
			Explanation: r.StopDetails.Explanation,
		}
	}
	return resp
}

// streamEvent represents a single SSE event from the Anthropic streaming API.
type streamEvent struct {
	Type                 string                `json:"type"`
	InputTransformations []inputTransformation `json:"input_transformations,omitempty"`

	// message_start
	Message *response `json:"message,omitempty"`

	// content_block_start
	Index        int      `json:"index,omitempty"`
	ContentBlock *content `json:"content_block,omitempty"`

	// content_block_delta
	Delta json.RawMessage `json:"delta,omitempty"`

	// message_delta
	Usage *usage `json:"usage,omitempty"`
}

// streamDelta represents the delta field in content_block_delta and message_delta events.
type streamDelta struct {
	Type string `json:"type"`

	// text_delta
	Text string `json:"text,omitempty"`

	// thinking_delta
	Thinking string `json:"thinking,omitempty"`

	// input_json_delta
	PartialJSON string `json:"partial_json,omitempty"`

	// signature_delta
	Signature string `json:"signature,omitempty"`

	// message_delta
	StopReason   string       `json:"stop_reason,omitempty"`
	StopSequence *string      `json:"stop_sequence,omitempty"`
	StopDetails  *stopDetails `json:"stop_details,omitempty"`
}

// sseEvent represents a parsed Server-Sent Event per the SSE spec.
// See https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation
type sseEvent struct {
	EventType string // from "event:" field; empty if not set
	Data      string // from "data:" field(s); multiple data lines joined with "\n"
}

// iterSSEEvents reads an SSE stream and yields parsed events.
// It follows the SSE spec: events are delimited by blank lines,
// multiple "data:" lines are joined with "\n", and the "event:" field
// sets the event type.
func iterSSEEvents(r io.Reader, yield func(sseEvent) error) error {
	scanner := bufio.NewScanner(r)
	// SSE lines can be large (e.g. tool input JSON).
	// Max buffer: 10MB to handle very large content blocks.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		eventType string
		dataLines []string
		hasData   bool
	)

	dispatch := func() error {
		if !hasData {
			// Reset and skip — no data fields means no event to dispatch
			eventType = ""
			return nil
		}
		ev := sseEvent{
			EventType: eventType,
			Data:      strings.Join(dataLines, "\n"),
		}
		// Reset state
		eventType = ""
		dataLines = dataLines[:0]
		hasData = false
		return yield(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Blank line dispatches the event
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}

		// Lines starting with ':' are comments
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Split into field name and value
		var field, value string
		if idx := strings.IndexByte(line, ':'); idx >= 0 {
			field = line[:idx]
			value = line[idx+1:]
			// SSE spec: if value starts with a space, remove it
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		} else {
			field = line
		}

		switch field {
		case "event":
			eventType = value
		case "data":
			dataLines = append(dataLines, value)
			hasData = true
			// "id" and "retry" fields are ignored for our use case
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading SSE stream: %w", err)
	}

	// Dispatch any trailing event (stream ended without final blank line)
	return dispatch()
}

// truncateForError returns a string representation of data suitable for error messages,
// truncating to a reasonable length.
func truncateForError(data string, maxLen int) string {
	if len(data) <= maxLen {
		return data
	}
	return data[:maxLen] + fmt.Sprintf("... (%d bytes total)", len(data))
}

func invalidThinkingSignature(message string) bool {
	normalized := strings.ToLower(message)
	return strings.Contains(normalized, "invalid") &&
		strings.Contains(normalized, "signature") &&
		strings.Contains(normalized, "thinking")
}

// parseSSEStream reads an SSE stream and assembles the complete response.
// If onStream is non-nil, it is called with each text/thinking delta as it arrives.
func parseSSEStream(r io.Reader, onStream func(llm.StreamDelta)) (*response, error) {
	var (
		resp            *response
		contents        []content // indexed by content block index
		messageDone     bool
		transformations []inputTransformation
	)

	err := iterSSEEvents(r, func(sse sseEvent) error {
		data := sse.Data
		if data == "[DONE]" {
			return nil
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("parsing SSE event (event=%q, data=%s): %w",
				sse.EventType, truncateForError(data, 512), err)
		}

		switch event.Type {
		case "message_start":
			if event.Message == nil {
				return fmt.Errorf("message_start event has no message")
			}
			resp = event.Message
			resp.Content = nil // will be rebuilt from content blocks

		case "content_block_start":
			if event.ContentBlock == nil {
				return fmt.Errorf("content_block_start event has no content_block")
			}
			// Grow slice to accommodate index
			for len(contents) <= event.Index {
				contents = append(contents, content{})
			}
			block := *event.ContentBlock
			// For tool_use and server_tool_use blocks, the initial input is
			// always empty {}; clear it so delta accumulation starts fresh.
			if block.Type == "tool_use" || block.Type == "server_tool_use" {
				block.ToolInput = nil
			}
			contents[event.Index] = block

		case "content_block_delta":
			if event.Index >= len(contents) {
				return fmt.Errorf("content_block_delta index %d out of range", event.Index)
			}
			var delta streamDelta
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				return fmt.Errorf("parsing content_block_delta: %w", err)
			}
			c := &contents[event.Index]
			switch delta.Type {
			case "text_delta":
				if c.Text == nil {
					c.Text = new(string)
				}
				*c.Text += delta.Text
				if onStream != nil {
					onStream(llm.StreamDelta{Type: "text", Text: delta.Text, Index: event.Index})
				}
			case "thinking_delta":
				if c.Thinking == nil {
					c.Thinking = new(string)
				}
				*c.Thinking += delta.Thinking
				if onStream != nil {
					onStream(llm.StreamDelta{Type: "thinking", Text: delta.Thinking, Index: event.Index})
				}
			case "input_json_delta":
				// Accumulate raw JSON for tool_use input
				c.ToolInput = append(c.ToolInput, []byte(delta.PartialJSON)...)
			case "signature_delta":
				c.Signature += delta.Signature
			}

		case "content_block_stop":
			// nothing to do; the block is already assembled

		case "message_delta":
			var delta streamDelta
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				return fmt.Errorf("parsing message_delta: %w", err)
			}
			if resp != nil {
				resp.StopReason = delta.StopReason
				resp.StopSequence = delta.StopSequence
				if delta.StopDetails != nil {
					resp.StopDetails = delta.StopDetails
				}
			}
			if event.Usage != nil && resp != nil {
				// message_delta usage contains output_tokens
				resp.Usage.OutputTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			messageDone = true

		case "ping":
			// keepalive, ignore

		case "error":
			return fmt.Errorf("stream error event: %s", data)
		}
		if event.Type == "message_start" || event.Type == "message_delta" {
			entries := event.InputTransformations
			if event.Type == "message_start" && event.Message != nil {
				entries = event.Message.InputTransformations
			}
			for _, entry := range entries {
				if !slices.Contains(transformations, entry) {
					transformations = append(transformations, entry)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return nil, fmt.Errorf("no message_start event in stream")
	}

	if !messageDone {
		return nil, fmt.Errorf("incomplete stream: no stop_reason received (stream may have been truncated)")
	}

	// Ensure tool_use blocks always have a non-nil ToolInput.
	// When a tool has empty input {}, the stream sends input_json_delta
	// with partial_json:"", and append(nil, []byte("")...) stays nil.
	// Anthropic requires the "input" field on tool_use blocks, and
	// json:"input,omitempty" omits nil, causing a 400 error.
	for i := range contents {
		if contents[i].Type == "tool_use" && contents[i].ToolInput == nil {
			contents[i].ToolInput = json.RawMessage("{}")
		}
	}

	// Drop empty text blocks. A stream can open a text content block (via
	// content_block_start) that never receives any text_delta, leaving Text
	// nil/"". Persisting such a block poisons the conversation history: on the
	// next turn the whole history is replayed and Anthropic rejects it with
	// 400 "text content blocks must be non-empty", wedging the conversation.
	// Filter here so the bad block is never persisted; fromLLMMessage also
	// filters at send time to heal already-persisted histories.
	filtered := contents[:0]
	for _, c := range contents {
		if c.Type == "text" && (c.Text == nil || *c.Text == "") && len(c.Citations) == 0 {
			continue
		}
		filtered = append(filtered, c)
	}
	contents = filtered

	resp.InputTransformations = transformations
	resp.Content = contents
	return resp, nil
}

// Do sends a streaming request to Anthropic and collects the full response.
func (s *Service) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	var err error
	ir, err = llm.PrepareRequestCitations(ctx, ir, "anthropic", adaptCitation)
	if err != nil {
		return nil, err
	}
	startTime := time.Now()
	request := s.fromLLMRequest(ir)
	request.Stream = true
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')

	// strippedPayload is built lazily on the first invalid thinking signature.
	var strippedPayload []byte

	backoff := s.Backoff
	if backoff == nil {
		// Long tail: many model providers have multi-hour incidents, and it is
		// a much worse UX to return after a few minutes than to keep waiting.
		backoff = []time.Duration{
			15 * time.Second,
			30 * time.Second,
			60 * time.Second,
			2 * time.Minute,
			5 * time.Minute,
			10 * time.Minute,
			20 * time.Minute,
			30 * time.Minute,
		}
	}

	url := cmp.Or(s.URL, DefaultURL)
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)

	// retry loop
	retryStart := time.Now()
	var errs error               // accumulated errors across all attempts
	var lastErrSummary string    // short description of the most recent attempt failure
	var retryAfter time.Duration // hint from upstream Retry-After header, reset each attempt
	for attempts := 0; ; attempts++ {
		if attempts > 15 {
			return nil, fmt.Errorf("anthropic request failed after %d attempts: %w", attempts, errs)
		}
		if attempts > 0 {
			// Bail out early if context is already done — no point sleeping
			// and retrying when every attempt will fail immediately.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("anthropic request failed after %d attempts (context cancelled): %w", attempts, errs)
			}
			base := backoff[min(attempts-1, len(backoff)-1)]
			jitter := time.Duration(rand.Int64N(max(min(int64(base), int64(time.Second)), 1)))
			sleep := base + jitter
			if retryAfter > sleep {
				sleep = retryAfter
			}
			retryAfter = 0
			slog.WarnContext(ctx, "anthropic request sleep before retry", "sleep", sleep, "attempts", attempts, "elapsed", time.Since(retryStart).Round(time.Second), "last_error", lastErrSummary)
			if ir.OnRetry != nil {
				ir.OnRetry(llm.RetryEvent{Attempt: attempts + 1, Sleep: sleep, Err: lastErrSummary, Provider: "anthropic", Model: cmp.Or(s.Model, DefaultModel)})
			}
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return nil, fmt.Errorf("anthropic request failed after %d attempts (context cancelled during backoff): %w", attempts, errs)
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
		if err != nil {
			return nil, errors.Join(errs, err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", s.APIKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		if request.Thinking != nil && request.Thinking.BlockBinding != nil {
			req.Header.Set("Anthropic-Beta", thinkingBindingBeta)
		}

		resp, err := httpc.Do(req)
		if err != nil {
			// Don't retry httprr cache misses
			if strings.Contains(err.Error(), "cached HTTP response not found") {
				return nil, err
			}
			lastErrSummary = "transport: " + llm.Truncate(err.Error(), 160)
			errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: %w", attempts+1, time.Now().Format(time.DateTime), err))
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			response, err := parseSSEStream(resp.Body, ir.OnStream)
			resp.Body.Close()
			if err != nil {
				// Stream parse errors might be transient (connection reset, etc.)
				lastErrSummary = "stream: " + llm.Truncate(err.Error(), 160)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: %w", attempts+1, time.Now().Format(time.DateTime), err))
				continue
			}
			// Calculate and set the cost_usd field
			response.Usage.CostUSD = llm.CostUSDFromResponse(resp.Header)

			endTime := time.Now()
			for _, entry := range response.InputTransformations {
				slog.WarnContext(ctx, "anthropic_input_transformation", "provider", "anthropic",
					"model", response.Model, "request_id", resp.Header.Get("Request-Id"), "response_id", response.ID,
					"type", entry.Type, "path", entry.Path, "reason", entry.Reason)
			}
			origin := s.messageOrigin(request.Model)
			result := toLLMResponse(response)
			result.Origin = &origin
			result.StartTime = &startTime
			result.EndTime = &endTime
			result.URL = url
			return result, nil
		default:
			buf, _ := io.ReadAll(resp.Body)
			retryAfterHdr := resp.Header.Get("Retry-After")
			resp.Body.Close()

			switch {
			case resp.StatusCode >= 500 && resp.StatusCode < 600:
				// server error, retry
				retryAfter = llm.ParseRetryAfter(retryAfterHdr)
				lastErrSummary = fmt.Sprintf("status %d: %s", resp.StatusCode, llm.Truncate(string(buf), 160))
				slog.WarnContext(ctx, "anthropic_request_failed", "response", string(buf), "status_code", resp.StatusCode, "url", url, "model", s.Model, "retry_after", retryAfter)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %v (url=%s, model=%s): %s", attempts+1, time.Now().Format(time.DateTime), resp.Status, url, cmp.Or(s.Model, DefaultModel), buf))
				continue
			case resp.StatusCode == 429:
				// rate limited, retry
				retryAfter = llm.ParseRetryAfter(retryAfterHdr)
				lastErrSummary = fmt.Sprintf("status 429 rate limited: %s", llm.Truncate(string(buf), 160))
				slog.WarnContext(ctx, "anthropic_request_rate_limited", "response", string(buf), "url", url, "model", s.Model, "retry_after", retryAfter)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %v (url=%s, model=%s): %s", attempts+1, time.Now().Format(time.DateTime), resp.Status, url, cmp.Or(s.Model, DefaultModel), buf))
				continue
			case resp.StatusCode >= 400 && resp.StatusCode < 500:
				if strippedPayload == nil && invalidThinkingSignature(string(buf)) {
					strippedReq := s.buildRequest(ir, true)
					strippedReq.Stream = true
					newPayload, err := json.Marshal(strippedReq)
					if err != nil {
						return nil, errors.Join(errs, fmt.Errorf("failed to marshal stripped request: %w", err))
					}
					newPayload = append(newPayload, '\n')
					if !bytes.Equal(newPayload, payload) {
						slog.WarnContext(ctx, "anthropic_invalid_thinking_signature, retrying without thinking blocks",
							"response", string(buf), "url", url, "model", s.Model)
						strippedPayload = newPayload
						request = strippedReq
						payload = strippedPayload
						errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: invalid thinking signature, retrying without thinking blocks", attempts+1, time.Now().Format(time.DateTime)))
						continue
					}
				}
				// some other 400, probably unrecoverable
				slog.WarnContext(ctx, "anthropic_request_failed", "response", string(buf), "status_code", resp.StatusCode, "url", url, "model", s.Model)
				return nil, errors.Join(errs, fmt.Errorf("attempt %d at %s: status %v (url=%s, model=%s): %s", attempts+1, time.Now().Format(time.DateTime), resp.Status, url, cmp.Or(s.Model, DefaultModel), buf))
			default:
				// ...retry, I guess?
				slog.WarnContext(ctx, "anthropic_request_failed", "response", string(buf), "status_code", resp.StatusCode, "url", url, "model", s.Model)
				errs = errors.Join(errs, fmt.Errorf("attempt %d at %s: status %v (url=%s, model=%s): %s", attempts+1, time.Now().Format(time.DateTime), resp.Status, url, cmp.Or(s.Model, DefaultModel), buf))
				continue
			}
		}
	}
}

// ConfigDetails returns configuration information for logging
func (s *Service) ConfigDetails() map[string]string {
	model := cmp.Or(s.Model, DefaultModel)
	url := cmp.Or(s.URL, DefaultURL)
	return map[string]string{
		"url":             url,
		"model":           model,
		"has_api_key_set": fmt.Sprintf("%v", s.APIKey != ""),
	}
}
