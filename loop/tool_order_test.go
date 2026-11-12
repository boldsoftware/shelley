package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"shelley.exe.dev/llm"
)

// groupHarness provides fake tools that share a fake working directory.
type groupHarness struct {
	mu      sync.Mutex
	cwd     string
	log     []string
	release map[string]chan struct{}
	cancel  context.CancelFunc
}

func newGroupHarness(cancel context.CancelFunc, blocked ...string) *groupHarness {
	h := &groupHarness{cwd: "/start", release: map[string]chan struct{}{}, cancel: cancel}
	for _, name := range blocked {
		h.release[name] = make(chan struct{})
	}
	return h
}

func (h *groupHarness) assertLog(t *testing.T, want ...string) {
	t.Helper()
	h.mu.Lock()
	got := slices.Clone(h.log)
	h.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

type groupInput struct {
	Name string `json:"name"`
}

type groupDirInput struct {
	Path string `json:"path"`
}

// tool records "name@cwd" when it starts, then waits for its release channel.
// A backgrounded tool reports that its work is still running.
func (h *groupHarness) tool(toolName string, backgrounded, endsTurn bool) *llm.Tool {
	return &llm.Tool{
		Name:     toolName,
		EndsTurn: endsTurn,
		Run: llm.RunJSON(func(_ context.Context, in groupInput) llm.ToolOut {
			h.mu.Lock()
			h.log = append(h.log, in.Name+"@"+h.cwd)
			h.mu.Unlock()
			if ch := h.release[in.Name]; ch != nil {
				<-ch
			}
			return llm.ToolOut{LLMContent: llm.TextContent("ok:" + in.Name), Backgrounded: backgrounded}
		}),
	}
}

// cancelTool cancels the run and reports cancellation.
func (h *groupHarness) cancelTool() *llm.Tool {
	return &llm.Tool{Name: "cancel", Run: func(context.Context, json.RawMessage) llm.ToolOut {
		h.cancel()
		return llm.ErrorToolOut(context.Canceled)
	}}
}

// changeDir fails for paths containing "missing", and cancels the run for
// paths containing "cancel".
func (h *groupHarness) changeDir() *llm.Tool {
	return &llm.Tool{Name: "change_dir", Run: llm.RunJSON(func(_ context.Context, in groupDirInput) llm.ToolOut {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.log = append(h.log, "cd:"+in.Path)
		target := in.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(h.cwd, target)
		}
		switch {
		case in.Path == "":
			return llm.ErrorfToolOut("path is required")
		case strings.Contains(target, "cancel"):
			h.cancel()
			return llm.ErrorfToolOut("cancelled")
		case strings.Contains(target, "missing"):
			return llm.ErrorfToolOut("no such directory")
		}
		h.cwd = target
		return llm.ToolOut{LLMContent: llm.TextContent("cwd:" + target)}
	})}
}

func (h *groupHarness) tools() []*llm.Tool {
	return []*llm.Tool{
		h.tool("work", false, false),
		h.tool("bg", true, false),
		h.tool("finish", false, true),
		h.cancelTool(),
		h.changeDir(),
	}
}

func use(id, tool, input string) llm.Content {
	return llm.Content{ID: id, Type: llm.ContentTypeToolUse, ToolName: tool, ToolInput: []byte(input)}
}

func call(tool, name string) llm.Content {
	return use(name, tool, fmt.Sprintf(`{"name":%q}`, name))
}

func cd(id, path string) llm.Content {
	return use(id, "change_dir", fmt.Sprintf(`{"path":%q}`, path))
}

type groupRun struct {
	results []llm.Content
	err     error
}

func startGroups(ctx context.Context, tools []*llm.Tool, calls []llm.Content) <-chan groupRun {
	var recorded []llm.Message
	loop := NewLoop(Config{Tools: tools, RecordMessage: func(_ context.Context, m llm.Message, _ llm.Usage, _ []llm.PurposedUsage) error {
		recorded = append(recorded, m)
		return nil
	}})
	done := make(chan groupRun, 1)
	go func() {
		err := loop.executeToolCalls(ctx, calls)
		var results []llm.Content
		if len(recorded) > 0 {
			results = recorded[0].Content // the single tool-result message
		}
		done <- groupRun{results, err}
	}()
	return done
}

// assertResults checks result order by tool-use ID and a summary of each result.
func assertResults(t *testing.T, got groupRun, calls []llm.Content, want ...string) {
	t.Helper()
	if len(got.results) != len(calls) {
		t.Fatalf("got %d results, want %d", len(got.results), len(calls))
	}
	for i, r := range got.results {
		if r.ToolUseID != calls[i].ID {
			t.Errorf("result %d has ID %q, want %q", i, r.ToolUseID, calls[i].ID)
		}
		text := r.ToolResult[0].Text
		switch {
		case text == notExecutedToolResultText:
			text = "notstarted"
		case text == directoryFailedText:
			text = "skipped"
		case text == backgroundedText:
			text = "backgrounded"
		case strings.Contains(text, "turn-ending tools must be called alone"):
			text = "rejected"
		case r.ToolError:
			text = "error"
		}
		if text != want[i] {
			t.Errorf("result %d (%s) = %q, want %q", i, calls[i].ID, text, want[i])
		}
	}
}

func TestToolCallsRunOneAtATimeInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		h := newGroupHarness(cancel, "a", "c")
		calls := []llm.Content{call("work", "a"), cd("cd1", "/a"), call("work", "c"), call("work", "d"), cd("cd2", "b"), call("work", "e")}
		done := startGroups(ctx, h.tools(), calls)

		synctest.Wait()
		h.assertLog(t, "a@/start")
		close(h.release["a"])
		synctest.Wait()
		h.assertLog(t, "a@/start", "cd:/a", "c@/a") // d waits for c
		close(h.release["c"])

		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		h.assertLog(t, "a@/start", "cd:/a", "c@/a", "d@/a", "cd:b", "e@/a/b")
		assertResults(t, got, calls, "ok:a", "cwd:/a", "ok:c", "ok:d", "cwd:/a/b", "ok:e")
	})
}

func TestToolCallsBackgroundedSkipsTheRest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := newGroupHarness(cancel)
	calls := []llm.Content{call("work", "a"), call("bg", "b"), cd("abs", "/abs"), call("work", "c")}
	got := <-startGroups(ctx, h.tools(), calls)
	if got.err != nil {
		t.Fatal(got.err)
	}
	assertResults(t, got, calls, "ok:a", "ok:b", "backgrounded", "backgrounded")
	h.assertLog(t, "a@/start", "b@/start")
}

func TestToolCallsFailureSkipsUntilAbsoluteRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := newGroupHarness(cancel)
	calls := []llm.Content{
		call("work", "x"),
		cd("fail", "missing"),
		call("work", "y"),
		cd("relative", "rel"),
		cd("empty", ""),
		use("malformed", "change_dir", `{bad`),
		cd("abs-fails", "/missing-abs"), // absolute changes run, but this one fails again
		call("work", "z"),
		cd("abs-ok", "/good"),
		call("work", "w"),
		cd("rel-ok", "sub"), // relative changes resume after recovery
		call("work", "v"),
	}
	got := <-startGroups(ctx, h.tools(), calls)
	if got.err != nil {
		t.Fatal(got.err)
	}
	assertResults(t, got, calls, "ok:x", "error", "skipped", "skipped", "skipped", "skipped", "error", "skipped", "cwd:/good", "ok:w", "cwd:/good/sub", "ok:v")
	h.assertLog(t, "x@/start", "cd:missing", "cd:/missing-abs", "cd:/good", "w@/good", "cd:sub", "v@/good/sub")
}

func TestToolCallsCancellationBlocksLaterGroups(t *testing.T) {
	for _, tc := range []struct {
		name    string
		calls   []llm.Content
		wantLog []string
	}{
		{"ordinary group", []llm.Content{use("cancel", "cancel", `{}`), cd("abs", "/abs"), call("work", "w"), cd("abs2", "/abs2")}, nil},
		{"failed directory", []llm.Content{cd("fail", "cancel-missing"), cd("abs", "/abs"), call("work", "w")}, []string{"cd:cancel-missing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h := newGroupHarness(cancel)
			got := <-startGroups(ctx, h.tools(), tc.calls)
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", got.err)
			}
			want := []string{"error"}
			for range tc.calls[1:] {
				want = append(want, "notstarted")
			}
			assertResults(t, got, tc.calls, want...)
			h.assertLog(t, tc.wantLog...)
		})
	}
}

func TestToolCallsTurnEndingMustBeWholeResponse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		calls    []llm.Content
		wantErr  error
		want     []string
		wantLogs []string
	}{
		{"alone", []llm.Content{call("finish", "f")}, errToolEndedTurn, []string{"ok:f"}, []string{"f@/start"}},
		{"after change_dir", []llm.Content{cd("cd", "/a"), call("finish", "f")}, nil, []string{"cwd:/a", "rejected"}, []string{"cd:/a"}},
		{"before change_dir", []llm.Content{call("finish", "f"), cd("cd", "/a")}, nil, []string{"rejected", "cwd:/a"}, []string{"cd:/a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h := newGroupHarness(cancel)
			got := <-startGroups(ctx, h.tools(), tc.calls)
			if !errors.Is(got.err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", got.err, tc.wantErr)
			}
			assertResults(t, got, tc.calls, tc.want...)
			h.assertLog(t, tc.wantLogs...)
		})
	}
}
