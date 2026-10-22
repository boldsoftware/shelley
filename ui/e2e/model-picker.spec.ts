import { test, expect } from "@playwright/test";
import { openModelPicker, testWorkingDirectory } from "./helpers";

// The unified model + effort picker (ChatStatusContent -> ModelPicker.vue) is
// built on PrimeVue <Select>. It renders on the new-conversation screen. Here
// we exercise the PrimeVue-specific open/select behavior, the inline
// reasoning-effort pill row, the Model heading's "Manage…" action, and
// persistence of the chosen model + effort to localStorage.
test.describe("Model picker (PrimeVue)", () => {
  test("opens, lists models, selecting one persists, Manage… opens manage modal", async ({
    page,
  }) => {
    test.setTimeout(60000);

    await page.goto("/new");
    await page.waitForLoadState("domcontentloaded");

    const picker = await openModelPicker(page);
    const panel = page.locator(".model-picker-panel");
    await expect(panel).toBeVisible();

    // At least one model is offered and the heading's actions are present.
    const options = panel.locator(".p-select-option");
    expect(await options.count()).toBeGreaterThanOrEqual(1);
    const manageBtn = panel.getByRole("button", { name: "Manage Models" });
    await expect(manageBtn).toBeVisible();
    await expect(panel.getByRole("button", { name: "Refresh" })).toBeVisible();

    // On a phone it is a sheet across the bottom of the screen.
    const viewport = page.viewportSize()!;
    if (viewport.width < 768) {
      const box = (await panel.boundingBox())!;
      expect(box.x).toBe(0);
      expect(box.width).toBe(viewport.width);
      expect(Math.round(box.y + box.height)).toBe(viewport.height);
    }

    // In a single-source install, no source sub-labels are rendered.
    await expect(panel.locator(".model-picker-option-source")).toHaveCount(0);

    // Pick the first model -> its label shows in the trigger and the raw model
    // id (not the pretty label) persists to localStorage. The panel stays
    // open, as for every choice in it.
    const firstName = (await options
      .first()
      .locator(".model-picker-option-name")
      .textContent())!.trim();
    await options.first().click();
    await expect(picker).toContainText(firstName);
    expect(await page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe(
      "predictable",
    );
    await expect(panel).toBeVisible();
    // Close hands focus back to the trigger.
    await panel.getByRole("button", { name: "Close" }).click();
    await expect(panel).toBeHidden();
    await expect(picker.locator(".p-select-label")).toBeFocused();

    // Manage… opens the manage-models modal, closing the panel.
    await openModelPicker(page);
    await expect(panel).toBeVisible();
    await panel.getByRole("button", { name: "Manage Models" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(panel).toBeHidden();
  });

  test("mobile new conversations keep model and directory in the menu", async ({ page }) => {
    for (const width of [412, 260]) {
      await page.setViewportSize({ width, height: 915 });
      await page.goto("/new");
      await expect(page.locator(".status-field-model")).toBeHidden();
      await expect(page.locator(".status-field-cwd")).toBeHidden();
      await expect(page.locator(".status-bar-new")).toBeHidden();
      const picker = await openModelPicker(page);
      await expect(picker).toHaveClass(/model-picker-menu/);
      await expect(page.locator(".model-picker-panel")).toBeVisible();
    }
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/new");
    await expect(page.locator(".status-field-model")).toBeVisible();
    await expect(page.locator(".status-field-cwd")).toBeVisible();
  });

  test("does not focus model search when opened on mobile", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/new");

    await openModelPicker(page);
    const searchbox = page.locator(".model-picker-panel").getByRole("searchbox");
    await expect(searchbox).toBeVisible();
    await expect(searchbox).not.toBeFocused();
  });

  test("keyboard focus follows a pick made with the pointer", async ({ page }) => {
    // A second model, so there is a row to pick other than the selected one.
    await page.route("**/api/models", async (route) => {
      const models = await (await route.fetch()).json();
      const twin = { ...models[0], id: "predictable-twin", display_name: "Predictable Twin" };
      await route.fulfill({ json: [...models, twin] });
    });
    // Desktop, where the search has focus from the start.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/new");
    const picker = page.locator(".model-picker.p-select");
    await openModelPicker(page);
    const panel = page.locator(".model-picker-panel");
    const twin = panel.locator(".p-select-option", { hasText: "Predictable Twin" });
    await expect(twin).toBeVisible();
    await expect(panel.getByRole("searchbox")).toBeFocused();

    // Keyboard on the selected row, then the pointer on the other: Enter
    // keeps the pointer's pick.
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("ArrowUp");
    await expect(panel.locator(".p-select-option.p-focus")).not.toContainText("Twin");
    await twin.click();
    await expect(twin).toHaveClass(/p-focus/);
    await page.keyboard.press("Enter");
    await expect(panel).toBeHidden();
    await expect(picker.locator(".model-picker-value-name")).toHaveText("Predictable Twin");
  });

  test("effort pills select a level, persist it, and keep the popover open", async ({ page }) => {
    test.setTimeout(60000);

    await page.goto("/new");
    await page.waitForLoadState("domcontentloaded");

    // Reset persisted level so assertions are deterministic across workers.
    await page.evaluate(() => localStorage.removeItem("shelley.thinkingLevel.v2"));
    await page.reload();
    await page.waitForLoadState("domcontentloaded");

    await openModelPicker(page);
    const panel = page.locator(".model-picker-panel");
    await expect(panel).toBeVisible();

    // Models without explicit capability metadata use the standard levels
    // through xhigh; rare max support must be advertised by the model.
    const pills = panel.locator(".model-picker-effort-pill");
    expect(await pills.count()).toBeGreaterThanOrEqual(6);
    await expect(pills.filter({ hasText: /^max$/ })).toHaveCount(0);

    // Pick "high" -> persists and the popover stays open.
    await pills.filter({ hasText: /^high$/ }).click();
    await expect(panel).toBeVisible();
    expect(await page.evaluate(() => localStorage.getItem("shelley.thinkingLevel.v2"))).toBe(
      "high",
    );
    await expect(pills.filter({ hasText: /^high$/ })).toHaveAttribute("aria-checked", "true");

    // The effort survives a reload.
    await page.keyboard.press("Escape");
    await expect(panel).toBeHidden();
    await page.reload();
    await openModelPicker(page);
    await expect(
      page.locator(".model-picker-panel").getByRole("radio", { name: "high", exact: true }),
    ).toHaveAttribute("aria-checked", "true");
  });

  test("shows recent combinations, hidden while searching", async ({ page, request }) => {
    test.setTimeout(60000);

    const response = await request.post("/api/conversations/new", {
      data: {
        message: "echo recent model picker",
        model: "predictable",
        cwd: testWorkingDirectory(),
        conversation_options: { thinking_level: "high" },
      },
    });
    expect(response.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await response.json();

    // The suite shares a conversation DB across workers. Other tests can
    // contribute a more popular recent model/effort combination, so keep
    // this page's picker history scoped to the conversation we just created.
    // Keep the stream connected for the picker, but ignore list patches
    // that would reintroduce other tests' conversations after the snapshot.
    await page.addInitScript(() => {
      const NativeEventSource = window.EventSource;
      window.EventSource = class extends NativeEventSource {
        constructor(url: string | URL, options?: EventSourceInit) {
          super(url, options);
          this.addEventListener("message", (event) => {
            const data = JSON.parse(event.data) as { conversation_list_patch?: unknown };
            if (data.conversation_list_patch) event.stopImmediatePropagation();
          });
        }
      };
    });
    await page.route("**/api/conversations/snapshot", async (route) => {
      const snapshotResponse = await route.fetch();
      const snapshot = (await snapshotResponse.json()) as {
        conversations: Array<{ conversation_id: string }>;
        hash: string;
      };
      const seed = snapshot.conversations.find(
        (conversation) => conversation.conversation_id === conversationId,
      );
      if (!seed) throw new Error("recent model picker conversation missing from snapshot");
      await route.fulfill({
        response: snapshotResponse,
        json: { ...snapshot, conversations: [seed] },
      });
    });

    // The composer has taken on the default profile already; its picks stand.
    await page.addInitScript(() => {
      localStorage.setItem("shelley.thinkingLevel.v2", "high");
      localStorage.setItem("shelley.profileApplied", "Default");
    });
    await page.goto("/new");
    await openModelPicker(page);

    const panel = page.locator(".model-picker-panel");
    await expect(panel.locator(".model-picker-group-label")).toHaveCount(0);

    const recentRow = panel.locator(".p-select-option:has(.model-picker-option-effort)");
    const modelRow = panel.locator(".p-select-option:not(:has(.model-picker-option-effort))");
    await expect(modelRow.locator(".model-picker-option-name")).toHaveText("predictable");
    await expect(modelRow.locator(".model-picker-option-check")).toBeVisible();

    await panel.locator(".model-picker-effort-pill").filter({ hasText: /^low$/ }).click();

    // Recent combinations are taken as the panel opens.
    await expect(panel.locator(".model-picker-group-label")).toHaveCount(0);
    await page.keyboard.press("Escape");
    await openModelPicker(page);
    await expect(panel.locator(".model-picker-group-label")).toHaveText("Recent");
    await expect(panel.locator(".model-picker-group-divider")).toBeVisible();
    await expect(recentRow.locator(".model-picker-option-name")).toHaveText("predictable");
    await expect(recentRow.locator(".model-picker-option-effort")).toHaveText("high");
    await expect(recentRow.locator(".model-picker-option-check")).toHaveCount(0);
    // A heading, not a highlight, sets them apart; the highlight is the
    // selected model's, where the keyboard starts.
    await expect(recentRow).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
    await expect(modelRow).toHaveClass(/p-focus/);
    const nameBox = await recentRow.locator(".model-picker-option-name").boundingBox();
    const effortPill = recentRow.locator(".model-picker-option-effort");
    const effortBox = await effortPill.boundingBox();
    expect(effortBox!.x).toBeGreaterThan(nameBox!.x + nameBox!.width);
    await expect(effortPill).not.toHaveCSS("background-color", "rgba(0, 0, 0, 0)");

    await recentRow.hover();
    await expect(recentRow).not.toHaveClass(/p-focus/);

    await panel.getByRole("searchbox").fill("predictable");
    await expect(panel.locator(".model-picker-group-label")).toHaveCount(0);
    await expect(panel.locator(".p-select-option-group:visible")).toHaveCount(0);

    await panel.getByRole("searchbox").fill("");
    await expect(panel.locator(".model-picker-group-label")).toBeVisible();

    // Picking one keeps it where it is; the model's check and the reasoning
    // pill show the choice.
    await recentRow.click();
    await expect(recentRow.locator(".model-picker-option-check")).toHaveCount(0);
    await expect(modelRow.locator(".model-picker-option-check")).toBeVisible();
    await expect(panel.getByRole("radio", { name: "high", exact: true })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(panel).toBeVisible();
  });
});
