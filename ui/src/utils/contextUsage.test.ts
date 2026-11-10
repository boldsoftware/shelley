import { contextUsageLevel, contextUsageLevelLabel, longConversationAdvice } from "./contextUsage";

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
  assert(contextUsageLevel(50_000, max, false) === "", "50k is plain");
  assert(contextUsageLevel(99_999, max, false) === "", "just under 100k is plain");
  assert(contextUsageLevel(100_000, max, false) === "warn", "100k warns");
  assert(contextUsageLevel(199_999, max, false) === "warn", "just under 200k warns");
  assert(contextUsageLevel(200_000, max, false) === "high", "200k is high");
  assert(contextUsageLevel(300_000, max, false) === "critical", "300k is critical");
  assert(contextUsageLevel(5_000_000, max, false) === "critical", "way over is critical");
});

run("contextUsageLevel also steps on fraction of the window", () => {
  const max = 20_000; // small window: absolute thresholds never trip
  assert(contextUsageLevel(10_000, max, false) === "", "half a small window is plain");
  assert(contextUsageLevel(14_000, max, false) === "warn", "70% warns");
  assert(contextUsageLevel(16_000, max, false) === "high", "80% is high");
  assert(contextUsageLevel(18_000, max, false) === "critical", "90% is critical");
});

run("contextUsageLevel tolerates an unknown window", () => {
  assert(contextUsageLevel(50_000, 0, false) === "", "no window, under 100k: plain");
  assert(contextUsageLevel(150_000, 0, false) === "warn", "no window, over 100k: warn");
});

run("contextUsageLevel stays plain when the agent is nudged", () => {
  assert(contextUsageLevel(150_000, 1_000_000, true) === "", "150k with nudges: plain");
  assert(contextUsageLevel(5_000_000, 1_000_000, true) === "", "way over with nudges: plain");
  assert(contextUsageLevel(19_000, 20_000, true) === "", "95% with nudges: plain");
});

run("longConversationAdvice waits for 250k, well after the count colors", () => {
  const max = 1_000_000;
  assert(!longConversationAdvice(100_000, max, false), "100k colors but says nothing");
  assert(!longConversationAdvice(249_999, max, false), "just under 250k says nothing");
  assert(longConversationAdvice(250_000, max, false), "250k advises");
  assert(!longConversationAdvice(0, 0, false), "empty, unknown window: nothing");
  assert(longConversationAdvice(300_000, 0, false), "unknown window, over 250k: advises");
});

run("longConversationAdvice still comes before a small window fills", () => {
  const max = 200_000; // 250k would never arrive
  assert(!longConversationAdvice(170_000, max, false), "85% says nothing");
  assert(longConversationAdvice(180_000, max, false), "90% advises");
  assert(!longConversationAdvice(244_000, 272_000, false), "a window past 250k waits for 250k");
});

run("longConversationAdvice is silent when the agent is nudged", () => {
  assert(!longConversationAdvice(250_000, 1_000_000, true), "250k with nudges: nothing");
  assert(!longConversationAdvice(5_000_000, 1_000_000, true), "way over with nudges: nothing");
  assert(!longConversationAdvice(190_000, 200_000, true), "95% with nudges: nothing");
});

if (failed > 0) {
  console.error(`\n${failed} failed, ${passed} passed`);
  process.exit(1);
}
console.log(`\ncontextUsage tests passed (${passed})`);
