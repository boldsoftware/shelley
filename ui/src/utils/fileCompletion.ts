// @ completion inserts a relative path reference; it never reads or attaches a file.
export interface FileToken {
  start: number;
  end: number;
  query: string;
}

/** Unquoted tokens end at whitespace or prose punctuation. @"..." allows
 * spaces and punctuation inside filenames.
 * Inspect the whole token so completing in its middle replaces its suffix too.
 * A selection, email address, or escaped @ is not a completion request. */
export function fileTokenAt(text: string, cursor: number, selectionEnd = cursor): FileToken | null {
  if (cursor !== selectionEnd || cursor < 0 || cursor > text.length) return null;
  const tokens = /(?:^|\s)@(?:"((?:\\.|[^"\n\\])*)"?|([^\s"@,:;!?()[\]{}]*))/g;
  for (const match of text.matchAll(tokens)) {
    const start = match.index! + match[0].indexOf("@");
    const end = match.index! + match[0].length;
    if (cursor <= start || cursor > end) continue;
    const quoted = text[start + 1] === '"';
    const queryStart = start + (quoted ? 2 : 1);
    if (cursor < queryStart) return null;
    const queryEnd = quoted ? Math.min(cursor, queryStart + match[1].length) : cursor;
    let query = text.slice(queryStart, queryEnd);
    if (quoted) query = query.replace(/\\(["\\])/g, "$1");
    return { start, end, query };
  }
  return null;
}

function splitPath(value: string) {
  const parts: string[] = [];
  for (const part of value.split("/")) {
    if (!part || part === ".") continue;
    if (part === ".." && parts.length && parts.at(-1) !== "..") parts.pop();
    else parts.push(part);
  }
  return parts;
}

/** Resolve a search result against searchDir, then display it relative to cwd. */
export function relativeFilePath(cwd: string, searchDir: string, path: string) {
  const isDirectory = path.endsWith("/");
  const base = splitPath(cwd);
  const target = splitPath(`${searchDir}/${path}`);
  let common = 0;
  while (common < base.length && base[common] === target[common]) common++;
  const relative = [
    ...Array.from({ length: base.length - common }, () => ".."),
    ...target.slice(common),
  ].join("/");
  return (relative || ".") + (isDirectory ? "/" : "");
}

export function relativeFileMatch<T extends { path: string; matched_indexes?: number[] }>(
  cwd: string,
  searchDir: string,
  match: T,
): T {
  const path = relativeFilePath(cwd, searchDir, match.path);
  const offset = Array.from(path).length - Array.from(match.path).length;
  return {
    ...match,
    path,
    matched_indexes: match.matched_indexes
      ?.map((index) => index + offset)
      .filter((index) => index >= 0),
  };
}

function referenceForPath(path: string) {
  // Quote only characters that would end or split an @ token.
  if (!/[\s"@,:;!?()[\]{}]/.test(path)) return `@${path}`;
  return `@${JSON.stringify(path)}`;
}

export function insertFilePath(text: string, token: FileToken, path: string) {
  const inserted = referenceForPath(path) + (token.end === text.length ? " " : "");
  return {
    text: text.slice(0, token.start) + inserted + text.slice(token.end),
    cursor: token.start + inserted.length,
  };
}
