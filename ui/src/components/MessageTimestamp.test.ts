import { JSDOM } from "jsdom";

const dom = new JSDOM("");
const g = globalThis as Record<string, unknown>;
g.window = dom.window;
g.document = dom.window.document;

import { formatRelative, formatDay } from "./MessageTimestamp";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// formatRelative covers a range of bucket sizes
assert(formatRelative(0) === "just now", "0ms is just now");
assert(formatRelative(2000) === "just now", "<5s is just now");
assert(formatRelative(-10_000) === "just now", "negative deltas don't go backwards");
assert(formatRelative(30_000) === "30s ago", "30s");
assert(formatRelative(5 * 60_000) === "5m ago", "5 minutes");
assert(formatRelative(3 * 3600_000) === "3h ago", "3 hours");
assert(formatRelative(2 * 86_400_000) === "2d ago", "2 days");
assert(formatRelative(45 * 86_400_000) === "2mo ago", "45 days -> 2 months (rounded)");
assert(formatRelative(400 * 86_400_000) === "1y ago", "400 days -> 1 year");

// formatDay: same year omits year, different year includes it
const now = new Date(2026, 4, 11); // May 11 2026
const sameYear = formatDay(new Date(2026, 0, 3), now);
assert(/2026/.test(sameYear) === false, `same-year omits year: got "${sameYear}"`);
const otherYear = formatDay(new Date(2024, 0, 3), now);
assert(/2024/.test(otherYear), `different-year includes year: got "${otherYear}"`);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
