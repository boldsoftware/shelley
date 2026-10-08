import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
import { dirname } from "node:path";
import {
  createConversationViaAPI,
  createConversationViaAPIWithDetails,
  testWorkingDirectory,
} from "./helpers";

// A small directory that is certain to exist on the machine running the suite,
// for the cwd-change spec. The e2e folder itself: a handful of entries, so the
// picker renders instantly (unlike /tmp on a busy machine).
const e2eDir = dirname(fileURLToPath(import.meta.url));

// Anchored-popover contract for the two floating popups migrating to PrimeVue
// Popover: the ConversationTOC "Jump to…" panel and the ContextUsageBar token
// popup (opened from the "<tokens> · <model>" status label). These specs pin
// the DOM/ARIA contract (classes, labels, dismissal behavior) so it holds
// across the hand-rolled and PrimeVue implementations.

test.describe("Conversation TOC popover", () => {
  test("opens from the nav button, lists entries, and dismisses", async ({ page, request }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo table of contents");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const tocButton = page.locator(".toc-button");
    await expect(tocButton).toBeVisible({ timeout: 30000 });
    await expect(tocButton).toHaveAttribute("aria-expanded", "false");
    await expect(tocButton).toHaveAccessibleName("Conversation table of contents");

    await tocButton.click();
    const popover = page.locator(".toc-popover");
    await expect(popover).toBeVisible();
    await expect(popover.locator(".toc-popover-list")).toBeVisible();
    await expect(tocButton).toHaveAttribute("aria-expanded", "true");
    await expect(popover.locator(".toc-popover-title")).toHaveText("Jump to…");

    // First/last entries are the fixed top/bottom anchors; the seeded user
    // message appears as a .toc-entry-user row in between.
    const entries = popover.locator(".toc-entry");
    await expect(entries.first()).toContainText("Top of conversation");
    await expect(entries.last()).toContainText("End of conversation");
    await expect(popover.locator(".toc-entry-user").first()).toContainText(
      "echo table of contents",
    );

    // Escape dismisses.
    await page.keyboard.press("Escape");
    await expect(popover).toBeHidden();
    await expect(tocButton).toHaveAttribute("aria-expanded", "false");

    // Outside click dismisses.
    await tocButton.click();
    await expect(popover).toBeVisible();
    await page.locator(".messages-container").click({ position: { x: 10, y: 10 } });
    await expect(popover).toBeHidden();

    // Clicking a user entry closes the popover and records a #m-<short> hash.
    await tocButton.click();
    await expect(popover).toBeVisible();
    await popover.locator(".toc-entry-user").first().click();
    await expect(popover).toBeHidden();
    await expect(async () => {
      expect(new URL(page.url()).hash).toMatch(/^#m-[a-zA-Z0-9]+$/);
    }).toPass({ timeout: 5000 });
  });

  test("shows timeline images as thumbnails", async ({ page, request }) => {
    test.setTimeout(180000);

    const inlineSlug = await createConversationViaAPI(request, "screenshot image", {
      agentTimeout: 90000,
    });
    await page.goto(`/c/${inlineSlug}`);
    const inlineImage = page.locator(".message-agent img").first();
    await expect(inlineImage).toBeVisible({ timeout: 30000 });

    await page.locator(".toc-button").click();
    const inlineEntry = page.locator(".toc-popover .toc-entry-eot").filter({
      hasText: "Verified against the real product",
    });
    const inlineThumbnail = inlineEntry.locator(".toc-entry-thumbnail");
    await expect(inlineThumbnail).toBeVisible();
    expect(await inlineThumbnail.getAttribute("src")).toBe(await inlineImage.getAttribute("src"));
    await page.keyboard.press("Escape");

    const toolSlug = await createConversationViaAPI(request, "screenshot", {
      agentTimeout: 90000,
    });
    await page.goto(`/c/${toolSlug}`);
    const toolImage = page.locator(".screenshot-tool img").first();
    await expect(toolImage).toBeVisible({ timeout: 30000 });
    const toolImageSrc = await toolImage.getAttribute("src");
    await page.locator(".screenshot-tool-header").first().click();
    await expect(toolImage).toBeHidden();

    await page.locator(".toc-button").click();
    const toolEntry = page.locator(".toc-popover .toc-entry-image").first();
    const toolThumbnail = toolEntry.locator(".toc-entry-thumbnail");
    await expect(toolThumbnail).toBeVisible();
    expect(await toolThumbnail.getAttribute("src")).toBe(toolImageSrc);

    await toolEntry.click();
    await expect(page.locator(".toc-popover")).toBeHidden();
    await expect(page).toHaveURL(/#t-[a-zA-Z0-9]+$/);
    await expect(page.locator(".screenshot-tool").first()).toHaveClass(/message-highlight/);
  });
});

test.describe("Message action bar", () => {
  test("uses CSS-only hover labels", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "echo action bar");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const message = page
      .locator('[data-testid="message"]')
      .filter({ hasText: "echo action bar" })
      .first();
    await expect(message).toBeVisible({ timeout: 30000 });
    await message.hover();

    const copy = message.getByRole("button", { name: "Copy" });
    await expect(copy).toBeVisible();
    await expect(copy).toHaveAttribute("data-tooltip", "Copy");
  });

  test("follows long messages and flips clipped tooltips below", async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, "echo sticky action bar");
    await page.setViewportSize({ width: 390, height: 667 });
    await page.goto(`/c/${slug}`);

    const message = page.locator('[data-testid="message"].message-agent').last();
    await expect(message).toBeVisible({ timeout: 30000 });
    const entity = message.locator('[data-content-entity="content"]');
    await expect(entity).toBeVisible();

    await entity.evaluate((element) => {
      element.style.minHeight = "1000px";
      const scrollContainer = element.closest<HTMLElement>(".messages-container");
      if (!scrollContainer) throw new Error("message scroll container not found");
      const containerTop = scrollContainer.getBoundingClientRect().top;
      scrollContainer.scrollTop += element.getBoundingClientRect().top - containerTop + 96;
      element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    const actionBar = entity.locator(".message-action-bar-wrapper");
    await expect(actionBar).toBeVisible();
    const stickyInset = await actionBar.evaluate((element) => {
      const scrollContainer = element.closest<HTMLElement>(".messages-container");
      if (!scrollContainer) throw new Error("message scroll container not found");
      return element.getBoundingClientRect().top - scrollContainer.getBoundingClientRect().top;
    });
    expect(stickyInset).toBeCloseTo(8, 0);

    const fork = actionBar.getByRole("button", { name: "Fork conversation from here" });
    await fork.hover();
    await expect(fork).toHaveAttribute("data-tooltip-placement", "bottom");

    await entity.evaluate((element) => {
      const scrollContainer = element.closest<HTMLElement>(".messages-container");
      if (!scrollContainer) throw new Error("message scroll container not found");
      const containerTop = scrollContainer.getBoundingClientRect().top;
      scrollContainer.scrollTop += element.getBoundingClientRect().top - containerTop - 120;
    });
    await fork.dispatchEvent("mouseover");
    await expect(fork).toHaveAttribute("data-tooltip-placement", "top");
  });
});

test.describe("Context usage popup", () => {
  test("toggles from the usage label and closes on outside click", async ({ page, request }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo context usage");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const label = page.locator(".context-usage-label");
    await expect(label).toBeVisible({ timeout: 30000 });
    // The label reads "<tokens> · <model name>"; the terse visible text is
    // spelled out for assistive tech.
    await expect(label.locator(".context-usage-label-tokens")).not.toBeEmpty();
    // The readout reports actual usage only; no denominator or percentage. A
    // short test conversation is nowhere near any threshold, so no suffix either.
    await expect(label).toHaveAccessibleName(/^Context usage: .+ tokens$/);
    await expect(label).toHaveAttribute("aria-expanded", "false");

    await label.click();
    const popup = page.locator(".chat-context-popup");
    await expect(popup).toBeVisible();
    await expect(popup).toContainText("LLM call");
    // The current context size heads the popup, labeled.
    await expect(popup.locator(".usage-popup-title")).toHaveText(/^Current context: .+ tokens$/);
    await expect(label).toHaveAttribute("aria-expanded", "true");
    // The panel is teleported out of the button's subtree, so aria-controls is
    // the only thing tying the two together. It must resolve to the dialog.
    const panelId = await label.getAttribute("aria-controls");
    expect(panelId).toBeTruthy();
    await expect(page.locator(`#${panelId}`)).toHaveAttribute("role", "dialog");
    // The cost graph is always on (no feature flag) but its usage entries are
    // walked lazily, only once the label is hovered/focused/clicked. It must
    // actually populate — an empty walk renders "No usage data yet."
    await expect(popup.locator(".token-cost-graph")).toBeVisible();
    await expect(popup.locator(".token-cost-graph-svg")).toBeVisible();

    // Clicking the label again toggles it closed.
    await label.click();
    await expect(popup).toBeHidden();
    await expect(label).toHaveAttribute("aria-expanded", "false");

    // Reopen, then an outside click dismisses. aria-expanded must follow the
    // popover even on the dismissal paths that never reach our click handler.
    await label.click();
    await expect(popup).toBeVisible();
    await page.locator(".messages-container").click({ position: { x: 10, y: 10 } });
    await expect(popup).toBeHidden();
    await expect(label).toHaveAttribute("aria-expanded", "false");

    // Escape dismisses too (new with the PrimeVue Popover port).
    await label.click();
    await expect(popup).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(popup).toBeHidden();
    await expect(label).toHaveAttribute("aria-expanded", "false");
  });

  test("lists subagents in the breakdown and includes them in the total", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    // Desktop width, where sub-agents widen the popup.
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.route("**/api/model-costs", async (route) => {
      const body = route.request().postDataJSON() as { models: { model: string }[] };
      const costs = Object.fromEntries(
        body.models.map(({ model }) => [
          model,
          { input: 10, output: 10, cache_read: 10, cache_write: 10 },
        ]),
      );
      await route.fulfill({ json: { costs } });
    });
    let subagentRequests = 0;
    let subagentUsd = 55.161;
    let blockSubagentRequest = false;
    let releaseSubagentRequest: (() => void) | null = null;
    await page.route("**/api/conversation/*/subagent-usage", async (route) => {
      subagentRequests++;
      if (blockSubagentRequest) {
        await new Promise<void>((resolve) => {
          releaseSubagentRequest = resolve;
        });
        releaseSubagentRequest = null;
      }
      await route.fulfill({
        json: {
          per_model: [
            {
              model: "review-model",
              url: "https://review.test",
              llm_calls: 9,
              input_tokens: subagentUsd * 1e5,
              output_tokens: 0,
              cache_creation_input_tokens: 0,
              cache_read_input_tokens: 0,
              estimated_usd: subagentUsd,
              reported_usd: 0,
              cost: { input: 10, output: 10, cache_read: 10, cache_write: 10 },
            },
          ],
          llm_calls: 9,
          estimated_usd: subagentUsd,
          reported_usd: 0,
          unpriced_reported_usd: 0,
          unpriced_models: [],
          unpriced_calls: 0,
        },
      });
    });

    const slug = await createConversationViaAPI(request, "echo cost subtotal");
    await page.clock.install();
    await page.goto(`/c/${slug}`);
    await page.locator(".context-usage-label").click();

    const subagentRow = page.getByTestId("subagent-cost-row");
    await expect(subagentRow).toBeVisible({ timeout: 30000 });
    await expect(subagentRow).toContainText("$55.16");

    const total = page.getByTestId("token-cost-total");
    await expect(total).toBeVisible();
    await expect(total).toContainText("Total");
    await expect(total.locator("th")).toHaveCSS("border-top-style", "solid");
    // The total heads the receipt, above the subtotals, which in turn sit above
    // the per-model breakdown, rather than trailing it where a scrolling popup
    // cuts them off.
    const subtotalBox = (await page.getByTestId("conversation-cost-subtotal").boundingBox())!;
    const totalRowBox = (await total.boundingBox())!;
    const firstModelBox = (await page
      .locator(".token-cost-model-breakdown")
      .first()
      .boundingBox())!;
    expect(totalRowBox.y).toBeLessThan(subtotalBox.y);
    expect(subtotalBox.y + subtotalBox.height).toBeLessThanOrEqual(firstModelBox.y + 1);
    // The graphs cover the main conversation only, and say so.
    await expect(
      page.locator(".token-cost-graph-note", { hasText: "main conversation only" }),
    ).toBeVisible();
    // The graphs stretch across the popup, as wide as the receipt below them,
    // and the context size lines up with their left edge.
    const titleBox = (await page.locator(".usage-popup-title").boundingBox())!;
    const graphBox = (await page.locator(".token-cost-graph-svg").boundingBox())!;
    const tableBox = (await page.locator(".token-cost-table").boundingBox())!;
    expect(graphBox.width).toBeGreaterThan(500);
    expect(Math.abs(graphBox.width - tableBox.width)).toBeLessThan(1);
    expect(Math.abs(titleBox.x - graphBox.x)).toBeLessThan(1);
    const subagentCostBox = await subagentRow.locator(".token-cost-legend-cost").boundingBox();
    const totalCostBox = await total.locator(".token-cost-legend-cost").boundingBox();
    expect(subagentCostBox).not.toBeNull();
    expect(totalCostBox).not.toBeNull();
    expect(
      Math.abs(
        subagentCostBox!.x + subagentCostBox!.width - (totalCostBox!.x + totalCostBox!.width),
      ),
    ).toBeLessThan(1);

    blockSubagentRequest = true;
    const requestsAfterFirstOpen = subagentRequests;
    await page.clock.fastForward(5000);
    await expect.poll(() => subagentRequests).toBeGreaterThan(requestsAfterFirstOpen);
    const requestsWithSlowPoll = subagentRequests;
    await page.clock.fastForward(15000);
    expect(subagentRequests).toBe(requestsWithSlowPoll);
    subagentUsd = 60;
    blockSubagentRequest = false;
    const release = releaseSubagentRequest;
    expect(release).not.toBeNull();
    release?.();
    await expect.poll(() => releaseSubagentRequest).toBeNull();
    await expect(subagentRow).toContainText("$60.00");
    const subagentModels = page
      .locator(".token-cost-model-breakdown")
      .filter({ hasText: "review-model" });
    await expect(subagentModels).toContainText("review-model");
    await expect(subagentModels).toContainText("6.0M");
    await expect(subagentModels).toContainText("$60.00");

    const requestsBeforeReopen = subagentRequests;
    await page.locator(".context-usage-label").click();
    await expect(total).toBeHidden();
    await page.locator(".context-usage-label").click();
    await expect(total).toBeVisible();
    await expect.poll(() => subagentRequests).toBeGreaterThan(requestsBeforeReopen);
  });

  test("graphs stack, and each can be switched off", async ({ page, request }) => {
    test.setTimeout(60000);
    // Wide enough for the checkboxes to share a line with the context size.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.route("**/api/model-costs", async (route) => {
      const body = route.request().postDataJSON() as { models: { model: string }[] };
      const costs = Object.fromEntries(
        body.models.map(({ model }) => [
          model,
          { input: 10, output: 10, cache_read: 10, cache_write: 10 },
        ]),
      );
      await route.fulfill({ json: { costs } });
    });
    const slug = await createConversationViaAPI(request, "echo delta cost");
    await page.goto(`/c/${slug}`);
    await page.locator(".context-usage-label").click();

    const popup = page.locator(".chat-context-popup");
    const graph = popup.locator(".token-cost-graph");
    const svg = graph.locator(".token-cost-graph-svg");
    await expect(svg).toBeVisible({ timeout: 30000 });
    // One toggle per graph, all on: they stack rather than replace each other.
    const switchGroup = popup.getByRole("group", { name: "Graphs", exact: true });
    await expect(switchGroup.locator("label")).toHaveText(["context", "cumulative", "incremental"]);
    const toggles = switchGroup.getByRole("checkbox");
    for (const toggle of await toggles.all()) await expect(toggle).toBeChecked();
    // They share a line with the context size, and are not pills like calls/time.
    const titleLine = (await popup.locator(".usage-popup-title").boundingBox())!;
    const switchLine = (await switchGroup.boundingBox())!;
    expect(
      Math.abs(titleLine.y + titleLine.height / 2 - (switchLine.y + switchLine.height / 2)),
    ).toBeLessThan(6);
    expect(switchLine.x).toBeGreaterThan(titleLine.x + titleLine.width);
    await expect(popup.getByRole("button", { name: "calls" })).toBeVisible();
    await expect(switchGroup.getByRole("button")).toHaveCount(0);
    await expect(svg).toContainText("LLM call");
    await expect(svg).not.toContainText("LLM call number");
    // Each pane is labeled down its side, rotated.
    const labels = svg.locator(".token-cost-pane-title");
    await expect(labels).toHaveCount(3);
    expect(
      await labels.evaluateAll((els) => els.map((el) => el.getAttribute("aria-label"))),
    ).toEqual(["Context tokens", "Cumulative cost", "Incremental cost"]);
    const rotated = await labels
      .first()
      .locator("text")
      .first()
      .evaluate((el) => el.getAttribute("transform"));
    expect(rotated).toContain("rotate(-90)");

    // Context and cumulative cost get equal room; incremental a third of it.
    const height = async (pane: string) =>
      (await svg.locator(`g[data-pane="${pane}"] .token-cost-axis`).first().boundingBox())!.height;
    const [context, cumulative, incremental] = [
      await height("context"),
      await height("cumulative"),
      await height("incremental"),
    ];
    expect(Math.abs(context - cumulative)).toBeLessThan(1);
    expect(Math.abs(cumulative / 3 - incremental)).toBeLessThan(1);

    // The cumulative total is an area; the strip below it has a bar per call.
    const cumulativeFills = await svg
      .locator('g[data-pane="cumulative"] path[fill]')
      .evaluateAll((els) => els.map((el) => el.getAttribute("d") || ""));
    expect(cumulativeFills.some((d) => /^M[\d.]+,[\d.]+L/.test(d))).toBe(true);
    const incrementalFills = await svg
      .locator('g[data-pane="incremental"] path[fill]')
      .evaluateAll((els) => els.map((el) => el.getAttribute("d") || ""));
    expect(incrementalFills.some((d) => /^M[\d.]+,[\d.]+V[\d.]+H[\d.]+V[\d.]+Z/.test(d))).toBe(
      true,
    );

    // One readout follows the pointer, whichever pane it is over. The popup is
    // anchored above the status bar, so anything that changes its height (a
    // pane switched off, the sub-agent lookup landing) moves the graph: point
    // at it afresh on every attempt rather than at where it was measured.
    const readout = graph.locator(".token-cost-hover-readout");
    const hoverGraph = async (y: (height: number) => number, text: RegExp) => {
      await expect(async () => {
        const { width, height } = (await svg.boundingBox())!;
        await svg.hover({ position: { x: width / 2, y: y(height) } });
        await expect(readout).toHaveText(text, { timeout: 1000 });
      }).toPass({ timeout: 15000 });
    };
    await hoverGraph(
      (height) => height / 2,
      /^call 1, \$[\d.]+, cumulative \$[\d.]+, context [\d.]+k?( \(.+\))?$/,
    );

    // Switching a graph off removes its pane and its part of the readout.
    await popup.getByRole("checkbox", { name: "Context length" }).click();
    await expect(svg.locator("g[data-pane]")).toHaveCount(2);
    await expect(svg.locator('g[data-pane="context"]')).toHaveCount(0);
    await expect(graph.locator(".context-legend")).toHaveCount(0);
    await popup.getByRole("checkbox", { name: "Incremental cost" }).click();
    await expect(svg.locator("g[data-pane]")).toHaveCount(1);
    await hoverGraph(() => 40, /^call 1, cumulative \$[\d.]+( \(.+\))?$/);

    // With the cost graph off and context back on, only context is left.
    await popup.getByRole("checkbox", { name: "Context length" }).click();
    await popup.getByRole("checkbox", { name: "Cumulative cost" }).click();
    await expect(svg.locator("g[data-pane]")).toHaveCount(1);
    await expect(svg.locator('g[data-pane="context"]')).toHaveCount(1);
    await expect(graph.locator(".context-legend")).toBeVisible();

    // Nothing on: no graph, but the checkboxes remain to bring one back.
    await popup.getByRole("checkbox", { name: "Context length" }).click();
    await expect(svg).toHaveCount(0);
    await expect(toggles).toHaveCount(3);
  });

  test("remembers which graphs are on across page loads", async ({ page, request }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo remember graphs");
    await page.goto(`/c/${slug}`);
    await page.locator(".context-usage-label").click();
    const popup = page.locator(".chat-context-popup");
    const svg = popup.locator(".token-cost-graph-svg");
    await expect(svg).toBeVisible({ timeout: 30000 });
    await expect(svg.locator("g[data-pane]")).toHaveCount(3);

    await popup.getByRole("checkbox", { name: "Cumulative cost" }).click();
    await expect(svg.locator("g[data-pane]")).toHaveCount(2);

    await page.reload();
    await page.locator(".context-usage-label").click();
    await expect(svg).toBeVisible({ timeout: 30000 });
    await expect(svg.locator("g[data-pane]")).toHaveCount(2);
    await expect(svg.locator('g[data-pane="cumulative"]')).toHaveCount(0);
    await expect(popup.getByRole("checkbox", { name: "Context length" })).toBeChecked();
    await expect(popup.getByRole("checkbox", { name: "Cumulative cost" })).not.toBeChecked();
    await expect(popup.getByRole("checkbox", { name: "Incremental cost" })).toBeChecked();

    // Switching everything off is remembered too, rather than reverting to all.
    await popup.getByRole("checkbox", { name: "Context length" }).click();
    await popup.getByRole("checkbox", { name: "Incremental cost" }).click();
    await expect(svg).toHaveCount(0);
    await page.reload();
    await page.locator(".context-usage-label").click();
    await expect(popup.getByRole("checkbox", { name: "Context length" })).not.toBeChecked({
      timeout: 30000,
    });
    await expect(svg).toHaveCount(0);
  });

  // The token count is the only way into this popup, and it is styled to read as
  // part of the "~/dir · 115k · Model" line — no box, no chevron. Three things
  // therefore have to say it is a control: a dotted underline (always), a
  // pointer cursor, and a tooltip naming the destination (on hover). It used to
  // be announced by a warning triangle beside it, which said "something is
  // wrong" rather than "click me"; that is gone, and nothing may sit between
  // the readout's separators but the count itself.
  test("advertises itself as clickable, and is the segment's only content", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo usage affordance", {
      cwd: testWorkingDirectory(),
    });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const tokens = page.locator(".context-usage-label-tokens:visible").first();
    await expect(tokens).toBeVisible({ timeout: 30000 });
    // No icon, no badge: the segment's whole rendered text is the count. This
    // is what the triangle used to violate, and it catches any successor to it
    // — not just the one class that has been deleted.
    //
    // Read both texts in ONE evaluation. The count starts at 0 and is filled in
    // when usage arrives, so two sequential innerText() round-trips can straddle
    // that update and compare the segment's "0" against the count's "6k".
    const segment = page.locator(".context-usage-root:visible").first();
    await expect(async () => {
      const [segmentText, tokensText] = await segment.evaluate((root) => [
        (root as HTMLElement).innerText.trim(),
        (root.querySelector(".context-usage-label-tokens") as HTMLElement | null)?.innerText.trim(),
      ]);
      expect(segmentText).toBe(tokensText);
    }).toPass({ timeout: 10000 });

    const decoration = await tokens.evaluate((el) => {
      const s = getComputedStyle(el);
      return { line: s.textDecorationLine, style: s.textDecorationStyle, cursor: s.cursor };
    });
    expect(decoration.line).toBe("underline");
    expect(decoration.style).toBe("dotted");
    // The pointer cursor lives on the enclosing button and must reach the text.
    expect(decoration.cursor).toBe("pointer");

    // Hovering says what a click does. Park the pointer away from the segment
    // first: hovering where it already sits fires no mouseover.
    await page.mouse.move(0, 0);
    await tokens.hover();
    await expect(page.locator(".p-tooltip-text")).toContainText("Click for details");
  });

  // The escalation ramp itself. Reaching 100k+ tokens for real would mean
  // pushing ~400KB of message through the predictable model, so drive the
  // classes directly against the shipped stylesheet: what's under test is the
  // CSS (three defined steps, in both themes, each louder than the plain
  // count), not the threshold arithmetic — utils/contextUsage.test.ts owns that.
  test("escalates the token count legibly in both themes", async ({ page, request }) => {
    test.setTimeout(60000);
    const slug = await createConversationViaAPI(request, "echo usage ramp", {
      cwd: testWorkingDirectory(),
    });
    await page.goto(`/c/${slug}`);
    const tokens = page.locator(".context-usage-label-tokens:visible").first();
    await expect(tokens).toBeVisible({ timeout: 30000 });

    // sRGB relative luminance -> WCAG contrast against the status bar behind it.
    const sample = (dark: boolean) =>
      tokens.evaluate((el, isDark) => {
        // Theme changes and the count's own color are animated; a computed
        // style read mid-transition reports the color being left behind.
        // Kill every transition on the page first, then flip the theme.
        const freeze = document.createElement("style");
        freeze.textContent = "* { transition: none !important; }";
        document.head.appendChild(freeze);
        document.documentElement.classList.toggle("dark", isDark);
        const parse = (c: string) =>
          c
            .match(/[\d.]+/g)!
            .slice(0, 3)
            .map(Number);
        const lum = (rgb: number[]) => {
          const [r, g, b] = rgb.map((v) => {
            const s = v / 255;
            return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
          });
          return 0.2126 * r + 0.7152 * g + 0.0722 * b;
        };
        // The bar's own background, wherever it is painted: the readout sits
        // in the status bar on desktop and in the composer's controls row on
        // mobile, and either can be transparent down to an ancestor.
        const backdrop = (start: Element) => {
          for (let n: Element | null = start; n; n = n.parentElement) {
            const c = getComputedStyle(n).backgroundColor;
            const parts = parse(c);
            const alpha = c.match(/[\d.]+/g)!.length > 3 ? Number(c.match(/[\d.]+/g)![3]) : 1;
            if (alpha > 0) return lum(parts);
          }
          return 1;
        };
        const bg = backdrop(el);
        const read = (cls: string) => {
          el.className = cls ? `context-usage-label-tokens ${cls}` : "context-usage-label-tokens";
          const s = getComputedStyle(el);
          const fg = lum(parse(s.color));
          const [hi, lo] = fg > bg ? [fg, bg] : [bg, fg];
          return { color: s.color, weight: s.fontWeight, contrast: (hi + 0.05) / (lo + 0.05) };
        };
        try {
          return {
            plain: read(""),
            warn: read("context-usage-label-tokens-warn"),
            high: read("context-usage-label-tokens-high"),
            critical: read("context-usage-label-tokens-critical"),
          };
        } finally {
          freeze.remove();
        }
      }, dark);

    for (const dark of [false, true]) {
      const r = await sample(dark);
      const where = dark ? "dark" : "light";
      // Three distinct steps, none of them the plain color: a var() missing
      // from one theme's block would silently collapse a step into another.
      const colors = new Set([r.plain.color, r.warn.color, r.high.color, r.critical.color]);
      expect(colors.size, `${where}: steps must be distinct (${[...colors].join(", ")})`).toBe(4);
      // Escalating must never mean fading: each step reads at least as strongly
      // as the plain count, and no step is quieter than the one below it.
      expect(r.warn.contrast, `${where}: warn vs plain`).toBeGreaterThanOrEqual(r.plain.contrast);
      expect(r.high.contrast, `${where}: high vs warn`).toBeGreaterThanOrEqual(r.warn.contrast);
      expect(r.critical.contrast, `${where}: critical vs high`).toBeGreaterThanOrEqual(
        r.high.contrast,
      );
      // The loudest state is not carried by hue alone.
      expect(Number(r.critical.weight), `${where}: critical is bold`).toBeGreaterThan(
        Number(r.plain.weight),
      );
    }
  });

  // The status readout is "<cwd> · <tokens> · <model>" on one line, and it has
  // to survive a narrow viewport by ellipsizing the model name — not by
  // overflowing the bar or clipping the token count. This needs min-width: 0 on
  // every element between the flex container and the model name; a single
  // default `min-width: auto` anywhere in that chain floors the whole subtree at
  // its content width and silently disables the ellipsis, which looks like a
  // truncated model name with no "…".
  test("ellipsizes the model name instead of overflowing when narrow", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 360, height: 760 });
    const slug = await createConversationViaAPI(request, "echo narrow readout", {
      cwd: testWorkingDirectory(),
    });
    await page.goto(`/c/${slug}`);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30000 });

    // Squeeze the bar the way the tightest real layout does: while the agent
    // works, the working indicator and Stop button share the row.
    await input.fill("bash: sleep 5");
    await page.getByTestId("send-button").click();
    await expect(page.getByTestId("agent-thinking")).toBeVisible({ timeout: 20000 });
    await expect(page.locator(".context-usage-label")).toBeVisible();

    // Measures the visible instance: ChatStatusContent is in the DOM twice (the
    // standalone bar and the mobile controls row) and only one is displayed.
    const overflowOf = async (sel: string) => {
      const el = page.locator(`${sel}:visible`).first();
      await expect(el, `${sel} should be present and visible`).toBeVisible();
      return el.evaluate((e) => e.scrollWidth > e.clientWidth + 1);
    };

    // Nothing in the chain may overflow its box...
    expect(await overflowOf(".status-bar-active"), ".status-bar-active overflows").toBe(false);
    expect(await overflowOf(".status-readout"), ".status-readout overflows").toBe(false);
    // ...and the token count in particular is never clipped: a truncated "1"
    // reads as a different number.
    expect(await overflowOf(".context-usage-label-tokens"), "token count clipped").toBe(false);

    // The model name is the part that gives. The invariant that actually broke
    // is the min-width: 0 chain from the readout down to it — assert that
    // directly, since it holds regardless of how long the fixture model's name
    // happens to be.
    // Every ANCESTOR from the readout down to the name's own box must allow
    // shrinking. The name element itself is exempt: it is the thing being
    // clipped, and its min-content floor is what the ellipsis replaces.
    const chain = await page.evaluate(() => {
      const el = [...document.querySelectorAll(".model-picker-value-name")].find(
        (e) => (e as HTMLElement).offsetParent,
      ) as HTMLElement | undefined;
      if (!el) return null;
      const out: { cls: string; minWidth: string; isSelectLabel: boolean }[] = [];
      for (let n = el.parentElement; n; n = n.parentElement) {
        out.push({
          cls: n.className || n.tagName,
          minWidth: getComputedStyle(n).minWidth,
          isSelectLabel: n.classList.contains("p-select-label"),
        });
        if (n.classList.contains("status-readout")) break;
      }
      return out;
    });
    expect(chain, "model name span not found").not.toBeNull();
    expect(chain!.map((n) => n.cls)).toContain("status-readout");
    for (const n of chain!) {
      // PrimeVue's own .p-select-label is a block, not a flex item, so its
      // min-width: auto is inert — it can't floor anything. Only flex items
      // matter here, and every one of ours sets 0 explicitly.
      if (n.isSelectLabel) continue;
      expect(n.minWidth, `${n.cls} must not floor the shrink chain`).toBe("0px");
    }

    // And the ellipsis is really applied, rather than the name being wrapped,
    // hidden, or cut off flush.
    const model = page.locator(".model-picker-value-name:visible").first();
    const m = await model.evaluate((el) => ({
      over: el.scrollWidth > el.clientWidth + 1,
      ellipsis: getComputedStyle(el).textOverflow,
      full: (el.textContent ?? "").trim(),
    }));
    expect(m.full.length, "measured an empty model name span").toBeGreaterThan(0);
    expect(m.ellipsis).toBe("ellipsis");
    // This viewport is narrow enough that the fixture model's name cannot fit.
    // If a future fixture model has a very short name this is the assertion to
    // revisit (narrow the viewport further) — the ones above are the invariant.
    expect(m.over, `"${m.full}" fits at 360px, so this no longer proves anything`).toBe(true);
  });

  // ContextUsageBar isn't remounted on a conversation switch, but the usage
  // walk feeding the graph is lazy and its gate resets per conversation. An
  // open popup therefore has to re-ask, or it shows "No usage data yet."
  // forever for the conversation navigated to.
  test("keeps the graph populated when the conversation changes while open", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    const first = await createConversationViaAPI(request, "echo context usage one");
    const second = await createConversationViaAPI(request, "echo context usage two");
    await page.goto(`/c/${first}`);
    await page.waitForLoadState("domcontentloaded");

    const label = page.locator(".context-usage-label");
    await expect(label).toBeVisible({ timeout: 30000 });
    await label.click();
    const popup = page.locator(".chat-context-popup");
    await expect(popup.locator(".token-cost-graph-svg")).toBeVisible();

    // Client-side navigation (the pushState + popstate pattern App listens
    // for), so the component tree — and the open popover — survives.
    await page.evaluate((slug) => {
      history.pushState({}, "", `/c/${slug}`);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, second);
    await expect(
      page.locator('[data-testid="message"]').filter({ hasText: "two" }).first(),
    ).toBeVisible();
    await expect(popup.locator(".token-cost-graph-svg")).toBeVisible();
    await expect(popup).not.toContainText("No usage data yet.");
  });
});

// The status readout's three controls have distinct destinations: the cwd opens
// the directory picker, the token count opens the cost/compaction popup, the
// model name opens the model picker. They are adjacent, visually identical, and
// each other's most likely misclick, so pin that they don't lead to the same
// place.
test.describe("Status readout controls", () => {
  // All three segments have to look clickable in the same way — they are one
  // line of text with dot separators, so a control that skipped the underline
  // would read as inert prose next to two that don't.
  test("every segment carries the same dotted-underline affordance", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1280, height: 800 });
    const slug = await createConversationViaAPI(request, "echo readout affordances", {
      cwd: testWorkingDirectory(),
    });
    await page.goto(`/c/${slug}`);
    await expect(page.locator(".context-usage-label")).toBeVisible({ timeout: 30000 });

    // Every segment of the readout that is a control, keyed by the element
    // carrying its text. The model name is inside PrimeVue's Select label.
    for (const selector of [
      ".status-readout-cwd-path",
      ".context-usage-label-tokens",
      ".model-picker-inline .model-picker-value-name",
    ]) {
      const el = page.locator(`${selector}:visible`).first();
      await expect(el, `${selector} should be visible`).toBeVisible();
      const style = await el.evaluate((node) => {
        const s = getComputedStyle(node);
        return { line: s.textDecorationLine, style: s.textDecorationStyle, cursor: s.cursor };
      });
      expect(style.line, `${selector} underline`).toBe("underline");
      expect(style.style, `${selector} underline style`).toBe("dotted");
      expect(style.cursor, `${selector} cursor`).toBe("pointer");
    }
  });

  // Changing the directory of a conversation that already exists is the one
  // readout control with consequences beyond the UI: the agent's tools have to
  // move with it. This drives the whole path — readout button, picker modal,
  // server, broadcast — and then checks the two things that would be silently
  // wrong if only the database moved: what the readout says, and what the agent
  // was told.
  test("cwd segment moves the conversation, and tells the agent", async ({ page, request }) => {
    test.setTimeout(90000);
    await page.setViewportSize({ width: 1280, height: 800 });
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo cwd control",
      { cwd: testWorkingDirectory() },
    );
    await page.goto(`/c/${slug}`);

    const cwdSegment = page.locator(".status-readout-cwd:visible").first();
    await expect(cwdSegment).toBeVisible({ timeout: 30000 });
    await expect(cwdSegment).toHaveText(testWorkingDirectory());

    // The readout opens the same picker the composer's cwd chip does.
    await cwdSegment.click();
    const panel = page.locator(".modal.directory-picker-modal");
    await expect(panel).toBeVisible();

    // Somewhere that certainly exists and isn't where we started. The e2e
    // directory is small, so listing it stays fast.
    await panel.locator(".directory-picker-input").fill(e2eDir + "/");
    await expect(panel.locator(".directory-picker-current-path")).toContainText("e2e");
    await panel.locator(".modal-footer").getByRole("button", { name: "Select" }).click();
    await expect(panel).toBeHidden();

    // The readout follows the server's broadcast, not a local write. It shows
    // the path tildified, as every cwd in the UI is.
    const homeDir = await page.evaluate(() => window.__SHELLEY_INIT__?.home_dir || "");
    const expectedLabel = e2eDir.startsWith(homeDir + "/")
      ? "~" + e2eDir.slice(homeDir.length)
      : e2eDir;
    await expect(cwdSegment).toHaveText(expectedLabel, { timeout: 15000 });

    // And the conversation really moved, for the next turn's tools...
    await expect(async () => {
      const resp = await request.get(`/api/conversation/${conversationId}`);
      expect(resp.ok()).toBeTruthy();
      const body = await resp.json();
      expect(body.conversation?.cwd).toBe(e2eDir);
    }).toPass({ timeout: 15000 });

    // ...and the agent was told, in a message it will actually read. Without
    // this it keeps resolving relative paths against the old directory. It is a
    // user-role row (so the agent sees it) but renders as a status line, not a
    // chat bubble — the user didn't type it.
    const notice = page.locator('[data-testid="message-cwdchange"]').last();
    await expect(notice).toBeVisible();
    await expect(notice).toContainText("working directory");
    await expect(notice).toContainText(expectedLabel);
    // Not rendered as something the user said.
    await expect(
      page.locator(`[data-testid="message"].message-user:has-text("${e2eDir}")`),
    ).toHaveCount(0);
  });

  test("token count opens the cost popup, model name opens the picker", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1280, height: 800 });
    const slug = await createConversationViaAPI(request, "echo readout controls");
    await page.goto(`/c/${slug}`);

    const tokens = page.locator(".context-usage-label");
    const model = page.locator(".model-picker-inline .p-select-label");
    const costPopup = page.locator(".chat-context-popup");
    const pickerPanel = page.locator(".model-picker-panel");
    await expect(tokens).toBeVisible({ timeout: 30000 });
    await expect(model).toBeVisible();

    // Token count -> cost popup, and NOT the picker.
    await tokens.click();
    await expect(costPopup).toBeVisible();
    await expect(costPopup).toContainText("LLM call");
    await expect(pickerPanel).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(costPopup).toBeHidden();

    // Model name -> picker, and NOT the cost popup. The picker carries the
    // model list, the current selection, and the reasoning pills.
    await model.click();
    await expect(pickerPanel).toBeVisible();
    await expect(pickerPanel.locator(".p-select-option").first()).toBeVisible();
    await expect(pickerPanel.locator(".model-picker-effort-pills")).toBeVisible();
    await expect(costPopup).toBeHidden();
    await page.keyboard.press("Escape");
    await expect(pickerPanel).toBeHidden();
  });

  // The inline picker's overlay is portaled to <body> precisely so it can be
  // clamped to the viewport: anchored to the trigger (append-to="self") it is
  // positioned by relativePosition(), which aligns left edges and never clamps,
  // and the trigger sits at the right edge of the bar — so on a narrow screen the
  // panel ran off the left (measured x = -93 at 360px). Pin that it stays on
  // screen, on the viewport where there is least room.
  test("picker overlay stays on screen on a narrow viewport", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 360, height: 760 });
    const slug = await createConversationViaAPI(request, "echo narrow picker", {
      cwd: testWorkingDirectory(),
    });
    await page.goto(`/c/${slug}`);
    const trigger = page.locator(".model-picker-inline .p-select-label");
    await expect(trigger).toBeVisible({ timeout: 30000 });
    await trigger.click();

    const panel = page.locator(".model-picker-panel");
    await expect(panel).toBeVisible();
    await expect(panel.locator(".p-select-option").first()).toBeVisible();
    const box = await panel.boundingBox();
    const vp = page.viewportSize()!;
    expect(box, "overlay has no box").not.toBeNull();
    expect(box!.x, "overlay runs off the left edge").toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width, "overlay runs off the right edge").toBeLessThanOrEqual(vp.width);
    expect(box!.y, "overlay runs off the top").toBeGreaterThanOrEqual(0);
    expect(box!.y + box!.height, "overlay runs off the bottom").toBeLessThanOrEqual(vp.height);
    // The search field is the reason the overlay is worth opening on mobile.
    await expect(panel.locator("input")).toBeVisible();
  });

  // The picker's reasoning pills have the same problem as its model list: for a
  // conversation that already exists, conversation_options are locked server-side
  // (the send path stops resending them once promoted), so a purely local change
  // is a silent no-op that survives until the page is reloaded and then vanishes.
  // Both have to go through the server (POST /settings).
  test("reasoning pill persists server-side", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1280, height: 800 });
    const slug = await createConversationViaAPI(request, "echo reasoning pill");
    await page.goto(`/c/${slug}`);
    const trigger = page.locator(".model-picker-inline .p-select-label");
    await expect(trigger).toBeVisible({ timeout: 30000 });
    await trigger.click();

    const pills = page.locator(".model-picker-effort-pills [role=radio]");
    await expect(pills.first()).toBeVisible();
    // Any pill that isn't already selected, and isn't the "auto" sentinel
    // (that one means "defer to the model", which has no /model spelling).
    const pill = pills.filter({ hasNotText: "auto" }).and(page.locator('[aria-checked="false"]'));
    const chosen = (await pill.first().textContent())?.trim();
    expect(chosen, "no unselected reasoning pill to click").toBeTruthy();
    await pill.first().click();

    // The switch is recorded in the log like a model switch is...
    await expect(page.locator('[data-testid="message-modelchange"]').last()).toContainText(chosen!);
    // ...and lands in the conversation's persisted options, not just localStorage.
    await expect(async () => {
      const list = await (await request.get("/api/conversations")).json();
      const conv = (list.conversations || list).find((c: { slug?: string }) => c.slug === slug);
      expect(conv?.conversation_options ?? "").toContain(`"thinking_level":"${chosen}"`);
    }).toPass({ timeout: 10000 });

    // And the pill itself follows, which it can only do via the server echo:
    // nothing is applied locally, so without the conversation-options watch the
    // user's own click would appear to do nothing. Reopen only if the pill click
    // dismissed the panel.
    if (!(await pills.first().isVisible())) await trigger.click();
    await expect(pills.first()).toBeVisible();
    const chosenPill = page.getByRole("radio", { name: chosen!, exact: true });
    await expect(chosenPill, "the chosen reasoning pill should end up selected").toHaveAttribute(
      "aria-checked",
      "true",
    );

    // Re-clicking the pill that is already selected must not change settings
    // again: the pills are radios and re-emit on such a click, and each command
    // rebuilds the agent loop and appends a marker to the log.
    const markersBefore = await page.locator('[data-testid="message-modelchange"]').count();
    const sent: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/settings")) sent.push(r.postData() || "");
    });
    await chosenPill.click();
    await page.waitForTimeout(1000);
    expect(sent, "re-selecting the current level should send nothing").toEqual([]);
    expect(await page.locator('[data-testid="message-modelchange"]').count()).toBe(markersBefore);
  });

  // The same guard when the picker is already open as a turn starts (a
  // background job exiting starts one unprompted): the status bar keeps the
  // picker mounted across the turn, so it has to close itself.
  test("an open model picker closes when a turn starts", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1280, height: 800 });
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo open picker",
      { cwd: testWorkingDirectory() },
    );
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    await page.locator(".model-picker-inline .p-select-label").click();
    const panel = page.locator(".model-picker-panel");
    await expect(panel).toBeVisible();
    const sent: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/settings")) sent.push(r.postData() || "");
    });
    expect(
      (
        await request.post(`/api/conversation/${conversationId}/chat`, {
          data: { message: "bash: sleep 8", model: "predictable" },
        })
      ).ok(),
    ).toBe(true);
    await expect(page.getByTestId("agent-thinking")).toBeVisible({ timeout: 20000 });
    await expect(panel).toHaveCount(0);
    expect(sent).toEqual([]);
    await request.post(`/api/conversation/${conversationId}/cancel`);
  });

  // Switching model rebuilds the conversation's loop, and ApplyModelSettings
  // cancels a running turn to do it. Killing the turn the user is watching
  // because they wanted to read the model name is not acceptable, so the
  // control is disabled (and says why) while the agent works.
  test("model picker is disabled while the agent works", async ({ page, request }) => {
    test.setTimeout(60000);
    await page.setViewportSize({ width: 1280, height: 800 });
    const slug = await createConversationViaAPI(request, "echo busy picker", {
      cwd: testWorkingDirectory(),
    });
    await page.goto(`/c/${slug}`);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30000 });

    const model = page.locator(".model-picker-inline .p-select-label");
    await expect(model).toBeVisible();
    await expect(page.locator(".model-picker-inline")).not.toHaveClass(/p-disabled/);

    await input.fill("bash: sleep 8");
    await page.getByTestId("send-button").click();
    await expect(page.getByTestId("agent-thinking")).toBeVisible({ timeout: 20000 });

    await expect(page.locator(".model-picker-inline")).toHaveClass(/p-disabled/);
    // Clicking it must not open the picker.
    await model.click({ force: true });
    await expect(page.locator(".model-picker-panel")).toHaveCount(0);
    // It must not look clickable either, while staying legible as a readout.
    const look = await page.locator(".model-picker-inline").evaluate((el) => ({
      opacity: getComputedStyle(el).opacity,
      cursor: getComputedStyle(el.querySelector(".p-select-label")!).cursor,
    }));
    expect(look.cursor).toBe("default");
    expect(Number(look.opacity)).toBeLessThan(1);
    expect(Number(look.opacity)).toBeGreaterThan(0.4);
    // And it explains itself twice over, because the two audiences reach it
    // differently: a screen reader lands on the combobox, so the reason has to
    // be an ARIA attribute there...
    await expect(model).toHaveAttribute("aria-description", /switch models/);
    // ...while a pointer needs a tooltip, which can only hang off the wrapper (a
    // disabled PrimeVue control has pointer-events: none). Park the pointer
    // elsewhere first: the click above left it on the segment, and hovering where
    // the pointer already sits fires no mouseover. Somewhere inert, not a
    // neighbouring segment — every segment of the readout has its own tooltip
    // now, so hovering one would just swap this bubble for another.
    await page.mouse.move(0, 0);
    await page.locator(".status-readout-model").hover();
    await expect(page.locator(".p-tooltip-text")).toContainText("switch models");
    // Move off it so the tooltip doesn't sit over the token count.
    await page.mouse.move(0, 0);
    await expect(page.locator(".p-tooltip-text")).toHaveCount(0);
    // The token count stays usable — reading costs never cancels anything.
    await page.locator(".context-usage-label").click();
    await expect(page.locator(".chat-context-popup")).toBeVisible();

    // The cwd segment is disabled for the same reason, and the server agrees:
    // moving the directory mid-turn would change the ground under a running
    // bash command, so POST /cwd answers 409. The button must not offer what
    // the server would refuse.
    const cwd = page.locator(".status-readout-cwd:visible").first();
    await expect(cwd).toBeDisabled();
    const underline = await cwd
      .locator(".status-readout-cwd-path")
      .evaluate((el) => getComputedStyle(el).textDecorationLine);
    expect(underline, "a disabled segment must not look clickable").toBe("none");
  });
});
