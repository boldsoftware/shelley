package browse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"shelley.exe.dev/llm"
)

func TestToolCreation(t *testing.T) {
	// Create browser tools instance
	tools := NewBrowseTools(context.Background(), 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test each tool has correct name and description
	toolTests := []struct {
		tool          *llm.Tool
		expectedName  string
		shortDesc     string
		requiredProps []string
	}{
		{tools.NewNavigateTool(), "browser_navigate", "Navigate", []string{"url"}},
		{tools.NewEvalTool(), "browser_eval", "Evaluate", []string{"expression"}},
		{tools.NewResizeTool(), "browser_resize", "Resize", []string{"width", "height"}},
		{tools.NewScreenshotTool(), "browser_take_screenshot", "Take", nil},
	}

	for _, tt := range toolTests {
		t.Run(tt.expectedName, func(t *testing.T) {
			if tt.tool.Name != tt.expectedName {
				t.Errorf("expected name %q, got %q", tt.expectedName, tt.tool.Name)
			}

			if !strings.Contains(tt.tool.Description, tt.shortDesc) {
				t.Errorf("description %q should contain %q", tt.tool.Description, tt.shortDesc)
			}

			// Verify schema has required properties
			if len(tt.requiredProps) > 0 {
				var schema struct {
					Required []string `json:"required"`
				}
				if err := json.Unmarshal(tt.tool.InputSchema, &schema); err != nil {
					t.Fatalf("failed to unmarshal schema: %v", err)
				}

				for _, prop := range tt.requiredProps {
					if !slices.Contains(schema.Required, prop) {
						t.Errorf("property %q should be required", prop)
					}
				}
			}
		})
	}
}

func TestGetTools(t *testing.T) {
	// Create browser tools instance
	tools := NewBrowseTools(context.Background(), 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test with screenshot tools included
	t.Run("with screenshots", func(t *testing.T) {
		toolsWithScreenshots := tools.GetTools(true)
		if len(toolsWithScreenshots) != 7 {
			t.Errorf("expected 7 tools with screenshots, got %d", len(toolsWithScreenshots))
		}

		// Check tool naming convention
		for _, tool := range toolsWithScreenshots {
			// Most tools have browser_ prefix, except for read_image
			if tool.Name != "read_image" && !strings.HasPrefix(tool.Name, "browser_") {
				t.Errorf("tool name %q does not have prefix 'browser_'", tool.Name)
			}
		}
	})

	// Test without screenshot tools
	t.Run("without screenshots", func(t *testing.T) {
		noScreenshotTools := tools.GetTools(false)
		if len(noScreenshotTools) != 5 {
			t.Errorf("expected 5 tools without screenshots, got %d", len(noScreenshotTools))
		}
	})
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

	tools := NewBrowseTools(ctx, 0, 0)
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
	err = chromedp.Run(browserCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Title(&title),
	)
	if err != nil {
		t.Fatalf("Failed to navigate to about:blank: %v", err)
	}

	t.Logf("Successfully navigated to about:blank, title: %q", title)
}

// TestNavigateTool verifies that the navigate tool works correctly
func TestNavigateTool(t *testing.T) {
	// Skip long tests in short mode
	if testing.Short() {
		t.Skip("skipping navigate tool test in short mode")
	}

	// Create browser tools instance
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Get the navigate tool
	navTool := tools.NewNavigateTool()

	// Create input for the navigate tool
	input := map[string]string{"url": "https://example.com"}
	inputJSON, _ := json.Marshal(input)

	// Call the tool
	toolOut := navTool.Run(ctx, []byte(inputJSON))
	if toolOut.Error != nil {
		t.Fatalf("Error running navigate tool: %v", toolOut.Error)
	}
	result := toolOut.LLMContent

	// Verify the response is successful
	resultText := result[0].Text
	if !strings.Contains(resultText, "done") {
		// If browser automation is not available, skip the test
		if strings.Contains(resultText, "browser automation not available") {
			t.Skip("Browser automation not available in this environment")
		} else {
			t.Fatalf("Expected done in result text, got: %s", resultText)
		}
	}

	// Try to get the page title to verify the navigation worked
	browserCtx, err := tools.GetBrowserContext()
	if err != nil {
		// If browser automation is not available, skip the test
		if strings.Contains(err.Error(), "browser automation not available") {
			t.Skip("Browser automation not available in this environment")
		} else {
			t.Fatalf("Failed to get browser context: %v", err)
		}
	}

	var title string
	err = chromedp.Run(browserCtx, chromedp.Title(&title))
	if err != nil {
		t.Fatalf("Failed to get page title: %v", err)
	}

	t.Logf("Successfully navigated to example.com, title: %q", title)
	if title != "Example Domain" {
		t.Errorf("Expected title 'Example Domain', got '%s'", title)
	}
}

// TestScreenshotTool tests that the screenshot tool properly saves files
func TestScreenshotTool(t *testing.T) {
	// Create browser tools instance
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0, 0)
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

func TestReadImageTool(t *testing.T) {
	// Create a test BrowseTools instance
	ctx := context.Background()
	browseTools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		browseTools.Close()
	})

	// Create a test image
	testDir := t.TempDir()
	testImagePath := filepath.Join(testDir, "test_image.png")

	// Create a small 1x1 black PNG image
	smallPng := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0x60, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, 0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}

	// Write the test image
	err := os.WriteFile(testImagePath, smallPng, 0o644)
	if err != nil {
		t.Fatalf("Failed to create test image: %v", err)
	}

	// Create the tool
	readImageTool := browseTools.NewReadImageTool()

	// Prepare input
	input := fmt.Sprintf(`{"path": "%s"}`, testImagePath)

	// Run the tool
	toolOut := readImageTool.Run(ctx, []byte(input))
	if toolOut.Error != nil {
		t.Fatalf("Read image tool failed: %v", toolOut.Error)
	}
	result := toolOut.LLMContent

	// In the updated code, result is already a []llm.Content
	contents := result

	// Check that we got at least two content objects
	if len(contents) < 2 {
		t.Fatalf("Expected at least 2 content objects, got %d", len(contents))
	}

	// Check that the second content has image data
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

	// Skip if CI or headless testing environment
	if os.Getenv("CI") != "" || os.Getenv("HEADLESS_TEST") != "" {
		t.Skip("Skipping browser test in CI/headless environment")
	}

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Navigate to a simple page to ensure the browser is ready
	navInput := []byte(`{"url": "about:blank"}`)
	toolOut := tools.NewNavigateTool().Run(ctx, navInput)
	if toolOut.Error != nil {
		if strings.Contains(toolOut.Error.Error(), "browser automation not available") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Navigation error: %v", toolOut.Error)
	}
	content := toolOut.LLMContent
	if !strings.Contains(content[0].Text, "done") {
		t.Fatalf("Expected done in navigation response, got: %s", content[0].Text)
	}

	// Check default viewport dimensions via JavaScript
	evalInput := []byte(`{"expression": "({width: window.innerWidth, height: window.innerHeight})"}`)
	toolOut = tools.NewEvalTool().Run(ctx, evalInput)
	if toolOut.Error != nil {
		t.Fatalf("Evaluation error: %v", toolOut.Error)
	}
	content = toolOut.LLMContent

	// Parse the result to verify dimensions
	var response struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}

	text := content[0].Text
	text = strings.TrimPrefix(text, "<javascript_result>")
	text = strings.TrimSuffix(text, "</javascript_result>")

	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatalf("Failed to parse evaluation response (%q => %q): %v", content[0].Text, text, err)
	}

	// Verify the default viewport size is 1280x720
	expectedWidth := 1280.0
	expectedHeight := 720.0

	if response.Width != expectedWidth {
		t.Errorf("Expected default width %v, got %v", expectedWidth, response.Width)
	}
	if response.Height != expectedHeight {
		t.Errorf("Expected default height %v, got %v", expectedHeight, response.Height)
	}
}

// TestBrowserIdleShutdownAndRestart verifies the browser shuts down after idle and can restart
func TestBrowserIdleShutdownAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Use a short idle timeout for testing
	idleTimeout := 100 * time.Millisecond
	tools := NewBrowseTools(ctx, idleTimeout, 0)
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

	// Wait for idle timeout to fire
	time.Sleep(idleTimeout + 50*time.Millisecond)

	// Second use - should start a new browser (old one was killed)
	browserCtx2, err := tools.GetBrowserContext()
	if err != nil {
		t.Fatalf("Failed to get browser context after idle: %v", err)
	}
	if browserCtx2 == nil {
		t.Fatal("Expected non-nil browser context after restart")
	}

	// The contexts should be different (new browser instance)
	if browserCtx1 == browserCtx2 {
		t.Error("Expected different browser context after idle shutdown")
	}

	// Verify the new browser actually works
	navTool := tools.NewNavigateTool()
	input := []byte(`{"url": "about:blank"}`)
	toolOut := navTool.Run(ctx, input)
	if toolOut.Error != nil {
		t.Fatalf("Navigate failed after restart: %v", toolOut.Error)
	}
}

func TestReadImageToolResizesLargeImage(t *testing.T) {
	// Create a test BrowseTools instance with max dimension of 2000
	ctx := context.Background()
	browseTools := NewBrowseTools(ctx, 0, 2000)
	t.Cleanup(func() {
		browseTools.Close()
	})

	// Create a large test image (3000x2500 pixels)
	testDir := t.TempDir()
	testImagePath := filepath.Join(testDir, "large_image.png")

	// Create a large image using image package
	img := image.NewRGBA(image.Rect(0, 0, 3000, 2500))
	for y := 0; y < 2500; y++ {
		for x := 0; x < 3000; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}

	f, err := os.Create(testImagePath)
	if err != nil {
		t.Fatalf("Failed to create test image file: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("Failed to encode test image: %v", err)
	}
	f.Close()

	// Create the tool
	readImageTool := browseTools.NewReadImageTool()

	// Prepare input
	input := fmt.Sprintf(`{"path": "%s"}`, testImagePath)

	// Run the tool
	toolOut := readImageTool.Run(ctx, []byte(input))
	if toolOut.Error != nil {
		t.Fatalf("Read image tool failed: %v", toolOut.Error)
	}
	result := toolOut.LLMContent

	// Check that we got at least two content objects
	if len(result) < 2 {
		t.Fatalf("Expected at least 2 content objects, got %d", len(result))
	}

	// Check that the description mentions resizing
	if !strings.Contains(result[0].Text, "resized") {
		t.Errorf("Expected description to mention resizing, got: %s", result[0].Text)
	}

	// Decode the returned image and verify dimensions are within limits
	imageData, err := base64.StdEncoding.DecodeString(result[1].Data)
	if err != nil {
		t.Fatalf("Failed to decode base64 image: %v", err)
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(imageData))
	if err != nil {
		t.Fatalf("Failed to decode image config: %v", err)
	}

	if config.Width > 2000 || config.Height > 2000 {
		t.Errorf("Image dimensions still exceed 2000 pixels: %dx%d", config.Width, config.Height)
	}

	t.Logf("Large image resized from 3000x2500 to %dx%d", config.Width, config.Height)
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

// TestResizeRunErrorPaths tests error paths in resizeRun
func TestResizeRunErrorPaths(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test with invalid JSON input
	invalidInput := []byte(`{"width": "not-a-number"}`)
	toolOut := tools.resizeRun(ctx, invalidInput)
	if toolOut.Error == nil {
		t.Error("No error expected for invalid JSON input in clearConsoleLogsRun")
	}

	// Test with negative dimensions
	negativeInput := []byte(`{"width": -100, "height": 100}`)
	toolOut = tools.resizeRun(ctx, negativeInput)
	if toolOut.Error == nil {
		t.Error("Expected error for negative width")
	}

	// Test with zero dimensions
	zeroInput := []byte(`{"width": 0, "height": 100}`)
	toolOut = tools.resizeRun(ctx, zeroInput)
	if toolOut.Error == nil {
		t.Error("Expected error for zero width")
	}
}

// TestScreenshotRunErrorPaths tests error paths in screenshotRun
func TestScreenshotRunErrorPaths(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test with invalid JSON input
	invalidInput := []byte(`{"selector": 123}`)
	toolOut := tools.screenshotRun(ctx, invalidInput)
	if toolOut.Error == nil {
		t.Error("No error expected for invalid JSON input in clearConsoleLogsRun")
	}
}

func TestRecentConsoleLogsRunErrorPaths(t *testing.T) {
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Test with invalid JSON input
	invalidInput := []byte(`{"limit": "not-a-number"}`)
	toolOut := tools.recentConsoleLogsRun(ctx, invalidInput)
	if toolOut.Error == nil {
		t.Error("No error expected for invalid JSON input in clearConsoleLogsRun")
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

	// Test with screenshots enabled
	tools, cleanup := RegisterBrowserTools(ctx, true, 0)
	t.Cleanup(cleanup)

	if len(tools) != 7 {
		t.Errorf("Expected 7 tools with screenshots, got %d", len(tools))
	}

	// Test with screenshots disabled
	tools, cleanup = RegisterBrowserTools(ctx, false, 0)
	t.Cleanup(cleanup)

	if len(tools) != 5 {
		t.Errorf("Expected 5 tools without screenshots, got %d", len(tools))
	}

	// Verify that cleanup function works (doesn't panic)
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
	tools := NewBrowseTools(ctx, 0, 0)
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
