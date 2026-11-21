package lazycue

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSaveAndGetRoundtrip(t *testing.T) {
	dir := t.TempDir()
	desc := "navigate to /login and verify the form"
	steps := []byte(`[{"action":"navigate","url":"/login"}]`)
	meta := &CacheMetadata{Mode: "generated"}

	if err := SaveCachedTest(dir, desc, steps, 1, meta); err != nil {
		t.Fatal(err)
	}

	got, hit, err := GetCachedTest(dir, desc)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || hit == nil {
		t.Fatal("expected cached test, got nil")
	}
	if hit.Version != 1 {
		t.Fatalf("version = %d, want 1", hit.Version)
	}
	if got.Description != desc {
		t.Fatalf("description = %q, want %q", got.Description, desc)
	}
	if !jsonEqual(t, got.Steps, steps) {
		t.Fatalf("steps = %s, want %s", got.Steps, steps)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(va)
	jb, _ := json.Marshal(vb)
	return string(ja) == string(jb)
}

func TestGetMissingFile(t *testing.T) {
	dir := t.TempDir()
	got, hit, err := GetCachedTest(dir, "no such description")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != nil || hit != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", got, hit)
	}
}

func TestSavedFileHasBanner(t *testing.T) {
	dir := t.TempDir()
	desc := "some test description"
	steps := []byte(`[{"action":"screenshot"}]`)

	if err := SaveCachedTest(dir, desc, steps, 1, &CacheMetadata{Mode: "generated"}); err != nil {
		t.Fatal(err)
	}

	path := CacheFilePath(dir, desc)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["_README"]; !ok {
		t.Fatal("expected _README banner field in cache file")
	}
	if !strings.Contains(string(data), "managed by LazyCue") {
		t.Fatal("expected managed-by banner text in cache file")
	}
	if !strings.Contains(string(data), desc) {
		t.Fatal("expected description in cache file")
	}
}

func TestHealOverwrites(t *testing.T) {
	dir := t.TempDir()
	desc := "heal test"

	v1 := []byte(`[{"action":"navigate","url":"/old"}]`)
	if err := SaveCachedTest(dir, desc, v1, 1, &CacheMetadata{Mode: "generated"}); err != nil {
		t.Fatal(err)
	}

	v2 := []byte(`[{"action":"navigate","url":"/new"}]`)
	if err := SaveCachedTest(dir, desc, v2, 2, &CacheMetadata{Mode: "healed"}); err != nil {
		t.Fatal(err)
	}

	got, hit, err := GetCachedTest(dir, desc)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Version != 2 {
		t.Fatalf("version = %d, want 2", hit.Version)
	}
	if !strings.Contains(string(got.Steps), "/new") {
		t.Fatalf("expected v2 steps, got %s", got.Steps)
	}
}

func TestStepSummary(t *testing.T) {
	tests := []struct {
		step Step
		want string
	}{
		{Step{Action: "navigate", URL: "/new"}, "navigate /new"},
		{Step{Action: "click", Selector: "#btn"}, "click #btn"},
		{Step{Action: "fill", Selector: "input", Value: "hello"}, "fill input hello"},
		{Step{Action: "wait_visible", Selector: ".loading"}, "wait_visible .loading"},
		{Step{Action: "wait_text", Text: "Hello world"}, "wait_text Hello world"},
		{Step{Action: "assert_text", Selector: "h1", Text: "Title"}, "assert_text h1 Title"},
		{Step{Action: "press_key", Key: "Enter"}, "press_key Enter"},
		{Step{Action: "screenshot"}, "screenshot"},
		{Step{Action: "eval", Expression: "document.title"}, "eval document.title"},
		{Step{Action: "eval", Expression: "1+1", Expect: "2"}, "eval 1+1 expect=2"},
		{Step{Action: "assert_count", Selector: "li", Count: 3}, "assert_count li 3"},
		{Step{Action: "wait_url", Value: "/c/abc"}, "wait_url /c/abc"},
		{Step{Action: "wait_url", Text: "/c/"}, "wait_url ~/c/"},
		{Step{Action: "sleep", Timeout: "1s"}, "sleep 1s"},
	}
	for _, tt := range tests {
		got := StepSummary(tt.step)
		if got != tt.want {
			t.Errorf("StepSummary(%+v) = %q, want %q", tt.step, got, tt.want)
		}
	}
}

func TestSameSteps(t *testing.T) {
	a := []byte(`[{"action":"navigate","url":"/new"},{"action":"click","selector":"#go"}]`)
	// Identical content but reformatted (whitespace + key order differ).
	b := []byte(`[
	  { "url": "/new", "action": "navigate" },
	  { "selector": "#go", "action": "click" }
	]`)
	if !sameSteps(a, b) {
		t.Error("reformatted but identical steps should compare equal")
	}
	// A real difference in a step field.
	c := []byte(`[{"action":"navigate","url":"/new"},{"action":"click","selector":"#stop"}]`)
	if sameSteps(a, c) {
		t.Error("differing selector should compare unequal")
	}
	// Different number of steps.
	d := []byte(`[{"action":"navigate","url":"/new"}]`)
	if sameSteps(a, d) {
		t.Error("differing length should compare unequal")
	}
	// Optional fields are pointers; equal values at different addresses are
	// the same step.
	e := []byte(`[{"action":"hover","selector":"#a","x":1,"y":2}]`)
	if !sameSteps(e, []byte(`[{"action":"hover","selector":"#a","y":2,"x":1}]`)) {
		t.Error("identical offsets should compare equal")
	}
	if sameSteps(e, []byte(`[{"action":"hover","selector":"#a","x":1,"y":3}]`)) {
		t.Error("differing offsets should compare unequal")
	}
	// Unparseable input is never "same".
	if sameSteps(a, []byte(`not json`)) {
		t.Error("unparseable steps should compare unequal")
	}
}
