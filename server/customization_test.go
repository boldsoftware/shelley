package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
)

type customizationFixture struct {
	s             *Server
	database      *db.DB
	id, cwd, live string
	build         string // the dedicated build worktree
	exited        chan int
}

func customizationTestGit(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newCustomizationFixture(t *testing.T) *customizationFixture {
	t.Helper()
	s, database, _ := newTestServer(t)
	root := t.TempDir()
	cwd := filepath.Join(root, "checkout")
	live := filepath.Join(root, "live-shelley")
	s.customizations.deps = customizationDependencies{
		canonicalDir:      func() (string, error) { return cwd, nil },
		buildDir:          func() (string, error) { return filepath.Join(root, "customization-build"), nil },
		artifactsDir:      func() (string, error) { return filepath.Join(root, "artifacts"), nil },
		runningExecutable: func() (string, error) { return live, nil },
		targetExecutable:  func() (string, error) { return live, nil },
		invocationID:      func() string { return "test-invocation" },
		run:               successfulCustomizationBuild,
		install:           copyCustomizationFixture,
		installedCommit: func(context.Context, string) (string, error) {
			content, err := os.ReadFile(live)
			if commit, ok := strings.CutPrefix(string(content), verifiedExecutable); ok {
				return commit, err
			}
			return customizationTestGit(t, cwd, "rev-list", "--max-parents=0", "HEAD"), err
		},
	}
	db.EnableWorkspaceMode(t, s.db)
	if _, err := s.ensureWorkspace(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, cwd, "init", "--initial-branch=custom")
	if err := os.WriteFile(filepath.Join(cwd, ".gitignore"), []byte("bin/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, cwd, "add", ".gitignore")
	customizationTestGit(t, cwd, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
	project, err := database.GetProject(t.Context(), "Shelley")
	if err != nil {
		t.Fatal(err)
	}
	project.Cwd = cwd
	if err := database.UpdateProject(t.Context(), project.Name, *project); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.CreateConversation(t.Context(), nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MoveConversation(t.Context(), conversation.ConversationID, "Shelley"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("old running executable"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureCustomizations(t.Context()); err != nil {
		t.Fatal(err)
	}
	f := &customizationFixture{s: s, database: database, id: conversation.ConversationID, cwd: cwd, live: live, build: filepath.Join(root, "customization-build"), exited: make(chan int, 1)}
	s.exitDelay = 0
	s.exitProcess = func(code int) { f.exited <- code }
	t.Cleanup(func() {
		select {
		case <-s.shutdownCh:
		default:
			close(s.shutdownCh)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.waitActions(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}

// The fixture "executable" names the commit it was built from.
const verifiedExecutable = "verified executable of "

func successfulCustomizationBuild(_ context.Context, cwd string, args []string, output io.Writer) error {
	if _, err := fmt.Fprintln(output, "verified "+strings.Join(args, " ")); err != nil {
		return err
	}
	if reflect.DeepEqual(args, []string{"make", "build-custom"}) {
		if err := os.MkdirAll(filepath.Join(cwd, "bin"), 0o700); err != nil {
			return err
		}
		commit, err := exec.Command("git", "-C", cwd, "rev-parse", "HEAD").Output()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(cwd, "bin", "shelley"), []byte(verifiedExecutable+strings.TrimSpace(string(commit))), 0o751)
	}
	return nil
}

func copyCustomizationFixture(_ context.Context, artifact, destination, _ string) error {
	content, err := os.ReadFile(artifact)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, content, 0o751)
}

func (f *customizationFixture) verify(t *testing.T) *db.Customization {
	t.Helper()
	commit, err := customizationHead(t.Context(), f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.database.BeginCustomization(t.Context(), f.id, commit, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.verifyCustomization(t.Context(), state, f.cwd); err != nil {
		t.Fatal(err)
	}
	return f.state(t)
}

func (f *customizationFixture) state(t *testing.T) *db.Customization {
	t.Helper()
	state, err := f.database.GetCustomization(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func restartCustomizationBody(t *testing.T, state *db.Customization) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"commit": state.Commit, "artifact_sha256": state.ArtifactSHA256})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestCustomizationVerificationFixedPipelineAndImmutableCopy(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	initial := workspaceResponse[[]db.Customization](t, workspaceAPI(f.s, "GET", "/api/customizations", ""), http.StatusOK)
	if initial == nil || len(initial) != 0 {
		t.Fatalf("conversation existence is not verified readiness: %+v", initial)
	}
	var commands [][]string
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, out io.Writer) error {
		commands = append(commands, append([]string{}, args...))
		return successfulCustomizationBuild(ctx, cwd, args, out)
	}
	state := f.verify(t)
	if state.Status != "installed" || state.VerifiedAt == nil || state.InstalledAt == nil || len(state.ArtifactSHA256) != 64 || state.ArtifactPath == "" {
		t.Fatalf("state=%+v", state)
	}
	if sha, err := customizationFileSHA(f.live); err != nil || sha != state.ArtifactSHA256 {
		t.Fatalf("verified build not installed: sha=%s err=%v", sha, err)
	}
	if !reflect.DeepEqual(commands, customizationVerificationCommands) {
		t.Fatalf("commands=%v", commands)
	}
	if err := os.WriteFile(filepath.Join(f.build, "bin", "shelley"), []byte("next build"), 0o751); err != nil {
		t.Fatal(err)
	}
	sha, err := customizationFileSHA(state.ArtifactPath)
	if err != nil || sha != state.ArtifactSHA256 {
		t.Fatalf("immutable artifact changed: sha=%s err=%v", sha, err)
	}
	response := workspaceAPI(f.s, "GET", "/api/customizations", "")
	states := workspaceResponse[[]db.Customization](t, response, http.StatusOK)
	if len(states) != 1 || states[0].Status != "installed" || !states[0].RunningUnderSystemd || states[0].Output != "" {
		t.Fatalf("states=%+v", states)
	}
	if strings.Contains(response.Body.String(), state.ArtifactPath) {
		t.Fatal("API leaked private artifact path")
	}
	// The conversation's own state carries the output; the list leaves it out.
	own := workspaceResponse[db.Customization](t, workspaceAPI(f.s, "GET", "/api/conversation/"+f.id+"/customization", ""), http.StatusOK)
	if own.Status != "installed" || !strings.Contains(own.Output, "verified make build-custom") {
		t.Fatalf("own=%+v", own)
	}
	if _, err := f.database.ArchiveConversation(t.Context(), f.id); err != nil {
		t.Fatal(err)
	}
	if states := workspaceResponse[[]db.Customization](t, workspaceAPI(f.s, "GET", "/api/customizations", ""), http.StatusOK); len(states) != 0 {
		t.Fatalf("archived conversation listed: %+v", states)
	}
}

func TestCustomizationFailuresNeverReady(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"type-check", "lint", "test", "build-custom", "missing-artifact", "dirty-after-build", "head-after-build", "invalid-artifact-dir"} {
		t.Run(stage, func(t *testing.T) {
			f := newCustomizationFixture(t)
			f.verify(t) // Leaves a previous binary in the build worktree.
			if stage == "invalid-artifact-dir" {
				f.s.customizations.deps.artifactsDir = func() (string, error) { return filepath.Join(f.build, "bin", "artifacts"), nil }
			}
			f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, output io.Writer) error {
				if stage != "missing-artifact" && strings.Join(args, " ") == "make build-custom" {
					if err := successfulCustomizationBuild(ctx, cwd, args, output); err != nil {
						return err
					}
					if stage == "dirty-after-build" {
						return os.WriteFile(filepath.Join(cwd, "dirty"), []byte("uncommitted"), 0o600)
					}
					if stage == "head-after-build" {
						customizationTestGit(t, cwd, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "new HEAD")
						return nil
					}
				}
				for _, arg := range args {
					if arg == stage {
						return errors.New("fixture command failed")
					}
				}
				_, err := fmt.Fprintln(output, "fixture output")
				return err
			}
			commit, err := customizationHead(t.Context(), f.cwd)
			if err != nil {
				t.Fatal(err)
			}
			state, err := f.database.BeginCustomization(t.Context(), f.id, commit, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.verifyCustomization(t.Context(), state, f.cwd); err == nil {
				t.Fatal("verification unexpectedly succeeded")
			}
			state = f.state(t)
			// The commit itself failed (including a build that changes its own
			// sources), or the environment did (stale: re-verified automatically).
			want := "failed"
			if stage == "invalid-artifact-dir" {
				want = "stale"
			}
			if state.Status != want || state.Error == "" || state.ArtifactSHA256 != "" || state.VerifiedAt != nil {
				t.Fatalf("failed verification became ready: %+v", state)
			}
			if state.Output == "" {
				t.Fatal("missing failure output")
			}
		})
	}
}

func TestCustomizationAsyncVerificationInterruptedByRestarts(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, output io.Writer) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return successfulCustomizationBuild(ctx, cwd, args, output)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", `{"tests":"claimed successful"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("verification accepted a claim: %d %s", response.Code, response.Body.String())
	}
	response = workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}")
	state := workspaceResponse[db.Customization](t, response, http.StatusAccepted)
	if state.Status != "verifying" {
		t.Fatalf("status=%s", state.Status)
	}
	awaitConversationSignal(t, started)
	response = workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}")
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate verification status=%d: %s", response.Code, response.Body.String())
	}
	// A restart interrupts verification instead of waiting for it; the
	// interrupted build is queued, not failed, and runs again afterwards.
	if err := f.s.prepareRestart(t.Context()); err != nil {
		t.Fatalf("restart during verification: %v", err)
	}
	if state := f.state(t); state.Status != "waiting" || state.Error != "" {
		t.Fatalf("interrupted state=%+v", state)
	}
	f.s.releaseRestart()
	close(release)
	if state := f.poll(t)[f.id]; state.Status != "installed" {
		t.Fatalf("state=%+v", state)
	}
}

func TestCustomizationShutdownInterruptsVerification(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	started := make(chan struct{})
	f.s.customizations.deps.run = func(ctx context.Context, _ string, _ []string, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}"), http.StatusAccepted)
	awaitConversationSignal(t, started)
	close(f.s.shutdownCh)
	f.s.workspace.workers.Wait()
	if state := f.state(t); state.Status != "waiting" || state.Error != "" {
		t.Fatalf("state=%+v", state)
	}
}

func TestCustomizationReadyInvalidation(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"artifact", "missing-artifact", "directory", "project"} {
		t.Run(change, func(t *testing.T) {
			f := newCustomizationFixture(t)
			state := f.verify(t)
			switch change {
			case "artifact":
				if err := os.Chmod(state.ArtifactPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(state.ArtifactPath, []byte("corrupted"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing-artifact":
				if err := os.Remove(state.ArtifactPath); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := f.database.Pool().Exec(t.Context(), "UPDATE conversations SET cwd=? WHERE conversation_id=?", t.TempDir(), f.id); err != nil {
					t.Fatal(err)
				}
			case "project":
				db.AddTestProject(t, f.database, "my-project")
				if err := f.database.MoveConversation(t.Context(), f.id, "my-project"); err != nil {
					t.Fatal(err)
				}
			}
			// Listing changes nothing; reconciling (after a turn) does.
			if listed := workspaceResponse[[]db.Customization](t, workspaceAPI(f.s, "GET", "/api/customizations", ""), http.StatusOK); len(listed) != 1 || listed[0].Status != "installed" {
				t.Fatalf("listing reconciled: %+v", listed)
			}
			f.reconcile(t)
			states, err := f.database.ListCustomizations(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if change == "artifact" || change == "missing-artifact" {
				// The custom branch tip is re-verified automatically.
				if len(states) != 1 || states[0].Status != "verifying" {
					t.Fatalf("not re-verified: %+v", states)
				}
				f.s.workspace.workers.Wait()
				if healed := f.state(t); healed.Status != "installed" || healed.Commit != customizationTestGit(t, f.cwd, "rev-parse", "HEAD") {
					t.Fatalf("not healed: %+v", healed)
				}
				return
			}
			if len(states) != 1 || states[0].Status != "stale" || states[0].Error == "" {
				t.Fatalf("state not invalidated: %+v", states)
			}
			response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/restart", restartCustomizationBody(t, state))
			if response.Code != http.StatusConflict {
				t.Fatalf("stale restart status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCustomizationRestartExplicitAuthorizationAndFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing-authorization", "wrong-commit", "wrong-sha", "no-systemd", "stale-without-get", "replaced-executable", "target-discovery", "already-restarting"} {
		t.Run(failure, func(t *testing.T) {
			f := newCustomizationFixture(t)
			state := f.verify(t)
			body := restartCustomizationBody(t, state)
			var installed bool
			f.s.customizations.deps.install = func(context.Context, string, string, string) error {
				installed = true
				return errors.New("restart must not install")
			}
			switch failure {
			case "missing-authorization":
				body = "{}"
			case "wrong-commit":
				state.Commit = "wrong"
				body = restartCustomizationBody(t, state)
			case "wrong-sha":
				state.ArtifactSHA256 = "wrong"
				body = restartCustomizationBody(t, state)
			case "no-systemd":
				f.s.customizations.deps.invocationID = func() string { return "" }
			case "stale-without-get":
				if err := os.Remove(state.ArtifactPath); err != nil {
					t.Fatal(err)
				}
			case "replaced-executable":
				if err := os.WriteFile(f.live, []byte("someone else's build"), 0o751); err != nil {
					t.Fatal(err)
				}
			case "target-discovery":
				f.s.customizations.deps.targetExecutable = func() (string, error) { return "", errors.New("discovery failed") }
			case "already-restarting":
				// /exit's fence does not refuse a second /exit; a
				// customization restart refuses either.
				for range 2 {
					if err := f.s.prepareRestart(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
			}
			response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/restart", body)
			if response.Code < 400 {
				t.Fatalf("unauthorized/failed restart succeeded: %d %s", response.Code, response.Body.String())
			}
			if installed {
				t.Fatal("restart installed")
			}
			current := f.state(t)
			switch failure {
			case "target-discovery":
				// Still installed; the error explains why it did not restart.
				if current.Status != "installed" || current.Error == "" {
					t.Fatalf("restart failure not persisted: %+v", current)
				}
			case "stale-without-get", "replaced-executable":
				if current.Status != "stale" || current.Error == "" {
					t.Fatalf("changed build not invalidated: %+v", current)
				}
			}
			marker, err := f.database.GetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey)
			if err != nil || marker != "" {
				t.Fatalf("failed restart left resume marker=%q err=%v", marker, err)
			}
			select {
			case <-f.exited:
				t.Fatal("failed restart exited")
			default:
			}
		})
	}
}

func TestCustomizationRestartRunsInstalledBuildAndProvesStartup(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	state := f.verify(t)
	f.s.customizations.deps.install = func(context.Context, string, string, string) error {
		return errors.New("restart must not install")
	}
	response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/restart", restartCustomizationBody(t, state))
	result := workspaceResponse[db.Customization](t, response, http.StatusAccepted)
	if result.Status != "restarting" {
		t.Fatalf("response prematurely claims installed: %+v", result)
	}
	marker, err := f.database.GetSetting(t.Context(), db.ResumeAfterUpgradeSettingKey)
	if err != nil || marker != "1" {
		t.Fatalf("active conversations not marked for resume: marker=%q err=%v", marker, err)
	}
	select {
	case code := <-f.exited:
		if code != restartExitCode {
			t.Fatalf("exit code=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart exit not scheduled")
	}
	if state := f.state(t); state.Status != "restarting" {
		t.Fatalf("no new-process proof yet: %+v", state)
	}
	for operation, body := range map[string]string{"verify": "{}", "restart": restartCustomizationBody(t, state)} {
		duplicate := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/"+operation, body)
		if duplicate.Code != http.StatusConflict {
			t.Fatalf("%s during reserved restart: %d %s", operation, duplicate.Code, duplicate.Body.String())
		}
	}
	// This second server only hashes a temporary executable; it never serves,
	// installs to the real process, or invokes os.Exit.
	next := &Server{db: f.database, logger: f.s.logger}
	next.customizations.deps = f.s.customizations.deps
	if err := next.ensureCustomizations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if proven := f.state(t); proven.Status != "running" {
		t.Fatalf("startup did not prove installed binary: %+v", proven)
	}
}

func (f *customizationFixture) actionConversation(t *testing.T) string {
	t.Helper()
	project, err := f.database.CreateProject(t.Context(), db.Project{
		Name: "Command", Cwd: f.cwd, Actions: []db.ProjectAction{{ID: "run", Label: "Run", Command: "true"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := f.database.CreateConversation(t.Context(), nil, true, &f.cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.MoveConversation(t.Context(), conversation.ConversationID, project.Name); err != nil {
		t.Fatal(err)
	}
	return conversation.ConversationID
}

func TestCustomizationRestartRejectsRunningAction(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	state := f.verify(t)
	conversation := f.actionConversation(t)
	run, _, _, err := f.database.BeginAction(t.Context(), conversation, "run")
	if err != nil {
		t.Fatal(err)
	}
	response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/restart", restartCustomizationBody(t, state))
	if response.Code != http.StatusConflict || f.state(t).Status != "installed" {
		t.Fatalf("running action discarded: %d %s", response.Code, response.Body.String())
	}
	// Verification builds in its own worktree; actions don't hold it up.
	response = workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}")
	workspaceResponse[db.Customization](t, response, http.StatusAccepted)
	f.s.workspace.workers.Wait()
	if err := f.database.InterruptActionRun(t.Context(), run.ID, "test finished"); err != nil {
		t.Fatal(err)
	}
}

func TestCustomizationAutoInstallFailureIsVisible(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	f.s.customizations.deps.install = func(context.Context, string, string, string) error {
		return errors.New("sudo -n: a password is required")
	}
	state := f.verify(t)
	if state.Status != "ready" || !strings.Contains(state.Error, "Install failed: sudo -n: a password is required") {
		t.Fatalf("state=%+v", state)
	}
	if f.archived(t, f.id) {
		t.Fatal("failed install archived the conversation")
	}
	// The next reconciliation installs again, without verifying again.
	f.s.customizations.deps.install = copyCustomizationFixture
	f.s.customizations.deps.run = func(context.Context, string, []string, io.Writer) error {
		return errors.New("an install retry must not rebuild")
	}
	if state := f.poll(t)[f.id]; state.Status != "installed" || state.Error != "" {
		t.Fatalf("retry state=%+v", state)
	}
}

func TestCustomizationAutoInstallNeverDowngrades(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	old := customizationTestGit(t, f.cwd, "rev-parse", "HEAD")
	newer := f.commit(t, "installed elsewhere")
	customizationTestGit(t, f.cwd, "reset", "-q", "--hard", old)
	f.s.customizations.deps.installedCommit = func(context.Context, string) (string, error) { return newer, nil }
	f.s.customizations.deps.install = func(context.Context, string, string, string) error {
		return errors.New("downgrade installed")
	}
	state := f.verify(t)
	if state.Status != "ready" || !strings.Contains(state.Error, "downgrade") || state.Commit != old {
		t.Fatalf("state=%+v", state)
	}
}

func TestCustomizationInstallAtomicAndPreservesMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destination, artifact := filepath.Join(dir, "live"), filepath.Join(dir, "artifact")
	if err := os.WriteFile(destination, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("new"), 0o500); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	sha, err := customizationFileSHA(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := installCustomizationArtifact(t.Context(), artifact, destination, sha); err != nil {
		t.Fatal(err)
	}
	stillOld, err := io.ReadAll(old)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(stillOld) != "old" || string(now) != "new" || info.Mode().Perm() != 0o751 {
		t.Fatalf("non-atomic install or wrong mode: old=%s new=%s mode=%v", stillOld, now, info.Mode())
	}
	// A leftover staging file (a crash, a manual install) is left alone.
	if err := os.WriteFile(destination+".new", []byte("existing staging file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installCustomizationArtifact(t.Context(), artifact, destination, sha); err != nil {
		t.Fatal(err)
	}
	if leftover, err := os.ReadFile(destination + ".new"); err != nil || string(leftover) != "existing staging file" {
		t.Fatalf("leftover staging file changed: %q %v", leftover, err)
	}
	if err := installCustomizationArtifact(t.Context(), artifact, destination, "incorrect digest"); err == nil {
		t.Fatal("installed wrong artifact")
	}
}

func TestCustomizationInstallFailureCleansOnlyItsOwnStage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destination, artifact := filepath.Join(dir, "live"), filepath.Join(dir, "artifact")
	// The non-executable live mode deliberately makes staged validation fail
	// after copying. No production executable or privileged directory is used.
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("new"), 0o500); err != nil {
		t.Fatal(err)
	}
	sha, err := customizationFileSHA(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := installCustomizationArtifact(t.Context(), artifact, destination, sha); err == nil {
		t.Fatal("expected staged validation failure")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 2 {
		t.Fatalf("failed installation left its staging file: %v %v", entries, err)
	}
	old, err := os.ReadFile(destination)
	if err != nil || string(old) != "old" {
		t.Fatalf("failed installation replaced live binary: %q %v", old, err)
	}
	if err := os.Chmod(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installCustomizationArtifact(t.Context(), artifact, destination, sha); err != nil {
		t.Fatalf("clean retry failed: %v", err)
	}
}

func TestCustomizationInstallCleanupErrorsPropagate(t *testing.T) {
	dir := t.TempDir()
	destination, artifact := filepath.Join(dir, "live"), filepath.Join(dir, "artifact")
	for path, mode := range map[string]os.FileMode{destination: 0o600, artifact: 0o500} {
		if err := os.WriteFile(path, []byte("test executable"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "rm"), []byte("#!/bin/sh\necho cleanup-denied >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	sha, err := customizationFileSHA(artifact)
	if err != nil {
		t.Fatal(err)
	}
	err = installCustomizationArtifact(t.Context(), artifact, destination, sha)
	if err == nil || !strings.Contains(err.Error(), "not a regular executable") || !strings.Contains(err.Error(), "cleanup-denied") {
		t.Fatalf("cleanup error hidden: %v", err)
	}
}

func TestCustomizationVerificationRequiresCanonicalCustomConversation(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"project", "directory"} {
		t.Run(invalid, func(t *testing.T) {
			f := newCustomizationFixture(t)
			switch invalid {
			case "project":
				db.AddTestProject(t, f.database, "my-project")
				if err := f.database.MoveConversation(t.Context(), f.id, "my-project"); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := f.database.Pool().Exec(t.Context(), "UPDATE conversations SET cwd=? WHERE conversation_id=?", t.TempDir(), f.id); err != nil {
					t.Fatal(err)
				}
			}
			response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}")
			if response.Code != http.StatusBadRequest && response.Code != http.StatusConflict {
				t.Fatalf("verification accepted invalid conversation: %d %s", response.Code, response.Body.String())
			}
			if _, err := f.database.GetCustomization(t.Context(), f.id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("invalid conversation published verification: %v", err)
			}
		})
	}
}

func TestCustomizationNeedsWorkspaceMode(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	setWorkspaceFlag(t, f.s, "")
	for _, path := range []string{"/api/conversation/" + f.id + "/customization/verify", "/api/customizations"} {
		method := "POST"
		if path == "/api/customizations" {
			method = "GET"
		}
		if response := workspaceAPI(f.s, method, path, ""); response.Code != http.StatusNotFound {
			t.Fatalf("%s %s outside workspace mode: %d %s", method, path, response.Code, response.Body.String())
		}
	}
	project, err := f.database.GetConversationProject(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	if shelley, err := f.s.isShelleyProject(t.Context(), project); err != nil || shelley {
		t.Fatalf("the Shelley project customizes Shelley outside workspace mode: %v", err)
	}
	if scheduled, wait := f.s.autoRestartTick(t.Context()); scheduled || wait != 0 {
		t.Fatal("auto-restart ran outside workspace mode")
	}
}

// Output is persisted after each step, and a verification whose record is
// gone (superseded) stops there instead of going on toward a ready result.
func TestCustomizationOutputPersistedPerStep(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	commit, err := customizationHead(t.Context(), f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.database.BeginCustomization(t.Context(), f.id, commit, true)
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	f.s.customizations.deps.run = func(_ context.Context, _ string, args []string, out io.Writer) error {
		ran = append(ran, strings.Join(args, " "))
		switch len(ran) {
		case 1:
			_, err := out.Write([]byte(strings.Repeat("output", db.MaxActionOutput)))
			return err
		case 2:
			if persisted := f.state(t).Output; persisted == "" || len(persisted) > db.MaxActionOutput {
				t.Errorf("first step's output not persisted (bounded): %d bytes", len(persisted))
			}
			return f.database.Pool().Exec(t.Context(), "DELETE FROM shelley_customizations WHERE conversation_id=?", f.id)
		}
		return nil
	}
	if err := f.s.verifyCustomization(t.Context(), state, f.cwd); err == nil {
		t.Fatal("output persistence failure hidden")
	}
	if len(ran) != 2 {
		t.Fatalf("verification went on after its record was gone: %v", ran)
	}
}

func TestCustomizationVerificationNeverBuildsOverLiveExecutable(t *testing.T) {
	t.Parallel()
	for _, alias := range []string{"same-path", "hardlink", "symlink"} {
		t.Run(alias, func(t *testing.T) {
			f := newCustomizationFixture(t)
			if err := os.MkdirAll(filepath.Join(f.build, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			built := filepath.Join(f.build, "bin", "shelley")
			switch alias {
			case "same-path":
				if err := os.WriteFile(built, []byte("live binary"), 0o700); err != nil {
					t.Fatal(err)
				}
				f.s.customizations.deps.targetExecutable = func() (string, error) { return built, nil }
			case "hardlink":
				if err := os.Link(f.live, built); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(f.live, built); err != nil {
					t.Fatal(err)
				}
			}
			response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "live executable") {
				t.Fatalf("verification could replace live binary: %d %s", response.Code, response.Body.String())
			}
			if _, err := f.database.GetCustomization(t.Context(), f.id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unsafe verification published state: %v", err)
			}
		})
	}
}

func (f *customizationFixture) conversation(t *testing.T) string {
	t.Helper()
	conversation, err := f.database.CreateConversation(t.Context(), nil, true, &f.cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.MoveConversation(t.Context(), conversation.ConversationID, "Shelley"); err != nil {
		t.Fatal(err)
	}
	return conversation.ConversationID
}

func (f *customizationFixture) commit(t *testing.T, message string) string {
	t.Helper()
	customizationTestGit(t, f.cwd, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", message)
	return customizationTestGit(t, f.cwd, "rev-parse", "HEAD")
}

// poll reconciles as after a turn, waits for what that started, and returns
// every conversation's state.
func (f *customizationFixture) poll(t *testing.T) map[string]db.Customization {
	t.Helper()
	f.reconcile(t)
	f.s.workspace.workers.Wait()
	states, err := f.database.ListCustomizations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byConversation := map[string]db.Customization{}
	for _, state := range states {
		byConversation[state.ConversationID] = state
	}
	return byConversation
}

func (f *customizationFixture) reconcile(t *testing.T) {
	t.Helper()
	if err := f.s.reconcileCustomizationsNow(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f *customizationFixture) archived(t *testing.T, id string) bool {
	t.Helper()
	conversation, err := f.database.GetConversationByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return conversation.Archived
}

func TestCustomizationReadyConversationsUpgradeToNewerBuild(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other, committer := f.conversation(t), f.conversation(t)
	f.verify(t)
	// other's work goes on after this build; the rest is finished.
	if _, err := f.s.startCustomizationVerification(t.Context(), other, false); err != nil {
		t.Fatal(err)
	}
	f.s.workspace.workers.Wait()
	builds := 0
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, out io.Writer) error {
		if args[0] == "make" && args[1] == "build-custom" {
			builds++
		}
		return successfulCustomizationBuild(ctx, cwd, args, out)
	}
	// Another conversation commits: ready conversations keep their build; nothing rebuilds
	// until that conversation asks.
	old := f.state(t)
	head := f.commit(t, "another conversation's work")
	states := f.poll(t)
	if builds != 0 || states[f.id].Status != "installed" || states[f.id].Commit != old.Commit || states[other].Status != "installed" {
		t.Fatalf("builds=%d states=%+v", builds, states)
	}
	// Its verification upgrades every ready conversation it contains to one artifact.
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+committer+"/customization/verify", `{"finished":true}`), http.StatusAccepted)
	states = f.poll(t)
	if builds != 1 || len(states) != 3 {
		t.Fatalf("builds=%d states=%+v", builds, states)
	}
	for _, state := range states {
		if state.Status != "installed" || state.Commit != head || state.ArtifactSHA256 != states[committer].ArtifactSHA256 {
			t.Fatalf("not shared: %+v", states)
		}
	}
	// Restart installs it; the replacement process archives every finished
	// conversation whose verified build is running.
	if err := f.database.RecoverCustomizations(t.Context(), states[f.id].ArtifactSHA256, states[f.id].ArtifactSHA256); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.id, other, committer} {
		if f.archived(t, id) != (id != other) {
			t.Fatalf("%s archived=%v", id, f.archived(t, id))
		}
	}
}

func TestCustomizationFailedHeadNeverDowngradesReadyConversations(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other := f.conversation(t)
	ready := f.verify(t)
	lints := 0
	f.s.customizations.deps.run = func(_ context.Context, _ string, args []string, _ io.Writer) error {
		if slices.Contains(args, "lint") {
			lints++
			return errors.New("lint failed")
		}
		return nil
	}
	head := f.commit(t, "broken")
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+other+"/customization/verify", "{}"), http.StatusAccepted)
	states := f.poll(t)
	if failed := states[other]; failed.Status != "failed" || failed.Commit != head || !strings.Contains(failed.Error, "lint") {
		t.Fatalf("failure not reported: %+v", failed)
	}
	if kept := states[f.id]; kept.Status != "installed" || kept.Commit != ready.Commit || kept.Error != "" {
		t.Fatalf("ready conversation downgraded: %+v", kept)
	}
	f.poll(t)
	if lints != 1 {
		t.Fatalf("failed HEAD retried automatically: %d", lints)
	}
	// An explicit request still retries; the fix commit heals automatically
	// and upgrades the ready conversation.
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+other+"/customization/verify", "{}"), http.StatusAccepted)
	f.s.workspace.workers.Wait()
	if lints != 2 {
		t.Fatalf("explicit verification did not run: %d", lints)
	}
	f.s.customizations.deps.run = successfulCustomizationBuild
	fixed := f.commit(t, "fix")
	for _, state := range f.poll(t) {
		if state.Status != "installed" || state.Commit != fixed {
			t.Fatalf("fix did not heal: %+v", state)
		}
	}
}

func TestCustomizationInstallArchivesOnlyConversationsInBuild(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other := f.conversation(t)
	ready := f.verify(t)
	f.commit(t, "other conversation's work")
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, out io.Writer) error {
		if slices.Contains(args, "lint") {
			return errors.New("lint failed")
		}
		return successfulCustomizationBuild(ctx, cwd, args, out)
	}
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+other+"/customization/verify", "{}"), http.StatusAccepted)
	f.poll(t)
	if err := f.database.RecoverCustomizations(t.Context(), ready.ArtifactSHA256, ready.ArtifactSHA256); err != nil {
		t.Fatal(err)
	}
	if !f.archived(t, f.id) || f.archived(t, other) {
		t.Fatalf("archived installed=%v other=%v", f.archived(t, f.id), f.archived(t, other))
	}
}

// Another conversation's uncommitted edits, branch switch, or rebase in the shared
// checkout never affect readiness, verification, or restart.
func TestCustomizationIgnoresInProgressCheckoutWork(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other := f.conversation(t)
	ready := f.verify(t)
	tip := f.commit(t, "committed work")
	customizationTestGit(t, f.cwd, "switch", "-c", "in-progress")
	f.commit(t, "unfinished branch work")
	if err := os.WriteFile(filepath.Join(f.cwd, "dirty"), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state := f.poll(t)[f.id]; state.Status != "installed" || state.Commit != ready.Commit || state.Error != "" {
		t.Fatalf("in-progress work changed ready conversation: %+v", state)
	}
	var sawDirty bool
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, out io.Writer) error {
		if _, err := os.Stat(filepath.Join(cwd, "dirty")); err == nil {
			sawDirty = true
		}
		return successfulCustomizationBuild(ctx, cwd, args, out)
	}
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+other+"/customization/verify", "{}"), http.StatusAccepted)
	states := f.poll(t)
	if sawDirty || states[other].Status != "installed" || states[other].Commit != tip || states[f.id].Commit != tip {
		t.Fatalf("sawDirty=%v states=%+v", sawDirty, states)
	}
	if built := customizationTestGit(t, f.build, "rev-parse", "HEAD"); built != tip {
		t.Fatalf("built %s, want custom tip %s", built, tip)
	}
	state := f.state(t)
	if sha, err := customizationFileSHA(f.live); err != nil || sha != state.ArtifactSHA256 {
		t.Fatalf("tip not installed: %s %v", sha, err)
	}
	response := workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/restart", restartCustomizationBody(t, state))
	workspaceResponse[db.Customization](t, response, http.StatusAccepted)
	if code := <-f.exited; code != restartExitCode {
		t.Fatalf("exit code %d", code)
	}
}

func TestCustomizationVerifyQueuesBehindRunningVerification(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other := f.conversation(t)
	started, release := make(chan struct{}), make(chan struct{})
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, out io.Writer) error {
		if args[0] == "make" && args[1] == "build-custom" {
			close(started)
			<-release
		}
		return successfulCustomizationBuild(ctx, cwd, args, out)
	}
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+f.id+"/customization/verify", "{}"), http.StatusAccepted)
	awaitConversationSignal(t, started)
	waiting := workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+other+"/customization/verify", "{}"), http.StatusAccepted)
	if waiting.Status != "waiting" {
		t.Fatalf("waiting=%+v", waiting)
	}
	close(release)
	f.s.workspace.workers.Wait()
	a, b := f.state(t), func() *db.Customization {
		state, err := f.database.GetCustomization(t.Context(), other)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}()
	if a.Status != "installed" || b.Status != "installed" || a.ArtifactSHA256 != b.ArtifactSHA256 {
		t.Fatalf("queued conversation not covered: %+v %+v", a, b)
	}
}

func TestCustomizationContainsRebasedWork(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	base := customizationTestGit(t, f.cwd, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(f.cwd, "work"), []byte("conversation work"), 0o600); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, f.cwd, "add", "work")
	work := f.commit(t, "conversation work")
	customizationTestGit(t, f.cwd, "reset", "-q", "--hard", base)
	if err := os.WriteFile(filepath.Join(f.cwd, "mainline"), []byte("upstream"), 0o600); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, f.cwd, "add", "mainline")
	dropped := f.commit(t, "mainline only")
	customizationTestGit(t, f.cwd, "-c", "user.name=Test", "-c", "user.email=test@example.com", "cherry-pick", work)
	rebased := customizationTestGit(t, f.cwd, "rev-parse", "HEAD")
	// A conflict-resolved rebase changes the patch but keeps the commit's
	// author, author date and subject.
	customizationTestGit(t, f.cwd, "reset", "-q", "--hard", dropped)
	if err := os.WriteFile(filepath.Join(f.cwd, "work"), []byte("conversation work, resolved"), 0o600); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, f.cwd, "add", "work")
	customizationTestGit(t, f.cwd, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--reuse-message", work)
	resolved := customizationTestGit(t, f.cwd, "rev-parse", "HEAD")
	customizationTestGit(t, f.cwd, "reset", "-q", "--hard", dropped)
	if err := os.WriteFile(filepath.Join(f.cwd, "work"), []byte("other work"), 0o600); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, f.cwd, "add", "work")
	other := f.commit(t, "other work")
	for _, test := range []struct {
		head string
		want bool
	}{{rebased, true}, {resolved, true}, {dropped, false}, {other, false}, {work, true}} {
		got, err := customizationContains(t.Context(), f.cwd, test.head, work)
		if err != nil || got != test.want {
			t.Fatalf("contains(%s)=%v err=%v", test.head, got, err)
		}
	}
}

func TestCustomizationRunningBuildAppliesCoveredConversations(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other := f.conversation(t)
	state := f.verify(t)
	if _, err := f.database.WaitForCustomization(t.Context(), other, state.Commit, true); err != nil {
		t.Fatal(err)
	}
	// The verified build is installed and running in this process.
	f.s.customizations.runningSHA = state.ArtifactSHA256
	if err := f.database.RecoverCustomizations(t.Context(), state.ArtifactSHA256, state.ArtifactSHA256); err != nil {
		t.Fatal(err)
	}
	f.s.customizations.deps.run = func(context.Context, string, []string, io.Writer) error {
		return errors.New("running build must not be rebuilt")
	}
	if got := f.poll(t)[other]; got.Status != "running" || got.ArtifactSHA256 != state.ArtifactSHA256 {
		t.Fatalf("covered conversation not applied: %+v", got)
	}
	conversation, err := f.database.GetConversationByID(t.Context(), other)
	if err != nil || !conversation.Archived {
		t.Fatalf("applied conversation not archived: %v", err)
	}
}

// A commit gone from the checkout (rebased away, then garbage-collected)
// shows on its conversation and never fails reconciliation.
func TestCustomizationMissingCommit(t *testing.T) {
	t.Parallel()
	f := newCustomizationFixture(t)
	other := f.conversation(t)
	f.s.customizations.deps.run = func(ctx context.Context, cwd string, args []string, out io.Writer) error {
		if slices.Contains(args, "lint") {
			return errors.New("lint failed")
		}
		return successfulCustomizationBuild(ctx, cwd, args, out)
	}
	if err := os.WriteFile(filepath.Join(f.cwd, "work"), []byte("dropped"), 0o600); err != nil {
		t.Fatal(err)
	}
	customizationTestGit(t, f.cwd, "add", "work")
	dropped := f.commit(t, "dropped work")
	state, err := f.database.BeginCustomization(t.Context(), f.id, dropped, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.verifyCustomization(t.Context(), state, f.cwd); err == nil {
		t.Fatal("lint failure verified")
	}
	customizationTestGit(t, f.cwd, "reset", "-q", "--hard", "HEAD~1")
	f.commit(t, "rewritten work")
	f.s.customizations.deps.run = successfulCustomizationBuild
	workspaceResponse[db.Customization](t, workspaceAPI(f.s, "POST", "/api/conversation/"+other+"/customization/verify", "{}"), http.StatusAccepted)
	f.s.workspace.workers.Wait()
	customizationTestGit(t, f.cwd, "reflog", "expire", "--expire=now", "--all")
	customizationTestGit(t, f.cwd, "gc", "-q", "--prune=now")
	if err := exec.Command("git", "-C", f.cwd, "cat-file", "-e", dropped).Run(); err == nil {
		t.Fatal("dropped commit still in the checkout")
	}
	states := f.poll(t)
	if state := states[f.id]; state.Status != "stale" || !strings.Contains(state.Error, "no longer in the checkout") {
		t.Fatalf("missing commit not shown: %+v", state)
	}
	if state := states[other]; state.Status != "installed" {
		t.Fatalf("other conversation affected: %+v", state)
	}
	workspaceResponse[[]db.Customization](t, workspaceAPI(f.s, "GET", "/api/customizations", ""), http.StatusOK)
}
