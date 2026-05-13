package claudetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
)

// tildeReplace replaces the home directory prefix with ~ for display.
func tildeReplace(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// ChangeDirTool changes the working directory for bash commands.
type ChangeDirTool struct {
	// WorkingDir is the shared mutable working directory.
	WorkingDir *MutableWorkingDir
	// OnChange is called after the working directory changes successfully.
	// This can be used to persist the change to a database.
	OnChange func(newDir string)
}

const (
	changeDirName        = "change_dir"
	changeDirDescription = `Change the working directory for subsequent bash commands.

This affects the working directory used by the bash tool. The directory must exist.
Relative paths are resolved against the current working directory.

Prefer this tool over 'cd <path> && ...' in bash: 'cd' inside a bash
invocation does not persist, so you'd have to repeat it every call. Call
change_dir once, then run subsequent commands directly.
`
	changeDirInputSchema = `{
  "type": "object",
  "required": ["path"],
  "properties": {
    "path": {
      "type": "string",
      "description": "The directory path to change to (absolute or relative)"
    }
  }
}`
)

type changeDirInput struct {
	Path string `json:"path"`
}

// Tool returns an llm.Tool for changing directories.
func (c *ChangeDirTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        changeDirName,
		Description: changeDirDescription,
		InputSchema: llm.MustSchema(changeDirInputSchema),
		Run:         llm.RunJSON(c.run),
	}
}

// run executes the change_dir tool.
func (c *ChangeDirTool) run(ctx context.Context, req changeDirInput) llm.ToolOut {
	if req.Path == "" {
		return llm.ErrorfToolOut("path is required")
	}

	// Get current working directory
	currentWD := c.WorkingDir.Get()

	// Resolve the path
	targetPath := req.Path
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(currentWD, targetPath)
	}
	targetPath = filepath.Clean(targetPath)

	// Validate the directory exists
	info, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return llm.ErrorfToolOut("directory does not exist: %s", targetPath)
		}
		return llm.ErrorfToolOut("failed to stat path: %w", err)
	}
	if !info.IsDir() {
		return llm.ErrorfToolOut("path is not a directory: %s", targetPath)
	}

	// Update the working directory
	c.WorkingDir.Set(targetPath)

	// Notify callback if set
	if c.OnChange != nil {
		c.OnChange(targetPath)
	}

	// Check git status for the new directory
	state := gitstate.GetGitState(targetPath)
	var resultText string
	if state.IsRepo {
		resultText = fmt.Sprintf("Changed working directory to: %s\n\nGit repository detected (root: %s, branch: %s)", targetPath, tildeReplace(state.Worktree), state.Branch)
		if state.Branch == "" {
			resultText = fmt.Sprintf("Changed working directory to: %s\n\nGit repository detected (root: %s, detached HEAD)", targetPath, tildeReplace(state.Worktree))
		}
	} else {
		resultText = fmt.Sprintf("Changed working directory to: %s\n\nNot in a git repository.", targetPath)
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(resultText),
	}
}
