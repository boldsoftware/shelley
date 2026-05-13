package llm

import "context"

type workingDirCtxKeyType string

const workingDirCtxKey workingDirCtxKeyType = "workingDir"

func WithWorkingDir(ctx context.Context, wd string) context.Context {
	return context.WithValue(ctx, workingDirCtxKey, wd)
}

func WorkingDir(ctx context.Context) string {
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

func WithToolProgress(ctx context.Context, fn ToolProgressFunc) context.Context {
	return context.WithValue(ctx, toolProgressCtxKey, fn)
}

func GetToolProgress(ctx context.Context) ToolProgressFunc {
	fn, _ := ctx.Value(toolProgressCtxKey).(ToolProgressFunc)
	return fn
}

type toolUseIDCtxKeyType string

const toolUseIDCtxKey toolUseIDCtxKeyType = "toolUseID"

func WithToolUseID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolUseIDCtxKey, id)
}

func ToolUseID(ctx context.Context) string {
	id, _ := ctx.Value(toolUseIDCtxKey).(string)
	return id
}
