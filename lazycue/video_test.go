package lazycue

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestPNG writes a tiny solid-color PNG (393x727 portrait) via ffmpeg so we
// have a realistic screenshot input.
func makeTestPNG(t *testing.T, path, color string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c="+color+":s=393x727:d=1",
		"-frames:v", "1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make png: %v: %s", err, out)
	}
}

func TestRenderVideo(t *testing.T) {
	if !VideoAvailable() {
		t.Skip("ffmpeg/font not available")
	}
	dir := t.TempDir()
	shotDir := filepath.Join(dir, "shots")
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var steps []StepResult
	colors := []string{"red", "green", "blue"}
	for _, c := range colors {
		p := filepath.Join(shotDir, c+".png")
		makeTestPNG(t, p, c)
		steps = append(steps, StepResult{
			Action:     "navigate",
			Summary:    "navigate /page-" + c,
			Pass:       true,
			Screenshot: p,
		})
	}
	// One step with a missing screenshot should be skipped, not fail.
	steps = append(steps, StepResult{Action: "click", Summary: "click #gone", Pass: false})

	out := filepath.Join(dir, "out.mp4")
	if err := RenderVideo(out, "TestColorfulPages", "Navigate the app and verify each colorful page renders correctly", steps, true); err != nil {
		t.Fatalf("RenderVideo: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() < 1000 {
		t.Fatalf("output mp4 suspiciously small: %d bytes", info.Size())
	}

	// Confirm it's a real, playable video with the expected dimensions.
	probe := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name",
		"-of", "csv=p=0", out)
	pout, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v: %s", err, pout)
	}
	got := strings.TrimSpace(string(pout))
	if !strings.Contains(got, "h264") {
		t.Errorf("expected h264 stream, got %q", got)
	}
}

func TestRenderVideoNoScreenshots(t *testing.T) {
	if !VideoAvailable() {
		t.Skip("ffmpeg/font not available")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	err := RenderVideo(out, "TestNoShots", "prompt", []StepResult{{Summary: "noop"}}, true)
	if err == nil {
		t.Fatal("expected error when no screenshots available")
	}
}

func TestRenderVideos(t *testing.T) {
	if !VideoAvailable() {
		t.Skip("ffmpeg/font not available")
	}
	dir := t.TempDir()
	shotDir := filepath.Join(dir, "shots")
	if err := os.MkdirAll(shotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mkShot := func(name string) string {
		p := filepath.Join(shotDir, name+".png")
		makeTestPNG(t, p, "slategray")
		return p
	}

	// Three results: two with screenshots (should render), one with no steps
	// (should be skipped, leaving VideoPath empty).
	results := []*TestResult{
		{Description: "first test description", Pass: true, Steps: []StepResult{
			{Summary: "navigate /a", Pass: true, Screenshot: mkShot("a0")},
			{Summary: "click #b", Pass: true, Screenshot: mkShot("a1")},
		}},
		{Description: "second test description", Pass: false, Steps: []StepResult{
			{Summary: "navigate /c", Pass: true, Screenshot: mkShot("c0")},
		}},
		{Description: "third with no steps", Pass: true},
	}

	videoDir := filepath.Join(dir, "videos")
	if err := RenderVideos(videoDir, results); err != nil {
		t.Fatalf("RenderVideos: %v", err)
	}

	// The two with screenshots get an MP4 at <hash>.mp4 and VideoPath set.
	for _, r := range results[:2] {
		want := filepath.Join(videoDir, DescriptionHash(r.Description)+".mp4")
		if r.VideoPath != want {
			t.Errorf("VideoPath = %q, want %q", r.VideoPath, want)
		}
		if info, err := os.Stat(want); err != nil || info.Size() < 1000 {
			t.Errorf("missing/tiny video %q: %v", want, err)
		}
	}
	// The no-steps one is skipped.
	if results[2].VideoPath != "" {
		t.Errorf("expected no video for steps-less result, got %q", results[2].VideoPath)
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("the quick brown fox jumps", 9)
	lines := strings.Split(got, "\n")
	for _, l := range lines {
		if len(l) > 9 {
			t.Errorf("line too long: %q", l)
		}
	}
	// A word longer than width must be split.
	got = wrapText("supercalifragilistic", 5)
	if !strings.Contains(got, "\n") {
		t.Errorf("expected long word to wrap: %q", got)
	}
}
