package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"

	"shelley.exe.dev/llm"
)

// https://ai.google.dev/api/generate-content#request-body
type Request struct {
	// Field order matters for JSON serialization - stable fields should come first
	// to maximize prefix deduplication when storing LLM requests.
	CachedContent     string            `json:"cachedContent,omitempty"` // format: "cachedContents/{name}"
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	// Contents comes last since it grows with each request in a conversation
	Contents []Content `json:"contents"`
}

// https://ai.google.dev/api/generate-content#response-body
type Response struct {
	Candidates     []Candidate     `json:"candidates"`
	UsageMetadata  *UsageMetadata  `json:"usageMetadata,omitempty"`
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
	headers        http.Header     // captured HTTP response headers
}

type UsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
	ToolUsePromptTokenCount int64 `json:"toolUsePromptTokenCount,omitempty"`
}

type PromptFeedback struct {
	BlockReason        string `json:"blockReason,omitempty"`
	BlockReasonMessage string `json:"blockReasonMessage,omitempty"`
}

// Header returns the HTTP response headers.
func (r *Response) Header() http.Header {
	return r.headers
}

type Candidate struct {
	Index             int                `json:"index,omitempty"`
	Content           Content            `json:"content"`
	FinishReason      string             `json:"finishReason,omitempty"`
	FinishMessage     string             `json:"finishMessage,omitempty"`
	GroundingMetadata *GroundingMetadata `json:"groundingMetadata,omitempty"`
	CitationMetadata  json.RawMessage    `json:"citationMetadata,omitempty"`
}

type Content struct {
	Parts []Part `json:"parts"`
	Role  string `json:"role,omitempty"`
}

// Part is a part of the content.
// This is a union data structure, only one-of the fields can be set.
type Part struct {
	raw                 json.RawMessage
	Text                string               `json:"text,omitempty"`
	FunctionCall        *FunctionCall        `json:"functionCall,omitempty"`
	FunctionResponse    *FunctionResponse    `json:"functionResponse,omitempty"`
	ToolCall            *ToolCall            `json:"toolCall,omitempty"`
	ToolResponse        *ToolResponse        `json:"toolResponse,omitempty"`
	ExecutableCode      *ExecutableCode      `json:"executableCode,omitempty"`
	CodeExecutionResult *CodeExecutionResult `json:"codeExecutionResult,omitempty"`
	// ThoughtSignature is required for Gemini 3 models when using function calling.
	// It must be passed back exactly as received when sending the conversation history.
	// Note: presence of ThoughtSignature does NOT mean the part is a thought summary —
	// Gemini 3 attaches it to ordinary final-answer text and tool calls so that
	// internal reasoning state can be rehydrated on the next turn. Use Thought to
	// detect a thought summary.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
	// Thought is true when the part is a thought summary (only emitted when
	// thinkingConfig.includeThoughts is true). https://ai.google.dev/gemini-api/docs/thinking
	Thought    bool  `json:"thought,omitempty"`
	InlineData *Blob `json:"inlineData,omitempty"`
	// TODO fileData
}

func (p Part) IsText() bool {
	return p.FunctionCall == nil && p.FunctionResponse == nil &&
		p.ToolCall == nil && p.ToolResponse == nil &&
		p.ExecutableCode == nil && p.CodeExecutionResult == nil && p.InlineData == nil
}

func (p Part) MarshalJSON() ([]byte, error) {
	if len(p.raw) != 0 {
		return p.raw, nil
	}
	type wirePart Part
	if p.IsText() {
		// Empty signed text still needs its text payload type on replay.
		return json.Marshal(struct {
			Text string `json:"text"`
			wirePart
		}{Text: p.Text, wirePart: wirePart(p)})
	}
	return json.Marshal(wirePart(p))
}

func (p *Part) UnmarshalJSON(data []byte) error {
	type wirePart Part
	var decoded wirePart
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = Part(decoded)
	if p.ToolCall != nil || p.ToolResponse != nil {
		p.raw = append(json.RawMessage(nil), data...)
	}
	return nil
}

// Blob is inline binary data (e.g. an image).
// https://ai.google.dev/api/caching#Blob
type Blob struct {
	MimeType string `json:"mimeType"`
	// Data is base64-encoded binary content.
	Data string `json:"data"`
}

type FunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type FunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type ToolCall struct {
	ToolType string          `json:"toolType,omitempty"`
	ID       string          `json:"id,omitempty"`
	ToolName string          `json:"toolName,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
}

type ToolResponse struct {
	ToolType string `json:"toolType,omitempty"`
	ID       string `json:"id,omitempty"`
	Response struct {
		SearchSuggestions string `json:"search_suggestions,omitempty"`
	} `json:"response"`
}

type ToolConfig struct {
	IncludeServerSideToolInvocations bool                   `json:"includeServerSideToolInvocations,omitempty"`
	FunctionCallingConfig            *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type FunctionCallingConfig struct {
	Mode string `json:"mode,omitempty"`
}

type GroundingMetadata struct {
	SearchEntryPoint  *SearchEntryPoint  `json:"searchEntryPoint,omitempty"`
	WebSearchQueries  []string           `json:"webSearchQueries,omitempty"`
	GroundingChunks   []GroundingChunk   `json:"groundingChunks,omitempty"`
	GroundingSupports []GroundingSupport `json:"groundingSupports,omitempty"`
}

type SearchEntryPoint struct {
	RenderedContent string `json:"renderedContent,omitempty"`
}

type GroundingChunk struct {
	Web *GroundingWeb `json:"web,omitempty"`
}

type GroundingWeb struct {
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

type GroundingSupport struct {
	Segment               *GroundingSegment `json:"segment,omitempty"`
	GroundingChunkIndices []int             `json:"groundingChunkIndices,omitempty"`
}

type GroundingSegment struct {
	PartIndex  int    `json:"partIndex,omitempty"`
	StartIndex int    `json:"startIndex,omitempty"`
	EndIndex   int    `json:"endIndex,omitempty"`
	Text       string `json:"text,omitempty"`
}

type ExecutableCode struct {
	Language Language `json:"language"`
	Code     string   `json:"code"`
}

type Language int

const (
	LanguageUnspecified Language = 0
	LanguagePython      Language = 1 // python >= 3.10 with numpy and simpy
)

type CodeExecutionResult struct {
	Outcome Outcome `json:"outcome"`
	Output  string  `json:"output"`
}

type Outcome int

const (
	OutcomeUnspecified      Outcome = 0
	OutcomeOK               Outcome = 1
	OutcomeFailed           Outcome = 2
	OutcomeDeadlineExceeded Outcome = 3
)

// https://ai.google.dev/api/generate-content#v1beta.GenerationConfig
type GenerationConfig struct {
	ResponseMimeType string          `json:"responseMimeType,omitempty"` // text/plain, application/json, or text/x.enum
	ResponseSchema   *Schema         `json:"responseSchema,omitempty"`   // for JSON
	MaxOutputTokens  int             `json:"maxOutputTokens,omitempty"`
	ThinkingConfig   *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

// ThinkingConfig controls extended thinking for Gemini models.
// ThinkingLevel and ThinkingBudget are mutually exclusive: setting both
// returns a 400 from the API. Use ThinkingLevel for Gemini 3 and later, and
// ThinkingBudget for Gemini 2.5.
// https://ai.google.dev/gemini-api/docs/thinking
type ThinkingConfig struct {
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`   // Supported effort values depend on the model.
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`  // Gemini 2.5: token count, -1 dynamic, 0 disable
	IncludeThoughts bool   `json:"includeThoughts,omitempty"` // include thought summaries in response
}

// https://ai.google.dev/api/caching#Tool
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
	CodeExecution        *struct{}             `json:"codeExecution,omitempty"` // if present, enables the model to execute code
	GoogleSearch         *struct{}             `json:"googleSearch,omitempty"`
}

// https://ai.google.dev/api/caching#FunctionDeclaration
type FunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

// https://ai.google.dev/api/caching#Schema
type Schema struct {
	Type        DataType          `json:"type"`
	Format      string            `json:"string,omitempty"` // for NUMBER type: float, double for INTEGER type: int32, int64 for STRING type: enum
	Description string            `json:"description,omitempty"`
	Nullable    *bool             `json:"nullable,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
	MaxItems    string            `json:"maxItems,omitempty"`   // for ARRAY
	MinItems    string            `json:"minItems,omitempty"`   // for ARRAY
	Properties  map[string]Schema `json:"properties,omitempty"` // for OBJECT
	Required    []string          `json:"required,omitempty"`   // for OBJECT
	Items       *Schema           `json:"items,omitempty"`      // for ARRAY
}

type DataType int

const (
	DataTypeUNSPECIFIED = DataType(0) // Not specified, should not be used.
	DataTypeSTRING      = DataType(1)
	DataTypeNUMBER      = DataType(2)
	DataTypeINTEGER     = DataType(3)
	DataTypeBOOLEAN     = DataType(4)
	DataTypeARRAY       = DataType(5)
	DataTypeOBJECT      = DataType(6)
)

// DefaultEndpoint is the Gemini API base URL used when Model.Endpoint is empty.
const DefaultEndpoint = "https://generativelanguage.googleapis.com/v1beta"

// APIError is returned by GenerateContent when the server returns a non-200 status.
type APIError struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GenerateContent: HTTP status: %d, %s", e.StatusCode, e.Body)
}

type Model struct {
	Model    string // e.g. "models/gemini-1.5-flash"
	APIKey   string
	HTTPC    *http.Client // if nil, http.DefaultClient is used
	Endpoint string       // if empty, DefaultEndpoint is used
	OnStream func(llm.StreamDelta)
}

func (m Model) GenerateContent(ctx context.Context, req *Request) (*Response, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%s:streamGenerateContent?alt=sse", m.endpoint(), m.Model), bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	httpReq.Header.Add("Content-Type", "application/json")
	httpReq.Header.Add("Accept", "text/event-stream")
	httpReq.Header.Add("x-goog-api-key", m.APIKey)
	httpResp, err := m.httpc().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("GenerateContent: do: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("GenerateContent: reading error response: %w", err)
		}
		return nil, &APIError{StatusCode: httpResp.StatusCode, Body: string(body), Header: httpResp.Header}
	}
	if contentType, _, err := mime.ParseMediaType(httpResp.Header.Get("Content-Type")); err != nil || contentType != "text/event-stream" {
		return nil, fmt.Errorf("GenerateContent: expected text/event-stream, got %q", httpResp.Header.Get("Content-Type"))
	}
	res, err := parseStreamResponse(ctx, httpResp.Body, m.OnStream)
	if err != nil {
		return nil, fmt.Errorf("GenerateContent: %w", err)
	}
	res.headers = httpResp.Header
	return res, nil
}

func (m Model) endpoint() string {
	if m.Endpoint != "" {
		return m.Endpoint
	}
	return DefaultEndpoint
}

func (m Model) httpc() *http.Client {
	if m.HTTPC != nil {
		return m.HTTPC
	}
	return http.DefaultClient
}
