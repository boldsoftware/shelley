import { createWriteStream } from "fs";
import { mkdtempSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { execFileSync } from "child_process";

export function writeFIFO(path: string, data: string): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const stream = createWriteStream(path);
    stream.on("error", reject);
    stream.on("close", () => resolve());
    stream.end(data);
  });
}

export function unblockFIFO(path: string): Promise<void> {
  return writeFIFO(path, "go\n");
}

export function makeFIFO(prefix: string): string {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  const fifoPath = join(dir, "fifo");
  execFileSync("mkfifo", [fifoPath]);
  return fifoPath;
}
