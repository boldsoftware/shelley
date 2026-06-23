# Shelley LazyCue tests

Self-healing browser tests for the Shelley UI, powered by
[LazyCue](../../lazycue). Each test is a plain-English description living in a Go
test function (`shelley/test/lazycue_test.go`). At run time:

- If a cached DSL script exists (in `.lazycue/` next to this file), it
  executes directly — fast, no LLM.
- On a cache miss or a mechanical failure, an LLM agent generates or heals
  the DSL, then writes the result to `.lazycue/`.
- A genuine mismatch between the app and the description fails the test.

The cache files in `.lazycue/` are tracked in git and committed like any
other source. They are clearly marked as machine-managed — don't hand-edit
them; edit the test description instead.

### Two worlds: `.lazycue/vue/` and `.lazycue/react/`

Shelley ships two frontends ("worlds") from one binary, selected by the
`vue-ui` feature flag. The lazycue suite runs once per world (CI sets
`LAZYCUE_WORLD=vue|react`), and each world has its **own** cache subdir.
The cache key is the description hash, which is identical across worlds, so a
shared dir would let a heal in one world silently overwrite the other's cached
script. Keeping them separate means each world's DSL is healed and committed
independently — the worlds can diverge without healing each other.

## Adding a test

Add a Go test function in `shelley/test/lazycue_test.go` that calls
`lazyTest(t, "<description>")`. Be specific about selectors (prefer
`data-testid`), expected text, and expected states. The description is the
source of truth; if the app diverges from it, the test fails.

## Running locally

The tests run as ordinary Go integration tests in `shelley/test`. `TestMain`
boots one in-process, predictable-mode Shelley server shared by all of them,
and each `TestNewPage*` drives its description through a package-level
`lazycue.Harness` — no separate server process and no `lazycue` binary. They're
gated behind an env var so they stay out of the default `go test ./...` path:

```bash
cd shelley/ui && pnpm run build && cd ..   # the server embeds ui/dist
LAZYCUE_INTEGRATION=1 \
  LAZYCUE_ARTIFACT_DIR=/tmp/lazycue-artifacts \
  LAZYCUE_SUMMARY=/tmp/lazycue-summary.json \
  go test ./test/ -run TestNewPage -count=1 -v
```

The cache lives in `ui/lazycue/.lazycue/`. The LLM is only needed on a cache
miss. Point `ANTHROPIC_BASE_URL` / `ANTHROPIC_API_KEY` at any
Anthropic-compatible endpoint.

## CI

`.buildkite/steps/test-shelley-lazycue.sh` builds the UI, then runs the
`TestNewPage*` tests and uploads `lazycue-artifacts/` (per-step screenshots +
`index.html` report) and `lazycue-summary.json` as artifacts. The summary
makes the cache hit/miss breakdown explicit. On a green queue build, any new
or healed cache files in `.lazycue/` are written as a git patch to a directory
shared between steps on the agent; the rebase-and-push step applies and commits
that patch onto main (like auto-formatting) so later builds hit the fast path.
