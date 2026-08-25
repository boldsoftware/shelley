// Unit tests for pickReadyModel (composer + rebase-action model seeding).
// Run with: tsx src/vue/components/selectedModel.test.ts
import { pickReadyModel } from "./selectedModel";

let passed = 0;
let failed = 0;
const failures: string[] = [];
function check(name: string, cond: boolean, detail?: unknown) {
  if (cond) {
    passed++;
  } else {
    failed++;
    failures.push(`✗ ${name}${detail !== undefined ? `\n   ${JSON.stringify(detail)}` : ""}`);
  }
}

type M = { id: string; ready: boolean };
const models: M[] = [
  { id: "stale", ready: false },
  { id: "server-default", ready: true },
  { id: "other", ready: true },
];

function withDefault(id: string | undefined, fn: () => void) {
  (globalThis as { window?: unknown }).window = { __SHELLEY_INIT__: { default_model: id } };
  fn();
}

withDefault("server-default", () => {
  check("ready preference wins", pickReadyModel(models, "other") === "other");
  check(
    "unready preference falls back to server default",
    pickReadyModel(models, "stale") === "server-default",
  );
  check(
    "unknown preference falls back to server default",
    pickReadyModel(models, "gone") === "server-default",
  );
  check("no preference uses server default", pickReadyModel(models) === "server-default");
  check("empty catalog yields no model", pickReadyModel([], "other") === "");
  check("no ready models yields no model", pickReadyModel([{ id: "stale", ready: false }]) === "");
});

withDefault("not-ready-default", () => {
  check(
    "unready server default falls through to first ready model",
    pickReadyModel([{ id: "not-ready-default", ready: false }, ...models]) === "server-default",
  );
});

withDefault(undefined, () => {
  check(
    "absent server default uses first ready model",
    pickReadyModel(models) === "server-default",
  );
});

console.log(`selectedModel: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
