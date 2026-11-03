package browse

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"shelley.exe.dev/llm"
)

// PDFDir is the directory where the pdf action saves its output.
const PDFDir = "/tmp/shelley-pdfs"

// pdfRun prints the current page to a PDF. Backgrounds are kept (Chrome's
// print dialog drops them by default, which guts most styled pages), and CSS
// @page rules decide paper size, orientation, and margins, so the page itself
// is the one place to configure layout. PrintToPDFParams sends its margin and
// tagging fields even when unset, so Chrome's own defaults (1cm margins, a
// tagged PDF) must be restated; the outline turns headings into bookmarks.
func (b *BrowseTools) pdfRun(ctx context.Context, timeout string) llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	timeoutCtx, cancel := browserActionContext(ctx, browserCtx, timeout)
	defer cancel()

	var data []byte
	err = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		data, _, err = page.PrintToPDF().
			WithPrintBackground(true).
			WithPreferCSSPageSize(true).
			WithMarginTop(0.4).WithMarginBottom(0.4).WithMarginLeft(0.4).WithMarginRight(0.4).
			WithGenerateTaggedPDF(true).
			WithGenerateDocumentOutline(true).
			Do(ctx)
		return err
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if err := os.MkdirAll(PDFDir, 0o755); err != nil {
		return llm.ErrorToolOut(err)
	}
	path := filepath.Join(PDFDir, uuid.New().String()+".pdf")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return llm.ErrorToolOut(err)
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent(fmt.Sprintf("PDF (%d bytes) saved to: %s\nCopy it rather than moving it; the UI links this path.", len(data), path)),
		Display: map[string]any{
			"type": "pdf",
			"url":  "/api/read?path=" + url.QueryEscape(path),
			"path": path,
		},
	}
}
