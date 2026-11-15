import { hookDiagnosticPrompt } from "./hookHealth";
import type { HookHealthStatus } from "../types";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

const status: HookHealthStatus = {
  episode_id: 4,
  revision: 9,
  active: true,
  dismissed: false,
  failures: 2,
  last_failure_at: "2026-11-14T23:00:00Z",
  conversation_id: "affected-chat-42",
};
const prompt = hookDiagnosticPrompt({
  ...status,
  stderr: "RAW_STDERR_SENTINEL",
  error: "RAW_ERROR_SENTINEL",
  webhook_url: "https://example.invalid/SECRET_URL_SENTINEL",
} as HookHealthStatus);

for (const [value, label] of [
  ["end-of-turn", "hook name"],
  ["Consecutive failed invocations: 2", "failure count"],
  ["2026-11-14T23:00:00Z", "latest failure timestamp"],
  ["affected-chat-42", "affected conversation"],
  ["read-only", "read-only-first instruction"],
]) {
  assert(prompt.includes(value), `includes safe ${label}`);
}
assert(/redact/i.test(prompt), "includes redaction instruction");
for (const secret of ["RAW_STDERR_SENTINEL", "RAW_ERROR_SENTINEL", "SECRET_URL_SENTINEL"]) {
  assert(!prompt.includes(secret), `omits ${secret}`);
}

console.log("hookHealth tests passed");
