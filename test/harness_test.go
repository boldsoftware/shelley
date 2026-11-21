package test

import (
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"shelley.exe.dev/pty"
)

func TestTerminalKillTargets(t *testing.T) {
	const dir = "/tmp/se2e-1-2/3"
	for _, tt := range []struct {
		name, ps        string
		groups, servers []int
	}{{
		name: "Linux",
		// ps right-aligns the numbers. The shell runs a pipeline as a
		// job in a process group of its own.
		ps: `   1101       1    1101    1101 exe-scroll: session /tmp/se2e-1-2/3/terminals/a.sock
   1102    1101    1102    1102 sh -c bash --login
   1103    1102    1102    1102 bash -i
   1104    1103    1104    1102 sleep 600
   1105    1103    1104    1102 cat
   1106       1    1106    1106 sleep 600
`,
		groups:  []int{1102, 1104},
		servers: []int{1101},
	}, {
		name: "Darwin",
		// Darwin's ps prints sess as an address in hex.
		ps: `8101 1 8101 deadbeef exe-scroll: session /tmp/se2e-1-2/3/terminals/owned.sock
8102 8101 8102 cafe0123 sh -c bash
8103 8102 8103 cafe0123 sleep 120
8104 1 8104 baddcafe sleep 120
8105 1 8105 baddcafe exe-scroll: session /tmp/unrelated/terminals/x.sock
`,
		groups:  []int{8102, 8103},
		servers: []int{8101},
	}, {
		name: "several servers",
		ps: `2101 1 2101 2101 exe-scroll: session /tmp/se2e-1-2/3/terminals/a.sock
2102 2101 2102 2102 bash -i
2201 1 2201 2201 exe-scroll: session /tmp/se2e-1-2/3/other/terminals/b.sock
2202 2201 2202 2202 bash -i
2203 2202 2203 2202 sleep 600
`,
		groups:  []int{2102, 2202, 2203},
		servers: []int{2101, 2201},
	}, {
		name: "only servers under dir",
		// The first server's directory only starts like dir, and the
		// second process only mentions a server's title.
		ps: `3101 1 3101 3101 exe-scroll: session /tmp/se2e-1-2/30/terminals/a.sock
3102 3101 3102 3102 bash -i
3201 1 3201 3201 grep exe-scroll: session /tmp/se2e-1-2/3/terminals/b.sock
3202 3201 3202 3202 bash -i
`,
	}, {
		name: "the server's own session is not a terminal's",
		// The server's children in its session, one leading a process
		// group of its own: they are no terminal's shell.
		ps: `4101 1 4101 4101 exe-scroll: session /tmp/se2e-1-2/3/terminals/a.sock
4102 4101 4102 4101 helper
4103 4101 4101 4101 helper
`,
		servers: []int{4101},
	}, {
		name: "skips what is not a row",
		ps: `    PID    PPID    PGID    SESS COMMAND

5100 1
5101 1 5101 5101 exe-scroll: session /tmp/se2e-1-2/3/terminals/a.sock
5102 5101 5102 5102 bash -i`,
		groups:  []int{5102},
		servers: []int{5101},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			groups, servers := terminalKillTargets(tt.ps, dir)
			if !slices.Equal(groups, tt.groups) || !slices.Equal(servers, tt.servers) {
				t.Errorf("groups %v, servers %v; want groups %v, servers %v", groups, servers, tt.groups, tt.servers)
			}
		})
	}
}

// terminalScript is what a terminal runs in the tests below: a shell
// with another process in its process group, and a job in a group of its
// own, as job control makes one. $1 is a directory for their pids.
const terminalScript = `set -m; sleep 120 & echo $! >"$1/job"; set +m; sleep 120 & echo $! >"$1/member"; echo $$ >"$1/shell"; exec sleep 120`

// terminal is the processes of a terminal session: the exe-scroll session
// server, in a session of its own, its title naming its socket; the shell
// it runs, in a session of its own, as forkpty makes it; another process
// in the shell's group; and the shell's job.
type terminal struct{ server, shell, member, job int }

// startTerminal runs start, which starts a terminal on socket whose shell
// runs terminalScript with pids, and waits for its processes.
func startTerminal(t *testing.T, socket, pids string, start func() error) terminal {
	t.Helper()
	var term terminal
	t.Cleanup(func() {
		// A terminal that did not start may not have said its pids.
		killTerminalSessions(filepath.Dir(filepath.Dir(socket)))
		for _, pid := range []int{term.job, term.member, term.shell, term.server} {
			if pid == 0 {
				continue
			}
			if p := procState(pid); p.alive && p.pgid > 1 && p.pgid != syscall.Getpgrp() {
				syscall.Kill(-p.pgid, syscall.SIGKILL)
			}
		}
	})
	if err := start(); err != nil {
		t.Fatal(err)
	}
	pid := func(name string) int {
		b, _ := os.ReadFile(filepath.Join(pids, name))
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	for deadline := time.Now().Add(10 * time.Second); term.shell == 0 || term.member == 0 || term.job == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the terminal on %s did not start", socket)
		}
		term.shell, term.member, term.job = pid("shell"), pid("member"), pid("job")
	}
	shell := procState(term.shell)
	server, member, job := procState(shell.ppid), procState(term.member), procState(term.job)
	if !strings.HasPrefix(server.command, "exe-scroll: session "+socket) || server.sess == shell.sess ||
		shell.pgid != term.shell || member.pgid != term.shell || job.sess != shell.sess || job.pgid != term.job {
		t.Fatalf("the terminal is not laid out as a terminal: server %+v, shell %+v, member %+v, job %+v", server, shell, member, job)
	}
	// Only now is the shell's parent known to be the server.
	term.server = shell.ppid
	return term
}

// startFakeTerminal starts processes laid out as a terminal: a server
// titled as exe-scroll's runs the shell with setsid. Session servers can
// outlive their shells; this one waits on a FIFO nothing writes to.
func startFakeTerminal(t *testing.T, socket string) terminal {
	t.Helper()
	pids := t.TempDir()
	cmd := exec.Command("bash", "-c", `setsid bash -c "$2" fake "$1" &
mkfifo "$1/hold"
read <"$1/hold"`, "fake", pids, terminalScript)
	cmd.Args[0] = "exe-scroll: session " + socket
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return startTerminal(t, socket, pids, func() error {
		if err := cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait()
		return nil
	})
}

type procInfo struct {
	ppid, pgid    int
	sess, command string
	alive         bool
}

func procState(pid int) procInfo {
	out, _ := exec.Command("ps", "-o", "ppid=,pgid=,sess=,stat=,command=", "-p", strconv.Itoa(pid)).Output()
	m := regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\S+)\s+(\S+)\s+(.*)$`).FindStringSubmatch(strings.TrimRight(string(out), "\n"))
	if m == nil {
		return procInfo{}
	}
	ppid, _ := strconv.Atoi(m[1])
	pgid, _ := strconv.Atoi(m[2])
	return procInfo{ppid, pgid, m[3], m[5], !strings.HasPrefix(m[4], "Z")}
}

// waitKilled waits for the processes named in killed to die.
func waitKilled(t *testing.T, killed map[string]int) {
	t.Helper()
	for name, pid := range killed {
		for deadline := time.Now().Add(10 * time.Second); procState(pid).alive; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("%s survived", name)
				break
			}
		}
	}
}

func TestKillTerminalSessions(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal(err)
		}
		t.Skip("no setsid to start a fake terminal with")
	}
	dir := t.TempDir()
	owned := startFakeTerminal(t, dir+"/terminals/owned.sock")
	// A server whose directory only starts like dir.
	other := startFakeTerminal(t, dir+"0/terminals/other.sock")
	unrelated := exec.Command("sleep", "120")
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer unrelated.Process.Kill()
	go unrelated.Wait()
	spared := map[string]int{
		"the other server": other.server, "the other shell": other.shell,
		"a process in the other shell's group": other.member, "the other job": other.job,
		"the unrelated session": unrelated.Process.Pid,
	}
	killed := map[string]int{
		"the server": owned.server, "its shell": owned.shell,
		"a process in the shell's group": owned.member, "the shell's job": owned.job,
	}
	for _, procs := range []map[string]int{killed, spared} {
		for name, pid := range procs {
			if !procState(pid).alive {
				t.Fatalf("%s is missing before killTerminalSessions", name)
			}
		}
	}

	killTerminalSessions(dir)

	waitKilled(t, killed)
	for name, pid := range spared {
		if !procState(pid).alive {
			t.Errorf("%s was killed", name)
		}
	}
}

// TestKillTerminalSessionsExeScroll holds the fake terminals above to the
// exe-scroll Shelley embeds: started on a PTY as Shelley's server starts
// it, its terminal is laid out as theirs, and killTerminalSessions kills
// it whole.
func TestKillTerminalSessionsExeScroll(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	if !filepath.IsAbs(file) {
		t.Skip("built with -trimpath: no source directory to find exe-scroll in")
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	if _, err := os.Stat(filepath.Join(filepath.Dir(file), "..", "exescroll", "embed_"+platform+".go")); err != nil {
		t.Skipf("no exe-scroll embedded for %s here: %v", platform, err)
	}
	bin := filepath.Join(filepath.Dir(file), "..", "exescroll", "binary", "exe-scroll-"+runtime.GOOS+"-"+runtime.GOARCH)
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	// Darwin limits socket paths to 104 bytes, which t.TempDir there
	// nearly fills.
	dir, err := os.MkdirTemp("", "kts-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Mkdir(filepath.Join(dir, "terminals"), 0o755); err != nil {
		t.Fatal(err)
	}
	pids := t.TempDir()
	socket := filepath.Join(dir, "terminals", "t.sock")
	client := exec.Command(bin, socket, "--", "bash", "-c", terminalScript, "fake", pids)
	term := startTerminal(t, socket, pids, func() error {
		master, err := pty.Start(client, 80, 24)
		if err != nil {
			return err
		}
		go io.Copy(io.Discard, master)
		t.Cleanup(func() {
			client.Process.Kill()
			client.Wait()
			master.Close()
		})
		return nil
	})

	killTerminalSessions(dir)

	waitKilled(t, map[string]int{
		"the server": term.server, "its shell": term.shell,
		"a process in the shell's group": term.member, "the shell's job": term.job,
	})
}

func TestHermeticGitEnv(t *testing.T) {
	set := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	for _, tt := range []struct {
		name      string
		env, want []string
	}{{
		name: "another repository",
		env: []string{
			"HOME=/home/runner", "GIT_DIR=/repo/.git", "GIT_WORK_TREE=/repo", "GIT_INDEX_FILE=/repo/.git/index",
			"GIT_OBJECT_DIRECTORY=/repo/.git/objects", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/alt",
			"GIT_COMMON_DIR=/repo/.git", "GIT_NAMESPACE=ns", "GIT_TEMPLATE_DIR=/templates",
		},
		want: append([]string{"HOME=/home/runner"}, set...),
	}, {
		name: "the runner's identity",
		env: []string{
			"EMAIL=runner@example.com", "GIT_AUTHOR_NAME=Runner", "GIT_AUTHOR_EMAIL=runner@example.com",
			"GIT_AUTHOR_DATE=@0", "GIT_COMMITTER_NAME=Runner", "GIT_COMMITTER_EMAIL=runner@example.com",
			"GIT_COMMITTER_DATE=@0", "PATH=/usr/bin",
		},
		want: append([]string{"PATH=/usr/bin"}, set...),
	}, {
		name: "the runner's configuration",
		env: []string{
			"GIT_CONFIG_GLOBAL=/home/runner/.gitconfig", "GIT_CONFIG_NOSYSTEM=0",
			"GIT_CONFIG_PARAMETERS='core.hookspath'='/hooks'", "GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/hooks",
			"GIT_CONFIG_KEY_1=commit.gpgSign", "GIT_CONFIG_VALUE_1=true", "LANG=C",
		},
		want: append([]string{"LANG=C"}, set...),
	}, {
		name: "keeps the rest",
		env:  []string{"HOME=/home/runner", "EMAILS=x", "GIT_DIRS=x", "OPTS=a=b"},
		want: append([]string{"HOME=/home/runner", "EMAILS=x", "GIT_DIRS=x", "OPTS=a=b"}, set...),
	}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := hermeticGitEnv(tt.env); !slices.Equal(got, tt.want) {
				t.Errorf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestHermeticGitEnvIsolatesGit points each way git finds configuration
// at a commit-msg hook that fails, and GIT_DIR at another repository: with
// hermeticGitEnv, git init and commit work in their directory regardless.
func TestHermeticGitEnvIsolatesGit(t *testing.T) {
	fixture, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hooks, home, wrong := filepath.Join(fixture, "hooks"), filepath.Join(fixture, "home"), filepath.Join(fixture, "wrong")
	config := []byte("[core]\n\thooksPath = " + hooks + "\n")
	for _, err := range []error{
		os.Mkdir(hooks, 0o755), os.Mkdir(home, 0o755), os.Mkdir(wrong, 0o755),
		os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\necho hostile hook ran >&2\nexit 1\n"), 0o755),
		os.WriteFile(filepath.Join(home, ".gitconfig"), config, 0o644),
		os.WriteFile(filepath.Join(fixture, "gitconfig"), config, 0o644),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	git := func(env []string, dir string, args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Shelley Test", "-c", "user.email=test@example.com"}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// commit makes a repository with a commit in a new directory, and
	// returns where git put the repository and the directory.
	commit := func(env []string) (got, dir, out string, err error) {
		dir, _ = filepath.EvalSymlinks(t.TempDir())
		if out, err := git(env, dir, "init", "--quiet"); err != nil {
			return "", dir, out, err
		}
		if out, err := git(env, dir, "commit", "--quiet", "--allow-empty", "-m", "fixture"); err != nil {
			return "", dir, out, err
		}
		out, err = git(env, dir, "rev-parse", "--absolute-git-dir")
		return strings.TrimSpace(out), dir, out, err
	}
	// Without hermeticGitEnv, start from no git variables, an empty home
	// and no system configuration.
	clean := append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") }),
		"HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir(), "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := git(clean, wrong, "init", "--quiet"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	ways := map[string][]string{
		"HOME":                  {"HOME=" + home},
		"GIT_CONFIG_GLOBAL":     {"GIT_CONFIG_GLOBAL=" + filepath.Join(fixture, "gitconfig")},
		"GIT_CONFIG_SYSTEM":     {"GIT_CONFIG_SYSTEM=" + filepath.Join(fixture, "gitconfig")},
		"GIT_CONFIG_PARAMETERS": {"GIT_CONFIG_PARAMETERS='core.hookspath'='" + hooks + "'"},
		"GIT_CONFIG_COUNT":      {"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=" + hooks},
	}
	var all []string
	for _, name := range slices.Sorted(maps.Keys(ways)) {
		all = append(all, ways[name]...)
		// Each way on its own reaches git without hermeticGitEnv.
		if _, _, out, err := commit(append(slices.Clone(clean), ways[name]...)); err == nil || !strings.Contains(out, "hostile hook ran") {
			t.Fatalf("without hermeticGitEnv, the hook through %s did not stop a commit (git before 2.32 ignores some ways): %v\n%s", name, err, out)
		}
	}
	gitDir := "GIT_DIR=" + filepath.Join(wrong, ".git")
	if got, _, out, err := commit(append(slices.Clone(clean), gitDir)); err != nil || got != filepath.Join(wrong, ".git") {
		t.Fatalf("without hermeticGitEnv, GIT_DIR did not move the repository to %s: %q, %v\n%s", wrong, got, err, out)
	}
	all = append(all, gitDir)

	got, dir, out, err := commit(hermeticGitEnv(append(os.Environ(), all...)))
	if err != nil {
		t.Fatalf("with hermeticGitEnv: %v\n%s", err, out)
	}
	if want := filepath.Join(dir, ".git"); got != want {
		t.Errorf("the repository is %s, not %s", got, want)
	}
}
