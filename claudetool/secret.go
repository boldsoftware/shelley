package claudetool

import (
	"context"
	"encoding/json"

	"shelley.exe.dev/llm"
)

// Secrets is Shelley's secret store as tools see it (docs/secrets.md): the
// server takes secrets out of the user's messages, and `shelley secret ID`
// reads one into a file.
type Secrets interface {
	// Redact replaces every stored secret's value in s with its marker.
	Redact(ctx context.Context, s string) (string, error)
}

// redactTools wraps each tool so its results, errors, display data and
// progress pass through secrets.Redact before anything records, shows or
// sends them. A failed redaction fails the call, dropping its output.
func redactTools(tools []*llm.Tool, secrets Secrets) []*llm.Tool {
	out := make([]*llm.Tool, len(tools))
	for i, t := range tools {
		if t.Run == nil {
			out[i] = t
			continue
		}
		wrapped := *t
		run := t.Run
		wrapped.Run = func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			if progress := llm.GetToolProgress(ctx); progress != nil {
				ctx = llm.WithToolProgress(ctx, func(p llm.ToolProgress) {
					var err error
					if p.Output, err = secrets.Redact(ctx, p.Output); err != nil {
						p.Output = ""
					}
					progress(p)
				})
			}
			out, err := redactToolOut(ctx, secrets, run(ctx, input))
			if err != nil {
				return llm.ErrorfToolOut("redacting secrets from the result: %v", err)
			}
			return out
		}
		out[i] = &wrapped
	}
	return out
}

func redactToolOut(ctx context.Context, secrets Secrets, out llm.ToolOut) (llm.ToolOut, error) {
	var err error
	if out.Error != nil {
		msg, rerr := secrets.Redact(ctx, out.Error.Error())
		if rerr != nil {
			return out, rerr
		}
		if msg != out.Error.Error() {
			out.Error = redactedError{msg: msg, err: out.Error}
		}
	}
	if out.LLMContent, err = redactContents(ctx, secrets, out.LLMContent); err != nil {
		return out, err
	}
	if out.Display != nil {
		raw, err := json.Marshal(out.Display)
		if err != nil {
			return out, err
		}
		redacted, err := secrets.Redact(ctx, string(raw))
		if err != nil {
			return out, err
		}
		if redacted != string(raw) {
			out.Display = json.RawMessage(redacted)
		}
	}
	return out, nil
}

func redactContents(ctx context.Context, secrets Secrets, contents []llm.Content) ([]llm.Content, error) {
	if contents == nil {
		return nil, nil
	}
	out := make([]llm.Content, len(contents))
	for i, c := range contents {
		var err error
		if c.Text, err = secrets.Redact(ctx, c.Text); err != nil {
			return nil, err
		}
		if c.ToolResult, err = redactContents(ctx, secrets, c.ToolResult); err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

// redactedError is a tool error with secrets redacted from its message,
// still matching the original with errors.Is and errors.As.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }
