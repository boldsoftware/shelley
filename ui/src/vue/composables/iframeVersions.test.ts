import assert from "node:assert/strict";
import { test } from "node:test";
import type { Message } from "../../types";
import { groupIframeVersions } from "./iframeVersions";

let seq = 0;
function msg(content: unknown[], id = `m${++seq}`): Message {
  return { message_id: id, llm_data: JSON.stringify({ Content: content }) } as Message;
}
const use = (id: string, path: string, files?: Record<string, string>) =>
  msg([{ Type: 5, ID: id, ToolName: "output_iframe", ToolInput: { path, files } }]);
const result = (id: string, error = false) => msg([{ Type: 6, ToolUseID: id, ToolError: error }]);

function group(messages: Message[]) {
  return groupIframeVersions(messages, new Map()).versions;
}

test("same path counts versions; ./ prefix is ignored", () => {
  const v = group([use("a", "chart.html"), result("a"), use("b", "./chart.html"), result("b")]);
  assert.deepEqual(v.get("a"), { path: "chart.html", version: 1, total: 2, latestId: "b" });
  assert.equal(v.get("b")?.version, 2);
});

test("shared renderer splits by bundled files, ignoring key order", () => {
  const r = "/r.html";
  const v = group([
    use("a", r, { "d.mmd": "/tmp/d.mmd", "x.css": "/x.css" }),
    result("a"),
    use("b", r, { "d.mmd": "/tmp/ci.mmd" }),
    result("b"),
    use("c", r, { "x.css": "/x.css", "d.mmd": "/tmp/d.mmd" }),
    result("c"),
  ]);
  assert.equal(v.get("a")?.latestId, "c");
  assert.equal(v.get("a")?.path, "/tmp/d.mmd, /x.css");
  assert.equal(v.get("b")?.total, 1);
});

test("failed and pending showings are not versions", () => {
  const v = group([
    use("a", "x.html"),
    result("a"),
    use("b", "x.html"),
    result("b", true),
    use("c", "x.html"),
  ]);
  assert.deepEqual(v.get("a"), { path: "x.html", version: 1, total: 1, latestId: "a" });
  assert.equal(v.has("b"), false);
  assert.equal(v.has("c"), false);
});

test("a call carried forward by compaction counts once", () => {
  const v = group([use("a", "x.html"), result("a"), use("a", "x.html"), result("a")]);
  assert.deepEqual(v.get("a"), { path: "x.html", version: 1, total: 1, latestId: "a" });
});

test("malformed rows are skipped", () => {
  const bad = [
    { message_id: "bad1", llm_data: '{"Content": [output_iframe' },
    { message_id: "bad2", llm_data: '{"Content": {"output_iframe": 1}}' },
    msg([null, { Type: 5, ID: "z", ToolName: "output_iframe", ToolInput: null }]),
  ] as Message[];
  const v = group([...bad, use("a", "x.html"), result("a")]);
  assert.equal(v.get("a")?.total, 1);
});

test("cache keeps only current messages and unchanged entries keep identity", () => {
  const ms = [use("a", "x.html"), result("a")];
  const first = groupIframeVersions(ms, new Map());
  const second = groupIframeVersions(
    [...ms, msg([{ Type: 2, Text: "hi" }])],
    first.cache,
    first.versions,
  );
  assert.equal(second.versions.get("a"), first.versions.get("a"));
  const third = groupIframeVersions(ms.slice(0, 1), second.cache, second.versions);
  assert.equal(third.cache.size, 1);
});
