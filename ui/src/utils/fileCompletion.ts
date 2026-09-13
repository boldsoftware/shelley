// @ completion only inserts a quoted path; it never reads or attaches a file.
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

export function insertFilePath(text: string, token: FileToken, searchDir: string, path: string) {
  const absolutePath = `${searchDir.replace(/\/$/, "")}/${path}`;
  const inserted = JSON.stringify(absolutePath) + (token.end === text.length ? " " : "");
  return {
    text: text.slice(0, token.start) + inserted + text.slice(token.end),
    cursor: token.start + inserted.length,
  };
}
