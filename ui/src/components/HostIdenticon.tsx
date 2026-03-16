/**
 * HostIdenticon — a small deterministic identicon generated from the hostname.
 * 5×5 grid, vertically symmetric, with a hue derived from the hostname hash.
 */

function hashString(s: string): number {
  let hash = 0;
  for (let i = 0; i < s.length; i++) {
    hash = ((hash << 5) - hash + s.charCodeAt(i)) | 0;
  }
  return hash >>> 0; // unsigned
}

function HostIdenticon({ hostname, size = 32 }: { hostname: string; size?: number }) {
  const hash = hashString(hostname);
  const hue = hash % 360;
  const fg = `hsl(${hue}, 65%, 50%)`;
  const bg = `hsl(${hue}, 25%, 90%)`;

  // Generate a 5×5 symmetric grid from bits of the hash.
  // Only need 15 bits (5 rows × 3 cols, mirrored horizontally).
  const cells: boolean[][] = [];
  for (let row = 0; row < 5; row++) {
    const r: boolean[] = [];
    for (let col = 0; col < 3; col++) {
      const bit = (row * 3 + col);
      r.push(((hash >> bit) & 1) === 1);
    }
    // Mirror: col0 col1 col2 col1 col0
    cells.push([r[0], r[1], r[2], r[1], r[0]]);
  }

  const cellSize = size / 6; // 5 cells + 1 cell padding total
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

export default HostIdenticon;
