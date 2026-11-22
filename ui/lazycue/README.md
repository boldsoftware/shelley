# Shelley LazyCue tests

Self-healing browser tests for the Shelley UI, powered by
[LazyCue](../../lazycue). Each test is a plain-English description living in a Go
test function in `shelley/test` (`lazycue_test.go` and the `e2e_*_test.go`
files). At run time:

- If a cached DSL script exists (in `.lazycue/` next to this file), it
  executes directly — fast, no LLM.
- On a cache miss or a mechanical failure, an LLM agent generates or heals
  the DSL, then writes the result to `.lazycue/`.
- A genuine mismatch between the app and the description fails the test.

The cache files in `.lazycue/` are tracked in git and committed like any
other source. They are clearly marked as machine-managed — don't hand-edit
them; edit the test description instead.

## Adding a test

Most tests are `TestE2E*` functions in `shelley/test/e2e_*_test.go` that call
`e2e(t, useful(0), "<description>", setup)`. Each attempt at one runs against a shelley
of its own (a fresh database, home and git working directory), which `setup`
prepares: conversations, files, mocked requests, an init script for the page.
The simpler `TestNewPage*` functions in `lazycue_test.go` call
`lazyTest(t, useful(0), "<description>")` and share one server (`lazyTestIsolated` gives
one its own; `lazyRecordingTest` injects
`shelley/test/testdata/recording-mock.js` before the application loads).

Be specific about selectors (prefer `data-testid`), expected text, and
expected states. The description is the source of truth; if the app diverges
from it, the test fails.

`useful(N)` is how many times the test has been useful: how many of its
failures caught a real bug, rather than a flake or an intended change the test
had to follow. A new test starts at `useful(0)`, in its Test function (helpers
between take a `useful lazycue.Useful` and pass it on, so each test keeps its
own). A failing test's error says where its count is; **if the failure caught
a real bug, even one in your own uncommitted change, increment N in the commit
that fixes it**, for every test that caught it. That is how we find out
whether these tests earn their keep. To total them:

```bash
grep -rhoE '\buseful\([0-9]+\)' shelley/test | grep -oE '[0-9]+' | awk '{s+=$1} END {print s+0}'
```

## Running locally

The tests run as ordinary Go integration tests in `shelley/test`, with no
`lazycue` binary. `TestMain` boots one in-process, predictable-mode Shelley
server for the `TestNewPage*` tests; the `TestE2E*` tests start a shelley
binary, which the package builds once, for every attempt. They're gated
behind an env var so they stay out of the default `go test ./...` path, and
need a Chromium-family browser on PATH (see [LazyCue](../../lazycue)):

```bash
cd shelley && make build   # the server embeds ui/dist, the templates and exe-scroll
LAZYCUE_INTEGRATION=1 \
  LAZYCUE_ARTIFACT_DIR=/tmp/lazycue-artifacts \
  LAZYCUE_SUMMARY=/tmp/lazycue-summary.json \
  go test ./test/ -run 'TestNewPage|TestE2E' -count=1 -v -timeout 60m
```

Narrow `-run` to run some of them. The cache lives in `ui/lazycue/.lazycue/`.
The LLM is only needed on a cache miss or a heal; `LAZYCUE_MAX_AGENT_RUNS=0`
runs from the cache alone. Point `ANTHROPIC_BASE_URL` / `ANTHROPIC_API_KEY`
at any Anthropic-compatible endpoint.

## CI

`.buildkite/steps/test-shelley-lazycue.sh` builds the UI, then runs every
`TestNewPage*` and `TestE2E*` test and uploads `lazycue-summary.json`, and
`lazycue-artifacts/` (per-step screenshots + `index.html` report) when a test
failed, healed or was generated. The summary makes the cache hit/miss
breakdown explicit. On a green queue build, any new or healed cache files in
`.lazycue/` are handed to the rebase-and-push step, which commits them onto
main (like auto-formatting) so later builds hit the fast path.
