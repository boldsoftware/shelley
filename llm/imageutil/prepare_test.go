package imageutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrepare(t *testing.T) {
	data := createTestPNG(t, 300, 100)
	prepared, err := Prepare(data, "large.png", 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.MediaType != "image/png" || !prepared.Resized {
		t.Fatalf("prepared = %+v", prepared)
	}
	if prepared.Width != 200 || prepared.Height != 66 {
		t.Errorf("dimensions = %dx%d, want 200x66", prepared.Width, prepared.Height)
	}

	unchanged, err := Prepare(createTestPNG(t, 10, 8), "small.png", 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Resized || unchanged.Width != 10 || unchanged.Height != 8 {
		t.Errorf("unchanged = %+v", unchanged)
	}
}

func TestPrepareErrors(t *testing.T) {
	if _, err := Prepare([]byte("plain text"), "notes.txt", 0, 0); err == nil || !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("non-image error = %v", err)
	}

	data := createTestPNG(t, 64, 64)
	if _, err := Prepare(data[:len(data)/2], "broken.png", 0, 0); err == nil || !strings.Contains(err.Error(), "corrupt or truncated") {
		t.Fatalf("corrupt image error = %v", err)
	}

	data = createTestPNG(t, 10, 10)
	if _, err := Prepare(data, "large.png", 0, len(data)-1); err == nil || !strings.Contains(err.Error(), "image too large") {
		t.Fatalf("byte limit error = %v", err)
	}
}

func TestPreparePreservesBytesWithoutLimits(t *testing.T) {
	data := createTestPNG(t, 12, 9)
	prepared, err := Prepare(data, "image.png", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prepared.Data, data) {
		t.Error("Prepare changed an image that needed no conversion or resize")
	}
}
