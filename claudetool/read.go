package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/claudetool/hashline"
)

const ReadName = "read"

type ReadTool struct {
	WorkingDir *MutableWorkingDir
}

func (r *ReadTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        ReadName,
		Description: readDescription,
		InputSchema: llm.MustSchema(readInputSchema),
		Run:         r.Run,
	}
}

const readDescription = `Read a file with LINE#ID anchors for use with the edit tool.

Each output line is prefixed with a stable anchor: LINE#HASH:content
Use these anchors when making edits. The hash validates the line hasn't changed.

Prefer this over cat/bash for files you intend to edit.
`

const readInputSchema = `{
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the file to read"
    },
    "startLine": {
      "type": "integer",
      "description": "First line to include (1-indexed, default: 1)"
    },
    "endLine": {
      "type": "integer",
      "description": "Last line to include (1-indexed, default: end of file)"
    }
  }
}`

type readInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
}

func (r *ReadTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input readInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorToolOut(err)
	}

	path := input.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.WorkingDir.Get(), path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read %q: %v", path, err)
	}

	text := string(data)
	allLines := strings.Split(text, "\n")

	startLine := 1
	endLine := len(allLines)
	if input.StartLine > 0 {
		startLine = input.StartLine
	}
	if input.EndLine > 0 {
		endLine = input.EndLine
	}
	if startLine > len(allLines) {
		return llm.ErrorfToolOut("startLine %d exceeds file length (%d lines)", startLine, len(allLines))
	}
	if endLine > len(allLines) {
		endLine = len(allLines)
	}
	if startLine > endLine {
		return llm.ErrorfToolOut("startLine %d > endLine %d", startLine, endLine)
	}

	subset := strings.Join(allLines[startLine-1:endLine], "\n")
	formatted := hashline.FormatHashLines(subset, startLine)

	var header strings.Builder
	fmt.Fprintf(&header, "file: %s\n", path)
	if startLine != 1 || endLine != len(allLines) {
		fmt.Fprintf(&header, "lines: %d-%d of %d\n", startLine, endLine, len(allLines))
	} else {
		fmt.Fprintf(&header, "lines: %d total\n", len(allLines))
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(header.String() + formatted),
	}
}
