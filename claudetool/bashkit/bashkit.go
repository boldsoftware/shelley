package bashkit

import (
	"bytes"
	"fmt"
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

var checks = []func(*syntax.CallExpr) error{
	noBlindGitAdd,
	noDangerousRmRf,
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
		return true
	})

	return err
}

// ChainedCdPaths reports the literal paths from top-level `cd <path>` commands
// chained with a subsequent command via `&&` or `;`, e.g. `cd /tmp && ls` or
// `cd /tmp; ls`. Such patterns are better expressed by calling the change_dir
// tool first, since `cd` inside a bash invocation does not persist across tool
// calls.
//
// A non-literal path is returned as an empty string. The caller can still
// report the chain while avoiding assumptions about shell expansion.
//
// Patterns intentionally NOT reported:
//   - bare `cd` or a standalone `cd <path>` with nothing chained;
//   - `cd <path> || ...` (fallback/error path);
//   - `cd` inside a subshell like `(cd /tmp && ls)`, which is the
//     idiomatic way to scope a directory change without persistence.
func ChainedCdPaths(bashScript string) []string {
	r := strings.NewReader(bashScript)
	parser := syntax.NewParser()
	file, err := parser.Parse(r, "")
	if err != nil {
		return nil
	}
	paths := []string{}
	cdPath := func(s *syntax.Stmt) (string, bool) {
		if s == nil || s.Cmd == nil {
			return "", false
		}
		call, ok := s.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) < 2 {
			return "", false
		}
		if call.Args[0].Lit() != "cd" {
			return "", false
		}
		return call.Args[1].Lit(), true
	}
	var checkStmts func(stmts []*syntax.Stmt)
	var checkStmt func(s *syntax.Stmt)
	var andChainStmts func(s *syntax.Stmt) []*syntax.Stmt
	andChainStmts = func(s *syntax.Stmt) []*syntax.Stmt {
		if s == nil || s.Cmd == nil {
			return nil
		}
		binary, ok := s.Cmd.(*syntax.BinaryCmd)
		if !ok || binary.Op != syntax.AndStmt {
			return []*syntax.Stmt{s}
		}
		return append(andChainStmts(binary.X), andChainStmts(binary.Y)...)
	}
	checkStmts = func(stmts []*syntax.Stmt) {
		// `a; b` at the same level: report each non-final `cd <path>`.
		for i := 0; i+1 < len(stmts); i++ {
			if path, ok := cdPath(stmts[i]); ok {
				paths = append(paths, path)
			}
		}
		for _, s := range stmts {
			checkStmt(s)
		}
	}
	checkStmt = func(s *syntax.Stmt) {
		if s == nil || s.Cmd == nil {
			return
		}
		switch c := s.Cmd.(type) {
		case *syntax.BinaryCmd:
			if c.Op == syntax.AndStmt {
				chain := andChainStmts(s)
				for i := 0; i+1 < len(chain); i++ {
					if path, ok := cdPath(chain[i]); ok {
						paths = append(paths, path)
					}
				}
				for _, chained := range chain {
					checkStmt(chained)
				}
				return
			}
			checkStmt(c.X)
			checkStmt(c.Y)
		case *syntax.Block:
			checkStmts(c.Stmts)
		case *syntax.Subshell:
			// Intentionally do not recurse: `(cd ... && ...)` is scoped
			// and does not affect the caller's working directory.
			return
		}
	}
	checkStmts(file.Stmts)
	return paths
}

// GitDirs reports the directories bashScript runs git commands in when
// started in workingDir, in order of first use, following cd commands and
// git's -C option. Both workingDir and home must be absolute, as are the
// results. Directories reached through non-constant paths are omitted. A cd
// in a conditional or loop counts as run.
func GitDirs(bashScript, workingDir, home string) []string {
	file, err := syntax.NewParser().Parse(strings.NewReader(bashScript), "")
	if err != nil {
		return nil
	}
	var dirs []string
	dir := workingDir // where the next command runs; "" if unknown
	var visit func(syntax.Node) bool
	// scoped visits nodes that run in a subshell, or in a function body
	// that may never be called, so a cd there does not outlive them.
	scoped := func(nodes ...syntax.Node) bool {
		for _, n := range nodes {
			saved := dir
			syntax.Walk(n, visit)
			dir = saved
		}
		return false
	}
	visit = func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Stmt:
			if n.Background || n.Coprocess {
				return scoped(n.Cmd)
			}
		case *syntax.Subshell:
			return scoped(&syntax.Block{Stmts: n.Stmts})
		case *syntax.CmdSubst:
			return scoped(&syntax.Block{Stmts: n.Stmts})
		case *syntax.ProcSubst:
			return scoped(&syntax.Block{Stmts: n.Stmts})
		case *syntax.FuncDecl:
			return scoped(n.Body)
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				return scoped(n.X, n.Y)
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			switch n.Args[0].Lit() {
			case "cd":
				dir = joinPath(dir, cdTarget(n.Args[1:], home))
			case "git":
				if d := gitDir(dir, n.Args[1:], home); d != "" && !slices.Contains(dirs, d) {
					dirs = append(dirs, d)
				}
			}
		}
		return true
	}
	syntax.Walk(file, visit)
	return dirs
}

// cdTarget returns the directory operand of a cd command with args, home if
// there is none, or "" if it is not constant.
func cdTarget(args []*syntax.Word, home string) string {
	for i, arg := range args {
		switch a := arg.Lit(); {
		case a == "--":
			if i+1 < len(args) {
				return constPath(args[i+1], home)
			}
			return home
		case a == "-":
			return "" // the previous directory
		case len(a) < 2 || a[0] != '-':
			return constPath(arg, home)
		}
	}
	return home
}

// gitDir returns the directory a git command with args runs in when started
// in dir, applying any -C options.
func gitDir(dir string, args []*syntax.Word, home string) string {
	for i := 0; i < len(args); i++ {
		switch arg := args[i].Lit(); {
		case arg == "-C" && i+1 < len(args):
			i++
			dir = joinPath(dir, constPath(args[i], home))
		case arg == "-c":
			i++
		case !strings.HasPrefix(arg, "-"):
			return dir // the subcommand
		}
	}
	return dir
}

// joinPath resolves target against dir, as cd and git -C do. A relative
// target against an unknown dir, or an unknown target, is unknown ("").
func joinPath(dir, target string) string {
	switch {
	case path.IsAbs(target):
		return path.Clean(target)
	case target == "" || dir == "":
		return ""
	}
	return path.Join(dir, target)
}

// constPath returns the value of w after tilde expansion, for a ~ or ~/
// prefix, and quote removal. It returns "" if w has other expansions.
func constPath(w *syntax.Word, home string) string {
	var b strings.Builder
	for i, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			value := p.Value
			if i == 0 && strings.HasPrefix(value, "~") {
				if home == "" || !strings.HasPrefix(value, "~/") && (value != "~" || len(w.Parts) > 1) {
					return "" // ~user, or quotes in the tilde prefix
				}
				b.WriteString(home)
				value = value[1:]
			}
			b.WriteString(unescape(value, ""))
		case *syntax.SglQuoted:
			if p.Dollar {
				return ""
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return ""
			}
			for _, q := range p.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return ""
				}
				b.WriteString(unescape(lit.Value, "$`\"\\"))
			}
		default:
			return ""
		}
	}
	return b.String()
}

// unescape removes the backslashes that escape characters in s: any
// character if only is empty, else only those in only.
func unescape(s, only string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (only == "" || strings.IndexByte(only, s[i+1]) >= 0) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
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
