import assert from "node:assert/strict";
import { test } from "node:test";
import { applyStatus } from "./favicon";

const fish = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 205 193"><path fill="#F48E00" d="M0 0h1"/></svg>`;

test("workspace mode dots the logo only while working", () => {
  assert.equal(applyStatus(fish, "ready", true), fish);
  const working = applyStatus(fish, "working", true);
  assert.match(
    working,
    /^<svg [^>]*viewBox="0 0 32 32"><image href="data:image\/svg\+xml,([^"]+)"/,
  );
  assert.equal(decodeURIComponent(working.match(/href="data:image\/svg\+xml,([^"]+)"/)![1]), fish);
  assert.match(working, /<circle [^>]*fill="#2563eb"[^>]*\/><\/svg>$/);
});

test("stock Shelley ghosts the favicon while working", () => {
  assert.equal(applyStatus(fish, "ready", false), fish);
  assert.equal(applyStatus(fish, "working", false), fish.replace('193">', '193" opacity="0.35">'));
});
