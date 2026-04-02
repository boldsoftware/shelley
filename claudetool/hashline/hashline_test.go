package hashline

import (
	"strings"
	"testing"
)

func TestComputeLineHash(t *testing.T) {
	// Basic: same content, same line number → same hash.
	h1 := ComputeLineHash(1, "func main() {")
	h2 := ComputeLineHash(1, "func main() {")
	if h1 != h2 {
		t.Fatalf("same input produced different hashes: %s vs %s", h1, h2)
	}
	if len(h1) != 3 {
		t.Fatalf("hash should be 3 chars, got %q", h1)
	}

	// Different content → likely different hash.
	h3 := ComputeLineHash(1, "func other() {")
	if h1 == h3 {
		t.Logf("warning: collision between %q and %q", "func main() {", "func other() {")
	}

	// Non-significant lines at different positions → different hashes (line number mixed in).
	hBrace1 := ComputeLineHash(5, "}")
	hBrace2 := ComputeLineHash(10, "}")
	if hBrace1 == hBrace2 {
		t.Fatalf("closing braces at different lines should have different hashes")
	}

	// Trailing whitespace is ignored.
	hClean := ComputeLineHash(1, "hello")
	hTrailing := ComputeLineHash(1, "hello   ")
	if hClean != hTrailing {
		t.Fatalf("trailing whitespace should not affect hash: %s vs %s", hClean, hTrailing)
	}

	// \r is stripped.
	hCR := ComputeLineHash(1, "hello\r")
	if hClean != hCR {
		t.Fatalf("CR should not affect hash: %s vs %s", hClean, hCR)
	}

	// Hash chars are from the alphabet.
	for _, c := range h1 {
		if !strings.ContainsRune(alphabet, c) {
			t.Fatalf("hash char %c not in alphabet", c)
		}
	}
}

func TestFormatHashLines(t *testing.T) {
	text := "func hi() {\n  return\n}"
	result := FormatHashLines(text, 1)
	lines := strings.Split(result, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), result)
	}
	for i, line := range lines {
		prefix := FormatLineTag(i+1, strings.Split(text, "\n")[i]) + ":"
		if !strings.HasPrefix(line, prefix) {
			t.Errorf("line %d should start with %q, got %q", i+1, prefix, line)
		}
	}
}

func TestFormatHashLinesStartLine(t *testing.T) {
	text := "a\nb"
	result := FormatHashLines(text, 10)
	if !strings.HasPrefix(result, "10#") {
		t.Fatalf("expected to start with 10#, got %q", result)
	}
	lines := strings.Split(result, "\n")
	if !strings.HasPrefix(lines[1], "11#") {
		t.Fatalf("second line should start with 11#, got %q", lines[1])
	}
}

func TestParseTag(t *testing.T) {
	tests := []struct {
		input   string
		wantLine int
		wantHash string
		wantErr  bool
	}{
		{"5#0Zz", 5, "0Zz", false},
		{"42#VPq", 42, "VPq", false},
		{"  > 10#Q1m", 10, "Q1m", false},  // with prefix markers
		{">>> 3#SNr", 3, "SNr", false},    // mismatch marker
		{"bad", 0, "", true},
		{"0#0Zz", 0, "", true},            // line 0 invalid
		{"5#zz", 0, "", true},             // only 2 chars, need 3
	}
	for _, tt := range tests {
		a, err := ParseTag(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseTag(%q): expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTag(%q): %v", tt.input, err)
			continue
		}
		if a.Line != tt.wantLine || a.Hash != tt.wantHash {
			t.Errorf("ParseTag(%q) = %+v, want Line=%d Hash=%s", tt.input, a, tt.wantLine, tt.wantHash)
		}
	}
}

func TestValidateLineRef(t *testing.T) {
	lines := []string{"package main", "", "func main() {"}

	// Valid ref.
	hash := ComputeLineHash(1, lines[0])
	if err := ValidateLineRef(Anchor{Line: 1, Hash: hash}, lines); err != nil {
		t.Fatalf("valid ref errored: %v", err)
	}

	// Stale hash.
	err := ValidateLineRef(Anchor{Line: 1, Hash: "ZZZ"}, lines)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	mme, ok := err.(*HashMismatchError)
	if !ok {
		t.Fatalf("expected *HashMismatchError, got %T", err)
	}
	if len(mme.Mismatches) != 1 || mme.Mismatches[0].Line != 1 {
		t.Fatalf("unexpected mismatches: %+v", mme.Mismatches)
	}
	remaps := mme.Remaps()
	if _, ok := remaps["1#ZZZ"]; !ok {
		t.Fatalf("expected remap from 1#ZZZ, got %v", remaps)
	}

	// Error message contains >>>.
	if !strings.Contains(mme.Error(), ">>>") {
		t.Fatalf("error message should contain >>>, got: %s", mme.Error())
	}

	// Out of range.
	if err := ValidateLineRef(Anchor{Line: 99, Hash: "ZZZ"}, lines); err == nil {
		t.Fatal("expected out of range error")
	}
}

func TestIsSignificant(t *testing.T) {
	if !isSignificant("func main") {
		t.Error("expected significant")
	}
	if isSignificant("}") {
		t.Error("expected not significant")
	}
	if isSignificant("  \t  ") {
		t.Error("expected not significant")
	}
	if isSignificant("") {
		t.Error("expected not significant")
	}
	if !isSignificant("abc123") {
		t.Error("expected significant")
	}
}
