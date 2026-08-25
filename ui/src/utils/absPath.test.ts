import { resolveAbsPath } from "./absPath";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

run("absolute path passes through", () => {
  assert(resolveAbsPath("/a/b/c.txt", "/base") === "/a/b/c.txt", "absolute");
});

run("relative path joins the base", () => {
  assert(resolveAbsPath("b/c.txt", "/base") === "/base/b/c.txt", "joined");
  assert(resolveAbsPath("./b/c.txt", "/base") === "/base/b/c.txt", "dot-slash stripped");
  assert(resolveAbsPath("b/c.txt", "/base/") === "/base/b/c.txt", "trailing slash on base");
});

run("normalizes . .. and duplicate separators", () => {
  assert(resolveAbsPath("/a//b/./c.txt", null) === "/a/b/c.txt", "dupes and dots");
  assert(resolveAbsPath("../sibling/c.txt", "/base/dir") === "/base/sibling/c.txt", "parent");
  assert(resolveAbsPath("/../../x", null) === "/x", "cannot escape the root");
});

run("returns null when unresolvable", () => {
  assert(resolveAbsPath("", "/base") === null, "empty path");
  assert(resolveAbsPath("rel.txt", null) === null, "no base");
  assert(resolveAbsPath("rel.txt", "") === null, "empty base");
  assert(resolveAbsPath("rel.txt", "relative/base") === null, "relative base");
});

console.log("absPath tests passed");
