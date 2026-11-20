// High-level entrypoint for the excalidraw skill. Loaded as a module from
// /static/excalidraw/skill.js inside the sandboxed `output_html` produced
// by the skill template.
//
// Encapsulates: stylesheet injection, font-asset path, the Excalidraw React
// component, the scrollToContent-on-mount dance, and the three buttons
// (Download .excalidraw / Copy SVG with embedded scene / Copy JSON).
//
// The skill template just calls `render({ elements, mountId, statusId })`
// after providing the raw skeleton-form elements array. The toolbar button
// ids are fixed (`download`, `copy-svg`, `copy-json`).
import * as React from "react";
import * as ReactDOMClient from "react-dom/client";
import * as Mod from "@excalidraw/excalidraw";
// Resolved by esbuild via a node_modules-relative import (the package's
// exports map blocks the bare specifier, so we reach into the install path).
import excalidrawCSS from "../node_modules/@excalidraw/excalidraw/dist/prod/index.css";

function injectStylesheet() {
  if (document.querySelector("style[data-excalidraw-style]")) return;
  const style = document.createElement("style");
  style.setAttribute("data-excalidraw-style", "");
  style.textContent = excalidrawCSS;
  document.head.appendChild(style);
}

export function render({ elements, mountId = "editor", statusId = "status" } = {}) {
  // We're loaded from a blob: URL inside a sandboxed iframe, so cross-origin
  // font fetches won't work. Leave EXCALIDRAW_ASSET_PATH unset and let
  // Excalidraw fall back to system fonts; exported .excalidraw/SVG still
  // carry the font names so the diagram looks right when reopened.
  injectStylesheet();

  const initialElements = Mod.convertToExcalidrawElements(elements || [], { regenerateIds: false });
  let apiRef = null;
  const editor = React.createElement(Mod.Excalidraw, {
    initialData: { elements: initialElements, appState: { viewBackgroundColor: "#ffffff" } },
    // View-only: the sandboxed iframe has no edits-out channel, so any
    // change the user makes here would be silently lost on the next
    // render. Pan/zoom still work, and the toolbar's Copy JSON / Copy SVG
    // / Download buttons still capture the current scene, so users can
    // round-trip edits via the agent or excalidraw.com.
    viewModeEnabled: true,
    // Hide the built-in Library panel — the skill's own toolbar provides
    // download/copy actions, and library import/export inside the sandbox
    // is more confusing than useful.
    renderTopRightUI: () => null,
    libraryReturnUrl: null,
    excalidrawAPI: (api) => {
      apiRef = api;
      setTimeout(
        () => api.scrollToContent(initialElements, { fitToContent: true, animate: false }),
        0,
      );
    },
  });
  const mount = document.getElementById(mountId);
  if (!mount) throw new Error(`render: mount element #${mountId} not found`);
  ReactDOMClient.createRoot(mount).render(editor);

  const status = document.getElementById(statusId);
  const setStatus = (msg) => {
    if (status) status.textContent = msg;
  };

  function currentScene() {
    if (!apiRef) return null;
    const els = apiRef.getSceneElements();
    const appState = apiRef.getAppState();
    return {
      type: "excalidraw",
      version: 2,
      source: "shelley",
      elements: els,
      appState: {
        gridSize: appState.gridSize ?? null,
        viewBackgroundColor: appState.viewBackgroundColor || "#ffffff",
      },
      files: apiRef.getFiles() || {},
    };
  }

  async function copyText(text, label) {
    // Try the async Clipboard API first; in a sandboxed iframe with
    // `allow="clipboard-write"` it works in Chrome as long as transient
    // activation is still valid. Fall back to a hidden-textarea +
    // execCommand('copy') for sandboxed contexts where the async API is
    // blocked (Safari, older Chromes, or when activation has expired
    // after a slow await).
    try {
      await navigator.clipboard.writeText(text);
      setStatus(`copied ${label} (${text.length} chars)`);
      return;
    } catch {
      /* fall through to legacy path */
    }
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      ta.style.left = "-9999px";
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      const ok = document.execCommand("copy");
      ta.remove();
      if (ok) {
        setStatus(`copied ${label} (${text.length} chars)`);
      } else {
        setStatus("copy failed: execCommand returned false");
      }
    } catch (e) {
      setStatus("copy failed: " + e.message);
    }
  }

  // Safari/WebKit invalidates the transient-activation token across awaits,
  // so an async producer (exportToSvg / exportToBlob) followed by
  // navigator.clipboard.write* gets rejected with NotAllowedError. The
  // ClipboardItem API accepts a Promise<Blob> per MIME type for exactly this
  // case — the activation is bound to the synchronous .write() call, and
  // the platform waits for the producer to settle.
  async function copyBlob(producePromise, mime, label) {
    if (typeof ClipboardItem === "undefined" || !navigator.clipboard?.write) {
      setStatus("copy failed: ClipboardItem unsupported in this browser");
      return;
    }
    try {
      const item = new ClipboardItem({ [mime]: producePromise });
      await navigator.clipboard.write([item]);
      setStatus(`copied ${label}`);
    } catch (e) {
      setStatus("copy failed: " + (e?.message || e));
    }
  }

  // Excalidraw's exportToSvg/exportToBlob both want an appState; build the
  // shared options once.
  function exportOpts() {
    return {
      elements: apiRef.getSceneElements(),
      appState: {
        ...apiRef.getAppState(),
        exportEmbedScene: true,
        exportBackground: true,
        viewBackgroundColor: "#ffffff",
      },
      files: apiRef.getFiles() || null,
      exportingFrame: null,
    };
  }

  async function sceneSvgString() {
    const svg = await Mod.exportToSvg(exportOpts());
    return new XMLSerializer().serializeToString(svg);
  }

  async function scenePngBlob() {
    // exportToBlob defaults to image/png; bump pixel density for crisp
    // pasting / wallpaper-quality downloads.
    return await Mod.exportToBlob({
      ...exportOpts(),
      mimeType: "image/png",
      quality: 1,
      exportScale: 2,
    });
  }

  function triggerDownload(blob, filename) {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = filename;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  function bind(id, fn) {
    const el = document.getElementById(id);
    if (el) el.addEventListener("click", fn);
  }

  // The skill template provides buttons with these ids; bind whatever exists.
  bind("download", () => {
    const scene = currentScene();
    if (!scene) return;
    const blob = new Blob([JSON.stringify(scene, null, 2)], {
      type: "application/vnd.excalidraw+json",
    });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "diagram.excalidraw";
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    setStatus("downloaded");
  });
  bind("download-png", async () => {
    if (!apiRef) return;
    try {
      triggerDownload(await scenePngBlob(), "diagram.png");
      setStatus("downloaded PNG");
    } catch (e) {
      setStatus("PNG export failed: " + (e?.message || e));
    }
  });
  bind("copy-json", () => {
    const scene = currentScene();
    if (!scene) return;
    copyText(JSON.stringify(scene.elements, null, 2), "JSON");
  });
  bind("copy-svg", () => {
    if (!apiRef) return;
    // exportEmbedScene serializes the full scene into the <svg> as metadata,
    // so the resulting SVG round-trips back into Excalidraw. Wrap the
    // string in a Blob so we can hand it to ClipboardItem as a
    // Promise<Blob> — keeps the user-activation token alive across the
    // async export on Safari.
    const blobP = sceneSvgString().then((s) => new Blob([s], { type: "text/plain" }));
    copyBlob(blobP, "text/plain", "SVG");
  });
  bind("copy-png", () => {
    if (!apiRef) return;
    copyBlob(scenePngBlob(), "image/png", "PNG");
  });

  return {
    getApi: () => apiRef,
    getScene: currentScene,
  };
}
