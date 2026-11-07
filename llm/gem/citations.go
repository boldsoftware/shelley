package gem

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

func adaptCitation(_ llm.CitationContext, kind string, fields map[string]json.RawMessage) (string, bool, error) {
	switch kind {
	case "url_citation", "web_search_result_location", "google_grounding":
	default:
		return "", false, fmt.Errorf("cannot convert citation type %q to a source URL", kind)
	}
	url, err := llm.CitationString(fields, "url", true, false)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(url) == "" {
		return "", false, fmt.Errorf("url must be a nonempty string")
	}
	web := kind == "web_search_result_location"
	title, err := llm.CitationString(fields, "title", web, web)
	if err != nil {
		return "", false, err
	}
	quote, err := llm.CitationString(fields, "cited_text", web, false)
	if err != nil {
		return "", false, err
	}
	return llm.FormatCitationURLReference(url, title, quote), false, nil
}

func prepareCitations(ctx context.Context, req *llm.Request) (*llm.Request, error) {
	prepared, err := llm.PrepareRequestCitations(ctx, req, "gemini", adaptCitation)
	if err != nil {
		return nil, err
	}
	for i := range prepared.Messages {
		var content []llm.Content
		var references string
		for j, c := range prepared.Messages[i].Content {
			original := req.Messages[i].Content[j]
			// A signature may follow the text in a separate part.
			if c.Text != original.Text {
				references += strings.TrimPrefix(c.Text, original.Text)
				c.Text = original.Text
			}
			content = append(content, c)
		}
		if references != "" {
			content = append(content, llm.StringContent(references))
		}
		prepared.Messages[i].Content = content
	}
	return prepared, nil
}
