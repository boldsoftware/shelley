import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
import path, { dirname } from "node:path";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { createConversationViaAPI } from "./helpers";

const e2eDir = dirname(fileURLToPath(import.meta.url));

// The base Modal.vue is backed by PrimeVue Dialog (focus trap,
// role="dialog"/aria-modal, mask + Escape owned by PrimeVue). See
// components/Modal.vue. The DOM/ARIA contract that the other specs rely on
// (.modal-overlay mask, .modal panel, .modal-header .btn-icon close,
// .modal-body) is preserved via the #container slot and the mask passthrough;
// here we exercise the PrimeVue-specific guarantees.
//
// The Feature flags modal is the simplest consumer of the base Modal, and is
// reachable via the command palette (Cmd/Ctrl+K -> "feature").
test.describe("Base modal (PrimeVue Dialog)", () => {
  test("opens via palette, traps focus, and closes via X / Escape / backdrop", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const openFeatureFlags = async () => {
      // Cmd/Ctrl+K opens the command palette; type to filter to "Feature flags".
      await page.keyboard.press("ControlOrMeta+k");
      const search = page.locator(".command-palette-input");
      await expect(search).toBeVisible();
      await search.fill("feature");
      const item = page.locator(".command-palette-item").first();
      await expect(item).toBeVisible();
      await item.click();
    };

    // --- Open + contract / a11y ---
    await openFeatureFlags();

    const mask = page.locator(".modal-overlay");
    const panel = page.locator(".modal");
    await expect(mask).toBeVisible();
    await expect(panel).toBeVisible();
    await expect(page.locator(".modal-title")).toHaveText("Feature flags");

    // PrimeVue Dialog gives us proper dialog semantics on the wrapper around
    // our .modal panel.
    const dialog = page.getByRole("dialog");
    await expect(dialog).toHaveAttribute("aria-modal", "true");

    // The dialog has an accessible name: aria-labelledby must resolve to the
    // real .modal-title (not PrimeVue's default, never-rendered _header id).
    const labelledBy = await dialog.getAttribute("aria-labelledby");
    expect(labelledBy).toBeTruthy();
    await expect(page.locator(`#${labelledBy}`)).toHaveText("Feature flags");
    await expect(page.locator(`#${labelledBy}`)).toHaveClass(/modal-title/);

    // Focus moves into the dialog on open (so the focus trap engages).
    await expect
      .poll(() =>
        page.evaluate(() => document.querySelector(".modal")?.contains(document.activeElement)),
      )
      .toBe(true);

    // The mask must carry the legacy .modal-overlay class (passthrough), and
    // the close button must keep its aria-label contract.
    await expect(page.locator(".modal-header .btn-icon[aria-label='Close modal']")).toBeVisible();

    // --- Close via the X button ---
    await page.locator(".modal-header .btn-icon[aria-label='Close modal']").click();
    await expect(panel).toHaveCount(0);

    // --- Close via Escape (PrimeVue document-level handler) ---
    await openFeatureFlags();
    await expect(panel).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(panel).toHaveCount(0);

    // --- Close via backdrop / dismissable mask click ---
    await openFeatureFlags();
    await expect(panel).toBeVisible();
    // Click the mask at a corner well outside the centered panel.
    await mask.click({ position: { x: 5, y: 5 } });
    await expect(panel).toHaveCount(0);
  });
});

// On desktop the centered panel leaves wide gaps left/right. The transparent
// p-dialog wrapper spans the mask there, and must not swallow backdrop clicks
// (pointer-events: none in styles.css): PrimeVue's dismissable-mask only
// fires when the click target IS the mask element itself. On the default
// mobile viewport the panel is near-full-width, so this can only regress on
// desktop — hence the viewport override.
test.describe("Base modal backdrop click beside the panel (desktop)", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("dismisses when clicking beside the panel at panel mid-height", async ({
    page,
    request,
  }) => {
    test.setTimeout(60000);

    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    await page.keyboard.press("ControlOrMeta+k");
    const search = page.locator(".command-palette-input");
    await expect(search).toBeVisible();
    await search.fill("feature");
    await page.locator(".command-palette-item").first().click();

    const panel = page.locator(".modal");
    await expect(panel).toBeVisible();
    const box = await panel.boundingBox();
    await page.mouse.click(box!.x / 2, box!.y + box!.height / 2);
    await expect(panel).toHaveCount(0);
  });
});

test.describe("Command palette home-directory action", () => {
  test("starts a new conversation in $HOME", async ({ page }) => {
    await page.goto("/new");
    const homeDir = await page.evaluate(() => window.__SHELLEY_INIT__?.home_dir || "");
    expect(homeDir).not.toBe("");

    await page.evaluate(
      (cwd) => localStorage.setItem("shelley_selected_cwd", cwd),
      `${homeDir}/not-home`,
    );
    await page.reload();

    await page.keyboard.press("ControlOrMeta+k");
    const search = page.locator(".command-palette-input");
    await expect(search).toBeVisible();
    await search.fill("home directory");
    await page.getByText("New Conversation in Home Directory", { exact: true }).click();

    await expect(page.locator(".status-field-cwd .status-chip")).toHaveText("~");
    await expect
      .poll(() => page.evaluate(() => localStorage.getItem("shelley_selected_cwd")))
      .toBe(homeDir);
  });
});

// DirectoryPickerModal is the most involved consumer of the shared Modal: it
// uses the #footer slot (New Folder / Cancel / Select) and has an inline
// create form whose local Escape must cancel create mode without closing the
// dialog (it stopPropagation()s before the shared modal Escape stack sees it).
test.describe("Directory picker modal (shared Modal consumer)", () => {
  test("saves favorites, remembers the view, and selects on double-click", async ({
    page,
    request,
  }) => {
    const settings = await (await request.get("/settings")).json();
    const headers = { "X-Shelley-Request": "1" };
    await request.post("/settings", {
      headers,
      data: { key: "directory_favorites", value: "[]" },
    });
    try {
      await page.goto("/new");
      const homeDir = await page.evaluate(() => window.__SHELLEY_INIT__!.home_dir!);
      const serverCwd = await page.evaluate(() => window.__SHELLEY_INIT__!.default_cwd!);
      await page.evaluate((cwd) => localStorage.setItem("shelley_selected_cwd", cwd), e2eDir);
      await page.reload();

      const chip = page.locator(".status-field-cwd .status-chip");
      const panel = page.locator(".modal.directory-picker-modal");
      await chip.click();
      await panel
        .locator(".directory-picker-input")
        .fill(`${path.relative(serverCwd, homeDir) || "."}/`);
      await panel.getByRole("button", { name: "Add ~ to favorites", exact: true }).click();
      await expect(
        panel.getByRole("button", { name: "Remove ~ from favorites", exact: true }),
      ).toBeEnabled();
      const saved = await (await request.get("/settings")).json();
      expect(JSON.parse(saved.directory_favorites)).toEqual([homeDir]);
      await panel.getByRole("button", { name: "Favorites", exact: true }).click();
      await panel.getByRole("button", { name: "Cancel", exact: true }).click();
      await page.reload();
      await chip.click();
      await expect(panel.getByRole("button", { name: "Favorites", exact: true })).toHaveAttribute(
        "aria-pressed",
        "true",
      );

      const list = panel.locator(".directory-picker-list:visible");
      const favorite = list.getByTitle(homeDir, { exact: true });
      await expect(favorite).toContainText(path.basename(homeDir));
      await expect(favorite).toContainText("~");
      await favorite.click();
      await expect(panel.locator(".directory-picker-current-path")).toHaveText(homeDir);
      expect(await page.evaluate(() => localStorage.getItem("shelley_selected_cwd"))).toBe(e2eDir);

      const homeListing = (url: URL) =>
        url.pathname === "/api/list-directory" && url.searchParams.get("path") === homeDir;
      await page.route(homeListing, (route) =>
        route.fulfill({ json: { error: "Favorite directory no longer exists" } }),
      );
      await favorite.dblclick();
      await expect(panel.locator(".directory-picker-error")).toHaveText(
        "Favorite directory no longer exists",
      );
      await expect(panel).toBeVisible();
      expect(await page.evaluate(() => localStorage.getItem("shelley_selected_cwd"))).toBe(e2eDir);

      await panel.getByRole("button", { name: "Cancel", exact: true }).click();
      await page.unroute(homeListing);
      await chip.click();
      await favorite.dblclick();
      await expect(panel).toHaveCount(0);
      await expect(chip).toHaveText("~");
      expect(await page.evaluate(() => localStorage.getItem("shelley_selected_cwd"))).toBe(homeDir);

      await chip.click();
      await list.getByRole("button", { name: "Remove ~ from favorites", exact: true }).click();
      await expect(list).toContainText("No favorites yet");
      const cleared = await (await request.get("/settings")).json();
      expect(JSON.parse(cleared.directory_favorites)).toEqual([]);

      await panel.locator(".directory-picker-input").fill(`${e2eDir}/`);
      await panel.getByRole("button", { name: "Select", exact: true }).click();
      await expect(panel).toHaveCount(0);
      expect(await page.evaluate(() => localStorage.getItem("shelley_selected_cwd"))).toBe(e2eDir);
    } finally {
      await request.post("/settings", {
        headers,
        data: { key: "directory_favorites", value: settings.directory_favorites ?? "[]" },
      });
    }
  });

  test("uses a home-relative path in the new-conversation cwd chip", async ({ page }) => {
    await page.goto("/new");
    const homeDir = await page.evaluate(() => window.__SHELLEY_INIT__?.home_dir || "");
    expect(homeDir).not.toBe("");

    const absoluteCwd = `${homeDir}/exe`;
    await page.evaluate((cwd) => localStorage.setItem("shelley_selected_cwd", cwd), absoluteCwd);
    await page.reload();

    const chip = page.locator(".status-field-cwd .status-chip");
    await expect(chip).toHaveText("~/exe");

    await chip.click();
    const panel = page.locator(".modal.directory-picker-modal");
    await expect(panel.locator(".directory-picker-input")).toHaveValue(`${absoluteCwd}/`);

    const homeButton = panel.getByRole("button", { name: "Go to home directory" });
    await homeButton.click();
    await expect(panel.locator(".directory-picker-input")).toHaveValue(`${homeDir}/`);
    await expect(panel.locator(".directory-picker-current-path")).toHaveText(homeDir);

    const delayedPath = "/definitely-missing-shelley-directory";
    let releaseInvalid!: () => void;
    let markInvalidRequested!: () => void;
    let markInvalidFulfilled!: () => void;
    const invalidRequested = new Promise<void>((resolve) => (markInvalidRequested = resolve));
    const invalidReleased = new Promise<void>((resolve) => (releaseInvalid = resolve));
    const invalidFulfilled = new Promise<void>((resolve) => (markInvalidFulfilled = resolve));
    await page.route("**/api/list-directory?*", async (route) => {
      if (new URL(route.request().url()).searchParams.get("path") !== delayedPath) {
        await route.continue();
        return;
      }
      markInvalidRequested();
      await invalidReleased;
      await route.fulfill({ json: { error: "delayed directory error" } });
      markInvalidFulfilled();
    });

    await panel.locator(".directory-picker-input").fill(`${delayedPath}/`);
    await invalidRequested;
    await homeButton.click();
    await expect(panel.locator(".directory-picker-input")).toHaveValue(`${homeDir}/`);
    await expect(panel.locator(".directory-picker-current-path")).toHaveText(homeDir);

    releaseInvalid();
    await invalidFulfilled;
    await page.evaluate(
      () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())),
    );
    await expect(panel.locator(".directory-picker-error")).toHaveCount(0);
  });

  test("opens from the cwd chip, pins the footer, and handles nested Escape", async ({ page }) => {
    test.setTimeout(60000);

    // The cwd chip only shows on the new-conversation status bar. Use /new
    // explicitly: "/" resumes the most recent conversation on a shared server.
    await page.goto("/new");
    await page.waitForLoadState("domcontentloaded");

    const chip = page.locator(".status-field-cwd .status-chip");
    await expect(chip).toBeVisible();
    await chip.click();

    const panel = page.locator(".modal.directory-picker-modal");
    await expect(panel).toBeVisible();
    await expect(page.locator(".modal-title")).toHaveText("Select Directory");
    await expect(page.getByRole("dialog")).toHaveAttribute("aria-modal", "true");

    // Other tests seed conversations with cwd=/tmp, which can be huge on a
    // busy machine (tens of thousands of entries) and makes every re-render
    // crawl. Point the picker at this small, known directory instead.
    await panel.locator(".directory-picker-input").fill(e2eDir + "/");
    await expect(panel.locator(".directory-picker-current-path")).toContainText("e2e");

    // Footer slot renders the action row inside the shared .modal-footer.
    const footer = panel.locator(".modal-footer");
    await expect(footer.getByRole("button", { name: "Select" })).toBeVisible();
    await expect(footer.getByRole("button", { name: "Cancel" })).toBeVisible();

    // Enter inline-create mode; Escape cancels it but keeps the dialog open.
    await footer.getByRole("button", { name: "New Folder" }).click();
    const createInput = panel.locator(".directory-picker-create-input");
    await expect(createInput).toBeVisible();
    await createInput.press("Escape");
    await expect(createInput).toHaveCount(0);
    await expect(panel).toBeVisible();

    // A second Escape (now routed to the modal Escape stack) closes it.
    await page.keyboard.press("Escape");
    await expect(panel).toHaveCount(0);

    // Reopen and close via the footer Cancel button.
    await chip.click();
    await expect(panel).toBeVisible();
    await footer.getByRole("button", { name: "Cancel" }).click();
    await expect(panel).toHaveCount(0);
  });

  test("caps rendering of huge directories and filters within them", async ({ page }) => {
    test.setTimeout(60000);

    // A directory with more subdirs than MAX_VISIBLE_ENTRIES (500). Rendering
    // all of them used to freeze the tab for seconds (e.g. /tmp on a busy
    // box); the picker must cap the list and show a "type to filter" hint.
    const bigDir = mkdtempSync(path.join(tmpdir(), "dirpicker-big-"));
    try {
      for (let i = 0; i < 520; i++) {
        mkdirSync(path.join(bigDir, `sub-${String(i).padStart(4, "0")}`));
      }
      mkdirSync(path.join(bigDir, "zz-needle"));

      await page.goto("/new");
      await page.waitForLoadState("domcontentloaded");
      await page.locator(".status-field-cwd .status-chip").click();

      const panel = page.locator(".modal.directory-picker-modal");
      await expect(panel).toBeVisible();
      await panel.locator(".directory-picker-input").fill(bigDir + "/");

      // 500 rows rendered (+1 for the ".." parent row), remainder in the hint.
      await expect(panel.locator(".directory-picker-entry")).toHaveCount(501);
      await expect(panel.locator(".directory-picker-truncated")).toContainText(
        "more — type to filter",
      );

      // Filtering reaches entries beyond the cap. Count entry names (the
      // ".." parent row has no .directory-picker-entry-name).
      await panel.locator(".directory-picker-input").fill(bigDir + "/zz");
      await expect(panel.locator(".directory-picker-entry-name")).toHaveCount(1);
      await expect(panel.locator(".directory-picker-entry-name")).toContainText("zz-needle");

      await page.keyboard.press("Escape");
      await expect(panel).toHaveCount(0);
    } finally {
      rmSync(bigDir, { recursive: true, force: true });
    }
  });
});
