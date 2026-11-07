package ant

import (
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

func adaptCitation(_ llm.CitationContext, kind string, fields map[string]json.RawMessage) (reference string, retain bool, err error) {
	// Native objects are opaque, including signed fields. Full schema validation
	// remains Anthropic's responsibility; never fabricate encrypted_index values.
	switch kind {
	case "char_location", "page_location", "content_block_location", "search_result_location", "web_search_result_location":
		return "", true, nil
	case "url_citation", "google_grounding":
	default:
		return "", false, fmt.Errorf("unsupported citation type %q", kind)
	}
	url, err := llm.CitationString(fields, "url", true, false)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(url) == "" {
		return "", false, fmt.Errorf("url must be a nonempty string")
	}
	title, err := llm.CitationString(fields, "title", false, false)
	if err != nil {
		return "", false, err
	}
	quote, err := llm.CitationString(fields, "cited_text", false, false)
	if err != nil {
		return "", false, err
	}
	if _, err := llm.CitationString(fields, "encrypted_index", false, false); err != nil {
		return "", false, err
	}
	for _, name := range []string{"start_index", "end_index"} {
		if err := llm.CitationNonnegativeInteger(fields, name); err != nil {
			return "", false, err
		}
	}
	return llm.FormatCitationURLReference(url, title, quote), false, nil
}
