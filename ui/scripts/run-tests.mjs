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

import { readdirSync, statSync } from "node:fs";
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

let failed = 0;
for (const entry of entries) {
  const rel = relative(root, entry);
  console.log(`\n→ ${rel}`);
  const code = await new Promise((resolve) => {
    const child = spawn("tsx", [entry], { stdio: "inherit", cwd: root });
    child.on("error", (e) => {
      console.error(`failed to spawn tsx: ${e.message}`);
      resolve(1);
    });
    child.on("exit", (c) => resolve(c ?? 1));
  });
  if (code !== 0) {
    failed++;
    console.error(`✗ ${rel} exited ${code}`);
  }
}

if (failed > 0) {
  console.error(`\n${failed} test file(s) failed`);
  process.exit(1);
}
console.log(`\n✓ ${entries.length} test file(s) passed`);
