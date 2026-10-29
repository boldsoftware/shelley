import assert from "node:assert/strict";
import { test } from "node:test";
import type { ActionRunSummary } from "../services/workspace";
import { elapsed, runStatus } from "./actionStatus";

const run = (changes: Partial<ActionRunSummary> = {}): ActionRunSummary => ({
  id: "run",
  conversation_id: "task",
  project: "Blog",
  action_id: "publish",
  label: "Publish",
  status: "succeeded",
  started_at: "2026-01-01T12:00:00Z",
  exit_code: 0,
  report: "skipped",
  ...changes,
});

test("a run's pill is its command's outcome", () => {
  assert.equal(runStatus(run({ status: "running" })).label, "Running");
  assert.equal(runStatus(run({ status: "failed", exit_code: 2 })).label, "Failed (exit 2)");
  assert.equal(runStatus(run({ status: "interrupted" })).label, "Interrupted");
  assert.equal(runStatus(run()).label, "Succeeded");
});

test("elapsed times", () => {
  assert.equal(elapsed("2026-01-01T12:00:00Z", "2026-01-01T12:00:42Z"), "42s");
  assert.equal(elapsed("2026-01-01T12:00:00Z", "2026-01-01T12:03:12Z"), "3m 12s");
  assert.equal(elapsed("2026-01-01T12:00:00Z", "2026-01-01T13:04:00Z"), "1h 4m");
  assert.equal(
    elapsed("2026-01-01T12:00:00Z", undefined, Date.parse("2026-01-01T12:00:05Z")),
    "5s",
  );
  assert.equal(elapsed(undefined), "");
});
