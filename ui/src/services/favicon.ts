// Favicon service for dynamic status indication
// Modifies the server-injected favicon to show a colored dot when the agent state changes

type FaviconStatus = "working" | "ready";

let currentStatus: FaviconStatus = "ready";
let originalImageData: string | null = null;
let faviconImage: HTMLImageElement | null = null;
let imageLoaded = false;

// Get the existing favicon link (injected by server)
function getFaviconLink(): HTMLLinkElement | null {
  return document.querySelector('link[rel="icon"]');
}

// Draw the favicon with status dot using canvas
function drawFaviconWithStatus(
  status: FaviconStatus,
  onComplete: (dataUrl: string) => void
): void {
  if (!faviconImage || !imageLoaded) {
    return;
  }

  const size = 64; // Standard favicon size
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  // Draw the original image
  ctx.drawImage(faviconImage, 0, 0, size, size);

  // Draw status dot in bottom-right corner
  const dotRadius = 10;
  const dotX = size - dotRadius - 4;
  const dotY = size - dotRadius - 4;

  // White border
  ctx.beginPath();
  ctx.arc(dotX, dotY, dotRadius + 2, 0, Math.PI * 2);
  ctx.fillStyle = "white";
  ctx.fill();

  // Colored dot
  ctx.beginPath();
  ctx.arc(dotX, dotY, dotRadius, 0, Math.PI * 2);
  ctx.fillStyle = status === "working" ? "#f59e0b" : "#22c55e";
  ctx.fill();

  onComplete(canvas.toDataURL("image/png"));
}

// Update the favicon to reflect the current status
export function setFaviconStatus(status: FaviconStatus): void {
  if (status === currentStatus && originalImageData !== null) {
    return;
  }

  const link = getFaviconLink();
  if (!link) {
    return;
  }

  // Capture the original image on first call
  if (originalImageData === null) {
    originalImageData = link.href;

    // Load the image
    faviconImage = new Image();
    faviconImage.crossOrigin = "anonymous";
    faviconImage.onload = () => {
      imageLoaded = true;
      // Now that image is loaded, apply the status
      drawFaviconWithStatus(status, (dataUrl) => {
        link.href = dataUrl;
      });
    };
    faviconImage.src = originalImageData;
  }

  currentStatus = status;

  // If image already loaded, update immediately
  if (imageLoaded) {
    drawFaviconWithStatus(status, (dataUrl) => {
      link.href = dataUrl;
    });
  }
}

// Initialize the favicon service (call on app start)
export function initializeFavicon(): void {
  // Wait a tick for the server-injected favicon to be present
  setTimeout(() => {
    setFaviconStatus("ready");
  }, 0);
}
