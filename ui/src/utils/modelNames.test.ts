// Unit tests for prettyModelName / prettyModelLabels.
// Run with: tsx src/utils/modelNames.test.ts
import { prettyModelName, prettyModelLabels } from "./modelNames";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function eq(input: string, want: string) {
  const got = prettyModelName(input);
  if (got === want) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ prettyModelName(${input}) = ${got}, want ${want}`);
  }
}

// Anthropic
eq("claude-opus-4.8", "Claude Opus 4.8");
eq("claude-opus-4-5", "Claude Opus 4.5");
eq("claude-opus-4-1", "Claude Opus 4.1");
eq("claude-sonnet-5", "Claude Sonnet 5");
eq("claude-haiku-4-5", "Claude Haiku 4.5");
eq("claude-fable-5", "Claude Fable 5");

// OpenAI gpt-*
eq("gpt-5.6-sol", "GPT-5.6 Sol");
eq("gpt-5.6-terra", "GPT-5.6 Terra");
eq("gpt-5.4-mini", "GPT-5.4 Mini");
eq("gpt-5.3-codex", "GPT-5.3 Codex");
eq("gpt-5", "GPT-5");
eq("gpt-5-pro", "GPT-5 Pro");
eq("gpt-4o", "GPT-4o");
eq("gpt-4o-mini", "GPT-4o Mini");
eq("gpt-4.1-nano", "GPT-4.1 Nano");
eq("gpt-oss-120b", "GPT-OSS 120B");
eq("gpt-oss-20b-fireworks", "GPT-OSS 20B");

// OpenAI o-series: no family entry, verbatim.
eq("o3", "o3");
eq("o1-preview", "o1-preview");
eq("o3-deep-research", "o3-deep-research");
eq("o4-mini", "o4-mini");
eq("codex-mini-latest", "codex-mini-latest");

// Other families
eq("glm-5.2-fireworks", "GLM 5.2");
eq("glm-5p2", "GLM 5.2");
eq("deepseek-v4-pro-fireworks", "DeepSeek V4 Pro");
eq("deepseek-v4-flash-0731-fireworks", "DeepSeek V4 Flash 0731");
eq("grok-4.5", "Grok 4.5");
eq("kimi-k3-fireworks", "Kimi K3");
eq("kimi-k2.7-code-fireworks", "Kimi K2.7 Code");
eq("kimi-k2.6-fireworks", "Kimi K2.6");
eq("minimax-m3", "MiniMax M3");
eq("minimax-m2p7", "MiniMax M2.7");
eq("qwen3.7-plus-fireworks", "Qwen3.7 Plus");

// Unknown ids pass through verbatim.
eq("predictable", "predictable");
eq("my-custom-model", "my-custom-model");
eq("claude-somethingweird-9", "claude-somethingweird-9");
eq("llama-guard-3", "llama-guard-3");

// Collision guard: two ids that prettify identically keep raw ids.
{
  const labels = prettyModelLabels([{ id: "glm-5.2" }, { id: "glm-5.2-fireworks" }]);
  const a = labels.get("glm-5.2");
  const b = labels.get("glm-5.2-fireworks");
  if (a === "glm-5.2" && b === "glm-5.2-fireworks") {
    passed++;
  } else {
    failed++;
    failures.push(`✗ collision guard: got ${a} / ${b}`);
  }
}

// Explicit display_name wins.
{
  const labels = prettyModelLabels([{ id: "claude-opus-4.8", display_name: "Big Friendly Model" }]);
  if (labels.get("claude-opus-4.8") === "Big Friendly Model") {
    passed++;
  } else {
    failed++;
    failures.push(`✗ display_name override: got ${labels.get("claude-opus-4.8")}`);
  }
}

// display_name === id is treated as absent (prettify applies).
{
  const labels = prettyModelLabels([{ id: "claude-opus-4.8", display_name: "claude-opus-4.8" }]);
  if (labels.get("claude-opus-4.8") === "Claude Opus 4.8") {
    passed++;
  } else {
    failed++;
    failures.push(`✗ identity display_name: got ${labels.get("claude-opus-4.8")}`);
  }
}

console.log(`modelNames: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  for (const f of failures) console.error(f);
  process.exit(1);
}
