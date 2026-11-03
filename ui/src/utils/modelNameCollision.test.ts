// Unit tests for findDisplayNameCollision.
// Run with: tsx src/utils/modelNameCollision.test.ts
import { findDisplayNameCollision } from "./modelNameCollision";

let failed = 0;
function check(label: string, got: unknown, want: unknown) {
  if (got === want) return;
  failed++;
  console.error(`✗ ${label}: got ${String(got)}, want ${String(want)}`);
}

const models = [
  { id: "claude-opus-5.5", display_name: "Claude Opus 5.5" },
  { id: "gpt-5", display_name: "GPT-5" },
];
const id = (name: string, list = models) => findDisplayNameCollision(name, list)?.id ?? null;

check("exact id", id("gpt-5"), "gpt-5");
check("display_name", id("GPT-5"), "gpt-5");
check("case-insensitive", id("claude opus 5.5"), "claude-opus-5.5");
check("dash vs dot", id("claude-opus-5-5"), "claude-opus-5.5");
check("case + dot", id("Claude-Opus-5.5"), "claude-opus-5.5");
check("suffix no collision", id("claude-opus-5-5-api-anthropic-com"), null);
check("whitespace trimmed", id("  gpt-5  "), "gpt-5");
check("empty", id(""), null);
check("whitespace only", id("   "), null);
check("empty list", id("gpt-5", []), null);
if (failed > 0) {
  console.error(`${failed} failed`);
  process.exit(1);
}
console.log("modelNameCollision: all passed");
