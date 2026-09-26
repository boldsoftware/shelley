package test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	lazycue "github.com/boldsoftware/shelley/lazycue"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/server"
)

// LazyCue self-healing browser tests for the Shelley /new UI.
//
// Each test is a plain-English description run through a package-level
// lazycue.Harness. The harness hashes the description, looks up a cached DSL
// script in shelley/ui/lazycue/.lazycue/, and executes it without an LLM. On a
// cache miss or a mechanical failure it spawns an agent to generate/heal the
// script and writes it back to the cache. The descriptions are the source of
// truth: they live here, in the Go test, not in a separate data file.
//
// These need a real browser (headless-shell) and, on a cache miss/heal, an
// LLM. To keep the default `go test ./...` fast and hermetic they only run when
// LAZYCUE_INTEGRATION is set (the dedicated CI step sets it); they skip cleanly
// when no browser is available.
//
// TestMain boots one in-process predictable-mode Shelley server shared by all
// the tests, then (when artifacts are requested) writes an aggregate HTML
// report and JSON cache-stats summary. Optional env vars, set by CI to feed the
// reporting scripts:
//
//	LAZYCUE_ARTIFACT_DIR  write per-step screenshots + an HTML report here
//	LAZYCUE_VIDEO_DIR     render an MP4 per test (prompt + captioned screenshots) here
//	LAZYCUE_SUMMARY       write a machine-readable JSON cache-stats summary here

// app is the shared harness. Its BaseURL is filled in by TestMain once the
// server is listening.
var app *lazycue.Harness

func TestMain(m *testing.M) {
	// Resolve the LazyCue cache dir relative to this package before leaving
	// it for a temp working directory below.
	pkgDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	cacheDir := filepath.Join(pkgDir, "..", "ui", "lazycue", ".lazycue")

	// Run from a small temp dir, not the package dir. Conversations created
	// with an empty cwd fall back to os.Getwd(), and system-prompt generation
	// then walks the surrounding git repo (guidance-file + skills walks).
	// Inside a large monorepo checkout that walk is capped at 2s but still
	// costs hundreds of ms per conversation and floods go test's testlog,
	// slowing every LazyCue test for no coverage benefit.
	//
	// The working directory is tmp/cwd, which holds the @ file-completion
	// fixture (see setupCompletionFixture); tmp/elsewhere is a directory
	// outside it. Putting the fixture in the default cwd (rather than a
	// separate directory selected per test) keeps every conversation in one
	// place: /new inherits the newest conversation's cwd, so a different
	// directory would leak into all later tests.
	tmp, err := os.MkdirTemp("", "shelley-test-cwd-")
	if err != nil {
		panic(err)
	}
	cwd := filepath.Join(tmp, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		panic(err)
	}
	if err := os.Chdir(cwd); err != nil {
		panic(err)
	}
	// Note: os.Exit below skips deferred calls; remove tmp explicitly on
	// each exit path.

	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		// Tests below all skip; run them so `go test` reports them as skipped.
		code := m.Run()
		os.RemoveAll(tmp)
		os.Exit(code)
	}

	ts, cleanup := startPredictableServer()
	defer cleanup()

	// Prepare a deterministic git fixture for the diff-viewer test. The path
	// must be stable so the LazyCue description (which embeds it) hashes to the
	// same cache key across runs.
	if err := setupDiffFixtureRepo(diffFixtureDir); err != nil {
		panic(err)
	}
	defer os.RemoveAll(diffFixtureDir)
	if err := setupCompletionFixture(cwd, filepath.Join(tmp, "elsewhere")); err != nil {
		panic(err)
	}

	app = lazycue.New(lazycue.Options{
		BaseURL:     ts.URL,
		CacheDir:    cacheDir,
		Verbose:     true,
		ArtifactDir: os.Getenv("LAZYCUE_ARTIFACT_DIR"),
		VideoDir:    os.Getenv("LAZYCUE_VIDEO_DIR"),
	})

	code := m.Run()

	// Render per-test MP4s (out of the per-test hot path; concurrent). Do this
	// before WriteReport/WriteSummary so the report embeds the videos and the
	// summary records their paths. Best-effort.
	if err := app.RenderVideos(); err != nil {
		slog.Warn("lazycue: render videos", "error", err)
	}

	// Emit the reporting artifacts CI surfaces (HTML report + JSON summary).
	results := app.Results()
	if dir := os.Getenv("LAZYCUE_ARTIFACT_DIR"); dir != "" && len(results) > 0 {
		if err := lazycue.WriteReport(dir, results); err != nil {
			slog.Warn("lazycue: write report", "error", err)
		}
	}
	if path := os.Getenv("LAZYCUE_SUMMARY"); path != "" && len(results) > 0 {
		if err := lazycue.WriteSummary(path, results); err != nil {
			slog.Warn("lazycue: write summary", "error", err)
		}
	}

	os.RemoveAll(tmp)
	os.Exit(code)
}

func lazyTest(t *testing.T, description string) {
	t.Helper()
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run the LazyCue browser integration tests")
	}
	// Each lazycue run gets its own browser and its own conversation(s), so
	// the tests are independent; run them in parallel to cut suite wall time.
	// CI bounds concurrency with -parallel (browser instances are ~1 CPU
	// each). Tests that assert against the SHARED conversation list (ordering
	// or adjacency of other tests' conversations) must use lazyTestSerial.
	t.Parallel()
	app.Test(t, description)
}

// lazyTestSerial is lazyTest without t.Parallel, for descriptions that make
// assumptions about the global conversation list (e.g. "the conversation
// immediately below"). Go runs all serial tests before releasing the paused
// parallel ones, so a serial test never overlaps another lazycue test.
func lazyTestSerial(t *testing.T, description string) {
	t.Helper()
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run the LazyCue browser integration tests")
	}
	app.Test(t, description)
}

func TestNewPageSmoke(t *testing.T) {
	lazyTest(t, `Navigate to /new. The page title should be "Shelley Agent". The message input (a textarea with data-testid "message-input") should be visible and initially empty, and the send button (data-testid "send-button") should be visible but disabled while the input is empty.`)
}

func TestNewPageAccessibility(t *testing.T) {
	lazyTest(t, `Navigate to /new. The message input (data-testid "message-input") should have an aria-label of "Message input" and a non-empty placeholder attribute. The send button (data-testid "send-button") should have an aria-label of "Send message".`)
}

func TestNewPageSendEnables(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text "hello world" into the message input (data-testid "message-input"). After typing, the send button (data-testid "send-button") should become enabled.`)
}

// Regression test for the mobile "double UI" bug: on the Pixel 5 mobile
// viewport the harness uses, typing into the composer promotes the new
// conversation into a draft (the URL gains a "/c/" segment) while the
// model/effort/dir status block stays present. The block must render exactly
// once. A bug caused it to render twice for drafts on mobile — once in the
// standalone status bar and again inline in the composer's status slot.
func TestNewPageDraftStatusBarNotDuplicated(t *testing.T) {
	lazyTest(t, `Navigate to /new. There should be exactly one new-conversation status block (selector ".status-bar-new-conversation" should match exactly 1 element). Type "hello draft" into the message input (data-testid "message-input"). Typing promotes the conversation to a draft, so wait for the URL to contain "/c/". After that, add a short sleep (about 1 second) to let the UI settle, then confirm the new-conversation status block is NOT duplicated: selector ".status-bar-new-conversation" should still match exactly 1 element.`)
}

// Regression test for the iOS "keyboard closes after the first character" bug.
// Typing into the new-conversation composer lazily creates a draft conversation
// (~600ms after the first keystroke), which flips the app's conversationId from
// null to the new draft id. A bug made that flip run the conversation-switch
// load path on a cache miss, briefly setting loading=true and DISABLING the
// focused textarea. On iOS, disabling a focused element fires a blur event and
// dismisses the soft keyboard, forcing the user to tap the field again. We
// detect that symptom deterministically: focus the textarea, install a blur
// counter, type enough to materialize the draft, then assert the textarea never
// blurred, is still enabled, and is still the active element.
func TestNewPageDraftKeepsInputFocused(t *testing.T) {
	lazyTest(t, `Navigate to /new. Using an eval step, focus the message input and install a blur counter on it: run the JavaScript "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');el.focus();window.__blurCount=0;el.addEventListener('blur',function(){window.__blurCount++;});return document.activeElement===el;})()" and expect the result "true". Then type "hello there" into the message input (data-testid "message-input"). Typing lazily promotes the conversation to a draft, so wait for the URL to contain "/c/". After that, add a sleep of about 1.5 seconds to let the draft creation and any re-render settle. Then assert the textarea never lost focus during the draft creation: eval "window.__blurCount" and expect "0". Also assert the textarea is still enabled and focused: eval "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');return (!el.disabled)&&document.activeElement===el;})()" and expect "true".`)
}

// Regression test for the "draft opens to a forever spinner" bug. Selecting a
// draft conversation used to run the normal conversation-switch load path,
// which fires GET /api/conversation/<id> and shows a "Loading conversation…"
// spinner until it resolves. A draft has NO server-side messages (it only
// carries composer text), so nothing cleared the loading state for the empty
// result; if that fetch stalled (or a switch race tripped loadMessages'
// isCurrent() early-return), the spinner was stranded indefinitely. The fix
// short-circuits drafts so they never spin or hit the network. We reproduce the
// exact failure mode deterministically: install a fetch shim that makes the
// conversation-detail GET hang forever, then select the draft and assert it
// renders its composer immediately with no spinner.
//
// The shared test server accumulates drafts from the sibling draft tests, so we
// must reopen THIS test's own draft, not just "the first draft row" — hence we
// capture the draft's id from its URL and click the row carrying that
// data-conversation-id. Selecting a random sibling draft would open a
// conversation whose composer holds different text and fail the seed check.
func TestNewPageDraftOpensWithoutSpinner(t *testing.T) {
	lazyTest(t, `Reproduces the "open a draft, get a forever spinner" bug, which only surfaces on a COLD message cache. Perform these steps in order, exactly as described; do not add extra steps.
1. Navigate to /new.
2. Wait for the message input (data-testid "message-input") to be visible.
3. Fill the message input (data-testid "message-input") with the value "draft body text". This lazily creates a draft.
4. Wait for the URL to contain "/c/".
5. Eval: remember THIS draft's id (from its URL) so we can reopen exactly this draft later, since the shared server also holds other tests' drafts. Expression: "(function(){var m=location.pathname.match(/\/c\/(.+)$/);sessionStorage.setItem('draftSpinnerTargetId', m?m[1]:'');return m?'saved':'nourl';})()". Expect "saved".
6. Sleep about 1 second.
7. Navigate to /new.
8. Wait for the message input (data-testid "message-input") to be visible.
9. Eval: delete the message cache. Expression: "(function(){try{indexedDB.deleteDatabase('shelley-messages');}catch(e){}return 'cleared';})()". Expect "cleared".
10. Sleep about 1 second.
11. Navigate to /new (a fresh load with the now-empty cache).
12. Wait for the message input (data-testid "message-input") to be visible.
13. Eval: install a fetch shim so the conversation-detail GET hangs forever. Expression: "(function(){var o=window.fetch;window.fetch=function(u,opt){var s=(typeof u==='string')?u:u.url;if(/\/api\/conversation\/[^\/]+$/.test(s)&&(!opt||!opt.method||opt.method==='GET')){return new Promise(function(){});}return o(u,opt);};return 'stalled';})()". Expect "stalled".
14. Click the button with aria-label "Open conversations".
15. Wait for a draft row (selector ".conversation-title-draft") to be visible.
16. Eval: click THIS test's own draft row, matched by the id captured earlier, so a sibling test's draft in the shared list can't be opened by mistake. Expression: "(function(){var id=sessionStorage.getItem('draftSpinnerTargetId');var el=document.querySelector('.conversation-item[data-conversation-id=\"'+id+'\"]');if(!el)return 'notfound';el.click();return 'clicked';})()". Expect "clicked".
17. Sleep about 1.5 seconds.
18. Wait for the URL to contain "/c/" (the app should have switched to the draft).
19. Assert that selector ".spinner" matches 0 elements (no loading spinner, despite the stalled fetch).
20. Eval: confirm the composer is usable and seeded with the draft text. Expression: "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');return (!!el)&&(!el.disabled)&&el.value.indexOf('draft body text')>=0 ? 'true' : 'false';})()". Expect "true".`)
}

// --- Conversation tests (ported from ui/e2e/conversation.spec.ts) ---
//
// These drive the predictable LLM service through the real UI. The predictable
// service (llm/predictable/predictable.go) maps specific inputs to deterministic outputs:
//   - "Hello"            -> "Hello! I'm Shelley, your AI assistant. How can I help you today?"
//   - "hello"            -> "Well, hi there!"
//   - "echo: <text>"     -> echoes <text> back
//   - "bash: <cmd>"      -> bash tool call, agent text "I'll run the command: <cmd>"
//   - "think: <text>"    -> thinking content + "I've considered my approach."
//   - "patch: <file>"    -> patch tool call, agent text "I'll patch the file: <file>"
//   - "delay: <n>"       -> waits n seconds then "Delayed for <n> seconds"
//   - "error: <msg>"     -> surfaces an LLM error in the UI
//   - anything else      -> "edit predictable.go to add a response for that one..."

func TestNewPageHelloGreeting(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent's reply text "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear on the page. Both the sent "Hello" message and that reply should be visible.`)
}

func TestNewPageLowercaseHello(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "hello" (all lowercase) into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent's reply text "Well, hi there!" to appear on the page.`)
}

func TestNewPageEcho(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "echo: test message" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the echoed text "test message" to appear on the page. The sent message "echo: test message" should also be visible.`)
}

func TestNewPageSendWithEnter(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input"), then press the Enter key to send it (without clicking the send button). Wait for the agent's reply text "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear on the page.`)
}

func TestNewPageThinkingIndicator(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "delay: 2" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). While the agent is working, an element with data-testid "agent-thinking" should become visible. Then wait for the reply text "Delayed for 2 seconds" to appear, after which the "agent-thinking" element should no longer be visible.`)
}

func TestNewPageBashTool(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text bash: echo "hello world" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent text that begins with "I'll run the command:" and includes echo "hello world". A completed tool call (an element with data-testid "tool-call-completed") should become visible, and the text "bash" should be visible somewhere on the page.`)
}

// Replaces the Playwright test that treated predictable's "think:" response
// as a tool call and reused whichever shared conversation happened to be open.
func TestNewPageConsecutiveToolCalls(t *testing.T) {
	lazyTest(t, `Navigate to /new and confirm selector "[data-testid='tool-call-completed']" initially matches exactly 0 elements. Send three consecutive tool turns in this same conversation. For the first turn, type the text bash: echo "first command" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for exactly one completed tool call, then wait for the turn-ending agent message by evaluating "Array.from(document.querySelectorAll('.message-agent')).filter(function(el){return el.textContent.trim()==='Done.';}).length" and expecting "1"; this eval step must set timeout "30s". For the second turn, type the text bash: echo "second command" into the same message input and click the send button; wait for exactly two completed tool calls, then run the same eval and expect "2" with timeout "30s". For the third turn, type the text bash: echo "third command" into the same message input and click the send button; wait for exactly three completed tool calls, then run the same eval and expect "3" with timeout "30s". Finally, selector ".bash-tool[data-testid='tool-call-completed']" should match exactly 3 elements, selector ".bash-tool-command" should match exactly 3 elements, and selector ".bash-tool-header .tool-status-icon" should match exactly 0 elements. Evaluate "Array.from(document.querySelectorAll('.bash-tool-header')).every(function(el){return !el.textContent.includes('✓')&&!el.textContent.includes('✗');})" expecting "true". Evaluate "Array.from(document.querySelectorAll('.bash-tool-command')).map(function(el){return el.textContent.trim();}).join('|')" expecting "echo \"first command\"|echo \"second command\"|echo \"third command\"". The three sent user messages should all remain visible.`)
}

// Regression test for tool-progress render churn. A running tool reports
// partial output every ~500ms (claudetool/bash.go progressInterval); each
// report used to replace ChatInterface's toolProgress object, which was
// passed as a PROP to every rendered message component, re-rendering all of
// them per event (~800 updates/event in a 250-turn conversation, one ~80ms
// main-thread block per event for the life of the tool). Streaming output is
// now injected per tool call (ui/src/vue/composables/toolProgress.ts), so a
// progress event re-renders only the running tool's card. The UI counts
// component updates at window.__shelleyPerf (ui/src/utils/perf.ts); we watch
// a slow bash command stream its output and assert Message components stay
// quiet while progress events arrive.
func TestToolProgressDoesNotRerenderMessages(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "echo: warmup one" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"), then wait for the reply text "warmup one" to appear. Then type the text bash: for i in 1 2 3 4 5 6 7 8; do echo tick $i; sleep 1; done into the message input and click the send button. The bash tool card streams the command's output while it runs, so wait for the text "tick 2" to appear on the page (allow up to 30 seconds). Then reset the UI's recomputation counters: eval "(function(){window.__shelleyPerf.reset();return true;})()" and expect "true". Wait for at least 4 tool-progress events to arrive: eval "(function(){var s=window.__shelleyPerf.snapshot();return (((s['store.notifyTransient']||{}).count)||0)>=4;})()" with expect "true" (allow up to 15 seconds). Then assert the progress events did not re-render the conversation's message components: eval "(function(){var s=window.__shelleyPerf.snapshot();var prog=((s['store.notifyTransient']||{}).count)||0;var upd=((s['message.update']||{}).count)||0;return upd<=5?'pass':'fail: '+upd+' message updates during '+prog+' progress events';})()" and expect "pass". Finally wait for the completed tool call (data-testid "tool-call-completed") to become visible (allow up to 30 seconds).`)
}

func TestNewPageThinkTool(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "think: I need to analyze this problem" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent text "I've considered my approach." to appear. The thinking content (an element with data-testid "thinking-content") should become visible, and the 💭 emoji should be visible on the page.`)
}

func TestNewPagePatchTool(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "patch: test.txt" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent text "I'll patch the file: test.txt" to appear. A completed tool call (an element with data-testid "tool-call-completed") should become visible, and the text "patch" should be visible on the page.`)
}

func TestNewPageDefaultResponse(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "this is an undefined message" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the default fallback reply "edit predictable.go to add a response for that one..." to appear on the page. The sent message "this is an undefined message" should also be visible.`)
}

func TestNewPageConversationPersists(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear. Sending the first message navigates the app to a new conversation URL, so wait for the URL to contain "/c/" before continuing. Then type "echo: second message" into the same message input and click the send button again; wait for the echoed text "second message" to appear. Finally, both the first reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" and the text "second message" should still be present in the page body.`)
}

func TestNewPageLLMError(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "error: test error message" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the error text "LLM request failed: predictable error: test error message" to appear. An element with role "alert" should be visible and contain that error text.`)
}

// --- Linkify tests (ported from ui/e2e/linkify.spec.ts) ---

func TestNewPageLinkifyAgentMarkdown(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "echo: Check https://example.com and https://test.com" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. Inside the agent message's markdown content (a ".message-agent .markdown-content a" anchor) the first link should be visible with href "https://example.com", target "_blank", and rel "noopener noreferrer". The second such anchor should have href "https://test.com".`)
}

func TestNewPageLinkifyUserMessage(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "echo: Visit https://example.com" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the user message to render. Within the user message (".message-user") there should be exactly one anchor with class "text-link" (selector ".message-user a.text-link") whose href is "https://example.com".`)
}

// --- Markdown rendering & sanitization (ported from ui/e2e/markdown.spec.ts) ---

func TestNewPageMarkdownFormatting(t *testing.T) {
	lazyTest(t, "Navigate to /new. Type the text markdown: **bold** and *italic* and `code` into the message input (data-testid \"message-input\") and click the send button (data-testid \"send-button\"). Wait for the agent message to render markdown. The last agent message (\".message-agent\") should contain a <strong> element with text \"bold\", an <em> element with text \"italic\", and a <code> element with text \"code\".")
}

func TestNewPageMarkdownStripsScript(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: hello <script>alert("xss")</script> world into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "hello" and "world", but contain zero <script> elements (selector ".message-agent script" should match 0 elements).`)
}

func TestNewPageMarkdownStripsRemoteImg(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: ![tracker](https://evil.com/pixel.gif) safe text into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "safe text" but contain zero <img> elements (selector ".message-agent img" should match 0 elements).`)
}

func TestNewPageMarkdownStripsIframe(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: <iframe src="https://evil.com"></iframe> safe into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "safe" but contain zero <iframe> elements (selector ".message-agent iframe" should match 0 elements).`)
}

func TestNewPageMarkdownLinksNewTab(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: [example](https://example.com) into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The first anchor inside the last agent message (".message-agent a") should have href "https://example.com", target "_blank", and rel "noopener noreferrer".`)
}

func TestNewPageUserMessageNoMarkdown(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text **bold** and *italic* into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the user message to render. The last user message (".message-user") should contain the literal text "**bold**" and contain zero <strong> elements and zero <em> elements (selectors ".message-user strong" and ".message-user em" should each match 0 elements).`)
}

// --- Bash tool result rendering (ported from ui/e2e/ansi-rendering.spec.ts and
// the tool-result portions of conversation.spec.ts). The default UI renders
// bash tool calls as a collapsible ".bash-tool" card whose details panel
// (".bash-tool-details") is hidden until the ".bash-tool-header" is clicked.
// Inside the panel, output is rendered by the AnsiText component, which turns
// ANSI color codes into styled <span> elements rather than raw escape text. ---

func TestNewPageBashAnsiColors(t *testing.T) {
	lazyTest(t, "Navigate to /new. Into the message input (data-testid \"message-input\") type the text: "+
		"bash: printf '\\033[32mGreen\\033[0m \\033[31mRed\\033[0m \\033[1mBold\\033[0m plain' "+
		"and click the send button (data-testid \"send-button\"). Wait for a completed tool call "+
		"(data-testid \"tool-call-completed\") to appear, then click the bash tool header "+
		"(\".bash-tool-header\") to expand the details panel (\".bash-tool-details\" should become visible). "+
		"The output area (the last \".bash-tool-code\" element) should contain the readable words \"Green\", "+
		"\"Red\", \"Bold\" and \"plain\", and must NOT contain raw escape fragments like \"[0m\" or \"[32m\". "+
		"Because ANSI colors are present, that output element should contain at least one <span> element "+
		"(selector \".bash-tool-details .bash-tool-section:last-child .bash-tool-code span\" should match one or more elements).")
}

func TestNewPageBashPlainText(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text bash: echo "just plain text with no escapes" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for a completed tool call (data-testid "tool-call-completed") to appear, then click the bash tool header (".bash-tool-header") to expand the details panel (".bash-tool-details" should become visible). The output area (the last ".bash-tool-code" element) should contain the text "just plain text with no escapes". Since there are no ANSI codes, that output element should contain zero <span> elements (selector ".bash-tool-details .bash-tool-section:last-child .bash-tool-code span" should match 0 elements).`)
}

// TestNewPageBashAnsiCursorMovement guards against the regression where
// cursor-movement ANSI sequences (e.g. \x1b[1G, Cursor Horizontal Absolute —
// emitted by logging libraries to redraw/align status lines) left stray
// trailing letters in the rendered output ("GGGDev code has changes").
// ansi-to-html only understands SGR color codes; Shelley now pre-strips
// non-SGR sequences, so only the readable text remains.
func TestNewPageBashAnsiCursorMovement(t *testing.T) {
	lazyTest(t, "Navigate to /new. Into the message input (data-testid \"message-input\") type the text: "+
		"bash: printf '\\033[1G\\033[1G\\033[1GDev code has changes not yet deployed' "+
		"and click the send button (data-testid \"send-button\"). Wait for a completed tool call "+
		"(data-testid \"tool-call-completed\") to appear, then click the bash tool header "+
		"(\".bash-tool-header\") to expand the details panel (\".bash-tool-details\" should become visible). "+
		"The output area (the last \".bash-tool-code\" element) should contain the readable text "+
		"\"Dev code has changes not yet deployed\" and must NOT contain the stray fragment \"GGG\" "+
		"or the raw escape fragment \"[1G\".")
}

func TestNewPageBashCommandInHeader(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "bash: unique-test-command-xyz123" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for a completed tool call (data-testid "tool-call-completed") to appear. The bash tool command element (".bash-tool-command") should be visible and contain the text "unique-test-command-xyz123".`)
}

func TestNewPageBashCollapsibleDetails(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text bash: echo "testing tool results" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for a completed tool call (data-testid "tool-call-completed") to appear. The bash tool header (".bash-tool-header") should be visible. Initially the details panel (".bash-tool-details") is not visible. Click the header: the details panel should become visible. Click the header again: the details panel should become hidden again.`)
}

// --- Scroll behavior (ported from ui/e2e/scroll-behavior.spec.ts). The
// messages container (".messages-container") shows a ".scroll-to-bottom-button"
// when the user scrolls up away from the bottom; clicking it (or new content
// while pinned to the bottom) hides the button again. ---

func TestNewPageScrollToBottomButton(t *testing.T) {
	// "wide tables" makes the predictable agent emit a very tall markdown
	// response (several big tables), which reliably overflows the small mobile
	// viewport and makes the messages container scrollable.
	lazyTest(t, `Navigate to /new. Type "wide tables" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent's tall table response by waiting for the text "Wide Table (many columns)" to appear. The tall markdown content keeps laying out for a moment after that text appears, so add a short sleep (about 1 second) to let the scrollable messages container (".messages-container") settle. While pinned at the bottom, the scroll-to-bottom button (".scroll-to-bottom-button") should not be visible. Then, using an eval step, set the scrollTop of ".messages-container" to 0 to scroll to the top. After that, wait for the scroll-to-bottom button (".scroll-to-bottom-button") to become visible (it appears in response to the scroll event). Click it, then wait for ".scroll-to-bottom-button" to become hidden again.`)
}

// --- Queue messages (ported from ui/e2e/queue-messages.spec.ts). While the
// agent is working (kept busy with a long "delay:" command), the composer shows
// a split send button whose chevron (data-testid "send-options-button") opens a
// menu with a queue option (data-testid "queue-option"). A queued message shows
// a badge (data-testid "queued-badge") with a cancel button (data-testid
// "cancel-queued"). The agent-busy indicator is data-testid "agent-thinking". ---

func TestNewPageQueueSplitButtonAppears(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "delay: 15" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). The agent becomes busy: the thinking indicator (data-testid "agent-thinking") should become visible, and the split-button chevron (data-testid "send-options-button") should also be visible.`)
}

func TestNewPageQueueMessage(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "delay: 15" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the thinking indicator (data-testid "agent-thinking") to become visible. Then type "echo: queued hello" into the message input, click the split-button chevron (data-testid "send-options-button") to open the menu, and click the queue option (data-testid "queue-option"). A queued badge (data-testid "queued-badge") should become visible, and within it a cancel button (data-testid "cancel-queued") should be visible.`)
}

func TestNewPageQueueImmediateSendStillWorks(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "delay: 15" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the thinking indicator (data-testid "agent-thinking") to become visible. Then type "echo: immediate send" into the message input and click the main send button (data-testid "send-button") directly (do not use the dropdown). The text "echo: immediate send" should appear on the page as a normal user message, and no queued badge (data-testid "queued-badge") should exist on the page (the selector should match 0 elements).`)
}

// --- Cancellation (ported from ui/e2e/cancellation.spec.ts). A long-running
// agent turn can be cancelled with the stop button (".status-stop-button");
// after cancelling, the agent-thinking indicator disappears and the user can
// immediately send another message. ---

func TestNewPageCancelTextGeneration(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "delay: 30" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the thinking indicator (data-testid "agent-thinking") to become visible and the stop button (".status-stop-button") to become visible. Click the stop button. Afterwards the stop button (".status-stop-button") should no longer be visible and the thinking indicator (data-testid "agent-thinking") should no longer be visible.`)
}

func TestNewPageCancelThenContinue(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "bash: sleep 50" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the thinking indicator (data-testid "agent-thinking") and the stop button (".status-stop-button") to become visible. Click the stop button: the thinking indicator and the stop button should both become hidden. Then type "echo: after cancel" into the message input and click the send button again; the echoed text "after cancel" should appear on the page.`)
}

// --- More markdown sanitization (ported from ui/e2e/markdown.spec.ts). These
// confirm the DOMPurify-based renderer strips dangerous constructs from agent
// markdown while keeping the surrounding safe text. ---

func TestNewPageMarkdownStripsEventHandlers(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: <div onclick="alert(1)">click me</div> into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "click me". Evaluate the innerHTML of that agent message: it must NOT contain the substring "onclick" and must NOT contain the substring "alert".`)
}

func TestNewPageMarkdownSanitizesJavascriptHref(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: <a href="javascript:alert(document.cookie)">steal cookies</a> into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "steal cookies". Evaluate the innerHTML of that agent message: it must NOT contain the substring "javascript:".`)
}

func TestNewPageMarkdownStripsSvgScript(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: <svg onload="alert(1)"><circle r="50"/></svg> safe into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "safe". Evaluate the innerHTML of that agent message: it must NOT contain the substring "<svg" and must NOT contain the substring "onload".`)
}

func TestNewPageMarkdownStripsInputs(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text markdown: <input type="text" placeholder="Enter password"> <input type="password"> safe into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent message to render. The last agent message (".message-agent") should contain the text "safe". The text and password input elements should be stripped: selector ".message-agent input[type='text']" should match 0 elements and selector ".message-agent input[type='password']" should match 0 elements.`)
}

// --- Tool component details (ported from ui/e2e/tool-components.spec.ts). ---

func TestNewPageThinkToolHeader(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "think: This is a long thought that should be truncated in the header display" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for the agent text "I've considered my approach." to appear. The thinking content element (".thinking-content") should be visible and contain the text "This is a long thought".`)
}

func TestNewPagePatchCollapseExpand(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "patch success" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for a patch tool card (".patch-tool") to become visible; its details panel (".patch-tool-details") should be visible initially. Click the patch tool header (".patch-tool-header") to collapse it: the details panel (".patch-tool-details") should become hidden. Click the header again to expand: the details panel should become visible again.`)
}

func TestNewPageMarkdownStripsForm(t *testing.T) {
	// IMPORTANT: the exact text to type into the input (the fill value) is the
	// whole string below INCLUDING the leading "markdown: " prefix. The
	// predictable agent echoes everything after "markdown: " as rendered
	// markdown, so do not strip the prefix.
	lazyTest(t, "Navigate to /new. Into the message input (data-testid \"message-input\") fill exactly this value (keep the leading 'markdown: ' prefix): "+
		"`markdown: <form action=\"https://evil.com/steal\"><button type=\"submit\">Login</button></form> safe` "+
		"then click the send button (data-testid \"send-button\"). Wait for the agent message to render. The last agent message (\".message-agent\") should contain the text \"safe\". Inspect only the rendered markdown body via the last \".message-agent [data-testid='message-content']\" element's innerHTML: it must NOT contain the substring \"<form\", must NOT contain \"<button\", and must NOT contain \"evil.com\".")
}

func TestNewPageMarkdownLocalImage(t *testing.T) {
	// The "inline image" predictable pattern writes a tiny PNG into the
	// conversation cwd via bash, then references it with a relative-path
	// markdown image. The UI rewrites the src to the per-message file endpoint
	// and loads the bytes from the server.
	lazyTest(t, `Navigate to /new. Type "inline image" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait (up to 60 seconds) for an <img> element to appear inside an agent message (selector ".message-agent img"). That image's src attribute should match the pattern of starting with "/api/message/" and containing "/file?path=". The image should actually load: its naturalWidth (read via eval on the img element) should be greater than 0.`)
}

func TestNewPageMarkdownScreenshotImage(t *testing.T) {
	// The "screenshot image" predictable pattern is the reported bug: the file
	// is written to /tmp/shelley-screenshots (where the real screenshot tool
	// puts it) and referenced by absolute path, so it sits outside the
	// conversation's working directory. That is what the server used to refuse
	// with a 403, leaving a broken image icon. naturalWidth > 0 is the
	// assertion that matters: it is the difference between an <img> being
	// present and the picture actually rendering.
	lazyTest(t, `Navigate to /new. Type "screenshot image" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait (up to 60 seconds) for an <img> element to appear inside an agent message (selector ".message-agent img"). That image's src attribute should start with "/api/message/" and contain "/file?path=" and contain "shelley-screenshots". The image should actually load: its naturalWidth (read via eval on the img element) should be greater than 0.`)
}

// --- Remaining queue-message flows (ported from ui/e2e/queue-messages.spec.ts). ---

func TestNewPageQueueCancel(t *testing.T) {
	// After clicking cancel the server deletes the queued message, but the live
	// SSE update is metadata-only and doesn't refresh the message list, so the
	// badge only disappears after a page reload (matching the Playwright spec).
	lazyTest(t, `Navigate to /new. Type "delay: 60" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the thinking indicator (data-testid "agent-thinking") to become visible. Then type "echo: to be cancelled" into the message input, click the split-button chevron (data-testid "send-options-button"), and click the queue option (data-testid "queue-option"). Wait for the queued badge (data-testid "queued-badge") to become visible, then click the cancel button (data-testid "cancel-queued"). The cancel only takes effect on the server and is not reflected live, so reload the page (use an eval step that calls location.reload()) and wait for the message input (data-testid "message-input") to be visible again. After the reload, the queued badge (data-testid "queued-badge") should match 0 elements and the text "to be cancelled" should no longer be present on the page.`)
}

func TestNewPageQueueDrains(t *testing.T) {
	// Use a generous delay so the agent stays busy long enough to reliably
	// queue the follow-up message before the first turn finishes, even under
	// load; the drain waits below are 30s so they comfortably outlast it.
	lazyTest(t, `Navigate to /new. Type "delay: 10" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the thinking indicator (data-testid "agent-thinking") to become visible. Then type "echo: queued drain test" into the message input, click the split-button chevron (data-testid "send-options-button"), and click the queue option (data-testid "queue-option"); wait for the queued badge (data-testid "queued-badge") to become visible. Then wait (up to 30 seconds) for the first agent reply "Delayed for 10 seconds" to appear. After the agent finishes, the queued message drains and is processed: wait (up to 30 seconds) for the queued badge (data-testid "queued-badge") to no longer be visible, and wait (up to 30 seconds) for the echoed text "queued drain test" to appear on the page.`)
}

// --- Archive next-selection (regression for the drawer's filing-cabinet
// button). When you archive the currently-selected conversation from the
// conversation list, the app should select the conversation immediately BELOW
// the archived one in the visible list; if it was the last item, the one
// immediately ABOVE. The drawer computes that neighbor from the visible order
// and App switches to it.
//
// Conversations sort newest-first, so the most recently created one is at the
// TOP of the list. We create alpha, then bravo, so the visible order
// top-to-bottom is bravo, alpha, and bravo is current. Archiving bravo should
// select alpha (the conversation immediately below it).
//
// The reliable signal for "which conversation is selected" is the page URL
// (each conversation has a distinct /c/<id> path); the per-row preview text
// only refreshes on a full reload, so we don't depend on it. We stash each
// conversation's URL in sessionStorage as we create it (sessionStorage, not a
// window global, because creating each conversation starts from a full
// navigate to /new which would wipe window state), then assert the URL after
// archiving.
//
// Note on the flow: each conversation is created by navigating to /new (a fresh
// composer) and sending the first message, which creates the conversation and
// navigates the URL to /c/<id>. We deliberately use navigate /new rather than
// the in-app "New Conversation" button so the steps are simple and
// deterministic. ---

func TestArchiveSelectsConversationBelow(t *testing.T) {
	lazyTestSerial(t, `This test verifies that archiving the current conversation selects the one immediately below it in the list. Conversations are listed newest-first.
Step A — create conversation "alpha": navigate to /new, wait for the message input (data-testid "message-input") to be visible, fill it with "echo: alpha", and click the send button (data-testid "send-button"). Wait for the echoed text "alpha" to appear and for the URL to contain "/c/". Then run an eval step with expression "sessionStorage.setItem('alphaUrl', location.pathname)".
Step B — create conversation "bravo": navigate to /new again, wait for the message input (data-testid "message-input") to be visible, fill it with "echo: bravo", and click the send button (data-testid "send-button"). Wait for the echoed text "bravo" to appear and for the URL to contain "/c/". Then run an eval step with expression "sessionStorage.setItem('bravoUrl', location.pathname)".
Now "bravo" is the current conversation and is at the TOP of the list, with "alpha" immediately below it.
Step C — open the conversation drawer by clicking the button with aria-label "Open conversations", and wait for the active conversation item (selector ".conversation-item.active") to be visible. Confirm the current URL is bravo's: run an eval step whose expression is "location.pathname === sessionStorage.getItem('bravoUrl') ? 'true' : 'false'" and expect the result "true".
Step D — archive the active conversation. The archive button is a small icon button that a coordinate-based click can miss, so trigger it with an eval step whose expression is "document.querySelector(\".conversation-item.active button[aria-label='Archive']\").click()". After archiving, the app asynchronously selects the conversation immediately below bravo, which is alpha, and the URL changes to alpha's path. The selection update is not instantaneous, so add a sleep of about 2 seconds to let it settle before asserting. Then run an eval step whose expression is "location.pathname === sessionStorage.getItem('alphaUrl') ? 'true' : 'false'" and expect the result "true". Then run an eval step whose expression is "location.pathname === sessionStorage.getItem('bravoUrl') ? 'true' : 'false'" and expect the result "false".`)
}

// --- Diff viewer file list (regression for untracked/added files missing from
// the sidebar/picker). The diff viewer can be opened directly via the URL query
// params ?diff=working&cwd=<repo>. The file picker is a <select> with class
// "diff-viewer-select" whose <option> labels are prefixed with a status symbol:
// "+" for added, "~" for modified, "-" for deleted. `git diff --name-status
// HEAD` omits untracked files, so brand-new files used to be missing from this
// list; the handler now merges them in as "added". This is the same <select> on
// mobile and desktop, so the assertion is viewport-independent. ---

func TestDiffViewerListsAddedModifiedDeleted(t *testing.T) {
	lazyTest(t, fmt.Sprintf(`Navigate to /new?diff=working&cwd=%s . This opens the git diff viewer for the working-tree changes of that repository. Wait for the file picker (a <select> element with selector "select.diff-viewer-select") to be visible. The working tree has exactly four changed files, each appearing as an <option> in that select with a status-symbol prefix: a modified file shown as "~ tracked_mod.txt", a deleted file shown as "- tracked_del.txt", a brand-new untracked file shown as "+ untracked_added.txt", and a modified committed file whose name contains a space shown as "~ spaced name.txt". Verify all four options are present, with their full untruncated paths, by running an eval step whose expression is "(() => { const opts = Array.from(document.querySelectorAll('select.diff-viewer-select option')).map(o => o.textContent.trim()); const has = (sym, name) => opts.some(t => t.startsWith(sym) && t.includes(name)); return has('+','untracked_added.txt') && has('~','tracked_mod.txt') && has('-','tracked_del.txt') && has('~','spaced name.txt') ? 'true' : 'false'; })()" and expect the result "true". Two regression cases matter here: the untracked (added) file must be present with the "+" prefix, and the committed file with a space must appear as the full "spaced name.txt" (not truncated at the space to "spaced").`, diffFixtureDir))
}

// diffFixtureDir is a deterministic path holding a git repo with working-tree
// changes (a modified, a deleted, and a brand-new untracked file). The diff
// viewer's file list is loaded from here. The path is fixed (not random) so the
// LazyCue description that embeds it hashes to a stable cache key across runs.
var diffFixtureDir = filepath.Join(os.TempDir(), "shelley-lazycue-diff-fixture")

// setupDiffFixtureRepo (re)creates a git repo at dir with three kinds of
// working-tree change: a modified tracked file, a deleted tracked file, and a
// brand-new untracked file. The untracked file is the regression target — it
// must show up in the diff viewer's sidebar as an added file.
func setupDiffFixtureRepo(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	run := func(args ...string) error {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %w\n%s", args, err, out)
		}
		return nil
	}
	write := func(name, content string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	for _, step := range [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "user.email", "test@test.com"},
	} {
		if err := run(step...); err != nil {
			return err
		}
	}
	if err := write("tracked_mod.txt", "one\ntwo\n"); err != nil {
		return err
	}
	if err := write("tracked_del.txt", "remove me\n"); err != nil {
		return err
	}
	// A committed file whose name contains a space. The handler used to split
	// --name-status lines on whitespace, mangling this path so the file never
	// rendered — even though it was part of the commit. It must come through
	// intact.
	if err := write("spaced name.txt", "alpha\n"); err != nil {
		return err
	}
	if err := run("git", "add", "tracked_mod.txt", "tracked_del.txt", "spaced name.txt"); err != nil {
		return err
	}
	if err := run("git", "commit", "-m", "fixture base\n\nPrompt: lazycue diff fixture"); err != nil {
		return err
	}
	// Working-tree changes.
	if err := write("tracked_mod.txt", "one\ntwo\nthree\n"); err != nil {
		return err
	}
	if err := write("spaced name.txt", "alpha\nbeta\n"); err != nil {
		return err
	}
	if err := run("git", "rm", "tracked_del.txt"); err != nil {
		return err
	}
	if err := write("untracked_added.txt", "fresh\nuntracked\nlines\n"); err != nil {
		return err
	}
	return nil
}

// --- @ file completion (replaces ui/e2e/file-completion.spec.ts, which
// flaked on loaded CI shards: the menu never opened for a textarea that was
// focused before the conversation had finished loading). Typing an "@token" at
// the caret in the composer opens a menu (data-testid "file-completion-menu")
// listing files and folders under the conversation's working directory,
// matched by name and by content (git grep). Choosing an entry (Enter, Tab or a
// click) replaces the token with the path relative to that directory, quoted
// when it contains spaces, and adds NO attachment. The working directory is
// the server's default cwd, which TestMain fills with the fixture tree (see
// setupCompletionFixture), so the descriptions embed no paths. Descriptions
// that need a caret in the middle of the text set the value and selection in
// one eval step, since "fill" leaves the caret at the end.
//
// Not ported from the Playwright spec: cases that intercepted /api/find-files
// (a superseded slow query racing a fresh one, a 503 error state, arrow keys
// against a re-rooted search_dir) and the two draft-cwd persistence cases,
// which need per-request assertions LazyCue has no primitive for. ---

// completionPreamble opens /new and focuses the composer. Every completion
// description but one starts with it; the shared wording keeps the
// agent-generated openings alike.
const completionPreamble = `Navigate to /new, wait for the message input (data-testid "message-input") to be visible, and click it so the textarea has keyboard focus (the @ file-completion menu only opens for a focused textarea). `

func completionTest(t *testing.T, description string) {
	t.Helper()
	lazyTest(t, completionPreamble+description)
}

// Completion must work inside an existing conversation, not only in the /new
// draft the other tests use, so this one sends a message first.
func TestNewPageFileCompletionInsertsRelativePath(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear and for the URL to contain "/c/". Click the message input so the textarea has keyboard focus, then fill it with the text "Open @alpha". The file-completion menu (data-testid "file-completion-menu") should appear. Inside it, the element with role "listbox" has aria-label "Files and folders", and an element with role "option" and aria-label "src/alpha.ts" (the match, as a path relative to the conversation's working directory) should be visible; within that option the element matching ".grp-path mark" has text "alpha" (the highlighted part of the match). Press the Enter key. The token is replaced by the completed reference plus a trailing space (Enter completes; it does not send): eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='Open @src/alpha.ts '?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass". The menu is gone (selector "[data-testid='file-completion-menu']" matches 0 elements) and nothing was attached (selector "[data-testid='message-attachments']" matches 0 elements).`)
}

func TestNewPageFileCompletionContentSearch(t *testing.T) {
	completionTest(t, `Fill the message input with the text "Use @cardamom". No file NAME matches, but the file recipes.txt CONTAINS that word, so the file-completion menu (data-testid "file-completion-menu") should show an element with role "option" and aria-label "recipes.txt" (allow up to 15 seconds; content search follows the name search). Within that option the ".ff-snippet" element contains the text "secret ingredient: cardamom" and its "mark" descendant has text "cardamom". Click that option. The token is replaced: eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='Use @recipes.txt '?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass".`)
}

func TestNewPageFileCompletionQuotedQueryWithSpaces(t *testing.T) {
	completionTest(t, `Fill the message input with the text Read @"My Doc (an opening double quote after the @ lets the query contain spaces; there is no closing quote). The file-completion menu (data-testid "file-completion-menu") should show an element with role "option" and aria-label "My Document.md". Press the Tab key. Because the path contains a space, the completed reference is wrapped in double quotes, followed by a trailing space: eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='Read @\"My Document.md\" '?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass". Nothing was attached (selector "[data-testid='message-attachments']" matches 0 elements).`)
}

func TestNewPageFileCompletionMiddleCaret(t *testing.T) {
	completionTest(t, `Put the draft "before @gammaray after" into the message input with the caret at offset 11 (right after "@gam", in the middle of the text) using one eval step: "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');el.focus();Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set.call(el,'before @gammaray after');el.setSelectionRange(11,11);el.dispatchEvent(new Event('input',{bubbles:true}));return 'set';})()" expecting "set". The file-completion menu (data-testid "file-completion-menu") should show an element with role "option" and aria-label "gamma.md" (the query is the token text up to the caret, "gam"). Click that option. Only the @-token is replaced and the surrounding text is untouched, the textarea keeps focus, and the caret sits right after the inserted path (offset 16, with no trailing space inserted because text already follows): eval "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');return el.value==='before @gamma.md after'&&document.activeElement===el&&el.selectionStart===16?'pass':'fail:'+JSON.stringify(el.value)+' caret='+el.selectionStart+' focused='+(document.activeElement===el);})()" expecting "pass". The menu is gone (selector "[data-testid='file-completion-menu']" matches 0 elements).`)
}

func TestNewPageFileCompletionTokenBoundaries(t *testing.T) {
	completionTest(t, `An @ only starts a file token at the start of the text or after whitespace, and the token ends at an unquoted space. Fill the message input with "email foo@delta", wait about 1 second, and assert selector "[data-testid='file-completion-menu']" matches 0 elements (no menu: the @ follows a letter). Fill it with "prefix@delta", wait about 1 second, and assert the same selector matches 0 elements. Fill it with "@delta": now the file-completion menu (data-testid "file-completion-menu") appears with an element with role "option" and aria-label "delta.md". Finally fill it with "look @delta later" (the caret ends up after "later", outside the token), wait about 1 second, and assert the menu selector matches 0 elements.`)
}

func TestNewPageFileCompletionEscapeDismisses(t *testing.T) {
	completionTest(t, `Fill the message input with "@delta". The file-completion menu (data-testid "file-completion-menu") should appear with an element with role "option" and aria-label "delta.md". Press the Escape key. The menu closes (selector "[data-testid='file-completion-menu']" matches 0 elements) but the textarea keeps focus and its text is unchanged: eval "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');return document.activeElement===el&&el.value==='@delta'?'pass':'fail:'+JSON.stringify(el.value)+' focused='+(document.activeElement===el);})()" expecting "pass". Then press Enter while holding Shift (press_key "Enter" with modifiers "Shift"): with the menu dismissed, Shift+Enter inserts a newline as usual instead of completing anything, so eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='@delta'+String.fromCharCode(10)?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass".`)
}

func TestNewPageFileCompletionArrowKeysWrap(t *testing.T) {
	completionTest(t, `Fill the message input with "@My". The file-completion menu (data-testid "file-completion-menu") shows exactly 3 elements with role "option" (the files My Document.md and My Notes.md and the folder My Notes/), and the first option has aria-selected "true". Press the ArrowUp key: the highlight wraps around to the LAST option, so the third option has aria-selected "true" and the first has aria-selected "false". Press the ArrowDown key: it wraps back to the first option (aria-selected "true"). Press the ArrowDown key once more: the second option is highlighted. Typing lazily promotes the new conversation to a draft about 600ms after typing pauses; wait (up to 10 seconds) for the URL to contain "/c/" (the promotion has happened) and then sleep about 1 second: the second option must STILL be the highlighted one (aria-selected "true") — the promotion must not reset the highlight to the first entry. Remember its path with an eval step: "(function(){var opts=document.querySelectorAll('[data-testid=\"file-completion-menu\"] [role=\"option\"]');var el=document.querySelector('[data-testid=\"file-completion-menu\"] [role=\"option\"][aria-selected=\"true\"]');window.__picked=el.getAttribute('title');return el===opts[1]?'second':'other';})()" expecting "second". Press the Enter key. The highlighted entry is inserted; every candidate here contains a space, so it is double-quoted and followed by a space: eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='@\"'+window.__picked+'\" '?'pass':'fail:'+JSON.stringify(v)+' picked='+window.__picked;})()" expecting "pass".`)
}

func TestNewPageFileCompletionNoMatches(t *testing.T) {
	completionTest(t, `Fill the message input with "echo: newline @zzznomatch". The file-completion menu (data-testid "file-completion-menu") appears; once both searches settle (allow up to 15 seconds) the element with role "status" inside it has the exact text "No matching files or folders" and the menu contains 0 elements with role "option". A settled empty menu must leave the normal composer keys alone. Press Enter while holding Shift (press_key "Enter" with modifiers "Shift"): a newline is inserted, so eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='echo: newline @zzznomatch'+String.fromCharCode(10)?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass". Fill the message input with "echo: tab @zzznomatch" and wait for the menu's role "status" element to read "No matching files or folders" again. Press the Tab key: focus leaves the textarea and its text is unchanged, so eval "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');return document.activeElement!==el&&el.value==='echo: tab @zzznomatch'?'pass':'fail:'+JSON.stringify(el.value)+' focused='+(document.activeElement===el);})()" expecting "pass". Click the message input to focus it again, fill it with "echo: sent @zzznomatch", and wait for the "No matching files or folders" status once more. Press Enter while holding Control (press_key "Enter" with modifiers "Control"): that sends the message rather than completing anything. Wait (up to 30 seconds) for the agent's echoed reply: eval "Array.from(document.querySelectorAll('.message-agent')).some(function(el){return el.textContent.indexOf('sent @zzznomatch')>=0;})?'replied':'waiting'" expecting "replied" with timeout "30s". The textarea is then empty: eval "document.querySelector('[data-testid=\"message-input\"]').value===''?'empty':'not empty'" expecting "empty".`)
}

func TestNewPageFileCompletionPreservesColon(t *testing.T) {
	completionTest(t, `Put the text "Read @README: please" into the message input with the caret at offset 12 (immediately before the colon) using one eval step: "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');el.focus();Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set.call(el,'Read @README: please');el.setSelectionRange(12,12);el.dispatchEvent(new Event('input',{bubbles:true}));return 'set';})()" expecting "set". The file-completion menu (data-testid "file-completion-menu") should show an element with role "option" and aria-label "README.md". Press the Enter key. The token is completed and the colon directly after it is preserved, with no space inserted between them: eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='Read @README.md: please'?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass". The menu is gone (selector "[data-testid='file-completion-menu']" matches 0 elements).`)
}

func TestNewPageFileCompletionHoverThenTab(t *testing.T) {
	completionTest(t, `First install a recorder for file-content and completion requests with an eval step: "(function(){window.__contentRequests=[];window.__findRequests=0;var o=window.fetch;window.fetch=function(u,opt){var s=(typeof u==='string')?u:u.url;if(/\/api\/(read-file|upload)(\?|$)/.test(s)){window.__contentRequests.push(s);}if(s.indexOf('/api/find-files')>=0){window.__findRequests++;}return o.apply(this,arguments);};return 'armed';})()" expecting "armed". Fill the message input with the text Read @"My Notes (opening quote, no closing quote). The file-completion menu (data-testid "file-completion-menu") lists files and folders together: an element with role "option" and aria-label "My Notes/ (folder)" whose data-kind attribute is "folder", and an element with role "option" and aria-label "My Notes.md" whose data-kind attribute is "file"; exactly one of them has aria-selected "true". Hovering an option highlights it without taking focus from the textarea. Take the option that is NOT highlighted, remember its title attribute (its path) and dispatch a mouseenter event on it, in one eval step: "(function(){var el=document.querySelector('[data-testid=\"file-completion-menu\"] [role=\"option\"][aria-selected=\"false\"]');window.__hovered=el.getAttribute('title');el.dispatchEvent(new MouseEvent('mouseenter'));return 'hovered';})()" expecting "hovered". Afterwards that option is the highlighted one and the textarea is still the active element: eval "(function(){var opts=Array.from(document.querySelectorAll('[data-testid=\"file-completion-menu\"] [role=\"option\"]'));var el=opts.find(function(o){return o.getAttribute('title')===window.__hovered;});return el&&el.getAttribute('aria-selected')==='true'&&document.activeElement===document.querySelector('[data-testid=\"message-input\"]')?'pass':'fail';})()" expecting "pass". Typing lazily promotes the new conversation to a draft about 600ms after typing pauses. Record how many completion requests have been made so far: eval "String(window.__findRequests)" — remember the value with eval "(function(){window.__findBefore=window.__findRequests;return 'noted';})()" expecting "noted". Wait (up to 10 seconds) for the URL to contain "/c/" (the promotion has happened), then sleep about 1 second. The promotion must neither reset the highlight nor re-run the search (a regression: the completion briefly saw a different session id while the draft's props were being patched): run the same highlight eval again expecting "pass", and eval "window.__findRequests===window.__findBefore?'same':'refetched '+window.__findBefore+'->'+window.__findRequests" expecting "same". Press the Tab key. The hovered entry is inserted; both candidates contain a space, so it is double-quoted and followed by a space (a folder keeps its trailing slash inside the quotes): eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='Read @\"'+window.__hovered+'\" '?'pass':'fail:'+JSON.stringify(v)+' hovered='+window.__hovered;})()" expecting "pass". Choosing an entry attaches nothing and reads no file: selector "[data-testid='message-attachments']" matches 0 elements, and eval "String(window.__contentRequests.length)" expecting "0".`)
}

func TestNewPageFileCompletionBrowsesNestedFolder(t *testing.T) {
	completionTest(t, `Put the draft "Compare @./docs/ with another folder" into the message input with the caret at offset 16 (right after "@./docs/") using one eval step: "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');el.focus();Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set.call(el,'Compare @./docs/ with another folder');el.setSelectionRange(16,16);el.dispatchEvent(new Event('input',{bubbles:true}));return 'set';})()" expecting "set". A token ending in a slash browses that folder: the file-completion menu (data-testid "file-completion-menu") shows an element with role "option" and aria-label "docs/guide.md" and one with aria-label "docs/Final Notes/ (folder)". Click the folder option. The token is replaced by the quoted folder path, the text after it is preserved, the textarea keeps focus, and the caret sits right after the closing quote (offset 28): eval "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');return el.value==='Compare @\"docs/Final Notes/\" with another folder'&&document.activeElement===el&&el.selectionStart===28?'pass':'fail:'+JSON.stringify(el.value)+' caret='+el.selectionStart+' focused='+(document.activeElement===el);})()" expecting "pass".`)
}

func TestNewPageFileCompletionAbsolutePathOutsideCwd(t *testing.T) {
	completionTest(t, `An empty folder named "Empty Folder" lives in a directory "elsewhere" that is a SIBLING of the conversation's working directory, i.e. outside it. Put the text Inspect @"<absolute path of that folder>" into the message input with the caret at the end, computing the path from the page's default working directory, using one eval step: "(function(){var cwd=window.__SHELLEY_INIT__.default_cwd;var target=cwd.replace(/\/[^\/]+$/,'')+'/elsewhere/Empty Folder';var el=document.querySelector('[data-testid=\"message-input\"]');el.focus();Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set.call(el,'Inspect @\"'+target+'\"');el.setSelectionRange(el.value.length,el.value.length);el.dispatchEvent(new Event('input',{bubbles:true}));return 'set';})()" expecting "set". An exact absolute folder path offers the folder itself, shown relative to the working directory: the file-completion menu (data-testid "file-completion-menu") shows an element with role "option" and aria-label "../elsewhere/Empty Folder/ (folder)". Press the Enter key. The token becomes that quoted relative path plus a trailing space: eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return v==='Inspect @\"../elsewhere/Empty Folder/\" '?'pass':'fail:'+JSON.stringify(v);})()" expecting "pass".`)
}

// setupCompletionFixture fills cwd (the server's default working directory)
// with the completion fixture: a git repo, so content search works via
// `git grep --untracked`, holding files and folders whose names are chosen so
// each completion query in the tests above matches exactly the entries that
// test asserts on; plus an empty folder under elsewhere, a sibling directory
// outside cwd. Other tests run in cwd too and may drop files there (a bash
// tool's output, a patched test.txt, a demo image); keep query words unlikely
// to match such names.
func setupCompletionFixture(cwd, elsewhere string) error {
	for _, d := range []string{
		filepath.Join(cwd, "src"),
		filepath.Join(cwd, "My Notes"),
		filepath.Join(cwd, "docs", "Final Notes"),
		filepath.Join(elsewhere, "Empty Folder"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w\n%s", err, out)
	}
	for name, content := range map[string]string{
		"src/alpha.ts":      "export const alpha = 1;\n",
		"gamma.md":          "gamma\n",
		"delta.md":          "delta\n",
		"README.md":         "example\n",
		"My Document.md":    "notes\n",
		"My Notes.md":       "file contents must not be attached\n",
		"docs/guide.md":     "guide\n",
		"recipes.txt":       "secret ingredient: cardamom\n",
		"shopping-list.txt": "eggs and flour\n",
	} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// --- Bare "/model" + Enter picks the top model ---
//
// Regression for the exact user-reported bug: typing "/model" with NO trailing
// space and pressing Enter sent a useless bare "/model" to the server (which
// just echoes the help/model list) instead of selecting the top suggestion. The
// model-argument menu must open as soon as "/model" is typed (space optional),
// and Enter with nothing chosen yet must complete the top suggestion into the
// composer (e.g. "/model <top-id> ") rather than send. We assert on the textarea
// value: on the bug it becomes empty ("/model" was sent), on the fix it grows to
// a completed argument.
func TestNewPageBareModelEnterPicksTop(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear and for the URL to contain "/c/" (a real conversation is needed for /model). Then, using an eval step, set the message input's value to "/model" with NO trailing space via the native textarea value setter and dispatch an "input" event so the framework picks it up; the exact expression is: "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');var s=Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set;s.call(el,'/model');el.dispatchEvent(new Event('input',{bubbles:true}));el.focus();return 'set';})()" and expect "set". Wait for the model-argument menu (data-testid "model-arg-menu") to be visible (it must open even without a trailing space). Then, WITHOUT pressing any arrow keys, dispatch an Enter keydown on the message input: expression "(function(){document.querySelector('[data-testid=\"message-input\"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));return 'enter';})()", expect "enter". Finally assert that Enter COMPLETED the top model suggestion rather than sending a bare "/model": eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return (v.indexOf('/model ')===0 && v.length>'/model '.length) ? 'completed' : ('BUG:'+JSON.stringify(v));})()" and expect "completed".`)
}

// --- /model argument menu: Enter completes the highlighted option ---
//
// Regression for the bug where, in the /model argument autocomplete menu,
// arrowing to an option and pressing Enter sent a bare "/model" to the server
// (clearing the composer) instead of completing the highlighted option like a
// mouse click would. After navigating the menu with the arrow keys, Enter must
// insert the highlighted option into the composer (e.g. "/model <option> ") and
// leave the menu ready for the next argument — NOT send the command. We assert
// on the textarea value: on the bug it becomes empty ("/model" was sent), and
// on the fix it grows to a completed argument.
func TestNewPageModelMenuEnterCompletesHighlighted(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear and for the URL to contain "/c/" (a real conversation is needed for /model). Then, using an eval step, set the message input's value to "/model " (the word slash-model followed by a single trailing space) via the native textarea value setter and dispatch an "input" event so the framework picks it up; the exact expression is: "(function(){var el=document.querySelector('[data-testid=\"message-input\"]');var s=Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set;s.call(el,'/model ');el.dispatchEvent(new Event('input',{bubbles:true}));el.focus();return 'set';})()" and expect "set". Wait for the model-argument menu (data-testid "model-arg-menu") to be visible. Using an eval step, dispatch an ArrowDown keydown on the message input to move the highlight: expression "(function(){document.querySelector('[data-testid=\"message-input\"]').dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));return 'down';})()", expect "down". Then dispatch an Enter keydown on the message input: expression "(function(){document.querySelector('[data-testid=\"message-input\"]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));return 'enter';})()", expect "enter". Finally assert that Enter COMPLETED the highlighted option rather than sending a bare "/model": eval "(function(){var v=document.querySelector('[data-testid=\"message-input\"]').value;return (v.indexOf('/model ')===0 && v.length>'/model '.length) ? 'completed' : ('BUG:'+JSON.stringify(v));})()" and expect "completed".`)
}

// --- Compact and send: send-options menu offers compaction + queue in one ---
//
// The split send button's options menu (opened via the chevron, data-testid
// "send-options-button") offers a "Compact and send" action (data-testid
// "compact-and-send-option") whenever a conversation is open. Clicking it
// compacts the conversation into a new generation and queues the composed
// message so it runs once compaction completes. We drive the whole flow with
// the predictable server: send a first message to open a conversation, type an
// echo command, open the menu, click "Compact and send", then assert a new
// generation started (compaction ran) and the queued echo eventually drained.
func TestNewPageCompactAndSend(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type "Hello" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"); wait for the reply "Hello! I'm Shelley, your AI assistant. How can I help you today?" to appear and for the URL to contain "/c/". Then type "echo: compacted hello" into the message input (data-testid "message-input"). Click the split-button chevron (data-testid "send-options-button") to open the send-options menu, and wait for the "Compact and send" option (data-testid "compact-and-send-option") to be visible. Click it. Compaction runs into a new generation: wait (up to 30 seconds) for the text "New generation started" to appear on the page. After compaction finishes, the queued message drains: wait (up to 30 seconds) for the echoed text "compacted hello" to appear on the page.`)
}

// startPredictableServer boots a Shelley server in predictable mode backed by a
// temp DB and the embedded UI. It returns the test server and a cleanup func.
func startPredictableServer() (*httptest.Server, func()) {
	tempDB, err := os.MkdirTemp("", "lazycue-db-")
	if err != nil {
		panic(err)
	}
	database, err := db.New(db.Config{DSN: filepath.Join(tempDB, "test.db")})
	if err != nil {
		panic(err)
	}
	if err := database.Migrate(context.Background()); err != nil {
		panic(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	llmManager := server.NewLLMServiceManager(newPredictableLLMConfig(logger))
	svr := server.NewServer(database, llmManager, claudetool.ToolSetConfig{}, logger, true, "predictable", "")

	// RegisterRoutes wires up the SPA-aware static handler for the embedded UI
	// (so /new and its assets resolve) plus the API endpoints.
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	return ts, func() {
		ts.Close()
		database.Close()
		os.RemoveAll(tempDB)
	}
}
