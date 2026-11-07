package claudetool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

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
	// VM, if set, is where the conversation's project work happens; WorkingDir
	// is then on it, and each call names its target (see VM).
	VM VM
	// HostDir, with a VM, is the working directory here. It is the
	// conversation's scratch space, and moving it isn't persisted.
	HostDir *MutableWorkingDir
	mu      sync.Mutex
}

const (
	changeDirName        = "change_dir"
	changeDirDescription = `Change the working directory for subsequent tool calls.
The directory must exist; relative paths resolve against the current directory.
Within one response, change_dir separates sequential groups of parallel calls.
If it fails, dependent calls are skipped until an absolute change_dir succeeds.
Call this before inspecting, testing, or editing in another directory.
Omit redundant cd when already in the target directory.
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
	Path      string  `json:"path"`
	SSHTarget *string `json:"ssh_target,omitempty"`
}

// Tool returns an llm.Tool for changing directories.
func (c *ChangeDirTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        changeDirName,
		Description: changeDirDescription,
		InputSchema: llm.MustSchema(targetSchema(changeDirInputSchema, c.VM)),
		Run:         llm.RunJSON(c.run),
	}
}

// run executes the change_dir tool.
func (c *ChangeDirTool) run(ctx context.Context, req changeDirInput) llm.ToolOut {
	c.mu.Lock()
	defer c.mu.Unlock()

	if req.Path == "" {
		return llm.ErrorfToolOut("path is required")
	}

	vm, err := targetVM(c.VM, req.SSHTarget)
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	wd := c.WorkingDir
	if vm == nil && c.HostDir != nil {
		wd = c.HostDir
	}

	// Resolve the path
	targetPath := resolvePath(wd.Get(), req.Path)
	if vm != nil {
		return c.changeVMDir(ctx, vm, targetPath)
	}

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

	wd.Set(targetPath)
	// With a VM, the conversation's directory is there: this one is scratch.
	if c.OnChange != nil && wd == c.WorkingDir {
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

// vmChangeDirScript checks that $1 is a directory, exiting 44 if it is
// missing and 45 if it is not a directory, and prints the root and branch of
// the git repository it is in, if any.
const vmChangeDirScript = `[ -e "$1" ] || exit 44
[ -d "$1" ] || exit 45
cd -- "$1" || exit 1
root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
printf '%s\n%s\n' "$root" "$(git symbolic-ref --short -q HEAD)"`

// changeVMDir moves the working directory on vm, which WorkingDir is on.
func (c *ChangeDirTool) changeVMDir(ctx context.Context, vm VM, targetPath string) llm.ToolOut {
	if err := vm.Prepare(ctx); err != nil {
		return llm.ErrorToolOut(err)
	}
	argv := vm.Command(vmChangeDirScript, targetPath)
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr) && exitErr.ExitCode() == vmMissing:
		return llm.ErrorfToolOut("directory does not exist on %s: %s", vm.Name(), targetPath)
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 45:
		return llm.ErrorfToolOut("path is not a directory on %s: %s", vm.Name(), targetPath)
	case err != nil:
		return llm.ErrorfToolOut("failed to check %s on %s: %v: %s", targetPath, vm.Name(), err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	c.WorkingDir.Set(targetPath)
	if c.OnChange != nil {
		c.OnChange(targetPath)
	}
	resultText := fmt.Sprintf("Changed working directory on %s to: %s\n\nNot in a git repository.", vm.Name(), targetPath)
	if len(lines) == 2 {
		root, branch := lines[0], lines[1]
		resultText = fmt.Sprintf("Changed working directory on %s to: %s\n\nGit repository detected (root: %s, branch: %s)", vm.Name(), targetPath, root, branch)
		if branch == "" {
			resultText = fmt.Sprintf("Changed working directory on %s to: %s\n\nGit repository detected (root: %s, detached HEAD)", vm.Name(), targetPath, root)
		}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(resultText)}
}
