# LazyCue

We may want Playwright/Selenium/Cypress/etc. tests, but we don't want
to maintain them. If we're being honest, we don't want to write them
either.

LazyCue is an experiment in "self-healing" browser automation tests. The tests
themselves are free form instructions, and an agent, at run time, interprets
those instructions. Then, the interpreted instructions are cached, and these
cached instructions are re-used over and over again. When that eventually
fails, an agent is invoked again to fix them up.

Using this requires that we, as an industry, get more comfortable with CI
editing our commits for us. For example, if you have a merge queue, and you
submit a change that passes tests but has some trailing whitespace, the Right
Answer is for CI to fix the formatting in a new (or amended) commit, and submit
that change, even though it isn't quite the same thing as you pushed. LazyCue
takes this to the next level: let's not pretend we'd be doing anything but
mechanically curing the failing test. Materializing the cache in-repo vs. a
separate system (a database, a cache server, elsewhere in Git) is the choice
we've made here.

## Whence the name?

Some of the products in this space (Playwright, Puppetteer, Stagehand) have theatrical
names. So, here we are, with the LLM cueing the browser on what to do. The space is
surprisingly dense with names!

The lazy is self-congratulatory.

## Prerequisites

LazyCue uses the chromedp package to talk to a Chromium-based browser.
On Linux, [Headless Shell](https://hub.docker.com/r/chromedp/headless-shell/) is good, and you
can extract it like so.

```
sudo mkdir -p /headless-shell && go run github.com/google/go-containerregistry/cmd/crane@latest export chromedp/headless-shell:latest - | sudo tar -x -C /headless-shell
```

## Quick Start

```
$go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest   --base-url http://xkcd.com/   'Navigate to / and check there is a comic published'
PASS  [generated → v1]  26.015s total, 24.793s agent
  Navigate to / and check theres a comic published
  ✓ navigate /                                           39ms
  ✓ wait_visible #comic                                   2ms
  ✓ assert_visible #comic img                              0s
  ✓ assert_visible #middleContainer                       1ms
  ✓ wait_text Permanent link to this comic:                0s
  ✓ assert_visible a[href*='xkcd.com']                     0s
  ⚡ 26,242 in / 1,032 out tokens  ~$0.094

$go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest   --base-url http://xkcd.com/   'Navigate to / and check there is a comic published'
PASS  [cached v1]  1.317s
  Navigate to / and check theres a comic published
  ✓ navigate /                                           72ms
  ✓ wait_visible #comic                                   1ms
  ✓ assert_visible #comic img                             1ms
  ✓ assert_visible #middleContainer                        0s
  ✓ wait_text Permanent link to this comic:               1ms
  ✓ assert_visible a[href*='xkcd.com']                     0s
```

Or from Go tests:

```go
var app = lazycue.New(lazycue.Options{BaseURL: "http://localhost:3000"})

func TestHomepage(t *testing.T) {
    app.Test(t, `Navigate to / and verify the page title is "My App". The login button should be visible.`)
}
```

## Workflow of a Run

1. Hash description → look up `.lazycue/<hash>.json` next to your tests
2. If cached: execute DSL steps. If passes → done (fast path, ~1-2s)
3. If cached but fails mechanically: spawn LLM agent to fix → save new version
4. If cached but app is genuinely broken: **fail the test with explanation**
5. If not cached: spawn LLM agent to generate → save v1

### DSL

Cached tests are stored as JSON as arrays of steps, for example:

```json
[
  {"action": "navigate", "url": "/"},
  {"action": "assert_title", "text": "My App"},
  {"action": "wait_visible", "selector": "#login-button", "timeout": "10s"},
  {"action": "click", "selector": "#login-button"},
  {"action": "wait_text", "text": "Welcome back", "timeout": "10s"}
]
```

### Self-Healing vs Genuine Failures

The agent distinguishes between:
- **Mechanical failures**: wrong selectors, timing issues, missing waits → self-heals
- **Genuine failures**: app doesn't match the description → fails with explanation

The test description is the source of truth. If the description says "title should be X"
and the app shows "Y", that's a genuine failure.

## Configuration

| Flag / Option | Default | Description |
|---------------|---------|-------------|
| `--base-url` | | App URL (**required**) |
| `--cache-dir` | `.lazycue` | Directory holding cache JSON files |
| `--artifact-dir` | | Write per-step screenshots + an HTML report (`index.html`) here |
| `--video-dir` | `LAZYCUE_VIDEO_DIR` | Render an MP4 per test (prompt title card + captioned screenshots) here |
| `--json` | | Write a machine-readable JSON cache-stats summary here |
| `--model` | `claude-sonnet-4-6` | LLM model |
| `--api-url` | `ANTHROPIC_BASE_URL` or `https://api.anthropic.com` | Anthropic API base URL |
| `--api-key` | `ANTHROPIC_API_KEY` | Anthropic API key |
| `--verbose` | false | Verbose output |
| `Options.Setup` | | Starts a fresh app for every attempt at a test (and every run of steps by the agent) and returns its `Env`: base URL, `{{NAME}}` placeholder values, host-side `call` functions and the browser's `Launch` settings (init script, device, CSS media, time zone; default a Pixel 5 with touch that prefers reduced motion) (Go harness only) |
| `LAZYCUE_MAX_AGENT_RUNS` | unlimited | Agent runs (generations and heals) a process may start; 0 runs cached scripts only |
| `LAZYCUE_AGENT_CONCURRENCY` | 4 | Agents running at once |

## Screenshots & Video Artifacts

LazyCue can capture a screenshot after every executed step of an attempt that
follows a failure (a retry, the agent's runs) and,
optionally, stitch them into a short MP4. A cached script that passes first
time has nothing to debug and takes none.

- `--artifact-dir DIR` writes per-step PNGs into `DIR/<prompt-hash>/` (so
  screenshots are arranged by prompt) plus an HTML report at `DIR/index.html`.
- `--video-dir DIR` (or `LAZYCUE_VIDEO_DIR`) renders `DIR/<prompt-hash>.mp4`.
  The video opens with a title card showing the prompt, then shows each
  screenshot in order with its step instruction overlaid and a PASS/FAIL badge.
  Setting `--video-dir` alone also captures the underlying screenshots (under
  `DIR/<prompt-hash>/`), so you don't need `--artifact-dir` too.

Video rendering shells out to `ffmpeg` (and `drawtext`, so a TrueType font is
required). The font is auto-detected from common locations; override it with
`LAZYCUE_FONT=/path/to/font.ttf`. If `ffmpeg` or a font is missing, the test
still runs — only the video is skipped.

Rendering happens *after* the tests finish, not in the per-test path: each MP4
is a non-trivial ffmpeg job, so rendering inline would inflate every test's
wall time (and its timeout budget). The Go harness renders in `TestMain` after
`m.Run()` (call `Harness.RenderVideos()`); the CLI renders after the run. Each
MP4 is produced by a single ffmpeg invocation (a `filter_complex` that draws
the overlays once per frame and concats them), and videos for different tests
render concurrently.

```
lazycue --base-url http://localhost:3000 --video-dir ./lazycue-video \
  'Navigate to /login, sign in, and verify the dashboard loads'
# ... writes ./lazycue-video/<hash>.mp4 and ./lazycue-video/<hash>/step-*.png
```

## How the Cache Works

Cached tests live in a `.lazycue/` directory next to your tests.
Add them to your repo to take advantage of the caching.

For CI, you'll want to create commits for them as part of your CI,
and push those commits along.
