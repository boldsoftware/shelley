#!/usr/bin/env node
// FAST_BUILD must change only how dist/ is compressed, never what it contains.
//
// CI's test steps build the UI with FAST_BUILD=1 (no source maps, gzip level 1
// instead of 9) because it saves ~5s on each of the ~8 shelley jobs. That is
// only safe if the bytes the server ultimately serves are identical to a
// release build's (NO_SOURCEMAPS=1, no source maps either), so this test builds
// the UI both ways and compares the names and DECOMPRESSED payloads of every
// shipped asset. A regression here would mean CI is testing a different
// artifact than the one we release, which is exactly the class of bug that is
// invisible until production. The assertion is on the executable content
// itself, not a size or file count.

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { gunzipSync } from "node:zlib";
import { readFileSync, readdirSync, rmSync, cpSync, statSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const uiDir = new URL("..", import.meta.url).pathname;

function build(env) {
  execFileSync("node", ["scripts/build.js"], {
    cwd: uiDir,
    env: { ...process.env, ...env },
    stdio: "pipe",
  });
  const snapshot = mkdtempSync(join(tmpdir(), "ui-dist-"));
  cpSync(join(uiDir, "dist"), snapshot, { recursive: true });
  return snapshot;
}

// Payload identity: decompress .gz, hash the plaintext. Keyed by logical asset
// name so main.js and main.js.gz compare equal across the two builds.
function payloads(dir) {
  const out = new Map();
  for (const file of readdirSync(dir, { recursive: true })) {
    const name = String(file);
    const full = join(dir, name);
    if (!statSync(full).isFile()) continue;
    const stat = readFileSync(full);
    // build-info.json embeds a timestamp and commit state; it is expected to differ.
    if (name.endsWith("build-info.json")) continue;
    // checksums.json is derived from the compressed bytes, so it differs by design.
    if (name.endsWith("checksums.json")) continue;
    const logical = name.endsWith(".gz") ? name.slice(0, -3) : name;
    const bytes = name.endsWith(".gz") ? gunzipSync(stat) : stat;
    out.set(logical, createHash("sha256").update(bytes).digest("hex"));
  }
  return out;
}

let failures = 0;
function check(cond, msg) {
  if (cond) {
    console.log(`\u2713 ${msg}`);
  } else {
    console.error(`\u2717 ${msg}`);
    failures++;
  }
}

const release = build({ FAST_BUILD: "", NO_SOURCEMAPS: "1" });
const fast = build({ FAST_BUILD: "1" });

const a = payloads(release);
const b = payloads(fast);

check(a.size > 0, `release build produced assets (${a.size})`);
check(
  [...a.keys()].sort().join(",") === [...b.keys()].sort().join(","),
  "both builds ship the same set of assets",
);
for (const [name, hash] of a) {
  check(b.get(name) === hash, `identical decompressed payload: ${name}`);
}

rmSync(release, { recursive: true, force: true });
rmSync(fast, { recursive: true, force: true });

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log("\nFAST_BUILD is payload-equivalent to a release build.");
