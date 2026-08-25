package browse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
	"shelley.exe.dev/llm"
)

func TestNetworkRequestsSnapshotCopiesValues(t *testing.T) {
	request := &NetworkRequest{RequestID: "1", URL: "https://example.com/before"}
	tools := &BrowseTools{networkRequests: []*NetworkRequest{request}}

	snapshot := tools.networkRequestsSnapshot("example.com")
	if len(snapshot) != 1 {
		t.Fatalf("snapshot length = %d, want 1", len(snapshot))
	}
	tools.networkMutex.Lock()
	request.URL = "https://example.com/after"
	tools.networkMutex.Unlock()
	if snapshot[0].URL != "https://example.com/before" {
		t.Fatalf("snapshot changed after source mutation: %q", snapshot[0].URL)
	}
}

func TestCombinedTool(t *testing.T) {
	tools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	if tool.Name != "browser" {
		t.Errorf("expected name %q, got %q", "browser", tool.Name)
	}

	// Verify schema has action as required
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}
	if !slices.Contains(schema.Required, "action") {
		t.Error("action should be required")
	}

	// Verify all actions are listed in the enum, including the folded-in
	// emulate_*/network_*/accessibility_*/profile_* families.
	expectedActions := []string{
		"navigate", "eval", "resize", "console_logs", "clear_console_logs", "screenshot",
		"emulate_device", "emulate_custom", "emulate_reset", "emulate_dark_mode", "emulate_media",
		"network_enable", "network_get_log", "network_cookies", "network_clear_cache",
		"accessibility_tree", "accessibility_query", "accessibility_node",
		"profile_metrics", "profile_cpu_start", "profile_trace_start", "profile_coverage_start",
	}
	for _, action := range expectedActions {
		if !slices.Contains(schema.Properties["action"].Enum, action) {
			t.Errorf("action %q not in enum", action)
		}
	}

	// Verify description mentions all action families.
	for _, keyword := range []string{"navigate", "eval", "resize", "console_logs", "screenshot", "emulate_", "network_", "accessibility_", "profile_"} {
		if !strings.Contains(tool.Description, keyword) {
			t.Errorf("description missing %q", keyword)
		}
	}
}

// TestCombinedToolFoldedActions verifies the help actions of the folded-in
// families dispatch through the combined browser tool without needing a live
// browser (help actions are pure text).
func TestCombinedToolFoldedActions(t *testing.T) {
	tools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() { tools.Close() })

	tool := tools.CombinedTool()
	for _, action := range []string{"emulate_help", "network_help", "accessibility_help", "profile_help"} {
		out := tool.Run(context.Background(), []byte(fmt.Sprintf(`{"action": %q}`, action)))
		if out.Error != nil {
			t.Errorf("action %q returned error: %v", action, out.Error)
		}
		if len(out.LLMContent) == 0 || out.LLMContent[0].Text == "" {
			t.Errorf("action %q returned no help text", action)
		}
	}
}

func TestCombinedToolUnknownAction(t *testing.T) {
	tools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()
	toolOut := tool.Run(context.Background(), []byte(`{"action": "bogus"}`))
	if toolOut.Error == nil {
		t.Error("Expected error for unknown action")
	}
}

func TestGetTools(t *testing.T) {
	tools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() {
		tools.Close()
	})

	result := tools.GetTools()
	if len(result) != 2 {
		t.Fatalf("GetTools: expected 2 tools, got %d", len(result))
	}
	expectedNames := []string{"browser", "read_image"}
	for i, name := range expectedNames {
		if result[i].Name != name {
			t.Errorf("expected tool %d name %q, got %q", i, name, result[i].Name)
		}
	}
}

// TestBrowserInitialization verifies that the browser can start correctly
func TestBrowserInitialization(t *testing.T) {
	// Skip long tests in short mode
	if testing.Short() {
		t.Skip("skipping browser initialization test in short mode")
	}

	// Create browser tools instance
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Get browser context (this initializes the browser)
	browserCtx, err := tools.GetBrowserContext()
	if err != nil {
		if strings.Contains(err.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Failed to get browser context: %v", err)
	}

	// Try to navigate to a simple page
	var title string
	err = chromedp.Run(
		browserCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Title(&title),
	)
	if err != nil {
		t.Fatalf("Failed to navigate to about:blank: %v", err)
	}

	t.Logf("Successfully navigated to about:blank, title: %q", title)
}

// TestNavigateTool verifies that the navigate action works correctly
func TestNavigateTool(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping navigate tool test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	inputJSON := []byte(`{"action": "navigate", "url": "https://example.com"}`)
	toolOut := tool.Run(ctx, inputJSON)
	if toolOut.Error != nil {
		t.Fatalf("Error running navigate: %v", toolOut.Error)
	}

	resultText := toolOut.LLMContent[0].Text
	if !strings.Contains(resultText, "done") {
		if strings.Contains(resultText, "browser automation not available") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Expected done in result text, got: %s", resultText)
	}

	browserCtx, err := tools.GetBrowserContext()
	if err != nil {
		if strings.Contains(err.Error(), "browser automation not available") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Failed to get browser context: %v", err)
	}

	var title string
	err = chromedp.Run(browserCtx, chromedp.Title(&title))
	if err != nil {
		t.Fatalf("Failed to get page title: %v", err)
	}

	if title != "Example Domain" {
		t.Errorf("Expected title 'Example Domain', got '%s'", title)
	}
}

// TestScreenshotTool tests that the screenshot tool properly saves files
func TestScreenshotTool(t *testing.T) {
	// Create browser tools instance
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test SaveScreenshot function directly
	testData := []byte("test image data")
	id := tools.SaveScreenshot(testData)
	if id == "" {
		t.Fatal("SaveScreenshot returned empty ID")
	}

	// Get the file path and check if the file exists
	filePath := GetScreenshotPath(id)
	_, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Failed to find screenshot file: %v", err)
	}

	// Read the file contents
	contents, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read screenshot file: %v", err)
	}

	// Check the file contents
	if string(contents) != string(testData) {
		t.Errorf("File contents don't match: expected %q, got %q", string(testData), string(contents))
	}

	// Clean up the test file
	os.Remove(filePath)
}

// hasImageContent reports whether any LLM content carries a base64 image
// payload (a non-empty MediaType like "image/png").
func hasImageContent(contents []llm.Content) bool {
	for _, c := range contents {
		if strings.HasPrefix(c.MediaType, "image/") && c.Data != "" {
			return true
		}
	}
	return false
}

// TestScreenshotRunGatesOnImageSupport verifies that the screenshot action
// only sends image content to models that accept image inputs. Models that
// don't (e.g. GLM 5.2) get a text-only result so the API doesn't reject the
// request. The screenshot is still saved to disk and surfaced via Display.
func TestScreenshotRunGatesOnImageSupport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping screenshot tool test in short mode")
	}

	baseCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(baseCtx, 0)
	t.Cleanup(func() { tools.Close() })

	// Navigate somewhere so a screenshot can be captured.
	nav := tools.CombinedTool().Run(baseCtx, []byte(`{"action": "navigate", "url": "https://example.com"}`))
	if nav.Error != nil || (len(nav.LLMContent) > 0 && strings.Contains(nav.LLMContent[0].Text, "browser automation not available")) {
		t.Skip("Browser automation not available in this environment")
	}

	t.Run("image-capable model includes image", func(t *testing.T) {
		ctx := llm.WithLLMService(baseCtx, limitedService{})
		out := tools.screenshotRun(ctx, screenshotInput{})
		if out.Error != nil {
			t.Fatalf("screenshotRun: %v", out.Error)
		}
		if !hasImageContent(out.LLMContent) {
			t.Fatalf("expected image content for image-capable model, got %+v", out.LLMContent)
		}
	})

	t.Run("non-image model omits image", func(t *testing.T) {
		ctx := llm.WithLLMService(baseCtx, noImageService{})
		out := tools.screenshotRun(ctx, screenshotInput{})
		if out.Error != nil {
			t.Fatalf("screenshotRun: %v", out.Error)
		}
		if hasImageContent(out.LLMContent) {
			t.Fatalf("expected no image content for non-image model, got %+v", out.LLMContent)
		}
		if len(out.LLMContent) == 0 || !strings.Contains(out.LLMContent[0].Text, "saved as") {
			t.Fatalf("expected text result mentioning saved path, got %+v", out.LLMContent)
		}
		// The screenshot must still be saved and surfaced for the UI. The UI
		// renders the on-disk screenshot via Display["url"] (an /api/read URL)
		// when there's no inline image content, so that field must be present.
		if out.Display == nil {
			t.Fatal("expected Display payload even without image content")
		}
		display, ok := out.Display.(map[string]any)
		if !ok {
			t.Fatalf("expected Display to be a map, got %T", out.Display)
		}
		urlStr, _ := display["url"].(string)
		if !strings.HasPrefix(urlStr, "/api/read?path=") {
			t.Fatalf("expected Display[\"url\"] to be an /api/read URL, got %q", urlStr)
		}
	})
}

func TestReadImageTool(t *testing.T) {
	ctx := context.Background()
	browseTools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		browseTools.Close()
	})

	testDir := t.TempDir()
	testImagePath := filepath.Join(testDir, "test_image.png")

	// A complete, valid 1x1 RGB PNG. (A previous fixture declared 1x1 RGB but
	// carried only 2 bytes of pixel data — an incomplete image that Go's PNG
	// decoder rejects with "not enough pixel data"; read_image now validates
	// with a full decode, so the fixture must be a genuinely decodable image.)
	smallPng := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x10, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x62, 0x32, 0x4E, 0x9B, 0x09,
		0x08, 0x00, 0x00, 0xFF, 0xFF, 0x02, 0x0A, 0x01, 0x35, 0x41, 0xFD, 0x9C, 0x97, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}

	err := os.WriteFile(testImagePath, smallPng, 0o644)
	if err != nil {
		t.Fatalf("Failed to create test image: %v", err)
	}

	tool := browseTools.ReadImageTool()
	input := fmt.Sprintf(`{"path": "%s"}`, testImagePath)

	toolOut := tool.Run(ctx, []byte(input))
	if toolOut.Error != nil {
		t.Fatalf("Read image tool failed: %v", toolOut.Error)
	}

	contents := toolOut.LLMContent
	if len(contents) < 2 {
		t.Fatalf("Expected at least 2 content objects, got %d", len(contents))
	}
	if contents[1].MediaType == "" {
		t.Errorf("Expected MediaType in second content")
	}
	if contents[1].Data == "" {
		t.Errorf("Expected Data in second content")
	}
}

// TestDefaultViewportSize verifies that the browser starts with the correct default viewport size
func TestDefaultViewportSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if os.Getenv("CI") != "" || os.Getenv("HEADLESS_TEST") != "" {
		t.Skip("Skipping browser test in CI/headless environment")
	}

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	// Navigate
	toolOut := tool.Run(ctx, []byte(`{"action": "navigate", "url": "about:blank"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "browser automation not available") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Navigation error: %v", toolOut.Error)
	}
	if !strings.Contains(toolOut.LLMContent[0].Text, "done") {
		t.Fatalf("Expected done in navigation response, got: %s", toolOut.LLMContent[0].Text)
	}

	// Eval
	toolOut = tool.Run(ctx, []byte(`{"action": "eval", "expression": "({width: window.innerWidth, height: window.innerHeight})"}`))
	if toolOut.Error != nil {
		t.Fatalf("Evaluation error: %v", toolOut.Error)
	}

	var response struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}

	text := toolOut.LLMContent[0].Text
	text = strings.TrimPrefix(text, "<javascript_result>")
	text = strings.TrimSuffix(text, "</javascript_result>")

	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatalf("Failed to parse evaluation response: %v", err)
	}

	if response.Width != 1280 {
		t.Errorf("Expected default width 1280, got %v", response.Width)
	}
	if response.Height != 720 {
		t.Errorf("Expected default height 720, got %v", response.Height)
	}
}

// TestBrowserIdleShutdownAndRestart verifies the browser shuts down after idle and can restart
func TestBrowserIdleShutdownAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	idleTimeout := 100 * time.Millisecond
	tools := NewBrowseTools(ctx, idleTimeout)
	t.Cleanup(func() {
		tools.Close()
	})

	browserCtx1, err := tools.GetBrowserContext()
	if err != nil {
		if strings.Contains(err.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Failed to get browser context: %v", err)
	}
	if browserCtx1 == nil {
		t.Fatal("Expected non-nil browser context")
	}

	time.Sleep(idleTimeout + 50*time.Millisecond)

	browserCtx2, err := tools.GetBrowserContext()
	if err != nil {
		t.Fatalf("Failed to get browser context after idle: %v", err)
	}
	if browserCtx2 == nil {
		t.Fatal("Expected non-nil browser context after restart")
	}

	if browserCtx1 == browserCtx2 {
		t.Error("Expected different browser context after idle shutdown")
	}

	// Verify the new browser actually works via combined tool
	tool := tools.CombinedTool()
	toolOut := tool.Run(ctx, []byte(`{"action": "navigate", "url": "about:blank"}`))
	if toolOut.Error != nil {
		t.Fatalf("Navigate failed after restart: %v", toolOut.Error)
	}
}

// TestBrowserCrashRecovery verifies the browser auto-recovers from a crash
func TestBrowserCrashRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 30*time.Minute)
	t.Cleanup(func() {
		tools.Close()
	})

	// First use - should start the browser
	browserCtx1, err := tools.GetBrowserContext()
	if err != nil {
		if strings.Contains(err.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Failed to get browser context: %v", err)
	}
	if browserCtx1 == nil {
		t.Fatal("Expected non-nil browser context")
	}

	// Simulate a crash by canceling the browser context
	// This mimics what chromedp does when Chrome segfaults
	tools.mux.Lock()
	if tools.browserCtxCancel != nil {
		tools.browserCtxCancel()
	}
	tools.mux.Unlock()

	// Second use - should detect the dead context and start a new browser
	// (context cancellation is synchronous, no need to wait)
	browserCtx2, err := tools.GetBrowserContext()
	if err != nil {
		t.Fatalf("Failed to get browser context after crash: %v", err)
	}
	if browserCtx2 == nil {
		t.Fatal("Expected non-nil browser context after crash recovery")
	}

	// The contexts should be different (new browser instance)
	if browserCtx1 == browserCtx2 {
		t.Error("Expected different browser context after crash recovery")
	}

	// Verify the new browser actually works
	tool := tools.CombinedTool()
	toolOut := tool.Run(ctx, []byte(`{"action": "navigate", "url": "about:blank"}`))
	if toolOut.Error != nil {
		t.Fatalf("Navigate failed after crash recovery: %v", toolOut.Error)
	}
}

// limitedService is a minimal llm.Service used by tests to drive the
// image-limit checks via the tool call context.
type limitedService struct {
	maxDim   int
	maxBytes int
}

// noImageService reports that it does not support image inputs (like GLM 5.2).
// It embeds limitedService and overrides SupportsImages.
type noImageService struct{ limitedService }

func (s noImageService) SupportsImages() bool { return false }

func (s limitedService) Do(context.Context, *llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s limitedService) TokenContextWindow() int { return 0 }
func (s limitedService) MaxImageDimension() int  { return s.maxDim }
func (s limitedService) MaxImageBytes() int      { return s.maxBytes }
func (s limitedService) Provider() string        { return "test" }
func (s limitedService) SupportsImages() bool    { return true }

func TestReadImageToolResizesOversizedImage(t *testing.T) {
	browseTools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() {
		browseTools.Close()
	})
	// Model limit: 200px on a side. Oversized images are silently downscaled.
	ctx := llm.WithLLMService(context.Background(), limitedService{maxDim: 200})

	testDir := t.TempDir()
	testImagePath := filepath.Join(testDir, "large_image.png")

	f, err := os.Create(testImagePath)
	if err != nil {
		t.Fatalf("Failed to create test image file: %v", err)
	}
	if err := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 300, 250))); err != nil {
		f.Close()
		t.Fatalf("Failed to encode test image: %v", err)
	}
	f.Close()

	tool := browseTools.ReadImageTool()
	input := fmt.Sprintf(`{"path": "%s"}`, testImagePath)

	toolOut := tool.Run(ctx, []byte(input))
	if toolOut.Error != nil {
		t.Fatalf("unexpected error: %v", toolOut.Error)
	}
	if len(toolOut.LLMContent) < 2 {
		t.Fatalf("expected at least 2 content blocks, got %d", len(toolOut.LLMContent))
	}
	if !strings.Contains(toolOut.LLMContent[0].Text, "resized") {
		t.Errorf("expected description to mention resizing, got: %s", toolOut.LLMContent[0].Text)
	}
	imageBytes, err := base64.StdEncoding.DecodeString(toolOut.LLMContent[1].Data)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width > 200 || cfg.Height > 200 {
		t.Errorf("resized image still exceeds limit: %dx%d", cfg.Width, cfg.Height)
	}

	// The UI reports image comments in the source file's coordinates (the user
	// drags a box on the rendered image but the comment names the path), so
	// Display must carry the pre-resize dimensions.
	display, ok := toolOut.Display.(map[string]any)
	if !ok {
		t.Fatalf("Display = %#v, want map", toolOut.Display)
	}
	if display["path"] != testImagePath {
		t.Errorf("Display path = %v, want %s", display["path"], testImagePath)
	}
	if display["source_width"] != 300 || display["source_height"] != 250 {
		t.Errorf("Display source dimensions = %vx%v, want 300x250", display["source_width"], display["source_height"])
	}
}

func TestReadImageToolRejectsOversizedBytes(t *testing.T) {
	browseTools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() {
		browseTools.Close()
	})
	// Pick a byte limit so small that any encoded PNG will exceed it.
	ctx := llm.WithLLMService(context.Background(), limitedService{maxBytes: 16})

	testDir := t.TempDir()
	testImagePath := filepath.Join(testDir, "big_bytes.png")
	f, err := os.Create(testImagePath)
	if err != nil {
		t.Fatalf("Failed to create test image file: %v", err)
	}
	if err := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		f.Close()
		t.Fatalf("encode: %v", err)
	}
	f.Close()

	tool := browseTools.ReadImageTool()
	input := fmt.Sprintf(`{"path": "%s"}`, testImagePath)
	toolOut := tool.Run(ctx, []byte(input))
	if toolOut.Error == nil {
		t.Fatalf("Expected error for image exceeding byte limit")
	}
	if !strings.Contains(toolOut.Error.Error(), "bytes") {
		t.Errorf("Expected error to mention bytes, got: %v", toolOut.Error)
	}
}

func TestReadImageToolNoServicePassesThrough(t *testing.T) {
	// When no service is attached to the context (e.g. driven from tests or
	// non-loop callers) the size checks should be skipped.
	browseTools := NewBrowseTools(context.Background(), 0)
	t.Cleanup(func() {
		browseTools.Close()
	})

	testDir := t.TempDir()
	testImagePath := filepath.Join(testDir, "plain.png")
	f, err := os.Create(testImagePath)
	if err != nil {
		t.Fatalf("Failed to create test image file: %v", err)
	}
	if err := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 50, 50))); err != nil {
		f.Close()
		t.Fatalf("encode: %v", err)
	}
	f.Close()

	tool := browseTools.ReadImageTool()
	input := fmt.Sprintf(`{"path": "%s"}`, testImagePath)
	toolOut := tool.Run(context.Background(), []byte(input))
	if toolOut.Error != nil {
		t.Fatalf("unexpected error without service in context: %v", toolOut.Error)
	}
}

// TestIsPort80 tests the isPort80 function
func TestIsPort80(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
		name     string
	}{
		{"http://example.com:80", true, "http with explicit port 80"},
		{"http://example.com", true, "http without explicit port"},
		{"https://example.com:80", true, "https with explicit port 80"},
		{"http://example.com:8080", false, "http with different port"},
		{"https://example.com", false, "https without explicit port"},
		{"https://example.com:443", false, "https with standard port"},
		{"invalid-url", false, "invalid URL"},
		{"ftp://example.com:80", true, "ftp with port 80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPort80(tt.url)
			if result != tt.expected {
				t.Errorf("isPort80(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
}

// TestResizeRunErrorPaths tests error paths in resize action
func TestResizeRunErrorPaths(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	// Test with invalid JSON input
	toolOut := tool.Run(ctx, []byte(`{"action": "resize", "width": "not-a-number"}`))
	if toolOut.Error == nil {
		t.Error("Expected error for invalid JSON input")
	}

	// Test with negative dimensions
	toolOut = tool.Run(ctx, []byte(`{"action": "resize", "width": -100, "height": 100}`))
	if toolOut.Error == nil {
		t.Error("Expected error for negative width")
	}

	// Test with zero dimensions
	toolOut = tool.Run(ctx, []byte(`{"action": "resize", "width": 0, "height": 100}`))
	if toolOut.Error == nil {
		t.Error("Expected error for zero width")
	}
}

// TestScreenshotRunErrorPaths tests error paths in screenshot action
func TestScreenshotRunErrorPaths(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	// Test with invalid JSON input
	toolOut := tool.Run(ctx, []byte(`{"action": "screenshot", "selector": 123}`))
	if toolOut.Error == nil {
		t.Error("Expected error for invalid JSON input")
	}
}

func TestRecentConsoleLogsRunErrorPaths(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	// Test with invalid JSON input
	toolOut := tool.Run(ctx, []byte(`{"action": "console_logs", "limit": "not-a-number"}`))
	if toolOut.Error == nil {
		t.Error("Expected error for invalid JSON input")
	}
}

// TestParseTimeout tests the parseTimeout function
func TestParseTimeout(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		name     string
	}{
		{"10s", 10 * time.Second, "valid duration"},
		{"5m", 5 * time.Minute, "valid minutes"},
		{"", 15 * time.Second, "empty string defaults to 15s"},
		{"invalid", 15 * time.Second, "invalid duration defaults to 15s"},
		{"30ms", 30 * time.Millisecond, "valid milliseconds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTimeout(tt.input)
			if result != tt.expected {
				t.Errorf("parseTimeout(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestRegisterBrowserTools tests the RegisterBrowserTools function
func TestRegisterBrowserTools(t *testing.T) {
	ctx := context.Background()

	tools, cleanup := RegisterBrowserTools(ctx)
	t.Cleanup(cleanup)

	if len(tools) != 2 {
		t.Fatalf("RegisterBrowserTools: expected 2 tools, got %d", len(tools))
	}
	expectedNames := []string{"browser", "read_image"}
	for i, name := range expectedNames {
		if tools[i].Name != name {
			t.Errorf("expected tool %d name %q, got %q", i, name, tools[i].Name)
		}
	}
	cleanup()
}

// TestGetScreenshotPath tests the GetScreenshotPath function
func TestGetScreenshotPath(t *testing.T) {
	id := "test-id"
	expected := filepath.Join(ScreenshotDir, id+".png")
	actual := GetScreenshotPath(id)

	if actual != expected {
		t.Errorf("GetScreenshotPath(%q) = %q, want %q", id, actual, expected)
	}
}

// TestSaveScreenshotErrorPath tests error paths in SaveScreenshot
func TestSaveScreenshotErrorPath(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test with empty data (this should still work)
	id := tools.SaveScreenshot([]byte{})
	if id == "" {
		t.Error("Expected non-empty ID for empty data")
	}

	// Clean up the test file
	filePath := GetScreenshotPath(id)
	os.Remove(filePath)
}

// TestConsoleLogsWriteToFile tests that large console logs are written to file
func TestConsoleLogsWriteToFile(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tools.consoleLogsMutex.Lock()
	for i := 0; i < 50; i++ {
		tools.consoleLogs = append(tools.consoleLogs, &runtime.EventConsoleAPICalled{
			Type: runtime.APITypeLog,
			Args: []*runtime.RemoteObject{
				{Type: runtime.TypeString, Value: jsontext.Value(`"This is a long log message that will help exceed the 1KB threshold when repeated many times"`)},
			},
		})
	}
	tools.consoleLogsMutex.Unlock()

	// Mock browser context to avoid actual browser initialization
	tools.mux.Lock()
	tools.browserCtx = ctx
	tools.mux.Unlock()

	tool := tools.CombinedTool()
	toolOut := tool.Run(ctx, []byte(`{"action": "console_logs"}`))
	if toolOut.Error != nil {
		t.Fatalf("Unexpected error: %v", toolOut.Error)
	}

	resultText := toolOut.LLMContent[0].Text
	if !strings.Contains(resultText, "Output written to:") {
		t.Errorf("Expected output to be written to file, got: %s", resultText)
	}
	if !strings.Contains(resultText, ConsoleLogsDir) {
		t.Errorf("Expected file path to contain %s, got: %s", ConsoleLogsDir, resultText)
	}

	parts := strings.Split(resultText, "Output written to: ")
	if len(parts) < 2 {
		t.Fatalf("Could not extract file path from: %s", resultText)
	}
	filePath := strings.Split(parts[1], "\n")[0]
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Errorf("Expected file to exist at %s", filePath)
	} else {
		os.Remove(filePath)
	}
}

// TestGenerateDownloadFilename tests filename generation with randomness
func TestGenerateDownloadFilename(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tests := []struct {
		suggested string
		prefix    string
		ext       string
	}{
		{"test.txt", "test_", ".txt"},
		{"document.pdf", "document_", ".pdf"},
		{"noextension", "noextension_", ""},
		{"", "download_", ""},
		{"file.tar.gz", "file.tar_", ".gz"},
	}

	for _, tt := range tests {
		t.Run(tt.suggested, func(t *testing.T) {
			result := tools.generateDownloadFilename(tt.suggested)
			if !strings.HasPrefix(result, tt.prefix) {
				t.Errorf("Expected prefix %q, got %q", tt.prefix, result)
			}
			if !strings.HasSuffix(result, tt.ext) {
				t.Errorf("Expected suffix %q, got %q", tt.ext, result)
			}
			// Verify randomness (8 chars between prefix and extension)
			withoutPrefix := strings.TrimPrefix(result, tt.prefix)
			withoutExt := strings.TrimSuffix(withoutPrefix, tt.ext)
			if len(withoutExt) != 8 {
				t.Errorf("Expected 8 random chars, got %d in %q", len(withoutExt), result)
			}
		})
	}

	// Verify different calls produce different results
	result1 := tools.generateDownloadFilename("test.txt")
	result2 := tools.generateDownloadFilename("test.txt")
	if result1 == result2 {
		t.Errorf("Expected different filenames, got same: %s", result1)
	}
}

// TestDownloadTracking tests the download event handling
func TestDownloadTracking(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Simulate download start event
	tools.handleDownloadWillBegin(&browser.EventDownloadWillBegin{
		GUID:              "test-guid-123",
		URL:               "http://example.com/file.txt",
		SuggestedFilename: "file.txt",
	})

	// Verify download is tracked
	tools.downloadsMutex.Lock()
	info, exists := tools.downloads["test-guid-123"]
	tools.downloadsMutex.Unlock()

	if !exists {
		t.Fatal("Expected download to be tracked")
	}
	if info.URL != "http://example.com/file.txt" {
		t.Errorf("Expected URL %q, got %q", "http://example.com/file.txt", info.URL)
	}
	if info.Completed {
		t.Error("Download should not be completed yet")
	}

	// Simulate download progress - canceled
	tools.handleDownloadProgress(&browser.EventDownloadProgress{
		GUID:  "test-guid-123",
		State: browser.DownloadProgressStateCanceled,
	})

	// Verify download is marked as completed with error
	tools.downloadsMutex.Lock()
	info = tools.downloads["test-guid-123"]
	tools.downloadsMutex.Unlock()

	if !info.Completed {
		t.Error("Download should be completed after cancel")
	}
	if info.Error != "download canceled" {
		t.Errorf("Expected error %q, got %q", "download canceled", info.Error)
	}
}

// TestToolOutWithDownloads tests the download info appending to tool output.
func TestToolOutWithDownloads(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	generation := tools.beginDownloadAction()
	// Test with no downloads
	out := tools.toolOutWithDownloads("test message", generation)
	if out.LLMContent[0].Text != "test message" {
		t.Errorf("Expected %q, got %q", "test message", out.LLMContent[0].Text)
	}

	// Add a completed download
	tools.downloadsMutex.Lock()
	tools.downloads["guid1"] = &DownloadInfo{
		GUID:              "guid1",
		URL:               "http://example.com/files/test.txt",
		SuggestedFilename: "test.txt",
		FinalPath:         "/tmp/test_abc123.txt",
		Generation:        generation,
		Completed:         true,
	}
	tools.downloadsMutex.Unlock()

	out = tools.toolOutWithDownloads("done", generation)
	result := out.LLMContent[0].Text
	if !strings.Contains(result, "Downloads completed:") {
		t.Errorf("Expected downloads section, got: %s", result)
	}
	if !strings.Contains(result, "test.txt") {
		t.Errorf("Expected filename in output, got: %s", result)
	}
	if !strings.Contains(result, "http://example.com/files/test.txt") {
		t.Errorf("Expected URL in output, got: %s", result)
	}
	if !strings.Contains(result, "saved to:") {
		t.Errorf("Expected 'saved to:' in output, got: %s", result)
	}
	if !strings.Contains(result, "/tmp/test_abc123.txt") {
		t.Errorf("Expected final path in output, got: %s", result)
	}

	tools.downloadsMutex.Lock()
	_, exists := tools.downloads["guid1"]
	tools.downloadsMutex.Unlock()
	if exists {
		t.Error("Expected download to be cleared after retrieval")
	}

	lateGeneration := tools.beginDownloadAction()
	nextGeneration := tools.beginDownloadAction()
	tools.downloadsMutex.Lock()
	tools.downloads["late"] = &DownloadInfo{
		GUID:       "late",
		Generation: lateGeneration,
		Completed:  true,
	}
	tools.downloadsMutex.Unlock()
	out = tools.toolOutWithDownloads("next action", nextGeneration)
	if strings.Contains(out.LLMContent[0].Text, "Downloads completed") {
		t.Fatalf("later action claimed an earlier download: %s", out.LLMContent[0].Text)
	}
	tools.downloadsMutex.Lock()
	_, exists = tools.downloads["late"]
	tools.downloadsMutex.Unlock()
	if !exists {
		t.Fatal("earlier download was consumed by later action")
	}
}

// TestBrowserDownload tests the full browser download workflow with a real HTTP server
func TestBrowserDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser download test in short mode")
	}

	// Start a test HTTP server that triggers a download
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename=\"test.txt\"")
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Hello, this is a test file!"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<a id="download-link" href="/download">Download</a>
</body>
</html>`)))
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	// Create browser tools
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Navigate to the test page
	tool := tools.CombinedTool()
	navInput := []byte(fmt.Sprintf(`{"action": "navigate", "url": "http://127.0.0.1:%d/"}`, port))
	toolOut := tool.Run(ctx, navInput)
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Navigation error: %v", toolOut.Error)
	}

	// Click the download link
	toolOut = tool.Run(ctx, []byte(`{"action": "eval", "expression": "document.getElementById('download-link').click()"}`))
	if toolOut.Error != nil {
		t.Fatalf("Eval error: %v", toolOut.Error)
	}

	// Wait for download to complete (poll for completion)
	var downloadFound bool
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		files, err := os.ReadDir(DownloadDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			// Check for renamed file (test_*) or GUID file
			if strings.HasPrefix(f.Name(), "test_") || len(f.Name()) == 36 {
				filePath := filepath.Join(DownloadDir, f.Name())
				content, err := os.ReadFile(filePath)
				if err == nil && string(content) == "Hello, this is a test file!" {
					downloadFound = true
					t.Logf("Found downloaded file: %s", f.Name())
					// Clean up
					os.Remove(filePath)
					break
				}
			}
		}
		if downloadFound {
			break
		}
	}

	if !downloadFound {
		// List what's in the directory for debugging
		files, _ := os.ReadDir(DownloadDir)
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Errorf("Download file not found. Files in %s: %v", DownloadDir, names)
	}
}

// TestBrowserDownloadReported tests that downloads are reported in tool output
func TestBrowserDownloadReported(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser download test in short mode")
	}

	// Start a test HTTP server that triggers a download
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename=\"report_test.txt\"")
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Download report test file content"))
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	// Create browser tools
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Navigate directly to the download URL - should succeed with download info
	tool := tools.CombinedTool()
	navInput := []byte(fmt.Sprintf(`{"action": "navigate", "url": "http://127.0.0.1:%d/download"}`, port))
	toolOut := tool.Run(ctx, navInput)
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Navigation returned unexpected error: %v", toolOut.Error)
	}

	result := toolOut.LLMContent[0].Text
	t.Logf("Navigation result: %s", result)

	// Navigation to download URL should report the download directly
	if !strings.Contains(result, "download") {
		t.Errorf("Expected 'download' in output, got: %s", result)
	}
	if !strings.Contains(result, "report_test") {
		t.Errorf("Expected 'report_test' in download output, got: %s", result)
	}
	if !strings.Contains(result, DownloadDir) {
		t.Errorf("Expected download path, got: %s", result)
	}

	// Clean up any downloaded files
	files, _ := os.ReadDir(DownloadDir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "report_test_") {
			os.Remove(filepath.Join(DownloadDir, f.Name()))
		}
	}
}

// TestLargeJSOutputWriteToFile tests that large JS eval results are written to file
func TestLargeJSOutputWriteToFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	toolOut := tool.Run(ctx, []byte(`{"action": "navigate", "url": "about:blank"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Navigation error: %v", toolOut.Error)
	}

	toolOut = tool.Run(ctx, []byte(`{"action": "eval", "expression": "'x'.repeat(2000)"}`))

	if toolOut.Error != nil {
		t.Fatalf("Eval error: %v", toolOut.Error)
	}

	result := toolOut.LLMContent[0].Text
	t.Logf("Result: %s", result[:min(200, len(result))])

	// Should be written to file
	if !strings.Contains(result, "JavaScript result") {
		t.Errorf("Expected 'JavaScript result' in output, got: %s", result)
	}
	if !strings.Contains(result, "written to:") {
		t.Errorf("Expected 'written to:' in output, got: %s", result)
	}
	if !strings.Contains(result, ConsoleLogsDir) {
		t.Errorf("Expected file path to contain %s, got: %s", ConsoleLogsDir, result)
	}

	// Extract and verify file exists
	parts := strings.Split(result, "written to: ")
	if len(parts) >= 2 {
		filePath := strings.Split(parts[1], "\n")[0]
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			t.Errorf("Expected file to exist at %s", filePath)
		} else {
			// Verify content
			content, err := os.ReadFile(filePath)
			if err != nil {
				t.Errorf("Failed to read file: %v", err)
			} else if len(content) < 2000 {
				t.Errorf("Expected file to contain large result, got %d bytes", len(content))
			}
			// Clean up
			os.Remove(filePath)
		}
	}
}

// TestSmallJSOutputInline tests that small JS results are returned inline
func TestSmallJSOutputInline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	tool := tools.CombinedTool()

	toolOut := tool.Run(ctx, []byte(`{"action": "navigate", "url": "about:blank"}`))
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Navigation error: %v", toolOut.Error)
	}

	toolOut = tool.Run(ctx, []byte(`{"action": "eval", "expression": "'hello world'"}`))

	if toolOut.Error != nil {
		t.Fatalf("Eval error: %v", toolOut.Error)
	}

	result := toolOut.LLMContent[0].Text

	// Should be inline
	if !strings.Contains(result, "<javascript_result>") {
		t.Errorf("Expected '<javascript_result>' in output, got: %s", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("Expected 'hello world' in output, got: %s", result)
	}
	if strings.Contains(result, "written to:") {
		t.Errorf("Small result should not be written to file, got: %s", result)
	}
}
