package test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/image-comments.spec.ts.

// Clicking an image in the conversation opens the annotation view: drag a box
// around part of the image, type a comment in the dialog that opens, and the
// comment goes straight into the message input as a quoted block naming the
// image and the region — the image-side counterpart of the diff viewer's line
// comments, using the same CommentDialog and the same insert-as-you-go flow.
//
// The "inline image" predictable pattern writes a 48x48 PNG into the
// conversation cwd via bash and then references it with relative-path
// markdown, so we get a real, deterministically-sized image to annotate.

// rotatedJPEG100x50 is a 100x50 JPEG tagged EXIF Orientation=6 (rotate 90° CW
// for display), so browsers render it 50x100 while a header-only decode
// reports 100x50. Pre-encoded because that tag is the whole point and no PNG
// can carry it.
const rotatedJPEG100x50 = "/9j/4AAQSkZJRgABAQAAAQABAAD/4QAiRXhpZgAATU0AKgAAAAgAAQESAAMAAAABAAYAAAAAAAD/2wBDABsSFBcU" +
	"ERsXFhceHBsgKEIrKCUlKFE6PTBCYFVlZF9VXVtqeJmBanGQc1tdhbWGkJ6jq62rZ4C8ybqmx5moq6T/2wBDARwe" +
	"HigjKE4rK06kbl1upKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKT/wAAR" +
	"CAAyAGQDASIAAhEBAxEB/8QAGQAAAwEBAQAAAAAAAAAAAAAAAAIDAQQG/8QAFxABAQEBAAAAAAAAAAAAAAAAAAEC" +
	"Ev/EABoBAQEAAwEBAAAAAAAAAAAAAAMBAAIEBgX/xAAYEQEBAQEBAAAAAAAAAAAAAAAAARECEv/aAAwDAQACEQMR" +
	"AD8A87MGmFZg0w11pO0Zg0wtMNmF0s7SmGzC0waYZpJ2jMGmFZg0wuknaMwaYWmGzC6SdpTBphWYNMLpJ2jwF+Az" +
	"W/txTBphaYbMObXnZ2lMNmFpg0wzSztGYNMKzBphdJO0phswtMNmF0k7SmDTCswaYXSTtGYNMLTDZhmknaPAdHAX" +
	"W/txTBphWYNMOXXnZ2jMGmFZg0wulnaUw2YWmDTC6SdozBphWYNMLpJ2jMGmFphswzSTtKYbMLTBphdJO0eAvwF1" +
	"v7cENAHO+FGw0AUkNGwBSQ0NAGEjYaAKWGhoApI0AK3f/9k="

// imageCommentJS has the spec's helpers.
const imageCommentJS = `(() => {
  // The WxH+X+Y the open comment dialog's title names, or "".
  window.geometry = () => {
    const title = document.querySelector('.diff-viewer-comment-dialog-handle')?.textContent ?? '';
    return title.match(/\d+x\d+\+\d+\+\d+/)?.[0] ?? '';
  };
  // window.kept(i) is the geometry window.keepGeometry() kept i-th.
  window.__kept = [];
  window.keepGeometry = () => { window.__kept.push(window.geometry()); return window.__kept.length; };
  window.kept = (i) => window.__kept[i];
  // "ok" if the dialog's geometry is the middle half of a width x height
  // image, else what is off.
  //
  // Pointer coordinates are quantized to whole device pixels, so a drag aimed
  // at 25% can land a fraction off and move the result by a pixel. That slack
  // is inherent and harmless: what these tests are about is whether the region
  // is expressed in the right coordinate space at all, and a scaling bug is off
  // by a factor, not by one pixel. Tolerance is in source pixels and scales
  // with how much the image is shrunk on screen, since that is what one device
  // pixel of slop is worth.
  window.middleHalf = (width, height) => {
    const geometry = window.geometry();
    const m = geometry.match(/^(\d+)x(\d+)\+(\d+)\+(\d+)$/);
    if (!m) return 'the dialog title names no region';
    const [w, h, x, y] = m.slice(1).map(Number);
    const off = [];
    const near = (got, want, span, label) => {
      const slack = Math.max(2, Math.ceil(span / 100));
      if (Math.abs(got - want) > slack) off.push(label + ': ' + got + ' vs ' + want + ' (±' + slack + ')');
    };
    near(w, width / 2, width, 'width');
    near(h, height / 2, height, 'height');
    near(x, width / 4, width, 'x');
    near(y, height / 4, height, 'y');
    return off.length ? geometry + ': ' + off.join(', ') : 'ok';
  };
  const box = (el) => el.getBoundingClientRect();
  const close = (a, b) => Math.abs(a - b) < 0.5;
  // "ok" if the image comment frame shrink-wraps the image (which is what
  // makes a fraction of the frame a fraction of the image) and the whole
  // image is reachable without scrolling in a viewport viewportHeight tall,
  // else what is off.
  window.shrinkWraps = (viewportHeight) => {
    const frame = box(document.querySelector('.image-comment-overlay .image-comment-frame'));
    const img = box(document.querySelector('.image-comment-overlay .image-comment-img'));
    const off = [];
    for (const k of ['x', 'y', 'width', 'height']) {
      if (!close(frame[k], img[k])) off.push('frame ' + k + ' ' + frame[k] + ' vs image ' + img[k]);
    }
    if (img.height > viewportHeight) off.push('image height ' + img.height + ' > ' + viewportHeight);
    return off.length ? off.join(', ') : 'ok';
  };
  // "ok" if the first image matching selector, of a width x height source,
  // fits the conversation, else what is off.
  window.fits = (selector, width, height) => {
    const el = document.querySelector(selector);
    if (!el) return 'no ' + selector;
    const port = document.querySelector('.messages-container');
    const link = el.closest('.commentable-image-link');
    const img = box(el), portBox = box(port), linkBox = box(link);
    const badge = box(link.querySelector('.commentable-image-badge'));
    const what = width + 'x' + height;
    const off = [];
    if (img.x + img.width > portBox.x + portBox.width) off.push(what + ' does not fit the column');
    if (img.height > port.clientHeight) off.push(what + ' does not fit the message area');
    if (!close(img.height, img.width * height / width)) off.push(what + ' does not keep its aspect ratio');
    // The badge is positioned against the link, so the link has to shrink
    // with the image rather than keep its natural width.
    if (!close(linkBox.width, img.width)) off.push(what + ' link does not hug the image');
    if (badge.x + badge.width > img.x + img.width) off.push(what + ' badge is off the image');
    return off.length ? off.join(', ') : 'ok';
  };
})();`

const imageTerms = `"The overlay" is ".image-comment-overlay"; "the frame" is ".image-comment-overlay .image-comment-frame" (it shrink-wraps the image, so its box is the image's displayed pixels and a fraction of it is a fraction of the image). "Drag from (a, b) to (c, d)" means: move the mouse over the frame at offsets (a × its width, b × its height) from its top-left, press the left button, move to (c × width, d × height) in N steps (5 unless said otherwise), and release; read the frame's box with getBoundingClientRect when computing the offsets. "Drag the middle half" is a drag from (0.25, 0.25) to (0.75, 0.75). The open comment dialog has its title ".diff-viewer-comment-dialog-handle" (it names the region being commented on), its input ".diff-viewer-comment-input" and its button "Add Comment" (name exactly that). window.geometry() returns the WxH+X+Y region the dialog title names; window.middleHalf(W, H) returns "ok" if that region is the middle half of a WxH image, within tolerance, and otherwise what is off; window.keepGeometry() keeps the current geometry for later, and window.kept(i) returns the i-th one kept (from 0). Check the message input's (data-testid "message-input") value with an eval comparing it to the exact string, building it with window.kept(i) where it says G or Gi; \n is a newline. `

// imageConversation creates the "inline image" conversation, slug inline,
// whose 48x48 shelley-inline-image-demo.png the agent wrote into the cwd.
func (s *shelley) imageConversation() {
	s.initScript(imageCommentJS)
	s.conversation("inline image", "inline")
}

// readImage writes data to name in a scratch directory and has a conversation,
// slug image, read it with read_image from there; {{IMAGE}} is its path.
func (s *shelley) readImage(name string, data []byte) string {
	s.initScript(imageCommentJS)
	dir := s.mkdir(filepath.Join(s.dir, "scratch"))
	path := filepath.Join(dir, name)
	s.check(os.WriteFile(path, data, 0o644))
	s.v("IMAGE", path)
	s.conversationWith(map[string]any{"message": "read_image: " + path, "model": "predictable", "cwd": dir}, "image")
	return dir
}

const inlineImage = `Navigate to /c/inline. Wait (up to 30s) for the image ".markdown-content img[src*=\"shelley-inline-image-demo.png\"]" to be visible. `

func TestE2EImageCommentsDragRegion(t *testing.T) {
	e2e(t, `Drag a region, comment, and it lands in the message input. `+imageTerms+`Perform the steps in order.
1. `+inlineImage+`Click it.
2. The overlay should be visible, and its ".image-comment-title" should have text exactly "shelley-inline-image-demo.png". Drawing is inert until the image reports its intrinsic size, so wait for the overlay's ".image-comment-hint" to contain "Drag a box".
3. Drag the middle half.
4. Releasing the drag opens the comment dialog for that region, named in the 48x48 image's own pixels: the dialog's input should be focused (document.activeElement), window.middleHalf(48, 48) should return exactly "ok". Keep the geometry (G0).
5. Fill the dialog's input with "this corner is wrong" and click "Add Comment".
6. The comment goes straight into the message input, and the view stays open so more of the same image can be commented on: there should be no dialog input (count 0); the overlay should still be visible; its ".image-comment-sent" should contain "1 comment added". The region stays drawn and numbered, so it is clear what has been covered: exactly 1 ".image-comment-overlay .image-comment-region", and ".image-comment-overlay .image-comment-region-badge" with text exactly "1". The message input's value should be exactly "> image shelley-inline-image-demo.png [region G0 of 48x48]\nthis corner is wrong\n\n".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.imageConversation()
	})
}

func TestE2EImageCommentsInsertedAsWritten(t *testing.T) {
	e2e(t, `Each comment is inserted as it is written, without a staging step. `+imageTerms+`Perform the steps in order.
1. `+inlineImage+`Click it. The overlay should be visible, and its ".image-comment-hint" should contain "Drag a box".
2. Three regions in a row. Each one's comment appends to the message input as soon as it is written, so the composer is up to date throughout rather than at the end. For each (i, text, drag) of (0, "top left", from (0.05, 0.05) to (0.3, 0.3)), (1, "middle", from (0.4, 0.4) to (0.6, 0.6)), (2, "bottom right", from (0.7, 0.7) to (0.95, 0.95)), in order: make the drag in 8 steps; keep the geometry (Gi) once the dialog shows it (window.geometry() non-empty); fill the dialog's input with the text; click "Add Comment"; the message input's value should then be exactly the comments so far, each "> image shelley-inline-image-demo.png [region Gi of 48x48]\n<text>\n\n", concatenated in order; every commented region stays drawn and numbered in order: there should be exactly i + 1 ".image-comment-overlay .image-comment-region", and the (i+1)-th ".image-comment-overlay .image-comment-region-badge" should have text exactly i + 1.
3. Focus must not follow the injected text into the composer behind the still-open view, or the next keystroke would go to the wrong place: the message input must not be focused, and ".image-comment-overlay .image-comment-container" should be (document.activeElement).
4. A whole-image comment appends the same way and draws no region: click the overlay's button "Comment on the whole image", fill the dialog's input with "and overall" and click "Add Comment". The message input's value should be exactly the three region comments followed by "> image shelley-inline-image-demo.png [whole image, 48x48]\nand overall\n\n". There should still be exactly 3 regions, and the overlay's ".image-comment-sent" should contain "4 comments added".
5. Only closing dismisses the view: click the overlay's button "Close image comments". The overlay should be gone (count 0), and the message input's value unchanged (exactly the four comments).`, func(s *shelley) {
		s.viewport(1280, 800)
		s.imageConversation()
	})
}

func TestE2EImageCommentsWholeImageAndEscape(t *testing.T) {
	e2e(t, `Comment on the whole image, and Escape discards. `+imageTerms+`Perform the steps in order.
1. `+inlineImage+`Click it. The overlay should be visible.
2. Escape backs out of the open comment first, leaving the view up: the text being abandoned is the thing Escape is most likely meant for. Click the overlay's button "Comment on the whole image", fill the dialog's input with "abandoned", and press Escape. There should be no dialog input (count 0), the overlay should still be visible, and the message input's value should be exactly "".
3. A second Escape closes the view, still without touching the composer: press Escape. The overlay should be gone (count 0), and the message input's value still exactly "".
4. Markdown images are keyboard-reachable: Enter on a focused image opens the view, same as a click. Focus the image (a one-shot eval calling its focus()) and press Enter. The overlay should be visible.
5. A whole-image comment says so in the dialog title, and in the header it writes, instead of naming a region: click "Comment on the whole image"; the dialog's title should contain "whole image". Fill the input with "looks good overall" and click "Add Comment". The message input's value should be exactly "> image shelley-inline-image-demo.png [whole image, 48x48]\nlooks good overall\n\n".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.imageConversation()
	})
}

func TestE2EImageCommentsScreenshot(t *testing.T) {
	e2e(t, `Screenshot tool images are commentable and reference the file on disk. `+imageTerms+`Perform the steps in order.
1. Navigate to /c/shot. Wait (up to 30s) for the first ".screenshot-tool .commentable-image" to be visible.
2. The affordance that says the image is clickable at all: this browser emulates a Pixel 5, so (hover: none) applies and the badge is always shown. The first ".screenshot-tool .commentable-image-badge" should have computed opacity exactly "1".
3. Click the image. The overlay should be visible. The header names the screenshot's on-disk path (Display.path), which the agent can crop, rather than the image endpoint URL: the overlay's ".image-comment-title" text should end with ".png".
4. Click the overlay's button "Comment on the whole image", fill the dialog's input with "check the header" and click "Add Comment".
5. The message input's value should match the regular expression /> image \/tmp\/shelley-screenshots\/[^\s]+\.png \[whole image, \d+x\d+\]\ncheck the header/ (eval regex.test(value), expect true).`, func(s *shelley) {
		s.viewport(1280, 800)
		s.initScript(imageCommentJS)
		s.conversation("screenshot", "shot")
	})
}

const readImage = `Navigate to /c/image. Wait (up to 30s) for the first ".screenshot-tool .commentable-image" (the image) to be visible. `

func TestE2EImageCommentsSourcePixels(t *testing.T) {
	// read_image downscales images that exceed the model's limits, so the
	// rendered image is smaller than the file the comment header names. A
	// region has to be scaled back into the file's coordinates, or the crop
	// the agent runs lands somewhere else entirely.
	// 3001x60, over predictable's 2000px limit and lopsided enough that the
	// downscale's whole-pixel rounding shifts the aspect ratio by 2.5% (it
	// becomes 2000x39). Nothing may key off that drift: only the scale differs,
	// and the region still has to be reported in the source's own space.
	e2e(t, `Regions are reported in the source file's pixels, not the downscaled copy's: the conversation read a 3001x60 PNG, {{IMAGE}}, with read_image, which serves a downscaled copy. `+imageTerms+`Perform the steps in order.
1. `+readImage+`
2. The served copy really is downscaled, and did load; otherwise this test proves nothing (predictable's MaxImageDimension is 2000): the image's naturalWidth should come (within 15s) to be greater than 0, and should then be less than 3001.
3. Click the image. The overlay's ".image-comment-hint" should contain "Drag a box".
4. Drag the middle half. Reported in the 3001x60 source's pixels, not the rendered copy's: window.middleHalf(3001, 60) should return exactly "ok". Keep the geometry (G0).
5. Fill the dialog's input with "scaled correctly" and click "Add Comment". The message input's value should be exactly "> image {{IMAGE}} [region G0 of 3001x60]\nscaled correctly\n\n".`, func(s *shelley) {
		s.viewport(1280, 800)
		s.readImage("wide.png", makePNG(3001, 60))
	})
}

// A portrait image is constrained by max-height, not width, which changes the
// displayed size the drag is measured against; that must not change the
// geometry that comes out. Desktop is not a formality here: the two viewports
// take different max-height rules, and a rule that clamped the frame while
// letting the image overflow it made everything below the fold silently
// unclickable on desktop only.
func tallImage(t *testing.T, name string, width, height int) {
	e2e(t, `A tall image on `+name+` viewport (`+strconv.Itoa(width)+`x`+strconv.Itoa(height)+`) still maps regions correctly: the conversation read a 400x1600 PNG with read_image. `+imageTerms+`Perform the steps in order.
1. `+readImage+`Click it.
2. The hint that gates other tests is hidden at phone width, so wait on the whole-image button instead: it is disabled until the image reports its size, which is also when drawing becomes live. The overlay's button "Comment on the whole image" should be enabled.
3. The frame shrink-wraps the image, which is what makes a fraction of the frame a fraction of the image; and the whole image is reachable without scrolling, so every part of it can be dragged on: window.shrinkWraps(`+strconv.Itoa(height)+`) should return exactly "ok".
4. Drag the middle half. window.middleHalf(400, 1600) should return exactly "ok".`, func(s *shelley) {
		s.viewport(width, height)
		s.readImage("tall.png", makePNG(400, 1600))
	})
}

func TestE2EImageCommentsTallImagePhone(t *testing.T) {
	tallImage(t, "a phone", 390, 844)
}

func TestE2EImageCommentsTallImageDesktop(t *testing.T) {
	tallImage(t, "desktop", 1280, 800)
}

// Images render at their intrinsic size unless styled, and a browser-tool
// screenshot taken at a phone's device scale factor is 1170x2532: wider than
// the message column and several screens tall. Every conversation image has
// to fit the column and the message area, keep its aspect ratio, and keep the
// comment badge on its own corner. The tall fixture is taller still, so it
// is over the height cap on both viewports.
func imagesFit(t *testing.T, name string, width, height int) {
	e2e(t, `Images fit the conversation on `+name+` viewport (`+strconv.Itoa(width)+`x`+strconv.Itoa(height)+`). Markdown images carry no size; tool cards pass width/height attributes (of the downscaled copy) to reserve their space. /c/markdown shows a 1170x4000 tall.png and a 3000x200 wide.png as markdown images; /c/image read the same tall.png with read_image. window.fits(selector, W, H) returns "ok" if the first image matching selector, of a WxH source, fits the conversation: it fits the column (".messages-container") and the message area (the container's clientHeight), keeps its aspect ratio (within half a pixel), its ".commentable-image-link" hugs it (width within half a pixel), and the link's ".commentable-image-badge" is on the image; otherwise it says what is off. "Check that S of WxH fits" means: wait (up to 30s) for the first image matching S to be visible; wait (up to 15s) for its naturalWidth to be greater than 0, since before the bytes arrive a markdown image has no size; then window.fits(S, W, H) should return exactly "ok". Perform the steps in order.
1. Navigate to /c/markdown. Check that ".markdown-content img[src*=\"tall.png\"]" of 1170x4000 fits, then that ".markdown-content img[src*=\"wide.png\"]" of 3000x200 fits.
2. Navigate to /c/image. Check that ".screenshot-tool .commentable-image" of 1170x4000 fits.`, func(s *shelley) {
		s.viewport(width, height)
		dir := s.readImage("tall.png", makePNG(1170, 4000))
		s.check(os.WriteFile(filepath.Join(dir, "wide.png"), makePNG(3000, 200), 0o644))
		s.conversationWith(map[string]any{"message": "echo: ![tall](tall.png) ![wide](wide.png)", "model": "predictable", "cwd": dir}, "markdown")
	})
}

func TestE2EImageCommentsFitPhone(t *testing.T) {
	imagesFit(t, "a phone", 390, 844)
}

func TestE2EImageCommentsFitDesktop(t *testing.T) {
	imagesFit(t, "desktop", 1280, 800)
}

func TestE2EImageCommentsTouchDrag(t *testing.T) {
	// Touch is the drag path most likely to break: it is the one with pointer
	// capture, cancellation, and more than one pointer in flight. The browser
	// emulates a touch device, so drive real touch events.
	e2e(t, `A touch drag draws a box, and a second finger cannot hijack it: the conversation read an 800x600 PNG with read_image. `+imageTerms+`Touch points below are fractions (a, b) of the frame: offsets (a × its width, b × its height) from its top-left; touch on the frame. "The draft is half the frame wide" means: in an eval, wait two animation frames (one for Vue to flush the style, one for layout: new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))), then check that the width of ".image-comment-overlay .image-comment-draft" is within 0.5px of half the frame's width (getBoundingClientRect), expect true. Perform the steps in order.
1. `+readImage+`Tap it (touch start on it, then touch end).
2. The overlay's button "Comment on the whole image" should be enabled.
3. Finger one drags the middle half: touch start at (0.25, 0.25), then touch move to (0.75, 0.75). The draft should be half the frame wide (percentage layout plus coordinate quantization put this within a pixel; a hijacked drag would be off by a third of the image).
4. Finger two arrives mid-drag and wanders to the far corner. It gets its own pointerId, so the box in flight must not follow it -- if it did, the box would shrink back toward that corner: touch start with two fingers at (0.75, 0.75) and (0.05, 0.95), then touch move with them at (0.75, 0.75) and (0.02, 0.98). The draft should still be half the frame wide.
5. Lift the fingers (touch end).
6. window.middleHalf(800, 600) should return exactly "ok". And the drag really is over: no draft box is left hanging (".image-comment-overlay .image-comment-draft" count 0).`, func(s *shelley) {
		s.viewport(1280, 800)
		s.readImage("touch.png", makePNG(800, 600))
	})
}

func TestE2EImageCommentsRotatedJPEG(t *testing.T) {
	// Browsers apply EXIF orientation; a raw pixel read does not. So the tool
	// reports source dimensions already corrected for it (100x50 stored, 50x100
	// as everyone sees it), and the comment carries the instruction to
	// auto-orient -- croppers like `magick` ignore the tag too, and without it
	// the crop would land on a different part of the picture.
	e2e(t, `A rotated JPEG is commented on in the pixels that were rendered: the conversation read {{IMAGE}}, a 100x50 JPEG tagged to be rotated 90° for display, with read_image. `+imageTerms+`Perform the steps in order.
1. `+readImage+`
2. The premise: the browser really does rotate it. Without this the test would pass on a build that never had the bug to fix. The image's naturalWidth should come (within 15s) to be exactly 50, and its naturalHeight should then be exactly 100.
3. Click the image. The overlay's ".image-comment-hint" should contain "Drag a box".
4. Drag the middle half. The middle half of the image as displayed, in the displayed image's own 50x100 space -- not 100x50, which would describe a different region: window.middleHalf(50, 100) should return exactly "ok". Keep the geometry (G0).
5. Fill the dialog's input with "rotated" and click "Add Comment". The message input's value should be exactly "> image {{IMAGE}} [region G0 of 50x100, auto-orient first]\nrotated\n\n".`, func(s *shelley) {
		s.viewport(1280, 800)
		jpeg, err := base64.StdEncoding.DecodeString(rotatedJPEG100x50)
		s.check(err)
		s.readImage("rotated.jpg", jpeg)
	})
}

// The default device is a phone, where (hover: none) keeps the badge visible;
// a mouse-capable browser is the only place the reveal is testable.
func TestE2EImageCommentsBadgeOnHover(t *testing.T) {
	e2e(t, `With a mouse, the comment badge appears on hover. Perform the steps in order.
1. `+inlineImage+`
2. The first ".markdown-content .commentable-image-badge" (the badge) should have computed opacity exactly "0".
3. Hover the image. The badge's computed opacity should become exactly "1".`, func(s *shelley) {
		s.device(lazycue.Device{Width: 1280, Height: 800, Scale: 2.75})
		s.imageConversation()
	})
}
