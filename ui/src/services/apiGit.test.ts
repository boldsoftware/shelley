import { api } from "./api";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

async function withFetch(fake: typeof globalThis.fetch, fn: () => Promise<void>): Promise<void> {
  const original = globalThis.fetch;
  globalThis.fetch = fake;
  try {
    await fn();
  } finally {
    globalThis.fetch = original;
  }
}

async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`✓ ${name}`);
  } catch (error) {
    console.error(`✗ ${name}`);
    throw error;
  }
}

await run("getGitDiffs requests the recorded commit on its recorded host", async () => {
  let requestedUrl = "";
  await withFetch(
    (async (input) => {
      requestedUrl = String(input);
      return Response.json({ diffs: [], gitRoot: "/tmp/repo" });
    }) as typeof globalThis.fetch,
    async () => {
      await api.getGitDiffs(
        "/tmp/a repo",
        { conversationId: "c1", expectedMachine: "", expectedCheckout: "/tmp/a repo" },
        "abc1234",
      );
    },
  );
  assert(
    requestedUrl ===
      "/api/git/diffs?cwd=%2Ftmp%2Fa+repo&conversation_id=c1&expected_machine=&expected_checkout=%2Ftmp%2Fa+repo&commit=abc1234",
    `url = ${requestedUrl}`,
  );
});
