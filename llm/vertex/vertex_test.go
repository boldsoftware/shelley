package vertex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
	"shelley.exe.dev/llm"
)

// TestBuildContents verifies llm.Message → []*genai.Content conversion.
func TestBuildContents(t *testing.T) {
	s := &Service{Model: Gemini30FlashPreview, ThinkingLevel: llm.ThinkingLevelMedium}

	sig := []byte{0x01, 0x02, 0x03}
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	messages := []llm.Message{
		{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{Type: llm.ContentTypeThinking, Thinking: "I think...", Signature: sigB64},
				{Type: llm.ContentTypeToolUse, ID: "tc1", ToolName: "bash", ToolInput: json.RawMessage(`{"cmd":"ls"}`)},
			},
		},
		{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{
					Type:      llm.ContentTypeToolResult,
					ToolUseID: "tc1",
					ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "file.txt"}},
				},
			},
		},
	}

	contents, err := s.buildContents(messages)
	if err != nil {
		t.Fatalf("buildContents: %v", err)
	}

	// Expect 2 contents: one model, one user.
	if len(contents) != 2 {
		t.Fatalf("want 2 contents, got %d", len(contents))
	}

	// Model content: thinking part + function call part.
	modelContent := contents[0]
	if modelContent.Role != "model" {
		t.Errorf("want role=model, got %q", modelContent.Role)
	}
	if len(modelContent.Parts) != 2 {
		t.Fatalf("want 2 model parts, got %d", len(modelContent.Parts))
	}

	// Thought part must carry ThoughtSignature.
	thoughtPart := modelContent.Parts[0]
	if !thoughtPart.Thought {
		t.Errorf("want Thought=true")
	}
	if string(thoughtPart.ThoughtSignature) != string(sig) {
		t.Errorf("want ThoughtSignature=%v, got %v", sig, thoughtPart.ThoughtSignature)
	}
	if thoughtPart.Text != "I think..." {
		t.Errorf("want Text='I think...', got %q", thoughtPart.Text)
	}

	// Function call part.
	fcPart := modelContent.Parts[1]
	if fcPart.FunctionCall == nil {
		t.Fatalf("want FunctionCall, got nil")
	}
	if fcPart.FunctionCall.Name != "bash" {
		t.Errorf("want FunctionCall.Name=bash, got %q", fcPart.FunctionCall.Name)
	}

	// User content: function response.
	userContent := contents[1]
	if userContent.Role != "user" {
		t.Errorf("want role=user, got %q", userContent.Role)
	}
	if len(userContent.Parts) != 1 {
		t.Fatalf("want 1 user part, got %d", len(userContent.Parts))
	}
	if userContent.Parts[0].FunctionResponse == nil {
		t.Fatalf("want FunctionResponse, got nil")
	}
	if userContent.Parts[0].FunctionResponse.Name != "bash" {
		t.Errorf("want FunctionResponse.Name=bash, got %q", userContent.Parts[0].FunctionResponse.Name)
	}
}

// TestToTools verifies llm.Tool → *genai.Tool schema conversion.
func TestToTools(t *testing.T) {
	tools := []*llm.Tool{
		{
			Name:        "search",
			Description: "Search the web",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		},
	}

	gt, err := toTools(tools)
	if err != nil {
		t.Fatalf("toTools: %v", err)
	}
	if len(gt) != 1 {
		t.Fatalf("want 1 tool, got %d", len(gt))
	}
	fd := gt[0].FunctionDeclarations
	if len(fd) != 1 {
		t.Fatalf("want 1 FunctionDeclaration, got %d", len(fd))
	}
	if fd[0].Name != "search" {
		t.Errorf("want name=search, got %q", fd[0].Name)
	}
	if fd[0].Parameters == nil {
		t.Fatalf("want Parameters, got nil")
	}
	if fd[0].Parameters.Type != genai.TypeObject {
		t.Errorf("want TypeObject, got %q", fd[0].Parameters.Type)
	}
	if _, ok := fd[0].Parameters.Properties["query"]; !ok {
		t.Errorf("want 'query' property")
	}
}

// TestToLLMResponseThoughtSignature verifies that thought signatures survive the
// genai→llm round-trip: ThoughtSignature bytes → base64 string in llm.Content.Signature.
func TestToLLMResponseThoughtSignature(t *testing.T) {
	sig := []byte{0xde, 0xad, 0xbe, 0xef}

	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{Thought: true, Text: "reasoning...", ThoughtSignature: sig},
						{FunctionCall: &genai.FunctionCall{Name: "bash", Args: map[string]any{"cmd": "ls"}}},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
		},
	}

	s := &Service{Model: Gemini30FlashPreview}
	llmResp := s.toLLMResponse(resp)

	if len(llmResp.Content) != 2 {
		t.Fatalf("want 2 content items, got %d", len(llmResp.Content))
	}

	// First content should be a thinking block with the signature encoded as base64.
	thinking := llmResp.Content[0]
	if thinking.Type != llm.ContentTypeThinking {
		t.Errorf("want ContentTypeThinking, got %v", thinking.Type)
	}
	if thinking.Text != "reasoning..." {
		t.Errorf("want Text='reasoning...', got %q", thinking.Text)
	}
	wantSig := base64.StdEncoding.EncodeToString(sig)
	if thinking.Signature != wantSig {
		t.Errorf("want Signature=%q, got %q", wantSig, thinking.Signature)
	}

	// Second content should be a tool use.
	toolUse := llmResp.Content[1]
	if toolUse.Type != llm.ContentTypeToolUse {
		t.Errorf("want ContentTypeToolUse, got %v", toolUse.Type)
	}
	if toolUse.ToolName != "bash" {
		t.Errorf("want ToolName=bash, got %q", toolUse.ToolName)
	}

	// StopReason: function call present → ToolUse.
	if llmResp.StopReason != llm.StopReasonToolUse {
		t.Errorf("want StopReasonToolUse, got %v", llmResp.StopReason)
	}
}

// TestToThinkingConfig verifies ThinkingLevel → ThinkingConfig mapping.
func TestToThinkingConfig(t *testing.T) {
	cases := []struct {
		level     llm.ThinkingLevel
		wantNil   bool
		wantLevel genai.ThinkingLevel
	}{
		{llm.ThinkingLevelOff, true, ""},
		{llm.ThinkingLevelMinimal, false, genai.ThinkingLevelMinimal},
		{llm.ThinkingLevelLow, false, genai.ThinkingLevelLow},
		{llm.ThinkingLevelMedium, false, genai.ThinkingLevelMedium},
		{llm.ThinkingLevelHigh, false, genai.ThinkingLevelHigh},
	}
	for _, c := range cases {
		tc := toThinkingConfig(c.level)
		if c.wantNil {
			if tc != nil {
				t.Errorf("level %v: want nil ThinkingConfig, got %+v", c.level, tc)
			}
			continue
		}
		if tc == nil {
			t.Fatalf("level %v: want non-nil ThinkingConfig", c.level)
		}
		if tc.ThinkingLevel != c.wantLevel {
			t.Errorf("level %v: want ThinkingLevel=%q, got %q", c.level, c.wantLevel, tc.ThinkingLevel)
		}
		if !tc.IncludeThoughts {
			t.Errorf("level %v: want IncludeThoughts=true", c.level)
		}
	}
}

// TestToStopReason verifies FinishReason → llm.StopReason mapping.
func TestToStopReason(t *testing.T) {
	cases := []struct {
		reason genai.FinishReason
		want   llm.StopReason
	}{
		{genai.FinishReasonStop, llm.StopReasonEndTurn},
		{genai.FinishReasonMaxTokens, llm.StopReasonMaxTokens},
		{genai.FinishReasonSafety, llm.StopReasonRefusal},
		{genai.FinishReasonProhibitedContent, llm.StopReasonRefusal},
		{genai.FinishReasonBlocklist, llm.StopReasonRefusal},
		{genai.FinishReasonSPII, llm.StopReasonRefusal},
		{genai.FinishReasonImageSafety, llm.StopReasonRefusal},
		{genai.FinishReasonImageProhibitedContent, llm.StopReasonRefusal},
		{genai.FinishReasonUnspecified, llm.StopReasonEndTurn},
	}
	for _, c := range cases {
		got := toStopReason(c.reason)
		if got != c.want {
			t.Errorf("toStopReason(%v) = %v, want %v", c.reason, got, c.want)
		}
	}
}

func TestToFunctionCallingConfig(t *testing.T) {
	cases := []struct {
		name     string
		tc       *llm.ToolChoice
		wantMode genai.FunctionCallingConfigMode
		wantFns  []string
	}{
		{"nil → auto", nil, genai.FunctionCallingConfigModeAuto, nil},
		{"auto", &llm.ToolChoice{Type: llm.ToolChoiceTypeAuto}, genai.FunctionCallingConfigModeAuto, nil},
		{"any", &llm.ToolChoice{Type: llm.ToolChoiceTypeAny}, genai.FunctionCallingConfigModeAny, nil},
		{"none", &llm.ToolChoice{Type: llm.ToolChoiceTypeNone}, genai.FunctionCallingConfigModeNone, nil},
		{"specific tool", &llm.ToolChoice{Type: llm.ToolChoiceTypeTool, Name: "bash"}, genai.FunctionCallingConfigModeAny, []string{"bash"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toFunctionCallingConfig(c.tc)
			if got.Mode != c.wantMode {
				t.Errorf("want Mode=%q, got %q", c.wantMode, got.Mode)
			}
			if len(got.AllowedFunctionNames) != len(c.wantFns) {
				t.Errorf("want AllowedFunctionNames=%v, got %v", c.wantFns, got.AllowedFunctionNames)
			}
			for i, fn := range c.wantFns {
				if got.AllowedFunctionNames[i] != fn {
					t.Errorf("want AllowedFunctionNames[%d]=%q, got %q", i, fn, got.AllowedFunctionNames[i])
				}
			}
		})
	}
}

type mockTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.roundTrip(req)
}

func TestDoRetry(t *testing.T) {
	var attempts int
	mock := &mockTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts < 3 {
				// Return 429 for the first two attempts
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`{"error": {"code": 429, "message": "Rate limit exceeded", "status": "RESOURCE_EXHAUSTED"}}`)),
					Header:     make(http.Header),
				}, nil
			}
			// Return 200 for the third attempt
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"candidates": [{"content": {"parts": [{"text": "Success"}]}}]}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	httpClient := &http.Client{Transport: mock}
	// We need to bypass the real client creation
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: httpClient,
		Backend:    genai.BackendVertexAI,
		Project:    "test-project",
		Location:   "us-central1",
	})
	if err != nil {
		t.Fatalf("failed to create genai client: %v", err)
	}

	s := &Service{
		Publisher: "google",
		Model:     "gemini-1.5-pro",
	}
	s.genaiClient = client
	s.genaiClientOnce.Do(func() {}) // Mark as done

	ir := &llm.Request{
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hi"}}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := s.do(ctx, ir)
	if err != nil {
		t.Fatalf("do failed: %v", err)
	}

	if attempts != 3 {
		t.Errorf("want 3 attempts, got %d", attempts)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "Success" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestModelIDFormatting(t *testing.T) {
	cases := []struct {
		publisher string
		model     string
		want      string
	}{
		{PublisherGoogle, "gemini-1.5-pro", "gemini-1.5-pro"},
		{"anthropic", "claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
		{"zai-org", "glm-4", "zai-org/glm-4"},
		{"", "my-model", "my-model"},
	}

	for _, c := range cases {
		t.Run(c.publisher+"/"+c.model, func(t *testing.T) {
			// Create a mock transport that captures the model ID
			var capturedModel string
			mock := &mockTransport{
				roundTrip: func(req *http.Request) (*http.Response, error) {
					// The genai SDK will include the model ID in the URL for Vertex AI
					// For example: /v1/projects/.../locations/.../publishers/.../models/MODEL_ID:streamGenerateContent
					capturedModel = req.URL.Path
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"candidates": [{"content": {"parts": [{"text": "Success"}]}}]}`)),
						Header:     make(http.Header),
					}, nil
				},
			}
			httpClient := &http.Client{Transport: mock}
			client, _ := genai.NewClient(context.Background(), &genai.ClientConfig{
				HTTPClient: httpClient,
				Backend:    genai.BackendVertexAI,
				Project:    "p",
				Location:   "l",
			})

			s := &Service{Publisher: c.publisher, Model: c.model}
			s.genaiClient = client
			s.genaiClientOnce.Do(func() {})

			ir := &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "Hi"}}}}}
			_, err := s.do(context.Background(), ir)
			if err != nil {
				t.Fatalf("do failed: %v", err)
			}

			wantSuffix := "/" + c.want + ":generateContent"
			if c.publisher != "" && c.publisher != PublisherGoogle {
				wantSuffix = "/publishers/" + c.publisher + "/models/" + c.model + ":generateContent"
			}
			
			if !strings.HasSuffix(capturedModel, wantSuffix) {
				t.Errorf("want URL suffix %q, got %q", wantSuffix, capturedModel)
			}
		})
	}
}
