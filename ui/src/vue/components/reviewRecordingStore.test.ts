// ReviewRecordingStore keeps a recording's audio and events until deleted.
import "fake-indexeddb/auto";
import { ReviewRecordingStore, type ReviewSession } from "./reviewRecordingStore";

function assertEqual<T>(actual: T, expected: T, msg: string): void {
  const a = JSON.stringify(actual);
  const e = JSON.stringify(expected);
  if (a !== e) throw new Error(`${msg}: expected ${e}, got ${a}`);
}

async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

const session = (id: string): ReviewSession => ({
  id,
  cwd: "/repo",
  mimeType: "audio/webm",
  startedAt: "2026-09-29T00:00:00.000Z",
  durationMs: 0,
  conversationId: "c1",
});
const bytes = (text: string) => new TextEncoder().encode(text).buffer as ArrayBuffer;

await run("assembles audio and events in order, per session", async () => {
  const store = new ReviewRecordingStore("review-test-order");
  await store.create(session("a"));
  await store.create(session("b"));
  // Writes can land out of order; sequence numbers decide the result.
  await store.appendChunk("a", 1, bytes("world"), 2000);
  await store.appendChunk("a", 0, bytes("hello "), 1000);
  await store.appendChunk("b", 0, bytes("other"), 1000);
  await store.appendEvents("a", 1, [{ ms: 5, type: "stop" }]);
  await store.appendEvents("a", 0, [{ ms: 0, type: "view", where: "Files" }]);
  assertEqual(await (await store.audio("a", "audio/webm")).text(), "hello world", "audio");
  assertEqual((await store.audio("a", "audio/webm")).type, "audio/webm", "mime");
  assertEqual(
    (await store.events("a")).map((event) => event.type),
    ["view", "stop"],
    "events",
  );
  assertEqual((await store.get("a"))?.durationMs, 1000, "duration follows the last write");
});

await run("updates and deletes one session without touching another", async () => {
  const store = new ReviewRecordingStore("review-test-delete");
  await store.create(session("a"));
  await store.create(session("b"));
  await store.appendChunk("a", 0, bytes("a"), 1);
  await store.appendChunk("b", 0, bytes("b"), 1);
  const updated = await store.update("a", { durationMs: 5, audioPath: "/tmp/a.webm" });
  assertEqual([updated.durationMs, updated.audioPath], [5, "/tmp/a.webm"], "update");
  await store.delete("a");
  assertEqual(
    (await store.list()).map((s) => s.id),
    ["b"],
    "remaining sessions",
  );
  assertEqual((await store.audio("a", "audio/webm")).size, 0, "chunks deleted");
  assertEqual(await (await store.audio("b", "audio/webm")).text(), "b", "other chunks kept");
  let failed = 0;
  await store.update("a", {}).catch(() => failed++);
  await store.appendChunk("a", 1, bytes("late"), 2).catch(() => failed++);
  assertEqual(failed, 2, "writing to a deleted session fails");
  assertEqual((await store.audio("a", "audio/webm")).size, 0, "no orphaned chunks");
});
