package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"shelley.exe.dev/llm"
)

// NetworkRequest represents a captured network request with its response metadata.
type NetworkRequest struct {
	RequestID  string  `json:"request_id"`
	URL        string  `json:"url"`
	Method     string  `json:"method"`
	Status     int64   `json:"status,omitempty"`
	StatusText string  `json:"status_text,omitempty"`
	Type       string  `json:"type,omitempty"`
	MimeType   string  `json:"mime_type,omitempty"`
	StartTime  float64 `json:"start_time"`
	EndTime    float64 `json:"end_time,omitempty"`
	Size       float64 `json:"encoded_size,omitempty"`
}

// timeToSeconds converts a *cdp.MonotonicTime to a float64 seconds value.
func timeToSeconds(t time.Time) float64 {
	return float64(t.UnixMilli()) / 1000.0
}

// captureNetworkRequest handles a RequestWillBeSent event by creating a new NetworkRequest entry.
func (b *BrowseTools) captureNetworkRequest(e *network.EventRequestWillBeSent) {
	b.networkMutex.Lock()
	defer b.networkMutex.Unlock()

	req := &NetworkRequest{
		RequestID: string(e.RequestID),
		URL:       e.Request.URL,
		Method:    e.Request.Method,
		Type:      e.Type.String(),
		StartTime: timeToSeconds(e.Timestamp.Time()),
	}
	b.networkRequests = append(b.networkRequests, req)

	maxReqs := b.maxNetworkRequests
	if maxReqs <= 0 {
		maxReqs = 200
	}
	if len(b.networkRequests) > maxReqs {
		b.networkRequests = b.networkRequests[len(b.networkRequests)-maxReqs:]
	}
}

// captureNetworkResponse handles a ResponseReceived event by updating the matching request
// with status code, status text, MIME type, and resource type.
func (b *BrowseTools) captureNetworkResponse(e *network.EventResponseReceived) {
	b.networkMutex.Lock()
	defer b.networkMutex.Unlock()

	for i := len(b.networkRequests) - 1; i >= 0; i-- {
		if b.networkRequests[i].RequestID == string(e.RequestID) {
			b.networkRequests[i].Status = e.Response.Status
			b.networkRequests[i].StatusText = e.Response.StatusText
			b.networkRequests[i].MimeType = e.Response.MimeType
			b.networkRequests[i].Type = e.Type.String()
			break
		}
	}
}

// captureNetworkFinished handles a LoadingFinished event by updating the matching request
// with encoded data length and end timestamp.
func (b *BrowseTools) captureNetworkFinished(e *network.EventLoadingFinished) {
	b.networkMutex.Lock()
	defer b.networkMutex.Unlock()

	for i := len(b.networkRequests) - 1; i >= 0; i-- {
		if b.networkRequests[i].RequestID == string(e.RequestID) {
			b.networkRequests[i].Size = e.EncodedDataLength
			b.networkRequests[i].EndTime = timeToSeconds(e.Timestamp.Time())
			break
		}
	}
}

// networkInput is the input schema for the browser_network tool.
type networkInput struct {
	Action string `json:"action"`
	Limit  int    `json:"limit,omitempty"`
	Filter string `json:"filter,omitempty"`
}

// NetworkTool returns the browser_network tool for monitoring network requests.
func (b *BrowseTools) NetworkTool() *llm.Tool {
	description := `Network monitoring and inspection. Actions: help, enable, disable, get_log, clear, cookies, clear_cache.`

	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "The network action to perform",
				"enum": ["help", "enable", "disable", "get_log", "clear", "cookies", "clear_cache"]
			},
			"limit": {
				"type": "integer",
				"description": "Max number of requests to return (get_log action, default 50)"
			},
			"filter": {
				"type": "string",
				"description": "Filter requests by URL substring (get_log action)"
			}
		},
		"required": ["action"]
	}`

	return &llm.Tool{
		Name:        "browser_network",
		Description: description,
		InputSchema: json.RawMessage(schema),
		Run:         b.networkRun,
	}
}

func (b *BrowseTools) networkRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input networkInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	switch input.Action {
	case "help":
		return b.networkHelpRun()
	case "enable":
		return b.networkEnableRun()
	case "disable":
		return b.networkDisableRun()
	case "get_log":
		return b.networkGetLogRun(input.Limit, input.Filter)
	case "clear":
		return b.networkClearRun()
	case "cookies":
		return b.networkCookiesRun()
	case "clear_cache":
		return b.networkClearCacheRun()
	default:
		return llm.ErrorfToolOut("unknown action: %q — use \"help\" to see available actions", input.Action)
	}
}

func (b *BrowseTools) networkHelpRun() llm.ToolOut {
	help := `browser_network — Monitor browser network activity.

Actions:
  enable    — Start capturing network requests. Sets up listeners for HTTP
              requests, responses, and loading events. Call this before
              navigating to the page you want to monitor.

  disable   — Stop capturing network requests. Previously captured requests
              are retained until cleared.

  get_log   — Retrieve captured network requests as JSON.
              Parameters:
                limit  (int, default 50) — max entries to return (most recent)
                filter (string)         — only include requests whose URL
                                          contains this substring
              Large outputs are written to a file and the path is returned.

  clear     — Delete all captured network requests.

  cookies   — Return all browser cookies as JSON.

  clear_cache — Clear the browser HTTP cache. Useful when testing fresh
              loads of pages or assets without restarting the browser.
              Does not affect cookies (cached content only).

Typical workflow:
  1. enable
  2. navigate to a page with the browser tool
  3. get_log to inspect requests
  4. disable when done`

	return llm.ToolOut{LLMContent: llm.TextContent(help)}
}

func (b *BrowseTools) networkEnableRun() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.networkMutex.Lock()
	alreadyEnabled := b.networkEnabled
	b.networkMutex.Unlock()

	if alreadyEnabled {
		return llm.ToolOut{LLMContent: llm.TextContent("Network monitoring is already enabled.")}
	}

	if err := chromedp.Run(browserCtx, network.Enable()); err != nil {
		return llm.ErrorfToolOut("failed to enable network monitoring: %w", err)
	}

	b.networkMutex.Lock()
	b.networkEnabled = true
	b.networkMutex.Unlock()

	return llm.ToolOut{LLMContent: llm.TextContent("Network monitoring enabled. Requests will be captured.")}
}

func (b *BrowseTools) networkDisableRun() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.networkMutex.Lock()
	wasEnabled := b.networkEnabled
	b.networkMutex.Unlock()

	if !wasEnabled {
		return llm.ToolOut{LLMContent: llm.TextContent("Network monitoring is already disabled.")}
	}

	if err := chromedp.Run(browserCtx, network.Disable()); err != nil {
		return llm.ErrorfToolOut("failed to disable network monitoring: %w", err)
	}

	b.networkMutex.Lock()
	b.networkEnabled = false
	b.networkMutex.Unlock()

	return llm.ToolOut{LLMContent: llm.TextContent("Network monitoring disabled.")}
}

func (b *BrowseTools) networkGetLogRun(limit int, filter string) llm.ToolOut {
	// Ensure browser is initialized
	_, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if limit <= 0 {
		limit = 50
	}

	b.networkMutex.Lock()
	// Copy and optionally filter
	var filtered []*NetworkRequest
	for _, req := range b.networkRequests {
		if filter != "" && !strings.Contains(req.URL, filter) {
			continue
		}
		filtered = append(filtered, req)
	}
	b.networkMutex.Unlock()

	// Apply limit (take most recent)
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}

	logData, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return llm.ErrorfToolOut("failed to serialize network requests: %w", err)
	}

	// If output exceeds threshold, write to file
	if len(logData) > ConsoleLogSizeThreshold {
		filename := fmt.Sprintf("network_log_%s.json", uuid.New().String()[:8])
		filePath := filepath.Join(ConsoleLogsDir, filename)
		if err := os.WriteFile(filePath, logData, 0o644); err != nil {
			return llm.ErrorfToolOut("failed to write network log to file: %w", err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
			"Retrieved %d network requests (%d bytes).\nOutput written to: %s\nUse `cat %s` to view the full content.",
			len(filtered), len(logData), filePath, filePath))}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Retrieved %d network requests:\n\n", len(filtered)))
	if len(filtered) == 0 {
		sb.WriteString("No network requests captured.")
		b.networkMutex.Lock()
		enabled := b.networkEnabled
		b.networkMutex.Unlock()
		if !enabled {
			sb.WriteString(` (Network monitoring is disabled — use "enable" first.)`)
		}
	} else {
		sb.Write(logData)
	}

	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

func (b *BrowseTools) networkClearRun() llm.ToolOut {
	// Ensure browser is initialized
	_, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	b.networkMutex.Lock()
	count := len(b.networkRequests)
	b.networkRequests = nil
	b.networkMutex.Unlock()

	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Cleared %d network requests.", count))}
}

func (b *BrowseTools) networkCookiesRun() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	var cookies []*network.Cookie
	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		cookies, err = network.GetCookies().Do(ctx)
		return err
	}))
	if err != nil {
		return llm.ErrorfToolOut("failed to get cookies: %w", err)
	}

	cookieData, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return llm.ErrorfToolOut("failed to serialize cookies: %w", err)
	}

	// If output exceeds threshold, write to file
	if len(cookieData) > ConsoleLogSizeThreshold {
		filename := fmt.Sprintf("cookies_%s.json", uuid.New().String()[:8])
		filePath := filepath.Join(ConsoleLogsDir, filename)
		if err := os.WriteFile(filePath, cookieData, 0o644); err != nil {
			return llm.ErrorfToolOut("failed to write cookies to file: %w", err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
			"Retrieved %d cookies (%d bytes).\nOutput written to: %s\nUse `cat %s` to view the full content.",
			len(cookies), len(cookieData), filePath, filePath))}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Retrieved %d cookies:\n\n", len(cookies)))
	if len(cookies) == 0 {
		sb.WriteString("No cookies found.")
	} else {
		sb.Write(cookieData)
	}

	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

func (b *BrowseTools) networkClearCacheRun() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if err := chromedp.Run(browserCtx, network.ClearBrowserCache()); err != nil {
		return llm.ErrorfToolOut("failed to clear browser cache: %w", err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("Browser cache cleared.")}
}
