package claudetool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

const readCodeName = "read_code"

var readCodeResultsDir = sync.OnceValues(func() (string, error) {
	return os.MkdirTemp("", "shelley-read-code-results-")
})

type readCodeSearchRequest struct {
	Query   string `json:"query"`
	Regex   bool   `json:"regex"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
	Context int    `json:"context"`
}
type readCodeReadRequest struct {
	Path string `json:"path"`
	From int    `json:"from"`
	To   int    `json:"to"`
}
type readCodeInput struct {
	Searches []readCodeSearchRequest `json:"searches"`
	Reads    []readCodeReadRequest   `json:"reads"`
}

func (in *readCodeInput) UnmarshalJSON(data []byte) error {
	type wire readCodeInput
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode((*wire)(in))
}

func readCodeTool(wd *MutableWorkingDir) *llm.Tool {
	return &llm.Tool{
		Name:        readCodeName,
		Description: "Put text searches in searches and numbered line ranges in reads. Supply an empty array for an unused group. Search is literal and case-sensitive unless regex is true; empty search path means current directory. Read ranges are 1-indexed and inclusive. Searches run first. Paths are relative, except reads can use the saved result paths returned by this tool. Each request has an 8KB inline preview; oversized results are saved to a file (up to 1MB). Narrow oversized searches or read selected ranges from their saved results.",
		InputSchema: llm.MustSchema(`{
   "type":"object","additionalProperties":false,"required":["searches","reads"],"properties":{
    "searches":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["query"],"properties":{
     "query":{"type":"string","minLength":1,"maxLength":512},"regex":{"type":"boolean"},"path":{"type":"string"},"glob":{"type":"string"},"context":{"type":"integer","minimum":0,"maximum":12}}}},
    "reads":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["path","from","to"],"properties":{
     "path":{"type":"string"},"from":{"type":"integer","minimum":1},"to":{"type":"integer","minimum":1}}}}
   }
  }`),
		Run: llm.RunJSON(func(ctx context.Context, in readCodeInput) llm.ToolOut {
			count := len(in.Searches) + len(in.Reads)
			if count < 1 || count > 8 {
				return llm.ErrorfToolOut("expected 1–8 searches and reads combined")
			}
			dir := wd.Get()
			root, err := os.OpenRoot(dir)
			if err != nil {
				return llm.ErrorToolOut(err)
			}
			defer root.Close()
			var output strings.Builder
			var runErr error
			run := func(kind, title string, get func(func(string) bool) (bool, error)) {
				budget := 8000
				if output.Len() > 0 {
					output.WriteByte('\n')
				}
				fmt.Fprintln(&output, title)
				var result strings.Builder
				preview := 0
				n := 0
				truncated, err := get(func(line string) bool {
					if result.Len()+len(line)+1 > 1<<20 {
						return false
					}
					if result.Len() == preview && len(line)+1 <= budget {
						budget -= len(line) + 1
						preview += len(line) + 1
					}
					fmt.Fprintln(&result, line)
					n++
					return true
				})
				output.WriteString(result.String()[:preview])
				if result.Len() > preview {
					path, saveErr := saveReadCodeResult(result.String())
					if saveErr != nil {
						fmt.Fprintf(&output, "Error saving oversized result: %q\n", saveErr)
						runErr = errors.Join(runErr, err, saveErr)
						return
					}
					fmt.Fprintf(&output, "[%s too large for inline output. Saved result: %s]\n", kind, path)
					fmt.Fprintln(&output, "Narrow the request or use read_code reads on selected line ranges from this file.")
				}
				switch {
				case err != nil:
					fmt.Fprintf(&output, "Error: %q\n", err.Error())
					runErr = errors.Join(runErr, fmt.Errorf("%s: %w", title, err))
				case truncated:
					fmt.Fprintln(&output, "[Result incomplete: 1MB output limit reached or source lines clipped]")
				case n == 0:
					fmt.Fprintln(&output, "[No matches]")
				}
			}
			for _, req := range in.Searches {
				if req.Path == "" {
					req.Path = "."
				}
				run("Search", fmt.Sprintf("Search %q in %q", req.Query, req.Path), func(add func(string) bool) (bool, error) {
					return readCodeSearch(ctx, root, dir, req, add)
				})
			}
			for _, req := range in.Reads {
				run("Read", fmt.Sprintf("Read %q lines %d–%d", req.Path, req.From, req.To), func(add func(string) bool) (bool, error) {
					return readCodeRead(root, req, add)
				})
			}
			if runErr != nil {
				return llm.ErrorToolOut(fmt.Errorf("%w\n\n%s", runErr, output.String()))
			}
			return llm.ToolOut{LLMContent: llm.TextContent(output.String())}
		}),
	}
}

func saveReadCodeResult(text string) (string, error) {
	dir, err := readCodeResultsDir()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "result-*.txt")
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(text)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return f.Name(), nil
}

func readCodeSearch(ctx context.Context, root *os.Root, dir string, req readCodeSearchRequest, add func(string) bool) (bool, error) {
	if !filepath.IsLocal(req.Path) {
		return false, fmt.Errorf("path must be relative to the working directory")
	}
	if req.Query == "" || req.Context < 0 || req.Context > 12 || strings.ContainsAny(req.Query, "\r\n") || len(req.Query) > 512 {
		return false, fmt.Errorf("invalid search bounds or query")
	}
	info, err := root.Stat(req.Path)
	if err != nil {
		return false, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return false, fmt.Errorf("search path is not a directory or regular file")
	}
	args := []string{"--no-config", "--color=never", "--line-number", "--with-filename", "--no-heading", "--null", "--sort=path", "--max-columns=600", fmt.Sprintf("--context=%d", req.Context)}
	if !req.Regex {
		args = append(args, "--fixed-strings")
	}
	if req.Glob != "" {
		args = append(args, "--glob", req.Glob)
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rg", append(args, "--", req.Query, "./"+filepath.ToSlash(req.Path))...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err = cmd.Start(); err != nil {
		return false, err
	}
	scan := bufio.NewScanner(pipe)
	truncated := false
	stopped := false
	var file string
	last := 0
	for scan.Scan() {
		path, rest, ok := strings.Cut(scan.Text(), "\x00")
		if !ok && path == "--" {
			continue
		}
		split := strings.IndexAny(rest, ":-")
		if !ok || split < 1 {
			cancel()
			_ = cmd.Wait()
			return false, fmt.Errorf("unexpected ripgrep output")
		}
		line, err := strconv.Atoi(rest[:split])
		if err != nil {
			cancel()
			_ = cmd.Wait()
			return false, fmt.Errorf("invalid ripgrep line: %w", err)
		}
		text := rest[split+1:]
		marker := " "
		if rest[split] == ':' {
			marker = "›"
		}
		row := fmt.Sprintf("%s %d │ %s", marker, line, text)
		switch {
		case file != path:
			row = fmt.Sprintf("File: %s\n%s", strings.TrimPrefix(path, "./"), row)
		case line > last+1:
			row = "  …\n" + row
		}
		file, last = path, line
		if strings.Contains(text, "[Omitted long ") {
			truncated = true
		}
		if !add(row) {
			truncated = true
			stopped = true
			cancel()
			break
		}
	}
	scanErr := scan.Err()
	waitErr := cmd.Wait()
	if parentCtx.Err() != nil {
		return false, parentCtx.Err()
	}
	if stopped && ctx.Err() == context.Canceled {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if scanErr != nil {
		return false, scanErr
	}
	if e, ok := waitErr.(*exec.ExitError); ok && e.ExitCode() == 1 {
		return truncated, nil
	}
	if waitErr != nil {
		return false, fmt.Errorf("rg: %w: %.400s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return truncated, nil
}

func readCodeRead(root *os.Root, req readCodeReadRequest, add func(string) bool) (bool, error) {
	if filepath.IsAbs(req.Path) {
		dir, err := readCodeResultsDir()
		if err != nil {
			return false, err
		}
		if filepath.Dir(req.Path) != dir {
			return false, fmt.Errorf("absolute paths must be saved read_code results")
		}
		resultRoot, err := os.OpenRoot(dir)
		if err != nil {
			return false, err
		}
		defer resultRoot.Close()
		root = resultRoot
		req.Path = filepath.Base(req.Path)
	}
	if !filepath.IsLocal(req.Path) {
		return false, fmt.Errorf("path must be relative to the working directory or a saved read_code result")
	}
	if req.From < 1 || req.To < req.From || req.To-req.From >= 300 || req.To > 1000000 {
		return false, fmt.Errorf("expected 1 <= from <= to <= 1000000, at most 300 lines")
	}
	info, err := root.Stat(req.Path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("read path is not a regular file")
	}
	f, err := root.Open(req.Path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	truncated := false
	line := 0
	for line < req.To && scan.Scan() {
		line++
		if line < req.From {
			continue
		}
		text := scan.Text()
		if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
			return false, fmt.Errorf("source is not UTF-8 text")
		}
		if len(text) > 1200 {
			text = strings.ToValidUTF8(text[:1200], "") + " … [line clipped]"
			truncated = true
		}
		row := fmt.Sprintf("  %d │ %s", line, text)
		if line == req.From {
			row = fmt.Sprintf("File: %s\n%s", req.Path, row)
		}
		if !add(row) {
			return true, nil
		}
	}
	if err := scan.Err(); err != nil {
		return false, err
	}
	if line < req.From {
		return false, fmt.Errorf("from line %d is past EOF (%d lines)", req.From, line)
	}
	return truncated, nil
}
