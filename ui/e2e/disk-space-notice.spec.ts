import { test, expect, type Page } from "@playwright/test";

const lowStatus = {
  episode_id: 7,
  revision: 3,
  active: true,
  critical: false,
  dismissed: false,
  available_bytes: 1_600_000_000,
  total_bytes: 79_000_000_000,
};

// mockStream answers every /api/stream2 connection with status(). The mocked
// body ends right away, so the client reconnects about a second later, and
// once it knows the conversation list hash the URL gains a query string; both
// must hit the mock, or the real server's status replaces the one under test.
async function mockStream(page: Page, status: () => object) {
  await page.route(
    (url) => url.pathname === "/api/stream2",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `data: ${JSON.stringify({ disk_space_status: status() })}\n\n`,
      });
    },
  );
}

test.describe("Disk space notice", () => {
  test("shows stream status and dismisses the current episode", async ({ page }) => {
    let current = lowStatus;
    await mockStream(page, () => current);

    let dismissedEpisode: number | undefined;
    await page.route("**/api/disk-space/dismiss", async (route) => {
      dismissedEpisode = route.request().postDataJSON().episode_id;
      current = { ...lowStatus, revision: 4, dismissed: true };
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(current),
      });
    });

    await page.goto("/");

    const notice = page.getByTestId("disk-space-notice");
    await expect(notice).toBeVisible({ timeout: 30000 });
    await expect(notice).toContainText("Disk space is low");
    await expect(notice).toContainText("1.6 GB remaining");
    await expect(notice).toHaveAttribute("role", "status");

    await page.getByTestId("disk-space-notice-dismiss").click();

    await expect.poll(() => dismissedEpisode).toBe(lowStatus.episode_id);
    await expect(notice).toHaveCount(0);
  });

  test("links to an exe.dev resize suggestion on exe.dev hosts", async ({ page }) => {
    await page.addInitScript(() => {
      let init: Record<string, unknown> | undefined;
      Object.defineProperty(window, "__SHELLEY_INIT__", {
        configurable: true,
        get: () => init,
        set: (value) => {
          init = { ...value, is_exe_dev: true, hostname: "myvm.exe.xyz" };
        },
      });
    });
    await mockStream(page, () => lowStatus);

    await page.goto("/");

    const link = page.getByTestId("disk-space-notice-link");
    await expect(link).toHaveText("Resize this VM", { timeout: 30000 });
    await expect(link).toHaveAttribute(
      "href",
      "https://exe.dev/suggest?command=resize%20myvm%20--disk%3D90GB",
    );
  });

  test("announces critical status as an alert", async ({ page }) => {
    await mockStream(page, () => ({ ...lowStatus, critical: true, available_bytes: 400_000_000 }));

    await page.goto("/");

    const notice = page.getByTestId("disk-space-notice");
    await expect(notice).toBeVisible({ timeout: 30000 });
    await expect(notice).toContainText("Disk space is critically low");
    await expect(notice).toHaveAttribute("role", "alert");
  });
});
