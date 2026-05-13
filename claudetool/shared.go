// Package claudetool provides tools for Claude AI models.
//
// When adding, removing, or modifying tools in this package,
// remember to update the tool display template in termui/termui.go
// to ensure proper tool output formatting.
package claudetool

import (
	"context"

	"shelley.exe.dev/llm"
)

func WithWorkingDir(ctx context.Context, wd string) context.Context {
	return llm.WithWorkingDir(ctx, wd)
}

func WorkingDir(ctx context.Context) string {
	return llm.WorkingDir(ctx)
}

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return llm.WithSessionID(ctx, sessionID)
}

func SessionID(ctx context.Context) string {
	return llm.SessionID(ctx)
}

// WithToolProgress returns a context with the given ToolProgressFunc.
func WithToolProgress(ctx context.Context, fn llm.ToolProgressFunc) context.Context {
	return llm.WithToolProgress(ctx, fn)
}

// GetToolProgress retrieves the ToolProgressFunc from the context, or nil.
func GetToolProgress(ctx context.Context) llm.ToolProgressFunc {
	return llm.GetToolProgress(ctx)
}

// WithToolUseID returns a context with the given tool use ID.
func WithToolUseID(ctx context.Context, id string) context.Context {
	return llm.WithToolUseID(ctx, id)
}

func ToolUseID(ctx context.Context) string {
	return llm.ToolUseID(ctx)
}
