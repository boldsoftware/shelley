package bashkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestChainedCdPaths(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{"cd and command", "cd /tmp && ls", []string{"/tmp"}},
		{"cd semicolon command", "cd /tmp; ls", []string{"/tmp"}},
		{"cd and multiple commands", "cd foo/bar && make && ./run", []string{"foo/bar"}},
		{"multiple chained cd commands", "cd . && cd /tmp && ls", []string{".", "/tmp"}},
		{"cd with relative path", "cd ../sibling && go test ./...", []string{"../sibling"}},
		{"cd inside explicit block", "{ cd /tmp; ls; }", []string{"/tmp"}},
		{"non-literal path", `cd "$PWD" && ls`, []string{""}},
		{"bare cd", "cd", nil},
		{"cd no chain", "cd /tmp", nil},
		{"no cd", "ls -la", nil},
		{"pushd not flagged", "pushd /tmp && ls", nil},
		{"cd or fallback", "cd /tmp || exit 1", nil},
		// Subshells scope the cd; treat as intentional and do not report.
		{"cd in subshell and", "(cd /tmp && ls)", nil},
		{"cd in subshell semi", "(cd /tmp; ls)", nil},
		{"subshell then top-level cmd", "(cd /tmp && ls) && echo done", nil},
		{"unparseable", "cd /tmp &&", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ChainedCdPaths(tc.script)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ChainedCdPaths(%q) = %q, want %q", tc.script, got, tc.want)
			}
		})
	}
}

func TestGitDirs(t *testing.T) {
	tests := []struct {
		script string
		want   []string
	}{
		{"git commit -m x", []string{"/wd"}},
		{"cd /repo && git commit -m x", []string{"/repo"}},
		{"cd /repo; make; git status", []string{"/repo"}},
		{"cd ~/repo && git log", []string{"/home/u/repo"}},
		{"cd && git log", []string{"/home/u"}},
		{"cd sub && cd ../other && git add a && git commit -m x", []string{"/wd/other"}},
		{"cd /a && git log && cd /b && git log", []string{"/a", "/b"}},
		{"git -C /repo commit && git -C sub -C deeper log", []string{"/repo", "/wd/sub/deeper"}},
		{"cd /repo && git -c core.pager=cat -C sub log", []string{"/repo/sub"}},
		{"GIT_EDITOR=true git rebase --continue", []string{"/wd"}},
		{"if git diff --quiet; then cd /repo; git pull; fi", []string{"/wd", "/repo"}},
		{`cd '/my repo' && git -C "sub dir" log`, []string{"/my repo/sub dir"}},
		{`cd /my\ repo && git log`, []string{"/my repo"}},
		{"cd -P -- /repo && git log", []string{"/repo"}},
		// Only an unquoted ~ means home.
		{`cd "~/x" && git -C \~/y log`, []string{"/wd/~/x/~/y"}},
		// Subshells, substitutions, pipelines, functions and background
		// commands scope their cds.
		{"(cd /repo && git log) && git status", []string{"/repo", "/wd"}},
		{"echo $(cd /repo && git rev-parse HEAD) && git log", []string{"/repo", "/wd"}},
		{"cd /a; cat <(cd /b && git status); git commit", []string{"/b", "/a"}},
		{"cd /a | git log; git status", []string{"/wd"}},
		{"f() { cd /a; git log; }; git status", []string{"/a", "/wd"}},
		{"cd /a && git fetch &\ngit status", []string{"/a", "/wd"}},
		// Directories reached through non-constant paths are unknown.
		{`cd "$DIR" && git commit`, nil},
		{`cd "/repo/$sub" && git commit`, nil},
		{`cd $'/repo' && git commit`, nil},
		{`cd /repo && git -C "$sub" log && git log`, []string{"/repo"}},
		{`cd "$DIR" && cd /repo && git log`, []string{"/repo"}},
		{"cd - && git log", nil},
		{"cd ~other && git log", nil},
		{"cd /repo && make", nil},
		{"cd /repo &&", nil},
	}
	for _, tc := range tests {
		t.Run(tc.script, func(t *testing.T) {
			if got := GitDirs(tc.script, "/wd", "/home/u"); !slices.Equal(got, tc.want) {
				t.Errorf("GitDirs(%q) = %q, want %q", tc.script, got, tc.want)
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
	// Isolate from any host git config (e.g. a global core.hooksPath
	// that enforces commit-message policy on agent-driven commits).
	testEnv := append(
		os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = testEnv
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
	cmd.Env = testEnv
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
