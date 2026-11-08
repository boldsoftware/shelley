package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GitMovedBlock is a block of lines git's --color-moved=zebra sees as
// moved, and where its other copy is: for a block on the old side, where it
// went; on the new side, where it came from. Without OtherPath, where is
// unknown.
type GitMovedBlock struct {
	Line      int    `json:"line"`
	Count     int    `json:"count"`
	OtherPath string `json:"otherPath,omitempty"`
	OtherLine int    `json:"otherLine,omitempty"`
}

// Limits on git's work for /api/git/moved, generous so they only stop
// the pathological. Without moved lines, the app shows none.
const (
	movedTimeout   = 10 * time.Second
	movedMaxOutput = 64 << 20
)

// outputBounded is cmd's standard output, unless it runs past ctx or
// writes more than limit bytes, when it is killed.
func outputBounded(ctx context.Context, cmd *exec.Cmd, limit int64) ([]byte, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	kill := func() { cmd.Process.Kill() }
	defer context.AfterFunc(ctx, kill)()
	out, err := io.ReadAll(io.LimitReader(stdout, limit+1))
	if int64(len(out)) > limit {
		kill()
		err = errors.New("output too large")
	}
	if werr := cmd.Wait(); err == nil {
		err = werr
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// movedMatchBudget bounds the line comparisons spent finding blocks'
// other copies; blocks past it go without.
const movedMatchBudget = 1 << 22

// GitMovedFile is one file's moved blocks, by line number in its old and
// new versions.
type GitMovedFile struct {
	Path string          `json:"path"`
	Old  []GitMovedBlock `json:"old,omitempty"`
	New  []GitMovedBlock `json:"new,omitempty"`
}

// movedColors pins git's moved-line colors to codes of their own, so the
// output says how git marked each line whatever the user's config. Git
// alternates the two between adjacent blocks.
var movedColors = map[string]struct {
	n   int
	alt bool
}{
	"oldMoved": {19, false}, "newMoved": {20, false},
	"oldMovedAlternative": {21, true}, "newMovedAlternative": {22, true},
}

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// handleGitMoved returns the blocks git sees as moved across the whole diff
// (between files too), for the same diff and `to` as /api/git/diffs/{id}/files.
func (s *Server) handleGitMoved(w http.ResponseWriter, r *http.Request) {
	m, err := s.requestMachine(r)
	if err != nil {
		machineError(w, err)
		return
	}
	q := r.URL.Query()
	diffID, toRef := q.Get("diff"), q.Get("to")
	if !safeRef(diffID) || (toRef != "" && !safeRef(toRef)) {
		http.Error(w, "invalid diff or to", http.StatusBadRequest)
		return
	}
	gitRoot, err := m.gitRoot(m.dir(q.Get("cwd")))
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}
	// The lines must be the files' own, numbered as the app numbers them,
	// and matched as plain git does, whatever the repo's config says. Only
	// moved lines are colored, and blank context lines keep their space.
	args := []string{"-c", "diff.suppressBlankEmpty=false"}
	for _, name := range []string{
		"plain", "context", "meta", "frag", "func", "old", "new", "commit", "whitespace",
		"oldMovedDimmed", "newMovedDimmed", "oldMovedAlternativeDimmed", "newMovedAlternativeDimmed",
		"contextDimmed", "oldDimmed", "newDimmed", "contextBold", "oldBold", "newBold",
	} {
		args = append(args, "-c", "color.diff."+name+"=normal")
	}
	for name, c := range movedColors {
		args = append(args, "-c", fmt.Sprintf("color.diff.%s=%d", name, c.n))
	}
	args = append(args, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative",
		"--submodule=short", "--src-prefix=a/", "--dst-prefix=b/", "--color=always",
		"--color-moved=zebra", "--color-moved-ws=no", "--ws-error-highlight=none")
	switch {
	case diffID == "working":
		args = append(args, "HEAD")
	case toRef == "" || toRef == "working":
		args = append(args, m.parentRef(gitRoot, diffID))
	case toRef == "self":
		args = append(args, m.parentRef(gitRoot, diffID), diffID)
	default:
		args = append(args, m.parentRef(gitRoot, diffID), toRef)
	}
	ctx, cancel := context.WithTimeout(r.Context(), movedTimeout)
	defer cancel()
	m.ctx = ctx
	out, err := outputBounded(ctx, m.git(gitRoot, args...), movedMaxOutput)
	if err != nil {
		http.Error(w, "git diff failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, parseMovedDiff(out))
}

// movedLine is a moved line: where it is, its text, and its zebra stripe.
type movedLine struct {
	file *GitMovedFile
	line int
	text string
	alt  bool
}

// parseMovedDiff reads `git diff --color-moved=zebra` output colored with
// movedColors.
func parseMovedDiff(out []byte) []GitMovedFile {
	alts := map[string]bool{}
	for _, c := range movedColors {
		alts[fmt.Sprintf("\x1b[38;5;%dm", c.n)] = c.alt
	}
	var files []*GitMovedFile
	var file *GitMovedFile
	var olds, news []movedLine
	var oldLine, newLine, oldLeft, newLeft int
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		raw := sc.Text()
		line := ansiRE.ReplaceAllString(raw, "")
		if oldLeft > 0 || newLeft > 0 {
			// A moved line starts with its color.
			alt, moved := alts[ansiRE.FindString(raw)]
			moved = moved && strings.HasPrefix(raw, "\x1b")
			switch {
			case strings.HasPrefix(line, "-"):
				if moved {
					olds = append(olds, movedLine{file, oldLine, line[1:], alt})
				}
				oldLine++
				oldLeft--
			case strings.HasPrefix(line, "+"):
				if moved {
					news = append(news, movedLine{file, newLine, line[1:], alt})
				}
				newLine++
				newLeft--
			case strings.HasPrefix(line, " "):
				oldLine++
				newLine++
				oldLeft--
				newLeft--
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = &GitMovedFile{}
			files = append(files, file)
		case file == nil:
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// The new path, or the old one for a deleted file.
			// Git ends a path with a space in it with a tab.
			if p := unquotePath(strings.TrimSuffix(line[4:], "\t")); p != "/dev/null" {
				file.Path = p[2:]
			}
		default:
			if h := hunkHeaderRE.FindStringSubmatch(line); h != nil {
				oldLine, oldLeft = hunkStart(h[1], h[2])
				newLine, newLeft = hunkStart(h[3], h[4])
			}
		}
	}
	budget := movedMatchBudget
	for _, b := range movedBlocks(olds, news, &budget) {
		b.file.Old = append(b.file.Old, b.GitMovedBlock)
	}
	for _, b := range movedBlocks(news, olds, &budget) {
		b.file.New = append(b.file.New, b.GitMovedBlock)
	}
	kept := []GitMovedFile{}
	for _, f := range files {
		if len(f.Old)+len(f.New) > 0 {
			kept = append(kept, *f)
		}
	}
	return kept
}

type fileBlock struct {
	file *GitMovedFile
	GitMovedBlock
}

// movedBlocks groups one side's moved lines into git's blocks (runs of
// consecutive lines in one stripe), each with the first run of the other
// side's moved lines that it matches in full, if *budget comparisons
// find one.
func movedBlocks(lines, others []movedLine, budget *int) []fileBlock {
	at := map[string][]int{}
	for i, o := range others {
		at[o.text] = append(at[o.text], i)
	}
	var blocks []fileBlock
	for start := 0; start < len(lines); {
		end := start + 1
		for end < len(lines) && lines[end].file == lines[start].file &&
			lines[end].line == lines[end-1].line+1 && lines[end].alt == lines[start].alt {
			end++
		}
		b := fileBlock{lines[start].file, GitMovedBlock{Line: lines[start].line, Count: end - start}}
		for _, i := range at[lines[start].text] {
			k := 0
			for ; start+k < end && *budget > 0 && i+k < len(others) && others[i+k].file == others[i].file &&
				others[i+k].line == others[i].line+k && others[i+k].text == lines[start+k].text; k++ {
				*budget--
			}
			if start+k == end {
				b.OtherPath, b.OtherLine = others[i].file.Path, others[i].line
				break
			}
			if *budget--; *budget <= 0 {
				break
			}
		}
		blocks = append(blocks, b)
		start = end
	}
	return blocks
}

// hunkStart is a hunk side's first line and line count ("-0,0" for none).
func hunkStart(start, count string) (int, int) {
	s, _ := strconv.Atoi(start)
	n := 1
	if count != "" {
		n, _ = strconv.Atoi(count)
	}
	return s, n
}

// unquotePath undoes git's quoting of paths with unusual characters
// ("a/x" stays as it is).
func unquotePath(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}
