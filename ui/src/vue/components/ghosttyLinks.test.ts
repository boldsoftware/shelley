import { describe, expect, it, report } from "../../test/vitestShim";
import { urlsOnRow, type UrlRow } from "./ghosttyLinks";

// A buffer of `cols`-wide rows. A row ending in "\\" soft-wraps onto the
// next; with `unknown` the flag is left out, as for ghostty's scrollback.
function buffer(cols: number, lines: string[], unknown = false) {
  return (y: number): UrlRow | undefined => {
    const line = lines[y];
    if (line === undefined) return undefined;
    const soft = line.endsWith("\\");
    const text = (soft ? line.slice(0, -1) : line).padEnd(cols);
    const continues = lines[y - 1]?.endsWith("\\") ?? false;
    return { cells: [...text], continues: unknown ? undefined : continues };
  };
}

const found = (url: string, sx: number, sy: number, ex: number, ey: number) => ({
  url,
  start: { x: sx, y: sy },
  end: { x: ex, y: ey },
});

describe("urlsOnRow", () => {
  const url = "https://example.com/abcdefghijklmnopqrstuvwxyz";
  // 46 characters over 20-column rows, after "go ".
  const wrapped = ["go https://example.c\\", "om/abcdefghijklmnopq\\", "rstuvwxyz then", "next"];

  it("finds the whole URL from each row it wraps over", () => {
    for (const y of [0, 1, 2]) {
      expect(urlsOnRow(y, buffer(20, wrapped))).toEqual([found(url, 3, 0, 8, 2)]);
    }
    expect(urlsOnRow(3, buffer(20, wrapped))).toEqual([]);
  });

  it("stops at a newline, even after a full row", () => {
    const rows = buffer(20, ["https://example.com/", "abc"]);
    expect(urlsOnRow(0, rows)).toEqual([found("https://example.com/", 0, 0, 19, 0)]);
    expect(urlsOnRow(1, rows)).toEqual([]);
  });

  it("takes a full row without the flag as wrapped", () => {
    const rows = buffer(
      20,
      ["go https://example.c", "om/abcdefghijklmnopq", "rstuvwxyz then"],
      true,
    );
    expect(urlsOnRow(1, rows)).toEqual([found(url, 3, 0, 8, 2)]);
    expect(urlsOnRow(1, buffer(20, ["go https://example.", "com/x"], true))).toEqual([]);
  });

  it("leaves out trailing punctuation and URLs elsewhere in the line", () => {
    const rows = buffer(10, ["see http:/\\", "/a.b/c). o\\", "r ftp://x.\\", "y/z"]);
    expect(urlsOnRow(1, rows)).toEqual([found("http://a.b/c", 4, 0, 5, 1)]);
    expect(urlsOnRow(3, rows)).toEqual([found("ftp://x.y/z", 2, 2, 2, 3)]);
  });

  it("reads no further than a URL could reach", () => {
    const read: number[] = [];
    const rows = buffer(10, ["aaaaaaaaa\\", "a a https:\\", "//b.c/dddd\\", "e f\\", "gggggggggg"]);
    urlsOnRow(2, (y) => (read.push(y), rows(y)));
    expect(read.sort()).toEqual([1, 2, 3]);
    // Nor above a row that doesn't carry on from it.
    read.length = 0;
    urlsOnRow(1, (y) => (read.push(y), buffer(20, ["https://example.com/", "abc"])(y)));
    expect(read).toEqual([1]);
  });

  it("maps wide characters to their cells", () => {
    const rows = (y: number): UrlRow | undefined =>
      [
        { cells: ["漢", "", " ", "h", "t", "t", "p", ":", "/", "/"], continues: false },
        { cells: ["a", ".", "b", " ", "👩‍💻", "", " ", " ", " ", " "], continues: true },
      ][y];
    expect(urlsOnRow(0, rows)).toEqual([found("http://a.b", 3, 0, 2, 1)]);
  });
});

report("ghosttyLinks");
