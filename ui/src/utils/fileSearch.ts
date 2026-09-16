export interface FileSearchMatch {
  path: string;
  line?: number;
  snippet?: string;
  snippet_matched_indexes?: number[];
}

/** Attach content explanations to name matches, then append content-only hits. */
export function mergeContentMatches<T extends FileSearchMatch>(
  existing: T[],
  hits: T[],
  limit: number,
): { matches: T[]; capped: boolean } {
  const matches = existing.map((match) => ({ ...match }));
  const byPath = new Map(matches.map((match) => [match.path, match]));
  let capped = false;
  for (const hit of hits) {
    const match = byPath.get(hit.path);
    if (match) {
      match.line = hit.line;
      match.snippet = hit.snippet;
      match.snippet_matched_indexes = hit.snippet_matched_indexes;
    } else if (matches.length < limit) {
      const appended = { ...hit };
      matches.push(appended);
      byPath.set(appended.path, appended);
    } else {
      capped = true;
    }
  }
  return { matches, capped };
}

export interface HighlightSegment {
  text: string;
  hit: boolean;
}

/** Split text into highlighted/plain runs using Unicode code-point offsets. */
export function highlightSegments(text: string, positions?: number[]): HighlightSegment[] {
  if (!positions?.length) return [{ text, hit: false }];
  const chars = Array.from(text);
  const sorted = [...positions].sort((a, b) => a - b);
  const out: HighlightSegment[] = [];
  let cursor = 0;
  let i = 0;
  while (i < sorted.length) {
    let j = i;
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j] + 1) j++;
    const start = sorted[i];
    const end = sorted[j] + 1;
    if (start > cursor) out.push({ text: chars.slice(cursor, start).join(""), hit: false });
    out.push({ text: chars.slice(start, end).join(""), hit: true });
    cursor = end;
    i = j + 1;
  }
  if (cursor < chars.length) out.push({ text: chars.slice(cursor).join(""), hit: false });
  return out;
}
