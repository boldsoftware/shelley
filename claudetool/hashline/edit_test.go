package hashline

import (
	"strings"
	"testing"
)

func makeAnchors(text string) map[int]Anchor {
	lines := strings.Split(text, "\n")
	m := make(map[int]Anchor, len(lines))
	for i, line := range lines {
		num := i + 1
		m[num] = Anchor{Line: num, Hash: ComputeLineHash(num, line)}
	}
	return m
}

func anchorPtr(a Anchor) *Anchor { return &a }

func TestApplyEditsReplaceRange(t *testing.T) {
	text := "line1\nline2\nline3\nline4\nline5"
	anchors := makeAnchors(text)

	result, err := ApplyEdits(text, []Edit{{
		Op:      "replace_range",
		Pos:     anchorPtr(anchors[2]),
		End:     anchorPtr(anchors[3]),
		Content: []string{"new2", "new3", "new3b"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "line1\nnew2\nnew3\nnew3b\nline4\nline5"
	if result != want {
		t.Fatalf("got:\n%s\nwant:\n%s", result, want)
	}
}

func TestApplyEditsDelete(t *testing.T) {
	text := "a\nb\nc\nd"
	anchors := makeAnchors(text)

	result, err := ApplyEdits(text, []Edit{{
		Op:      "replace_range",
		Pos:     anchorPtr(anchors[2]),
		End:     anchorPtr(anchors[3]),
		Content: nil, // delete
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "a\nd" {
		t.Fatalf("got %q, want %q", result, "a\nd")
	}
}

func TestApplyEditsAppendAt(t *testing.T) {
	text := "a\nb\nc"
	anchors := makeAnchors(text)

	result, err := ApplyEdits(text, []Edit{{
		Op:      "append_at",
		Pos:     anchorPtr(anchors[1]),
		Content: []string{"inserted"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "a\ninserted\nb\nc" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyEditsPrependAt(t *testing.T) {
	text := "a\nb\nc"
	anchors := makeAnchors(text)

	result, err := ApplyEdits(text, []Edit{{
		Op:      "prepend_at",
		Pos:     anchorPtr(anchors[2]),
		Content: []string{"inserted"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "a\ninserted\nb\nc" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyEditsAppendFile(t *testing.T) {
	text := "a\nb"
	result, err := ApplyEdits(text, []Edit{{
		Op:      "append_file",
		Content: []string{"c", "d"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "a\nb\nc\nd" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyEditsPrependFile(t *testing.T) {
	text := "a\nb"
	result, err := ApplyEdits(text, []Edit{{
		Op:      "prepend_file",
		Content: []string{"header"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "header\na\nb" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyEditsMultipleBottomUp(t *testing.T) {
	text := "1\n2\n3\n4\n5"
	anchors := makeAnchors(text)

	// Two replacements: lines 2 and 4. Bottom-up means line 4 applied first.
	result, err := ApplyEdits(text, []Edit{
		{
			Op:      "replace_range",
			Pos:     anchorPtr(anchors[2]),
			End:     anchorPtr(anchors[2]),
			Content: []string{"TWO"},
		},
		{
			Op:      "replace_range",
			Pos:     anchorPtr(anchors[4]),
			End:     anchorPtr(anchors[4]),
			Content: []string{"FOUR"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "1\nTWO\n3\nFOUR\n5" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyEditsHashMismatch(t *testing.T) {
	text := "hello\nworld"
	_, err := ApplyEdits(text, []Edit{{
		Op:      "replace_range",
		Pos:     &Anchor{Line: 1, Hash: "ZZZ"}, // wrong hash
		End:     &Anchor{Line: 1, Hash: "ZZZ"},
		Content: []string{"goodbye"},
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	mme, ok := err.(*HashMismatchError)
	if !ok {
		t.Fatalf("expected *HashMismatchError, got %T: %v", err, err)
	}
	if len(mme.Mismatches) == 0 {
		t.Fatal("expected at least one mismatch")
	}
	if !strings.Contains(mme.Error(), ">>>") {
		t.Fatalf("error should contain >>>, got: %s", mme.Error())
	}
}

func TestApplyEditsOutOfRange(t *testing.T) {
	text := "a\nb"
	_, err := ApplyEdits(text, []Edit{{
		Op:      "append_at",
		Pos:     &Anchor{Line: 99, Hash: "ZZZ"},
		Content: []string{"x"},
	}})
	if err == nil {
		t.Fatal("expected error for out of range")
	}
}

func TestApplyEditsEmptyEdits(t *testing.T) {
	text := "unchanged"
	result, err := ApplyEdits(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != text {
		t.Fatalf("got %q", result)
	}
}

func TestApplyEditsUnknownOp(t *testing.T) {
	_, err := ApplyEdits("a", []Edit{{Op: "nope"}})
	if err == nil {
		t.Fatal("expected error for unknown op")
	}
}
