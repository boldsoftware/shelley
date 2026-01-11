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
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/imageutil"
)

// ScreenshotDir is the directory where screenshots are stored
const ScreenshotDir = "/tmp/shelley-screenshots"

// DownloadDir is the directory where browser downloads are stored
const DownloadDir = "/tmp/shelley-downloads"

// DefaultIdleTimeout is how long to wait before shutting down an idle browser
const DefaultIdleTimeout = 30 * time.Minute

// downloadInfo tracks the state of an in-progress or completed download
type downloadInfo struct {
	GUID              string
	URL               string
	SuggestedFilename string
	FilePath          string
	State             browser.DownloadProgressState
	TotalBytes        float64
	ReceivedBytes     float64
	Done              chan struct{}
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
	// Download tracking
	downloads      map[string]*downloadInfo // keyed by GUID
	downloadsMutex sync.Mutex
	// Idle timeout management
	idleTimeout time.Duration
	idleTimer   *time.Timer
	// Max image dimension for resizing (0 means use default)
	maxImageDimension int
}

// NewBrowseTools creates a new set of browser automation tools.
// idleTimeout is how long to wait before shutting down an idle browser (0 uses default).
// maxImageDimension is the max pixel dimension for images (0 means unlimited).
func NewBrowseTools(ctx context.Context, idleTimeout time.Duration, maxImageDimension int) *BrowseTools {
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	if err := os.MkdirAll(ScreenshotDir, 0o755); err != nil {
		log.Printf("Failed to create screenshot directory: %v", err)
	}
	if err := os.MkdirAll(DownloadDir, 0o755); err != nil {
		log.Printf("Failed to create download directory: %v", err)
	}

	return &BrowseTools{
		ctx:               ctx,
		screenshots:       make(map[string]time.Time),
		consoleLogs:       make([]*runtime.EventConsoleAPICalled, 0),
		maxConsoleLogs:    100,
		downloads:         make(map[string]*downloadInfo),
		maxImageDimension: maxImageDimension,
		idleTimeout:       idleTimeout,
	}
}

// GetBrowserContext returns the browser context, initializing if needed and resetting the idle timer.
func (b *BrowseTools) GetBrowserContext() (context.Context, error) {
	b.mux.Lock()
	defer b.mux.Unlock()

	// If browser exists, reset idle timer and return
	if b.browserCtx != nil {
		b.resetIdleTimerLocked()
		return b.browserCtx, nil
	}

	// Initialize a new browser
	opts := chromedp.DefaultExecAllocatorOptions[:]
	opts = append(opts, chromedp.NoSandbox)
	opts = append(opts, chromedp.Flag("--disable-dbus", true))
	opts = append(opts, chromedp.WSURLReadTimeout(60*time.Second))

	allocCtx, allocCancel := chromedp.NewExecAllocator(b.ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(
		allocCtx,
		chromedp.WithLogf(log.Printf),
		chromedp.WithErrorf(log.Printf),
		chromedp.WithBrowserOption(chromedp.WithDialTimeout(60*time.Second)),
	)

	// Set up console log and download event listeners
	chromedp.ListenTarget(browserCtx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			b.captureConsoleLog(e)
		case *browser.EventDownloadWillBegin:
			b.handleDownloadWillBegin(e)
		case *browser.EventDownloadProgress:
			b.handleDownloadProgress(e)
		}
	})

	// Start the browser
	if err := chromedp.Run(browserCtx); err != nil {
		allocCancel()
		return nil, fmt.Errorf("failed to start browser (please apt get chromium or equivalent): %w", err)
	}

	// Configure download behavior - allow downloads and save to our directory
	if err := chromedp.Run(browserCtx,
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllowAndName).
			WithDownloadPath(DownloadDir).
			WithEventsEnabled(true),
	); err != nil {
		log.Printf("Warning: failed to configure download behavior: %v", err)
	}

	// Set default viewport size to 1280x720 (16:9 widescreen)
	if err := chromedp.Run(browserCtx, chromedp.EmulateViewport(1280, 720)); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("failed to set default viewport: %w", err)
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
func (b *BrowseTools) closeBrowserLocked() {
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}

	if b.browserCtxCancel != nil {
		b.browserCtxCancel()
		b.browserCtxCancel = nil
	}

	if b.allocCancel != nil {
		b.allocCancel()
		b.allocCancel = nil
	}

	b.browserCtx = nil
	b.allocCtx = nil
}

// Close shuts down the browser
func (b *BrowseTools) Close() {
	b.mux.Lock()
	defer b.mux.Unlock()
	b.closeBrowserLocked()
}

// NavigateTool definition
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

// NewNavigateTool creates a tool for navigating to URLs
func (b *BrowseTools) NewNavigateTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_navigate",
		Description: "Navigate the browser to a specific URL and wait for page to load",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "The URL to navigate to"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			},
			"required": ["url"]
		}`),
		Run: b.navigateRun,
	}
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

	// Clear any previous download tracking before navigating
	b.clearDownloads()

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	// Use page.Navigate directly to get the isDownload flag
	var isDownload bool
	var errorText string
	err = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, errorText, isDownload, err = page.Navigate(input.URL).Do(ctx)
		return err
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	if errorText != "" && !isDownload {
		return llm.ErrorToolOut(fmt.Errorf("navigation failed: %s", errorText))
	}

	// If this triggered a download, wait for it to complete
	if isDownload {
		downloadCtx, downloadCancel := context.WithTimeout(browserCtx, 60*time.Second)
		defer downloadCancel()

		downloadInfo, downloadErr := b.waitForDownload(downloadCtx)
		if downloadErr != nil {
			return llm.ErrorToolOut(fmt.Errorf("download failed: %w", downloadErr))
		}
		return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
			"File downloaded to: %s (%.0f bytes)",
			downloadInfo.FilePath,
			downloadInfo.TotalBytes,
		))}
	}

	// Normal page load - wait for body to be ready
	err = chromedp.Run(timeoutCtx, chromedp.WaitReady("body"))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("done")}
}

// ResizeTool definition
type resizeInput struct {
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Timeout string `json:"timeout,omitempty"`
}

// NewResizeTool creates a tool for resizing the browser viewport
func (b *BrowseTools) NewResizeTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_resize",
		Description: "Resize the browser viewport to a specific width and height",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"width": {
					"type": "integer",
					"description": "Viewport width in pixels"
				},
				"height": {
					"type": "integer",
					"description": "Viewport height in pixels"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			},
			"required": ["width", "height"]
		}`),
		Run: b.resizeRun,
	}
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

// EvalTool definition
type evalInput struct {
	Expression string `json:"expression"`
	Timeout    string `json:"timeout,omitempty"`
	Await      *bool  `json:"await,omitempty"`
}

// NewEvalTool creates a tool for evaluating JavaScript
func (b *BrowseTools) NewEvalTool() *llm.Tool {
	return &llm.Tool{
		Name: "browser_eval",
		Description: `Evaluate JavaScript in the browser context.
Your go-to tool for interacting with content: clicking buttons, typing, getting content, scrolling, resizing, waiting for content/selector to be ready, etc.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"expression": {
					"type": "string",
					"description": "JavaScript expression to evaluate"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				},
				"await": {
					"type": "boolean",
					"description": "If true, wait for promises to resolve and return their resolved value (default: true)"
				}
			},
			"required": ["expression"]
		}`),
		Run: b.evalRun,
	}
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

	return llm.ToolOut{LLMContent: llm.TextContent("<javascript_result>" + string(response) + "</javascript_result>")}
}

// ScreenshotTool definition
type screenshotInput struct {
	Selector string `json:"selector,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

// NewScreenshotTool creates a tool for taking screenshots
func (b *BrowseTools) NewScreenshotTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_take_screenshot",
		Description: "Take a screenshot of the page or a specific element",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"selector": {
					"type": "string",
					"description": "CSS selector for the element to screenshot (optional)"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			}
		}`),
		Run: b.screenshotRun,
	}
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

// GetTools returns browser tools, optionally filtering out screenshot-related tools
func (b *BrowseTools) GetTools(includeScreenshotTools bool) []*llm.Tool {
	tools := []*llm.Tool{
		b.NewNavigateTool(),
		b.NewEvalTool(),
		b.NewResizeTool(),
		b.NewRecentConsoleLogsTool(),
		b.NewClearConsoleLogsTool(),
	}

	// Add screenshot-related tools if supported
	if includeScreenshotTools {
		tools = append(tools, b.NewScreenshotTool())
		tools = append(tools, b.NewReadImageTool())
	}

	return tools
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

// ReadImageTool definition
type readImageInput struct {
	Path    string `json:"path"`
	Timeout string `json:"timeout,omitempty"`
}

// NewReadImageTool creates a tool for reading images and returning them as base64 encoded data
func (b *BrowseTools) NewReadImageTool() *llm.Tool {
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

// handleDownloadWillBegin is called when a download starts
func (b *BrowseTools) handleDownloadWillBegin(e *browser.EventDownloadWillBegin) {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()

	b.downloads[e.GUID] = &downloadInfo{
		GUID:              e.GUID,
		URL:               e.URL,
		SuggestedFilename: e.SuggestedFilename,
		State:             browser.DownloadProgressStateInProgress,
		Done:              make(chan struct{}),
	}
	log.Printf("Download started: %s -> %s", e.URL, e.SuggestedFilename)
}

// uniqueFilePath returns a unique file path, adding (1), (2), etc. if the file exists
// e.g., "file.pdf" -> "file (1).pdf" -> "file (2).pdf"
func uniqueFilePath(dir, filename string) string {
	path := filepath.Join(dir, filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)

	for i := 1; ; i++ {
		path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
	}
}

// handleDownloadProgress is called as download progresses and completes
func (b *BrowseTools) handleDownloadProgress(e *browser.EventDownloadProgress) {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()

	info, ok := b.downloads[e.GUID]
	if !ok {
		return
	}

	info.State = e.State
	info.TotalBytes = e.TotalBytes
	info.ReceivedBytes = e.ReceivedBytes

	if e.State == browser.DownloadProgressStateCompleted {
		// Chrome saves the file with the GUID as filename in DownloadDir
		// Move it to the suggested filename, adding (1), (2), etc. if needed
		guidPath := filepath.Join(DownloadDir, e.GUID)
		finalPath := uniqueFilePath(DownloadDir, info.SuggestedFilename)

		if err := os.Rename(guidPath, finalPath); err != nil {
			log.Printf("Failed to rename download %s to %s: %v", guidPath, finalPath, err)
			finalPath = guidPath // Fall back to GUID path
		}

		info.FilePath = finalPath
		log.Printf("Download completed: %s", finalPath)
		close(info.Done)
	} else if e.State == browser.DownloadProgressStateCanceled {
		log.Printf("Download canceled: %s", info.URL)
		close(info.Done)
	}
}

// waitForDownload waits for any pending download to complete and returns its info
func (b *BrowseTools) waitForDownload(ctx context.Context) (*downloadInfo, error) {
	// Poll for download to start or complete
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		b.downloadsMutex.Lock()
		var pending *downloadInfo
		var completed *downloadInfo
		for _, info := range b.downloads {
			if info.State == browser.DownloadProgressStateInProgress {
				pending = info
				break
			}
			if info.State == browser.DownloadProgressStateCompleted && info.FilePath != "" {
				// Track most recent completed download
				if completed == nil {
					completed = info
				}
			}
		}
		b.downloadsMutex.Unlock()

		// If we have a pending download, wait for it
		if pending != nil {
			select {
			case <-pending.Done:
				if pending.State == browser.DownloadProgressStateCanceled {
					return nil, fmt.Errorf("download was canceled")
				}
				return pending, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// If we have a recently completed download, return it
		if completed != nil {
			return completed, nil
		}

		// Wait before checking again, or timeout
		select {
		case <-ticker.C:
			// Continue polling
		case <-ctx.Done():
			return nil, fmt.Errorf("no download detected: %w", ctx.Err())
		}
	}
}

// getMostRecentDownload returns the most recently completed download
func (b *BrowseTools) getMostRecentDownload() *downloadInfo {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()

	var mostRecent *downloadInfo
	for _, info := range b.downloads {
		if info.State == browser.DownloadProgressStateCompleted {
			if mostRecent == nil || info.FilePath > mostRecent.FilePath {
				mostRecent = info
			}
		}
	}
	return mostRecent
}

// clearDownloads removes all tracked downloads
func (b *BrowseTools) clearDownloads() {
	b.downloadsMutex.Lock()
	defer b.downloadsMutex.Unlock()
	b.downloads = make(map[string]*downloadInfo)
}

// RecentConsoleLogsTool definition
type recentConsoleLogsInput struct {
	Limit int `json:"limit,omitempty"`
}

// NewRecentConsoleLogsTool creates a tool for retrieving recent console logs
func (b *BrowseTools) NewRecentConsoleLogsTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_recent_console_logs",
		Description: "Get recent browser console logs",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit": {
					"type": "integer",
					"description": "Maximum number of log entries to return (default: 100)"
				}
			}
		}`),
		Run: b.recentConsoleLogsRun,
	}
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

// ClearConsoleLogsTool definition
type clearConsoleLogsInput struct{}

// NewClearConsoleLogsTool creates a tool for clearing console logs
func (b *BrowseTools) NewClearConsoleLogsTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_clear_console_logs",
		Description: "Clear all captured browser console logs",
		InputSchema: llm.EmptySchema(),
		Run:         b.clearConsoleLogsRun,
	}
}

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
