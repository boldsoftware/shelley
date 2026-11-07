package llm

import "context"

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

type llmServiceCtxKeyType string

const llmServiceCtxKey llmServiceCtxKeyType = "llmService"

// WithLLMService attaches the LLM service this tool call is running under to
// ctx. Tools that need provider-specific knowledge (e.g. image size limits)
// can retrieve it with ServiceFromContext.
func WithLLMService(ctx context.Context, svc Service) context.Context {
	return context.WithValue(ctx, llmServiceCtxKey, svc)
}

// ServiceFromContext returns the LLM service attached to ctx by WithLLMService,
// or nil if none was set (e.g. in tests that invoke a tool directly).
func ServiceFromContext(ctx context.Context) Service {
	svc, _ := ctx.Value(llmServiceCtxKey).(Service)
	return svc
}
