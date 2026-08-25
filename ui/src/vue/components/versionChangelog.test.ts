import { strict as assert } from "node:assert";
import type { CommitInfo } from "../../types";
import { createVersionChangelogLoader, versionChangelogTags } from "./versionChangelog";

const currentTag = "v0.956.955465414";
const latestTag = "v0.960.975600146";

assert.equal(versionChangelogTags(false, true, currentTag, latestTag), null);
assert.equal(versionChangelogTags(true, false, currentTag, currentTag), null);
assert.deepEqual(versionChangelogTags(true, true, currentTag, latestTag), [currentTag, latestTag]);
assert.equal(versionChangelogTags(true, true, currentTag, ""), null);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const older = deferred<CommitInfo[]>();
const newer = deferred<CommitInfo[]>();
const loader = createVersionChangelogLoader((_, latest) => {
  return latest === "v2" ? newer.promise : older.promise;
});
const olderResult = loader.load("v0", "v1");
const newerResult = loader.load("v0", "v2");
const newestCommit = { sha: "new", message: "newest", author: "", date: "" };
newer.resolve([newestCommit]);
assert.deepEqual(await newerResult, { ok: true, commits: [newestCommit] });
older.resolve([{ sha: "old", message: "stale", author: "", date: "" }]);
assert.equal(await olderResult, null);

console.log("versionChangelogTags tests passed");
