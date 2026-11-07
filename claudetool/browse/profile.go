package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/cdproto/profiler"
	"github.com/chromedp/cdproto/tracing"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"shelley.exe.dev/llm"
)

func (b *BrowseTools) profileHelp() llm.ToolOut {
	help := `browser performance profiling — actions on the browser tool.

Actions (pass as the browser tool's "action"):
  profile_help            — Show this help message.

  profile_metrics         — Get a snapshot of performance metrics from the
                    browser. Returns timing, memory, DOM, and layout metrics
                    as an aligned text table.

  profile_cpu_start       — Start CPU profiling via the Chrome DevTools
                    Profiler. Records JavaScript execution samples.

  profile_cpu_stop        — Stop CPU profiling and save the profile to a JSON
                    file. Returns the file path. The file can be loaded in
                    Chrome DevTools (Performance tab) for analysis.

  profile_trace_start     — Start a Chrome trace recording.
                    Parameters:
                      categories (string, optional) — comma-separated trace
                        categories, e.g. "devtools.timeline,v8.execute".
                        If omitted, default categories are used.

  profile_trace_stop      — Stop tracing and save the trace to a JSON file.
                    Returns the file path and event count. The file can be
                    loaded in Chrome DevTools (Performance tab) or
                    chrome://tracing.

  profile_coverage_start  — Start collecting precise JavaScript code coverage.

  profile_coverage_stop   — Stop coverage collection and save results to a JSON
                    file. Returns the file path with per-script coverage data.

Typical workflows:
  CPU profiling:  profile_cpu_start → interact with page → profile_cpu_stop
  Tracing:        profile_trace_start → interact → profile_trace_stop
  Coverage:       profile_coverage_start → interact → profile_coverage_stop`

	return llm.ToolOut{LLMContent: llm.TextContent(help)}
}

func (b *BrowseTools) profileMetrics(ctx context.Context, timeout string) llm.ToolOut {
	var metrics []*performance.Metric
	err := b.browserRun(ctx, timeout, "failed to get performance metrics", chromedp.ActionFunc(func(ctx context.Context) error {
		if err := performance.Enable().Do(ctx); err != nil {
			return err
		}
		var e error
		metrics, e = performance.GetMetrics().Do(ctx)
		return e
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if len(metrics) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("No performance metrics available.")}
	}

	// Find max name length for alignment
	maxLen := 0
	for _, m := range metrics {
		if len(m.Name) > maxLen {
			maxLen = len(m.Name)
		}
	}

	var out string
	out = fmt.Sprintf("Performance metrics (%d entries):\n\n", len(metrics))
	for _, m := range metrics {
		out += fmt.Sprintf("  %-*s  %g\n", maxLen, m.Name, m.Value)
	}

	return llm.ToolOut{LLMContent: llm.TextContent(out)}
}

func (b *BrowseTools) profileCPUStart(ctx context.Context, timeout string) llm.ToolOut {
	b.traceMutex.Lock()
	if b.profilingActive {
		b.traceMutex.Unlock()
		return llm.ToolOut{LLMContent: llm.TextContent("CPU profiling is already active.")}
	}
	b.traceMutex.Unlock()

	err := b.browserRun(ctx, timeout, "failed to start CPU profiling", chromedp.ActionFunc(func(ctx context.Context) error {
		if err := profiler.Enable().Do(ctx); err != nil {
			return err
		}
		return profiler.Start().Do(ctx)
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	b.profilingActive = true
	b.traceMutex.Unlock()
	return llm.ToolOut{LLMContent: llm.TextContent("CPU profiling started.")}
}

func (b *BrowseTools) profileCPUStop(ctx context.Context, timeout string) llm.ToolOut {
	b.traceMutex.Lock()
	if !b.profilingActive {
		b.traceMutex.Unlock()
		return llm.ErrorfToolOut("CPU profiling is not active — call cpu_start first")
	}
	b.traceMutex.Unlock()

	var profile *profiler.Profile
	err := b.browserRun(ctx, timeout, "failed to stop CPU profiling", chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		profile, e = profiler.Stop().Do(ctx)
		if e != nil {
			return e
		}
		return profiler.Disable().Do(ctx)
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	b.profilingActive = false
	b.traceMutex.Unlock()

	data, err := json.Marshal(profile)
	if err != nil {
		return llm.ErrorfToolOut("failed to marshal CPU profile: %w", err)
	}

	filename := fmt.Sprintf("cpu_profile_%s.json", uuid.New().String()[:8])
	filePath := filepath.Join(ConsoleLogsDir, filename)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return llm.ErrorfToolOut("failed to write CPU profile: %w", err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("CPU profile saved to: %s", filePath))}
}

func (b *BrowseTools) profileTraceStart(ctx context.Context, timeout, categories string) llm.ToolOut {
	b.traceMutex.Lock()
	if b.tracingActive {
		b.traceMutex.Unlock()
		return llm.ToolOut{LLMContent: llm.TextContent("Tracing is already active.")}
	}
	// Clear previous trace data and set up completion channel
	b.traceEvents = nil
	b.traceCompleteCh = make(chan struct{}, 1)
	b.traceMutex.Unlock()

	err := b.browserRun(ctx, timeout, "failed to start tracing", chromedp.ActionFunc(func(ctx context.Context) error {
		params := tracing.Start()
		if categories != "" {
			catList := strings.Split(categories, ",")
			for i := range catList {
				catList[i] = strings.TrimSpace(catList[i])
			}
			params = params.WithTraceConfig(&tracing.TraceConfig{
				IncludedCategories: catList,
			})
		}
		return params.Do(ctx)
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	b.tracingActive = true
	b.traceMutex.Unlock()

	msg := "Tracing started."
	if categories != "" {
		msg = fmt.Sprintf("Tracing started with categories: %s", categories)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(msg)}
}

// profileTraceStop runs tracing.End itself rather than via browserRun because
// it needs the browser context to notice a browser death while waiting for
// the trace data.
func (b *BrowseTools) profileTraceStop(ctx context.Context, timeout string) llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	if !b.tracingActive {
		b.traceMutex.Unlock()
		return llm.ErrorfToolOut("tracing is not active — call trace_start first")
	}
	doneCh := b.traceCompleteCh
	b.traceMutex.Unlock()

	actionCtx, cancel := browserActionContext(ctx, browserCtx, timeout)
	defer cancel()
	if err := chromedp.Run(actionCtx, tracing.End()); err != nil {
		return llm.ErrorfToolOut("failed to stop tracing: %w", err)
	}

	// Wait with timeout, respecting context cancellation
	select {
	case <-doneCh:
	case <-browserCtx.Done():
		return llm.ErrorfToolOut("browser context cancelled while waiting for trace data")
	case <-time.After(30 * time.Second):
		return llm.ErrorfToolOut("timeout waiting for trace data")
	}

	// Collect trace events
	b.traceMutex.Lock()
	b.tracingActive = false
	b.traceCompleteCh = nil
	events := make([]json.RawMessage, len(b.traceEvents))
	copy(events, b.traceEvents)
	b.traceEvents = nil
	b.traceMutex.Unlock()

	// Write as {"traceEvents":[...]}
	wrapper := struct {
		TraceEvents []json.RawMessage `json:"traceEvents"`
	}{TraceEvents: events}

	data, err := json.Marshal(wrapper)
	if err != nil {
		return llm.ErrorfToolOut("failed to marshal trace data: %w", err)
	}

	filename := fmt.Sprintf("trace_%s.json", uuid.New().String()[:8])
	filePath := filepath.Join(ConsoleLogsDir, filename)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return llm.ErrorfToolOut("failed to write trace data: %w", err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
		"Trace saved to: %s (%d events)", filePath, len(events),
	))}
}

func (b *BrowseTools) profileCoverageStart(ctx context.Context, timeout string) llm.ToolOut {
	err := b.browserRun(ctx, timeout, "failed to start coverage", chromedp.ActionFunc(func(ctx context.Context) error {
		if err := profiler.Enable().Do(ctx); err != nil {
			return err
		}
		_, err := profiler.StartPreciseCoverage().Do(ctx)
		return err
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("JavaScript coverage collection started.")}
}

func (b *BrowseTools) profileCoverageStop(ctx context.Context, timeout string) llm.ToolOut {
	var coverage []*profiler.ScriptCoverage
	err := b.browserRun(ctx, timeout, "failed to stop coverage", chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		coverage, _, e = profiler.TakePreciseCoverage().Do(ctx)
		if e != nil {
			return e
		}
		if err := profiler.StopPreciseCoverage().Do(ctx); err != nil {
			return err
		}
		return profiler.Disable().Do(ctx)
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	data, err := json.Marshal(coverage)
	if err != nil {
		return llm.ErrorfToolOut("failed to marshal coverage data: %w", err)
	}

	filename := fmt.Sprintf("coverage_%s.json", uuid.New().String()[:8])
	filePath := filepath.Join(ConsoleLogsDir, filename)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return llm.ErrorfToolOut("failed to write coverage data: %w", err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
		"Coverage data saved to: %s (%d scripts)", filePath, len(coverage),
	))}
}
