import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { JSDOM } from "jsdom";

test("timeline renders both token counts as text, not HTML", (t) => {
  const dom = new JSDOM("<main></main>", { runScripts: "outside-only" });
  t.after(() => dom.window.close());
  dom.window.eval("window.TimelineGraphs = { unmount() {} };");
  dom.window.eval(
    readFileSync(new URL("../../server/debug/timelines/timeline.js", import.meta.url), "utf8"),
  );
  const root = dom.window.document.querySelector("main")!;
  const payload = '<img src=x onerror="window.injected=true"> &';
  const render = dom.window.eval("ShelleyTimeline.render") as (
    root: Element,
    lanes: unknown[],
  ) => void;
  render(root, [
    {
      id: "conversation",
      name: "Conversation",
      events: [
        {
          kind: "llm",
          category: "thinking",
          begin: 0,
          finish: 1000,
          tokens: payload,
          usage: { output_tokens: payload },
        },
      ],
    },
  ]);
  assert.ok(
    root
      .querySelector(".activity-detail")
      ?.textContent?.includes(`Tokens: ${payload} · output ${payload}`),
  );
  assert.equal(root.querySelector(".activity-detail img"), null);
});
