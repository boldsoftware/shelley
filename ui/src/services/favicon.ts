// Favicon service for dynamic status indication
// Modifies the server-injected favicon to show a colored dot when the agent state changes

type FaviconStatus = "working" | "ready";

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

// Add a status dot to the SVG
function addStatusDot(svg: string, status: FaviconStatus): string {
  // Remove the closing </svg> tag
  const closingTagIndex = svg.lastIndexOf("</svg>");
  if (closingTagIndex === -1) {
    return svg;
  }

  const svgWithoutClose = svg.substring(0, closingTagIndex);

  // Add the status dot in the bottom-right corner
  const dotColor = status === "working" ? "#f59e0b" : "#22c55e";
  const statusDot = `
  <circle cx="26" cy="26" r="6" fill="white"/>
  <circle cx="26" cy="26" r="5" fill="${dotColor}"/>
`;

  return svgWithoutClose + statusDot + "</svg>";
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
      // If we can't extract SVG, give up
      return;
    }
  }

  currentStatus = status;

  // Generate new SVG with status dot
  const newSVG = addStatusDot(originalSVG, status);
  const newDataURI = "data:image/svg+xml," + encodeURIComponent(newSVG);

  // Update the favicon
  link.href = newDataURI;
}

// Initialize the favicon service (call on app start)
export function initializeFavicon(): void {
  // Wait a tick for the server-injected favicon to be present
  setTimeout(() => {
    setFaviconStatus("ready");
  }, 0);
}
