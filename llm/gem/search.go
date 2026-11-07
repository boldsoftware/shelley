package gem

import (
	"encoding/json"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

type groundingCitation struct {
	EndUTF16  int    `json:"end_utf16"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	CitedText string `json:"cited_text"`
}

func convertSearchResponse(res *gemini.Response) ([]llm.Content, error) {
	if len(res.Candidates) == 0 {
		return convertGeminiResponseToContent(res), nil
	}
	candidate := res.Candidates[0]
	var content []llm.Content
	var answerParts []int
	partTargets := make([]int, len(candidate.Content.Parts))
	toolSuggestions := map[string]bool{}
	for i, part := range candidate.Content.Parts {
		partTargets[i] = len(content)
		if part.ToolCall == nil && part.ToolResponse == nil {
			if part.IsText() && !part.Thought && part.Text != "" {
				answerParts = append(answerParts, len(content))
			}
			one := &gemini.Response{Candidates: []gemini.Candidate{{Content: gemini.Content{Parts: []gemini.Part{part}}}}}
			content = append(content, convertGeminiResponseToContent(one)...)
			continue
		}
		opaque, err := json.Marshal(part)
		if err != nil {
			return nil, fmt.Errorf("marshal Gemini server tool: %w", err)
		}
		c := llm.Content{ToolName: "web_search", GeminiServerTool: opaque}
		if part.ToolCall != nil {
			if part.ToolCall.ToolType != "GOOGLE_SEARCH_WEB" {
				return nil, fmt.Errorf("unsupported Gemini server tool %q", part.ToolCall.ToolType)
			}
			c.Type = llm.ContentTypeServerToolUse
			c.ID = part.ToolCall.ID
			c.ToolInput = part.ToolCall.Args
		} else {
			if part.ToolResponse.ToolType != "GOOGLE_SEARCH_WEB" {
				return nil, fmt.Errorf("unsupported Gemini server tool result %q", part.ToolResponse.ToolType)
			}
			c.Type = llm.ContentTypeWebSearchToolResult
			c.ToolUseID = part.ToolResponse.ID
			if html := part.ToolResponse.Response.SearchSuggestions; html != "" {
				c.ToolResult = []llm.Content{{Type: llm.ContentTypeText, GoogleSearchSuggestions: html}}
				toolSuggestions[html] = true
			}
		}
		content = append(content, c)
	}
	metadata := candidate.GroundingMetadata
	if metadata == nil {
		return content, nil
	}
	citations := make(map[int][]groundingCitation)
	for _, support := range metadata.GroundingSupports {
		if support.Segment == nil {
			return nil, fmt.Errorf("Gemini grounding support has no segment")
		}
		segment := support.Segment
		if segment.PartIndex < 0 || segment.PartIndex >= len(partTargets) {
			return nil, fmt.Errorf("Gemini grounding part %d out of range", segment.PartIndex)
		}
		part := candidate.Content.Parts[segment.PartIndex]
		if !part.IsText() || part.Thought || part.Text == "" {
			return nil, fmt.Errorf("Gemini grounding part %d is not answer text", segment.PartIndex)
		}
		target := partTargets[segment.PartIndex]
		text := content[target].Text
		if segment.StartIndex < 0 || segment.EndIndex <= segment.StartIndex || segment.EndIndex > len(text) || !utf8.ValidString(text[segment.StartIndex:segment.EndIndex]) {
			return nil, fmt.Errorf("invalid Gemini grounding byte range [%d,%d) for text part %d", segment.StartIndex, segment.EndIndex, segment.PartIndex)
		}
		// JavaScript string offsets count UTF-16 code units.
		end := len(utf16.Encode([]rune(text[:segment.EndIndex])))
		for _, index := range support.GroundingChunkIndices {
			if index < 0 || index >= len(metadata.GroundingChunks) {
				return nil, fmt.Errorf("Gemini grounding chunk index %d out of range", index)
			}
			web := metadata.GroundingChunks[index].Web
			if web == nil || web.URI == "" {
				return nil, fmt.Errorf("Gemini grounding chunk %d has no web source URI", index)
			}
			citations[target] = append(citations[target], groundingCitation{
				EndUTF16: end,
				Type:     "google_grounding", URL: web.URI, Title: web.Title, CitedText: support.Segment.Text,
			})
		}
	}
	for target, refs := range citations {
		raw, err := json.Marshal(refs)
		if err != nil {
			return nil, fmt.Errorf("marshal Gemini grounding citations: %w", err)
		}
		content[target].Citations = raw
	}
	if entry := metadata.SearchEntryPoint; entry != nil && len(answerParts) > 0 && !toolSuggestions[entry.RenderedContent] {
		content[answerParts[len(answerParts)-1]].GoogleSearchSuggestions = entry.RenderedContent
	}
	return content, nil
}
