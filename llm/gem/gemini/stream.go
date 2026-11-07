package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"shelley.exe.dev/llm"
)

func parseStreamResponse(ctx context.Context, r io.Reader, onStream func(llm.StreamDelta)) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var response Response
	var data []string
	dispatch := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(data) == 0 {
			return nil
		}
		var event struct {
			Response
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event)
		data = data[:0]
		if err != nil {
			return fmt.Errorf("decoding Gemini stream event: %w", err)
		}
		if event.Error != nil {
			// In-band errors must not retry a response whose deltas may already be visible.
			return fmt.Errorf("Gemini stream error %d (%s): %s", event.Error.Code, event.Error.Status, event.Error.Message)
		}
		if len(event.Candidates) == 0 && event.UsageMetadata == nil && event.PromptFeedback == nil {
			return fmt.Errorf("empty Gemini stream event")
		}
		llm.ReportProgress(ctx)
		if event.UsageMetadata != nil {
			response.UsageMetadata = event.UsageMetadata
		}
		if event.PromptFeedback != nil {
			response.PromptFeedback = event.PromptFeedback
		}
		if len(event.Candidates) > 1 {
			return fmt.Errorf("multiple Gemini stream candidates are not supported")
		}
		for _, candidate := range event.Candidates {
			if candidate.Index != 0 {
				return fmt.Errorf("unexpected Gemini stream candidate index %d", candidate.Index)
			}
			if len(response.Candidates) == 0 {
				response.Candidates = append(response.Candidates, Candidate{})
			}
			dst := &response.Candidates[0]
			if candidate.Content.Role != "" {
				dst.Content.Role = candidate.Content.Role
			}
			if candidate.FinishReason != "" {
				dst.FinishReason = candidate.FinishReason
			}
			if candidate.FinishMessage != "" {
				dst.FinishMessage = candidate.FinishMessage
			}
			for _, part := range candidate.Content.Parts {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !part.IsText() && part.FunctionCall == nil {
					return fmt.Errorf("unsupported Gemini stream part")
				}
				if part.IsText() && part.Text == "" && part.ThoughtSignature == "" {
					continue
				}
				index := len(dst.Content.Parts)
				if index > 0 && canMergeText(dst.Content.Parts[index-1], part) {
					index--
					dst.Content.Parts[index].Text += part.Text
				} else {
					dst.Content.Parts = append(dst.Content.Parts, part)
				}
				if onStream != nil && part.IsText() && part.Text != "" {
					kind := "text"
					if part.Thought {
						kind = "thinking"
					}
					onStream(llm.StreamDelta{Type: kind, Text: part.Text, Index: index})
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return nil, err
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if field == "data" {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading Gemini stream: %w", err)
	}
	if len(data) != 0 {
		return nil, fmt.Errorf("unterminated Gemini SSE event: %w", io.ErrUnexpectedEOF)
	}
	if feedback := response.PromptFeedback; feedback != nil && feedback.BlockReason != "" && feedback.BlockReason != "BLOCK_REASON_UNSPECIFIED" {
		return &response, nil
	}
	if len(response.Candidates) == 0 {
		return nil, fmt.Errorf("Gemini stream has no candidates or prompt block: %w", io.ErrUnexpectedEOF)
	}
	if reason := response.Candidates[0].FinishReason; reason == "" || reason == "FINISH_REASON_UNSPECIFIED" {
		return nil, fmt.Errorf("Gemini stream ended with unfinished finish reason %q: %w", reason, io.ErrUnexpectedEOF)
	}
	return &response, nil
}

func canMergeText(a, b Part) bool {
	return a.IsText() && b.IsText() && a.Text != "" && b.Text != "" &&
		a.ThoughtSignature == "" && b.ThoughtSignature == "" && a.Thought == b.Thought
}
