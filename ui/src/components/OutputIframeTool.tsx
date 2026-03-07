import React, { useState, useRef, useEffect, useCallback } from "react";
import JSZip from "jszip";
import { LLMContent } from "../types";

interface EmbeddedFile {
  name: string;
  path: string;
  content: string;
  type: string;
}

interface OutputIframeToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { path: string, title?: string, files?: object }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown; // OutputIframeDisplay from the Go tool
}

// Script injected into iframe to report its content height
const HEIGHT_REPORTER_SCRIPT = `
<script>
(function() {
  function reportHeight() {
    var height = Math.max(
      document.body.scrollHeight,
      document.body.offsetHeight,
      document.documentElement.scrollHeight,
      document.documentElement.offsetHeight
    );
    window.parent.postMessage({ type: 'iframe-height', height: height }, '*');
  }
  // Report on load
  if (document.readyState === 'complete') {
    reportHeight();
  } else {
    window.addEventListener('load', reportHeight);
  }
  // Report after a short delay to catch async content
  setTimeout(reportHeight, 100);
  setTimeout(reportHeight, 500);
  // Report on resize
  window.addEventListener('resize', reportHeight);
  // Observe DOM changes
  if (typeof MutationObserver !== 'undefined') {
    var observer = new MutationObserver(reportHeight);
    observer.observe(document.body, { childList: true, subtree: true, attributes: true });
  }
})();
</script>
`;

const MIN_HEIGHT = 100;
const MAX_HEIGHT = 600;

// Remove injected scripts/styles from HTML to get the original version for download
function getOriginalHtml(html: string): string {
  // Remove the window.__FILES__ script block
  let result = html.replace(
    /<script>\s*window\.__FILES__\s*=\s*window\.__FILES__\s*\|\|\s*\{\};[\s\S]*?<\/script>\s*/g,
    "",
  );
  // Remove injected style tags
  result = result.replace(/<style data-file="[^"]*">[\s\S]*?<\/style>\s*/g, "");
  // Remove injected script tags
  result = result.replace(/<script data-file="[^"]*">[\s\S]*?<\/script>\s*/g, "");
  // Remove empty head tags that might have been added
  result = result.replace(/<head>\s*<\/head>\s*/g, "");
  return result;
}

function OutputIframeTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  display,
}: OutputIframeToolProps) {
  // Default to expanded for visual content
  const [isExpanded, setIsExpanded] = useState(true);
  const [iframeHeight, setIframeHeight] = useState(300);
  const iframeRef = useRef<HTMLIFrameElement>(null);

  // Extract input data
  const getTitle = (input: unknown): string | undefined => {
    if (
      typeof input === "object" &&
      input !== null &&
      "title" in input &&
      typeof input.title === "string"
    ) {
      return input.title;
    }
    return undefined;
  };

  const getHtmlFromInput = (input: unknown): string | undefined => {
    if (
      typeof input === "object" &&
      input !== null &&
      "html" in input &&
      typeof input.html === "string"
    ) {
      return input.html;
    }
    return undefined;
  };

  // Get display data - prefer from display prop, fall back to toolInput
  const getDisplayData = (): {
    html?: string;
    title?: string;
    filename?: string;
    files?: EmbeddedFile[];
  } => {
    // First try display prop (from tool result)
    if (display && typeof display === "object" && display !== null) {
      const d = display as {
        html?: string;
        title?: string;
        filename?: string;
        files?: EmbeddedFile[];
      };
      return {
        html: typeof d.html === "string" ? d.html : undefined,
        title: typeof d.title === "string" ? d.title : undefined,
        filename: typeof d.filename === "string" ? d.filename : undefined,
        files: Array.isArray(d.files) ? d.files : undefined,
      };
    }
    // Fall back to toolInput
    return {
      html: getHtmlFromInput(toolInput),
      title: getTitle(toolInput),
    };
  };

  const displayData = getDisplayData();
  const title = displayData.title || "HTML Output";
  const html = displayData.html;
  const filename = displayData.filename || "output.html";
  const files = displayData.files || [];
  const hasMultipleFiles = files.length > 0;

  // Inject height reporter script into HTML
  const htmlWithHeightReporter = html
    ? html.includes("</body>")
      ? html.replace("</body>", HEIGHT_REPORTER_SCRIPT + "</body>")
      : html + HEIGHT_REPORTER_SCRIPT
    : undefined;

  // Listen for height messages from iframe
  const handleMessage = useCallback((event: MessageEvent) => {
    if (
      event.data &&
      typeof event.data === "object" &&
      event.data.type === "iframe-height" &&
      typeof event.data.height === "number"
    ) {
      // Verify the message is from our iframe
      if (iframeRef.current && event.source === iframeRef.current.contentWindow) {
        const newHeight = Math.min(Math.max(event.data.height, MIN_HEIGHT), MAX_HEIGHT);
        setIframeHeight(newHeight);
      }
    }
  }, []);

  useEffect(() => {
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [handleMessage]);

  // Escape HTML special characters for safe embedding
  const escapeHtml = (str: string): string => {
    return str
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  };

  // Open HTML in new tab with sandbox protection
  const handleOpenInNewTab = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!html) return;

    // Create a wrapper HTML page that embeds the content in a sandboxed iframe
    // This preserves security even when opened in a new tab
    const escapedHtml = escapeHtml(html);
    const escapedTitle = escapeHtml(title);

    const wrapperHtml = `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>${escapedTitle}</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    html, body { height: 100%; background: #f5f5f5; }
    iframe { 
      width: 100%; 
      height: 100%; 
      border: none;
      background: white;
    }
  </style>
</head>
<body>
  <iframe sandbox="allow-scripts" srcdoc="${escapedHtml}"></iframe>
</body>
</html>`;

    const blob = new Blob([wrapperHtml], { type: "text/html" });
    const url = URL.createObjectURL(blob);
    window.open(url, "_blank");
    // Clean up the URL after a delay
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  // Download files - single HTML or zip with all files
  const handleDownload = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!html) return;

    if (hasMultipleFiles) {
      // Create a zip file with all files
      const zip = new JSZip();

      // Add the original HTML (without injected content)
      const originalHtml = getOriginalHtml(html);
      zip.file(filename, originalHtml);

      // Add all embedded files
      for (const file of files) {
        zip.file(file.path || file.name, file.content);
      }

      // Generate and download the zip
      const zipBlob = await zip.generateAsync({ type: "blob" });
      const url = URL.createObjectURL(zipBlob);
      const a = document.createElement("a");
      a.href = url;
      // Use the HTML filename without extension for the zip name
      const zipName = filename.replace(/\.[^.]+$/, "") + ".zip";
      a.download = zipName;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } else {
      // Single file download
      const blob = new Blob([html], { type: "text/html" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    }
  };

  const isComplete = !isRunning && toolResult !== undefined;
  const downloadLabel = hasMultipleFiles ? "Download ZIP" : "Download HTML";

  return (
    <div
      className="output-iframe-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="output-iframe-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="output-iframe-tool-summary">
          <span className={`output-iframe-tool-emoji ${isRunning ? "running" : ""}`}>✨</span>
          <span className="output-iframe-tool-title">{title}</span>
          {isComplete && hasError && <span className="output-iframe-tool-error">✗</span>}
          {isComplete && !hasError && <span className="output-iframe-tool-success">✓</span>}
        </div>
        <div className="output-iframe-tool-actions">
          {isComplete && !hasError && html && (
            <>
              <button
                className="output-iframe-tool-download-btn"
                onClick={handleDownload}
                aria-label={downloadLabel}
                title={downloadLabel}
              >
                <svg
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
                  <polyline points="7 10 12 15 17 10" />
                  <line x1="12" y1="15" x2="12" y2="3" />
                </svg>
              </button>
              <button
                className="output-iframe-tool-open-btn"
                onClick={handleOpenInNewTab}
                aria-label="Open in new tab"
                title="Open in new tab"
              >
                <svg
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
                  <polyline points="15 3 21 3 21 9" />
                  <line x1="10" y1="14" x2="21" y2="3" />
                </svg>
              </button>
            </>
          )}
          <button
            className="output-iframe-tool-toggle"
            onClick={(e) => {
              e.stopPropagation();
              setIsExpanded(!isExpanded);
            }}
            aria-label={isExpanded ? "Collapse" : "Expand"}
            aria-expanded={isExpanded}
          >
            <svg
              width="12"
              height="12"
              viewBox="0 0 12 12"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
              className={`tool-chevron${isExpanded ? " tool-chevron-expanded" : ""}`}
            >
              <path
                d="M4.5 3L7.5 6L4.5 9"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        </div>
      </div>

      {isExpanded && (
        <div className="output-iframe-tool-details">
          {isComplete && !hasError && htmlWithHeightReporter && (
            <div className="output-iframe-tool-section">
              {executionTime && (
                <div className="output-iframe-tool-label">
                  <span>Output:</span>
                  <span className="output-iframe-tool-time">{executionTime}</span>
                </div>
              )}
              <div className="output-iframe-container">
                <iframe
                  ref={iframeRef}
                  srcDoc={htmlWithHeightReporter}
                  sandbox="allow-scripts"
                  title={title}
                  className="output-iframe-wrapper"
                  style={{
                    height: `${iframeHeight}px`,
                  }}
                />
              </div>
            </div>
          )}

          {isComplete && hasError && (
            <div className="output-iframe-tool-section">
              <div className="output-iframe-tool-label">
                <span>Error:</span>
                {executionTime && <span className="output-iframe-tool-time">{executionTime}</span>}
              </div>
              <pre className="output-iframe-tool-error-message">
                {toolResult && toolResult[0]?.Text
                  ? toolResult[0].Text
                  : "Failed to display HTML content"}
              </pre>
            </div>
          )}

          {isRunning && (
            <div className="output-iframe-tool-section">
              <div className="output-iframe-tool-label">Preparing HTML output...</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default OutputIframeTool;
