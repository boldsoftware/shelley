// Serialize writes per file across file-editor instances. Switching files
// replaces a keyed editor, but its pending auto-save must finish before any
// later save or read of the same path.
const pendingWrites = new Map<string, Promise<Response>>();
const unsavedContents = new Map<string, { content: string }>();

export function writeFileInOrder(path: string, content: string): Promise<Response> {
  // Keep the last buffer available if a keyed editor unmounts and its save fails.
  const unsaved = { content };
  unsavedContents.set(path, unsaved);
  const previous = pendingWrites.get(path);
  const write = (previous ? previous.catch(() => undefined) : Promise.resolve()).then(async () => {
    const response = await fetch("/api/write-file", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path, content }),
    });
    if (response.ok && unsavedContents.get(path) === unsaved) unsavedContents.delete(path);
    return response;
  });
  pendingWrites.set(path, write);
  const cleanup = () => {
    if (pendingWrites.get(path) === write) pendingWrites.delete(path);
  };
  void write.then(cleanup, cleanup);
  return write;
}

export function unsavedFileContent(path: string): string | undefined {
  return unsavedContents.get(path)?.content;
}

// A custom load URL may resolve to a different path than the UI initially
// knows (user AGENTS.md), so that read waits for all outstanding file writes.
export async function waitForPendingFileWrites(path?: string): Promise<void> {
  const writes = path === undefined ? [...pendingWrites.values()] : [pendingWrites.get(path)];
  // Even when a write failed, the next editor should read the actual disk
  // state after it settles rather than stay permanently disabled.
  const settled = await Promise.allSettled(writes.filter((write) => write !== undefined));
  for (const result of settled) {
    if (result.status === "rejected") console.error("Prior file save failed:", result.reason);
    else if (!result.value.ok) console.warn(`Prior file save failed: HTTP ${result.value.status}`);
  }
}
