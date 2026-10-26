package claudetool

import (
	"context"
	"encoding/json"
	"fmt"

	"shelley.exe.dev/llm"
)

// CompactInPlaceName is the tool that lets the agent compact its own context.
const CompactInPlaceName = "compact_in_place"

// InPlaceCompactor indexes and compacts a conversation's context. It is
// implemented by the server package to avoid import cycles.
type InPlaceCompactor interface {
	// Index lists the part of the LLM's current view that can be compacted.
	Index(ctx context.Context) (string, error)
	// Compact validates and records in, a compact or skip; the turn
	// continues on the compacted history.
	Compact(ctx context.Context, in CompactInPlaceInput) (string, error)
}

// IndexID is an id from the index: a sequence id or a note id. Models send
// sequence ids as numbers or strings; both are accepted.
type IndexID string

func (id *IndexID) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*id = IndexID(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("id must be a string or number: %s", b)
	}
	*id = IndexID(s)
	return nil
}

// CompactCollapse replaces the index rows From..To (inclusive) with Note.
type CompactCollapse struct {
	From IndexID `json:"from"`
	To   IndexID `json:"to"`
	Note string  `json:"note"`
}

// CompactInPlaceInput is the compact_in_place tool's input.
type CompactInPlaceInput struct {
	Action   string            `json:"action"`
	Trim     []IndexID         `json:"trim"`
	Collapse []CompactCollapse `json:"collapse"`
}

const compactInPlaceDescription = `Compact this conversation's context in place.
Call with action "index" first: it lists the older messages and explains what to do.
(Right after a nudge that carries the index, go straight to "compact".)
Then call with action "compact" to collapse message ranges into short notes
and trim tool outputs. Originals stay in the database.
If nothing is worth compacting, call with action "skip": it compacts nothing,
but drops the context nudges and earlier compact_in_place calls from your context.`

const compactInPlaceSchema = `{
  "type": "object",
  "required": ["action"],
  "properties": {
    "action": {"type": "string", "enum": ["index", "compact", "skip"]},
    "trim": {
      "type": "array",
      "description": "compact: ids of index rows whose tool outputs to trim",
      "items": {"type": "string"}
    },
    "collapse": {
      "type": "array",
      "description": "compact: ranges of index ids (inclusive) to replace with a note",
      "items": {
        "type": "object",
        "required": ["from", "to", "note"],
        "properties": {
          "from": {"type": "string"},
          "to": {"type": "string"},
          "note": {"type": "string", "description": "what still matters from the range"}
        }
      }
    }
  }
}`

// CompactInPlaceTool returns the compact_in_place tool backed by c.
func CompactInPlaceTool(c InPlaceCompactor) *llm.Tool {
	return &llm.Tool{
		Name:        CompactInPlaceName,
		Description: compactInPlaceDescription,
		InputSchema: llm.MustSchema(compactInPlaceSchema),
		Run: llm.RunJSON(func(ctx context.Context, in CompactInPlaceInput) llm.ToolOut {
			var text string
			var err error
			switch in.Action {
			case "index":
				text, err = c.Index(ctx)
			case "compact", "skip":
				text, err = c.Compact(ctx, in)
			default:
				err = fmt.Errorf("action must be index, compact or skip")
			}
			if err != nil {
				return llm.ErrorToolOut(err)
			}
			return llm.ToolOut{LLMContent: llm.TextContent(text)}
		}),
	}
}
