package claudetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"shelley.exe.dev/llm"
)

// Secrets is Shelley's secret store as tools see it (docs/secrets.md):
// messages hold {{secret:ID}}, and the value only leaves the store into a
// file the write_secret tool writes.
type Secrets interface {
	// ToolEnabled reports whether write_secret is offered (the secrets
	// feature flag).
	ToolEnabled(ctx context.Context) bool
	// Value is the secret's plaintext.
	Value(ctx context.Context, id string) (string, error)
	// RecordInjection notes that the secret was written to path on machine.
	RecordInjection(ctx context.Context, id, conversationID, machine, path string) error
	// Redact replaces every stored secret's value in s with its
	// placeholder.
	Redact(ctx context.Context, s string) (string, error)
}

// SecretIDPattern matches a secret's ID, as in {{secret:ID}}.
var SecretIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// SecretPlaceholder is what messages hold instead of a secret's value.
func SecretPlaceholder(id string) string { return "{{secret:" + id + "}}" }

const (
	WriteSecretName        = "write_secret"
	writeSecretDescription = `Write a secret the user stored in Shelley to a file, without you seeing it.
The user's messages show a secret as {{secret:ID}}; its value is kept out of your context.
The file gets mode 0600 (new directories 0700) and holds exactly the value, with no trailing newline; an existing file is replaced.
Use the file without printing it, e.g. TOKEN=$(cat path) some-command, or a config file the program reads.
Tool output that contains a stored secret's value shows {{secret:ID}} instead.
`
	writeSecretInputSchema = `{
  "type": "object",
  "required": ["id", "path"],
  "properties": {
    "id": {"type": "string", "description": "The secret's ID: openai-key-k3f9 for {{secret:openai-key-k3f9}}"},
    "path": {"type": "string", "description": "The file to write, absolute or relative to the working directory"}
  }
}`
)

// WriteSecretDisplay is the UI's record of a write_secret call.
type WriteSecretDisplay struct {
	ID      string `json:"id"`
	Machine string `json:"machine"`
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
}

type writeSecretInput struct {
	ID        string  `json:"id"`
	Path      string  `json:"path"`
	SSHTarget *string `json:"ssh_target,omitempty"`
}

// WriteSecretTool is the write_secret tool.
type WriteSecretTool struct {
	Secrets        Secrets
	ConversationID string
	WorkingDir     *MutableWorkingDir
	VM             VM
	HostDir        *MutableWorkingDir
}

func (w *WriteSecretTool) Tool() *llm.Tool {
	return &llm.Tool{
		Name:        WriteSecretName,
		Description: writeSecretDescription,
		InputSchema: llm.MustSchema(targetSchema(writeSecretInputSchema, w.VM)),
		Run:         llm.RunJSON(w.run),
	}
}

// vmWriteSecretScript writes stdin to $1, mode 0600 even if it existed.
const vmWriteSecretScript = `umask 077
mkdir -p -- "$(dirname -- "$1")" && : >"$1" && chmod 600 "$1" && cat >"$1"`

func (w *WriteSecretTool) run(ctx context.Context, in writeSecretInput) llm.ToolOut {
	if !SecretIDPattern.MatchString(in.ID) {
		return llm.ErrorfToolOut("id %q: pass the ID alone, e.g. openai-key-k3f9 for {{secret:openai-key-k3f9}}", in.ID)
	}
	vm, err := targetVM(w.VM, in.SSHTarget)
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	value, err := w.Secrets.Value(ctx, in.ID)
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	dir, machine := w.WorkingDir.Get(), Localhost
	if vm != nil {
		machine = vm.Name()
		if err := vm.Prepare(ctx); err != nil {
			return llm.ErrorToolOut(err)
		}
	} else if w.HostDir != nil {
		dir = w.HostDir.Get()
	}
	path := in.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path = filepath.Clean(path)
	if vm != nil {
		_, err = vmFS{vm}.run(ctx, "open", path, []byte(value), vmWriteSecretScript)
	} else {
		err = writeSecretFile(path, value)
	}
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	if err := w.Secrets.RecordInjection(ctx, in.ID, w.ConversationID, machine, path); err != nil {
		return llm.ErrorToolOut(err)
	}
	where := path
	if w.VM != nil {
		where = machine + ":" + path
	}
	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("Wrote %s (%d bytes) to %s, mode 0600.", SecretPlaceholder(in.ID), len(value), where)),
		Display:    WriteSecretDisplay{ID: in.ID, Machine: machine, Path: path, Bytes: len(value)},
	}
}

func writeSecretFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// An existing file keeps its mode on open; tighten it before writing.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(value); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
