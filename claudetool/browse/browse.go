// Package browse provides browser automation tools for the agent
package browse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/tracing"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/imageutil"
)

// ScreenshotDir is the directory where screenshots are stored
const ScreenshotDir = "/tmp/shelley-screenshots"

// DownloadDir is the directory where downloads are stored
const DownloadDir = "/tmp/shelley-downloads"

// ConsoleLogsDir is the directory where large console logs are stored
const ConsoleLogsDir = "/tmp/shelley-console-logs"

// ConsoleLogSizeThreshold is the size in bytes above which console logs are written to a file
const ConsoleLogSizeThreshold = 1024

// DefaultIdleTimeout is how long to wait before shutting down an idle browser
const DefaultIdleTimeout = 30 * time.Minute

// DownloadInfo tracks information about a completed download
type DownloadInfo struct {
	GUID              string
	URL               string
	SuggestedFilename string
	FinalPath         string
	Completed         bool
	Error             string
}

// BrowseTools contains all browser tools and manages a shared browser instance
type BrowseTools struct {
	ctx              context.Context
	allocCtx         context.Context
	allocCancel      context.CancelFunc
	browserCtx       context.Context
	browserCtxCancel context.CancelFunc
	mux              sync.Mutex
	// Map to track screenshots by ID and their creation time
	screenshots      map[string]time.Time
	screenshotsMutex sync.Mutex
	// Console logs storage
	consoleLogs      []*runtime.EventConsoleAPICalled
	consoleLogsMutex sync.Mutex
	maxConsoleLogs   int
	// Idle timeout management
	idleTimeout time.Duration
	idleTimer   *time.Timer
	// Max image dimension for resizing (0 means use default)
	maxImageDimension int
	// Download tracking
	downloads      map[string]*DownloadInfo // keyed by GUID
	downloadsMutex sync.Mutex
	downloadCond   *sync.Cond
	// Network monitoring
	networkEnabled     bool
	networkRequests    []*NetworkRequest
	networkMutex       sync.Mutex
	maxNetworkRequests int
	// Profiling state
	profilingActive bool
	tracingActive   bool
	traceEvents     []json.RawMessage
	traceCompleteCh chan struct{}
	traceMutex      sync.Mutex
}

// NewBrowseTools creates a new set of browser automation tools.
// idleTimeout is how long to wait before shutting down an idle browser (0 uses default).
// maxImageDimension is the max pixel dimension for images (0 means unlimited).
func NewBrowseTools(ctx context.Context, idleTimeout time.Duration, maxImageDimension int) *BrowseTools {
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	for _, dir := range []string{ScreenshotDir, DownloadDir, ConsoleLogsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("Failed to create directory %s: %v", dir, err)
		}
	}

	bt := &BrowseTools{
		ctx:               ctx,
		screenshots:       make(map[string]time.Time),
		consoleLogs:       make([]*runtime.EventConsoleAPICalled, 0),
		maxConsoleLogs:    100,
		maxImageDimension: maxImageDimension,
		idleTimeout:       idleTimeout,
		downloads:         make(map[string]*DownloadInfo),
	}
	bt.downloadCond = sync.NewCond(&bt.downloadsMutex)
	return bt
}

// GetBrowserContext returns the browser context, initializing if needed and resetting the idle timer.
func (b *BrowseTools) GetBrowserContext() (context.Context, error) {
	b.mux.Lock()
	defer b.mux.Unlock()

	// If browser exists, check if it's still alive
	if b.browserCtx != nil {
		// Check if the browser context has been cancelled (e.g., due to crash)
		if b.browserCtx.Err() != nil {
			log.Printf("Browser context is dead (err: %v), restarting browser", b.browserCtx.Err())
			b.closeBrowserLocked()
			// Fall through to create a new browser
		} else {
			b.resetIdleTimerLocked()
			return b.browserCtx, nil
		}
	}

	// Initialize a new browser
	opts := chromedp.DefaultExecAllocatorOptions[:]
	opts = append(opts, chromedp.NoSandbox)
	opts = append(opts, chromedp.Flag("--disable-dbus", true))
	opts = append(opts, chromedp.WSURLReadTimeout(60*time.Second))
	// Disable WebAuthn to prevent segfaults on FIDO/WebAuthn sites (issue #78)
	// Must include all default disabled features plus WebAuthentication
	// (chromedp v0.14.1 defaults: site-per-process,Translate,BlinkGenPropertyTrees)
	opts = append(opts, chromedp.Flag("disable-features",
		"site-per-process,Translate,BlinkGenPropertyTrees,WebAuthentication"))

	allocCtx, allocCancel := chromedp.NewExecAllocator(b.ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(
		allocCtx,
		chromedp.WithLogf(log.Printf),
		chromedp.WithErrorf(log.Printf),
		chromedp.WithBrowserOption(chromedp.WithDialTimeout(60*time.Second)),
	)

	// Set up event listeners for console logs, downloads, network, and tracing.
	// All listeners are registered once at browser startup and gated by enable flags.
	chromedp.ListenTarget(browserCtx, b.handleBrowserEvent)

	// Start the browser
	if err := chromedp.Run(browserCtx); err != nil {
		allocCancel()
		return nil, fmt.Errorf("failed to start browser (please apt get chromium or equivalent): %w", err)
	}

	// Set default viewport size to 1280x720 (16:9 widescreen)
	if err := chromedp.Run(browserCtx, chromedp.EmulateViewport(1280, 720)); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("failed to set default viewport: %w", err)
	}

	// Configure download behavior to allow downloads and emit events
	if err := chromedp.Run(browserCtx,
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllowAndName).
			WithDownloadPath(DownloadDir).
			WithEventsEnabled(true),
	); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("failed to configure download behavior: %w", err)
	}

	b.allocCtx = allocCtx
	b.allocCancel = allocCancel
	b.browserCtx = browserCtx
	b.browserCtxCancel = browserCancel

	b.resetIdleTimerLocked()

	return b.browserCtx, nil
}

// resetIdleTimerLocked resets or starts the idle timer. Caller must hold b.mux.
func (b *BrowseTools) resetIdleTimerLocked() {
	if b.idleTimer != nil {
		b.idleTimer.Stop()
	}
	b.idleTimer = time.AfterFunc(b.idleTimeout, b.idleShutdown)
}

// idleShutdown is called when the idle timer fires
func (b *BrowseTools) idleShutdown() {
	b.mux.Lock()
	defer b.mux.Unlock()

	if b.browserCtx == nil {
		return
	}

	log.Printf("Browser idle for %v, shutting down", b.idleTimeout)
	b.closeBrowserLocked()
}

// closeBrowserLocked shuts down the browser. Caller must hold b.mux.
// It extracts the cancel functions and clears state under the lock,
// then releases the lock to call the cancel functions (which may block
// waiting for the chrome process to exit).
func (b *BrowseTools) closeBrowserLocked() {
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}

	browserCancel := b.browserCtxCancel
	allocCancel := b.allocCancel
	b.browserCtxCancel = nil
	b.allocCancel = nil
	b.browserCtx = nil
	b.allocCtx = nil

	// Release the lock before calling cancel functions. allocCancel in
	// particular can block waiting for the chrome process to exit, and
	// holding the mux would prevent GetBrowserContext from proceeding
	// (it would see browserCtx == nil and start a new browser).
	b.mux.Unlock()
	defer b.mux.Lock()

	if browserCancel != nil {
		browserCancel()
	}
	if allocCancel != nil {
		allocCancel()
	}
}

// Close shuts down the browser
func (b *BrowseTools) Close() {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.closeBrowserLocked()
}

// handleBrowserEvent is the unified event handler for all CDP events.
func (b *BrowseTools) handleBrowserEvent(ev any) {
	switch e := ev.(type) {
	case *runtime.EventConsoleAPICalled:
		b.captureConsoleLog(e)
	case *browser.EventDownloadWillBegin:
		b.handleDownloadWillBegin(e)
	case *browser.EventDownloadProgress:
		b.handleDownloadProgress(e)
	case *network.EventRequestWillBeSent:
		b.networkMutex.Lock()
		enabled := b.networkEnabled
		b.networkMutex.Unlock()
		if enabled {
			b.captureNetworkRequest(e)
		}
	case *network.EventResponseReceived:
		b.networkMutex.Lock()
		enabled := b.networkEnabled
		b.networkMutex.Unlock()
		if enabled {
			b.captureNetworkResponse(e)
		}
	case *network.EventLoadingFinished:
		b.networkMutex.Lock()
		enabled := b.networkEnabled
		b.networkMutex.Unlock()
		if enabled {
			b.captureNetworkFinished(e)
		}
	case *tracing.EventDataCollected:
		b.traceMutex.Lock()
		if b.tracingActive {
			for _, v := range e.Value {
				b.traceEvents = append(b.traceEvents, json.RawMessage(v))
			}
		}
		b.traceMutex.Unlock()
	case *tracing.EventTracingComplete:
		b.traceMutex.Lock()
		if b.traceCompleteCh != nil {
			select {
			case b.traceCompleteCh <- struct{}{}:
			default:
			}
		}
		b.traceMutex.Unlock()
	}
}

// navigateInput is the input for the navigate action.
type navigateInput struct {
	URL     string `json:"url"`
	Timeout string `json:"timeout,omitempty"`
}

// isPort80 reports whether urlStr definitely uses port 80.
func isPort80(urlStr string) bool {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}
	port := parsedURL.Port()
	return port == "80" || (port == "" && parsedURL.Scheme == "http")
}

func (b *BrowseTools) navigateRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input navigateInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	if isPort80(input.URL) {
		return llm.ErrorToolOut(fmt.Errorf("port 80 is not the port you're looking for--port 80 is the main sketch server"))
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(input.URL),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		// Navigation to download URLs fails with ERR_ABORTED, but the download may have succeeded.
		// Wait briefly for download events to be processed, then check if we got any downloads.
		if strings.Contains(err.Error(), "net::ERR_ABORTED") {
			time.Sleep(500 * time.Millisecond)
			downloads := b.GetRecentDownloads()
			if len(downloads) > 0 {
				// Download succeeded - report it instead of error
				var sb strings.Builder
				sb.WriteString("Navigation triggered download(s):")
				for _, d := range downloads {
					if d.Error != "" {
						sb.WriteString(fmt.Sprintf("\n  - %s (from %s): ERROR: %s", d.SuggestedFilename, d.URL, d.Error))
					} else {
						sb.WriteString(fmt.Sprintf("\n  - %s (from %s) saved to: %s", d.SuggestedFilename, d.URL, d.FinalPath))
					}
				}
				return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
			}
		}
		return llm.ErrorToolOut(err)
	}

	return b.toolOutWithDownloads("done")
}

type resizeInput struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Timeout string `json:"timeout,omitempty"`
}

func (b *BrowseTools) resizeRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input resizeInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	if input.Width <= 0 || input.Height <= 0 {
		return llm.ErrorToolOut(fmt.Errorf("invalid dimensions: width and height must be positive"))
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	err = chromedp.Run(timeoutCtx,
		chromedp.EmulateViewport(int64(input.Width), int64(input.Height)),
	)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("done")}
}

type evalInput struct {
	Expression string `json:"expression"`
	Timeout    string `json:"timeout,omitempty"`
	Await      *bool  `json:"await,omitempty"`
}

func (b *BrowseTools) evalRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input evalInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	var result any
	var evalOps []chromedp.EvaluateOption

	await := true
	if input.Await != nil {
		await = *input.Await
	}
	if await {
		evalOps = append(evalOps, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		})
	}

	evalAction := chromedp.Evaluate(input.Expression, &result, evalOps...)

	err = chromedp.Run(timeoutCtx, evalAction)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Return the result as JSON
	response, err := json.Marshal(result)
	if err != nil {
		return llm.ErrorfToolOut("failed to marshal response: %w", err)
	}

	// If output exceeds threshold, write to file
	if len(response) > ConsoleLogSizeThreshold {
		filename := fmt.Sprintf("js_result_%s.json", uuid.New().String()[:8])
		filePath := filepath.Join(ConsoleLogsDir, filename)
		if err := os.WriteFile(filePath, response, 0o644); err != nil {
			return llm.ErrorfToolOut("failed to write JS result to file: %w", err)
		}
		return b.toolOutWithDownloads(fmt.Sprintf(
			"JavaScript result (%d bytes) written to: %s\nUse `cat %s` to view the full content.",
			len(response), filePath, filePath))
	}

	return b.toolOutWithDownloads("<javascript_result>" + string(response) + "</javascript_result>")
}

type screenshotInput struct {
	Selector string `json:"selector,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

func (b *BrowseTools) screenshotRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input screenshotInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Try to get a browser context; if unavailable, return an error
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	var buf []byte
	var actions []chromedp.Action

	if input.Selector != "" {
		// Take screenshot of specific element
		actions = append(actions,
			chromedp.WaitReady(input.Selector),
			chromedp.Screenshot(input.Selector, &buf, chromedp.NodeVisible),
		)
	} else {
		// Take full page screenshot
		actions = append(actions, chromedp.CaptureScreenshot(&buf))
	}

	err = chromedp.Run(timeoutCtx, actions...)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Save the screenshot and get its ID for potential future reference
	id := b.SaveScreenshot(buf)
	if id == "" {
		return llm.ErrorToolOut(fmt.Errorf("failed to save screenshot"))
	}

	// Get the full path to the screenshot
	screenshotPath := GetScreenshotPath(id)

	// Resize image if needed to fit within model's image dimension limits
	imageData := buf
	format := "png"
	resized := false
	if b.maxImageDimension > 0 {
		var err error
		imageData, format, resized, err = imageutil.ResizeImage(buf, b.maxImageDimension)
		if err != nil {
			return llm.ErrorToolOut(fmt.Errorf("failed to resize screenshot: %w", err))
		}
	}

	base64Data := base64.StdEncoding.EncodeToString(imageData)
	mediaType := "image/" + format

	display := map[string]any{
		"type":     "screenshot",
		"id":       id,
		"url":      "/api/read?path=" + url.QueryEscape(screenshotPath),
		"path":     screenshotPath,
		"selector": input.Selector,
	}

	description := fmt.Sprintf("Screenshot taken (saved as %s)", screenshotPath)
	if resized {
		description += " [resized]"
	}

	return llm.ToolOut{LLMContent: []llm.Content{
		{
			Type: llm.ContentTypeText,
			Text: description,
		},
		{
			Type:      llm.ContentTypeText,
			MediaType: mediaType,
			Data:      base64Data,
		},
	}, Display: display}
}

// GetTools returns all browser tools.
func (b *BrowseTools) GetTools() []*llm.Tool {
	return []*llm.Tool{
		b.CombinedTool(),
		b.ReadImageTool(),
		b.EmulateTool(),
		b.NetworkTool(),
		b.AccessibilityTool(),
		b.ProfileTool(),
	}
}

// CombinedTool returns a single tool that handles all browser actions via an "action" field.
func (b *BrowseTools) CombinedTool() *llm.Tool {
	description := `Browser automation tool. Use the "action" field to select an operation:

- action: "navigate"
  Navigate the browser to a specific URL and wait for page to load.
  Parameters: url (string, required), timeout (string, optional)

- action: "eval"
  Evaluate JavaScript in the browser context. Your go-to for interacting with content: clicking buttons, typing, getting content, scrolling, waiting for content/selector to be ready, etc.
  Parameters: expression (string, required), timeout (string, optional), await (boolean, default true)

- action: "resize"
  Resize the browser viewport to a specific width and height.
  Parameters: width (integer, required), height (integer, required), timeout (string, optional)

- action: "screenshot"
  Take a screenshot of the page or a specific element.
  Parameters: selector (string, optional), timeout (string, optional)

- action: "console_logs"
  Get recent browser console logs.
  Parameters: limit (integer, optional, default 100)

- action: "clear_console_logs"
  Clear all captured browser console logs.
  No additional parameters.`

	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "The browser action to perform",
				"enum": ["navigate", "eval", "resize", "screenshot", "console_logs", "clear_console_logs"]
			},
			"url": {
				"type": "string",
				"description": "URL to navigate to (navigate action)"
			},
			"expression": {
				"type": "string",
				"description": "JavaScript expression to evaluate (eval action)"
			},
			"await": {
				"type": "boolean",
				"description": "Wait for promises to resolve (eval action, default true)"
			},
			"width": {
				"type": "integer",
				"description": "Viewport width in pixels (resize action)"
			},
			"height": {
				"type": "integer",
				"description": "Viewport height in pixels (resize action)"
			},
			"limit": {
				"type": "integer",
				"description": "Max log entries to return (console_logs action, default 100)"
			},
			"selector": {
				"type": "string",
				"description": "CSS selector for element to screenshot (screenshot action)"
			},
			"timeout": {
				"type": "string",
				"description": "Timeout as a Go duration string (default: 15s)"
			}
		},
		"required": ["action"]
	}`

	return &llm.Tool{
		Name:        "browser",
		Description: description,
		InputSchema: json.RawMessage(schema),
		Run:         b.combinedRun(),
	}
}

// ReadImageTool returns a standalone tool for reading image files.
func (b *BrowseTools) ReadImageTool() *llm.Tool {
	return &llm.Tool{
		Name:        "read_image",
		Description: "Read an image file (such as a screenshot) and encode it for sending to the LLM",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the image file to read"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			},
			"required": ["path"]
		}`),
		Run: b.readImageRun,
	}
}

// combinedInput is the unified input for the combined browser tool.
type combinedInput struct {
	Action     string `json:"action"`
	URL        string `json:"url,omitempty"`
	Expression string `json:"expression,omitempty"`
	Await      *bool  `json:"await,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
}

func (b *BrowseTools) combinedRun() func(context.Context, json.RawMessage) llm.ToolOut {
	return func(ctx context.Context, m json.RawMessage) llm.ToolOut {
		var input combinedInput
		if err := json.Unmarshal(m, &input); err != nil {
			return llm.ErrorfToolOut("invalid input: %w", err)
		}

		switch input.Action {
		case "navigate":
			return b.navigateRun(ctx, m)
		case "eval":
			return b.evalRun(ctx, m)
		case "resize":
			return b.resizeRun(ctx, m)
		case "screenshot":
			return b.screenshotRun(ctx, m)
		case "console_logs":
			return b.recentConsoleLogsRun(ctx, m)
		case "clear_console_logs":
			return b.clearConsoleLogsRun(ctx, m)
		default:
			return llm.ErrorfToolOut("unknown action: %q", input.Action)
		}
	}
}

// SaveScreenshot saves a screenshot to disk and returns its ID
func (b *BrowseTools) SaveScreenshot(data []byte) string {
	// Generate a unique ID
	id := uuid.New().String()

	// Save the file
	filePath := filepath.Join(ScreenshotDir, id+".png")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("Failed to save screenshot: %v", err)
		return ""
	}

	// Track this screenshot
	b.screenshotsMutex.Lock()
	b.screenshots[id] = time.Now()
	b.screenshotsMutex.Unlock()

	return id
}

// GetScreenshotPath returns the full path to a screenshot by ID
func GetScreenshotPath(id string) string {
	return filepath.Join(ScreenshotDir, id+".png")
}

type readImageInput struct {
	Path    string `json:"path"`
	Timeout string `json:"timeout,omitempty"`
}

func (b *BrowseTools) readImageRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input readImageInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Check if the path exists
	if _, err := os.Stat(input.Path); os.IsNotExist(err) {
		return llm.ErrorfToolOut("image file not found: %s", input.Path)
	}

	// Read the file
	imageData, err := os.ReadFile(input.Path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read image file: %w", err)
	}

	// Convert HEIC to PNG if needed (Go's image library doesn't support HEIC)
	converted := false
	if imageutil.IsHEIC(imageData) {
		imageData, err = imageutil.ConvertHEICToPNG(imageData)
		if err != nil {
			return llm.ErrorfToolOut("failed to convert HEIC image: %w", err)
		}
		converted = true
	}

	detectedType := http.DetectContentType(imageData)
	if !strings.HasPrefix(detectedType, "image/") {
		return llm.ErrorfToolOut("file is not an image: %s", detectedType)
	}

	// Resize image if needed to fit within model's image dimension limits
	resized := false
	format := strings.TrimPrefix(detectedType, "image/")
	if b.maxImageDimension > 0 {
		var err error
		imageData, format, resized, err = imageutil.ResizeImage(imageData, b.maxImageDimension)
		if err != nil {
			return llm.ErrorToolOut(fmt.Errorf("failed to resize image: %w", err))
		}
	}

	base64Data := base64.StdEncoding.EncodeToString(imageData)
	mediaType := "image/" + format

	description := fmt.Sprintf("Image from %s (type: %s)", input.Path, mediaType)
	if converted {
		description += " [converted from HEIC]"
	}
	if resized {
		description += " [resized]"
	}

	return llm.ToolOut{LLMContent: []llm.Content{
		{
			Type: llm.ContentTypeText,
			Text: description,
		},
		{
			Type:      llm.ContentTypeText,
			MediaType: mediaType,
			Data:      base64Data,
		},
	}}
}

// parseTimeout parses a timeout string and returns a time.Duration
// It returns a default of 5 seconds if the timeout is empty or invalid
func parseTimeout(timeout string) time.Duration {
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return 15 * time.Second
	}
	return dur
}

// captureConsoleLog captures a console log event and stores it
func (b *BrowseTools) captureConsoleLog(e *runtime.EventConsoleAPICalled) {
	// Add to logs with mutex protection
	b.consoleLogsMutex.Lock()
	defer b.consoleLogsMutex.Unlock()

	// Add the log and maintain max size
	b.consoleLogs = append(b.consoleLogs, e)
	if len(b.consoleLogs) > b.maxConsoleLogs {
		b.consoleLogs = b.consoleLogs[len(b.consoleLogs)-b.maxConsoleLogs:]
	}
}

// handleDownloadWillBegin handles the browser download start event
func (b *BrowseTools) handleDownloadWillBegin(e *browser.EventDownloadWillBegin) {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()

	b.downloads[e.GUID] = &DownloadInfo{
		GUID:              e.GUID,
		URL:               e.URL,
		SuggestedFilename: e.SuggestedFilename,
	}
}

// handleDownloadProgress handles the browser download progress event
func (b *BrowseTools) handleDownloadProgress(e *browser.EventDownloadProgress) {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()

	info, ok := b.downloads[e.GUID]
	if !ok {
		// Download started before we started tracking, create entry
		info = &DownloadInfo{GUID: e.GUID}
		b.downloads[e.GUID] = info
	}

	switch e.State {
	case browser.DownloadProgressStateCompleted:
		info.Completed = true
		// The file is downloaded with GUID as filename, rename to suggested filename with random suffix
		guidPath := filepath.Join(DownloadDir, e.GUID)
		finalName := b.generateDownloadFilename(info.SuggestedFilename)
		finalPath := filepath.Join(DownloadDir, finalName)
		// Retry rename a few times as file might still be being written
		var renamed bool
		for i := 0; i < 10; i++ {
			if err := os.Rename(guidPath, finalPath); err == nil {
				info.FinalPath = finalPath
				renamed = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !renamed {
			// File might have different path or couldn't be renamed
			if e.FilePath != "" {
				info.FinalPath = e.FilePath
			} else {
				info.FinalPath = guidPath
			}
		}
		b.downloadCond.Broadcast()
	case browser.DownloadProgressStateCanceled:
		info.Completed = true
		info.Error = "download canceled"
		b.downloadCond.Broadcast()
	}
}

// generateDownloadFilename creates a filename with randomness
func (b *BrowseTools) generateDownloadFilename(suggested string) string {
	if suggested == "" {
		suggested = "download"
	}
	// Extract extension if present
	ext := filepath.Ext(suggested)
	base := strings.TrimSuffix(suggested, ext)
	// Add random suffix
	randomSuffix := uuid.New().String()[:8]
	return fmt.Sprintf("%s_%s%s", base, randomSuffix, ext)
}

// GetRecentDownloads returns download info for recently completed downloads and clears the list
func (b *BrowseTools) GetRecentDownloads() []*DownloadInfo {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()

	var completed []*DownloadInfo
	for guid, info := range b.downloads {
		if info.Completed {
			completed = append(completed, info)
			delete(b.downloads, guid)
		}
	}
	return completed
}

// toolOutWithDownloads creates a tool output that includes any completed downloads
func (b *BrowseTools) toolOutWithDownloads(message string) llm.ToolOut {
	downloads := b.GetRecentDownloads()
	if len(downloads) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent(message)}
	}

	var sb strings.Builder
	sb.WriteString(message)
	sb.WriteString("\n\nDownloads completed:")
	for _, d := range downloads {
		if d.Error != "" {
			sb.WriteString(fmt.Sprintf("\n  - %s (from %s): ERROR: %s", d.SuggestedFilename, d.URL, d.Error))
		} else {
			sb.WriteString(fmt.Sprintf("\n  - %s (from %s) saved to: %s", d.SuggestedFilename, d.URL, d.FinalPath))
		}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

type recentConsoleLogsInput struct {
	Limit int `json:"limit,omitempty"`
}

func (b *BrowseTools) recentConsoleLogsRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input recentConsoleLogsInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Ensure browser is initialized
	_, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Apply limit (default to 100 if not specified)
	limit := 100
	if input.Limit > 0 {
		limit = input.Limit
	}

	// Get console logs with mutex protection
	b.consoleLogsMutex.Lock()
	logs := make([]*runtime.EventConsoleAPICalled, 0, len(b.consoleLogs))
	start := 0
	if len(b.consoleLogs) > limit {
		start = len(b.consoleLogs) - limit
	}
	logs = append(logs, b.consoleLogs[start:]...)
	b.consoleLogsMutex.Unlock()

	// Format the logs as JSON
	logData, err := json.MarshalIndent(logs, "", "  ")
	if err != nil {
		return llm.ErrorfToolOut("failed to serialize logs: %w", err)
	}

	// If output exceeds threshold, write to file
	if len(logData) > ConsoleLogSizeThreshold {
		filename := fmt.Sprintf("console_logs_%s.json", uuid.New().String()[:8])
		filePath := filepath.Join(ConsoleLogsDir, filename)
		if err := os.WriteFile(filePath, logData, 0o644); err != nil {
			return llm.ErrorfToolOut("failed to write console logs to file: %w", err)
		}
		return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
			"Retrieved %d console log entries (%d bytes).\nOutput written to: %s\nUse `cat %s` to view the full content.",
			len(logs), len(logData), filePath, filePath))}
	}

	// Format the logs
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Retrieved %d console log entries:\n\n", len(logs)))

	if len(logs) == 0 {
		sb.WriteString("No console logs captured.")
	} else {
		// Add the JSON data for full details
		sb.WriteString(string(logData))
	}

	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

type clearConsoleLogsInput struct{}

func (b *BrowseTools) clearConsoleLogsRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input clearConsoleLogsInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Ensure browser is initialized
	_, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Clear console logs with mutex protection
	b.consoleLogsMutex.Lock()
	logCount := len(b.consoleLogs)
	b.consoleLogs = make([]*runtime.EventConsoleAPICalled, 0)
	b.consoleLogsMutex.Unlock()

	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Cleared %d console log entries.", logCount))}
}
