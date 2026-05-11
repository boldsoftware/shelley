package bashkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantErr  bool
		errMatch string // string to match in error message, if wantErr is true
	}{
		{
			name:     "valid script",
			script:   "echo hello world",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "invalid syntax",
			script:   "echo 'unterminated string",
			wantErr:  false, // As per implementation, syntax errors are not flagged
			errMatch: "",
		},
		// Git add validation tests
		{
			name:     "git add with -A flag",
			script:   "git add -A",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with --all flag",
			script:   "git add --all",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with dot",
			script:   "git add .",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with asterisk",
			script:   "git add *",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with multiple flags including -A",
			script:   "git add -v -A",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with specific file",
			script:   "git add main.go",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "git add with multiple specific files",
			script:   "git add main.go utils.go",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "git add with directory path",
			script:   "git add src/main.go",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "git add with git flags before add",
			script:   "git -C /path/to/repo add -A",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with valid flags",
			script:   "git add -v main.go",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "git command without add",
			script:   "git status",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "multiline script with blind git add",
			script:   "echo 'Adding files' && git add -A && git commit -m 'Update'",
			wantErr:  true,
			errMatch: "blind git add commands",
		},
		{
			name:     "git add with pattern that looks like blind but is specific",
			script:   "git add file.A",
			wantErr:  false,
			errMatch: "",
		},
		{
			name:     "commented blind git add",
			script:   "# git add -A",
			wantErr:  false,
			errMatch: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.script)
			if (err != nil) != tc.wantErr {
				t.Errorf("Check() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), tc.errMatch) {
				t.Errorf("Check() error message = %v, want containing %v", err, tc.errMatch)
			}
		})
	}
}

func TestWillRunGitCommit(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		wantCommit bool
	}{
		{
			name:       "simple git commit",
			script:     "git commit -m 'Add feature'",
			wantCommit: true,
		},
		{
			name:       "git command without commit",
			script:     "git status",
			wantCommit: false,
		},
		{
			name:       "multiline script with git commit",
			script:     "echo 'Making changes' && git add . && git commit -m 'Update files'",
			wantCommit: true,
		},
		{
			name:       "multiline script without git commit",
			script:     "echo 'Checking status' && git status",
			wantCommit: false,
		},
		{
			name:       "script with commented git commit",
			script:     "# git commit -m 'This is commented out'",
			wantCommit: false,
		},
		{
			name:       "git commit with variables",
			script:     "MSG='Fix bug' && git commit -m 'Using variable'",
			wantCommit: true,
		},
		{
			name:       "only git command",
			script:     "git",
			wantCommit: false,
		},
		{
			name:       "script with invalid syntax",
			script:     "git commit -m 'unterminated string",
			wantCommit: false,
		},
		{
			name:       "commit used in different context",
			script:     "echo 'commit message'",
			wantCommit: false,
		},
		{
			name:       "git with flags before commit",
			script:     "git -C /path/to/repo commit -m 'Update'",
			wantCommit: true,
		},
		{
			name:       "git with multiple flags",
			script:     "git --git-dir=.git -C repo commit -a -m 'Update'",
			wantCommit: true,
		},
		{
			name:       "git with env vars",
			script:     "GIT_AUTHOR_NAME=\"Josh Bleecher Snyder\" GIT_AUTHOR_EMAIL=\"josharian@gmail.com\" git commit -am \"Updated code\"",
			wantCommit: true,
		},
		{
			name:       "git with redirections",
			script:     "git commit -m 'Fix issue' > output.log 2>&1",
			wantCommit: true,
		},
		{
			name:       "git with piped commands",
			script:     "echo 'Committing' | git commit -F -",
			wantCommit: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCommit, err := WillRunGitCommit(tc.script)
			if err != nil {
				t.Errorf("WillRunGitCommit() error = %v", err)
				return
			}
			if gotCommit != tc.wantCommit {
				t.Errorf("WillRunGitCommit() = %v, want %v", gotCommit, tc.wantCommit)
			}
		})
	}
}

func TestSketchWipBranchProtection(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		wantErr     bool
		errMatch    string
		resetBefore bool // if true, reset warning state before test
	}{
		{
			name:        "git branch rename sketch-wip",
			script:      "git branch -m sketch-wip new-branch",
			wantErr:     true,
			errMatch:    "cannot leave 'sketch-wip' branch",
			resetBefore: true,
		},
		{
			name:        "git branch force rename sketch-wip",
			script:      "git branch -M sketch-wip new-branch",
			wantErr:     false, // second call should not error (already warned)
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git checkout to other branch",
			script:      "git checkout main",
			wantErr:     false, // third call should not error (already warned)
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git switch to other branch",
			script:      "git switch main",
			wantErr:     false, // fourth call should not error (already warned)
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git checkout file (should be allowed)",
			script:      "git checkout -- file.txt",
			wantErr:     false,
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git checkout path (should be allowed)",
			script:      "git checkout -- src/main.go",
			wantErr:     false,
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git commit (should be allowed)",
			script:      "git commit -m 'test'",
			wantErr:     false,
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git status (should be allowed)",
			script:      "git status",
			wantErr:     false,
			errMatch:    "",
			resetBefore: false,
		},
		{
			name:        "git branch rename other branch (should be allowed)",
			script:      "git branch -m old-branch new-branch",
			wantErr:     false,
			errMatch:    "",
			resetBefore: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.resetBefore {
				ResetSketchWipWarning()
			}
			err := Check(tc.script)
			if (err != nil) != tc.wantErr {
				t.Errorf("Check() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), tc.errMatch) {
				t.Errorf("Check() error message = %v, want containing %v", err, tc.errMatch)
			}
		})
	}
}

func TestHasSketchWipBranchChanges(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantHas bool
	}{
		{
			name:    "git branch rename sketch-wip",
			script:  "git branch -m sketch-wip new-branch",
			wantHas: true,
		},
		{
			name:    "git branch force rename sketch-wip",
			script:  "git branch -M sketch-wip new-branch",
			wantHas: true,
		},
		{
			name:    "git checkout to branch",
			script:  "git checkout main",
			wantHas: true,
		},
		{
			name:    "git switch to branch",
			script:  "git switch main",
			wantHas: true,
		},
		{
			name:    "git checkout file",
			script:  "git checkout -- file.txt",
			wantHas: false,
		},
		{
			name:    "git checkout path",
			script:  "git checkout src/main.go",
			wantHas: false,
		},
		{
			name:    "git checkout with .extension",
			script:  "git checkout file.go",
			wantHas: false,
		},
		{
			name:    "git status",
			script:  "git status",
			wantHas: false,
		},
		{
			name:    "git commit",
			script:  "git commit -m 'test'",
			wantHas: false,
		},
		{
			name:    "git branch rename other",
			script:  "git branch -m old-branch new-branch",
			wantHas: false,
		},
		{
			name:    "git switch with flag",
			script:  "git switch -c new-branch",
			wantHas: false,
		},
		{
			name:    "git checkout with flag",
			script:  "git checkout -b new-branch",
			wantHas: false,
		},
		{
			name:    "not a git command",
			script:  "echo hello",
			wantHas: false,
		},
		{
			name:    "empty command",
			script:  "",
			wantHas: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.script)
			parser := syntax.NewParser()
			file, err := parser.Parse(r, "")
			if err != nil {
				if tc.wantHas {
					t.Errorf("Parse error: %v", err)
				}
				return
			}

			found := false
			syntax.Walk(file, func(node syntax.Node) bool {
				callExpr, ok := node.(*syntax.CallExpr)
				if !ok {
					return true
				}
				if hasSketchWipBranchChanges(callExpr) {
					found = true
					return false
				}
				return true
			})

			if found != tc.wantHas {
				t.Errorf("hasSketchWipBranchChanges() = %v, want %v", found, tc.wantHas)
			}
		})
	}
}

func TestDangerousRmRf(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantErr  bool
		errMatch string
	}{
		// Dangerous rm -rf commands that should be blocked
		{
			name:     "rm -rf .git",
			script:   "rm -rf .git",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf with path ending in .git",
			script:   "rm -rf /path/to/.git",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf ~ (home directory)",
			script:   "rm -rf ~",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf ~/ (home directory with slash)",
			script:   "rm -rf ~/",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf ~/path",
			script:   "rm -rf ~/Documents",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf $HOME",
			script:   "rm -rf $HOME",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf ${HOME}",
			script:   "rm -rf ${HOME}",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf / (root)",
			script:   "rm -rf /",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf .* (hidden files wildcard)",
			script:   "rm -rf .*",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf * (all files wildcard)",
			script:   "rm -rf *",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf /* (root wildcard)",
			script:   "rm -rf /*",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf with separate flags",
			script:   "rm -r -f .git",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -Rf .git (capital R)",
			script:   "rm -Rf .git",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm --recursive --force .git",
			script:   "rm --recursive --force .git",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:     "rm -rf path/.*/",
			script:   "rm -rf path/.*",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		// Safe rm commands that should be allowed
		{
			name:    "rm -rf specific directory",
			script:  "rm -rf /tmp/build",
			wantErr: false,
		},
		{
			name:    "rm -rf node_modules",
			script:  "rm -rf node_modules",
			wantErr: false,
		},
		{
			name:    "rm -rf specific file",
			script:  "rm -rf /tmp/file.txt",
			wantErr: false,
		},
		{
			name:    "rm without recursive (safe)",
			script:  "rm -f .git",
			wantErr: false,
		},
		{
			name:    "rm without force (safe)",
			script:  "rm -r .git",
			wantErr: false,
		},
		{
			name:    "rm single file",
			script:  "rm file.txt",
			wantErr: false,
		},
		{
			name:    "rm -rf with quoted $HOME (literal string)",
			script:  "rm -rf '$HOME'",
			wantErr: false, // single quotes make it literal
		},
		// Complex commands
		{
			name:     "multiline with dangerous rm",
			script:   "echo cleaning && rm -rf .git && echo done",
			wantErr:  true,
			errMatch: "could delete critical data",
		},
		{
			name:    "multiline with safe rm",
			script:  "echo cleaning && rm -rf /tmp/build && echo done",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.script)
			if (err != nil) != tc.wantErr {
				t.Errorf("Check() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), tc.errMatch) {
				t.Errorf("Check() error message = %v, want containing %v", err, tc.errMatch)
			}
		})
	}
}

func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		wantErr     bool
		resetBefore bool // if true, reset warning state before test
	}{
		{
			name:        "git branch -m with current branch to sketch-wip (should be allowed)",
			script:      "git branch -m current-branch sketch-wip",
			wantErr:     false,
			resetBefore: true,
		},
		{
			name:        "git branch -m sketch-wip with no destination (should be blocked)",
			script:      "git branch -m sketch-wip",
			wantErr:     true,
			resetBefore: true,
		},
		{
			name:        "git branch -M with current branch to sketch-wip (should be allowed)",
			script:      "git branch -M current-branch sketch-wip",
			wantErr:     false,
			resetBefore: true,
		},
		{
			name:        "git checkout with -- flags (should be allowed)",
			script:      "git checkout -- --weird-filename",
			wantErr:     false,
			resetBefore: true,
		},
		{
			name:        "git switch with create flag (should be allowed)",
			script:      "git switch --create new-branch",
			wantErr:     false,
			resetBefore: true,
		},
		{
			name:        "complex git command with sketch-wip rename",
			script:      "git add . && git commit -m \"test\" && git branch -m sketch-wip production",
			wantErr:     true,
			resetBefore: true,
		},
		{
			name:        "git switch with -c short form (should be allowed)",
			script:      "git switch -c feature-branch",
			wantErr:     false,
			resetBefore: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.resetBefore {
				ResetSketchWipWarning()
			}
			err := Check(tc.script)
			if (err != nil) != tc.wantErr {
				t.Errorf("Check() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestHasBlindGitAddEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantHas bool
	}{
		{
			name:    "command with less than 2 args",
			script:  "git",
			wantHas: false,
		},
		{
			name:    "non-git command",
			script:  "ls -A",
			wantHas: false,
		},
		{
			name:    "git command without add subcommand",
			script:  "git status",
			wantHas: false,
		},
		{
			name:    "git add with no arguments after add",
			script:  "git add",
			wantHas: false,
		},
		{
			name:    "git add with valid file after flags",
			script:  "git add -v file.txt",
			wantHas: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.script)
			parser := syntax.NewParser()
			file, err := parser.Parse(r, "")
			if err != nil {
				if tc.wantHas {
					t.Errorf("Parse error: %v", err)
				}
				return
			}

			found := false
			syntax.Walk(file, func(node syntax.Node) bool {
				callExpr, ok := node.(*syntax.CallExpr)
				if !ok {
					return true
				}
				if hasBlindGitAdd(callExpr) {
					found = true
					return false
				}
				return true
			})

			if found != tc.wantHas {
				t.Errorf("hasBlindGitAdd() = %v, want %v", found, tc.wantHas)
			}
		})
	}
}

func TestHasSketchWipBranchChangesEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantHas bool
	}{
		{
			name:    "git command with less than 2 args",
			script:  "git",
			wantHas: false,
		},
		{
			name:    "non-git command",
			script:  "ls main",
			wantHas: false,
		},
		{
			name:    "git branch -m with sketch-wip not as source",
			script:  "git branch -m other-branch sketch-wip",
			wantHas: false,
		},
		{
			name:    "git checkout with complex path",
			script:  "git checkout src/components/file.go",
			wantHas: false,
		},
		{
			name:    "git switch with complex flag",
			script:  "git switch --detach HEAD~1",
			wantHas: false,
		},
		{
			name:    "git checkout with multiple flags",
			script:  "git checkout --ours --theirs file.txt",
			wantHas: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.script)
			parser := syntax.NewParser()
			file, err := parser.Parse(r, "")
			if err != nil {
				if tc.wantHas {
					t.Errorf("Parse error: %v", err)
				}
				return
			}

			found := false
			syntax.Walk(file, func(node syntax.Node) bool {
				callExpr, ok := node.(*syntax.CallExpr)
				if !ok {
					return true
				}
				if hasSketchWipBranchChanges(callExpr) {
					found = true
					return false
				}
				return true
			})

			if found != tc.wantHas {
				t.Errorf("hasSketchWipBranchChanges() = %v, want %v", found, tc.wantHas)
			}
		})
	}
}

func TestChainsCdWithCommand(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   bool
	}{
		{"cd and command", "cd /tmp && ls", true},
		{"cd semicolon command", "cd /tmp; ls", true},
		{"cd and multiple commands", "cd foo/bar && make && ./run", true},
		{"cd with relative path", "cd ../sibling && go test ./...", true},
		{"cd inside explicit block", "{ cd /tmp; ls; }", true},
		{"bare cd", "cd", false},
		{"cd no chain", "cd /tmp", false},
		{"no cd", "ls -la", false},
		{"pushd not flagged", "pushd /tmp && ls", false},
		{"cd or fallback", "cd /tmp || exit 1", false},
		// Subshells scope the cd; treat as intentional and do not flag.
		{"cd in subshell and", "(cd /tmp && ls)", false},
		{"cd in subshell semi", "(cd /tmp; ls)", false},
		{"subshell then top-level cmd", "(cd /tmp && ls) && echo done", false},
		{"unparseable", "cd /tmp &&", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ChainsCdWithCommand(tc.script)
			if got != tc.want {
				t.Errorf("ChainsCdWithCommand(%q) = %v, want %v", tc.script, got, tc.want)
			}
		})
	}
}

func TestAddCoauthorTrailer(t *testing.T) {
	const trailer = "Co-authored-by: Shelley <shelley@exe.dev>"
	tests := []struct {
		name     string
		in       string
		wantSubs []string // substrings that must appear in the output
		wantSame bool     // expect output unchanged
	}{
		{
			name:     "non-commit unchanged",
			in:       "echo hello",
			wantSame: true,
		},
		{
			name: "git commit gets -c and --trailer",
			in:   `git commit -m "hi"`,
			wantSubs: []string{
				`-c "trailer.ifexists=addIfDifferent"`,
				`--trailer "Co-authored-by: Shelley <shelley@exe.dev>"`,
			},
		},
		{
			name: "git commit --amend",
			in:   `git commit --amend -F msg.txt`,
			wantSubs: []string{
				`-c "trailer.ifexists=addIfDifferent"`,
				`--trailer "Co-authored-by: Shelley <shelley@exe.dev>"`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AddCoauthorTrailer(tc.in, trailer)
			if tc.wantSame {
				if got != tc.in {
					t.Fatalf("expected unchanged, got: %q", got)
				}
				return
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing %q\noutput: %s", sub, got)
				}
			}
			// -c must come before commit.
			dashC := strings.Index(got, "-c ")
			commit := strings.Index(got, "commit")
			if dashC < 0 || commit < 0 || dashC > commit {
				t.Errorf("expected -c before commit; got: %s", got)
			}
		})
	}
}

// TestAddCoauthorTrailer_NoDuplicates is the end-to-end check: run the
// rewritten command through real git and confirm we don't end up with two
// identical Co-authored-by lines when the message already has one (with
// another trailer following it).
func TestAddCoauthorTrailer_NoDuplicates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	const trailer = "Co-authored-by: Shelley <shelley@exe.dev>"

	dir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "f")

	// Write a commit message that already contains the trailer, plus an
	// additional trailer (CC: ...) after it. With git's default policy of
	// addIfDifferentNeighbor, naively passing --trailer would duplicate.
	msgPath := filepath.Join(dir, "MSG")
	msg := "subj\n\nbody\n\n" + trailer + "\nCC: philip\n"
	if err := os.WriteFile(msgPath, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	rewritten := AddCoauthorTrailer("git commit -F MSG", trailer)
	if rewritten == "git commit -F MSG" {
		t.Fatalf("AddCoauthorTrailer did not rewrite the command")
	}

	cmd := exec.Command("bash", "-c", rewritten)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running %q failed: %v\n%s", rewritten, err, out)
	}

	log := runGit("log", "-1", "--format=%B")
	if n := strings.Count(log, trailer); n != 1 {
		t.Fatalf("expected exactly 1 occurrence of trailer, got %d\nlog:\n%s", n, log)
	}
	if !strings.Contains(log, "CC: philip") {
		t.Errorf("expected CC: philip preserved\nlog:\n%s", log)
	}
}
