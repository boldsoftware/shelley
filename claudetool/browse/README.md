# Browser Tools for Claude

This package provides browser automation tools for Claude, built using the
[chromedp](https://github.com/chromedp/chromedp) library.

## Tools

### `browser` (combined tool)

Uses an `action` field to select the operation:

| Action | Description |
|--------|-------------|
| `navigate` | Navigate to a URL and wait for the page to load |
| `eval` | Evaluate JavaScript in the browser context |
| `resize` | Resize the browser viewport |
| `screenshot` | Take a screenshot of the page or a specific element |
| `pdf` | Print the page to a PDF in `/tmp/shelley-pdfs/` |
| `console_logs` | Get recent browser console logs |
| `clear_console_logs` | Clear all captured console logs |

### `read_image` (standalone tool)

Reads an image file and encodes it for the LLM. Separate from the browser tool
because it doesn't require a browser instance.

## Usage

```go
ctx := context.Background()

tools, cleanup := browse.RegisterBrowserTools(ctx, 0)
defer cleanup()

// tools contains [browser, read_image]
for _, tool := range tools {
    agent.AddTool(tool)
}
```

## Requirements

- Chrome or Chromium must be installed on the system
- The `chromedp` package handles launching and controlling the browser

## Example Tool Input

```json
{"action": "navigate", "url": "https://example.com"}
```

```json
{"action": "eval", "expression": "document.title"}
```

```json
{"action": "screenshot", "selector": "#main"}
```

## Screenshot Storage

Screenshots are saved to `/tmp/shelley-screenshots/` with a unique UUID filename.
The web UI can fetch them using the `/api/read?path=...` endpoint.
