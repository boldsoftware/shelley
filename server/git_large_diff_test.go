package server

import (
	"bytes"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitDiffLimitReasonBoundaries(t *testing.T) {
	mib := bytes.Repeat([]byte(strings.Repeat("x", 255)+"\n"), 4096)
	for _, tc := range []struct {
		name       string
		old, new   []byte
		wantReason string
	}{
		{"empty", nil, nil, ""},
		{"combined byte limit", mib, mib, ""},
		{"combined byte overflow", mib, append(bytes.Clone(mib), 'x'), "Combined file size"},
		{"line count limit", bytes.Repeat([]byte("x\n"), 19999), nil, ""},
		{"line count overflow", nil, bytes.Repeat([]byte("x\n"), 20000), "20,000 lines"},
		{"line counts are per side", bytes.Repeat([]byte("x\n"), 15000), bytes.Repeat([]byte("y\n"), 15000), ""},
		{"line byte limit", []byte(strings.Repeat("x", 20000)), nil, ""},
		{"line byte overflow", nil, []byte(strings.Repeat("x", 20001)), "20,000 bytes"},
		{"newline resets line bytes", []byte(strings.Repeat("x", 20000) + "\n" + strings.Repeat("y", 20000)), nil, ""},
		{"UTF-8 byte limit", []byte(strings.Repeat("é", 10000)), nil, ""},
		{"UTF-8 byte overflow", nil, []byte(strings.Repeat("é", 10001)), "20,000 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := gitDiffLimitReason(tc.old, tc.new)
			if tc.wantReason == "" && got != "" || tc.wantReason != "" && !strings.Contains(got, tc.wantReason) {
				t.Fatalf("limit reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

func TestGitFileDiffLargeMetadataAndTextBypass(t *testing.T) {
	h := NewTestHarness(t)
	mib := bytes.Repeat([]byte(strings.Repeat("x", 255)+"\n"), 4096)
	binaryLarge := append([]byte{0}, bytes.Repeat([]byte("b"), 2<<20)...)
	// Text whose first 512 bytes, all that's read of it, end mid-rune.
	accentedLarge := append([]byte("x"), bytes.Repeat([]byte("é"), 1<<20+1)...)
	cases := []struct {
		path                 string
		old, new             []byte
		oldExists, newExists bool
		binary               bool
		reason               string
	}{
		{"boundary.txt", mib, mib, true, true, false, ""},
		{"combined.txt", mib, append(bytes.Clone(mib), 'y'), true, true, false, "Combined file size"},
		{"line.txt", []byte("old\n"), []byte(strings.Repeat("x", 20001)), true, true, false, "20,000 bytes"},
		{"lines.txt", bytes.Repeat([]byte("x\n"), 20000), []byte("new\n"), true, true, false, "20,000 lines"},
		{"binary.bin", binaryLarge, binaryLarge, true, true, true, "Combined file size"},
		{"added.txt", nil, append(bytes.Clone(mib), mib...), false, true, false, ""},
		{"deleted.txt", binaryLarge, nil, true, false, true, "Combined file size"},
		{"accented.txt", nil, accentedLarge, false, true, false, "Combined file size"},
		{"empty-added.txt", nil, nil, false, true, false, ""},
		{"empty-deleted.txt", nil, nil, true, false, false, ""},
	}
	initial := make(map[string][]byte)
	for _, tc := range cases {
		if tc.oldExists {
			initial[tc.path] = tc.old
		}
	}
	repo := binaryDiffRepo(t, initial)
	for _, tc := range cases {
		path := filepath.Join(repo, tc.path)
		if tc.newExists {
			if err := os.WriteFile(path, tc.new, 0o644); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			for _, query := range []string{"", "text=true"} {
				diff := requestBinaryDiff(t, h, repo, "working", tc.path, query)
				if diff.Path != tc.path || diff.OldExists != tc.oldExists || diff.NewExists != tc.newExists {
					t.Fatalf("%q: incorrect path/presence metadata: %+v", query, diff)
				}
				if diff.OldBytes != int64(len(tc.old)) || diff.NewBytes != int64(len(tc.new)) {
					t.Fatalf("%q: sizes = %d/%d, want %d/%d", query, diff.OldBytes, diff.NewBytes, len(tc.old), len(tc.new))
				}
				if diff.Binary != tc.binary || diff.TooLarge != (tc.reason != "") {
					t.Fatalf("%q: binary=%v tooLarge=%v, want %v/%v", query, diff.Binary, diff.TooLarge, tc.binary, tc.reason != "")
				}
				if tc.reason != "" {
					if !strings.Contains(diff.LimitReason, tc.reason) || diff.OldContent != "" || diff.NewContent != "" {
						t.Fatalf("%q: oversized response must contain a reason but no contents", query)
					}
				} else if diff.LimitReason != "" || diff.OldContent != string(tc.old) || diff.NewContent != string(tc.new) {
					t.Fatalf("%q: in-limit text content or reason differs", query)
				}
			}
		})
	}
	patch := requestGitPatch(t, h, repo, "working", "combined.txt", "")
	want := binaryDiffGit(t, repo, "diff", "--no-ext-diff", "--no-textconv", "--binary", "HEAD", "--", "combined.txt")
	if string(patch) != want || len(patch) == 0 {
		t.Fatal("oversized preview must still permit a complete patch download")
	}
}

func TestReadGitDiffSideBounded(t *testing.T) {
	data := []byte("0123456789")
	repo := binaryDiffRepo(t, map[string][]byte{"file.txt": data})
	blob, err := localMachine.gitDiffBlob(repo, "HEAD", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	working, err := localMachine.gitDiffWorkingFile(repo, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		side gitDiffSide
	}{
		{"blob", blob},
		{"working", working},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range []int64{4, 10, 11, -1} {
				got, err := localMachine.readGitDiffSide(repo, tc.side, limit)
				want := data
				if limit >= 0 && limit < int64(len(want)) {
					want = want[:limit]
				}
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("limit=%d: data=%q err=%v, want %q", limit, got, err, want)
				}
			}
		})
	}
	got, err := localMachine.readGitDiffSide(repo, gitDiffSide{}, 4)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing side: data=%q err=%v", got, err)
	}
}

func TestGitFileDiffPatchSelectedRanges(t *testing.T) {
	h := NewTestHarness(t)
	path := `nested/odd "name".txt`
	repo := binaryDiffRepo(t, map[string][]byte{path: []byte("first\n")})
	first := strings.TrimSpace(binaryDiffGit(t, repo, "rev-parse", "HEAD"))
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("second\n")
	second := binaryDiffCommit(t, repo, "second")
	write("third\n")
	third := binaryDiffCommit(t, repo, "third")
	write("working\n")
	for _, tc := range []struct {
		name, id, to string
		revs         []string
	}{
		{"working", "working", "", []string{third}},
		{"cumulative", second, "", []string{first}},
		{"explicit working", second, "working", []string{first}},
		{"self", second, "self", []string{first, second}},
		{"other commit", second, third, []string{first, third}},
		{"unchanged range", second, first, []string{first, first}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestGitPatch(t, h, repo, tc.id, path, tc.to)
			args := append([]string{"diff", "--no-ext-diff", "--no-textconv", "--binary"}, tc.revs...)
			args = append(args, "--", path)
			want := binaryDiffGit(t, repo, args...)
			if string(got) != want {
				t.Fatalf("patch differs for %s:\n%s\nwant:\n%s", tc.name, got, want)
			}
		})
	}
}

func TestGitFileDiffPatchFileKinds(t *testing.T) {
	h := NewTestHarness(t)
	oldName, newName := "old name.txt", "new name.txt"
	baseText := strings.Repeat("unchanged\n", 20)
	repo := binaryDiffRepo(t, map[string][]byte{
		oldName:       []byte(baseText + "old\n"),
		"deleted.txt": []byte("deleted\n"),
		"binary.bin":  {0, 1, 0xff},
	})
	first := strings.TrimSpace(binaryDiffGit(t, repo, "rev-parse", "HEAD"))
	binaryDiffGit(t, repo, "mv", oldName, newName)
	for path, content := range map[string][]byte{
		newName:      []byte(baseText + "new\n"),
		"added.txt":  []byte("added\n"),
		"binary.bin": {0, 2, 0xfe},
	} {
		if err := os.WriteFile(filepath.Join(repo, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	second := binaryDiffCommit(t, repo, "rename add delete binary")
	for _, tc := range []struct {
		name, id, path string
		revs, paths    []string
	}{
		{"rename", second, newName, []string{first, second}, []string{oldName, newName}},
		{"added", second, "added.txt", []string{first, second}, []string{"added.txt"}},
		{"deleted", second, "deleted.txt", []string{first, second}, []string{"deleted.txt"}},
		{"binary", second, "binary.bin", []string{first, second}, []string{"binary.bin"}},
		{"root", first, oldName, []string{emptyTreeHash, first}, []string{oldName}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestGitPatch(t, h, repo, tc.id, tc.path, "self")
			args := append([]string{"diff", "--no-ext-diff", "--no-textconv", "--binary"}, tc.revs...)
			args = append(args, "--")
			args = append(args, tc.paths...)
			want := binaryDiffGit(t, repo, args...)
			if string(got) != want || len(got) == 0 {
				t.Fatalf("patch differs for %s:\n%s\nwant:\n%s", tc.name, got, want)
			}
			if tc.name == "rename" && !bytes.Contains(got, []byte("rename from "+oldName)) {
				t.Fatal("rename patch lost its old path")
			}
			if tc.name == "binary" && !bytes.Contains(got, []byte("GIT binary patch")) {
				t.Fatal("binary patch is not applicable binary data")
			}
		})
	}

	t.Run("untracked", func(t *testing.T) {
		path := "untracked name.txt"
		content := []byte("untracked\n")
		if err := os.WriteFile(filepath.Join(repo, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
		patch := requestGitPatch(t, h, repo, "working", path, "")
		assertGitPatchRecreatesFile(t, repo, path, patch, content)
	})
}

func TestGitFileDiffBinaryMediaShapeGuard(t *testing.T) {
	h := NewTestHarness(t)
	wav := make([]byte, 32<<10)
	copy(wav, "RIFF")
	copy(wav[8:], "WAVEfmt ")
	giant := make([]byte, (2<<20)+1)
	copy(giant, wav)
	repo := binaryDiffRepo(t, map[string][]byte{"sound.wav": wav, "giant.wav": giant})

	diff := requestBinaryDiff(t, h, repo, "working", "sound.wav", "")
	if !diff.Binary || !diff.OldAudio || !diff.NewAudio || diff.TooLarge || diff.LimitReason != "" ||
		diff.OldContent != "" || diff.NewContent != "" || diff.OldBytes != int64(len(wav)) || diff.NewBytes != int64(len(wav)) {
		t.Fatalf("ordinary binary media must keep its native preview: %+v", diff)
	}
	diff = requestBinaryDiff(t, h, repo, "working", "sound.wav", "text=true")
	if !diff.Binary || !diff.OldAudio || !diff.NewAudio || !diff.TooLarge ||
		!strings.Contains(diff.LimitReason, "20,000 bytes") || diff.OldContent != "" || diff.NewContent != "" {
		t.Fatalf("explicit binary text preview must respect line-shape limits: %+v", diff)
	}
	diff = requestBinaryDiff(t, h, repo, "working", "giant.wav", "")
	if !diff.Binary || !diff.TooLarge || !strings.Contains(diff.LimitReason, "Combined file size") ||
		diff.OldContent != "" || diff.NewContent != "" || diff.OldBytes != int64(len(giant)) || diff.NewBytes != int64(len(giant)) {
		t.Fatalf("binary native preview must still respect the byte limit: %+v", diff)
	}
}

func TestGitFileDiffPatchUntrackedDash(t *testing.T) {
	h := NewTestHarness(t)
	repo := binaryDiffRepo(t, map[string][]byte{"tracked.txt": []byte("tracked\n")})
	content := []byte("literal dash file, not stdin\n")
	if err := os.WriteFile(filepath.Join(repo, "-"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	patch := requestGitPatch(t, h, repo, "working", "-", "")
	assertGitPatchRecreatesFile(t, repo, "-", patch, content)
}

func TestGitFileDiffPatchUntrackedReplacesDirectory(t *testing.T) {
	h := NewTestHarness(t)
	repo := binaryDiffRepo(t, map[string][]byte{"a/child": []byte("tracked child\n")})
	for _, path := range []string{"a/child", "a"} {
		if err := os.Remove(filepath.Join(repo, path)); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("untracked replacement file\n")
	if err := os.WriteFile(filepath.Join(repo, "a"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	patch := requestGitPatch(t, h, repo, "working", "a", "")
	if bytes.Count(patch, []byte("diff --git ")) != 1 || !bytes.Contains(patch, []byte("new file mode")) {
		t.Fatalf("untracked replacement must download a file addition, not an empty or recursive patch:\n%s", patch)
	}
	assertGitPatchRecreatesFile(t, repo, "a", patch, content)
}

func assertGitPatchRecreatesFile(t *testing.T, repo, path string, patch, content []byte) {
	t.Helper()
	patchPath := filepath.Join(t.TempDir(), "change.patch")
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, path)); err != nil {
		t.Fatal(err)
	}
	binaryDiffGit(t, repo, "apply", patchPath)
	got, err := os.ReadFile(filepath.Join(repo, path))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("applied patch for %q: contents=%q err=%v", path, got, err)
	}
}

func TestGitFileDiffPatchIgnoresDisplayConfig(t *testing.T) {
	h := NewTestHarness(t)
	repo := binaryDiffRepo(t, map[string][]byte{"file.txt": []byte("before\n")})
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := binaryDiffGit(t, repo, "diff", "--no-color", "--binary", "HEAD", "--", "file.txt")
	binaryDiffGit(t, repo, "config", "color.ui", "always")
	binaryDiffGit(t, repo, "config", "diff.noprefix", "true")
	patch := requestGitPatch(t, h, repo, "working", "file.txt", "")
	if bytes.Contains(patch, []byte("\x1b[")) || string(patch) != want {
		t.Fatalf("display config changed downloadable patch:\n%q\nwant:\n%s", patch, want)
	}
}

func TestGitFileDiffFileDirectoryTransitions(t *testing.T) {
	h := NewTestHarness(t)
	repo := binaryDiffRepo(t, map[string][]byte{
		"file-to-dir":           []byte("old file\n"),
		"dir-to-file/child.txt": []byte("old child\n"),
	})
	for _, path := range []string{"file-to-dir", "dir-to-file/child.txt", "dir-to-file"} {
		if err := os.Remove(filepath.Join(repo, path)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(repo, "file-to-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"file-to-dir/child.txt": "new child\n",
		"dir-to-file":           "new file\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second := binaryDiffCommit(t, repo, "file directory transitions")
	for _, tc := range []struct {
		path, old, new string
	}{
		{"file-to-dir", "old file\n", ""},
		{"file-to-dir/child.txt", "", "new child\n"},
		{"dir-to-file", "", "new file\n"},
		{"dir-to-file/child.txt", "old child\n", ""},
	} {
		for _, to := range []string{"self", "working"} {
			t.Run(tc.path+"/"+to, func(t *testing.T) {
				diff := requestBinaryDiff(t, h, repo, second, tc.path, "to="+to)
				if diff.OldExists != (tc.old != "") || diff.NewExists != (tc.new != "") ||
					diff.OldContent != tc.old || diff.NewContent != tc.new ||
					diff.OldBytes != int64(len(tc.old)) || diff.NewBytes != int64(len(tc.new)) {
					t.Fatalf("file/directory transition metadata or contents differ: %+v", diff)
				}
				patch := requestGitPatch(t, h, repo, second, tc.path, to)
				if bytes.Count(patch, []byte("diff --git ")) != 1 {
					t.Fatalf("per-file patch includes unrelated directory children:\n%s", patch)
				}
				mode := "new file mode"
				if tc.new == "" {
					mode = "deleted file mode"
				}
				if !bytes.Contains(patch, []byte(mode)) {
					t.Fatalf("patch does not describe the selected file transition:\n%s", patch)
				}
			})
		}
	}
}

func requestGitPatch(t *testing.T, h *TestHarness, repo, id, path, to string) []byte {
	t.Helper()
	query := "download=patch"
	if to != "" {
		query += "&to=" + url.QueryEscape(to)
	}
	w := httptest.NewRecorder()
	h.server.handleGitFileDiff(w, binaryDiffRequest(repo, id, path, query))
	if w.Code != http.StatusOK {
		t.Fatalf("patch download status=%d: %s", w.Code, w.Body.String())
	}
	disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != filepath.Base(path)+".patch" {
		t.Fatalf("patch content disposition=%q err=%v", w.Header().Get("Content-Disposition"), err)
	}
	if w.Header().Get("Content-Type") != "text/x-patch" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("patch headers=%v", w.Header())
	}
	return bytes.Clone(w.Body.Bytes())
}
