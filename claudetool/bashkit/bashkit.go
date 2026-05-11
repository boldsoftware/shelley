package bashkit

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"
)

var checks = []func(*syntax.CallExpr) error{
	noBlindGitAdd,
	noDangerousRmRf,
}

// Process-level checks that track state across calls
var processAwareChecks = []func(*syntax.CallExpr) error{
	noSketchWipBranchChangesOnce,
}

// Track whether sketch-wip branch warning has been shown in this process
var (
	sketchWipWarningMu    sync.Mutex
	sketchWipWarningShown bool
)

// ResetSketchWipWarning resets the warning state for testing purposes
func ResetSketchWipWarning() {
	sketchWipWarningMu.Lock()
	sketchWipWarningShown = false
	sketchWipWarningMu.Unlock()
}

// Check inspects bashScript and returns an error if it ought not be executed.
// Check DOES NOT PROVIDE SECURITY against malicious actors.
// It is intended to catch straightforward mistakes in which a model
// does things despite having been instructed not to do them.
func Check(bashScript string) error {
	r := strings.NewReader(bashScript)
	parser := syntax.NewParser()
	file, err := parser.Parse(r, "")
	if err != nil {
		// Execution will fail, but we'll get a better error message from bash.
		// Note that if this were security load bearing, this would be a terrible idea:
		// You could smuggle stuff past Check by exploiting differences in what is considered syntactically valid.
		// But it is not.
		return nil
	}

	syntax.Walk(file, func(node syntax.Node) bool {
		if err != nil {
			return false
		}
		callExpr, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		// Run regular checks
		for _, check := range checks {
			err = check(callExpr)
			if err != nil {
				return false
			}
		}
		// Run process-aware checks
		for _, check := range processAwareChecks {
			err = check(callExpr)
			if err != nil {
				return false
			}
		}
		return true
	})

	return err
}

// WillRunGitCommit reports whether bashScript contains a git commit command.
func WillRunGitCommit(bashScript string) (bool, error) {
	r := strings.NewReader(bashScript)
	parser := syntax.NewParser()
	file, err := parser.Parse(r, "")
	if err != nil {
		// Parsing failed, but let's not consider this an error for the same reasons as in Check
		return false, nil
	}

	willCommit := false

	syntax.Walk(file, func(node syntax.Node) bool {
		callExpr, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if isGitCommitCommand(callExpr) {
			willCommit = true
			return false
		}
		return true
	})

	return willCommit, nil
}

// ChainsCdWithCommand reports whether bashScript chains a top-level
// `cd <path>` with a subsequent command via `&&` or `;`, e.g.
// `cd /tmp && ls` or `cd /tmp; ls`. Such patterns are better expressed by
// calling the change_dir tool first, since `cd` inside a bash invocation
// does not persist across tool calls.
//
// Patterns intentionally NOT flagged:
//   - bare `cd` or a standalone `cd <path>` with nothing chained;
//   - `cd <path> || ...` (fallback/error path);
//   - `cd` inside a subshell like `(cd /tmp && ls)`, which is the
//     idiomatic way to scope a directory change without persistence.
func ChainsCdWithCommand(bashScript string) bool {
	r := strings.NewReader(bashScript)
	parser := syntax.NewParser()
	file, err := parser.Parse(r, "")
	if err != nil {
		return false
	}
	isCdWithArg := func(s *syntax.Stmt) bool {
		if s == nil || s.Cmd == nil {
			return false
		}
		call, ok := s.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) < 2 {
			return false
		}
		return call.Args[0].Lit() == "cd"
	}
	var checkStmts func(stmts []*syntax.Stmt) bool
	var checkStmt func(s *syntax.Stmt) bool
	checkStmts = func(stmts []*syntax.Stmt) bool {
		// `a; b` at the same level: flag if any non-final stmt is `cd <path>`.
		for i := 0; i+1 < len(stmts); i++ {
			if isCdWithArg(stmts[i]) {
				return true
			}
		}
		for _, s := range stmts {
			if checkStmt(s) {
				return true
			}
		}
		return false
	}
	checkStmt = func(s *syntax.Stmt) bool {
		if s == nil || s.Cmd == nil {
			return false
		}
		switch c := s.Cmd.(type) {
		case *syntax.BinaryCmd:
			if c.Op == syntax.AndStmt && isCdWithArg(c.X) {
				return true
			}
			if checkStmt(c.X) || checkStmt(c.Y) {
				return true
			}
		case *syntax.Block:
			if checkStmts(c.Stmts) {
				return true
			}
		case *syntax.Subshell:
			// Intentionally do not recurse: `(cd ... && ...)` is scoped
			// and does not affect the caller's working directory.
			return false
		}
		return false
	}
	return checkStmts(file.Stmts)
}

// noDangerousRmRf checks for rm -rf commands that could delete critical directories.
// It rejects patterns that could delete .git directories, home directories (~, $HOME),
// or root directories.
func noDangerousRmRf(cmd *syntax.CallExpr) error {
	if hasDangerousRmRf(cmd) {
		return fmt.Errorf("permission denied: this rm command could delete critical data (.git, home directory, or root). If you really need to run this command, spell out the full path explicitly (no wildcards, ~, or $HOME). Consider confirming with the user before running destructive cleanup commands")
	}
	return nil
}

// hasDangerousRmRf checks if an rm command could delete critical directories.
func hasDangerousRmRf(cmd *syntax.CallExpr) bool {
	if len(cmd.Args) < 1 {
		return false
	}

	// Check if the command is rm
	firstArg := cmd.Args[0].Lit()
	if firstArg != "rm" {
		return false
	}

	// Check if -r or -R is present (recursive)
	hasRecursive := false
	hasForce := false
	for _, arg := range cmd.Args[1:] {
		lit := arg.Lit()
		// Handle combined flags like -rf, -fr, -Rf, etc.
		if strings.HasPrefix(lit, "-") && !strings.HasPrefix(lit, "--") {
			if strings.ContainsAny(lit, "rR") {
				hasRecursive = true
			}
			if strings.Contains(lit, "f") {
				hasForce = true
			}
		}
		if lit == "--recursive" {
			hasRecursive = true
		}
		if lit == "--force" {
			hasForce = true
		}
	}

	// Only check for dangerous paths if it's a recursive and forced rm
	if !hasRecursive || !hasForce {
		return false
	}

	// Check arguments for dangerous patterns
	for _, arg := range cmd.Args[1:] {
		lit := arg.Lit()
		// Skip flags
		if strings.HasPrefix(lit, "-") {
			continue
		}

		// Check for .git directory patterns
		if lit == ".git" || strings.HasSuffix(lit, "/.git") ||
			strings.Contains(lit, ".git/") || strings.Contains(lit, ".git ") {
			return true
		}

		// Check for home directory patterns
		if lit == "~" || lit == "~/" || strings.HasPrefix(lit, "~/") {
			return true
		}

		// Check for root directory
		if lit == "/" {
			return true
		}

		// Check for wildcards that could match .git
		if lit == ".*" || strings.HasSuffix(lit, "/.*") {
			return true
		}

		// Check for broad wildcards at dangerous locations
		if lit == "*" || lit == "/*" {
			return true
		}
	}

	// Also check if the argument uses variable expansion (like $HOME)
	// We need to walk the AST more carefully for this
	for _, arg := range cmd.Args[1:] {
		if containsHomeVariable(arg) {
			return true
		}
	}

	return false
}

// containsHomeVariable checks if a word contains $HOME or ${HOME} expansion
func containsHomeVariable(word *syntax.Word) bool {
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.ParamExp:
			if p.Param != nil && p.Param.Value == "HOME" {
				return true
			}
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				if pe, ok := inner.(*syntax.ParamExp); ok {
					if pe.Param != nil && pe.Param.Value == "HOME" {
						return true
					}
				}
			}
		}
	}
	return false
}

// noBlindGitAdd checks for git add commands that blindly add all files.
// It rejects patterns like 'git add -A', 'git add .', 'git add --all', 'git add *'.
func noBlindGitAdd(cmd *syntax.CallExpr) error {
	if hasBlindGitAdd(cmd) {
		return fmt.Errorf("permission denied: blind git add commands (git add -A, git add ., git add --all, git add *) are not allowed, specify files explicitly")
	}
	return nil
}

func hasBlindGitAdd(cmd *syntax.CallExpr) bool {
	if len(cmd.Args) < 2 {
		return false
	}
	if cmd.Args[0].Lit() != "git" {
		return false
	}

	// Find the 'add' subcommand
	addIndex := -1
	for i, arg := range cmd.Args {
		if arg.Lit() == "add" {
			addIndex = i
			break
		}
	}

	if addIndex < 0 {
		return false
	}

	// Check arguments after 'add' for blind patterns
	for i := addIndex + 1; i < len(cmd.Args); i++ {
		arg := cmd.Args[i].Lit()
		// Check for blind add patterns
		if arg == "-A" || arg == "--all" || arg == "." || arg == "*" {
			return true
		}
	}

	return false
}

// AddCoauthorTrailer modifies a bash script to add a Co-authored-by trailer
// to any git commit commands. Returns the modified script.
func AddCoauthorTrailer(bashScript, trailer string) string {
	r := strings.NewReader(bashScript)
	parser := syntax.NewParser(syntax.KeepComments(true))
	file, err := parser.Parse(r, "")
	if err != nil {
		// Can't parse, return original
		return bashScript
	}

	modified := false
	syntax.Walk(file, func(node syntax.Node) bool {
		callExpr, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if addTrailerToGitCommit(callExpr, trailer) {
			modified = true
		}
		return true
	})

	if !modified {
		return bashScript
	}

	var buf bytes.Buffer
	printer := syntax.NewPrinter()
	if err := printer.Print(&buf, file); err != nil {
		return bashScript
	}
	return buf.String()
}

// addTrailerToGitCommit adds --trailer to a git commit command.
// Returns true if the command was modified.
func addTrailerToGitCommit(cmd *syntax.CallExpr, trailer string) bool {
	if !isGitCommitCommand(cmd) {
		return false
	}

	// Find where to insert --trailer (right after "commit")
	insertIdx := -1
	for i := 1; i < len(cmd.Args); i++ {
		if cmd.Args[i].Lit() == "commit" {
			insertIdx = i + 1
			break
		}
	}
	if insertIdx < 0 {
		return false
	}

	// Override git's default --trailer policy. The default is
	// addIfDifferentNeighbor, which only compares against the trailer
	// immediately preceding ours; if a user-authored commit message already
	// has this trailer but another trailer (e.g. `CC: ...`) sits between it
	// and the end, git re-appends and we get a duplicate. addIfDifferent
	// compares against all trailers in the message. This is inserted before
	// `commit`, so it appears in cmd.Args at insertIdx-1's position.
	configArg := &syntax.Word{
		Parts: []syntax.WordPart{&syntax.Lit{Value: "-c"}},
	}
	configVal := &syntax.Word{
		Parts: []syntax.WordPart{
			&syntax.DblQuoted{
				Parts: []syntax.WordPart{
					&syntax.Lit{Value: "trailer.ifexists=addIfDifferent"},
				},
			},
		},
	}

	// Create --trailer argument
	trailerArg := &syntax.Word{
		Parts: []syntax.WordPart{
			&syntax.Lit{Value: "--trailer"},
		},
	}
	// Create the trailer value argument
	trailerVal := &syntax.Word{
		Parts: []syntax.WordPart{
			&syntax.DblQuoted{
				Parts: []syntax.WordPart{
					&syntax.Lit{Value: trailer},
				},
			},
		},
	}

	// Insert -c <config> before 'commit' (at insertIdx-1) and --trailer
	// <value> after 'commit' (at insertIdx).
	commitIdx := insertIdx - 1
	newArgs := make([]*syntax.Word, 0, len(cmd.Args)+4)
	newArgs = append(newArgs, cmd.Args[:commitIdx]...)
	newArgs = append(newArgs, configArg, configVal, cmd.Args[commitIdx], trailerArg, trailerVal)
	newArgs = append(newArgs, cmd.Args[commitIdx+1:]...)
	cmd.Args = newArgs

	return true
}

// isGitCommitCommand checks if a command is 'git commit'.
func isGitCommitCommand(cmd *syntax.CallExpr) bool {
	if len(cmd.Args) < 2 {
		return false
	}

	// First argument must be 'git'
	if cmd.Args[0].Lit() != "git" {
		return false
	}

	// Look for 'commit' in any position after 'git'
	for i := 1; i < len(cmd.Args); i++ {
		if cmd.Args[i].Lit() == "commit" {
			return true
		}
	}

	return false
}

// noSketchWipBranchChangesOnce checks for git commands that would change the sketch-wip branch.
// It rejects commands that would rename the sketch-wip branch or switch away from it.
// This check only shows the warning once per process.
func noSketchWipBranchChangesOnce(cmd *syntax.CallExpr) error {
	if hasSketchWipBranchChanges(cmd) {
		// Check if we've already warned in this process
		sketchWipWarningMu.Lock()
		alreadyWarned := sketchWipWarningShown
		if !alreadyWarned {
			sketchWipWarningShown = true
		}
		sketchWipWarningMu.Unlock()

		if !alreadyWarned {
			return fmt.Errorf("permission denied: cannot leave 'sketch-wip' branch. This branch is designated for change detection and auto-push; work on other branches may be lost. Warning shown once per session. Repeat command if needed for temporary operations (rebase, bisect, etc.) but return to sketch-wip afterward. Note: users can push to any branch via the Push button in the UI")
		}
	}
	return nil
}

// hasSketchWipBranchChanges checks if a git command would change the sketch-wip branch.
func hasSketchWipBranchChanges(cmd *syntax.CallExpr) bool {
	if len(cmd.Args) < 2 {
		return false
	}
	if cmd.Args[0].Lit() != "git" {
		return false
	}

	// Look for subcommands that could change the sketch-wip branch
	for i := 1; i < len(cmd.Args); i++ {
		arg := cmd.Args[i].Lit()
		switch arg {
		case "branch":
			// Check for branch rename: git branch -m sketch-wip newname or git branch -M sketch-wip newname
			if i+2 < len(cmd.Args) {
				// Look for -m or -M flag
				for j := i + 1; j < len(cmd.Args)-1; j++ {
					flag := cmd.Args[j].Lit()
					if flag == "-m" || flag == "-M" {
						// Check if sketch-wip is the source branch
						if cmd.Args[j+1].Lit() == "sketch-wip" {
							return true
						}
					}
				}
			}
		case "checkout":
			// Check for branch switching: git checkout otherbranch
			// But allow git checkout files/paths
			if i+1 < len(cmd.Args) {
				nextArg := cmd.Args[i+1].Lit()
				// Skip if it's a flag
				if !strings.HasPrefix(nextArg, "-") {
					// This might be a branch checkout - we'll be conservative and warn
					// unless it looks like a file path
					if !strings.Contains(nextArg, "/") && !strings.Contains(nextArg, ".") {
						return true
					}
				}
			}
		case "switch":
			// Check for branch switching: git switch otherbranch
			if i+1 < len(cmd.Args) {
				nextArg := cmd.Args[i+1].Lit()
				// Skip if it's a flag
				if !strings.HasPrefix(nextArg, "-") {
					return true
				}
			}
		}
	}

	return false
}
