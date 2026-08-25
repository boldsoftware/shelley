import { test, expect, type Locator } from "@playwright/test";
import { createConversationViaAPI, setPageFeatureFlag } from "./helpers";

// A running tool pill shows the last line of the tool's streamed output in
// place of its headline, so a long-running command is legible without opening
// its detail modal. The output is transient: it disappears when the tool
// finishes and the headline comes back.
test("running tool pill shows the last line of streamed output", async ({ page, request }) => {
  await setPageFeatureFlag(page, "tool-pills", true);

  const slug = await createConversationViaAPI(request, "hello");
  await page.goto(`/c/${slug}`);
  const input = page.getByTestId("message-input");
  await expect(input).toBeVisible({ timeout: 30000 });

  // Emit a new line every 200ms for ~12s: bash reports its output tail to the
  // UI every 500ms while it runs. The lines are longer than the pill can show
  // on the default 393px-wide phone project, so they exercise clipping.
  await input.fill(
    "bash: /opt/toolchain/bin/some-really-long-builder-name --flag; " +
      'for i in $(seq 1 60); do echo "tick-$i compiling a rather long module name ... ok"; sleep 0.2; done',
  );
  await page.getByTestId("send-button").click();

  // The conversation's only tool call, so it can be tracked across states.
  const pill = page.locator('.tool-pill[data-tool-name="bash"]');
  await expect(pill).toHaveAttribute("data-testid", "tool-call-running", { timeout: 30000 });
  const output = pill.locator(".tool-pill-output");
  const headline = pill.locator(".tool-pill-text");
  await expect(output).toHaveText(/tick-\d+/, { timeout: 30000 });

  // The output replaces the headline while it streams, and the headline stays
  // the button's accessible name (so the pill doesn't rename itself ~2x/sec)
  // and its hover title (so what's running stays discoverable).
  await expect(headline).toHaveCount(0);
  await expect(pill).toHaveAttribute("aria-label", /^some-really[\s\S]*running$/);
  await expect(output).toHaveAttribute("title", /^some-really[\s\S]*tick-\d+/);

  // The pill tracks the newest line rather than sticking at the first one.
  // Reads 0 once the tool finishes (the output span is replaced by the
  // headline, so the locator matches nothing), which fails the poll promptly
  // and legibly instead of hanging on a detached element.
  const tick = async () => {
    const texts = await output.allTextContents();
    return Number(/tick-(\d+)/.exec(texts[0] ?? "")?.[1] ?? 0);
  };
  const first = await tick();
  await expect.poll(tick, { timeout: 15000 }).toBeGreaterThan(first);
  // Still exactly one label several ticks in: the headline does not creep back
  // alongside the output.
  await expect(headline).toHaveCount(0);

  // The output clips rather than widening the pill row past the conversation
  // (the default project is a 393px-wide phone, where there is no slack).
  await expect(output).toHaveCount(1);
  const overflow = (l: Locator) => l.evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(await overflow(pill.locator("xpath=ancestor::ul[1]"))).toBeLessThanOrEqual(1);
  expect(await overflow(page.locator("html"))).toBeLessThanOrEqual(1);

  // Once the tool completes the headline is back and the output is gone.
  await expect(pill).toHaveAttribute("data-testid", "tool-call-completed", { timeout: 60000 });
  await expect(output).toHaveCount(0);
  await expect(headline).toBeVisible();
});
