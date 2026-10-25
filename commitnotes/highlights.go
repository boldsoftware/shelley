package commitnotes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Highlights is a lightweight reviewer's guide to a commit, stored as a git
// note: the changed files ordered most important first, and short comments
// pinned to the lines worth a reviewer's attention.
type Highlights struct {
	Version    int         `json:"version"`
	Files      []string    `json:"files"`
	Highlights []Highlight `json:"highlights"`
}

// Highlight is a short plain-text comment on one line of a changed file.
// Line numbers count from 1 in the file as of the commit, so deleted files
// have none.
type Highlight struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Comment string `json:"comment"`
}

// MaxHighlightComment is the longest comment, in characters, a highlight
// may carry. Highlights render inline in the diff, so they must be short.
const MaxHighlightComment = 200

// ParseHighlights decodes a highlights note. Unknown fields are errors so a
// misspelled key fails loudly instead of silently dropping data.
func ParseHighlights(data []byte) (*Highlights, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var h *Highlights
	if err := dec.Decode(&h); err != nil {
		return nil, fmt.Errorf("parse highlights: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("parse highlights: unexpected data after the JSON object")
	}
	if h == nil {
		return nil, errors.New("parse highlights: expected JSON object")
	}
	// Omitted lists mean empty ones; readers then always get arrays.
	if h.Files == nil {
		h.Files = []string{}
	}
	if h.Highlights == nil {
		h.Highlights = []Highlight{}
	}
	return h, nil
}

// HighlightsScaffold returns highlights for commit listing every changed
// file in diff order and no comments, ready to be reordered and annotated.
func HighlightsScaffold(dir, commit string) (*Highlights, error) {
	hash, err := CommitHash(dir, commit)
	if err != nil {
		return nil, err
	}
	files, err := changedFiles(dir, hash)
	if err != nil {
		return nil, err
	}
	h := &Highlights{Version: 1, Files: []string{}, Highlights: []Highlight{}}
	for _, f := range files {
		h.Files = append(h.Files, f.path)
	}
	return h, nil
}

// VerifyHighlights checks h against commit: Files must list each changed file
// exactly once, and every highlight must name a file the commit changes but
// does not delete, and a line that exists in it.
func VerifyHighlights(dir, commit string, h *Highlights) error {
	if h == nil {
		return errors.New("highlights is nil")
	}
	if h.Version != 1 {
		return fmt.Errorf("version is %d, want 1", h.Version)
	}
	hash, err := CommitHash(dir, commit)
	if err != nil {
		return err
	}
	files, err := changedFiles(dir, hash)
	if err != nil {
		return err
	}
	changed := make(map[string]changedFile, len(files))
	for _, f := range files {
		changed[f.path] = f
	}
	listed := make(map[string]bool, len(h.Files))
	for i, path := range h.Files {
		if _, ok := changed[path]; !ok {
			return fmt.Errorf("files[%d] %q is not changed by the commit", i, path)
		}
		if listed[path] {
			return fmt.Errorf("files[%d] %q is listed twice", i, path)
		}
		listed[path] = true
	}
	for _, f := range files {
		if !listed[f.path] {
			return fmt.Errorf("files is missing %q", f.path)
		}
	}

	lineCounts := make(map[string]int)
	type spot struct {
		file string
		line int
	}
	seen := make(map[spot]bool, len(h.Highlights))
	for i, hl := range h.Highlights {
		f, ok := changed[hl.File]
		if !ok {
			return fmt.Errorf("highlights[%d] file %q is not changed by the commit", i, hl.File)
		}
		if f.deleted {
			return fmt.Errorf("highlights[%d]: %s is deleted by the commit; highlight a line that exists at the commit", i, hl.File)
		}
		if strings.TrimSpace(hl.Comment) == "" {
			return fmt.Errorf("highlights[%d] (%s:%d) has an empty comment", i, hl.File, hl.Line)
		}
		if strings.ContainsAny(hl.Comment, "\r\n") {
			return fmt.Errorf("highlights[%d] (%s:%d) comment spans lines", i, hl.File, hl.Line)
		}
		if n := utf8.RuneCountInString(hl.Comment); n > MaxHighlightComment {
			return fmt.Errorf("highlights[%d] (%s:%d) comment is %d characters, max %d", i, hl.File, hl.Line, n, MaxHighlightComment)
		}
		lines, ok := lineCounts[f.path]
		if !ok {
			data, err := gitOutput(dir, "cat-file", "blob", hash+":"+f.path)
			if err != nil {
				return fmt.Errorf("highlights[%d]: %w", i, err)
			}
			lines = bytes.Count(data, []byte("\n"))
			if len(data) > 0 && data[len(data)-1] != '\n' {
				lines++
			}
			lineCounts[f.path] = lines
		}
		if hl.Line < 1 || hl.Line > lines {
			return fmt.Errorf("highlights[%d] line %d is outside %s (1..%d)", i, hl.Line, hl.File, lines)
		}
		if seen[spot{hl.File, hl.Line}] {
			return fmt.Errorf("highlights[%d] repeats %s:%d; combine the comments", i, hl.File, hl.Line)
		}
		seen[spot{hl.File, hl.Line}] = true
	}
	return nil
}

// ReadHighlights returns commit's highlights note.
func ReadHighlights(dir, commit string) ([]byte, error) {
	return readNote(dir, highlightsNotesRef, commit)
}

// WriteHighlights stores data as commit's highlights note.
func WriteHighlights(dir, commit string, data []byte) error {
	return writeNote(dir, highlightsNotesRef, commit, data)
}

// ListHighlights returns the set of commits with highlights notes.
func ListHighlights(dir string) (map[string]bool, error) {
	return listNotes(dir, highlightsNotesRef)
}

type changedFile struct {
	path    string // new path; old path for deletions
	deleted bool
}

// changedFiles lists the files commit changes against its first parent (or
// the empty tree), in diff order, under the paths Shelley's diff viewer
// shows.
func changedFiles(dir, hash string) ([]changedFile, error) {
	out, err := gitOutput(
		dir,
		"-c", "log.showRoot=true",
		"show", hash,
		"--format=", "--no-color", "--no-show-signature", "--name-status", "-z",
		"--no-ext-diff", "--no-relative", "--find-renames",
		"--diff-merges=first-parent", "-O/dev/null",
	)
	if err != nil {
		return nil, err
	}
	// Records are NUL-terminated: a status, then one path, or source and
	// destination paths for renames and copies.
	fields := strings.Split(strings.TrimLeft(string(out), "\n"), "\x00")
	var files []changedFile
	for i := 0; i+1 < len(fields); {
		status := fields[i]
		switch {
		case status == "":
			i++
		case status[0] == 'R' || status[0] == 'C':
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("malformed name-status record %q", status)
			}
			files = append(files, changedFile{path: fields[i+2]})
			i += 3
		default:
			files = append(files, changedFile{path: fields[i+1], deleted: status == "D"})
			i += 2
		}
	}
	return files, nil
}
