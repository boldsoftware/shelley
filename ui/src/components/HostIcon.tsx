/**
 * HostIcon — loads an LLM-generated SVG icon for the current host from /api/host-icon.
 * Falls back to a simple hash-based identicon if the API icon isn't available yet.
 */
import { useState, useEffect } from "react";

function hashString(s: string): number {
  let hash = 0;
  for (let i = 0; i < s.length; i++) {
    hash = ((hash << 5) - hash + s.charCodeAt(i)) | 0;
  }
  return hash >>> 0;
}

function FallbackIdenticon({ hostname, size }: { hostname: string; size: number }) {
  const hash = hashString(hostname);
  const hue = hash % 360;
  const fg = `hsl(${hue}, 65%, 50%)`;
  const bg = `hsl(${hue}, 25%, 90%)`;

  const cells: boolean[][] = [];
  for (let row = 0; row < 5; row++) {
    const r: boolean[] = [];
    for (let col = 0; col < 3; col++) {
      r.push(((hash >> (row * 3 + col)) & 1) === 1);
    }
    cells.push([r[0], r[1], r[2], r[1], r[0]]);
  }

  const cellSize = size / 6;
  const pad = cellSize / 2;

  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      className="host-identicon"
      aria-label={`Host: ${hostname}`}
    >
      <title>{hostname}</title>
      <rect className="identicon-bg" width={size} height={size} rx={size * 0.15} fill={bg} />
      {cells.map((row, ri) =>
        row.map((on, ci) =>
          on ? (
            <rect
              key={`${ri}-${ci}`}
              x={pad + ci * cellSize}
              y={pad + ri * cellSize}
              width={cellSize}
              height={cellSize}
              fill={fg}
            />
          ) : null,
        ),
      )}
    </svg>
  );
}

function HostIcon({ hostname, size = 36 }: { hostname: string; size?: number }) {
  const [svgMarkup, setSvgMarkup] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/host-icon")
      .then((res) => {
        if (!res.ok) throw new Error(res.statusText);
        return res.text();
      })
      .then((svg) => {
        if (!cancelled) setSvgMarkup(svg);
      })
      .catch(() => {}); // fallback rendered below
    return () => {
      cancelled = true;
    };
  }, []);

  if (!svgMarkup) {
    return <FallbackIdenticon hostname={hostname} size={size} />;
  }

  return (
    <div
      className="host-icon"
      style={{ width: size, height: size }}
      title={hostname}
      // biome-ignore lint/security/noDangerouslySetInnerHtml: trusted server SVG
      dangerouslySetInnerHTML={{ __html: svgMarkup }}
    />
  );
}

export default HostIcon;
