// Unit tests for customModelBadge / endpointHost.
// Run with: tsx src/utils/customModelBadge.test.ts
import { customModelBadge, endpointHost } from "./customModelBadge";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function eq(name: string, got: unknown, want: unknown) {
  if (JSON.stringify(got) === JSON.stringify(want)) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
}

eq(
  "custom",
  customModelBadge({
    source: "custom",
    base_url: "https://api.anthropic.com/v1/messages",
  }),
  { label: "custom", title: "api.anthropic.com" },
);
eq(
  "custom with source_id",
  customModelBadge({ source: "custom", source_id: "conn-1", base_url: "https://x.example/v1" }),
  null,
);
eq("built-in", customModelBadge({ source: "exe.dev gateway" }), null);
eq("undefined", customModelBadge(undefined), null);
eq("null", customModelBadge(null), null);
eq("endpointHost unparseable", endpointHost("not a url"), "not a url");
eq("endpointHost port", endpointHost("https://host:8443/path"), "host:8443");
eq("endpointHost undefined", endpointHost(undefined), "");

console.log(`customModelBadge: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  for (const f of failures) console.error(f);
  process.exit(1);
}
