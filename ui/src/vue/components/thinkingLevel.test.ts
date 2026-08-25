import {
  normalizeThinkingLevelForModel,
  roundThinkingLevel,
  supportedThinkingLevels,
} from "./thinkingLevel";

let passed = 0;
let failed = 0;

function expectRound(
  level: Parameters<typeof roundThinkingLevel>[0],
  supported: Parameters<typeof roundThinkingLevel>[1],
  want: ReturnType<typeof roundThinkingLevel>,
) {
  const got = roundThinkingLevel(level, supported);
  if (got === want) {
    passed++;
  } else {
    failed++;
    console.error(
      `FAIL: roundThinkingLevel(${level}, ${supported.join(",")}) = ${got}, want ${want}`,
    );
  }
}

expectRound("high", ["low", "high"], "high");
expectRound("max", ["off", "high", "xhigh"], "xhigh");
expectRound("xhigh", ["high", "max"], "high");
expectRound("minimal", ["off", "low"], "low");
expectRound("off", ["low", "high"], "low");

function expectModelLevel(
  level: Parameters<typeof normalizeThinkingLevelForModel>[0],
  model: Parameters<typeof normalizeThinkingLevelForModel>[1],
  want: ReturnType<typeof normalizeThinkingLevelForModel>,
) {
  const got = normalizeThinkingLevelForModel(level, model);
  if (got === want) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: normalizeThinkingLevelForModel(${level}) = ${got}, want ${want}`);
  }
}

expectModelLevel(
  "max",
  { supports_reasoning: true, reasoning_levels: ["off", "high", "xhigh"] },
  "xhigh",
);
expectModelLevel("minimal", { supports_reasoning: true, reasoning_levels: ["off", "low"] }, "low");
expectModelLevel("high", { supports_reasoning: true, reasoning_levels: ["off"] }, "default");
expectModelLevel("high", { supports_reasoning: false }, "default");
expectModelLevel("max", { supports_reasoning: true }, "default");
expectModelLevel("xhigh", { supports_reasoning: true }, "xhigh");

function expectSupported(
  model: Parameters<typeof supportedThinkingLevels>[0],
  want: readonly string[],
) {
  const got = supportedThinkingLevels(model);
  if (got.join(",") === want.join(",")) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: supportedThinkingLevels = ${got.join(",")}, want ${want.join(",")}`);
  }
}

expectSupported({ supports_reasoning: true, reasoning_levels: ["off", "high", "max"] }, [
  "off",
  "high",
  "max",
]);
expectSupported({ supports_reasoning: false }, []);
expectSupported({ supports_reasoning: true }, ["off", "minimal", "low", "medium", "high", "xhigh"]);
expectSupported(undefined, ["off", "minimal", "low", "medium", "high", "xhigh"]);

if (failed > 0) process.exit(1);
console.log(`thinkingLevel: ${passed} passed`);
