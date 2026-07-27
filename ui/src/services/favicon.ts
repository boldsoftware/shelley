// Favicon service for dynamic conversation-status indication
// When the agent is working, the entire favicon is ghosted rather than badged

type FaviconStatus = "working" | "ready";

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

// Ghost every element in the favicon while the agent is working
function applyStatus(svg: string, status: FaviconStatus): string {
  if (status === "ready") {
    return svg;
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

// Initialize the favicon service (call on app start)
export function initializeFavicon(): void {
  // Wait a tick for the server-injected favicon to be present
  setTimeout(() => {
    setFaviconStatus("ready");
  }, 0);
}
