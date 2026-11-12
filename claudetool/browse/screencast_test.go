package browse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestScreencastStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Verify no screencast is running.
	active, _, _, _ := tools.screencastStatus()
	if active {
		t.Fatal("expected no active screencast")
	}

	// Start via combined tool.
	tool := tools.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"resize","width":881,"height":495}`))
	if out.Error != nil {
		t.Fatalf("resize: %v", out.Error)
	}
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "Screencast recording") {
		if strings.Contains(text, "failed to start browser") || strings.Contains(text, "ffmpeg") {
			t.Skip("Browser or ffmpeg not available")
		}
		t.Fatalf("unexpected start result: %s", text)
	}
	if !strings.Contains(text, ".mp4") {
		t.Fatalf("expected .mp4 in start message, got: %s", text)
	}
	t.Logf("Start result: %s", text)

	// Double-start should error.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "already active") {
		t.Fatalf("expected already-active error, got: %s", text)
	}

	// Navigate to generate some frames.
	out = tool.Run(ctx, json.RawMessage(`{"action":"navigate","url":"data:text/html,<h1>Screencast Test</h1>"}`))
	text = contentText(t, out)
	if strings.Contains(text, "Error") {
		t.Fatalf("navigate failed: %s", text)
	}

	// Poll until we have at least one frame.
	var sessionID string
	var frameCount int
	var elapsed time.Duration
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var active bool
		active, sessionID, frameCount, elapsed = tools.screencastStatus()
		if active && frameCount > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if frameCount == 0 {
		t.Fatal("expected at least one screencast frame")
	}
	t.Logf("Status: session=%s frames=%d elapsed=%v", sessionID, frameCount, elapsed)

	// Stop via combined tool.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "Screencast stopped") {
		t.Fatalf("unexpected stop result: %s", text)
	}
	if !strings.Contains(text, ".mp4") {
		t.Fatalf("expected .mp4 path in stop message, got: %s", text)
	}
	t.Logf("Stop result: %s", text)

	// Verify MP4 file exists on disk.
	mp4Path := ScreencastDir + "/" + sessionID + ".mp4"
	info, err := os.Stat(mp4Path)
	if err != nil {
		t.Fatalf("MP4 file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("MP4 file is empty")
	}
	t.Logf("MP4 file: %s (%d bytes)", mp4Path, info.Size())

	// An odd-sized viewport must yield an encodable, decodable MP4.
	if _, err := exec.LookPath("ffprobe"); err == nil {
		probe := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=width,height", "-of", "csv=p=0", mp4Path)
		dimensions, err := probe.CombinedOutput()
		if err != nil {
			t.Fatalf("ffprobe: %v: %s", err, dimensions)
		}
		var w, h int
		if _, err := fmt.Sscanf(string(dimensions), "%d,%d", &w, &h); err != nil || w%2 != 0 || h%2 != 0 {
			t.Fatalf("expected even video dimensions, got %q: %v", dimensions, err)
		}
	}

	// Double-stop should error.
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	text = contentText(t, out)
	if !strings.Contains(text, "no active screencast") {
		t.Fatalf("expected no-active error, got: %s", text)
	}

	// Status should show inactive.
	active, _, _, _ = tools.screencastStatus()
	if active {
		t.Fatal("expected no active screencast after stop")
	}
}

// Exercise the actual FFmpeg command with odd frames and a mid-recording
// resize. Both CDP output formats must produce a playable, fixed-size MP4.
func TestScreencastFFmpegOddDimensions(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	for _, format := range []string{"png", "mjpeg"} {
		t.Run(format, func(t *testing.T) {
			var input bytes.Buffer
			for _, size := range []image.Point{{881, 495}, {880, 496}, {883, 497}} {
				frame := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
				var err error
				if format == "png" {
					err = png.Encode(&input, frame)
				} else {
					err = jpeg.Encode(&input, frame, &jpeg.Options{Quality: 80})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			mp4Path := filepath.Join(t.TempDir(), "recording.mp4")
			cmd := screencastFFmpegCommand(map[string]string{"png": "image2pipe", "mjpeg": "mjpeg"}[format], mp4Path)
			cmd.Stdin = &input
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v: %s", err, output)
			}
			probe := exec.Command("ffprobe", "-v", "error", "-count_frames",
				"-select_streams", "v:0", "-show_entries", "stream=width,height,nb_read_frames",
				"-of", "csv=p=0", mp4Path)
			output, err := probe.CombinedOutput()
			if err != nil {
				t.Fatalf("ffprobe: %v: %s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != "882,496,3" {
				t.Fatalf("want three frames at fixed even dimensions, got %q", got)
			}
			decode := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", mp4Path, "-f", "null", "-")
			if output, err := decode.CombinedOutput(); err != nil {
				t.Fatalf("decode MP4: %v: %s", err, output)
			}
		})
	}
}

func TestScreencastStopReportsEncoderFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.mp4")
	// A nonempty file must not mask a nonzero encoder exit.
	if err := os.WriteFile(path, []byte("partial recording"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "echo encoder failed >&2; exit 1")
	cmd.Stderr = &limitedBuffer{max: 4096}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b := &BrowseTools{screencast: screencastState{
		active: true, sessionID: "test", outputPath: path,
		ffmpegCmd: cmd, startTime: time.Now(),
	}}
	_, _, _, _, err := b.screencastStop()
	if err == nil || !strings.Contains(err.Error(), "encoder failed") || !strings.Contains(err.Error(), path) {
		t.Fatalf("expected encoder failure with stderr and path, got %v", err)
	}
}

func TestScreencastKilledEncoderFailsStopTool(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	b := NewBrowseTools(ctx, 0)
	t.Cleanup(b.Close)
	tool := b.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_start"}`))
	if out.Error != nil {
		if strings.Contains(out.Error.Error(), "failed to start browser") ||
			strings.Contains(out.Error.Error(), "failed to start ffmpeg") {
			t.Skip(out.Error)
		}
		t.Fatal(out.Error)
	}
	b.screencast.mu.Lock()
	process := b.screencast.ffmpegCmd.Process
	b.screencast.mu.Unlock()
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	out = tool.Run(ctx, json.RawMessage(`{"action":"screencast_stop"}`))
	if out.Error == nil || !strings.Contains(out.Error.Error(), "ffmpeg failed") || out.Display != nil {
		t.Fatalf("expected tool error without playable MP4 display, got %+v", out)
	}
}

func TestScreencastConcurrentStopSharesFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.mp4")
	cmd := exec.Command("sh", "-c", "echo encoder failed >&2; exit 1")
	cmd.Stderr = &limitedBuffer{max: 4096}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b := &BrowseTools{screencast: screencastState{
		active: true, sessionID: "test", outputPath: path,
		ffmpegCmd: cmd, startTime: time.Now(),
	}}
	b.screencast.mu.Lock()
	resources := b.screencast.claimStopLocked()
	b.screencast.mu.Unlock()

	// One caller owns finalization; any other caller must see its result,
	// not mistake the already-inactive session for a successful stop.
	errCh := make(chan error, 1)
	go func() { errCh <- b.screencastStopInternal() }()
	err := b.finishScreencastStop(resources)
	if err == nil || !strings.Contains(err.Error(), "encoder failed") {
		t.Fatalf("expected encoder failure, got %v", err)
	}
	if otherErr := <-errCh; otherErr == nil || otherErr.Error() != err.Error() {
		t.Fatalf("concurrent stop got %v, want %v", otherErr, err)
	}
}

func TestScreencastStalledWriterTimesOut(t *testing.T) {
	// This child keeps stdin open but never reads it, so a large frame write
	// blocks until stop's deadline kills the child and breaks its pipe.
	cmd := exec.Command("sh", "-c", "exec sleep 60")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	b := &BrowseTools{screencast: screencastState{
		active: true, sessionID: "test", ffmpegCmd: cmd,
		ffmpegIn: in, outputPath: filepath.Join(t.TempDir(), "recording.mp4"),
	}}
	b.screencast.mu.Lock()
	b.screencast.writers.Add(1)
	b.screencast.mu.Unlock()
	writeErr := make(chan error, 1)
	go func() {
		_, err := in.Write(make([]byte, 1<<20))
		b.screencast.writers.Done()
		writeErr <- err
	}()
	b.screencast.mu.Lock()
	resources := b.screencast.claimStopLocked()
	b.screencast.mu.Unlock()
	resources.timeout = 50 * time.Millisecond
	if err := b.finishScreencastStop(resources); err == nil {
		t.Fatal("expected timed-out encoder to fail")
	}
	if err := <-writeErr; err == nil {
		t.Fatal("expected blocked frame writer to be interrupted")
	}
}

func TestScreencastStopReportsMissingOutput(t *testing.T) {
	for _, createEmptyFile := range []bool{false, true} {
		t.Run(fmt.Sprint("empty=", createEmptyFile), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recording.mp4")
			if createEmptyFile {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-c", "exit 0")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			b := &BrowseTools{screencast: screencastState{
				active: true, sessionID: "test", outputPath: path,
				ffmpegCmd: cmd, startTime: time.Now(),
			}}
			_, _, _, _, err := b.screencastStop()
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected missing/empty MP4 failure, got %v", err)
			}
		})
	}
}

func TestScreencastStderrRetainsLastError(t *testing.T) {
	lb := &limitedBuffer{max: 12}
	for _, part := range []string{"long banner\n", "actual error"} {
		if n, err := lb.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("write: n=%d err=%v", n, err)
		}
	}
	if got := lb.String(); got != "actual error" {
		t.Fatalf("stderr tail = %q", got)
	}
}

func TestScreencastLimitsAreReasonable(t *testing.T) {
	if ScreencastMaxFrames < 1000 {
		t.Fatalf("ScreencastMaxFrames too low: %d", ScreencastMaxFrames)
	}
	if ScreencastMaxDuration < 10*time.Minute {
		t.Fatalf("ScreencastMaxDuration too low: %v", ScreencastMaxDuration)
	}
}

func TestScreencastStatusWhenInactive(t *testing.T) {
	ctx := t.Context()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	out := tool.Run(ctx, json.RawMessage(`{"action":"screencast_status"}`))
	text := contentText(t, out)
	if !strings.Contains(text, "No active screencast") {
		t.Fatalf("expected no-active message, got: %s", text)
	}
}

func TestScreencastSchemaIncludes(t *testing.T) {
	tools := NewBrowseTools(t.Context(), 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}

	for _, action := range []string{"screencast_start", "screencast_stop", "screencast_status"} {
		found := false
		for _, a := range schema.Properties["action"].Enum {
			if a == action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("action %q not in schema enum", action)
		}
	}

	for _, prop := range []string{"format", "quality", "max_width", "max_height", "every_nth_frame"} {
		if _, ok := schema.Properties[prop]; !ok {
			t.Errorf("expected property %q in schema", prop)
		}
	}
}

// contentText extracts the text from a tool output, including errors.
func contentText(t *testing.T, out llm.ToolOut) string {
	t.Helper()
	if out.Error != nil {
		return out.Error.Error()
	}
	var parts []string
	for _, c := range out.LLMContent {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}
