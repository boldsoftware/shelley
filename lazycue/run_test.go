package lazycue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Every attempt at a test gets a fresh app from Setup, closed afterwards.
func TestRunSetsUpAFreshAppPerAttempt(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	description := "The page says ready."
	cacheDir := t.TempDir()
	steps := []byte(`[{"action":"navigate","url":"/"},{"action":"assert_text","selector":"#s","text":"ready","timeout":"500ms"}]`)
	if err := SaveCachedTest(cacheDir, description, steps, 1, nil); err != nil {
		t.Fatal(err)
	}
	var setups, closes int
	opts := Options{
		CacheDir: cacheDir,
		Setup: func(context.Context) (*Env, error) {
			setups++
			// Only the second app is ready: the first attempt fails, and
			// the retry passes only because it gets a fresh app.
			state := "starting"
			if setups == 2 {
				state = "ready"
			}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `<p id="s">%s</p>`, state)
			}))
			return &Env{BaseURL: ts.URL, Close: func() { closes++; ts.Close() }}, nil
		},
	}
	r, err := Run(context.Background(), opts, description)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass || r.Mode != RunModeCached {
		t.Errorf("pass=%v mode=%s error=%s, want a cached pass", r.Pass, r.Mode, r.Error)
	}
	if setups != 2 || closes != 2 {
		t.Errorf("%d setups and %d closes, want 2 of each", setups, closes)
	}
}

// With no agent runs left, tests fail rather than generate or heal, and say
// which step failed.
func TestRunWithoutAgentRuns(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	prev := agentRunsLeft.Swap(0)
	t.Cleanup(func() { agentRunsLeft.Store(prev) })
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(ts.Close)
	opts := Options{CacheDir: t.TempDir(), BaseURL: ts.URL}
	broken := "The page has a heading."
	if err := SaveCachedTest(opts.CacheDir, broken, []byte(`[{"action":"navigate","url":"/"},{"action":"assert_visible","selector":"h1","timeout":"100ms"}]`), 1, nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, description := range []string{"Anything at all.", broken} {
			r, err := Run(context.Background(), opts, description)
			if err != nil {
				t.Fatal(err)
			}
			if r.Pass || !strings.Contains(r.Error, "LAZYCUE_MAX_AGENT_RUNS") {
				t.Errorf("%q: pass=%v error=%q, want a failure for the agent run limit", description, r.Pass, r.Error)
			}
			if description == broken && !strings.Contains(r.Error, "step 1 (assert_visible)") {
				t.Errorf("%q: error %q does not name the failed step", description, r.Error)
			}
		}
	}
	if n := agentRunsLeft.Load(); n != 0 {
		t.Errorf("%d agent runs left, want 0", n)
	}
}

// Every run_steps of the agent gets a fresh app and browser, as a cached run
// does: what one run left in localStorage or on the server is gone in the
// next, and its result says what the placeholders were.
func TestAgentRunsGetFreshApps(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run LazyCue browser tests")
	}
	var turns []string // tool results the fake model saw
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		turns = append(turns, string(req.Messages[len(req.Messages)-1].Content))
		run := func(final bool, steps string) string {
			return fmt.Sprintf(`{"content":[{"type":"tool_use","id":"t%d","name":"run_steps","input":{"final":%t,"steps":%s}}],"stop_reason":"tool_use","usage":{}}`, len(turns), final, steps)
		}
		switch len(turns) {
		case 1:
			fmt.Fprint(w, run(false, `[{"action":"navigate","url":"/"},{"action":"eval","expression":"localStorage.setItem('k', 'v'), fetch('/visit').then(r => r.text())","expect":"1"}]`))
		case 2:
			fmt.Fprint(w, run(true, `[{"action":"navigate","url":"/"},{"action":"eval","expression":"String(localStorage.getItem('k'))","expect":"null","timeout":"1s"},{"action":"eval","expression":"fetch('/visit').then(r => r.text())","expect":"1","timeout":"1s"}]`))
		default:
			fmt.Fprint(w, `{"content":[{"type":"text","text":"giving up"}],"stop_reason":"end_turn","usage":{}}`)
		}
	}))
	t.Cleanup(model.Close)
	var setups int
	opts := Options{
		CacheDir:         t.TempDir(),
		AnthropicBaseURL: model.URL,
		AnthropicAPIKey:  "test-key",
		Setup: func(context.Context) (*Env, error) {
			setups++
			var visits int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/visit" {
					visits++
					fmt.Fprint(w, visits)
				}
			}))
			return &Env{BaseURL: ts.URL, Vars: map[string]string{"RUN": fmt.Sprintf("run-%d", setups)}, Close: ts.Close}, nil
		},
	}
	r, err := Run(context.Background(), opts, "Each run starts afresh.")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass || r.Mode != RunModeGenerated {
		t.Fatalf("pass=%v mode=%s error=%s, want a generated pass", r.Pass, r.Mode, r.Error)
	}
	if setups != 2 {
		t.Errorf("%d setups, want one per run_steps", setups)
	}
	if len(turns) != 2 || !strings.Contains(turns[1], "{{RUN}} = run-1") {
		t.Errorf("first run's result does not list its placeholders: %v", turns)
	}
}

// An agent gets a limited number of git commands, then has to test.
func TestAgentGitCommandsAreCapped(t *testing.T) {
	var seen []string // what the fake model saw last on each turn
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, string(req.Messages[len(req.Messages)-1].Content))
		turns := len(seen)
		if turns > maxGitCommands+1 {
			fmt.Fprint(w, `{"content":[{"type":"text","text":"giving up"}],"stop_reason":"end_turn","usage":{}}`)
			return
		}
		fmt.Fprintf(w, `{"content":[{"type":"tool_use","id":"t%d","name":"git_command","input":{"command":"git rev-parse --is-inside-work-tree"}}],"stop_reason":"tool_use","usage":{}}`, turns)
	}))
	t.Cleanup(model.Close)
	opts := Options{
		CacheDir:         t.TempDir(),
		AnthropicBaseURL: model.URL,
		AnthropicAPIKey:  "test-key",
		Setup:            func(context.Context) (*Env, error) { return &Env{BaseURL: "http://127.0.0.1:1"}, nil },
	}
	if _, err := Run(context.Background(), opts, "Explore forever."); err != nil {
		t.Fatal(err)
	}
	limited := fmt.Sprintf("git_command is limited to %d calls", maxGitCommands)
	if len(seen) < maxGitCommands+2 || strings.Contains(seen[maxGitCommands], limited) || !strings.Contains(seen[maxGitCommands+1], limited) {
		t.Errorf("want git commands 1-%d to run and the next refused; saw %q", maxGitCommands, seen)
	}
}

// An agent knows its turn budget, and is told to submit its test when only a
// few turns are left, rather than probing until it runs out.
func TestAgentIsToldWhenTurnsRunOut(t *testing.T) {
	prev := agentTurns
	agentTurns = wrapUpTurns + 3
	t.Cleanup(func() { agentTurns = prev })
	var system string
	var seen []string // what the fake model saw last on each turn
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		system = req.System[0].Text
		seen = append(seen, string(req.Messages[len(req.Messages)-1].Content))
		fmt.Fprintf(w, `{"content":[{"type":"tool_use","id":"t%d","name":"git_command","input":{"command":"git rev-parse --is-inside-work-tree"}}],"stop_reason":"tool_use","usage":{}}`, len(seen))
	}))
	t.Cleanup(model.Close)
	opts := Options{
		CacheDir:         t.TempDir(),
		AnthropicBaseURL: model.URL,
		AnthropicAPIKey:  "test-key",
		Setup:            func(context.Context) (*Env, error) { return &Env{BaseURL: "http://127.0.0.1:1"}, nil },
	}
	r, err := Run(context.Background(), opts, "Probe forever.")
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass || !strings.Contains(r.Error, "exhausted maximum turns") {
		t.Fatalf("pass=%v error=%q, want the turns exhausted", r.Pass, r.Error)
	}
	if want := fmt.Sprintf("at most %d turns", agentTurns); !strings.Contains(system, want) {
		t.Errorf("system prompt does not say %q", want)
	}
	if len(seen) < agentTurns {
		t.Fatalf("%d turns, want %d", len(seen), agentTurns)
	}
	// seen[k] is what the model saw on turn k, after k turns were taken.
	for k, s := range seen[:agentTurns] {
		left := agentTurns - k
		note := fmt.Sprintf("%d of your %d turns left", left, agentTurns)
		if told := strings.Contains(s, "turns left"); told != (left <= wrapUpTurns) {
			t.Errorf("turn %d (%d left): told=%v, want a note only in the last %d turns; saw %q", k, left, told, wrapUpTurns, s)
		} else if told && !strings.Contains(s, note) {
			t.Errorf("turn %d: want %q; saw %q", k, note, s)
		}
		// The note rides in the tool result: text after tool results
		// teaches the model to end its turn and wait for more.
		var blocks []struct {
			Type string `json:"type"`
		}
		if k > 0 && (json.Unmarshal([]byte(s), &blocks) != nil || len(blocks) != 1 || blocks[0].Type != "tool_result") {
			t.Errorf("turn %d: want only the tool result; saw %q", k, s)
		}
	}
}

func TestEnvKnobs(t *testing.T) {
	t.Setenv("LAZYCUE_TEST_KNOB", "")
	if got := envInt("LAZYCUE_TEST_KNOB", 25); got != 25 {
		t.Errorf("unset int = %d, want the default", got)
	}
	if got := envDuration("LAZYCUE_TEST_KNOB", 5*time.Minute); got != 5*time.Minute {
		t.Errorf("unset duration = %v, want the default", got)
	}
	t.Setenv("LAZYCUE_TEST_KNOB", "60")
	if got := envInt("LAZYCUE_TEST_KNOB", 25); got != 60 {
		t.Errorf("int = %d, want 60", got)
	}
	t.Setenv("LAZYCUE_TEST_KNOB", "15m")
	if got := envDuration("LAZYCUE_TEST_KNOB", 5*time.Minute); got != 15*time.Minute {
		t.Errorf("duration = %v, want 15m", got)
	}
	for _, bad := range []string{"15", "-1m", "0s", "soon"} {
		t.Setenv("LAZYCUE_TEST_KNOB", bad)
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("duration %q did not panic", bad)
				}
			}()
			envDuration("LAZYCUE_TEST_KNOB", 5*time.Minute)
		}()
	}
	t.Setenv("LAZYCUE_TEST_KNOB", "many")
	defer func() {
		if recover() == nil {
			t.Error("int \"many\" did not panic")
		}
	}()
	envInt("LAZYCUE_TEST_KNOB", 25)
}
