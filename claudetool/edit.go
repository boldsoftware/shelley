package claudetool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/diff"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/claudetool/hashline"
)

const EditName = "edit"

type EditTool struct {
	WorkingDir *MutableWorkingDir
}

func (e *EditTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        EditName,
		Description: editDescription,
		InputSchema: llm.MustSchema(editInputSchema),
		Run:         e.Run,
	}
}

const editDescription = `Apply precise edits using LINE#ID anchors from read output.

Workflow: read the file first, then use the LINE#ID anchors to target edits.
Batch all edits for one file in a single call. Re-read before editing again.

Edit entry: { loc, content }
- loc: where to apply (see below)
- content: array of replacement lines, or null to delete

loc values:
- { range: { pos: "N#ID", end: "N#ID" } } — replace inclusive line range
- { append: "N#ID" } — insert after anchored line
- { prepend: "N#ID" } — insert before anchored line
- "append" — append to end of file
- "prepend" — prepend to start of file

Rules:
- Use anchors exactly as shown in read output.
- range requires both pos and end.
- content must use literal file indentation (real tabs if file uses tabs).
- When content ends with a closing delimiter (}, */, etc), verify end includes
  the original line with that delimiter to avoid duplication.
`

const editInputSchema = `{
  "type": "object",
  "required": ["path", "edits"],
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the file to edit"
    },
    "edits": {
      "type": "array",
      "description": "List of edit operations",
      "items": {
        "type": "object",
        "required": ["loc"],
        "properties": {
          "loc": {
            "description": "Where to apply: {range:{pos:\"N#ID\",end:\"N#ID\"}}, {append:\"N#ID\"}, {prepend:\"N#ID\"}, \"append\", or \"prepend\""
          },
          "content": {
            "description": "Replacement lines (array of strings). null or omitted to delete.",
            "type": "array",
            "items": { "type": "string" }
          }
        }
      }
    }
  }
}`

// JSON input types — loc is polymorphic.
type editInput struct {
	Path  string          `json:"path"`
	Edits []editEntryJSON `json:"edits"`
}

type editEntryJSON struct {
	Loc     json.RawMessage `json:"loc"`
	Content *[]string       `json:"content"` // nil means delete
}

type locRange struct {
	Range struct {
		Pos string `json:"pos"`
		End string `json:"end"`
	} `json:"range"`
}

type locAppend struct {
	Append string `json:"append"`
}

type locPrepend struct {
	Prepend string `json:"prepend"`
}

func parseLoc(raw json.RawMessage) (hashline.Edit, error) {
	// Try string first: "append" or "prepend".
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "append":
			return hashline.Edit{Op: "append_file"}, nil
		case "prepend":
			return hashline.Edit{Op: "prepend_file"}, nil
		default:
			return hashline.Edit{}, fmt.Errorf("unknown loc string %q (expected \"append\" or \"prepend\")", s)
		}
	}

	// Try object forms.
	var lr locRange
	if json.Unmarshal(raw, &lr) == nil && lr.Range.Pos != "" {
		pos, err := hashline.ParseTag(lr.Range.Pos)
		if err != nil {
			return hashline.Edit{}, fmt.Errorf("range.pos: %w", err)
		}
		end, err := hashline.ParseTag(lr.Range.End)
		if err != nil {
			return hashline.Edit{}, fmt.Errorf("range.end: %w", err)
		}
		return hashline.Edit{Op: "replace_range", Pos: &pos, End: &end}, nil
	}

	var la locAppend
	if json.Unmarshal(raw, &la) == nil && la.Append != "" {
		anchor, err := hashline.ParseTag(la.Append)
		if err != nil {
			return hashline.Edit{}, fmt.Errorf("append anchor: %w", err)
		}
		return hashline.Edit{Op: "append_at", Pos: &anchor}, nil
	}

	var lp locPrepend
	if json.Unmarshal(raw, &lp) == nil && lp.Prepend != "" {
		anchor, err := hashline.ParseTag(lp.Prepend)
		if err != nil {
			return hashline.Edit{}, fmt.Errorf("prepend anchor: %w", err)
		}
		return hashline.Edit{Op: "prepend_at", Pos: &anchor}, nil
	}

	return hashline.Edit{}, fmt.Errorf("unrecognized loc: %s", string(raw))
}

func (e *EditTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input editInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorToolOut(err)
	}

	path := input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.WorkingDir.Get(), path)
	}

	if len(input.Edits) == 0 {
		return llm.ErrorfToolOut("no edits provided")
	}

	orig, err := os.ReadFile(path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read %q: %v", path, err)
	}

	var edits []hashline.Edit
	var parseErrors []error
	for i, entry := range input.Edits {
		he, err := parseLoc(entry.Loc)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Errorf("edit %d: %w", i, err))
			continue
		}
		if entry.Content != nil {
			he.Content = *entry.Content
		}
		edits = append(edits, he)
	}
	if len(parseErrors) > 0 {
		return llm.ErrorToolOut(errors.Join(parseErrors...))
	}

	result, err := hashline.ApplyEdits(string(orig), edits)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if err := os.WriteFile(path, []byte(result), 0o600); err != nil {
		return llm.ErrorfToolOut("failed to write %q: %v", path, err)
	}

	// Generate diff for display.
	var diffBuf strings.Builder
	_ = diff.Text(path, path, string(orig), result, &diffBuf)

	display := PatchDisplayData{
		Path: path,
		Diff: diffBuf.String(),
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent("<edits_applied>all</edits_applied>"),
		Display:    display,
	}
}
