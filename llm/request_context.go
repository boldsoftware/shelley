package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type requestContextKey int

const (
	requestTraceContextKey requestContextKey = iota
	purposeContextKey
	usageCollectorContextKey
	progressContextKey
)

// RequestTrace collects provider-neutral named diagnostics in insertion order.
type RequestTrace struct {
	mu     sync.Mutex
	order  []string
	values map[string]string
}

// Set records a non-empty diagnostic, replacing its value without reordering it.
func (t *RequestTrace) Set(name, value string) {
	if name == "" || value == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.values == nil {
		t.values = make(map[string]string)
	}
	if _, ok := t.values[name]; !ok {
		t.order = append(t.order, name)
	}
	t.values[name] = value
}

// Value returns the named diagnostic, or "" if it was not recorded.
func (t *RequestTrace) Value(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.values[name]
}

// String returns the diagnostics as a compact, deterministic string.
func (t *RequestTrace) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := make([]string, 0, len(t.order))
	for _, name := range t.order {
		parts = append(parts, fmt.Sprintf("%s=%s", name, t.values[name]))
	}
	return strings.Join(parts, " ")
}

// WithRequestTrace attaches a new RequestTrace to ctx and returns both.
func WithRequestTrace(ctx context.Context) (context.Context, *RequestTrace) {
	trace := &RequestTrace{}
	return context.WithValue(ctx, requestTraceContextKey, trace), trace
}

// RequestTraceFromContext returns the RequestTrace attached to ctx, if any.
func RequestTraceFromContext(ctx context.Context) *RequestTrace {
	trace, _ := ctx.Value(requestTraceContextKey).(*RequestTrace)
	return trace
}

// WithPurpose tags an indirect LLM call with its usage purpose.
func WithPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, purposeContextKey, purpose)
}

// PurposeFromContext returns the indirect LLM call purpose, if any.
func PurposeFromContext(ctx context.Context) string {
	purpose, _ := ctx.Value(purposeContextKey).(string)
	return purpose
}

// UsageCollector receives the usage of one indirect LLM call.
// Implementations must be safe for concurrent use.
type UsageCollector func(purpose string, usage Usage)

// WithUsageCollector returns a context that collects indirect LLM usage.
func WithUsageCollector(ctx context.Context, collector UsageCollector) context.Context {
	return context.WithValue(ctx, usageCollectorContextKey, collector)
}

// UsageCollectorFromContext returns the indirect usage collector, if any.
func UsageCollectorFromContext(ctx context.Context) UsageCollector {
	collector, _ := ctx.Value(usageCollectorContextKey).(UsageCollector)
	return collector
}

// UsageAccumulator safely accumulates indirect usage entries.
type UsageAccumulator struct {
	mu      sync.Mutex
	entries []PurposedUsage
}

// Collect implements UsageCollector.
func (a *UsageAccumulator) Collect(purpose string, usage Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, PurposedUsage{Purpose: purpose, Usage: usage})
}

// Take returns and resets the accumulated entries.
func (a *UsageAccumulator) Take() []PurposedUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	entries := a.entries
	a.entries = nil
	return entries
}

// WithProgress attaches f to ctx. Providers call it, via ReportProgress, each
// time a streamed response delivers a model event; keepalives do not count.
func WithProgress(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, progressContextKey, f)
}

// ReportProgress calls the function attached by WithProgress, if any.
func ReportProgress(ctx context.Context) {
	if f, ok := ctx.Value(progressContextKey).(func()); ok {
		f()
	}
}
