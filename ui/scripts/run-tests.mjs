#!/usr/bin/env node
// Discover and run every test file under src/.
//
// Two conventions coexist:
//   * <name>.test.ts          — self-executing on import
//   * <name>.test.ts + <name>.test.runner.ts
//                              — the .test.ts exports runTests(); the .runner.ts
//                                invokes it. In that case the runner is the
//                                entry point and the .test.ts is a library.
//
// We glob for both, and for any pair we prefer the .test.runner.ts.
//
// Each file runs in its own tsx process, and most of a file's wall time is that
// process starting up, so files run concurrently: one per CPU, but at most 16,
// since a few files time real deadlines and the CI host's 128 CPUs are shared
// with every other job in the build. A file's output is buffered and printed as
// a block when it exits, so blocks never interleave.

import { readdirSync, statSync } from "node:fs";
import { availableParallelism } from "node:os";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const here = fileURLToPath(new URL(".", import.meta.url));
const root = join(here, "..");
const srcDir = join(root, "src");

function walk(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    const s = statSync(p);
    if (s.isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const all = walk(srcDir);
const runners = new Set();
const plain = new Set();
for (const f of all) {
  if (f.endsWith(".test.runner.ts")) runners.add(f);
  else if (f.endsWith(".test.ts") || f.endsWith(".test.tsx")) plain.add(f);
}
// Drop any .test.ts that has a matching .test.runner.ts sibling.
for (const r of runners) {
  const base = r.replace(/\.test\.runner\.ts$/, ".test.ts");
  plain.delete(base);
}

const entries = [...[...runners].sort(), ...[...plain].sort()];
if (entries.length === 0) {
  console.error("no test files found under src/");
  process.exit(1);
}

// The files still running, so a CI timeout (SIGTERM) or Ctrl-C can still show
// which files hung and what they printed, and stop them.
const running = new Map();

function run(rel, entry) {
  return new Promise((resolve) => {
    const out = [];
    const child = spawn("tsx", [entry], { stdio: ["ignore", "pipe", "pipe"], cwd: root });
    running.set(rel, { child, out });
    child.stdout.on("data", (d) => out.push(d));
    child.stderr.on("data", (d) => out.push(d));
    child.on("error", (e) => {
      out.push(Buffer.from(`failed to spawn tsx: ${e.message}\n`));
      resolve({ status: "failed to spawn", out });
    });
    // "close", not "exit": it fires only once both pipes have drained.
    child.on("close", (code, signal) =>
      resolve({ status: code === 0 ? "" : signal ? `killed by ${signal}` : `exited ${code}`, out }),
    );
  }).finally(() => running.delete(rel));
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    for (const [rel, { child, out }] of running) {
      child.kill(signal);
      process.stdout.write(`\n→ ${rel} (still running at ${signal})\n${Buffer.concat(out)}`);
    }
    process.exit(1);
  });
}

let failed = 0;
const queue = [...entries];
async function worker() {
  for (let entry = queue.shift(); entry; entry = queue.shift()) {
    const rel = relative(root, entry);
    const { status, out } = await run(rel, entry);
    process.stdout.write(`\n→ ${rel}\n${Buffer.concat(out)}`);
    if (status) {
      failed++;
      console.error(`✗ ${rel} ${status}`);
    }
  }
}
await Promise.all(Array.from({ length: Math.min(availableParallelism(), 16) }, worker));

if (failed > 0) {
  console.error(`\n${failed} test file(s) failed`);
  process.exit(1);
}
console.log(`\n✓ ${entries.length} test file(s) passed`);
