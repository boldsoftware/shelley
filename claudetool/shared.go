// Package claudetool provides tools for Claude AI models.
//
// When adding, removing, or modifying tools in this package,
// remember to update the tool display template in termui/termui.go
// to ensure proper tool output formatting.
package claudetool

import (
	"context"
	"fmt"

	"shelley.exe.dev/llm"
)

// FlexBool is a bool that also accepts string values "true" and "false" in JSON.
// LLMs sometimes send boolean values as strings.
type FlexBool bool

func (b *FlexBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", `"true"`:
		*b = true
	case "false", `"false"`:
		*b = false
	default:
		return fmt.Errorf("invalid bool value: %s", data)
	}
	return nil
}

type workingDirCtxKeyType string

const workingDirCtxKey workingDirCtxKeyType = "workingDir"

func WithWorkingDir(ctx context.Context, wd string) context.Context {
	return context.WithValue(ctx, workingDirCtxKey, wd)
}

func WorkingDir(ctx context.Context) string {
	// If cmd.Dir is empty, it uses the current working directory,
	// so we can use that as a fallback.
	wd, _ := ctx.Value(workingDirCtxKey).(string)
	return wd
}

type sessionIDCtxKeyType string

const sessionIDCtxKey sessionIDCtxKeyType = "sessionID"

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDCtxKey, sessionID)
}

func SessionID(ctx context.Context) string {
	sessionID, _ := ctx.Value(sessionIDCtxKey).(string)
	return sessionID
}

type toolProgressCtxKeyType string

const toolProgressCtxKey toolProgressCtxKeyType = "toolProgress"

type toolUseIDCtxKeyType string

const toolUseIDCtxKey toolUseIDCtxKeyType = "toolUseID"

// WithToolProgress returns a context with the given ToolProgressFunc.
func WithToolProgress(ctx context.Context, fn llm.ToolProgressFunc) context.Context {
	return context.WithValue(ctx, toolProgressCtxKey, fn)
}

// GetToolProgress retrieves the ToolProgressFunc from the context, or nil.
func GetToolProgress(ctx context.Context) llm.ToolProgressFunc {
	fn, _ := ctx.Value(toolProgressCtxKey).(llm.ToolProgressFunc)
	return fn
}

// WithToolUseID returns a context with the given tool use ID.
func WithToolUseID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolUseIDCtxKey, id)
}
