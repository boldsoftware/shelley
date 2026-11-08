// Favicon service for dynamic status indication. Stock Shelley ghosts the
// whole favicon while the open conversation's agent works; workspace mode
// badges exe's logo with a blue dot while any task runs (App.vue).
import { workspaceMode } from "../utils/workspaceMode";

export type FaviconStatus = "working" | "ready";

const WORKING_OPACITY = "0.35";

let currentStatus: FaviconStatus = "ready";
let originalSVG: string | null = null;

// Get the existing favicon link (injected by server)
function getFaviconLink(): HTMLLinkElement | null {
  return document.querySelector('link[rel="icon"]');
}

// Extract and decode the SVG from the data URI
function extractSVGFromDataURI(dataURI: string): string | null {
  if (!dataURI.startsWith("data:image/svg+xml,")) {
    return null;
  }
  try {
    return decodeURIComponent(dataURI.substring("data:image/svg+xml,".length));
  } catch {
    return null;
  }
}

// The workspace dot: --primary blue (the nav's working dot), ringed in white
// to stand out on the fish and on dark tabs, in a 32-unit square.
const WORKING_DOT = `<circle cx="24.5" cy="24.5" r="6" fill="#2563eb" stroke="#fff" stroke-width="2"/>`;

// The favicon to show for status: in workspace mode, svg under the dot
// while working; otherwise svg, ghosted while working.
export function applyStatus(svg: string, status: FaviconStatus, workspace = workspaceMode): string {
  if (status === "ready") {
    return svg;
  }
  if (workspace) {
    const base = "data:image/svg+xml," + encodeURIComponent(svg);
    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><image href="${base}" width="32" height="32"/>${WORKING_DOT}</svg>`;
  }

  const openingTagEnd = svg.indexOf(">");
  if (openingTagEnd === -1 || !svg.startsWith("<svg")) {
    return svg;
  }

  return `${svg.slice(0, openingTagEnd)} opacity="${WORKING_OPACITY}"${svg.slice(openingTagEnd)}`;
}

// Update the favicon to reflect the current status
export function setFaviconStatus(status: FaviconStatus): void {
  if (status === currentStatus && originalSVG !== null) {
    return;
  }

  const link = getFaviconLink();
  if (!link) {
    return;
  }

  // Capture the original SVG on first call
  if (originalSVG === null) {
    const extracted = extractSVGFromDataURI(link.href);
    if (extracted) {
      originalSVG = extracted;
    } else {
      return;
    }
  }

  currentStatus = status;
  const newSVG = applyStatus(originalSVG, status);
  link.href = "data:image/svg+xml," + encodeURIComponent(newSVG);
}

// Replace the favicon (e.g. after the emoji changes), keeping the status.
export function setFaviconHref(href: string): void {
  const svg = extractSVGFromDataURI(href);
  const link = getFaviconLink();
  if (!svg || !link) {
    return;
  }
  originalSVG = svg;
  link.href = "data:image/svg+xml," + encodeURIComponent(applyStatus(svg, currentStatus));
}

// Initialize the favicon service (call on app start)
export function initializeFavicon(): void {
  // Wait a tick for the server-injected favicon to be present
  setTimeout(() => {
    setFaviconStatus("ready");
  }, 0);
}
