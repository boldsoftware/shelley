<!-- Vue port of components/OutputIframeTool.tsx. Renders HTML output in a
     sandboxed iframe with postMessage-based library streaming (excalidraw
     skill). jszip is framework-agnostic and reused directly.

     Preserves: .output-iframe-tool, .output-iframe-tool-header,
     .output-iframe-tool-summary, .output-iframe-tool-emoji,
     .output-iframe-tool-title, .output-iframe-tool-details,
     .output-iframe-tool-toggle, .output-iframe-tool-actions,
     .output-iframe-container, .output-iframe-wrapper,
     .output-iframe-tool-download-btn, .output-iframe-tool-open-btn,
     data-testid tool-call-completed/running, and all other classes/aria. -->
<template>
  <div
    class="output-iframe-tool"
    :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'"
  >
    <div class="output-iframe-tool-header" @click="isExpanded = !isExpanded">
      <div class="output-iframe-tool-summary">
        <span class="output-iframe-tool-emoji" :class="{ running: isRunning }">✨</span>
        <span class="output-iframe-tool-title" :title="title">{{ title }}</span>
        <span v-if="isComplete && hasError" class="output-iframe-tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="output-iframe-tool-success">✓</span>
      </div>
      <div class="output-iframe-tool-actions">
        <template v-if="isComplete && !hasError && html">
          <button
            class="output-iframe-tool-download-btn"
            :aria-label="downloadLabel"
            :title="downloadLabel"
            @click.stop="handleDownload"
          >
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
              <polyline points="7 10 12 15 17 10" />
              <line x1="12" y1="15" x2="12" y2="3" />
            </svg>
          </button>
          <button
            class="output-iframe-tool-open-btn"
            aria-label="Open in new tab"
            title="Open in new tab"
            @click.stop="handleOpenInNewTab"
          >
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
              <polyline points="15 3 21 3 21 9" />
              <line x1="10" y1="14" x2="21" y2="3" />
            </svg>
          </button>
        </template>
        <button
          class="output-iframe-tool-toggle"
          :aria-label="isExpanded ? 'Collapse' : 'Expand'"
          :aria-expanded="isExpanded"
          @click.stop="isExpanded = !isExpanded"
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            class="tool-chevron"
            :class="{ 'tool-chevron-expanded': isExpanded }"
          >
            <path
              d="M4.5 3L7.5 6L4.5 9"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </button>
      </div>
    </div>

    <div v-if="isExpanded" class="output-iframe-tool-details">
      <div
        v-if="isComplete && !hasError && htmlWithHeightReporter"
        class="output-iframe-tool-section"
      >
        <div v-if="executionTime" class="output-iframe-tool-label">
          <span>Output:</span>
          <span class="output-iframe-tool-time">{{ executionTime }}</span>
        </div>
        <div class="output-iframe-container">
          <iframe
            ref="iframeRef"
            :srcdoc="htmlWithHeightReporter"
            sandbox="allow-scripts allow-downloads"
            allow="clipboard-write"
            :title="title"
            class="output-iframe-wrapper"
            :style="{ height: iframeHeight + 'px' }"
            @load="handleIframeLoad"
          />
        </div>
      </div>

      <div v-if="isComplete && hasError" class="output-iframe-tool-section">
        <div class="output-iframe-tool-label">
          <span>Error:</span>
          <span v-if="executionTime" class="output-iframe-tool-time">{{ executionTime }}</span>
        </div>
        <pre class="output-iframe-tool-error-message">{{
          toolResult && toolResult[0]?.Text ? toolResult[0].Text : "Failed to display HTML content"
        }}</pre>
      </div>

      <div v-if="isRunning" class="output-iframe-tool-section">
        <div class="output-iframe-tool-label">Preparing HTML output...</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from "vue";
import JSZip from "jszip";
import type { LLMContent } from "../../../types";

interface EmbeddedFile {
  name: string;
  path: string;
  content: string;
  type: string;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}>();

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
  if (document.readyState === 'complete') {
    reportHeight();
  } else {
    window.addEventListener('load', reportHeight);
  }
  setTimeout(reportHeight, 100);
  setTimeout(reportHeight, 500);
  window.addEventListener('resize', reportHeight);
  if (typeof MutationObserver !== 'undefined') {
    var observer = new MutationObserver(reportHeight);
    observer.observe(document.body, { childList: true, subtree: true, attributes: true });
  }
})();
<\/script>
`;

const MIN_HEIGHT = 100;
const MAX_HEIGHT = 600;

// /static/ paths the parent fetches for each named library.
const LIBRARY_PATHS: Record<string, string> = {
  excalidraw: "/static/excalidraw/skill.js",
};

// Remove injected scripts/styles from HTML to get the original version for download
function getOriginalHtml(html: string): string {
  let result = html.replace(
    /<script>\s*window\.__FILES__\s*=\s*window\.__FILES__\s*\|\|\s*\{\};[\s\S]*?<\/script>\s*/g,
    "",
  );
  result = result.replace(/<script data-libs-bootstrap="[^"]*">[\s\S]*?<\/script>\s*/g, "");
  result = result.replace(/<style data-file="[^"]*">[\s\S]*?<\/style>\s*/g, "");
  result = result.replace(/<script data-file="[^"]*">[\s\S]*?<\/script>\s*/g, "");
  result = result.replace(/<head>\s*<\/head>\s*/g, "");
  return result;
}

// Escape HTML special characters for safe embedding
function escapeHtml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// State
const isExpanded = ref(true);
const iframeHeight = ref(300);
const iframeRef = ref<HTMLIFrameElement | null>(null);

// Extract display data
const displayDataComputed = computed(() => {
  // First try display prop (from tool result)
  if (props.display && typeof props.display === "object" && props.display !== null) {
    const d = props.display as {
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
  const input = props.toolInput;
  return {
    html:
      typeof input === "object" &&
      input !== null &&
      "html" in input &&
      typeof (input as { html: unknown }).html === "string"
        ? (input as { html: string }).html
        : undefined,
    title:
      typeof input === "object" &&
      input !== null &&
      "title" in input &&
      typeof (input as { title: unknown }).title === "string"
        ? (input as { title: string }).title
        : undefined,
    filename: undefined as string | undefined,
    files: undefined as EmbeddedFile[] | undefined,
    libraries: undefined as string[] | undefined,
  };
});

const title = computed(() => displayDataComputed.value.title || "HTML Output");
const html = computed(() => displayDataComputed.value.html);
const filename = computed(() => displayDataComputed.value.filename || "output.html");
const files = computed(() => displayDataComputed.value.files || []);
const libraries = computed(() => displayDataComputed.value.libraries || []);
const hasMultipleFiles = computed(() => files.value.length > 0);
const usesLibraries = computed(() => libraries.value.length > 0);

// Bootstrap script for library loading via postMessage
const libsBootstrapScript = computed(() => {
  if (!libraries.value.length) return "";
  return `<script data-libs-bootstrap="postmessage">
(function(){
  var resolveLibs, rejectLibs;
  window.__LIBS__ = new Promise(function(res, rej){ resolveLibs = res; rejectLibs = rej; });
  async function onMessage(ev){
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
<\/script>`;
});

const htmlWithHeightReporter = computed(() => {
  if (!html.value) return undefined;
  let out = html.value;
  const headInject = libsBootstrapScript.value;
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
});

// Listen for height messages from iframe
function handleMessage(event: MessageEvent) {
  if (
    event.data &&
    typeof event.data === "object" &&
    event.data.type === "iframe-height" &&
    typeof event.data.height === "number"
  ) {
    if (iframeRef.value && event.source === iframeRef.value.contentWindow) {
      const newHeight = Math.min(Math.max(event.data.height, MIN_HEIGHT), MAX_HEIGHT);
      iframeHeight.value = newHeight;
    }
  }
}

onMounted(() => {
  window.addEventListener("message", handleMessage);
});
onUnmounted(() => {
  window.removeEventListener("message", handleMessage);
});

// After iframe loads, fetch libraries and forward via postMessage
function handleIframeLoad() {
  if (!libraries.value.length || !iframeRef.value) return;
  const win = iframeRef.value.contentWindow;
  if (!win) return;
  (async () => {
    const libs: Record<string, string> = {};
    for (const name of libraries.value) {
      const libPath = LIBRARY_PATHS[name];
      if (!libPath) continue;
      try {
        const resp = await fetch(libPath, { credentials: "same-origin" });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        libs[name] = await resp.text();
      } catch (e) {
        console.error(`output_iframe: failed to fetch library ${name}`, e);
      }
    }
    win.postMessage({ type: "shelley-libs", libs }, "*");
  })();
}

// Fetch libraries and produce a self-contained HTML with inline base64 bootstrap
async function inlineLibrariesIntoHtml(baseHtml: string): Promise<string> {
  if (!usesLibraries.value) return baseHtml;
  const enc = new TextEncoder();
  const libsB64: Record<string, string> = {};
  for (const name of libraries.value) {
    const libPath = LIBRARY_PATHS[name];
    if (!libPath) continue;
    const resp = await fetch(libPath, { credentials: "same-origin" });
    if (!resp.ok) throw new Error(`fetch ${libPath}: HTTP ${resp.status}`);
    const text = await resp.text();
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
<\/script>`;
  if (baseHtml.includes("<head>")) {
    return baseHtml.replace("<head>", "<head>" + bootstrap);
  }
  return bootstrap + baseHtml;
}

// Open HTML in new tab with sandbox protection
async function handleOpenInNewTab(e: MouseEvent) {
  e.stopPropagation();
  if (!html.value) return;

  // Open synchronously before any await to avoid popup blocker
  const win = window.open("", "_blank");
  if (!win) return;

  const standalone = await inlineLibrariesIntoHtml(html.value);
  const escapedHtml = escapeHtml(standalone);
  const escapedTitle = escapeHtml(title.value);

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
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// Download files - single HTML or zip with all files
async function handleDownload(e: MouseEvent) {
  e.stopPropagation();
  if (!html.value) return;

  const standalone = await inlineLibrariesIntoHtml(html.value);

  if (hasMultipleFiles.value) {
    const zip = new JSZip();
    const originalHtml = getOriginalHtml(standalone);
    zip.file(filename.value, originalHtml);
    for (const file of files.value) {
      zip.file(file.path || file.name, file.content);
    }
    const zipBlob = await zip.generateAsync({ type: "blob" });
    const url = URL.createObjectURL(zipBlob);
    const a = document.createElement("a");
    a.href = url;
    const zipName = filename.value.replace(/\.[^.]+$/, "") + ".zip";
    a.download = zipName;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } else {
    const blob = new Blob([standalone], { type: "text/html" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename.value;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
const downloadLabel = computed(() => (hasMultipleFiles.value ? "Download ZIP" : "Download HTML"));
</script>
