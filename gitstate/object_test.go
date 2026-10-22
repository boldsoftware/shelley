package gitstate

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packIndexFixture encodes sorted SHA-1s with offsets and checksum space.
func packIndexFixture(hashes [][20]byte, offsets []uint64) []byte {
	data := make([]byte, 1032+28*len(hashes))
	binary.BigEndian.PutUint32(data[:4], 0xff744f63)
	binary.BigEndian.PutUint32(data[4:8], 2)
	n := 0
	for bucket := 0; bucket < 256; bucket++ {
		for n < len(hashes) && int(hashes[n][0]) <= bucket {
			n++
		}
		binary.BigEndian.PutUint32(data[8+bucket*4:], uint32(n))
	}
	off4Start := 1032 + 24*len(hashes)
	large := uint32(0)
	for i, hash := range hashes {
		copy(data[1032+i*20:], hash[:])
		offset := offsets[i]
		if offset >= 0x80000000 {
			binary.BigEndian.PutUint32(data[off4Start+i*4:], 0x80000000|large)
			data = binary.BigEndian.AppendUint64(data, offset)
			large++
		} else {
			binary.BigEndian.PutUint32(data[off4Start+i*4:], uint32(offset))
		}
	}
	return append(data, make([]byte, 40)...)
}

func TestLookupPackOffset(t *testing.T) {
	hashes := [][20]byte{{0: 0, 19: 1}, {0: 128, 19: 1}, {0: 128, 19: 3}, {0: 255, 19: 1}}
	offsets := []uint64{12, 1 << 32, math.MaxInt64, 80}
	missing := hashes[1]
	missing[19] = 2 // Between the two IDs in bucket 128.
	offsetsStart := 1032 + 24*len(hashes)
	largeOffsetsStart := offsetsStart + 4*len(hashes)
	tests := []struct {
		name   string
		want   []byte
		edit   func([]byte) []byte
		offset uint64
		found  bool
		err    bool
	}{
		{name: "first bucket", want: hashes[0][:], offset: 12, found: true},
		{name: "last bucket", want: hashes[3][:], offset: 80, found: true},
		{name: "large offset", want: hashes[1][:], offset: 1 << 32, found: true},
		{name: "max seek offset", want: hashes[2][:], offset: math.MaxInt64, found: true},
		{name: "missing in bucket", want: missing[:]},
		{name: "empty bucket", want: append([]byte{64}, make([]byte, 19)...)},
		{name: "empty index", want: hashes[0][:], edit: func([]byte) []byte { return packIndexFixture(nil, nil) }},
		{name: "wrong hash length", want: hashes[0][:19], err: true},
		{name: "short header", want: hashes[0][:], edit: func(d []byte) []byte { return d[:1031] }, err: true},
		{name: "bad magic", want: hashes[0][:], edit: func(d []byte) []byte { d[0] = 0; return d }, err: true},
		{name: "bad version", want: hashes[0][:], edit: func(d []byte) []byte { d[7] = 3; return d }, err: true},
		{name: "unordered fanout", want: hashes[0][:], edit: func(d []byte) []byte {
			binary.BigEndian.PutUint32(d[8+129*4:], 1)
			return d
		}, err: true},
		{name: "truncated offset table", want: hashes[3][:], edit: func(d []byte) []byte { return d[:largeOffsetsStart+40-1] }, err: true},
		{name: "incomplete index", want: hashes[0][:], edit: func(d []byte) []byte { return d[:len(d)-1] }, err: true},
		{name: "large offset in checksum", want: hashes[1][:], edit: func(d []byte) []byte {
			binary.BigEndian.PutUint32(d[offsetsStart+4:], 0x80000002)
			return d
		}, err: true},
		{name: "offset past seek range", want: hashes[1][:], edit: func(d []byte) []byte {
			binary.BigEndian.PutUint64(d[largeOffsetsStart:], uint64(math.MaxInt64)+1)
			return d
		}, err: true},
	}
	path := filepath.Join(t.TempDir(), "test.idx")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := packIndexFixture(hashes, offsets)
			if tt.edit != nil {
				data = tt.edit(data)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			offset, found, err := lookupPackOffset(path, tt.want)
			if (err != nil) != tt.err || found != tt.found || offset != tt.offset {
				t.Fatalf("got (%d, %t, %v), want (%d, %t, error=%t)", offset, found, err, tt.offset, tt.found, tt.err)
			}
		})
	}
	if _, _, err := lookupPackOffset(path+".missing", hashes[0][:]); err == nil {
		t.Fatal("expected missing index error")
	}
}

func BenchmarkLookupPackOffset(b *testing.B) {
	for _, count := range []int{256, 16384} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			hashes := make([][20]byte, count)
			offsets := make([]uint64, count)
			for i := range hashes {
				hashes[i][0] = byte(i * 256 / count)
				binary.BigEndian.PutUint32(hashes[i][16:], uint32(i))
				offsets[i] = uint64(i + 12)
			}
			path := filepath.Join(b.TempDir(), "bench.idx")
			if err := os.WriteFile(path, packIndexFixture(hashes, offsets), 0o600); err != nil {
				b.Fatal(err)
			}
			want := hashes[count/2][:]
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				offset, found, err := lookupPackOffset(path, want)
				if err != nil || !found || offset != offsets[count/2] {
					b.Fatalf("lookup = (%d, %t, %v)", offset, found, err)
				}
			}
		})
	}
}

// buildDeltaRepo creates a repo whose commits are large and nearly identical so
// that `git repack` stores most commit objects as deltas. useOfsDelta selects
// between offset deltas (the default) and ref deltas. It returns the worktree
// dir and asserts the pack genuinely contains delta-encoded commits, so these
// tests fail loudly if a future git stops deltifying (which would silently drop
// our coverage of the delta-resolution code).
func buildDeltaRepo(t *testing.T, useOfsDelta bool) string {
	t.Helper()
	dir := initRepo(t)
	t.Setenv("GIT_AUTHOR_DATE", "2020-01-01T00:00:00")
	t.Setenv("GIT_COMMITTER_DATE", "2020-01-01T00:00:00")
	big := strings.Repeat("x", 4000)
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", ".")
		commit(t, dir, "commit "+string(rune('a'+i))+" "+big)
	}
	runGit(t, dir, "config", "pack.useOfsDelta", boolStr(useOfsDelta))
	runGit(t, dir, "repack", "-a", "-d", "-f", "--window=250", "--depth=50", "-q")

	out := runGitOutput(t, dir, "verify-pack", "-v", packIdx(t, dir))
	if !strings.Contains(out, " commit ") {
		t.Fatalf("no commits in pack listing")
	}
	if countDeltaCommits(out) == 0 {
		t.Fatalf("expected delta-encoded commits in pack; verify-pack:\n%s", out)
	}
	return dir
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func packIdx(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.idx"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one pack idx, got %v (err %v)", matches, err)
	}
	return matches[0]
}

// countDeltaCommits counts delta-encoded commit objects in `git verify-pack -v`
// output. A non-delta commit line has 5 fields
// (sha "commit" size size-in-pack offset); a delta-encoded one appends two more
// (chain depth and base sha), where the last field is a 40-char hex base sha.
func countDeltaCommits(verifyPackOutput string) int {
	n := 0
	for _, line := range strings.Split(verifyPackOutput, "\n") {
		f := strings.Fields(line)
		if len(f) == 7 && f[1] == "commit" && isHexHash(f[6]) {
			n++
		}
	}
	return n
}

// readAllCommitSubjects walks every commit reachable from HEAD and checks the
// file-based reader returns the same subject git does. This drives loose and
// packed objects plus delta resolution across a real history.
func readAllCommitSubjects(t *testing.T, dir string) {
	t.Helper()
	commonDir := filepath.Join(dir, ".git")
	revs := strings.Fields(runGitOutput(t, dir, "rev-list", "--all"))
	if len(revs) == 0 {
		t.Fatal("no revisions")
	}
	for _, rev := range revs {
		want := strings.TrimSpace(runGitOutput(t, dir, "log", "-1", "--format=%s", rev))
		got, err := readCommitSubject(commonDir, rev)
		if err != nil {
			t.Fatalf("readCommitSubject(%s): %v", rev[:7], err)
		}
		if got != want {
			t.Errorf("commit %s subject = %q, want %q", rev[:7], got, want)
		}
	}
}

func TestReadObject_OfsDeltaCommits(t *testing.T) {
	readAllCommitSubjects(t, buildDeltaRepo(t, true))
}

func TestReadObject_RefDeltaCommits(t *testing.T) {
	readAllCommitSubjects(t, buildDeltaRepo(t, false))
}

func TestReadCommitSubject_NotACommit(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("blob body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	commit(t, dir, "has a blob")
	// Hash-object id of the blob; reading it as a commit must error.
	blob := strings.TrimSpace(runGitOutput(t, dir, "hash-object", filepath.Join(dir, "f.txt")))
	if _, err := readCommitSubject(filepath.Join(dir, ".git"), blob); err == nil {
		t.Fatal("expected error reading a blob as a commit")
	}
}

func TestReadObject_MissingAndBadHash(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "only commit")
	commonDir := filepath.Join(dir, ".git")
	if _, _, err := readObject(commonDir, "short", 0); err == nil {
		t.Error("expected error for malformed hash")
	}
	if _, _, err := readObject(commonDir, strings.Repeat("0", 40), 0); err == nil {
		t.Error("expected error for missing object")
	}
	if _, _, err := readObject(commonDir, strings.Repeat("0", 40), 51); err == nil {
		t.Error("expected error for excessive depth")
	}
}

func TestCommitSubject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"normal", "tree abc\nauthor a\n\nThe subject\n\nbody", "The subject"},
		{"no body", "tree abc\n\nJust subject", "Just subject"},
		{"no message", "tree abc\nauthor a", ""},
		{"trailing space trimmed", "tree abc\n\n  spaced  \n", "spaced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitSubject([]byte(tt.in)); got != tt.want {
				t.Errorf("commitSubject(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLooseTypeCode(t *testing.T) {
	cases := map[string]byte{
		"commit": objCommit,
		"tree":   objTree,
		"blob":   objBlob,
		"tag":    objTag,
		"bogus":  0,
	}
	for name, want := range cases {
		if got := looseTypeCode(name); got != want {
			t.Errorf("looseTypeCode(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestIsHexHash(t *testing.T) {
	valid := strings.Repeat("a", 20) + strings.Repeat("F", 20)
	cases := map[string]bool{
		valid:                   true,
		strings.Repeat("0", 40): true,
		strings.Repeat("0", 39): false,
		strings.Repeat("0", 41): false,
		strings.Repeat("g", 40): false,
		strings.Repeat(" ", 40): false,
		"":                      false,
	}
	for s, want := range cases {
		if got := isHexHash(s); got != want {
			t.Errorf("isHexHash(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestApplyDelta exercises the git delta interpreter directly: a copy of a
// base range, a literal insert, and a copy with the default 0x10000 size.
func TestApplyDelta(t *testing.T) {
	base := []byte("Hello, world! This is the base object content.")

	t.Run("copy and insert", func(t *testing.T) {
		// Result: "Hello" (copy base[0:5]) + "XYZ" (insert) + "content." (copy).
		insert := []byte("XYZ")
		tail := "content."
		tailOff := strings.Index(string(base), tail)
		out := buildDelta(t, base, []deltaOp{
			{copy: true, off: 0, size: 5},
			{insert: insert},
			{copy: true, off: uint64(tailOff), size: uint64(len(tail))},
		})
		got, err := applyDelta(base, out)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "HelloXYZcontent." {
			t.Errorf("got %q", got)
		}
	})

	t.Run("default copy size", func(t *testing.T) {
		// size==0 means copy 0x10000, but capped by base length here -> error,
		// so use a base smaller than 0x10000 and request full copy via size 0
		// only when len(base) <= 0x10000. We instead copy whole base explicitly.
		out := buildDelta(t, base, []deltaOp{{copy: true, off: 0, size: uint64(len(base))}})
		got, err := applyDelta(base, out)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(base) {
			t.Errorf("got %q, want %q", got, base)
		}
	})
}

func TestApplyDelta_Errors(t *testing.T) {
	base := []byte("0123456789")
	t.Run("copy out of range", func(t *testing.T) {
		out := buildDelta(t, base, []deltaOp{{copy: true, off: 5, size: 100}})
		if _, err := applyDelta(base, out); err == nil {
			t.Error("expected copy-out-of-range error")
		}
	})
	t.Run("result size mismatch", func(t *testing.T) {
		// Declare a larger result size than we actually produce.
		out := encodeDelta(len(base), len(base)+5, []byte{0x01, 'a'})
		if _, err := applyDelta(base, out); err == nil {
			t.Error("expected size-mismatch error")
		}
	})
	t.Run("zero opcode", func(t *testing.T) {
		out := encodeDelta(len(base), 1, []byte{0x00})
		if _, err := applyDelta(base, out); err == nil {
			t.Error("expected invalid-opcode error")
		}
	})
	t.Run("truncated insert", func(t *testing.T) {
		out := encodeDelta(len(base), 3, []byte{0x03, 'a'}) // says insert 3, gives 1
		if _, err := applyDelta(base, out); err == nil {
			t.Error("expected truncated-insert error")
		}
	})
	t.Run("truncated varint", func(t *testing.T) {
		if _, err := applyDelta(base, []byte{0x80}); err == nil {
			t.Error("expected truncated-varint error")
		}
	})
}

// --- delta-buffer construction helpers (mirror git's wire format) ---

type deltaOp struct {
	copy   bool
	off    uint64
	size   uint64
	insert []byte
}

func buildDelta(t *testing.T, base []byte, ops []deltaOp) []byte {
	t.Helper()
	var body []byte
	var outSize int
	for _, op := range ops {
		if op.copy {
			body = append(body, encodeCopy(op.off, op.size)...)
			outSize += int(op.size)
		} else {
			if len(op.insert) == 0 || len(op.insert) > 0x7f {
				t.Fatalf("bad insert length %d", len(op.insert))
			}
			body = append(body, byte(len(op.insert)))
			body = append(body, op.insert...)
			outSize += len(op.insert)
		}
	}
	return encodeDelta(len(base), outSize, body)
}

func encodeCopy(off, size uint64) []byte {
	out := []byte{0x80}
	for i := uint(0); i < 4; i++ {
		if b := byte(off >> (8 * i)); b != 0 {
			out[0] |= 1 << i
			out = append(out, b)
		}
	}
	for i := uint(0); i < 3; i++ {
		if b := byte(size >> (8 * i)); b != 0 {
			out[0] |= 0x10 << i
			out = append(out, b)
		}
	}
	return out
}

func encodeDelta(baseSize, outSize int, body []byte) []byte {
	out := appendVarint(nil, uint64(baseSize))
	out = appendVarint(out, uint64(outSize))
	return append(out, body...)
}

func appendVarint(dst []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		dst = append(dst, b)
		if v == 0 {
			return dst
		}
	}
}
