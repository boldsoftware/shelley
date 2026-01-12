package bashkit

import (
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
