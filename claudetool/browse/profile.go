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

// profileInput is the input schema for the browser_profile tool.
type profileInput struct {
	Action     string `json:"action"`
	Categories string `json:"categories,omitempty"`
}

// ProfileTool returns the browser_profile tool for performance profiling and tracing.
func (b *BrowseTools) ProfileTool() *llm.Tool {
	description := `Performance profiling and tracing. Actions: help, metrics, cpu_start, cpu_stop, trace_start, trace_stop, coverage_start, coverage_stop.`

	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "The profiling action to perform",
				"enum": ["help", "metrics", "cpu_start", "cpu_stop", "trace_start", "trace_stop", "coverage_start", "coverage_stop"]
			},
			"categories": {
				"type": "string",
				"description": "Comma-separated trace categories (trace_start action, optional)"
			}
		},
		"required": ["action"]
	}`

	return &llm.Tool{
		Name:        "browser_profile",
		Description: description,
		InputSchema: json.RawMessage(schema),
		Run:         llm.RunJSON(b.profileRun),
	}
}

func (b *BrowseTools) profileRun(ctx context.Context, input profileInput) llm.ToolOut {
	switch input.Action {
	case "help":
		return b.profileHelp()
	case "metrics":
		return b.profileMetrics()
	case "cpu_start":
		return b.profileCPUStart()
	case "cpu_stop":
		return b.profileCPUStop()
	case "trace_start":
		return b.profileTraceStart(input.Categories)
	case "trace_stop":
		return b.profileTraceStop()
	case "coverage_start":
		return b.profileCoverageStart()
	case "coverage_stop":
		return b.profileCoverageStop()
	default:
		return llm.ErrorfToolOut("unknown action: %q — use \"help\" to see available actions", input.Action)
	}
}

func (b *BrowseTools) profileHelp() llm.ToolOut {
	help := `browser_profile — Performance profiling and tracing.

Actions:
  help            — Show this help message.

  metrics         — Get a snapshot of performance metrics from the browser.
                    Returns timing, memory, DOM, and layout metrics as an
                    aligned text table.

  cpu_start       — Start CPU profiling via the Chrome DevTools Profiler.
                    The profiler records JavaScript execution samples.

  cpu_stop        — Stop CPU profiling and save the profile to a JSON file.
                    Returns the file path. The file can be loaded in Chrome
                    DevTools (Performance tab) for analysis.

  trace_start     — Start a Chrome trace recording.
                    Parameters:
                      categories (string, optional) — comma-separated trace
                        categories, e.g. "devtools.timeline,v8.execute".
                        If omitted, default categories are used.

  trace_stop      — Stop tracing and save the trace to a JSON file.
                    Returns the file path and event count. The file can be
                    loaded in Chrome DevTools (Performance tab) or
                    chrome://tracing.

  coverage_start  — Start collecting precise JavaScript code coverage.

  coverage_stop   — Stop coverage collection and save results to a JSON file.
                    Returns the file path with per-script coverage data.

Typical workflows:
  CPU profiling:  cpu_start → interact with page → cpu_stop
  Tracing:        trace_start → interact with page → trace_stop
  Coverage:       coverage_start → interact with page → coverage_stop`

	return llm.ToolOut{LLMContent: llm.TextContent(help)}
}

func (b *BrowseTools) profileMetrics() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	var metrics []*performance.Metric
	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := performance.Enable().Do(ctx); err != nil {
			return err
		}
		var e error
		metrics, e = performance.GetMetrics().Do(ctx)
		return e
	}))
	if err != nil {
		return llm.ErrorfToolOut("failed to get performance metrics: %w", err)
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

func (b *BrowseTools) profileCPUStart() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	if b.profilingActive {
		b.traceMutex.Unlock()
		return llm.ToolOut{LLMContent: llm.TextContent("CPU profiling is already active.")}
	}
	b.traceMutex.Unlock()

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := profiler.Enable().Do(ctx); err != nil {
			return err
		}
		return profiler.Start().Do(ctx)
	}))
	if err != nil {
		return llm.ErrorfToolOut("failed to start CPU profiling: %w", err)
	}

	b.traceMutex.Lock()
	b.profilingActive = true
	b.traceMutex.Unlock()
	return llm.ToolOut{LLMContent: llm.TextContent("CPU profiling started.")}
}

func (b *BrowseTools) profileCPUStop() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	if !b.profilingActive {
		b.traceMutex.Unlock()
		return llm.ErrorfToolOut("CPU profiling is not active — call cpu_start first")
	}
	b.traceMutex.Unlock()

	var profile *profiler.Profile
	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		profile, e = profiler.Stop().Do(ctx)
		if e != nil {
			return e
		}
		return profiler.Disable().Do(ctx)
	}))
	if err != nil {
		return llm.ErrorfToolOut("failed to stop CPU profiling: %w", err)
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

func (b *BrowseTools) profileTraceStart(categories string) llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.traceMutex.Lock()
	if b.tracingActive {
		b.traceMutex.Unlock()
		return llm.ToolOut{LLMContent: llm.TextContent("Tracing is already active.")}
	}
	// Clear previous trace data and set up completion channel
	b.traceEvents = nil
	b.traceCompleteCh = make(chan struct{}, 1)
	b.traceMutex.Unlock()

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
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
		return llm.ErrorfToolOut("failed to start tracing: %w", err)
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

func (b *BrowseTools) profileTraceStop() llm.ToolOut {
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

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return tracing.End().Do(ctx)
	}))
	if err != nil {
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

func (b *BrowseTools) profileCoverageStart() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := profiler.Enable().Do(ctx); err != nil {
			return err
		}
		_, err := profiler.StartPreciseCoverage().Do(ctx)
		return err
	}))
	if err != nil {
		return llm.ErrorfToolOut("failed to start coverage: %w", err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("JavaScript coverage collection started.")}
}

func (b *BrowseTools) profileCoverageStop() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	var coverage []*profiler.ScriptCoverage
	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
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
		return llm.ErrorfToolOut("failed to stop coverage: %w", err)
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
