package claudetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
)

// ReadContextFileTool reads files from the orchestrator's shared context directory.
type ReadContextFileTool struct {
	ContextDir string
}

func (t *ReadContextFileTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        "read_context_file",
		Description: readContextFileDescription,
		InputSchema: llm.MustSchema(readContextFileInputSchema),
		Run:         llm.RunJSON(t.run),
	}
}

const readContextFileDescription = `Read a file from the shared context directory.

The context directory is used to pass information between subagents.
Subagents write their findings and outputs as files here, and the
orchestrator can read them to track progress and share context.

Only files within the context directory can be read.`

const readContextFileInputSchema = `{
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": {
      "type": "string",
      "description": "Relative path within the context directory (e.g., 'analysis.md', 'results/output.txt')"
    }
  }
}`

type readContextFileInput struct {
	Path string `json:"path"`
}

func (t *ReadContextFileTool) run(ctx context.Context, input readContextFileInput) llm.ToolOut {
	if input.Path == "" {
		return llm.ErrorfToolOut("path is required")
	}

	// Resolve and validate the path is within the context directory
	fullPath := filepath.Join(t.ContextDir, filepath.Clean(input.Path))
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return llm.ErrorfToolOut("invalid path: %v", err)
	}
	// Resolve symlinks to prevent escaping the context directory
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	absContextDir, err := filepath.Abs(t.ContextDir)
	if err != nil {
		return llm.ErrorfToolOut("invalid context directory: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(absContextDir); err == nil {
		absContextDir = resolved
	}
	if !strings.HasPrefix(absPath, absContextDir+string(filepath.Separator)) && absPath != absContextDir {
		return llm.ErrorfToolOut("path must be within the context directory")
	}

	// Check if it's a directory — list contents
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return llm.ErrorfToolOut("file not found: %s", input.Path)
		}
		return llm.ErrorfToolOut("failed to stat path: %v", err)
	}

	if info.IsDir() {
		entries, err := os.ReadDir(absPath)
		if err != nil {
			return llm.ErrorfToolOut("failed to list directory: %v", err)
		}
		var listing strings.Builder
		fmt.Fprintf(&listing, "Directory listing for %s:\n", input.Path)
		for _, entry := range entries {
			if entry.IsDir() {
				fmt.Fprintf(&listing, "  %s/\n", entry.Name())
			} else {
				fmt.Fprintf(&listing, "  %s\n", entry.Name())
			}
		}
		if len(entries) == 0 {
			listing.WriteString("  (empty)\n")
		}
		return llm.ToolOut{LLMContent: llm.TextContent(listing.String())}
	}

	// Read file content
	data, err := os.ReadFile(absPath)
	if err != nil {
		return llm.ErrorfToolOut("failed to read file: %v", err)
	}

	// Limit to 100KB
	if len(data) > 100*1024 {
		return llm.ToolOut{
			LLMContent: llm.TextContent(fmt.Sprintf("File %s (truncated to 100KB):\n%s", input.Path, string(data[:100*1024]))),
		}
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("File %s:\n%s", input.Path, string(data))),
	}
}
