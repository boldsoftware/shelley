// Tail-first conversation mounting: on load only the trailing chunks of a
// large conversation mount; older history renders as fixed-height
// placeholders that a background sweep (or scrolling near them) replaces
// with real content. See the tail-first block in ChatInterface.vue.
//
// Real chunks are 50 rows and the initial tail is 10 chunks, so exercising
// the placeholder path for real would need 500+ messages — too slow to seed
// in an e2e test. The shelley.tailFirstTest localStorage knob (read once at
// ChatInterface setup) shrinks both so a ~60-message conversation spans many
// chunks. Note LIVE_TAIL_CHUNKS=5 is not overridable, so the effective
// mounted tail is max(tailChunks, 5) chunks.
import { test, expect, type Page, type APIRequestContext } from "@playwright/test";

async function seedConversation(request: APIRequestContext, turns: number): Promise<string> {
  const generated = await request.post("/debug/loremipsum?json=1", {
    form: { size: String(turns), model: "predictable" },
  });
  expect(generated.ok()).toBeTruthy();
  const { conversation_id: conversationId } = await generated.json();
  return `loremipsum-${turns}turns-${conversationId}`;
}

/** Shrink chunking so a ~60-row conversation spans many chunks, and scale
 * the chunk height estimate to match. The estimate must stay BELOW any real
 * chunk height — production guarantees this (50-row chunks book 4000px,
 * measure ~7000px), and the sweep's stability depends on it: an
 * over-estimate deflates when a mounted chunk lays out near the viewport,
 * and that shrink can race the clamp accounting into disarming follow. 50px
 * is safely below any 5-row chunk. --messages-chunk-estimate feeds both
 * .messages-chunk's contain-intrinsic-size and the placeholder height. */
function tailFirstSetup(
  page: Page,
  overrides: { tailChunks: number; chunkSize: number; sweep?: boolean },
) {
  return page.addInitScript((opts) => {
    localStorage.setItem("shelley.tailFirstTest", JSON.stringify(opts));
    const style = document.createElement("style");
    style.textContent = ".messages-list { --messages-chunk-estimate: 50px; }";
    document.addEventListener("DOMContentLoaded", () => document.head.appendChild(style));
  }, overrides);
}

async function openConversation(page: Page, slug: string) {
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
  // The transcript can land after the composer; wait for real content so
  // chunk counts are meaningful.
  await expect(page.getByTestId("message").last()).toBeVisible({ timeout: 30000 });
}

function chunkCounts(page: Page) {
  return page.evaluate(() => ({
    pending: document.querySelectorAll('[data-testid="pending-chunk"]').length,
    mounted: document.querySelectorAll(".messages-chunk").length,
  }));
}

/** Distance from the bottom of the scroll container, in px. */
function bottomOffset(page: Page) {
  return page
    .locator(".messages-container")
    .evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
}

test.describe("Tail-first conversation mounting", () => {
  test("mounts only the tail at load and keeps the bottom pinned", async ({ page, request }) => {
    await tailFirstSetup(page, { tailChunks: 2, chunkSize: 5, sweep: false });
    const slug = await seedConversation(request, 15);
    await openConversation(page, slug);

    // Placeholders stand in for older history; the tail is real content.
    const counts = await chunkCounts(page);
    expect(counts.pending).toBeGreaterThan(0);
    expect(counts.mounted).toBeGreaterThan(0);

    // The viewport sits at the true bottom over real content — not merely
    // near it (a placeholder mispinned at the bottom would still be "near").
    expect(await bottomOffset(page)).toBeLessThan(5);
    await expect(page.getByTestId("message").last()).toBeInViewport();
    await expect(page.locator(".scroll-to-bottom-button")).not.toBeVisible();
  });

  test("background sweep mounts everything without moving the viewport", async ({
    page,
    request,
  }) => {
    await tailFirstSetup(page, { tailChunks: 2, chunkSize: 5 });
    const slug = await seedConversation(request, 15);
    await openConversation(page, slug);

    // Record the worst scroll drift DURING the sweep, not just the endpoint:
    // the follow machinery could mask a jump by re-pinning afterward.
    await page.evaluate(() => {
      const el = document.querySelector(".messages-container") as HTMLElement;
      const state = window as Window & { __maxDrift?: number };
      state.__maxDrift = 0;
      const expected = () => el.scrollHeight - el.clientHeight;
      el.addEventListener("scroll", () => {
        const drift = Math.abs(expected() - el.scrollTop);
        if (drift > (state.__maxDrift ?? 0)) state.__maxDrift = drift;
      });
    });

    // The sweep replaces every placeholder…
    await expect
      .poll(async () => (await chunkCounts(page)).pending, { timeout: 30000 })
      .toBe(0);
    //  …without disturbing the bottom-pinned viewport, even transiently.
    await expect.poll(() => bottomOffset(page), { timeout: 15000 }).toBeLessThan(5);
    const maxDrift = await page.evaluate(
      () => (window as Window & { __maxDrift?: number }).__maxDrift ?? 0,
    );
    expect(maxDrift).toBeLessThan(120);
    await expect(page.locator(".scroll-to-bottom-button")).not.toBeVisible();
  });

  test("scrolling up reveals placeholders so none is left in the viewport", async ({
    page,
    request,
  }) => {
    await tailFirstSetup(page, { tailChunks: 2, chunkSize: 5, sweep: false });
    const slug = await seedConversation(request, 15);
    await openConversation(page, slug);

    const before = await chunkCounts(page);
    expect(before.pending).toBeGreaterThan(0);

    // Scroll to the top of the transcript: placeholders enter the scrollport
    // and must hydrate into real content.
    await page
      .locator(".messages-container")
      .evaluate((el) => el.scrollTo({ top: 0, behavior: "instant" as ScrollBehavior }));
    await expect
      .poll(async () => (await chunkCounts(page)).mounted, { timeout: 15000 })
      .toBeGreaterThan(before.mounted);
    // The invariant that matters: after the reveals settle, no placeholder
    // intersects the viewport (no blank band where the user is looking).
    await expect
      .poll(
        () =>
          page.evaluate(() => {
            const container = document.querySelector(".messages-container")!;
            const view = container.getBoundingClientRect();
            let visible = 0;
            for (const el of document.querySelectorAll('[data-testid="pending-chunk"]')) {
              const r = el.getBoundingClientRect();
              if (r.bottom > view.top && r.top < view.bottom) visible++;
            }
            return visible;
          }),
        { timeout: 15000 },
      )
      .toBe(0);
  });

  test("TOC jump into unmounted history mounts the target chunk", async ({ page, request }) => {
    await tailFirstSetup(page, { tailChunks: 2, chunkSize: 5, sweep: false });
    const slug = await seedConversation(request, 15);
    await openConversation(page, slug);

    const before = await chunkCounts(page);
    expect(before.pending).toBeGreaterThan(0);

    // Jump to the first user turn via the TOC. Its chunk is a placeholder, so
    // the jump must first mount it, then scroll to and highlight the message.
    await page.locator(".toc-button").click();
    const firstUserEntry = page.locator(".toc-entry", { hasText: "Turn 1:" }).first();
    await firstUserEntry.click();

    await expect
      .poll(async () => (await chunkCounts(page)).mounted, { timeout: 15000 })
      .toBeGreaterThan(before.mounted);
    // The mount + jump landed: the highlighted target is inside the viewport
    // (toBeVisible alone doesn't check the scroll position).
    await expect(page.locator(".message-highlight")).toBeInViewport({ timeout: 15000 });
  });

  test("streaming append while history is unmounted keeps the tail pinned", async ({
    page,
    request,
  }) => {
    await tailFirstSetup(page, { tailChunks: 2, chunkSize: 5, sweep: false });
    const slug = await seedConversation(request, 15);
    await openConversation(page, slug);
    const before = await chunkCounts(page);
    expect(before.pending).toBeGreaterThan(0);

    // Send a message: the append must render at the bottom (tail chunks are
    // live), stay pinned, and not disturb the placeholder region.
    const input = page.getByTestId("message-input");
    await input.fill("echo tail-first append");
    await page.getByTestId("send-button").click();
    await expect(page.locator("text=echo tail-first append").last()).toBeVisible({
      timeout: 30000,
    });
    await expect(page.getByTestId("agent-thinking")).toBeHidden({ timeout: 30000 });

    await expect.poll(() => bottomOffset(page), { timeout: 15000 }).toBeLessThan(5);
    const after = await chunkCounts(page);
    expect(after.pending).toBeGreaterThan(0); // placeholders untouched (sweep off)
  });
});
