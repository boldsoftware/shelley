import React, { useState, useRef, useEffect, useCallback } from "react";
import JSZip from "jszip";
import { ChevronRightIcon, DownloadIcon, ExternalLinkIcon } from "lucide-react";
import { LLMContent } from "../types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

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

// /static/ paths the parent fetches for each named library. Keep in sync
// with allowedLibraries in shelley/claudetool/output_iframe.go — if the Go
// allowlist accepts a name not listed here, the bundle will never reach the
// iframe and window.__LIBS__ will resolve without that entry.
const LIBRARY_PATHS: Record<string, string> = {
  excalidraw: "/static/excalidraw/skill.js",
};

// Remove injected scripts/styles from HTML to get the original version for download
function getOriginalHtml(html: string): string {
  // Remove the window.__FILES__ script block
  let result = html.replace(
    /<script>\s*window\.__FILES__\s*=\s*window\.__FILES__\s*\|\|\s*\{\};[\s\S]*?<\/script>\s*/g,
    "",
  );
  // Remove the __LIBS__ bootstrap (both postMessage and base64-inline variants).
  result = result.replace(/<script data-libs-bootstrap="[^"]*">[\s\S]*?<\/script>\s*/g, "");
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
    libraries?: string[];
  } => {
    // First try display prop (from tool result)
    if (display && typeof display === "object" && display !== null) {
      const d = display as {
        html?: string;
        title?: string;
        filename?: string;
        files?: EmbeddedFile[];
        libraries?: string[];
      };
      return {
        html: typeof d.html === "string" ? d.html : undefined,
        title: typeof d.title === "string" ? d.title : undefined,
        filename: typeof d.filename === "string" ? d.filename : undefined,
        files: Array.isArray(d.files) ? d.files : undefined,
        libraries: Array.isArray(d.libraries) ? d.libraries : undefined,
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
  const libraries = displayData.libraries || [];
  const hasMultipleFiles = files.length > 0;
  // Libraries live outside the conversation; for download / open-in-new-tab
  // we inline them as base64 in inlineLibrariesIntoHtml so the standalone
  // artifact still resolves window.__LIBS__ without the host page.
  const usesLibraries = libraries.length > 0;

  // Bootstrap: define window.__LIBS__ as a Promise that resolves once the
  // parent postMessages the library source in. The skill page can do:
  //   const { render } = (await window.__LIBS__).excalidraw;
  // Each library source is wrapped in a Blob (so the import stays inside
  // the iframe's own opaque origin — no cross-origin module load) and
  // import()ed. The actual bytes never appear in the conversation; the
  // parent fetches them from same-origin /static/ where its session cookie
  // works, and forwards them via postMessage.
  const libsBootstrapScript = libraries.length
    ? `<script data-libs-bootstrap="postmessage">
(function(){
  var resolveLibs, rejectLibs;
  window.__LIBS__ = new Promise(function(res, rej){ resolveLibs = res; rejectLibs = rej; });
  async function onMessage(ev){
    // Only accept library bytes from the embedder. The sandbox prevents
    // other frames from scripting us, but the source check is the standard
    // defense and cheap.
    if (ev.source !== window.parent) return;
    if (!ev.data || ev.data.type !== 'shelley-libs') return;
    window.removeEventListener('message', onMessage);
    var out = {};
    try {
      for (var name in ev.data.libs) {
        var src = ev.data.libs[name];
        var url = URL.createObjectURL(new Blob([src], {type: 'text/javascript'}));
        try { out[name] = await import(url); }
        finally { URL.revokeObjectURL(url); }
      }
      resolveLibs(out);
    } catch (e) { rejectLibs(e); }
  }
  window.addEventListener('message', onMessage);
})();
</script>`
    : "";
  const htmlWithHeightReporter = html
    ? (() => {
        let out = html;
        const headInject = libsBootstrapScript;
        if (out.includes("<head>")) {
          out = out.replace("<head>", "<head>" + headInject);
        } else {
          out = headInject + out;
        }
        if (out.includes("</body>")) {
          out = out.replace("</body>", HEIGHT_REPORTER_SCRIPT + "</body>");
        } else {
          out = out + HEIGHT_REPORTER_SCRIPT;
        }
        return out;
      })()
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

  // After the iframe loads, fetch each requested library from same-origin
  // /static/ (where our session cookie works) and forward the source text
  // into the iframe via postMessage. The bootstrap script in the iframe
  // resolves window.__LIBS__ with a {name: module} map.
  const libsKey = libraries.join(",");
  const handleIframeLoad = useCallback(() => {
    if (!libraries.length || !iframeRef.current) return;
    const win = iframeRef.current.contentWindow;
    if (!win) return;
    (async () => {
      const libs: Record<string, string> = {};
      for (const name of libraries) {
        const path = LIBRARY_PATHS[name];
        if (!path) continue;
        try {
          const resp = await fetch(path, { credentials: "same-origin" });
          if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
          libs[name] = await resp.text();
        } catch (e) {
          console.error(`output_iframe: failed to fetch library ${name}`, e);
        }
      }
      win.postMessage({ type: "shelley-libs", libs }, "*");
    })();
    // libraries identity changes on each render; depend on its stable string
    // form via libsKey so we don't refetch unnecessarily.
  }, [libsKey]);

  // Escape HTML special characters for safe embedding
  const escapeHtml = (str: string): string => {
    return str
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  };

  // Fetch each requested library and produce a self-contained HTML payload
  // by inlining the library sources into a synchronous bootstrap script.
  // Without this, downloaded/opened-in-tab pages would hang on a
  // never-resolved `window.__LIBS__` (no parent React app to feed them).
  //
  // The libs are still loaded via `import()` of a blob: URL so modules
  // resolve inside the page's own origin — just like the runtime path.
  const inlineLibrariesIntoHtml = async (baseHtml: string): Promise<string> => {
    if (!usesLibraries) return baseHtml;
    // Encode each library to base64 and decode at runtime. Naively inlining
    // the JS source via JSON.stringify breaks for bundles containing
    // U+2028/U+2029 (valid in JSON, syntax errors in JS string literals) or
    // other Unicode edge cases. Base64 sidesteps all of it; we lose ~33%
    // size in the produced artifact, but that artifact is downloaded on
    // demand, not stored anywhere persistent.
    const enc = new TextEncoder();
    const libsB64: Record<string, string> = {};
    for (const name of libraries) {
      const path = LIBRARY_PATHS[name];
      if (!path) continue;
      const resp = await fetch(path, { credentials: "same-origin" });
      if (!resp.ok) throw new Error(`fetch ${path}: HTTP ${resp.status}`);
      const text = await resp.text();
      // btoa requires latin-1; use a binary string built from UTF-8 bytes.
      const bytes = enc.encode(text);
      let bin = "";
      for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
      libsB64[name] = btoa(bin);
    }
    const serialized = JSON.stringify(libsB64);
    const bootstrap = `<script data-libs-bootstrap="inline">
(function(){
  var libsB64 = ${serialized};
  var dec = new TextDecoder();
  window.__LIBS__ = (async function(){
    var out = {};
    for (var name in libsB64) {
      var bin = atob(libsB64[name]);
      var bytes = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      var src = dec.decode(bytes);
      var url = URL.createObjectURL(new Blob([src], {type:'text/javascript'}));
      try { out[name] = await import(url); }
      finally { URL.revokeObjectURL(url); }
    }
    return out;
  })();
})();
</script>`;
    if (baseHtml.includes("<head>")) {
      return baseHtml.replace("<head>", "<head>" + bootstrap);
    }
    return bootstrap + baseHtml;
  };

  // Open HTML in new tab with sandbox protection.
  // Pop the window open SYNCHRONOUSLY (before any await) so the click's
  // transient user activation is still valid — otherwise the popup blocker
  // silently nukes window.open(). We then write into the about:blank doc
  // once the library bytes (if any) have been fetched.
  const handleOpenInNewTab = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!html) return;

    const win = window.open("", "_blank");
    if (!win) return; // popup blocked despite the synchronous open

    const standalone = await inlineLibrariesIntoHtml(html);
    const escapedHtml = escapeHtml(standalone);
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
  <iframe sandbox="allow-scripts allow-downloads" allow="clipboard-write" srcdoc="${escapedHtml}"></iframe>
</body>
</html>`;

    const blob = new Blob([wrapperHtml], { type: "text/html" });
    const url = URL.createObjectURL(blob);
    win.location.href = url;
    // Clean up the URL after a delay
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  // Download files - single HTML or zip with all files
  const handleDownload = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!html) return;

    const standalone = await inlineLibrariesIntoHtml(html);

    if (hasMultipleFiles) {
      // Create a zip file with all files
      const zip = new JSZip();

      // Add the original HTML (without injected content)
      const originalHtml = getOriginalHtml(standalone);
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
      const blob = new Blob([standalone], { type: "text/html" });
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
      className="my-1 overflow-hidden rounded-lg border border-border bg-card text-card-foreground"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div
        className="flex cursor-pointer items-center gap-2 px-3 py-1.5 select-none hover:bg-muted/50"
        onClick={() => setIsExpanded(!isExpanded)}
      >
        <span className={cn("shrink-0 text-base leading-none", isRunning && "animate-pulse")}>
          ✨
        </span>
        <span className="min-w-0 flex-1 truncate text-[13px] font-medium" title={title}>
          {title}
        </span>
        {isComplete && hasError && (
          <span className="shrink-0 text-xs font-medium text-destructive" aria-label="failed">
            ✗
          </span>
        )}
        {isComplete && !hasError && (
          <span
            className="shrink-0 text-xs font-medium text-emerald-600 dark:text-emerald-500"
            aria-label="succeeded"
          >
            ✓
          </span>
        )}
        <div className="flex shrink-0 items-center gap-0.5">
          {isComplete && !hasError && html && (
            <>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground"
                onClick={handleDownload}
                aria-label={downloadLabel}
                title={downloadLabel}
              >
                <DownloadIcon className="size-3.5" />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground"
                onClick={handleOpenInNewTab}
                aria-label="Open in new tab"
                title="Open in new tab"
              >
                <ExternalLinkIcon className="size-3.5" />
              </Button>
            </>
          )}
          <button
            type="button"
            className="shrink-0 text-muted-foreground"
            onClick={(e) => {
              e.stopPropagation();
              setIsExpanded(!isExpanded);
            }}
            aria-label={isExpanded ? "Collapse" : "Expand"}
            aria-expanded={isExpanded}
          >
            <ChevronRightIcon
              className={cn("size-3.5 transition-transform", isExpanded && "rotate-90")}
            />
          </button>
        </div>
      </div>

      {isExpanded && (
        <div className="border-t border-border px-3 py-2 text-sm">
          {isComplete && !hasError && htmlWithHeightReporter && (
            <div className="mb-2 last:mb-0">
              {executionTime && (
                <div className="mb-1 flex items-center gap-2 text-xs font-medium text-muted-foreground">
                  <span>Output:</span>
                  <span className="text-muted-foreground/70">{executionTime}</span>
                </div>
              )}
              <div className="overflow-hidden rounded-md border border-border bg-white">
                <iframe
                  ref={iframeRef}
                  srcDoc={htmlWithHeightReporter}
                  // allow-scripts: skills run JS.
                  // allow-downloads: skills offer file downloads (e.g.
                  // excalidraw "Download .excalidraw"); without this the
                  // anchor-click trigger is silently blocked.
                  // No allow-same-origin: the iframe stays cross-origin
                  // so it cannot read host cookies or DOM.
                  sandbox="allow-scripts allow-downloads"
                  // Permissions Policy: enable clipboard writes for
                  // skill "Copy SVG" / "Copy JSON" buttons. Cross-origin
                  // iframes need an explicit `allow` to use clipboard APIs.
                  allow="clipboard-write"
                  title={title}
                  className="output-iframe-wrapper block w-full border-0"
                  onLoad={handleIframeLoad}
                  style={{
                    height: `${iframeHeight}px`,
                  }}
                />
              </div>
            </div>
          )}

          {isComplete && hasError && (
            <div className="mb-2 last:mb-0">
              <div className="mb-1 flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <span>Error:</span>
                {executionTime && (
                  <span className="text-muted-foreground/70">{executionTime}</span>
                )}
              </div>
              <pre className="max-h-96 overflow-auto rounded-md bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-words text-destructive">
                {toolResult && toolResult[0]?.Text
                  ? toolResult[0].Text
                  : "Failed to display HTML content"}
              </pre>
            </div>
          )}

          {isRunning && (
            <div className="mb-2 text-xs font-medium text-muted-foreground last:mb-0">
              Preparing HTML output...
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default OutputIframeTool;
