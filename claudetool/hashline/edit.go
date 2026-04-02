package hashline

import (
	"fmt"
	"slices"
	"strings"
)

// Edit represents a single hashline edit operation.
type Edit struct {
	Op      string   // "replace_range", "append_at", "prepend_at", "append_file", "prepend_file"
	Pos     *Anchor  // start anchor (nil for file-level ops)
	End     *Anchor  // end anchor (only for replace_range)
	Content []string // replacement/inserted lines; nil means delete
}

// ApplyEdits validates all anchors and applies edits to file content.
// Edits are applied bottom-up so line-number shifts don't affect earlier edits.
func ApplyEdits(text string, edits []Edit) (string, error) {
	if len(edits) == 0 {
		return text, nil
	}

	fileLines := strings.Split(text, "\n")

	// Pre-validate: collect all hash mismatches before mutating.
	var mismatches []HashMismatch
	validateRef := func(ref *Anchor) {
		if ref == nil {
			return
		}
		if ref.Line < 1 || ref.Line > len(fileLines) {
			// Out-of-range is a hard error, not a mismatch.
			return
		}
		actual := ComputeLineHash(ref.Line, fileLines[ref.Line-1])
		if actual != ref.Hash {
			mismatches = append(mismatches, HashMismatch{Line: ref.Line, Expected: ref.Hash, Actual: actual})
		}
	}

	for _, edit := range edits {
		switch edit.Op {
		case "replace_range":
			if edit.Pos == nil || edit.End == nil {
				return "", fmt.Errorf("replace_range requires both pos and end anchors")
			}
			if edit.Pos.Line > edit.End.Line {
				return "", fmt.Errorf("range start line %d must be <= end line %d", edit.Pos.Line, edit.End.Line)
			}
			validateRef(edit.Pos)
			validateRef(edit.End)
		case "append_at", "prepend_at":
			if edit.Pos == nil {
				return "", fmt.Errorf("%s requires pos anchor", edit.Op)
			}
			validateRef(edit.Pos)
		case "append_file", "prepend_file":
			// No anchor needed.
		default:
			return "", fmt.Errorf("unknown edit operation %q", edit.Op)
		}

		// Range check.
		if edit.Pos != nil && (edit.Pos.Line < 1 || edit.Pos.Line > len(fileLines)) {
			return "", fmt.Errorf("line %d does not exist (file has %d lines)", edit.Pos.Line, len(fileLines))
		}
		if edit.End != nil && (edit.End.Line < 1 || edit.End.Line > len(fileLines)) {
			return "", fmt.Errorf("line %d does not exist (file has %d lines)", edit.End.Line, len(fileLines))
		}
	}

	if len(mismatches) > 0 {
		return "", &HashMismatchError{Mismatches: mismatches, FileLines: fileLines}
	}

	// Compute sort key for bottom-up application.
	type annotated struct {
		edit      Edit
		sortLine  int
		precedence int // lower = applied first at same line
	}
	anns := make([]annotated, len(edits))
	for i, e := range edits {
		var sortLine, prec int
		switch e.Op {
		case "replace_range":
			sortLine = e.End.Line
			prec = 0
		case "append_at":
			sortLine = e.Pos.Line
			prec = 1
		case "prepend_at":
			sortLine = e.Pos.Line
			prec = 2
		case "append_file":
			sortLine = len(fileLines) + 1
			prec = 1
		case "prepend_file":
			sortLine = 0
			prec = 2
		}
		anns[i] = annotated{edit: e, sortLine: sortLine, precedence: prec}
	}

	slices.SortStableFunc(anns, func(a, b annotated) int {
		if a.sortLine != b.sortLine {
			return b.sortLine - a.sortLine // descending
		}
		return a.precedence - b.precedence
	})

	// Apply bottom-up.
	for _, ann := range anns {
		e := ann.edit
		lines := e.Content
		if lines == nil {
			lines = []string{} // delete = replace with nothing
		}

		switch e.Op {
		case "replace_range":
			start := e.Pos.Line - 1 // 0-indexed
			count := e.End.Line - e.Pos.Line + 1
			fileLines = slices.Replace(fileLines, start, start+count, lines...)
		case "append_at":
			fileLines = slices.Insert(fileLines, e.Pos.Line, lines...)
		case "prepend_at":
			fileLines = slices.Insert(fileLines, e.Pos.Line-1, lines...)
		case "append_file":
			fileLines = append(fileLines, lines...)
		case "prepend_file":
			fileLines = slices.Insert(fileLines, 0, lines...)
		}
	}

	return strings.Join(fileLines, "\n"), nil
}
