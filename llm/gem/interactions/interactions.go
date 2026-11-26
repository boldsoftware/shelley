package interactions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"shelley.exe.dev/llm"
)

const (
	// DefaultEndpoint is the Gemini Interactions API base URL when Client.Endpoint is empty.
	DefaultEndpoint = "https://generativelanguage.googleapis.com/v1beta"
	// APIRevision is the Interactions REST protocol revision sent on every request.
	APIRevision = "2026-05-20"
)

// Request is the POST /interactions request body.
// Stable fields come first to maximize prefix deduplication when storing requests.
type Request struct {
	Model             string            `json:"model"`
	SystemInstruction string            `json:"system_instruction,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generation_config,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	Store             *bool             `json:"store,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	Input             []Step            `json:"input"`
}

// GenerationConfig configures token limits and reasoning for an interaction.
type GenerationConfig struct {
	MaxOutputTokens   int    `json:"max_output_tokens,omitempty"`
	ThinkingLevel     string `json:"thinking_level,omitempty"`
	ThinkingSummaries string `json:"thinking_summaries,omitempty"`
}

// Tool declares a client function or built-in server tool (such as google_search).
type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Content is a text or media item inside user_input, model_output, thought summary,
// or function_result.result.
type Content struct {
	Type        string       `json:"type"`
	Text        string       `json:"text,omitempty"`
	Data        string       `json:"data,omitempty"`
	URI         string       `json:"uri,omitempty"`
	MimeType    string       `json:"mime_type,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
}

// Annotation attaches a source citation to a UTF-8 byte span of a text content item.
type Annotation struct {
	Type       string `json:"type"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
}

// GoogleSearchResultItem is one entry in a google_search_result step's result array.
type GoogleSearchResultItem struct {
	SearchSuggestions string `json:"search_suggestions,omitempty"`
	URL               string `json:"url,omitempty"`
	Title             string `json:"title,omitempty"`
	RenderedContent   string `json:"rendered_content,omitempty"`
}

// Step is one entry in Request.Input or Response.Outputs.
// Native steps retain their original fields for replay.
type Step struct {
	raw        json.RawMessage
	Type       string          `json:"type"`
	Content    []Content       `json:"content,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	Summary    []Content       `json:"summary,omitempty"`
	ID         string          `json:"id,omitempty"`
	CallID     string          `json:"call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	SearchType string          `json:"search_type,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

func (s Step) MarshalJSON() ([]byte, error) {
	if len(s.raw) != 0 {
		return s.raw, nil
	}
	return json.Marshal(stepFields(s))
}

type stepFields Step

// WithArguments replaces only arguments, leaving other native fields intact.
func (s Step) WithArguments(args json.RawMessage) (Step, error) {
	if bytes.Equal(s.Arguments, args) {
		return s, nil
	}
	if len(s.raw) != 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(s.raw, &fields); err != nil {
			return Step{}, err
		}
		fields["arguments"] = args
		raw, err := json.Marshal(fields)
		if err != nil {
			return Step{}, err
		}
		s.raw = raw
	}
	s.Arguments = args
	return s, nil
}

func (s *Step) UnmarshalJSON(data []byte) error {
	var decoded stepFields
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = Step(decoded)
	s.raw = append(json.RawMessage(nil), data...)
	return nil
}

// Usage reports token accounting for a completed interaction.
type Usage struct {
	TotalTokens        int64 `json:"total_tokens,omitempty"`
	TotalInputTokens   int64 `json:"total_input_tokens,omitempty"`
	TotalCachedTokens  int64 `json:"total_cached_tokens,omitempty"`
	TotalOutputTokens  int64 `json:"total_output_tokens,omitempty"`
	TotalToolUseTokens int64 `json:"total_tool_use_tokens,omitempty"`
	TotalThoughtTokens int64 `json:"total_thought_tokens,omitempty"`
}

// Response is the assembled result of a streaming Interactions API call.
type Response struct {
	ID      string      `json:"id,omitempty"`
	Model   string      `json:"model,omitempty"`
	Status  string      `json:"status,omitempty"`
	Outputs []Step      `json:"outputs,omitempty"`
	Usage   *Usage      `json:"usage,omitempty"`
	headers http.Header // captured HTTP response headers
}

// Header returns the HTTP response headers.
func (r *Response) Header() http.Header {
	return r.headers
}

// APIError is returned by Create when the server responds with a non-200 status.
type APIError struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Interactions: HTTP status: %d, %s", e.StatusCode, e.Body)
}

// Client sends requests to the Gemini Interactions API.
type Client struct {
	APIKey   string
	HTTPC    *http.Client // defaults to http.DefaultClient if nil
	Endpoint string       // defaults to DefaultEndpoint if empty
	OnStream func(llm.StreamDelta)
}

// Create starts a streaming interaction and returns the assembled response.
func (c Client) Create(ctx context.Context, req *Request) (*Response, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	url := strings.TrimRight(c.endpoint(), "/") + "/interactions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-goog-api-key", c.APIKey)
	httpReq.Header.Set("Api-Revision", APIRevision)

	httpResp, err := c.httpc().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("Interactions: do: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("Interactions: reading error response: %w", err)
		}
		return nil, &APIError{StatusCode: httpResp.StatusCode, Body: string(body), Header: httpResp.Header}
	}
	if contentType, _, err := mime.ParseMediaType(httpResp.Header.Get("Content-Type")); err != nil || contentType != "text/event-stream" {
		return nil, fmt.Errorf("Interactions: expected text/event-stream, got %q", httpResp.Header.Get("Content-Type"))
	}
	res, err := parseStreamResponse(ctx, httpResp.Body, c.OnStream)
	if err != nil {
		return nil, fmt.Errorf("Interactions: %w", err)
	}
	res.headers = httpResp.Header
	return res, nil
}

func (c Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return DefaultEndpoint
}

func (c Client) httpc() *http.Client {
	if c.HTTPC != nil {
		return c.HTTPC
	}
	return http.DefaultClient
}
