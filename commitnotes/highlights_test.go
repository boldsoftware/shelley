package commitnotes

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHighlightsScaffoldAndVerify(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "keep.txt", []byte(numbered(1, 10)))
	write(t, dir, "gone.txt", []byte(numbered(1, 5)))
	write(t, dir, "old/name.txt", []byte(numbered(1, 20)))
	root := commit(t, git, "root")

	root1, err := HighlightsScaffold(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gone.txt", "keep.txt", "old/name.txt"}; !slices.Equal(root1.Files, want) {
		t.Fatalf("root scaffold files = %q, want %q", root1.Files, want)
	}

	write(t, dir, "keep.txt", []byte(numbered(1, 12)))
	git("rm", "-q", "gone.txt")
	if err := os.Rename(filepath.Join(dir, "old"), filepath.Join(dir, "new")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "added.txt", []byte("no trailing newline"))
	hash := commit(t, git, "change")
	// A hostile config must not change which paths are listed.
	git("config", "diff.renames", "false")
	git("config", "diff.relative", "true")
	git("config", "diff.orderFile", "/does/not/exist")

	scaffold, err := HighlightsScaffold(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"added.txt", "gone.txt", "keep.txt", "new/name.txt"}; !slices.Equal(scaffold.Files, want) {
		t.Fatalf("scaffold files = %q, want %q", scaffold.Files, want)
	}
	if err := VerifyHighlights(dir, hash, scaffold); err != nil {
		t.Fatalf("scaffold does not verify: %v", err)
	}

	valid := func() *Highlights {
		return &Highlights{
			Version: 1,
			Files:   []string{"keep.txt", "new/name.txt", "gone.txt", "added.txt"},
			Highlights: []Highlight{
				{File: "keep.txt", Line: 12, Comment: "last line of the new version"},
				{File: "new/name.txt", Line: 1, Comment: "renamed files use the new path"},
				{File: "added.txt", Line: 1, Comment: "an unterminated last line counts"},
				{File: "keep.txt", Line: 1, Comment: "unchanged context is fine"},
			},
		}
	}
	if err := VerifyHighlights(dir, hash, valid()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Highlights)
		want   string
	}{
		{"version", func(h *Highlights) { h.Version = 2 }, "version is 2"},
		{"missing file", func(h *Highlights) { h.Files = h.Files[1:] }, `files is missing "keep.txt"`},
		{"unchanged file", func(h *Highlights) { h.Files = append(h.Files, "old/name.txt") }, `files[4] "old/name.txt" is not changed`},
		{"duplicate file", func(h *Highlights) { h.Files = append(h.Files, "keep.txt") }, `files[4] "keep.txt" is listed twice`},
		{"unknown highlight file", func(h *Highlights) { h.Highlights[0].File = "nope.txt" }, `highlights[0] file "nope.txt"`},
		{"line zero", func(h *Highlights) { h.Highlights[0].Line = 0 }, "line 0 is outside keep.txt (1..12)"},
		{"line past end", func(h *Highlights) { h.Highlights[0].Line = 13 }, "line 13 is outside keep.txt (1..12)"},
		{"deleted file", func(h *Highlights) { h.Highlights[1].File = "gone.txt" }, "gone.txt is deleted by the commit"},
		{"renamed file's old path", func(h *Highlights) { h.Highlights[1].File = "old/name.txt" }, `highlights[1] file "old/name.txt" is not changed`},
		{"line past unterminated end", func(h *Highlights) { h.Highlights[2].Line = 2 }, "line 2 is outside added.txt (1..1)"},
		{"empty comment", func(h *Highlights) { h.Highlights[0].Comment = " " }, "empty comment"},
		{"multiline comment", func(h *Highlights) { h.Highlights[0].Comment = "a\nb" }, "spans lines"},
		{"long comment", func(h *Highlights) { h.Highlights[0].Comment = strings.Repeat("é", MaxHighlightComment+1) }, "comment is 201 characters, max 200"},
		{"duplicate spot", func(h *Highlights) { h.Highlights[3].Line = 12 }, "repeats keep.txt:12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := valid()
			tc.mutate(h)
			err := VerifyHighlights(dir, hash, h)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("VerifyHighlights error = %v, want %q", err, tc.want)
			}
		})
	}
	long := valid()
	long.Highlights[0].Comment = strings.Repeat("é", MaxHighlightComment)
	if err := VerifyHighlights(dir, hash, long); err != nil {
		t.Fatalf("comment at the limit: %v", err)
	}
}

func TestHighlightsMergeUsesFirstParent(t *testing.T) {
	dir, git := gitRepo(t)
	write(t, dir, "base.txt", []byte("base\n"))
	commit(t, git, "base")
	git("checkout", "-q", "-b", "side")
	write(t, dir, "side.txt", []byte("side\n"))
	commit(t, git, "side")
	git("checkout", "-q", "main")
	write(t, dir, "main.txt", []byte("main\n"))
	commit(t, git, "main")
	git("merge", "-q", "--no-ff", "-m", "merge", "side")
	h, err := HighlightsScaffold(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"side.txt"}; !slices.Equal(h.Files, want) {
		t.Fatalf("merge files = %q, want %q", h.Files, want)
	}
}

func TestParseHighlights(t *testing.T) {
	h, err := ParseHighlights([]byte(`{"version":1,"files":["a"],"highlights":[{"file":"a","line":3,"comment":"c"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Highlights) != 1 || h.Highlights[0] != (Highlight{File: "a", Line: 3, Comment: "c"}) {
		t.Fatalf("parsed %+v", h)
	}
	empty, err := ParseHighlights([]byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Files == nil || empty.Highlights == nil {
		t.Fatalf("omitted lists parse as nil: %+v", empty)
	}
	for _, bad := range []string{
		`null`,
		`[]`,
		`{"version":1,"files":[],"highlights":[{"file":"a","line":1,"text":"c"}]}`,
		`{"version":1,"files":[],"highlights":[]} trailing`,
		`{"version":1,"files":[],"highlights":[]}{}`,
	} {
		if _, err := ParseHighlights([]byte(bad)); err == nil {
			t.Errorf("ParseHighlights(%s) succeeded", bad)
		}
	}
}
