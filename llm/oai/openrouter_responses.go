package oai

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

func (s *ResponsesService) isOpenRouter() bool {
	return s.ProviderName == "openrouter" ||
		endpointHostMatches(cmp.Or(s.ModelURL, s.Model.URL), "openrouter.ai", "openrouter.int.exe.xyz")
}

func (s *ResponsesService) replaysOpenRouterReasoning() bool {
	return s.isOpenRouter() && (s.ReasoningReplay == "" || s.ReasoningReplay == ReasoningReplayAuto)
}

// Do resumes explicit Gemini length stops without changing the original
// conversation or increasing its cumulative output budget.
// https://openrouter.ai/docs/cookbook/evaluate-and-optimize/model-migrations/gemini-4#responses-api
func (s *ResponsesService) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	ctx = llmhttp.WithOpenAIProtocol(ctx)
	req, fullURL, origin, err := s.buildRequest(ctx, ir)
	if err != nil {
		return nil, err
	}
	originalInput := slices.Clone(req.Input)
	var answer *llm.Response
	seenTokens := make(map[string]bool)
	for hop := 0; ; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := s.doResponses(ctx, ir, req, fullURL, origin)
		if err != nil {
			return nil, err
		}
		if answer == nil {
			answer = result
		} else {
			contextInput := answer.Usage.ContextWindowUsed() - answer.Usage.OutputTokens
			// A length-stopped hop can contain a partial function call. Only
			// the final hop's calls may reach the agent's tool executor.
			answer.Content = slices.DeleteFunc(answer.Content, func(c llm.Content) bool {
				return c.Type == llm.ContentTypeToolUse
			})
			answer.Content = append(answer.Content, result.Content...)
			answer.Usage.Add(result.Usage)
			answer.Usage.ContextWindowTokens = contextInput + answer.Usage.OutputTokens
			answer.ID = result.ID
			answer.Model = result.Model
			answer.StopReason = result.StopReason
			answer.EndTime = result.EndTime
		}
		token := latestGeminiContinuation(result.Content)
		if !s.replaysOpenRouterReasoning() ||
			result.StopReason != llm.StopReasonMaxTokens || token == nil ||
			req.MaxOutputTokens <= 0 || answer.Usage.OutputTokens >= uint64(req.MaxOutputTokens) {
			return answer, nil
		}
		if result.Usage.OutputTokens == 0 {
			return nil, fmt.Errorf("Gemini continuation stopped without output token usage")
		}
		if seenTokens[token.EncryptedContent] {
			return nil, fmt.Errorf("Gemini continuation repeated its checkpoint without progressing")
		}
		seenTokens[token.EncryptedContent] = true
		// A corrupt provider must not turn a single call into unbounded spending.
		if hop >= 15 {
			return nil, fmt.Errorf("Gemini continuation exceeded 16 hops within output budget %d", req.MaxOutputTokens)
		}
		summary := make([]responsesSummary, len(token.Summary))
		for i, part := range token.Summary {
			summary[i] = responsesSummary{Type: part.Type, Text: part.Text}
		}
		req.Input = append(slices.Clone(originalInput), responsesInputItem{
			Type: "reasoning", ID: token.ID, Summary: &summary,
			EncryptedContent: token.EncryptedContent, Format: token.Format,
		})
	}
}

func latestGeminiContinuation(content []llm.Content) *llm.OpenAIResponsesReasoningMetadata {
	for i := len(content) - 1; i >= 0; i-- {
		metadata := content[i].OpenAIResponsesReasoning
		if content[i].Type == llm.ContentTypeThinking && metadata != nil &&
			metadata.ID == "gemini-continuation" && metadata.EncryptedContent != "" {
			return metadata
		}
	}
	return nil
}
