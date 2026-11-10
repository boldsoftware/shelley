import assert from "node:assert/strict";
import { test } from "node:test";
import { formatCompactTime } from "./compactTime";
import { en } from "../i18n/en";

const MIN = 60_000;
const DAY = 24 * 60 * MIN;
const fmt = (ms: number) => formatCompactTime(ms, (k) => en[k]);

test("formats ages as a number and a one-letter unit", () => {
  const cases: [number, string][] = [
    [-5 * MIN, "now"],
    [0, "now"],
    [59_999, "now"],
    [MIN, "1m"],
    [59 * MIN + 59_999, "59m"],
    [60 * MIN, "1h"],
    [DAY - 1, "23h"],
    [DAY, "1d"],
    [7 * DAY - 1, "6d"],
    [7 * DAY, "1w"],
    [29 * DAY, "4w"],
    [30 * DAY, "1mo"],
    [61 * DAY, "2mo"],
    [364 * DAY, "11mo"],
    [365 * DAY, "1y"],
    [3 * 365 * DAY, "3y"],
  ];
  for (const [ms, want] of cases) assert.equal(fmt(ms), want, `${ms}ms`);
});

test("takes its units from the locale", () => {
  const units: Record<string, string> = { timeNow: "jetzt", timeHours: "{n} Std." };
  const t = (k: string) => units[k] ?? "?";
  assert.equal(formatCompactTime(0, t), "jetzt");
  assert.equal(formatCompactTime(2 * 60 * MIN, t), "2 Std.");
});
