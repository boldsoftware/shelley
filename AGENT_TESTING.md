# Shelley Agent Testing Guide

This document provides instructions for automated testing of the Shelley coding agent product.

## Prerequisites

- `ANTHROPIC_API_KEY` environment variable set
- Node.js and pnpm installed
- Go installed
- `headless` browser tool available (check with `which headless`)

## Setup Instructions

### 1. Build Shelley

```bash
cd /path/to/shelley
make build
```

This will:
- Build the UI (`pnpm install && pnpm run build`)
- Create template tarballs
- Build the Go binary to `bin/shelley`

### 2. Install Playwright for E2E Tests

```bash
cd ui
pnpm install
pnpm exec playwright install chromium
```

### 3. Start Shelley Server

For testing with Claude:
```bash
./bin/shelley --model claude-sonnet-4.6 --db test.db serve --port 9001
```

For testing with predictable model (no API key needed):
```bash
./bin/shelley --model predictable --db test.db serve --port 9001
```

### 4. Start Headless Browser (if using headless tool)

```bash
headless start
```

## Test Categories

### CLI Tests

Test these commands manually:

```bash
# List available models
./bin/shelley models
```

### E2E Tests (Automated)

Run the full E2E test suite:

```bash
cd ui
pnpm run test:e2e
```

Run specific test files:
```bash
pnpm run test:e2e -- --grep "smoke"
pnpm run test:e2e -- --grep "conversation"
pnpm run test:e2e -- --grep "cancellation"
```

### Headless Browser Testing

```bash
# Navigate to Shelley
headless navigate http://localhost:9001

# Check page title
headless eval 'document.title'

# Get page content
headless eval 'document.body.innerText.slice(0, 2000)'

# Take screenshot
headless screenshot screenshot.png

# Set input value (React-compatible method)
headless eval '(() => {
  const input = document.querySelector("[data-testid=\"message-input\"]");
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
  setter.call(input, "Your message here");
  input.dispatchEvent(new Event("input", { bubbles: true }));
  return "done";
})()'

# Click send button
headless eval 'document.querySelector("[data-testid=\"send-button\"]").click()'

# Check if agent is thinking
headless eval 'document.querySelector("[data-testid=\"agent-thinking\"]")?.innerText || "not thinking"'

# Check for errors
headless eval 'document.querySelector("[role=\"alert\"]")?.innerText || "no errors"'
```

## Test Checklist

### Things That Work Well (Regression Tests)

- [ ] **Page loads correctly** - Title is "Shelley", message input visible
- [ ] **Send button state** - Disabled when empty, enabled when text entered
- [ ] **Claude integration** - Messages send and receive responses (~2-3 seconds)
- [ ] **Prompt caching** - Check server logs for `cache_read_input_tokens`
- [ ] **Tool execution - bash** - Ask to run `echo hello`, verify tool output
- [ ] **Tool execution - think** - Send `think: analyzing...`, verify think tool appears
- [ ] **Tool execution - edit** - Send `edit: test.txt`, verify edit tool appears
- [ ] **Conversation persistence** - Multiple messages in same conversation work
- [ ] **Enter key sends** - Press Enter in textarea to send message
- [ ] **Model selector** - Shows available models in UI
- [ ] **Working directory** - Shows current directory path
- [ ] **Accessibility labels** - Input has `aria-label="Message input"`, button has `aria-label="Send message"`

### Known Issues (Need Fixing/Re-checking)

- [ ] **Empty message bug (CRITICAL)** - Rapid sequential messages cause 400 errors
  - Test: Send 5+ messages quickly in succession
  - Expected: All should succeed
  - Actual: API returns `messages.N: all messages must have non-empty content`

- [ ] **Cancellation state after reload** - Cancelled operations don't show "cancelled" text
  - Test: Start `bash: sleep 100`, cancel it, reload page
  - Expected: Should show "cancelled" or "[Operation cancelled]"
  - Actual: Shows tool with `x` but no cancelled text

- [ ] **Thinking indicator stuck on error** - Indicator doesn't hide when LLM fails
  - Test: Trigger an LLM error (e.g., via rapid messages)
  - Expected: Indicator should hide, error should display
  - Actual: "Agent working..." stays visible indefinitely

- [ ] **Menu button outside viewport** - Hamburger menu not clickable on mobile
  - Test: On mobile viewport, try clicking menu button
  - Expected: Menu should open
  - Actual: Button reported as "outside of the viewport"

- [ ] **Programmatic input filling** - Direct `.value` assignment doesn't enable send button
  - Test: Use browser automation to set input value
  - Expected: Send button should enable
  - Actual: Button stays disabled (need to use native setter method)

## Screenshots to Capture

When testing, capture these screenshots for the report:

1. `initial-load.png` - Fresh page load
2. `message-typed.png` - Message in input field
3. `agent-thinking.png` - Thinking indicator visible
4. `response-received.png` - After Claude responds
5. `tool-execution.png` - After a tool (bash/think/edit) runs
6. `error-state.png` - If any errors occur
7. `menu-open.png` - Sidebar/conversation list open

## Report Template

Create `test-report/SHELLEY_TEST_REPORT.md` with:

1. **Executive Summary** - Overall pass/fail, key issues
2. **Test Environment** - Platform, models tested, browser
3. **Test Results Summary** - Table of categories and pass/fail counts
4. **Issues Found** - Detailed description of each issue with:
   - File/location
   - Description
   - Expected vs Actual
   - Screenshot
   - Impact
5. **What's Working Well** - Positive findings
6. **Recommendations** - Prioritized fixes (Critical/High/Medium/Low)
7. **Screenshots Index** - List of captured screenshots

## Common Issues & Solutions

### Build fails with "no matching files found"
```bash
# Templates need to be built first
make templates
# Then build
make build
```

### Playwright not finding chromium
```bash
cd ui
pnpm exec playwright install chromium
```

### Server already running
```bash
# Find and kill existing process
lsof -i :9001 | grep LISTEN | awk '{print $2}' | xargs kill
```

### Headless browser already running
```bash
headless stop
headless start
```

## API Endpoints for Manual Testing

```bash
# List conversations
curl http://localhost:9001/api/conversations

# Get specific conversation
curl http://localhost:9001/api/conversation/<id>

# Create new conversation (POST)
curl -X POST http://localhost:9001/api/conversations/new \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4.6","cwd":"/path/to/dir"}'

# Send message (POST)
curl -X POST http://localhost:9001/api/conversation/<id>/chat \
  -H "Content-Type: application/json" \
  -d '{"content":"Hello!"}'

# Stream conversation (SSE)
curl http://localhost:9001/api/conversation/<id>/stream
```

## Server Logs to Watch

When testing, monitor server output for:

- `LLM request completed` - Shows model, duration, token usage, cost
- `cache_creation_input_tokens` / `cache_read_input_tokens` - Prompt caching
- `Generated slug for conversation` - Conversation naming
- `400 Bad Request` or other errors - API failures
- `Agent message` with `end_of_turn=true` - Conversation turns completing
