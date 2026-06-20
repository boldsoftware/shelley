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
//	LAZYCUE_SUMMARY       write a machine-readable JSON cache-stats summary here

// app is the shared harness. Its BaseURL is filled in by TestMain once the
// server is listening.
var app *lazycue.Harness

func TestMain(m *testing.M) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		// Tests below all skip; run them so `go test` reports them as skipped.
		os.Exit(m.Run())
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

	app = lazycue.New(lazycue.Options{
		BaseURL:     ts.URL,
		CacheDir:    filepath.Join("..", "ui", "lazycue", ".lazycue"),
		Verbose:     true,
		ArtifactDir: os.Getenv("LAZYCUE_ARTIFACT_DIR"),
	})

	code := m.Run()

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

	os.Exit(code)
}

func lazyTest(t *testing.T, description string) {
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

// --- Conversation tests (ported from ui/e2e/conversation.spec.ts) ---
//
// These drive the predictable LLM service through the real UI. The predictable
// service (loop/predictable.go) maps specific inputs to deterministic outputs:
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
		"(selector \".bash-tool-details .bash-tool-code span\" should match one or more elements).")
}

func TestNewPageBashPlainText(t *testing.T) {
	lazyTest(t, `Navigate to /new. Type the text bash: echo "just plain text with no escapes" into the message input (data-testid "message-input") and click the send button (data-testid "send-button"). Wait for a completed tool call (data-testid "tool-call-completed") to appear, then click the bash tool header (".bash-tool-header") to expand the details panel (".bash-tool-details" should become visible). The output area (the last ".bash-tool-code" element) should contain the text "just plain text with no escapes". Since there are no ANSI codes, that output element should contain zero <span> elements (selector ".bash-tool-details .bash-tool-code span" should match 0 elements).`)
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
	lazyTest(t, `This test verifies that archiving the current conversation selects the one immediately below it in the list. Conversations are listed newest-first.
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
// viewer's file list is loaded from here. The path is stable so the LazyCue
// description that references it hashes to a consistent cache key.
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
