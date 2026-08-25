import { contextUsageLevel, contextUsageLevelLabel } from "./contextUsage";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}
function run(name: string, fn: () => void) {
  try {
    fn();
  } catch (e) {
    failed++;
    console.error(`FAIL: ${name} threw ${e}`);
  }
}

run("every non-empty level has words to go with the color", () => {
  assert(contextUsageLevelLabel("") === "", "plain says nothing");
  for (const level of ["warn", "high", "critical"] as const) {
    assert(contextUsageLevelLabel(level).length > 0, `${level} has a label`);
  }
});

run("contextUsageLevel steps on absolute token counts", () => {
  const max = 1_000_000; // large window: percentage never trips
  assert(contextUsageLevel(50_000, max) === "", "50k is plain");
  assert(contextUsageLevel(99_999, max) === "", "just under 100k is plain");
  assert(contextUsageLevel(100_000, max) === "warn", "100k warns");
  assert(contextUsageLevel(199_999, max) === "warn", "just under 200k warns");
  assert(contextUsageLevel(200_000, max) === "high", "200k is high");
  assert(contextUsageLevel(300_000, max) === "critical", "300k is critical");
  assert(contextUsageLevel(5_000_000, max) === "critical", "way over is critical");
});

run("contextUsageLevel also steps on fraction of the window", () => {
  const max = 20_000; // small window: absolute thresholds never trip
  assert(contextUsageLevel(10_000, max) === "", "half a small window is plain");
  assert(contextUsageLevel(14_000, max) === "warn", "70% warns");
  assert(contextUsageLevel(16_000, max) === "high", "80% is high");
  assert(contextUsageLevel(18_000, max) === "critical", "90% is critical");
});

run("contextUsageLevel tolerates an unknown window", () => {
  assert(contextUsageLevel(50_000, 0) === "", "no window, under 100k: plain");
  assert(contextUsageLevel(150_000, 0) === "warn", "no window, over 100k: warn");
});

if (failed > 0) {
  console.error(`\n${failed} failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\ncontextUsage tests passed (${passed})`);
