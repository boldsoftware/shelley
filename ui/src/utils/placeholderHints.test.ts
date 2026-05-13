import {
  PLACEHOLDER_HINTS,
  hintsForPlatform,
  pickPlaceholderHint,
  PlaceholderHint,
} from "./placeholderHints";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// Platform filtering
const desktopHints = hintsForPlatform(false);
const mobileHints = hintsForPlatform(true);
assert(
  desktopHints.some((h) => h.id === "command-k"),
  "desktop includes command-k",
);
assert(!mobileHints.some((h) => h.id === "command-k"), "mobile excludes command-k");
assert(
  desktopHints.some((h) => h.id === "default"),
  "desktop includes default",
);
assert(
  mobileHints.some((h) => h.id === "default"),
  "mobile includes default",
);

// Weighted selection is deterministic with injected rand
const sample: PlaceholderHint[] = [
  { id: "a", text: "a", platform: "any", weight: 1 },
  { id: "b", text: "b", platform: "any", weight: 3 },
];
assert(pickPlaceholderHint(false, () => 0, sample).id === "a", "r=0 picks first");
assert(pickPlaceholderHint(false, () => 0.999, sample).id === "b", "r=~1 picks last");
// weight 1 / total 4 ⇒ r in [0, 0.25) picks a; r=0.3 picks b
assert(pickPlaceholderHint(false, () => 0.3, sample).id === "b", "weighted past first bucket");

// Default has the highest weight in the canonical list
const def = PLACEHOLDER_HINTS.find((h) => h.id === "default")!;
const maxWeight = Math.max(...PLACEHOLDER_HINTS.map((h) => h.weight));
assert(def.weight === maxWeight, "default has highest weight");

// pickPlaceholderHint never returns mobile-disallowed hint on mobile
for (let i = 0; i < 50; i++) {
  const h = pickPlaceholderHint(true);
  assert(h.platform !== "desktop", `mobile pick should not be desktop-only (got ${h.id})`);
}

console.log(`placeholderHints: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
