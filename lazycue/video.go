package lazycue

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// videoCanvasW and videoCanvasH define the output frame size. The default
// browser viewport is a 393x727 portrait (Pixel 5) rendered at 2.75x DPR.
// 600x1110 keeps its aspect ratio, so native screenshots fit with no
// letterboxing.
const (
	videoCanvasW = 600
	videoCanvasH = 1110
	videoBG      = "0x0d1117" // matches the HTML report background
)

// titleSeconds and stepSeconds control how long each frame is shown.
const (
	titleSeconds = 3.0
	stepSeconds  = 2.5
)

// videoFPS is the output frame rate. The video is a static slideshow (nothing
// animates within a frame), so a low rate is plenty and keeps rendering cheap:
// the single-invocation pipeline applies drawtext per output frame, so cost
// scales with fps. 10 is smooth enough for scrubbing while ~3x cheaper than 30.
const videoFPS = 10

// VideoAvailable reports whether the external dependencies needed to render a
// video (ffmpeg + a usable font) are present.
func VideoAvailable() bool {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return false
	}
	return findFont() != ""
}

// findFont returns the path to a TrueType font for drawtext, honoring
// LAZYCUE_FONT, else probing common locations. Returns "" if none found.
func findFont() string {
	if f := os.Getenv("LAZYCUE_FONT"); f != "" {
		if _, err := os.Stat(f); err == nil {
			return f
		}
	}
	for _, p := range []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
		"/usr/share/fonts/truetype/freefont/FreeSans.ttf",
		"/System/Library/Fonts/Supplemental/Arial.ttf",
		"/Library/Fonts/Arial.ttf",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// RenderVideos renders an MP4 for every result that has on-disk screenshots,
// writing <videoDir>/<DescriptionHash>.mp4 and setting r.VideoPath on success.
//
// Rendering is deliberately kept OUT of the per-test hot path: each MP4 is a
// non-trivial ffmpeg job, so doing it inline would inflate every test's wall
// time (and timeout budget). Callers invoke this once, after all tests finish.
// Renders run concurrently (bounded by GOMAXPROCS) since they're independent.
//
// It is best-effort: if ffmpeg or a font is unavailable it is a no-op (returns
// nil). Per-video failures are collected and returned joined, but successful
// videos are still produced and their VideoPath set.
func RenderVideos(videoDir string, results []*TestResult) error {
	if videoDir == "" || len(results) == 0 || !VideoAvailable() {
		return nil
	}

	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, r := range results {
		if len(r.Steps) == 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(r *TestResult) {
			defer wg.Done()
			defer func() { <-sem }()
			out := filepath.Join(videoDir, DescriptionHash(r.Description)+".mp4")
			if err := RenderVideo(out, r.Name, r.Description, r.Steps, r.Pass); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", DescriptionHash(r.Description), err))
				mu.Unlock()
				return
			}
			r.VideoPath = out
		}(r)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// RenderVideo produces an MP4 at outPath that opens with a title card showing
// the test name and prompt, followed by each captured screenshot with its step
// instruction overlaid. Steps without a screenshot on disk are skipped. name
// may be empty (the title card then shows only the prompt).
//
// The whole video is produced by a SINGLE ffmpeg invocation: a filter_complex
// builds the title card from a color source and each frame from its looped PNG
// input, then concats them. This avoids spawning O(steps) ffmpeg processes (the
// old approach rendered a PNG per step plus a final encode), which dominated
// wall time. Returns an error if ffmpeg/font are unavailable or rendering
// fails.
func RenderVideo(outPath, name, prompt string, steps []StepResult, pass bool) error {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}
	font := findFont()
	if font == "" {
		return fmt.Errorf("no usable TrueType font found (set LAZYCUE_FONT)")
	}

	// Only include steps whose screenshot actually exists on disk.
	var shots []StepResult
	for _, s := range steps {
		if s.Screenshot == "" {
			continue
		}
		if _, statErr := os.Stat(s.Screenshot); statErr == nil {
			shots = append(shots, s)
		}
	}
	if len(shots) == 0 {
		return fmt.Errorf("no screenshots available to render")
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	// drawtext reads its text from files (avoids filtergraph escaping hell).
	work, err := os.MkdirTemp("", "lazycue-video-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// Each input is a SINGLE frame: input 0 is a 1-frame color source for the
	// title card; inputs 1..N are the screenshot PNGs (one frame each). Each
	// chain draws its text once on that single frame, then loops the finished
	// still to the desired duration (see staticFrameTail). Drawing once instead
	// of per output frame is what keeps rendering fast.
	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	args = append(
		args,
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=%dx%d:d=1:r=1", videoBG, videoCanvasW, videoCanvasH),
	)
	for _, s := range shots {
		args = append(args, "-i", s.Screenshot)
	}

	// filter_complex: one chain per input producing [v0]..[vN], then concat.
	var fc strings.Builder
	fc.WriteString("[0:v]" + titleChain(font, work, name, prompt, pass, len(shots)) + "[v0];")
	for i, s := range shots {
		caption := fmt.Sprintf("Step %d/%d   %s", i+1, len(shots), s.Summary)
		fmt.Fprintf(&fc, "[%d:v]%s[v%d];", i+1, stepChain(font, work, i, caption, s.Pass), i+1)
	}
	for i := 0; i <= len(shots); i++ {
		fmt.Fprintf(&fc, "[v%d]", i)
	}
	fmt.Fprintf(&fc, "concat=n=%d:v=1:a=0[outv]", len(shots)+1)

	args = append(
		args,
		"-filter_complex", fc.String(),
		"-map", "[outv]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-movflags", "+faststart",
		outPath,
	)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// titleChain returns the filtergraph chain (operating on the color input) that
// draws the header, the Go test name, the prompt, and a PASS/FAIL footer. name
// may be empty, in which case the name line is omitted.
//
// The prompt is auto-fitted: long descriptions get a smaller font (and a
// correspondingly wider wrap) so the whole thing always fits inside a fixed
// middle band, and it is vertically centered within that band so it never
// collides with the name above or the footer below.
func titleChain(font, work, name, prompt string, pass bool, nSteps int) string {
	hdr := writeTextFile(work, "title-hdr", "LAZYCUE")
	statusText := fmt.Sprintf("PASS · %d steps", nSteps)
	statusColor := "0x3fb950"
	if !pass {
		statusText = fmt.Sprintf("FAIL · %d steps", nSteps)
		statusColor = "0xf85149"
	}
	foot := writeTextFile(work, "title-foot", statusText)

	// The prompt band: everything between the name row and the footer.
	const bandTop, bandBottom = 210, videoCanvasH - 150
	promptSize, promptWrap, promptSpacing := fitPromptText(prompt, bandBottom-bandTop)
	body := writeTextFile(work, "title-body", wrapText(prompt, promptWrap))

	chain := []string{
		drawtext(font, hdr, "0x8b949e", 26, 6, "(w-text_w)/2", "55", ""),
	}
	// The Go test name (e.g. TestNewPageSmoke) goes right under the header, in
	// the accent color used for code elsewhere, so each clip is self-identifying.
	if name != "" {
		nameFile := writeTextFile(work, "title-name", wrapText(name, 22))
		chain = append(chain, drawtext(font, nameFile, "0xd2a8ff", 30, 6, "(w-text_w)/2", "100", ""))
	}
	// Center the body vertically within [bandTop, bandBottom].
	bodyY := fmt.Sprintf("%d+(%d-text_h)/2", bandTop, bandBottom-bandTop)
	chain = append(
		chain,
		drawtext(font, body, "white", promptSize, promptSpacing, "(w-text_w)/2", bodyY, ""),
		drawtext(font, foot, statusColor, 34, 6, "(w-text_w)/2", "h-text_h-70", ""),
		staticFrameTail(titleSeconds),
	)
	return strings.Join(chain, ",")
}

// fitPromptText picks a font size, wrap width, and line spacing for the title
// card's prompt so the wrapped text fits within availH pixels of vertical
// space on the videoCanvasW-wide canvas. Longer prompts shrink until they fit
// (down to a floor); short prompts stay large.
func fitPromptText(prompt string, availH int) (size, wrap, spacing int) {
	// Usable text width leaves a margin on each side of the canvas.
	const usableW = videoCanvasW - 80
	spacing = 12
	for size = 40; size > 16; size -= 2 {
		// DejaVu Sans advances at roughly 0.52em per character; derive the
		// wrap width that keeps a line inside usableW at this font size.
		wrap = int(float64(usableW) / (0.52 * float64(size)))
		if wrap < 12 {
			wrap = 12
		}
		lines := strings.Count(wrapText(prompt, wrap), "\n") + 1
		lineH := float64(size)*1.2 + float64(spacing)
		if float64(lines)*lineH <= float64(availH) {
			return size, wrap, spacing
		}
	}
	// Floor: smallest font, tighter spacing, widest wrap.
	spacing = 8
	wrap = int(float64(usableW) / (0.52 * float64(size)))
	if wrap < 12 {
		wrap = 12
	}
	return size, wrap, spacing
}

// stepChain returns the filtergraph chain (operating on screenshot input i)
// that scales/pads it onto the canvas and overlays a PASS/FAIL badge plus the
// instruction caption.
//
// Both overlays sit at the TOP of the frame on purpose: a browser's <video>
// control bar is drawn over the bottom edge, so a caption anchored to the
// bottom gets clipped behind the scrubber (which is exactly what we saw). The
// caption box auto-sizes to its (wrapped) text, so multi-line instructions grow
// downward into the empty area below the app header rather than being cut off.
func stepChain(font, work string, i int, caption string, pass bool) string {
	capFile := writeTextFile(work, fmt.Sprintf("cap-%03d", i), wrapText(caption, 34))
	badgeColor := "0x3fb950"
	status := "PASS"
	if !pass {
		badgeColor = "0xf85149"
		status = "FAIL"
	}
	badgeFile := writeTextFile(work, fmt.Sprintf("badge-%03d", i), status)

	scalePad := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:%s",
		videoCanvasW, videoCanvasH, videoCanvasW, videoCanvasH, videoBG,
	)
	return strings.Join([]string{
		scalePad,
		// Badge on its own line at the very top, caption box just beneath it.
		drawtext(font, badgeFile, badgeColor, 26, 0, "(w-text_w)/2", "26", "black@0.7"),
		drawtextBox(font, capFile, "white", 24, 8, "(w-text_w)/2", "84", "black@0.7", 14),
		staticFrameTail(stepSeconds),
	}, ",")
}

// staticFrameTail loops the single finished frame to fill `seconds` at videoFPS
// and normalizes output for libx264. loop=size=1 buffers one frame and emits it
// loop+1 times; setpts then lays those copies out on a constant-rate timeline.
func staticFrameTail(seconds float64) string {
	frames := int(seconds*float64(videoFPS) + 0.5)
	if frames < 1 {
		frames = 1
	}
	return strings.Join([]string{
		fmt.Sprintf("loop=loop=%d:size=1:start=0", frames-1),
		"settb=AVTB",
		fmt.Sprintf("setpts=N/%d/TB", videoFPS),
		fmt.Sprintf("fps=%d", videoFPS),
		"format=yuv420p", "setsar=1",
	}, ",")
}

// drawtext builds a drawtext filter clause reading from a textfile. boxColor
// "" disables the background box.
func drawtext(font, textFile, color string, size, lineSpacing int, x, y, boxColor string) string {
	parts := []string{
		"drawtext=fontfile=" + ffEscapeFilterArg(font),
		"textfile=" + ffEscapeFilterArg(textFile),
		"fontcolor=" + color,
		fmt.Sprintf("fontsize=%d", size),
		fmt.Sprintf("line_spacing=%d", lineSpacing),
		"x=" + x,
		"y=" + y,
	}
	if boxColor != "" {
		parts = append(parts, "box=1", "boxcolor="+boxColor, "boxborderw=14")
	}
	return strings.Join(parts, ":")
}

// drawtextBox is drawtext with an explicit box and border width.
func drawtextBox(font, textFile, color string, size, lineSpacing int, x, y, boxColor string, border int) string {
	return strings.Join([]string{
		"drawtext=fontfile=" + ffEscapeFilterArg(font),
		"textfile=" + ffEscapeFilterArg(textFile),
		"fontcolor=" + color,
		fmt.Sprintf("fontsize=%d", size),
		fmt.Sprintf("line_spacing=%d", lineSpacing),
		"box=1", "boxcolor=" + boxColor, fmt.Sprintf("boxborderw=%d", border),
		"x=" + x,
		"y=" + y,
	}, ":")
}

// writeTextFile writes content to a uniquely named file in dir and returns its
// path. drawtext reads text from files to avoid filter-string escaping hell.
func writeTextFile(dir, name, content string) string {
	safe := sanitize(name)
	p := filepath.Join(dir, safe+".txt")
	_ = os.WriteFile(p, []byte(content), 0o644)
	return p
}

// wrapText hard-wraps text to roughly width characters per line, preserving
// existing newlines and breaking long words if needed.
func wrapText(s string, width int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			for len(w) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, w[:width])
				w = w[width:]
			}
			switch {
			case line == "":
				line = w
			case len(line)+1+len(w) <= width:
				line += " " + w
			default:
				out = append(out, line)
				line = w
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// ffEscapeFilterArg escapes a path used as a value inside an ffmpeg filtergraph
// (colons and backslashes are special). Paths produced here avoid special
// characters, but escape defensively.
func ffEscapeFilterArg(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, ":", `\:`)
	p = strings.ReplaceAll(p, "'", `\'`)
	return p
}
