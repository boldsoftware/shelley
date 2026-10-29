package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"shelley.exe.dev/db"
)

// Verification deliberately accepts no commands, test reports, or artifact
// paths from agents/clients. A successful command must finish before the next.
// make build-custom builds the UI, templates and exe-scroll the tests embed.
var customizationVerificationCommands = [][]string{
	{"scripts/with-node", "pnpm", "--dir", "ui", "install", "--frozen-lockfile"},
	{"scripts/with-node", "pnpm", "--dir", "ui", "run", "type-check"},
	{"scripts/with-node", "pnpm", "--dir", "ui", "run", "type-check:vue"},
	{"scripts/with-node", "pnpm", "--dir", "ui", "run", "lint"},
	{"make", "build-custom"},
	{"go", "test", "./server", "./db"},
}

type customizationDependencies struct {
	canonicalDir      func() (string, error)
	buildDir          func() (string, error)
	artifactsDir      func() (string, error)
	runningExecutable func() (string, error)
	targetExecutable  func() (string, error)
	invocationID      func() string
	run               func(context.Context, string, []string, io.Writer) error
	install           func(context.Context, string, string, string) error
	installedCommit   func(context.Context, string) (string, error)
}

func defaultCustomizationDependencies() customizationDependencies {
	return customizationDependencies{
		canonicalDir: func() (string, error) { return customizationHomePath("shelley-customization") },
		buildDir:     func() (string, error) { return customizationHomePath("customization-build") },
		artifactsDir: func() (string, error) { return customizationHomePath("customization-artifacts") },
		// The file this process runs, even once an install replaced its path.
		runningExecutable: func() (string, error) { return "/proc/self/exe", nil },
		targetExecutable:  upgradeTarget,
		invocationID:      func() string { return os.Getenv("INVOCATION_ID") },
		run:               runCustomizationCommand,
		install:           installCustomizationArtifact,
		installedCommit:   executableCommit,
	}
}

type customizationState struct {
	// deps are set by NewServer; tests replace them before first use.
	deps       customizationDependencies
	once       sync.Once
	err        error
	runningSHA string
	// used is set once ensureCustomizations succeeded.
	used atomic.Bool
	// mu serializes verifications, reconciliation, installs and restarts,
	// and guards the verification in progress, so a restart can interrupt
	// it. Never take it while holding workspace.mu.
	mu           sync.Mutex
	cancelVerify context.CancelFunc
	verifyDone   chan struct{}
	// Real user activity (UnixNano), and the latest auto-restart evaluation.
	activity    atomic.Int64
	autoRestart atomic.Pointer[autoRestartPlan]
	// autoMu serializes automatic restarts with cancelling them.
	autoMu sync.Mutex
	// kick wakes the auto-restart worker to evaluate again.
	kick chan struct{}
}

func customizationHomePath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "shelley", name), nil
}

// ensureCustomizations runs once, on first use in workspace mode: it
// records the running executable's digest and recovers the work a restart
// interrupted or applied.
func (s *Server) ensureCustomizations(ctx context.Context) error {
	state := &s.customizations
	state.once.Do(func() {
		state.err = func() error {
			executable, err := state.deps.runningExecutable()
			if err != nil {
				return err
			}
			if state.runningSHA, err = customizationFileSHA(executable); err != nil {
				return err
			}
			target, err := state.deps.targetExecutable()
			if err != nil {
				return err
			}
			installedSHA, err := customizationFileSHA(target)
			if err != nil {
				return err
			}
			return s.db.RecoverCustomizations(context.WithoutCancel(ctx), state.runningSHA, installedSHA)
		}()
		state.used.Store(state.err == nil)
	})
	return state.err
}

// startCustomizations reconciles at startup in workspace mode, verifying
// again what a restart interrupted.
func (s *Server) startCustomizations() {
	s.noteUserActivity() // Nobody has been idle since before Shelley started.
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
		if err := s.reconcileCustomizationsNow(context.Background()); err != nil {
			s.logger.Error("Customization startup reconciliation failed", "error", err)
		}
	}()
}

func (s *Server) registerCustomizationRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/customizations", s.handleCustomizations)
	api.HandleFunc("GET /api/conversation/{id}/customization", s.handleCustomization)
	api.HandleFunc("POST /api/conversation/{id}/customization/verify", s.handleVerifyCustomization)
	api.HandleFunc("POST /api/conversation/{id}/customization/restart", s.handleRestartCustomization)
	api.HandleFunc("POST /api/conversation/{id}/customization/auto-restart/cancel", s.handleCancelAutoRestart)
	// The heartbeat is a POST, which trackUserActivity counts.
	api.HandleFunc("POST /api/activity", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// customizationReady refuses customization requests outside workspace mode,
// which is what seeds the Shelley project.
func (s *Server) customizationReady(w http.ResponseWriter, r *http.Request) bool {
	if s.assistantID() == "" {
		http.Error(w, "Customization needs workspace mode", http.StatusNotFound)
		return false
	}
	if err := s.ensureCustomizations(r.Context()); err != nil {
		s.workspaceHTTPError(w, err)
		return false
	}
	return true
}

// isShelleyProject reports whether project is Shelley's own: workspace mode
// is on and its directory is the canonical customization checkout, on this
// host.
func (s *Server) isShelleyProject(ctx context.Context, project *db.Project) (bool, error) {
	if project.VM != nil || project.Cwd == "" {
		return false, nil
	}
	if s.assistantID() == "" {
		return false, nil
	}
	canonical, err := s.customizations.deps.canonicalDir()
	if err != nil {
		return false, err
	}
	same, err := sameDirectory(project.Cwd, canonical)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // A missing directory is no checkout.
	}
	return same, err
}

// sameDirectory compares a and b with symlinks resolved, so that a
// different git worktree is never taken for the canonical checkout.
func sameDirectory(a, b string) (bool, error) {
	a, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false, err
	}
	b, err = filepath.EvalSymlinks(b)
	return err == nil && a == b, err
}

// A conversation's project and its actual directory must both refer to the
// canonical checkout.
func (s *Server) customizationDirectory(ctx context.Context, id string) (string, error) {
	project, err := s.db.GetConversationProject(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	shelley := false
	if project != nil {
		if shelley, err = s.isShelleyProject(ctx, project); err != nil {
			return "", err
		}
	}
	if !shelley {
		return "", fmt.Errorf("%w: only Shelley-project conversations support customization verification", db.ErrInvalid)
	}
	conversation, err := s.db.GetConversationByID(ctx, id)
	if err != nil {
		return "", err
	}
	canonical, err := s.customizations.deps.canonicalDir()
	if err != nil {
		return "", err
	}
	cwd := derefString(conversation.Cwd)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("%w: the conversation must use the canonical Shelley checkout", db.ErrInvalid)
	}
	if same, err := sameDirectory(cwd, canonical); err != nil || !same {
		return "", errors.Join(err, fmt.Errorf("%w: the conversation must use the canonical Shelley checkout", db.ErrInvalid))
	}
	return filepath.EvalSymlinks(canonical)
}

func customizationGit(ctx context.Context, cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	out := &customizationTail{}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out.text)
	}
	return strings.TrimSpace(out.text), nil
}

type customizationTail struct{ text string }

func (out *customizationTail) Write(p []byte) (int, error) {
	out.text = db.BoundedOutput(out.text + string(p))
	return len(p), nil
}

// customizationHead is the tip of the custom branch. Verification builds that
// exact commit in a separate worktree, so uncommitted or in-progress work in
// the shared checkout (another branch, a rebase) never affects it.
func customizationHead(ctx context.Context, cwd string) (string, error) {
	root, err := customizationGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if root != cwd {
		return "", fmt.Errorf("%w: canonical directory must be the checkout root", db.ErrInvalid)
	}
	return customizationGit(ctx, cwd, "rev-parse", "--verify", "refs/heads/custom^{commit}")
}

func customizationFileSHA(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not a regular executable", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Server) validateCustomization(ctx context.Context, state *db.Customization) error {
	if _, err := s.customizationDirectory(ctx, state.ConversationID); err != nil {
		return err
	}
	sha, err := customizationFileSHA(state.ArtifactPath)
	if err != nil {
		return err
	}
	if sha != state.ArtifactSHA256 || sha == "" {
		return fmt.Errorf("%w: verified artifact changed; verify again", db.ErrConflict)
	}
	return nil
}

func (s *Server) validateCustomizationBuildTarget(cwd string) error {
	destination, err := s.customizations.deps.targetExecutable()
	if err != nil {
		return err
	}
	built := filepath.Join(cwd, "bin", "shelley")
	conflict := filepath.Clean(destination) == built
	builtInfo, err := os.Stat(built)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		liveInfo, err := os.Stat(destination)
		if err != nil {
			return err
		}
		conflict = conflict || os.SameFile(builtInfo, liveInfo)
	}
	if conflict {
		return fmt.Errorf("%w: build output is the live executable; run Shelley from an installed path outside bin/shelley before verifying", db.ErrConflict)
	}
	return nil
}

func (s *Server) customizationResponse(state *db.Customization) {
	state.RunningUnderSystemd = s.customizations.deps.invocationID() != ""
}

// handleCustomizations lists the states of the conversations not archived,
// without their output.
func (s *Server) handleCustomizations(w http.ResponseWriter, r *http.Request) {
	if !s.customizationReady(w, r) {
		return
	}
	states, err := s.db.ListCustomizations(r.Context())
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	listed := []db.Customization{}
	for _, state := range states {
		if state.Archived {
			continue
		}
		state.Output = ""
		s.customizationResponse(&state)
		s.autoRestartResponse(&state)
		listed = append(listed, state)
	}
	writeJSON(w, http.StatusOK, listed)
}

// handleCustomization is one conversation's state with its verification output.
func (s *Server) handleCustomization(w http.ResponseWriter, r *http.Request) {
	if !s.customizationReady(w, r) {
		return
	}
	state, err := s.db.GetCustomization(r.Context(), r.PathValue("id"))
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.customizationResponse(state)
	s.autoRestartResponse(state)
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleVerifyCustomization(w http.ResponseWriter, r *http.Request) {
	if !s.customizationReady(w, r) {
		return
	}
	// finished: the conversation's work is done, so running the build archives it.
	var body struct {
		Finished bool `json:"finished"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeStrict(w, r, &body) {
		return
	}
	s.customizations.mu.Lock()
	defer s.customizations.mu.Unlock()
	id := r.PathValue("id")
	verifying, err := s.db.HasVerifyingCustomization(r.Context())
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	var state *db.Customization
	if verifying {
		// Queue behind the running verification instead of refusing; HEAD is
		// verified next and its outcome is shared with this conversation.
		var cwd, commit string
		cwd, err = s.customizationDirectory(r.Context(), id)
		if err == nil {
			commit, err = customizationHead(r.Context(), cwd)
		}
		if err == nil {
			state, err = s.db.WaitForCustomization(r.Context(), id, commit, body.Finished)
		}
	} else {
		state, err = s.startCustomizationVerification(r.Context(), id, body.Finished)
	}
	if err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	s.notifyWorkspaceChanged()
	s.customizationResponse(state)
	writeJSON(w, http.StatusAccepted, state)
}

// startCustomizationVerification verifies the checkout HEAD on behalf of a
// conversation. The caller holds customizations.mu.
func (s *Server) startCustomizationVerification(ctx context.Context, id string, finished bool) (*db.Customization, error) {
	s.workspace.mu.Lock()
	restarting := s.workspace.restarting
	s.workspace.mu.Unlock()
	if restarting {
		return nil, fmt.Errorf("%w: server is restarting", db.ErrConflict)
	}
	select {
	case <-s.shutdownCh:
		return nil, fmt.Errorf("%w: server is shutting down", db.ErrConflict)
	default:
	}
	cwd, err := s.customizationDirectory(ctx, id)
	if err != nil {
		return nil, err
	}
	buildDir, err := s.customizations.deps.buildDir()
	if err != nil {
		return nil, err
	}
	if err := s.validateCustomizationBuildTarget(buildDir); err != nil {
		return nil, err
	}
	commit, err := customizationHead(ctx, cwd)
	if err != nil {
		return nil, err
	}
	state, err := s.db.BeginCustomization(ctx, id, commit, finished)
	if err != nil {
		return nil, err
	}
	s.workspace.workers.Add(1)
	verification := *state
	verifyCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.customizations.cancelVerify, s.customizations.verifyDone = cancel, done
	go func() {
		defer s.workspace.workers.Done()
		defer close(done)
		defer cancel()
		if err := s.verifyCustomization(verifyCtx, &verification, cwd); err != nil {
			s.logger.Error("Customization verification failed", "conversationID", state.ConversationID, "error", err)
		}
	}()
	return state, nil
}

// reconcileCustomizations moves every conversation toward a healthy state without
// agent or user involvement. A ready or installed build stays so while its
// artifact is intact, whatever happens in the shared checkout. Conversations whose commits the
// custom branch tip contains share the tip's outcome: conversations still needing a
// build (waiting, stale, or failed on an older commit) adopt a running, ready,
// or failed build of the tip, and otherwise the tip is verified for them; ready
// conversations only upgrade to a running or ready build of the tip, so a newer commit
// never downgrades them. A failed tip is retried only on explicit request. A
// conversation whose commit is gone from the checkout (rebased away, then
// garbage-collected) shows so and waits for an explicit request. The caller
// holds customizations.mu.
func (s *Server) reconcileCustomizations(ctx context.Context) ([]db.Customization, error) {
	states, err := s.db.ListCustomizations(ctx)
	if err != nil {
		return nil, err
	}
	changed := false
	defer func() {
		if changed {
			s.notifyWorkspaceChanged()
		}
	}()
	for i := range states {
		state := &states[i]
		if state.Status != "ready" && state.Status != "installed" {
			continue
		}
		if validation := s.validateCustomization(ctx, state); validation != nil {
			updated, err := s.db.UpdateCustomization(ctx, state.ConversationID, state.Generation, func(current *db.Customization) {
				current.Status, current.Error = "stale", validation.Error()
			})
			if err != nil {
				return nil, err
			}
			*state, changed = *updated, true
		}
	}
	upgradable := func(state *db.Customization) bool {
		return (state.Status == "ready" || state.Status == "installed") && !state.Archived
	}
	if !slices.ContainsFunc(states, func(state db.Customization) bool { return state.Unsettled() || upgradable(&state) }) {
		return states, nil
	}
	cwd, err := s.customizations.deps.canonicalDir()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil {
		return nil, err
	}
	head, err := customizationHead(ctx, cwd)
	if err != nil {
		return nil, err
	}
	var built *db.Customization
	var covered []*db.Customization
	for i := range states {
		state := &states[i]
		if state.Commit == head && buildRank(state, s.customizations.runningSHA) > buildRank(built, s.customizations.runningSHA) {
			built = state
		}
		if state.Commit == head && (state.Status == "failed" || upgradable(state)) {
			continue
		}
		if !state.Unsettled() && !upgradable(state) {
			continue
		}
		contained, err := customizationContains(ctx, cwd, head, state.Commit)
		if missing := err; errors.Is(missing, errCommitMissing) && state.Unsettled() && state.Error != missing.Error() {
			updated, err := s.db.UpdateCustomization(ctx, state.ConversationID, state.Generation, func(current *db.Customization) {
				current.Status, current.Error = "stale", missing.Error()
			})
			if err != nil {
				return nil, err
			}
			*state, changed = *updated, true
			continue
		}
		if err != nil {
			if !errors.Is(err, errCommitMissing) {
				s.logger.Error("Customization not reconciled", "conversationID", state.ConversationID, "error", err)
			}
			continue
		}
		if contained {
			covered = append(covered, state)
		}
	}
	var runners []*db.Customization
	for _, state := range covered {
		if upgradable(state) && buildRank(built, s.customizations.runningSHA) < buildRank(state, s.customizations.runningSHA) {
			continue // Keep the last good build until the tip's build gets as far.
		}
		if built == nil || (built.Status == "failed" && state.Status == "waiting") {
			runners = append(runners, state)
			continue
		}
		updated, err := s.db.AdoptCustomization(ctx, state.ConversationID, state.Generation, built)
		if errors.Is(err, db.ErrConflict) {
			continue // Superseded concurrently.
		}
		if err != nil {
			return nil, err
		}
		*state, changed = *updated, true
	}
	if len(runners) != 0 {
		active, err := s.db.HasVerifyingCustomization(ctx)
		if err != nil {
			return nil, err
		}
		// Explicit requests first, then the most recently updated conversation.
		slices.SortStableFunc(runners, func(a, b *db.Customization) int {
			if (a.Status == "waiting") != (b.Status == "waiting") {
				if a.Status == "waiting" {
					return -1
				}
				return 1
			}
			return b.UpdatedAt.Compare(a.UpdatedAt)
		})
		for _, runner := range runners {
			if active {
				break
			}
			started, err := s.startCustomizationVerification(ctx, runner.ConversationID, runner.Finished)
			if err != nil {
				s.logger.Info("Automatic customization verification not started", "conversationID", runner.ConversationID, "error", err)
				if errors.Is(err, db.ErrInvalid) {
					continue // This conversation cannot run it; another covered conversation may.
				}
				break
			}
			*runner, changed, active = *started, true, true
		}
	}
	installed, err := s.installVerifiedCustomizations(ctx, cwd, head, states)
	changed = changed || installed
	return states, err
}

// installVerifiedCustomizations installs ready builds into the live executable
// path, tip first, without restarting; only a restart (or an unplanned start)
// runs them. A build is installed only if it contains the installed
// executable's commit, so an older build never downgrades it. Every ready or
// installed conversation the new build contains adopts it. A build that fails
// to install stays ready with the error, and is installed again at the next
// reconciliation. The caller holds customizations.mu, which serializes
// installs.
func (s *Server) installVerifiedCustomizations(ctx context.Context, cwd, head string, states []db.Customization) (bool, error) {
	changed := false
	tried := map[string]bool{}
	for {
		var candidate *db.Customization
		for i := range states {
			state := &states[i]
			if state.Status != "ready" || state.Archived || tried[state.ArtifactSHA256] {
				continue
			}
			switch {
			case candidate == nil, (state.Commit == head) != (candidate.Commit == head):
				if candidate == nil || state.Commit == head {
					candidate = state
				}
			case state.VerifiedAt.After(*candidate.VerifiedAt):
				candidate = state
			}
		}
		if candidate == nil {
			return changed, nil
		}
		changed = true
		source := *candidate
		tried[source.ArtifactSHA256] = true
		installErr := s.installCustomization(ctx, cwd, &source)
		now := time.Now().UTC()
		for i := range states {
			state := &states[i]
			if state.Archived || state.Status != "ready" || state.ArtifactSHA256 != source.ArtifactSHA256 {
				continue
			}
			updated, err := s.db.UpdateCustomization(ctx, state.ConversationID, state.Generation, func(current *db.Customization) {
				if installErr != nil {
					current.Error = installErr.Error()
				} else {
					current.Status, current.Error, current.InstalledAt = "installed", "", &now
				}
			})
			if err != nil {
				return changed, err
			}
			*state = *updated
			source = *updated
		}
		if installErr != nil {
			s.logger.Error("Customization install failed", "commit", source.Commit, "error", installErr)
			continue
		}
		s.kickAutoRestart()
		for i := range states {
			state := &states[i]
			if state.Archived || (state.Status != "ready" && state.Status != "installed") || state.ArtifactSHA256 == source.ArtifactSHA256 {
				continue
			}
			contained, err := customizationContains(ctx, cwd, source.Commit, state.Commit)
			if err != nil && !errors.Is(err, errCommitMissing) {
				return changed, err
			}
			if !contained {
				continue
			}
			updated, err := s.db.AdoptCustomization(ctx, state.ConversationID, state.Generation, &source)
			if err != nil {
				return changed, err
			}
			*state = *updated
		}
	}
}

// installCustomization atomically replaces the live executable with state's
// verified artifact if that is an upgrade.
func (s *Server) installCustomization(ctx context.Context, cwd string, state *db.Customization) error {
	destination, err := s.customizations.deps.targetExecutable()
	if err != nil {
		return fmt.Errorf("Install failed: %w", err)
	}
	installed, err := s.customizations.deps.installedCommit(ctx, destination)
	if err != nil {
		return fmt.Errorf("Install failed: reading the installed commit: %w", err)
	}
	contained, err := customizationContains(ctx, cwd, state.Commit, installed)
	if errors.Is(err, errCommitMissing) {
		return fmt.Errorf("Not installed: installed commit %.8s is not in the checkout", installed)
	}
	if err != nil {
		return fmt.Errorf("Install failed: %w", err)
	}
	if !contained {
		return fmt.Errorf("Not installed: this build does not contain installed commit %.8s; installing it would downgrade", installed)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := s.customizations.deps.install(ctx, state.ArtifactPath, destination, state.ArtifactSHA256); err != nil {
		return fmt.Errorf("Install failed: %w", err)
	}
	return nil
}

// executableCommit reports the commit a Shelley executable was built from.
func executableCommit(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	var info struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", err
	}
	if info.Commit == "" {
		return "", fmt.Errorf("%s reports no commit", path)
	}
	return info.Commit, nil
}

// buildRank orders a commit's outcomes a conversation can adopt: the running build
// (its work is applied), an installed build awaiting restart, a ready build,
// then a failure.
func buildRank(state *db.Customization, runningSHA string) int {
	switch {
	case state == nil:
		return 0
	case state.Status == "running" && state.ArtifactSHA256 == runningSHA:
		return 4
	case state.Status == "installed":
		return 3
	case state.Status == "ready":
		return 2
	case state.Status == "failed":
		return 1
	}
	return 0
}

// customizationContains reports whether head contains commit's changes,
// directly or as rebased commits (e.g. after a rebase upgrade): either
// patch-equivalent, or carrying the same author, author date and subject,
// which a rebase preserves even when it resolves conflicts. It fails with
// errCommitMissing if commit is not in the checkout.
func customizationContains(ctx context.Context, cwd, head, commit string) (bool, error) {
	if head == commit {
		return true, nil
	}
	const identity = "--format=%an%x00%ae%x00%at%x00%s"
	missing, err := customizationGit(ctx, cwd, "log", identity, "--cherry-pick", "--right-only", "--no-merges", head+"..."+commit)
	if err != nil {
		var exit *exec.ExitError
		_, verifyErr := customizationGit(ctx, cwd, "rev-parse", "--verify", "--quiet", commit+"^{commit}")
		if errors.As(verifyErr, &exit) && exit.ExitCode() == 1 {
			return false, fmt.Errorf("commit %.8s is %w; verify again", commit, errCommitMissing)
		}
		return false, err
	}
	if missing == "" {
		return true, nil
	}
	rebased, err := customizationGit(ctx, cwd, "log", identity, "--left-only", "--no-merges", head+"..."+commit)
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(rebased, "\n") {
		have[line] = true
	}
	for _, line := range strings.Split(missing, "\n") {
		if !have[line] {
			return false, nil
		}
	}
	return true, nil
}

// reconcileCustomizationsNow reconciles in workspace mode and wakes the
// auto-restart worker.
func (s *Server) reconcileCustomizationsNow(ctx context.Context) error {
	if s.assistantID() == "" {
		return nil
	}
	if err := s.ensureCustomizations(ctx); err != nil {
		return err
	}
	s.customizations.mu.Lock()
	_, err := s.reconcileCustomizations(ctx)
	s.customizations.mu.Unlock()
	s.kickAutoRestart()
	return err
}

// reconcileCustomizationsAfterTurn runs when an agent turn ends, the usual
// moment a Shelley-project conversation has just committed to the checkout.
// Until customization is first used (in workspace mode) it does nothing.
func (s *Server) reconcileCustomizationsAfterTurn(id string) {
	if !s.customizations.used.Load() {
		return
	}
	s.workspace.workers.Add(1)
	go func() {
		defer s.workspace.workers.Done()
		ctx := context.Background()
		project, err := s.db.GetConversationProject(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		shelley := false
		if err == nil {
			shelley, err = s.isShelleyProject(ctx, project)
		}
		if err == nil && shelley {
			err = s.reconcileCustomizationsNow(ctx)
		}
		if err != nil {
			s.logger.Error("Customization reconciliation failed", "conversationID", id, "error", err)
		}
	}()
}

// customizationOutput collects a verification's output, which is persisted
// after each step.
type customizationOutput struct {
	mu   sync.Mutex
	tail customizationTail
}

func (out *customizationOutput) Write(p []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	return out.tail.Write(p)
}

func (out *customizationOutput) String() string {
	out.mu.Lock()
	defer out.mu.Unlock()
	return out.tail.text
}

func runCustomizationCommand(ctx context.Context, cwd string, args []string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.Env = actionEnvironment(os.Environ(), nil)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout, cmd.Stderr = output, output
	return cmd.Run()
}

// prepareCustomizationWorktree checks out exactly commit, with no untracked
// files, in the dedicated build worktree of the canonical repository. Ignored
// files (node_modules, build caches) are kept to avoid reinstalling each time.
func prepareCustomizationWorktree(ctx context.Context, canonical, dir, commit string, output io.Writer) error {
	git := func(cwd string, args ...string) error {
		if _, err := fmt.Fprintf(output, "$ git %s\n", strings.Join(args, " ")); err != nil {
			return err
		}
		return runCustomizationCommand(ctx, cwd, append([]string{"git"}, args...), output)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := git(canonical, "worktree", "prune"); err != nil {
			return err
		}
		if err := git(canonical, "worktree", "add", "--detach", "--force", dir, commit); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	common := func(cwd string) (string, error) {
		return customizationGit(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	}
	want, err := common(canonical)
	if err != nil {
		return err
	}
	if got, err := common(dir); err != nil || got != want {
		return errors.Join(err, fmt.Errorf("%s is not a worktree of the canonical checkout", dir))
	}
	if err := git(dir, "checkout", "--detach", "--force", commit); err != nil {
		return err
	}
	if err := git(dir, "clean", "-ffd"); err != nil {
		return err
	}
	return customizationWorktreeClean(ctx, dir, commit)
}

func customizationWorktreeClean(ctx context.Context, dir, commit string) error {
	head, err := customizationGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	dirty, err := customizationGit(ctx, dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if head != commit || dirty != "" {
		return fmt.Errorf("build worktree is not a clean checkout of %s", commit)
	}
	return nil
}

func (s *Server) verifyCustomization(ctx context.Context, state *db.Customization, canonical string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-s.shutdownCh:
			cancel()
		case <-done:
		}
	}()
	output := &customizationOutput{}
	persist := func() error {
		_, err := s.db.UpdateCustomization(context.Background(), state.ConversationID, state.Generation, func(current *db.Customization) {
			current.Output = output.String()
		})
		return err
	}
	verifyErr := func() error {
		cwd, err := s.customizations.deps.buildDir()
		if err != nil {
			return err
		}
		if err := s.validateCustomizationBuildTarget(cwd); err != nil {
			return err
		}
		if err := prepareCustomizationWorktree(ctx, canonical, cwd, state.Commit, output); err != nil {
			if ctx.Err() != nil {
				return errors.Join(err, ctx.Err())
			}
			return err
		}
		for _, args := range customizationVerificationCommands {
			if err := persist(); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(output, "$ %s\n", strings.Join(args, " ")); err != nil {
				return err
			}
			if args[0] == "make" && args[1] == "build-custom" {
				// A no-op or failed build must never reuse a previous binary.
				if err := os.Remove(filepath.Join(cwd, "bin", "shelley")); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if err := s.customizations.deps.run(ctx, cwd, args, output); err != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("%s: %w", strings.Join(args, " "), ctx.Err())
				}
				return fmt.Errorf("%w: %s: %w", errCustomizationCommit, strings.Join(args, " "), err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// The build must not change the sources it was verifying.
		if err := customizationWorktreeClean(ctx, cwd, state.Commit); err != nil {
			return fmt.Errorf("%w: %w", errCustomizationCommit, err)
		}
		artifact, sha, err := s.copyCustomizationArtifact(cwd, state.Generation)
		if err != nil {
			return err
		}
		state.ArtifactPath, state.ArtifactSHA256 = artifact, sha
		return nil
	}()
	// Reconciliation and restart cannot observe ready until final
	// git/artifact validation and this durable state transition have completed.
	s.customizations.mu.Lock()
	defer s.customizations.mu.Unlock()
	if verifyErr == nil {
		verifyErr = s.validateCustomization(ctx, state)
	}
	commitFailed := errors.Is(verifyErr, errCustomizationCommit)
	_, err := s.db.UpdateCustomization(context.Background(), state.ConversationID, state.Generation, func(current *db.Customization) {
		current.Output = output.String()
		switch {
		case errors.Is(verifyErr, context.Canceled):
			// Interrupted by a restart or shutdown, not a failure; verify again.
			current.Status, current.Error = "waiting", ""
		case commitFailed:
			current.Status, current.Error = "failed", verifyErr.Error()
		case verifyErr != nil:
			// The environment failed (e.g. timeout); verify again.
			current.Status, current.Error = "stale", verifyErr.Error()
		default:
			now := time.Now().UTC()
			current.Status, current.Error, current.VerifiedAt = "ready", "", &now
			current.ArtifactPath, current.ArtifactSHA256 = state.ArtifactPath, state.ArtifactSHA256
		}
	})
	if err != nil {
		return err
	}
	s.notifyWorkspaceChanged()
	// Share a real outcome with covered conversations. An environmental failure waits
	// for the next trigger, so a persistent one cannot loop.
	if verifyErr == nil || commitFailed {
		if _, err := s.reconcileCustomizations(context.Background()); err != nil {
			return errors.Join(verifyErr, err)
		}
	}
	return verifyErr
}

var errCommitMissing = errors.New("no longer in the checkout")

// errCustomizationCommit marks failures caused by the verified commit itself
// (a verification command or missing build output), as opposed to the
// environment, which only makes the attempt stale.
var errCustomizationCommit = errors.New("verification failed")

func (s *Server) copyCustomizationArtifact(cwd, generation string) (string, string, error) {
	dir, err := s.customizations.deps.artifactsDir()
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(dir) {
		return "", "", fmt.Errorf("artifact directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", err
	}
	if rel, err := filepath.Rel(cwd, dir); err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", "", fmt.Errorf("artifact directory must be outside the checkout")
	}
	source := filepath.Join(cwd, "bin", "shelley")
	sha, err := customizationFileSHA(source)
	if err != nil {
		return "", "", fmt.Errorf("%w: build produced no executable: %w", errCustomizationCommit, err)
	}
	input, err := os.Open(source)
	if err != nil {
		return "", "", err
	}
	defer input.Close()
	path := filepath.Join(dir, generation+"-shelley")
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		return "", "", err
	}
	_, copyErr := io.Copy(output, input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return "", "", copyErr
	}
	if closeErr != nil {
		return "", "", closeErr
	}
	copiedSHA, err := customizationFileSHA(path)
	if err != nil {
		return "", "", err
	}
	if copiedSHA != sha {
		return "", "", fmt.Errorf("build artifact changed while copying")
	}
	return path, sha, nil
}

func (s *Server) handleRestartCustomization(w http.ResponseWriter, r *http.Request) {
	if !s.customizationReady(w, r) {
		return
	}
	var expected struct {
		Commit         string `json:"commit"`
		ArtifactSHA256 string `json:"artifact_sha256"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeStrict(w, r, &expected) {
		return
	}
	if expected.Commit == "" || expected.ArtifactSHA256 == "" {
		s.workspaceHTTPError(w, fmt.Errorf("%w: explicit commit and artifact_sha256 are required", db.ErrInvalid))
		return
	}
	if s.customizations.deps.invocationID() == "" {
		s.workspaceHTTPError(w, fmt.Errorf("%w: automatic restart requires systemd (INVOCATION_ID)", db.ErrConflict))
		return
	}
	if err := s.reserveRestart(); err != nil {
		s.workspaceHTTPError(w, err)
		return
	}
	if err := s.prepareRestart(r.Context()); err != nil {
		s.releaseRestart()
		s.workspaceHTTPError(w, err)
		return
	}
	state, err := s.restartCustomization(r.Context(), r.PathValue("id"), expected.Commit, expected.ArtifactSHA256)
	if err != nil {
		s.releaseRestart()
		s.workspaceHTTPError(w, err)
		return
	}
	s.customizationResponse(state)
	writeJSON(w, http.StatusAccepted, state)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
	s.scheduleExit(restartExitCode)
}

// restartCustomization marks conversation id's installed build as restarting, as
// the user's Restart Shelley click does; the caller holds the restart fence
// and schedules the exit. It only runs the verified build already on disk,
// installing nothing. A refused restart is recorded in the conversation's status.
func (s *Server) restartCustomization(ctx context.Context, id, commit, artifactSHA string) (*db.Customization, error) {
	s.customizations.mu.Lock()
	defer s.customizations.mu.Unlock()
	state, err := s.db.GetCustomization(ctx, id)
	if err != nil {
		return nil, err
	}
	if state.Status != "installed" || commit != state.Commit || artifactSHA != state.ArtifactSHA256 {
		return nil, fmt.Errorf("%w: requested commit/artifact is not installed; refresh and verify again", db.ErrConflict)
	}
	destination, err := s.customizations.deps.targetExecutable()
	if err != nil {
		return nil, s.failCustomizationRestart(state, err, nil)
	}
	if err := s.validateCustomization(ctx, state); err != nil {
		return nil, s.staleCustomizationRestart(state, err)
	}
	if sha, err := customizationFileSHA(destination); err != nil || sha != state.ArtifactSHA256 {
		return nil, s.staleCustomizationRestart(state, errors.Join(err, errors.New("the installed executable is no longer this build")))
	}
	resumeBefore, err := s.db.GetSetting(ctx, db.ResumeAfterUpgradeSettingKey)
	if err != nil {
		return nil, s.failCustomizationRestart(state, err, nil)
	}
	if err := s.markResumeAfterRestart(ctx); err != nil {
		return nil, s.failCustomizationRestart(state, err, &resumeBefore)
	}
	restarting, err := s.db.UpdateCustomization(ctx, state.ConversationID, state.Generation, func(current *db.Customization) {
		current.Status, current.Error = "restarting", ""
	})
	if err != nil {
		return nil, s.failCustomizationRestart(state, err, &resumeBefore)
	}
	s.notifyWorkspaceChanged()
	return restarting, nil
}

// staleCustomizationRestart refuses a restart whose build is no longer
// intact on disk; the stale conversation is verified and installed again.
func (s *Server) staleCustomizationRestart(state *db.Customization, cause error) error {
	_, persistErr := s.db.UpdateCustomization(context.Background(), state.ConversationID, state.Generation, func(current *db.Customization) {
		current.Status, current.Error = "stale", cause.Error()
	})
	s.notifyWorkspaceChanged()
	return errors.Join(fmt.Errorf("%w: %v", db.ErrConflict, cause), persistErr)
}

// failCustomizationRestart keeps the build installed and reports why the
// restart did not happen.
func (s *Server) failCustomizationRestart(state *db.Customization, cause error, resumeBefore *string) error {
	if resumeBefore != nil {
		var err error
		if *resumeBefore == "" {
			err = s.db.DeleteSetting(context.Background(), db.ResumeAfterUpgradeSettingKey)
		} else {
			err = s.db.SetSetting(context.Background(), db.ResumeAfterUpgradeSettingKey, *resumeBefore)
		}
		cause = errors.Join(cause, err)
	}
	_, persistErr := s.db.UpdateCustomization(context.Background(), state.ConversationID, state.Generation, func(current *db.Customization) {
		current.Status, current.Error = "installed", "Restart failed: "+cause.Error()
	})
	s.notifyWorkspaceChanged()
	return errors.Join(cause, persistErr)
}

// scheduleExit is shared with /exit. The delay lets an in-flight tool persist
// its result; the durable resume marker is consumed by the replacement process.
func (s *Server) scheduleExit(code int) {
	time.AfterFunc(s.exitDelay, func() {
		s.logger.Info("Exiting Shelley", "exit_code", code)
		s.exitProcess(code)
	})
}

// installCustomizationArtifact follows the documented side-by-side install:
// preserve live ownership/mode, replace staged contents, verify, atomic rename.
// No permission-error retry: sudo -n is selected up front when needed.
func installCustomizationArtifact(ctx context.Context, artifact, destination, expectedSHA string) (result error) {
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("live executable path is not absolute")
	}
	info, err := os.Stat(destination)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("live executable is not a regular file")
	}
	// A staging path of its own: a leftover of a crash or a manual install
	// never blocks it.
	stage := fmt.Sprintf("%s.%s.new", destination, uuid.NewString())
	sha, err := customizationFileSHA(artifact)
	if err != nil {
		return err
	}
	if sha != expectedSHA {
		return fmt.Errorf("verified artifact changed before installation")
	}
	needSudo := unix.Access(filepath.Dir(destination), unix.W_OK) != nil
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		needSudo = true
	}
	run := func(name string, args ...string) error {
		if needSudo {
			args = append([]string{"-n", name}, args...)
			name = "sudo"
		}
		cmd := exec.CommandContext(ctx, name, args...)
		out := &customizationTail{}
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, out.text)
		}
		return nil
	}
	created := false
	defer func() {
		if result != nil && created {
			// Cleanup has its own deadline: the failed operation may have
			// exhausted the installation context already.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			ctx = cleanupCtx
			result = errors.Join(result, run("rm", "--", stage))
		}
	}()
	if err := run("cp", "-p", "--", destination, stage); err != nil {
		if _, statErr := os.Lstat(stage); statErr == nil {
			created = true // cp can leave a partial file on disk-full.
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(err, statErr)
		}
		return err
	}
	created = true
	if err := run("cp", "--", artifact, stage); err != nil {
		return err
	}
	stagedSHA, err := customizationFileSHA(stage)
	if err != nil {
		return err
	}
	if stagedSHA != expectedSHA {
		return fmt.Errorf("staged executable SHA-256 does not match verified artifact")
	}
	return run("mv", "--", stage, destination)
}
