package claudetool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pkg/diff"
	"shelley.exe.dev/llm"
	"sketch.dev/claudetool/editbuf"
	"sketch.dev/claudetool/patchkit"
)

// PatchCallback defines the signature for patch tool callbacks.
// It runs after the patch tool has executed.
// It receives the patch input and the tool output,
// and returns a new, possibly altered tool output.
type PatchCallback func(input PatchInput, output llm.ToolOut) llm.ToolOut

type patchFileIdentity struct {
	info os.FileInfo
}

type patchPathLock struct {
	path     string
	identity atomic.Pointer[patchFileIdentity]
	mu       sync.Mutex
}

// PatchTool specifies an llm.Tool for patching files.
type PatchTool struct {
	Callback PatchCallback // may be nil; must support concurrent calls
	Provider string
	Profile  string
	// WorkingDir is the shared mutable working directory.
	WorkingDir     *MutableWorkingDir
	clipboardMu    sync.RWMutex
	clipboards     map[string]string
	clipboardLocks sync.Map // map[string]*sync.Mutex
	pathLocksMu    sync.Mutex
	pathLocks      []*patchPathLock
}

func canonicalPatchPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(parent, filepath.Base(path))
	}
	return path
}

func (p *PatchTool) lockPath(path string) func() {
	path = canonicalPatchPath(path)
	info, _ := os.Stat(path)

	p.pathLocksMu.Lock()
	var pathLock *patchPathLock
	for _, candidate := range p.pathLocks {
		identity := candidate.identity.Load()
		if candidate.path == path || (info != nil && identity != nil && os.SameFile(info, identity.info)) {
			pathLock = candidate
			break
		}
	}
	if pathLock == nil {
		pathLock = &patchPathLock{path: path}
		if info != nil {
			pathLock.identity.Store(&patchFileIdentity{info: info})
		}
		p.pathLocks = append(p.pathLocks, pathLock)
	}
	p.pathLocksMu.Unlock()

	pathLock.mu.Lock()
	return func() {
		if info, err := os.Stat(path); err == nil {
			pathLock.identity.Store(&patchFileIdentity{info: info})
		}
		pathLock.mu.Unlock()
	}
}

func (p *PatchTool) lockClipboards(patches []PatchRequest) func() {
	namesSet := make(map[string]bool)
	for _, patch := range patches {
		if patch.ToClipboard != "" {
			namesSet[patch.ToClipboard] = true
		}
		if patch.FromClipboard != "" {
			namesSet[patch.FromClipboard] = true
		}
	}
	names := make([]string, 0, len(namesSet))
	for name := range namesSet {
		names = append(names, name)
	}
	sort.Strings(names)

	locks := make([]*sync.Mutex, 0, len(names))
	for _, name := range names {
		lockValue, _ := p.clipboardLocks.LoadOrStore(name, &sync.Mutex{})
		lock := lockValue.(*sync.Mutex)
		lock.Lock()
		locks = append(locks, lock)
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

func (p *PatchTool) setClipboard(name, text string) {
	p.clipboardMu.Lock()
	defer p.clipboardMu.Unlock()
	if p.clipboards == nil {
		p.clipboards = make(map[string]string)
	}
	p.clipboards[name] = text
}

func (p *PatchTool) getClipboard(name string) (string, bool) {
	p.clipboardMu.RLock()
	defer p.clipboardMu.RUnlock()
	text, ok := p.clipboards[name]
	return text, ok
}

// getWorkingDir returns the current working directory.
func (p *PatchTool) getWorkingDir() string {
	return p.WorkingDir.Get()
}

// Tool returns an llm.Tool based on p.
func (p *PatchTool) Tool() *llm.Tool {
	if p.Profile == "codex_apply_patch" {
		return p.applyPatchTool()
	}
	schema := PatchNestedInputSchema
	description := strings.TrimSpace(PatchBaseDescription + PatchUsageNotes)
	if p.Profile == "simple" {
		schema = PatchSimpleInputSchema
		description = PatchSimpleDescription
	}
	return &llm.Tool{
		Name:        PatchName,
		Description: description,
		InputSchema: llm.MustSchema(schema),
		Run:         p.Run,
	}
}

const (
	PatchName            = "patch"
	PatchBaseDescription = `
File modification tool for precise text edits.

Operations:
- replace: Substitute unique text with new content
- append_eof: Append new text at the end of the file
- prepend_bof: Insert new text at the beginning of the file
- overwrite: Replace the entire file with new content (automatically creates the file)
`

	PatchClipboardDescription = `
Clipboard:
- toClipboard: Store oldText to a named clipboard before the operation
- fromClipboard: Use clipboard content as newText (ignores provided newText)
- Clipboards persist across patch calls
- Always use clipboards when moving/copying code (within or across files), even when the moved/copied code will also have edits.
  This prevents transcription errors and distinguishes intentional changes from unintentional changes.

Indentation adjustment:
- reindent applies to whatever text is being inserted
- First strips the specified prefix from each line, then adds the new prefix
- Useful when moving code from one indentation to another

Recipes:
- cut: replace with empty newText and toClipboard
- copy: replace with toClipboard and fromClipboard using the same clipboard name
- paste: replace with fromClipboard
- in-place indentation change: same as copy, but add indentation adjustment
`

	ApplyPatchName        = "apply_patch"
	ApplyPatchDescription = `The apply_patch tool edits files using the Codex patch format. This is a FREEFORM tool: send only an envelope beginning with "*** Begin Patch" and ending with "*** End Patch"; do not wrap it in JSON.
Use "*** Add File: path" with every content line prefixed "+", "*** Delete File: path", or "*** Update File: path" with hunk lines prefixed by exactly one " " (context), "-" (remove), or "+" (add). Context text after its one-character marker must match the file verbatim, including leading spaces and tabs.
For every update hunk, include enough unchanged context to identify one location—up to 3 unchanged lines before and after the edit when available, unless fewer lines already include a unique structural anchor such as a function declaration, type name, CSS selector, or test name. Do not patch using only a short repeated fragment.
The patch is validated as a unit: a parse or match failure rejects the entire patch without changing files. For "matched 0 locations," reread the current file and retry with exact current context; do not trim or normalize whitespace.`
	ApplyPatchGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?
hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change?
filename: /(.+)/
add_line: "+" /(.*)/ LF
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF
%import common.LF`

	PatchUsageNotes = `
Usage notes:
- All inputs are interpreted literally (no automatic newline or whitespace handling)
- For replace operations, oldText must appear EXACTLY ONCE in the file

IMPORTANT: Each patch call must be less than 60k tokens total. For large file
changes, break them into multiple smaller patch operations rather than one
large overwrite. Prefer incremental replace operations over full file overwrites.
`

	PatchNestedInputSchema = `
{
  "type": "object",
  "additionalProperties": false,
  "required": ["path", "patches"],
  "properties": {
    "path": {"type": "string", "description": "Path to the file to patch"},
    "patches": {
      "type": "array",
      "minItems": 1,
      "description": "Patch operations to apply in order",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["operation", "newText"],
        "properties": {
          "operation": {"type": "string", "enum": ["replace", "append_eof", "prepend_bof", "overwrite"]},
          "oldText": {"type": "string"},
          "newText": {"type": "string"}
        }
      }
    }
  }
}
`

	PatchSimpleDescription = `Edit one file with precise text replacements and optional content appended at EOF.

Each oldText must match exactly once in the original file. Edits must not overlap
or depend on text produced by another edit in the same call. Append does not need
an oldText anchor. The file is written only after every edit validates.`

	PatchSimpleInputSchema = `
{
  "type": "object",
  "additionalProperties": false,
  "required": ["path"],
  "properties": {
    "path": {"type": "string", "description": "Path to the file to edit"},
    "edits": {
      "type": "array",
      "description": "Non-overlapping replacements matched against the original file",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["oldText", "newText"],
        "properties": {
          "oldText": {"type": "string", "description": "Exact text to replace; must be unique in the original file"},
          "newText": {"type": "string", "description": "Replacement text"}
        }
      }
    },
    "append": {"type": "string", "description": "Content to append at the end of the file"}
  }
}
`
)

// TODO: maybe rename PatchRequest to PatchOperation or PatchSpec or PatchPart or just Patch?

// PatchInput represents the input structure for patch operations.
type PatchInput struct {
	Path    string         `json:"path"`
	Patches []PatchRequest `json:"patches"`
}

type PatchSimpleInput struct {
	Path   string            `json:"path"`
	Edits  []PatchSimpleEdit `json:"edits"`
	Append *string           `json:"append"`
}

type PatchSimpleEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// PatchDisplayData is the structured data sent to the UI for display.
type PatchDisplayData struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

type applyPatchInput struct {
	Input string `json:"input"`
}

type applyPatchFile struct {
	operation string
	path      string
	patches   []PatchRequest
}

type applyPatchMutation struct {
	path    string
	old     []byte
	new     []byte
	mode    os.FileMode
	created bool
	deleted bool
}

// PatchRequest represents a single patch operation.
type PatchRequest struct {
	Operation     string    `json:"operation"`
	OldText       string    `json:"oldText,omitempty"`
	NewText       string    `json:"newText,omitempty"`
	ToClipboard   string    `json:"toClipboard,omitempty"`
	FromClipboard string    `json:"fromClipboard,omitempty"`
	Reindent      *Reindent `json:"reindent,omitempty"`
}

// Reindent represents indentation adjustment configuration.
type Reindent struct {
	// TODO: it might be nice to make this more flexible,
	// so it can e.g. strip all whitespace,
	// or strip the prefix only on lines where it is present,
	// or strip based on a regex.
	Strip string `json:"strip,omitempty"`
	Add   string `json:"add,omitempty"`
}

// Run implements the patch tool logic.
func (p *PatchTool) applyPatchTool() *llm.Tool {
	return &llm.Tool{
		Name:          ApplyPatchName,
		Description:   ApplyPatchDescription,
		InputSchema:   llm.MustSchema(`{"type":"object","required":["input"],"properties":{"input":{"type":"string"}}}`),
		CustomGrammar: ApplyPatchGrammar,
		Run: func(ctx context.Context, raw json.RawMessage) llm.ToolOut {
			var input applyPatchInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return llm.ErrorfToolOut("invalid apply_patch input: %w", err)
			}
			return p.runApplyPatch(ctx, input.Input)
		},
	}
}

func (p *PatchTool) Run(ctx context.Context, m json.RawMessage) llm.ToolOut {
	input, err := p.patchParse(m)
	if err != nil {
		p.logResult(ctx, "invalid_input", err)
		return llm.ErrorToolOut(err)
	}
	output := p.runInput(ctx, input)
	if output.Error == nil {
		p.logResult(ctx, "success", nil)
	} else {
		p.logResult(ctx, classifyPatchError(output.Error), output.Error)
	}
	return output
}

func (p *PatchTool) logResult(ctx context.Context, result string, err error) {
	attrs := []any{"result", result, "provider", p.Provider, "profile", p.Profile}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.InfoContext(ctx, "patch_tool_result", attrs...)
}

func classifyPatchError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "old text not found"), strings.Contains(message, "matched 0 locations"):
		return "old_text_not_found"
	case strings.Contains(message, "old text not unique"), strings.Contains(message, "matched ") && strings.Contains(message, " locations at lines "):
		return "old_text_not_unique"
	case strings.Contains(message, "does not exist"):
		return "path_not_found"
	case strings.Contains(message, "failed to read file"):
		return "path_read_failed"
	default:
		return "execution_failed"
	}
}

func (p *PatchTool) runApplyPatch(ctx context.Context, text string) llm.ToolOut {
	files, err := parseApplyPatch(text)
	if err != nil {
		p.logResult(ctx, "invalid_input", err)
		return llm.ErrorToolOut(err)
	}

	pathSet := make(map[string]struct{}, len(files))
	for _, file := range files {
		path := file.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.getWorkingDir(), path)
		}
		pathSet[filepath.Clean(path)] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	// Acquire all locks in a stable order so validation and commit see one
	// consistent snapshot without deadlocking concurrent multi-file patches.
	sort.Strings(paths)
	unlocks := make([]func(), 0, len(paths))
	for _, path := range paths {
		unlocks = append(unlocks, p.lockPath(path))
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()

	mutations := make([]applyPatchMutation, 0, len(files))
	for _, file := range files {
		path := file.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.getWorkingDir(), path)
		}
		path = filepath.Clean(path)
		old, readErr := os.ReadFile(path)
		mutation := applyPatchMutation{path: path, old: old, mode: 0o600}
		if info, statErr := os.Stat(path); statErr == nil {
			mutation.mode = info.Mode().Perm()
		}

		switch file.operation {
		case "overwrite":
			if readErr == nil {
				err = fmt.Errorf("file %q already exists", path)
			} else if !errors.Is(readErr, os.ErrNotExist) {
				err = readErr
			} else {
				mutation.created = true
				mutation.new = []byte(file.patches[0].NewText)
			}
		case "delete":
			if readErr != nil {
				err = readErr
			} else {
				mutation.deleted = true
			}
		case "replace":
			if readErr != nil {
				err = readErr
				break
			}
			content := string(old)
			for _, request := range file.patches {
				spec, count := patchkit.Unique(content, request.OldText, request.NewText)
				if count != 1 {
					err = applyPatchMatchError(path, content, request.OldText)
					break
				}
				content = content[:spec.Off] + request.NewText + content[spec.Off+spec.Len:]
			}
			mutation.new = []byte(content)
		}
		if err != nil {
			p.logResult(ctx, "execution_failed", err)
			return llm.ErrorToolOut(err)
		}
		mutations = append(mutations, mutation)
	}

	// Apply from validated snapshots. Roll back earlier writes if an OS error
	// occurs so a multi-file patch is atomic from the agent's perspective.
	for i, mutation := range mutations {
		if mutation.deleted {
			err = os.Remove(mutation.path)
		} else {
			err = os.MkdirAll(filepath.Dir(mutation.path), 0o700)
			if err == nil {
				err = os.WriteFile(mutation.path, mutation.new, mutation.mode)
			}
		}
		if err != nil {
			for j := i - 1; j >= 0; j-- {
				prior := mutations[j]
				if prior.created {
					_ = os.Remove(prior.path)
				} else {
					_ = os.WriteFile(prior.path, prior.old, prior.mode)
				}
			}
			p.logResult(ctx, "execution_failed", err)
			return llm.ErrorToolOut(err)
		}
	}

	var diff strings.Builder
	for _, mutation := range mutations {
		diff.WriteString(generateUnifiedDiff(mutation.path, string(mutation.old), string(mutation.new)))
	}
	p.logResult(ctx, "success", nil)
	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("Applied patch to %d file(s).", len(mutations))),
		Display:    PatchDisplayData{Path: applyPatchDisplayPath(mutations), Diff: diff.String()},
	}
}

func applyPatchMatchError(path, content, oldText string) error {
	lines := exactMatchLines(content, oldText)
	if len(lines) == 0 {
		return fmt.Errorf("apply_patch update for %q matched 0 locations\n\nThe context must match exactly, including whitespace. Reread the current file and retry with context copied from it.\n\nNo files were changed", path)
	}

	lineText := make([]string, len(lines))
	for i, line := range lines {
		lineText[i] = strconv.Itoa(line)
	}
	return fmt.Errorf("apply_patch update for %q matched %d locations at lines %s\n\nInclude more surrounding unchanged lines so the context identifies one location.\n\nNo files were changed", path, len(lines), strings.Join(lineText, ", "))
}

func exactMatchLines(content, oldText string) []int {
	if oldText == "" {
		return nil
	}

	var lines []int
	for offset := 0; ; {
		match := strings.Index(content[offset:], oldText)
		if match < 0 {
			return lines
		}
		match += offset
		lines = append(lines, 1+strings.Count(content[:match], "\n"))
		offset = match + len(oldText)
	}
}

func applyPatchDisplayPath(mutations []applyPatchMutation) string {
	if len(mutations) == 1 {
		return mutations[0].path
	}
	return "multiple files"
}

func parseApplyPatch(text string) ([]applyPatchFile, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "*** Begin Patch" || lines[len(lines)-1] != "*** End Patch" {
		return nil, fmt.Errorf("apply_patch must start with '*** Begin Patch' and end with '*** End Patch'")
	}
	var files []applyPatchFile
	for i := 1; i < len(lines)-1; {
		line := lines[i]
		i++
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimPrefix(line, "*** Add File: ")
			if path == "" {
				return nil, fmt.Errorf("apply_patch add path is required")
			}
			var content []string
			for i < len(lines)-1 && !strings.HasPrefix(lines[i], "*** ") {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("add-file lines must start with '+'")
				}
				content = append(content, strings.TrimPrefix(lines[i], "+"))
				i++
			}
			if len(content) == 0 {
				return nil, fmt.Errorf("add-file hunk for %q is empty", path)
			}
			files = append(files, applyPatchFile{operation: "overwrite", path: path, patches: []PatchRequest{{Operation: "overwrite", NewText: strings.Join(content, "\n") + "\n"}}})
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimPrefix(line, "*** Delete File: ")
			if path == "" {
				return nil, fmt.Errorf("apply_patch delete path is required")
			}
			files = append(files, applyPatchFile{operation: "delete", path: path, patches: []PatchRequest{{Operation: "delete"}}})
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimPrefix(line, "*** Update File: ")
			if path == "" {
				return nil, fmt.Errorf("apply_patch update path is required")
			}
			var patches []PatchRequest
			for i < len(lines)-1 && !strings.HasPrefix(lines[i], "*** ") {
				if strings.HasPrefix(lines[i], "@@") {
					i++
				}
				var oldLines, newLines []string
				for i < len(lines)-1 && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "*** ") {
					switch {
					case strings.HasPrefix(lines[i], " "):
						value := strings.TrimPrefix(lines[i], " ")
						oldLines, newLines = append(oldLines, value), append(newLines, value)
					case strings.HasPrefix(lines[i], "-"):
						oldLines = append(oldLines, strings.TrimPrefix(lines[i], "-"))
					case strings.HasPrefix(lines[i], "+"):
						newLines = append(newLines, strings.TrimPrefix(lines[i], "+"))
					default:
						return nil, fmt.Errorf("update lines must start with ' ', '+', or '-'")
					}
					i++
				}
				if len(oldLines) == 0 {
					return nil, fmt.Errorf("update hunk for %q has no old lines", path)
				}
				patches = append(patches, PatchRequest{Operation: "replace", OldText: strings.Join(oldLines, "\n"), NewText: strings.Join(newLines, "\n")})
			}
			if len(patches) == 0 {
				return nil, fmt.Errorf("update hunk for %q is empty", path)
			}
			files = append(files, applyPatchFile{operation: "replace", path: path, patches: patches})
		default:
			return nil, fmt.Errorf("invalid apply_patch hunk header %q", line)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("apply_patch contains no file hunks")
	}
	return files, nil
}

func (p *PatchTool) runInput(ctx context.Context, input PatchInput) llm.ToolOut {
	if !filepath.IsAbs(input.Path) {
		input.Path = filepath.Join(p.getWorkingDir(), input.Path)
	}
	input.Path = filepath.Clean(input.Path)
	unlockClipboards := p.lockClipboards(input.Patches)
	defer unlockClipboards()
	unlockPath := p.lockPath(input.Path)
	defer unlockPath()
	output := p.patchRun(ctx, &input)
	if p.Callback != nil {
		return p.Callback(input, output)
	}
	return output
}

func (p *PatchTool) patchParse(m json.RawMessage) (PatchInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(m))
	decoder.DisallowUnknownFields()
	if p.Profile == "simple" {
		var simple PatchSimpleInput
		if err := decoder.Decode(&simple); err != nil {
			return PatchInput{}, fmt.Errorf("invalid patch input: %w; expected path, edits, and append fields", err)
		}
		patches := make([]PatchRequest, len(simple.Edits), len(simple.Edits)+1)
		for i, edit := range simple.Edits {
			patches[i] = PatchRequest{Operation: "replace", OldText: edit.OldText, NewText: edit.NewText}
		}
		if simple.Append != nil {
			patches = append(patches, PatchRequest{Operation: "append_eof", NewText: *simple.Append})
		}
		return validatePatchInput(PatchInput{Path: simple.Path, Patches: patches})
	}
	var nested PatchInput
	if err := decoder.Decode(&nested); err != nil {
		return PatchInput{}, fmt.Errorf("invalid patch input: %w; expected path and patches fields", err)
	}
	return validatePatchInput(nested)
}

func validatePatchInput(input PatchInput) (PatchInput, error) {
	if strings.TrimSpace(input.Path) == "" {
		return PatchInput{}, fmt.Errorf("patch path is required")
	}
	for i, patch := range input.Patches {
		switch patch.Operation {
		case "replace":
			if patch.OldText == "" {
				return PatchInput{}, fmt.Errorf("patch %d: oldText is required for replace operation", i)
			}
		case "append_eof", "prepend_bof", "overwrite":
		case "":
			return PatchInput{}, fmt.Errorf("patch %d: operation is required", i)
		default:
			return PatchInput{}, fmt.Errorf("patch %d: unrecognized operation %q", i, patch.Operation)
		}
	}
	return input, nil
}

// patchRun implements the guts of the patch tool.
// It populates input from m.
func (p *PatchTool) patchRun(ctx context.Context, input *PatchInput) llm.ToolOut {
	if len(input.Patches) == 0 {
		return llm.ErrorToolOut(fmt.Errorf("no patches provided"))
	}
	// TODO: check whether the file is autogenerated, and if so, require a "force" flag to modify it.

	orig, err := os.ReadFile(input.Path)
	// If the file doesn't exist, we can still apply patches
	// that don't require finding existing text.
	switch {
	case errors.Is(err, os.ErrNotExist):
		for _, patch := range input.Patches {
			switch patch.Operation {
			case "prepend_bof", "append_eof", "overwrite":
			default:
				return llm.ErrorfToolOut("file %q does not exist", input.Path)
			}
		}
	case err != nil:
		return llm.ErrorfToolOut("failed to read file %q: %w", input.Path, err)
	}

	likelyGoFile := strings.HasSuffix(input.Path, ".go")

	autogenerated := likelyGoFile && IsAutogeneratedGoFile(orig)

	origStr := string(orig)
	// Process the patches "simultaneously", minimizing them along the way.
	// Claude generates patches that interact with each other.
	buf := editbuf.NewBuffer(orig)

	// TODO: is it better to apply the patches that apply cleanly and report on the failures?
	// or instead have it be all-or-nothing?
	// For now, it is all-or-nothing.
	// TODO: when the model gets into a "cannot apply patch" cycle of doom, how do we get it unstuck?
	// Also: how do we detect that it's in a cycle?
	var patchErr error

	var clipboardsModified []string
	updateToClipboard := func(patch PatchRequest, spec *patchkit.Spec) {
		if patch.ToClipboard == "" {
			return
		}
		// Update clipboard with the actual matched text
		matchedOldText := origStr[spec.Off : spec.Off+spec.Len]
		p.setClipboard(patch.ToClipboard, matchedOldText)
		clipboardsModified = append(clipboardsModified, fmt.Sprintf(`<clipboard_modified name="%s"><message>clipboard contents altered in order to match uniquely</message><new_contents>%q</new_contents></clipboard_modified>`, patch.ToClipboard, matchedOldText))
	}

	for i, patch := range input.Patches {
		// Process toClipboard first, so that copy works
		if patch.ToClipboard != "" {
			if patch.Operation != "replace" {
				return llm.ErrorfToolOut("toClipboard (%s): can only be used with replace operation", patch.ToClipboard)
			}
			if patch.OldText == "" {
				return llm.ErrorfToolOut("toClipboard (%s): oldText cannot be empty when using toClipboard", patch.ToClipboard)
			}
			p.setClipboard(patch.ToClipboard, patch.OldText)
		}

		// Handle fromClipboard
		newText := patch.NewText
		if patch.FromClipboard != "" {
			clipboardText, ok := p.getClipboard(patch.FromClipboard)
			if !ok {
				return llm.ErrorfToolOut("fromClipboard (%s): no clipboard with that name", patch.FromClipboard)
			}
			newText = clipboardText
		}

		// Apply indentation adjustment if specified
		if patch.Reindent != nil {
			reindentedText, err := reindent(newText, patch.Reindent)
			if err != nil {
				return llm.ErrorfToolOut("reindent(%q -> %q): %w", patch.Reindent.Strip, patch.Reindent.Add, err)
			}
			newText = reindentedText
		}

		switch patch.Operation {
		case "prepend_bof":
			buf.Insert(0, newText)
		case "append_eof":
			buf.Insert(len(orig), newText)
		case "overwrite":
			buf.Replace(0, len(orig), newText)
		case "replace":
			if patch.OldText == "" {
				return llm.ErrorfToolOut("patch %d: oldText cannot be empty for %s operation", i, patch.Operation)
			}

			// Attempt to apply the patch.
			spec, count := patchkit.Unique(origStr, patch.OldText, newText)
			switch count {
			case 0:
				// no matches, maybe recoverable, continued below
			case 1:
				// exact match, apply
				slog.DebugContext(ctx, "patch_applied", "method", "unique")
				spec.ApplyToEditBuf(buf)
				continue
			case 2:
				// multiple matches
				patchErr = errors.Join(patchErr, fmt.Errorf("old text not unique:\n%s", patch.OldText))
				continue
			default:
				slog.ErrorContext(ctx, "unique returned unexpected count", "count", count)
				patchErr = errors.Join(patchErr, fmt.Errorf("internal error"))
				continue
			}

			// The following recovery mechanisms are heuristic.
			// They aren't perfect, but they appear safe,
			// and the cases they cover appear with some regularity.

			// Try adjusting the whitespace prefix.
			spec, ok := patchkit.UniqueDedent(origStr, patch.OldText, newText)
			if ok {
				slog.DebugContext(ctx, "patch_applied", "method", "unique_dedent")
				spec.ApplyToEditBuf(buf)
				updateToClipboard(patch, spec)
				continue
			}

			// Try ignoring leading/trailing whitespace in a semantically safe way.
			spec, ok = patchkit.UniqueInValidGo(origStr, patch.OldText, newText)
			if ok {
				slog.DebugContext(ctx, "patch_applied", "method", "unique_in_valid_go")
				spec.ApplyToEditBuf(buf)
				updateToClipboard(patch, spec)
				continue
			}

			// Try ignoring semantically insignificant whitespace.
			spec, ok = patchkit.UniqueGoTokens(origStr, patch.OldText, newText)
			if ok {
				slog.DebugContext(ctx, "patch_applied", "method", "unique_go_tokens")
				spec.ApplyToEditBuf(buf)
				updateToClipboard(patch, spec)
				continue
			}

			// Try trimming the first line of the patch, if we can do so safely.
			spec, ok = patchkit.UniqueTrim(origStr, patch.OldText, newText)
			if ok {
				slog.DebugContext(ctx, "patch_applied", "method", "unique_trim")
				spec.ApplyToEditBuf(buf)
				// Do NOT call updateToClipboard here,
				// because the trimmed text may vary significantly from the original text.
				continue
			}

			// No dice.
			patchErr = errors.Join(patchErr, fmt.Errorf("old text not found:\n%s", patch.OldText))
			continue
		default:
			return llm.ErrorfToolOut("unrecognized operation %q", patch.Operation)
		}
	}

	if patchErr != nil {
		errorMsg := patchErr.Error()
		for _, msg := range clipboardsModified {
			errorMsg += "\n" + msg
		}
		return llm.ErrorToolOut(fmt.Errorf("%s", errorMsg))
	}

	patched, err := buf.Bytes()
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	if err := os.MkdirAll(filepath.Dir(input.Path), 0o700); err != nil {
		return llm.ErrorfToolOut("failed to create directory %q: %w", filepath.Dir(input.Path), err)
	}
	if err := os.WriteFile(input.Path, patched, 0o600); err != nil {
		return llm.ErrorfToolOut("failed to write patched contents to file %q: %w", input.Path, err)
	}

	response := new(strings.Builder)
	fmt.Fprintf(response, "<patches_applied>all</patches_applied>\n")
	for _, msg := range clipboardsModified {
		fmt.Fprintln(response, msg)
	}

	if autogenerated {
		fmt.Fprintf(response, "<warning>%q appears to be autogenerated. Patches were applied anyway.</warning>\n", input.Path)
	}

	diff := generateUnifiedDiff(input.Path, string(orig), string(patched))

	// Display data for the UI includes the unified diff only.
	displayData := PatchDisplayData{
		Path: input.Path,
		Diff: diff,
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(response.String()),
		Display:    displayData,
	}
}

// IsAutogeneratedGoFile reports whether a Go file has markers indicating it was autogenerated.
func IsAutogeneratedGoFile(buf []byte) bool {
	for _, sig := range autogeneratedSignals {
		if bytes.Contains(buf, []byte(sig)) {
			return true
		}
	}

	// https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source
	// "This line must appear before the first non-comment, non-blank text in the file."
	// Approximate that by looking for it at the top of the file, before the last of the imports.
	// (Sometimes people put it after the package declaration, because of course they do.)
	// At least in the imports region we know it's not part of their actual code;
	// we don't want to ignore the generator (which also includes these strings!),
	// just the generated code.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", buf, parser.ImportsOnly|parser.ParseComments)
	if err == nil {
		for _, cg := range f.Comments {
			t := strings.ToLower(cg.Text())
			for _, sig := range autogeneratedHeaderSignals {
				if strings.Contains(t, sig) {
					return true
				}
			}
		}
	}

	return false
}

// autogeneratedSignals are signals that a file is autogenerated, when present anywhere in the file.
var autogeneratedSignals = [][]byte{
	[]byte("\nfunc bindataRead("), // pre-embed bindata packed file
}

// autogeneratedHeaderSignals are signals that a file is autogenerated, when present at the top of the file.
var autogeneratedHeaderSignals = []string{
	// canonical would be `(?m)^// Code generated .* DO NOT EDIT\.$`
	// but people screw it up, a lot, so be more lenient
	strings.ToLower("generate"),
	strings.ToLower("DO NOT EDIT"),
	strings.ToLower("export by"),
}

func generateUnifiedDiff(filePath, original, patched string) string {
	buf := new(strings.Builder)
	err := diff.Text(filePath, filePath, original, patched, buf)
	if err != nil {
		return fmt.Sprintf("(diff generation failed: %v)\n", err)
	}
	return buf.String()
}

// reindent applies indentation adjustments to text.
func reindent(text string, adj *Reindent) (string, error) {
	if adj == nil {
		return text, nil
	}

	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if line == "" {
			continue
		}
		var ok bool
		lines[i], ok = strings.CutPrefix(line, adj.Strip)
		if !ok {
			return "", fmt.Errorf("strip precondition failed: line %q does not start with %q", line, adj.Strip)
		}
	}

	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = adj.Add + line
	}

	return strings.Join(lines, "\n"), nil
}
