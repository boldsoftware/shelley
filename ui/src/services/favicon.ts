// Favicon service for dynamic notification badge

let baseIconLoaded: HTMLImageElement | null = null;
let currentState: "ready" | "working" | null = null;

// Load the base icon image once
function loadBaseIcon(): Promise<HTMLImageElement> {
  if (baseIconLoaded) {
    return Promise.resolve(baseIconLoaded);
  }

  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => {
      baseIconLoaded = img;
      resolve(img);
    };
    img.onerror = reject;
    img.src = "/icon-192.png";
  });
}

// Set the favicon to a data URL
function setFaviconUrl(url: string) {
  // Remove any existing favicon links
  document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove());

  const link = document.createElement("link");
  link.rel = "icon";
  link.href = url;
  document.head.appendChild(link);
}

// Draw the icon with an optional notification dot
async function drawFavicon(showDot: boolean): Promise<string> {
  const img = await loadBaseIcon();

  const size = 32; // Standard favicon size
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d")!;

  // Clear and draw base icon
  ctx.clearRect(0, 0, size, size);
  ctx.drawImage(img, 0, 0, size, size);

  if (showDot) {
    // Draw red notification dot in top-right corner
    const dotRadius = 6;
    const cx = size - dotRadius - 1;
    const cy = dotRadius + 1;

    // White border for visibility
    ctx.beginPath();
    ctx.arc(cx, cy, dotRadius + 1, 0, Math.PI * 2);
    ctx.fillStyle = "#ffffff";
    ctx.fill();

    // Red dot
    ctx.beginPath();
    ctx.arc(cx, cy, dotRadius, 0, Math.PI * 2);
    ctx.fillStyle = "#ef4444"; // Tailwind red-500
    ctx.fill();
  }

  return canvas.toDataURL("image/png");
}

/**
 * Update favicon to show ready state (with notification dot)
 */
export async function setFaviconReady() {
  if (currentState === "ready") return;
  currentState = "ready";

  try {
    const url = await drawFavicon(true);
    setFaviconUrl(url);
  } catch (err) {
    console.error("Failed to set favicon:", err);
  }
}

/**
 * Update favicon to show working state (no dot)
 */
export async function setFaviconWorking() {
  if (currentState === "working") return;
  currentState = "working";

  try {
    const url = await drawFavicon(false);
    setFaviconUrl(url);
  } catch (err) {
    console.error("Failed to set favicon:", err);
  }
}

