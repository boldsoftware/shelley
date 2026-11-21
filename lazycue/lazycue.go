// Package lazycue implements self-healing browser tests written as plain English.
//
// A test is a description string. The system hashes the description to look up a
// cached DSL test script stored as a JSON file in a .lazycue/ directory next to
// the tests. If cached, it executes the DSL. If the test passes, it's done. If
// it fails (or isn't cached), an LLM agent generates or fixes the DSL, and the
// new version is written back to the cache file.
package lazycue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Options configures a lazycue test run.
type Options struct {
	BaseURL string // Base URL of a long-running app under test; Setup replaces it
	// Setup starts a fresh instance of the app under test. Run calls it for
	// every attempt at a test (a cached run, each retry, and each script the
	// agent runs), so a script never passes only because of what an earlier
	// attempt left behind.
	Setup            func(ctx context.Context) (*Env, error)
	CacheDir         string // Directory holding cache JSON files (default: ".lazycue")
	Model            string // LLM model (default: "claude-sonnet-4-6")
	AnthropicBaseURL string // Anthropic API base URL (default: ANTHROPIC_BASE_URL or https://api.anthropic.com)
	AnthropicAPIKey  string // Anthropic API key (default: ANTHROPIC_API_KEY)
	Verbose          bool   // Verbose output
	ArtifactDir      string // If set, write per-step screenshots here and record their paths on StepResults
	VideoDir         string // If set, render an MP4 per test (prompt title card + captioned screenshots) here, named <DescriptionHash>.mp4
	RepoRoot         string // Repository root passed to the heal agent's git_command tool (default: `git rev-parse --show-toplevel` from the cwd, falling back to ".")
	// GitHidden lists path fragments the agent's git_command refuses to
	// mention: other tests, which it would copy instead of following its
	// description.
	GitHidden []string
}

// Env is one instance of the app under test.
type Env struct {
	BaseURL string
	// Launch is how the browser's tabs start out.
	Launch Launch
	// Vars holds the values {{NAME}} placeholders in steps stand for.
	// Placeholders keep a script independent of values that change from run
	// to run (a temporary directory, an id the test created). Each step reads
	// them as it runs, so a call may set one for the steps after it.
	Vars map[string]string
	// Calls are host-side functions that call steps run by name, for what a
	// page can neither do nor see: write a file, release a barrier in the
	// server, read what the server stored. A call returns a value that an
	// expectation can match.
	Calls map[string]func(ctx context.Context) (string, error)
	// Close tears the instance down; nil needs nothing.
	Close func()
}

func (e *Env) close() {
	if e.Close != nil {
		e.Close()
	}
}

// call runs a call step.
func (e *Env) call(ctx context.Context, step Step) (string, error) {
	f, ok := e.Calls[step.Value]
	if !ok {
		return "", fmt.Errorf("call: no function %q (have: %s)", step.Value, strings.Join(slices.Sorted(maps.Keys(e.Calls)), ", "))
	}
	timeout := parseTimeout(step.Timeout, defaultStepTimeout)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return expectResult(ctx, step, timeout, func() (string, error) { return f(callCtx) })
}

func (o *Options) defaults() {
	if o.CacheDir == "" {
		o.CacheDir = ".lazycue"
	}
	if o.Model == "" {
		o.Model = "claude-sonnet-4-6"
	}
	if o.AnthropicBaseURL == "" {
		if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
			o.AnthropicBaseURL = v
		} else {
			o.AnthropicBaseURL = "https://api.anthropic.com"
		}
	}
	if o.AnthropicAPIKey == "" {
		o.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if o.RepoRoot == "" {
		o.RepoRoot = detectRepoRoot()
	}
	if o.Setup == nil && o.BaseURL != "" {
		baseURL := o.BaseURL
		o.Setup = func(context.Context) (*Env, error) { return &Env{BaseURL: baseURL}, nil }
	}
}

// detectRepoRoot returns the git repository root for the current working
// directory, falling back to "." if git can't determine it (e.g. not a repo).
// The heal agent's git_command tool runs commands in this directory; without
// it, git would run in the process CWD, which may not be the repo root.
func detectRepoRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "."
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "."
	}
	return root
}

// perTestBudget bounds the whole resolution of a single lazycue test: the
// cached attempt and its retries, the wait for an agent slot, and the agent
// (agentBudget). It is kept under the `go test` package deadline (10m
// default) so that a single stuck test fails cleanly via t.Fatal rather than
// letting the package time out and panic, which would abort every other test
// in the package.
//
// It is set comfortably above agentBudget rather than just above it: under
// the flaky-long-wait conditions that trigger heals, the attempts before the
// agent (each with long wait timeouts) can consume a minute or more.
const perTestBudget = 8 * time.Minute

// RunMode describes how the test was resolved.
type RunMode string

const (
	RunModeCached    RunMode = "cached"    // test ran from cache
	RunModeGenerated RunMode = "generated" // agent generated fresh
	RunModeHealed    RunMode = "healed"    // agent fixed a cached test
)

// TestResult is the result of running a lazy test.
type TestResult struct {
	Pass           bool
	Error          string
	ScreenshotPath string
	Steps          []StepResult
	CacheVersion   int
	Description    string
	Name           string // the Go test name (t.Name()), e.g. "TestNewPageSmoke"; "" if unset
	Mode           RunMode
	TotalDuration  time.Duration // wall-clock time for the entire test
	AgentDuration  time.Duration // time spent in the LLM agent (0 if cached)
	InputTokens    int           // total input tokens used by agent (0 if cached)
	OutputTokens   int           // total output tokens used by agent (0 if cached)
	EstimatedCost  float64       // estimated USD cost
	VideoPath      string        // path to the rendered MP4, if VideoDir was set
	Heal           *HealInfo     // populated only when Mode == RunModeHealed: why/how the heal happened
}

// HealInfo records why a heal was triggered and how the agent behaved, so a
// human reviewing CI artifacts can see the cause of a heal and whether the
// agent did its job (or thrashed). It is only set when Mode == RunModeHealed.
type HealInfo struct {
	TriggerStepSummary string `json:"trigger_step_summary"` // the cached step that failed and triggered the heal (e.g. "wait_visible [data-testid='tool-call-completed']")
	TriggerError       string `json:"trigger_error"`        // the error that step produced (e.g. "timeout after 60s waiting for ...")
	TriggerWasTimeout  bool   `json:"trigger_was_timeout"`  // true if the trigger error looks like a transient wait timeout
	RetriesBeforeHeal  int    `json:"retries_before_heal"`  // how many fresh-browser retries ran before paying for the heal
	AgentTurns         int    `json:"agent_turns"`          // API round trips the heal agent took
	AgentToolCalls     int    `json:"agent_tool_calls"`     // tool calls the heal agent issued
	AgentHitMaxTurns   bool   `json:"agent_hit_max_turns"`  // true if the agent exhausted its turn budget
	StepsChanged       bool   `json:"steps_changed"`        // true if the healed steps actually differ from the cache
	CachedStepCount    int    `json:"cached_step_count"`    // number of steps in the cached (pre-heal) test
	HealedStepCount    int    `json:"healed_step_count"`    // number of steps the agent emitted
}

// Summary renders a one-line human-readable explanation of why and how a heal
// happened, suitable for a test log or CI artifact.
func (h *HealInfo) Summary() string {
	cause := "failure"
	if h.TriggerWasTimeout {
		cause = "transient timeout"
	}
	changed := "no"
	if h.StepsChanged {
		changed = fmt.Sprintf("yes (%d→%d steps)", h.CachedStepCount, h.HealedStepCount)
	}
	turns := fmt.Sprintf("%d turns, %d tool calls", h.AgentTurns, h.AgentToolCalls)
	if h.AgentHitMaxTurns {
		turns += " (hit max turns!)"
	}
	return fmt.Sprintf("Healed: triggered by %s on %q (%s); retries before heal: %d; agent used %s; steps changed: %s",
		cause, h.TriggerStepSummary, strings.TrimSpace(h.TriggerError), h.RetriesBeforeHeal, turns, changed)
}

// StepResult is the result of executing a single DSL step.
type StepResult struct {
	Action     string        `json:"action"`
	Summary    string        `json:"summary"` // e.g. "click #login-button"
	Pass       bool          `json:"pass"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration"`
	Screenshot string        `json:"screenshot,omitempty"` // path to PNG captured after this step (if enabled)
	Output     string        `json:"output,omitempty"`     // diagnostic output (e.g. the value returned by an eval step)
}

// Run executes a single lazy test described by the given plain-English description.
func Run(ctx context.Context, opts Options, description string) (*TestResult, error) {
	totalStart := time.Now()
	opts.defaults()

	// Bound the whole test resolution so a stall anywhere (browser exec, retry,
	// or heal) fails this one test cleanly instead of running until the package
	// test deadline and panicking (which takes the rest of the package down).
	ctx, cancel := context.WithTimeout(ctx, perTestBudget)
	defer cancel()

	if opts.Setup == nil {
		return nil, fmt.Errorf("BaseURL or Setup is required")
	}

	logf := func(format string, args ...any) {
		if opts.Verbose {
			log.Printf(format, args...)
		}
	}

	// Step 1: Check cache.
	logf("[lazycue] checking cache in %s", opts.CacheDir)
	cachedTest, cacheHit, err := GetCachedTest(opts.CacheDir, description)
	if err != nil {
		logf("[lazycue] warning: get cached test: %v", err)
	}
	var version int
	if cacheHit != nil {
		version = cacheHit.Version
		logf("[lazycue] cache hit: v%d", version)
	}

	// Optional artifact collection: per-step screenshots written to a per-test
	// subdirectory (named by description hash, so screenshots are "arranged by
	// prompt"). Enabled when either ArtifactDir or VideoDir is set; the video
	// renderer consumes the same on-disk screenshots.
	var collector *artifactCollector
	screenshotRoot := opts.ArtifactDir
	if screenshotRoot == "" {
		screenshotRoot = opts.VideoDir
	}
	if screenshotRoot != "" {
		subdir := filepath.Join(screenshotRoot, DescriptionHash(description))
		if c, cErr := newArtifactCollector(subdir); cErr == nil {
			collector = c
		} else {
			logf("[lazycue] warning: artifact collector: %v", cErr)
		}
	}

	// launch starts a fresh app instance and browser for one attempt, which
	// takes screenshots if shots is set. Only attempts after a failure take
	// them: there is nothing to debug in a passing cached run, and
	// screenshotting every step of every test costs real time.
	launch := func(shots bool) (*Env, *Browser, error) {
		env, err := opts.Setup(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("set up app: %w", err)
		}
		br, err := NewBrowser(ctx, env.Launch)
		if err != nil {
			env.close()
			return nil, nil, fmt.Errorf("launch browser: %w", err)
		}
		if shots && collector != nil {
			br.SetScreenshotSink(collector.sink())
		}
		return env, br, nil
	}
	// attempt runs steps against a fresh app instance and browser.
	attempt := func(steps []Step, shots bool) ([]StepResult, bool, error) {
		env, br, err := launch(shots)
		if err != nil {
			return nil, false, err
		}
		defer env.close()
		defer br.Close()
		results, execErr := br.ExecuteSteps(ctx, env, steps)
		return results, stepsAllPassed(results, execErr), nil
	}
	// runAgent runs the generating or healing agent, which gets a fresh app
	// instance and browser for every run of steps, its final one included.
	runAgent := func(cfg AgentConfig) (*AgentResult, error) {
		release, err := acquireAgent(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		cfg.Description = description
		cfg.Launch = func() (*Env, *Browser, error) { return launch(true) }
		cfg.Model = opts.Model
		cfg.AnthropicBaseURL = opts.AnthropicBaseURL
		cfg.AnthropicAPIKey = opts.AnthropicAPIKey
		cfg.RepoRoot = opts.RepoRoot
		cfg.GitHidden = opts.GitHidden
		cfg.Verbose = opts.Verbose
		return RunAgent(ctx, &cfg)
	}

	// Step 2: If cached, try executing.
	if cachedTest != nil {
		steps, parseErr := ParseSteps(cachedTest.Steps)
		if parseErr == nil {
			logf("[lazycue] found cached v%d (%d steps)", version, len(steps))
			results, allPassed, err := attempt(steps, false)
			if err != nil {
				return nil, err
			}
			// A cached test can fail spuriously under load (e.g. the app is slow
			// to deliver the first message and an element doesn't appear within a
			// wait timeout). Retry with a fresh app and browser before paying for
			// an LLM heal — a genuine app/test mismatch will fail every time. A
			// transient timeout (the common flake) almost always clears on the
			// next attempt, so this keeps the LLM out of the loop entirely. This
			// work only happens on the failure path, so it never slows the happy
			// path.
			const maxRetriesBeforeHeal = 2
			retriesBeforeHeal := 0
			for ; !allPassed && retriesBeforeHeal < maxRetriesBeforeHeal && ctx.Err() == nil; retriesBeforeHeal++ {
				logf("[lazycue] cached test failed (%s); retry %d/%d", summarizeFailure(results), retriesBeforeHeal+1, maxRetriesBeforeHeal)
				if results, allPassed, err = attempt(steps, true); err != nil {
					return nil, err
				}
			}
			if allPassed {
				logf("[lazycue] cached test passed")
				if collector != nil {
					collector.attach(results)
				}
				return &TestResult{
					Pass:          true,
					Steps:         results,
					CacheVersion:  version,
					Description:   description,
					Mode:          RunModeCached,
					TotalDuration: time.Since(totalStart),
				}, nil
			}

			// Cached test failed — need to fix it
			logf("[lazycue] cached test failed, spawning agent to fix")
			triggerStep, triggerErr := failedStep(results)
			agentStart := time.Now()
			agentResult, agentErr := runAgent(AgentConfig{
				Mode:          AgentModeFix,
				PreviousSteps: cachedTest.Steps,
				PreviousError: summarizeFailure(results),
				CacheFilePath: CacheFilePath(opts.CacheDir, description),
			})
			agentDur := time.Since(agentStart)
			if errors.Is(agentErr, errNoAgent) {
				if collector != nil {
					collector.attach(results)
				}
				return &TestResult{
					Pass:          false,
					Error:         summarizeFailure(results) + "; not healing: " + agentErr.Error(),
					Steps:         results,
					CacheVersion:  version,
					Description:   description,
					Mode:          RunModeCached,
					TotalDuration: time.Since(totalStart),
				}, nil
			}
			if agentErr != nil {
				return nil, fmt.Errorf("agent fix: %w", agentErr)
			}

			newVersion := version + 1
			stepsChanged := !sameSteps(cachedTest.Steps, agentResult.StepsJSON)
			if agentResult.Success {
				// A cached test can fail spuriously under heavy CI load even after
				// the retries (e.g. a 60s wait that just barely times out when
				// many tests run in parallel). The heal agent then often concludes
				// the steps were already correct and re-emits the SAME steps.
				// Rewriting the cache in that case only churns the tracked JSON
				// (bumping the version, refreshing metadata) for no behavioral
				// change, which breaks the queue's commit-back step. Only persist a
				// heal when the steps actually differ from what's already cached.
				if !stepsChanged {
					logf("[lazycue] healed steps identical to cache; not rewriting (transient flake)")
					newVersion = version
				} else {
					meta := buildCacheMetadata(opts, agentResult, "healed")
					if saveErr := SaveCachedTest(opts.CacheDir, description, agentResult.StepsJSON, newVersion, meta); saveErr != nil {
						logf("[lazycue] warning: save cached test: %v", saveErr)
					}
				}
			}

			heal := &HealInfo{
				TriggerStepSummary: triggerStep,
				TriggerError:       triggerErr,
				TriggerWasTimeout:  isTimeoutFailure(results),
				RetriesBeforeHeal:  retriesBeforeHeal,
				AgentTurns:         agentResult.Turns,
				AgentToolCalls:     agentResult.ToolCalls,
				AgentHitMaxTurns:   agentResult.HitMaxTurns,
				StepsChanged:       agentResult.Success && stepsChanged,
				CachedStepCount:    len(steps),
				HealedStepCount:    healedStepCount(agentResult.StepsJSON),
			}

			if collector != nil {
				collector.attach(agentResult.StepResults)
			}
			return &TestResult{
				Pass:           agentResult.Success,
				Error:          agentResult.Error,
				ScreenshotPath: agentResult.ScreenshotPath,
				Steps:          agentResult.StepResults,
				CacheVersion:   newVersion,
				Description:    description,
				Mode:           RunModeHealed,
				TotalDuration:  time.Since(totalStart),
				AgentDuration:  agentDur,
				InputTokens:    agentResult.InputTokens,
				OutputTokens:   agentResult.OutputTokens,
				EstimatedCost:  agentResult.Cost,
				Heal:           heal,
			}, nil
		}
		// Parse error — treat as uncached
		logf("[lazycue] cached test parse error: %v, regenerating", parseErr)
	}

	// Step 3: No cache — generate from scratch
	logf("[lazycue] no cached test, spawning agent to generate")
	agentStart := time.Now()
	agentResult, agentErr := runAgent(AgentConfig{Mode: AgentModeGenerate})
	agentDur := time.Since(agentStart)
	if errors.Is(agentErr, errNoAgent) {
		return &TestResult{
			Pass:          false,
			Error:         "no cached script; not generating one: " + agentErr.Error(),
			Description:   description,
			Mode:          RunModeGenerated,
			TotalDuration: time.Since(totalStart),
		}, nil
	}
	if agentErr != nil {
		return nil, fmt.Errorf("agent generate: %w", agentErr)
	}

	if agentResult.Success {
		meta := buildCacheMetadata(opts, agentResult, "generated")
		if saveErr := SaveCachedTest(opts.CacheDir, description, agentResult.StepsJSON, 1, meta); saveErr != nil {
			logf("[lazycue] warning: save cached test: %v", saveErr)
		}
	}

	if collector != nil {
		collector.attach(agentResult.StepResults)
	}
	return &TestResult{
		Pass:           agentResult.Success,
		Error:          agentResult.Error,
		ScreenshotPath: agentResult.ScreenshotPath,
		Steps:          agentResult.StepResults,
		CacheVersion:   1,
		Description:    description,
		Mode:           RunModeGenerated,
		TotalDuration:  time.Since(totalStart),
		AgentDuration:  agentDur,
		InputTokens:    agentResult.InputTokens,
		OutputTokens:   agentResult.OutputTokens,
		EstimatedCost:  agentResult.Cost,
	}, nil
}

var (
	// agentSlots bounds how many agents run at once;
	// LAZYCUE_AGENT_CONCURRENCY sets it (default 4). When a change breaks
	// many tests at once, their heals queue for a slot within each test's
	// budget rather than all running.
	agentSlots chan struct{}
	// agentRunsLeft is how many more agents this process may start;
	// LAZYCUE_MAX_AGENT_RUNS sets it (default, or negative: unlimited). Zero
	// runs cached scripts only, failing any test without one that passes.
	agentRunsLeft atomic.Int64
)

func init() {
	n := envInt("LAZYCUE_AGENT_CONCURRENCY", 4)
	if n < 1 {
		panic("LAZYCUE_AGENT_CONCURRENCY must be at least 1")
	}
	agentSlots = make(chan struct{}, n)
	agentRunsLeft.Store(int64(envInt("LAZYCUE_MAX_AGENT_RUNS", -1)))
}

// errNoAgent is why a test got no agent to generate or heal its script.
var errNoAgent = errors.New("no agent")

// acquireAgent waits for an agent slot, failing with errNoAgent when the
// process may start no more agents or the test runs out of time waiting.
func acquireAgent(ctx context.Context) (release func(), err error) {
	var n int64
	for {
		n = agentRunsLeft.Load()
		if n == 0 {
			return nil, fmt.Errorf("%w: LAZYCUE_MAX_AGENT_RUNS agent runs used up", errNoAgent)
		}
		if n < 0 || agentRunsLeft.CompareAndSwap(n, n-1) {
			break
		}
	}
	select {
	case agentSlots <- struct{}{}:
		return func() { <-agentSlots }, nil
	case <-ctx.Done():
		if n > 0 {
			agentRunsLeft.Add(1) // no agent ran
		}
		return nil, fmt.Errorf("%w: waiting for a slot: %w", errNoAgent, ctx.Err())
	}
}

// envInt reads an integer from the environment, def when unset.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		panic(fmt.Sprintf("%s=%q is not an integer", name, v))
	}
	return n
}

func summarizeFailure(results []StepResult) string {
	for i, r := range results {
		if !r.Pass {
			return fmt.Sprintf("step %d (%s) failed: %s", i, r.Action, r.Error)
		}
	}
	return "unknown failure"
}

// stepsAllPassed reports whether an ExecuteSteps run fully succeeded.
func stepsAllPassed(results []StepResult, execErr error) bool {
	if execErr != nil {
		return false
	}
	for _, r := range results {
		if !r.Pass {
			return false
		}
	}
	return true
}

// isTimeoutFailure reports whether the first failing step failed with a wait
// timeout (the signature pollJS emits: "timeout after <d> waiting for ...").
// These are the transient CI-load flakes we prefer to retry rather than heal.
func isTimeoutFailure(results []StepResult) bool {
	for _, r := range results {
		if !r.Pass {
			return strings.Contains(r.Error, "timeout after")
		}
	}
	return false
}

// failedStep returns the summary and error of the first failing step, for heal
// diagnostics.
func failedStep(results []StepResult) (summary, errMsg string) {
	for _, r := range results {
		if !r.Pass {
			return r.Summary, r.Error
		}
	}
	return "", ""
}

// healedStepCount returns the number of DSL steps in a raw steps blob (0 if it
// can't be parsed, e.g. an empty/genuine-failure result).
func healedStepCount(stepsJSON []byte) int {
	steps, err := ParseSteps(stepsJSON)
	if err != nil {
		return 0
	}
	return len(steps)
}

// Harness holds options for running lazycue tests. Create one as a
// package-level var and call Test from each test function:
//
//	var browser = lazycue.New(lazycue.Options{BaseURL: "http://localhost:3000"})
//
//	func TestLogin(t *testing.T) {
//	    browser.Test(t, "Navigate to /login and verify the login form is visible")
//	}
//
// A Harness accumulates every TestResult it runs, so a TestMain can emit an
// aggregate report/summary after all tests finish (see Results).
type Harness struct {
	opts Options

	mu      sync.Mutex
	results []*TestResult
}

// New creates a Harness with the given options.
func New(opts Options) *Harness {
	return &Harness{opts: opts}
}

// Results returns a copy of every TestResult run through this Harness so far.
// Use it from TestMain to write an aggregate report/summary, e.g.:
//
//	code := m.Run()
//	lazycue.WriteReport(dir, app.Results())
func (h *Harness) Results() []*TestResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*TestResult(nil), h.results...)
}

// RenderVideos renders an MP4 per accumulated result into the harness's
// VideoDir (no-op if unset). Call it from TestMain after m.Run() and before
// WriteReport/WriteSummary so the report embeds the videos and the summary
// records their paths. Best-effort; see the package-level RenderVideos.
func (h *Harness) RenderVideos() error {
	if h.opts.VideoDir == "" {
		return nil
	}
	return RenderVideos(h.opts.VideoDir, h.Results())
}

// Test runs a self-healing browser test described in plain English.
// It calls t.Fatal if the test fails or encounters an error.
func (h *Harness) Test(t testing.TB, description string) {
	t.Helper()
	result, err := Run(t.Context(), h.opts, description)
	if err != nil {
		t.Fatalf("lazycue: %v", err)
	}
	result.Name = t.Name()
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()

	// Log step results.
	var sb strings.Builder
	for _, s := range result.Steps {
		mark := "✓"
		if !s.Pass {
			mark = "✗"
		}
		fmt.Fprintf(&sb, "  %s %s  %s", mark, s.Summary, s.Duration.Round(time.Millisecond))
		if s.Error != "" {
			fmt.Fprintf(&sb, "  %s", s.Error)
		}
		sb.WriteByte('\n')
	}
	if result.InputTokens > 0 {
		fmt.Fprintf(&sb, "  ⚡ %d in / %d out tokens  ~$%.3f\n", result.InputTokens, result.OutputTokens, result.EstimatedCost)
	}
	if h := result.Heal; h != nil {
		fmt.Fprintf(&sb, "  🩹 %s\n", h.Summary())
	}
	t.Logf("lazycue [%s]: %s\n%s", result.Mode, description, sb.String())

	if !result.Pass {
		t.Fatalf("lazycue: test failed: %s", result.Error)
	}
}

// sameSteps reports whether two raw step JSON blobs describe the same sequence
// of DSL steps. It compares the parsed steps (not the raw bytes) so that
// differences in whitespace or key ordering don't count as a change. Used to
// avoid rewriting a cache file when a heal re-emits the already-cached steps
// after a transient flake.
func sameSteps(a, b []byte) bool {
	sa, err := ParseSteps(a)
	if err != nil {
		return false
	}
	sb, err := ParseSteps(b)
	if err != nil {
		return false
	}
	// DeepEqual, not ==: optional fields are pointers, which == compares by
	// address.
	return reflect.DeepEqual(sa, sb)
}

func buildCacheMetadata(opts Options, agentResult *AgentResult, mode string) *CacheMetadata {
	hostname, _ := os.Hostname()
	return &CacheMetadata{
		CreatedAt:        time.Now().UTC(),
		Hostname:         hostname,
		Model:            opts.Model,
		InputTokens:      agentResult.InputTokens,
		OutputTokens:     agentResult.OutputTokens,
		EstimatedCostUSD: agentResult.Cost,
		CIRun:            detectCIRun(),
		GitSHA:           detectGitSHA(),
		Mode:             mode,
	}
}
