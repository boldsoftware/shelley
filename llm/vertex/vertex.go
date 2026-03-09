package vertex

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/auth/credentials"
	"google.golang.org/genai"

	"shelley.exe.dev/llm"
)

const (
	// DefaultRegion is used when no region is specified.
	// "global" routes to the nearest available region automatically.
	DefaultRegion = "global"

	DefaultMaxTokens = 8192

	// CredentialsFileEnv is the environment variable for the service account JSON key file path.
	CredentialsFileEnv = "GOOGLE_VERTEX_AI_CREDENTIALS"

	// ProjectEnv is the environment variable for the Google Cloud project ID.
	ProjectEnv = "GOOGLE_CLOUD_PROJECT"

	// RegionEnv is the environment variable for the Vertex AI region.
	RegionEnv = "GOOGLE_VERTEX_AI_REGION"

	// PublisherGoogle is the Vertex AI publisher for Google Gemini models.
	PublisherGoogle = "google"

	// Vertex AI Google Gemini model names.
	Gemini30Pro          = "gemini-3-pro"
	Gemini30FlashPreview = "gemini-3-flash-preview"
	Gemini31ProPreview   = "gemini-3.1-pro-preview"
	Gemini25Pro          = "gemini-2.5-pro"
)

// Service provides LLM completions through Vertex AI.
// It uses service account credentials with automatic token refresh.
// Fields should not be altered concurrently with calling any method on Service.
type Service struct {
	ProjectID       string            // Google Cloud project ID
	Region          string            // Vertex AI region, defaults to DefaultRegion ("global")
	Publisher       string            // e.g., "anthropic"
	Model           string            // Vertex AI model name
	CredentialsJSON []byte            // raw service account JSON, used for Google native SDK
	MaxTokens       int               // defaults to DefaultMaxTokens if zero
	ThinkingLevel   llm.ThinkingLevel // thinking level

	genaiClient     *genai.Client // cached for PublisherGoogle; initialized once
	genaiClientOnce sync.Once
	genaiClientErr  error
}

var _ llm.Service = (*Service)(nil)

// NewServiceFromCredentialsFile creates a Service using a service account JSON key file.
func NewServiceFromCredentialsFile(credentialsFile, projectID, region, publisher, model string) (*Service, error) {
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("vertex: reading credentials file %q: %w", credentialsFile, err)
	}
	return NewServiceFromCredentialsJSON(data, projectID, region, publisher, model)
}

// NewServiceFromCredentialsJSON creates a Service using service account JSON credentials.
func NewServiceFromCredentialsJSON(credentialsJSON []byte, projectID, region, publisher, model string) (*Service, error) {
	return &Service{
		ProjectID:       projectID,
		Region:          cmp.Or(region, DefaultRegion),
		Publisher:       publisher,
		Model:           model,
		CredentialsJSON: credentialsJSON,
		ThinkingLevel:   llm.ThinkingLevelMedium,
	}, nil
}

// Do executes a request against the Vertex AI API.
func (s *Service) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	return s.do(ctx, ir)
}

// TokenContextWindow returns the context window size for this service.
func (s *Service) TokenContextWindow() int {
	return 200000
}

// MaxImageDimension returns the maximum image dimension.
func (s *Service) MaxImageDimension() int {
	return 2000
}

// ConfigDetails returns configuration information for logging.
func (s *Service) ConfigDetails() map[string]string {
	return map[string]string{
		"model":     s.Model,
		"publisher": s.Publisher,
		"project":   s.ProjectID,
		"region":    cmp.Or(s.Region, DefaultRegion),
	}
}

func (s *Service) do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	s.genaiClientOnce.Do(func() {
		// Use Background context so cancellation of the first request doesn't
		// prevent the client from being cached for subsequent calls.
		s.genaiClient, s.genaiClientErr = s.newClient(context.Background())
	})
	if s.genaiClientErr != nil {
		return nil, fmt.Errorf("vertex: creating client: %w", s.genaiClientErr)
	}
	client := s.genaiClient

	contents, err := s.buildContents(ir.Messages)
	if err != nil {
		return nil, fmt.Errorf("vertex: building contents: %w", err)
	}

	tools, err := toTools(ir.Tools)
	if err != nil {
		return nil, fmt.Errorf("vertex: building tools: %w", err)
	}

	cfg := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(defaultMaxTokens(s.MaxTokens)),
		ThinkingConfig:  toThinkingConfig(s.ThinkingLevel),
	}

	if len(ir.System) > 0 {
		systemText := ""
		for i, sys := range ir.System {
			if i > 0 && systemText != "" && sys.Text != "" {
				systemText += "\n"
			}
			systemText += sys.Text
		}
		if systemText != "" {
			cfg.SystemInstruction = &genai.Content{
				Parts: []*genai.Part{{Text: systemText}},
			}
		}
	}

	if len(tools) > 0 {
		cfg.Tools = tools
		cfg.ToolConfig = &genai.ToolConfig{
			FunctionCallingConfig: toFunctionCallingConfig(ir.ToolChoice),
		}
	}

	modelID := s.Model
	if s.Publisher != "" && s.Publisher != PublisherGoogle {
		modelID = fmt.Sprintf("%s/%s", s.Publisher, s.Model)
	}

	startTime := time.Now()
	backoff := []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second}
	var errs error
	var rateLimitStartTime time.Time
	var attempts int

	for {
		if attempts > 10 {
			return nil, fmt.Errorf("vertex: request failed after %d attempts: %w", attempts, errs)
		}

		resp, err := client.Models.GenerateContent(ctx, modelID, contents, cfg)
		if err == nil {
			endTime := time.Now()
			llmResp := s.toLLMResponse(resp)
			llmResp.StartTime = &startTime
			llmResp.EndTime = &endTime
			return llmResp, nil
		}

		// Handle error
		var apiErr genai.APIError
		if errors.As(err, &apiErr) {
			if apiErr.Code == 429 {
				if rateLimitStartTime.IsZero() {
					rateLimitStartTime = time.Now()
				}
				durationSinceStart := time.Since(rateLimitStartTime)
				if durationSinceStart < 120*time.Second {
					jitter := time.Duration(rand.Int64N(int64(time.Second)))
					sleep := time.Second + jitter
					slog.WarnContext(ctx, "vertex: request rate limited, retrying", "model", modelID, "sleep", sleep, "duration", durationSinceStart)
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(sleep):
					}
					continue
				}
				// 429 exceeded time limit
				errs = errors.Join(errs, err)
			} else if apiErr.Code >= 500 && apiErr.Code < 600 {
				errs = errors.Join(errs, err)
				attempts++
				sleep := backoff[min(attempts-1, len(backoff)-1)] + time.Duration(rand.Int64N(int64(time.Second)))
				slog.WarnContext(ctx, "vertex: request failed, retrying", "status_code", apiErr.Code, "model", modelID, "sleep", sleep, "attempts", attempts)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(sleep):
				}
				continue
			} else {
				errs = errors.Join(errs, err)
			}
		} else {
			errs = errors.Join(errs, err)
		}

		// Not a retriable error or 429/5xx retry limit reached
		return nil, fmt.Errorf("vertex: generate content: %w", errs)
	}
}

// newClient constructs a genai.Client for Vertex AI using the service's credentials.
func (s *Service) newClient(ctx context.Context) (*genai.Client, error) {
	if len(s.CredentialsJSON) == 0 {
		return nil, fmt.Errorf("vertex: CredentialsJSON is empty")
	}
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		CredentialsJSON: s.CredentialsJSON,
		Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
	})
	if err != nil {
		return nil, fmt.Errorf("vertex: parsing credentials: %w", err)
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     s.ProjectID,
		Location:    defaultRegion(s.Region),
		Credentials: creds,
	})
}

// defaultRegion returns the region, defaulting to DefaultRegion.
func defaultRegion(r string) string {
	if r == "" {
		return DefaultRegion
	}
	return r
}

// defaultMaxTokens returns MaxTokens or DefaultMaxTokens.
func defaultMaxTokens(n int) int {
	if n == 0 {
		return DefaultMaxTokens
	}
	return n
}

// buildContents converts llm.Message slice to []*genai.Content.
//
// Key invariants:
//   - ContentTypeThinking → Part{Thought:true, Text: thinking text, ThoughtSignature: decoded from Signature}
//   - ContentTypeToolUse  → Part{FunctionCall: ...}
//   - ContentTypeToolResult → separate "user" Content with Part{FunctionResponse: ...}
//   - Tool results carry the tool name looked up from prior tool use IDs across all messages.
func (s *Service) buildContents(messages []llm.Message) ([]*genai.Content, error) {
	var contents []*genai.Content

	// Build a map of toolUseID → toolName across all messages for FunctionResponse lookup.
	toolIDToName := map[string]string{}
	toolIDToSignature := map[string]string{}
	for _, msg := range messages {
		for _, c := range msg.Content {
			if c.Type == llm.ContentTypeToolUse && c.ID != "" {
				toolIDToName[c.ID] = c.ToolName
				toolIDToSignature[c.ID] = c.Signature
			}
		}
	}

	for _, msg := range messages {
		if msg.ExcludedFromContext {
			continue
		}
		role := vertexRole(msg.Role)

		// Tool results must be emitted as "user" content, separate from
		// other content in the same message.
		var regularParts []*genai.Part
		var toolResultParts []*genai.Part

		for _, c := range msg.Content {
			switch c.Type {
			case llm.ContentTypeText:
				regularParts = append(regularParts, &genai.Part{Text: c.Text})

			case llm.ContentTypeThinking:
				sig, err := base64.StdEncoding.DecodeString(c.Signature)
				if err != nil && c.Signature != "" {
					return nil, fmt.Errorf("vertex: decoding thought signature: %w", err)
				}
				regularParts = append(regularParts, &genai.Part{
					Thought:          true,
					Text:             c.Thinking,
					ThoughtSignature: sig,
				})

			case llm.ContentTypeRedactedThinking:
				sig, err := base64.StdEncoding.DecodeString(c.Signature)
				if err != nil && c.Signature != "" {
					return nil, fmt.Errorf("vertex: decoding redacted thought signature: %w", err)
				}
				regularParts = append(regularParts, &genai.Part{
					Thought:          true,
					ThoughtSignature: sig,
				})

			case llm.ContentTypeToolUse:
				var args map[string]any
				if err := json.Unmarshal(c.ToolInput, &args); err != nil {
					return nil, fmt.Errorf("vertex: unmarshalling tool input for %q: %w", c.ToolName, err)
				}
				toolSig, err := base64.StdEncoding.DecodeString(c.Signature)
				if err != nil && c.Signature != "" {
					return nil, fmt.Errorf("vertex: decoding tool use thought signature: %w", err)
				}
				regularParts = append(regularParts, &genai.Part{
					FunctionCall:     &genai.FunctionCall{Name: c.ToolName, Args: args},
					ThoughtSignature: toolSig,
				})

			case llm.ContentTypeToolResult:
				toolName := toolIDToName[c.ToolUseID]
				if toolName == "" {
					toolName = c.ToolUseID // fallback
				}

				var toolSig []byte
				if sig := toolIDToSignature[c.ToolUseID]; sig != "" {
					dec, err := base64.StdEncoding.DecodeString(sig)
					if err != nil {
						return nil, fmt.Errorf("vertex: decoding tool use thought signature for result: %w", err)
					}
					toolSig = dec
				}

				var texts []string
				for _, r := range c.ToolResult {
					if r.Text != "" {
						texts = append(texts, r.Text)
					}
				}
				resultText := strings.Join(texts, "\n")
				if c.ToolError {
					resultText = "error: " + resultText
				}
				toolResultParts = append(toolResultParts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						Name:     toolName,
						Response: map[string]any{"result": resultText},
					},
					ThoughtSignature: toolSig,
				})
			}
		}

		if len(regularParts) > 0 {
			contents = append(contents, &genai.Content{Role: role, Parts: regularParts})
		}
		if len(toolResultParts) > 0 {
			contents = append(contents, &genai.Content{Role: "user", Parts: toolResultParts})
		}
	}
	return contents, nil
}

func vertexRole(r llm.MessageRole) string {
	if r == llm.MessageRoleAssistant {
		return "model"
	}
	return "user"
}

// toTools converts []*llm.Tool to []*genai.Tool.
func toTools(tools []*llm.Tool) ([]*genai.Tool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	var decls []*genai.FunctionDeclaration
	for _, t := range tools {
		var schemaJSON map[string]any
		if err := json.Unmarshal(t.InputSchema, &schemaJSON); err != nil {
			return nil, fmt.Errorf("vertex: unmarshalling schema for tool %q: %w", t.Name, err)
		}
		schema := jsonSchemaToVertex(schemaJSON)
		decls = append(decls, &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}, nil
}

// jsonSchemaToVertex recursively converts a JSON schema map to *genai.Schema.
func jsonSchemaToVertex(s map[string]any) *genai.Schema {
	schema := &genai.Schema{}

	if t, ok := s["type"].(string); ok {
		switch t {
		case "string":
			schema.Type = genai.TypeString
		case "number":
			schema.Type = genai.TypeNumber
		case "integer":
			schema.Type = genai.TypeInteger
		case "boolean":
			schema.Type = genai.TypeBoolean
		case "array":
			schema.Type = genai.TypeArray
		case "object":
			schema.Type = genai.TypeObject
		}
	}
	if d, ok := s["description"].(string); ok {
		schema.Description = d
	}
	if enums, ok := s["enum"].([]any); ok {
		for _, v := range enums {
			if sv, ok := v.(string); ok {
				schema.Enum = append(schema.Enum, sv)
			}
		}
	}
	if props, ok := s["properties"].(map[string]any); ok {
		schema.Properties = map[string]*genai.Schema{}
		for k, v := range props {
			if vm, ok := v.(map[string]any); ok {
				schema.Properties[k] = jsonSchemaToVertex(vm)
			}
		}
	}
	if req, ok := s["required"].([]any); ok {
		for _, v := range req {
			if sv, ok := v.(string); ok {
				schema.Required = append(schema.Required, sv)
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		schema.Items = jsonSchemaToVertex(items)
	}
	return schema
}

// toThinkingConfig converts llm.ThinkingLevel to *genai.ThinkingConfig.
// Returns nil when thinking is off.
func toThinkingConfig(level llm.ThinkingLevel) *genai.ThinkingConfig {
	if level == llm.ThinkingLevelOff {
		return nil
	}
	var gl genai.ThinkingLevel
	switch level {
	case llm.ThinkingLevelMinimal:
		gl = genai.ThinkingLevelMinimal
	case llm.ThinkingLevelLow:
		gl = genai.ThinkingLevelLow
	case llm.ThinkingLevelHigh:
		gl = genai.ThinkingLevelHigh
	default: // Medium and any unrecognized value
		gl = genai.ThinkingLevelMedium
	}
	return &genai.ThinkingConfig{
		IncludeThoughts: true,
		ThinkingLevel:   gl,
	}
}

// toFunctionCallingConfig converts *llm.ToolChoice to *genai.FunctionCallingConfig.
func toFunctionCallingConfig(tc *llm.ToolChoice) *genai.FunctionCallingConfig {
	if tc == nil {
		return &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}
	}
	switch tc.Type {
	case llm.ToolChoiceTypeNone:
		return &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}
	case llm.ToolChoiceTypeAny:
		return &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAny}
	case llm.ToolChoiceTypeTool:
		return &genai.FunctionCallingConfig{
			Mode:                 genai.FunctionCallingConfigModeAny,
			AllowedFunctionNames: []string{tc.Name},
		}
	default: // Auto
		return &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto}
	}
}

// toLLMResponse converts *genai.GenerateContentResponse to *llm.Response.
func (s *Service) toLLMResponse(resp *genai.GenerateContentResponse) *llm.Response {
	result := &llm.Response{
		Model: s.Model,
		Role:  llm.MessageRoleAssistant,
	}

	if resp.UsageMetadata != nil {
		result.Usage = llm.Usage{
			InputTokens:  uint64(resp.UsageMetadata.PromptTokenCount),
			OutputTokens: uint64(resp.UsageMetadata.CandidatesTokenCount),
		}
	}

	if len(resp.Candidates) == 0 {
		return result
	}

	candidate := resp.Candidates[0]
	hasToolCall := false

	if candidate.Content != nil {
		var lastThoughtSignature string
		for _, part := range candidate.Content.Parts {
			if len(part.ThoughtSignature) > 0 {
				lastThoughtSignature = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
			}

			switch {
			case part.Thought:
				result.Content = append(result.Content, llm.Content{
					Type:      llm.ContentTypeThinking,
					Text:      part.Text,
					Thinking:  part.Text,
					Signature: lastThoughtSignature,
				})

			case part.FunctionCall != nil:
				hasToolCall = true
				args, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					args = []byte("{}")
				}

				sig := lastThoughtSignature
				if len(part.ThoughtSignature) > 0 {
					sig = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				}

				result.Content = append(result.Content, llm.Content{
					ID:        fmt.Sprintf("genai-%s-%d", part.FunctionCall.Name, time.Now().UnixNano()),
					Type:      llm.ContentTypeToolUse,
					ToolName:  part.FunctionCall.Name,
					ToolInput: json.RawMessage(args),
					Signature: sig,
				})
				lastThoughtSignature = ""

			case part.Text != "":
				result.Content = append(result.Content, llm.Content{
					Type: llm.ContentTypeText,
					Text: part.Text,
				})
			}
		}
	}

	if hasToolCall {
		result.StopReason = llm.StopReasonToolUse
	} else {
		result.StopReason = toStopReason(candidate.FinishReason)
	}

	return result
}

// toStopReason converts genai.FinishReason to llm.StopReason.
func toStopReason(r genai.FinishReason) llm.StopReason {
	switch r {
	case genai.FinishReasonMaxTokens:
		return llm.StopReasonMaxTokens
	case genai.FinishReasonStop:
		return llm.StopReasonEndTurn
	case genai.FinishReasonSafety, genai.FinishReasonProhibitedContent,
		genai.FinishReasonBlocklist, genai.FinishReasonSPII,
		genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent:
		return llm.StopReasonRefusal
	default:
		return llm.StopReasonEndTurn
	}
}
