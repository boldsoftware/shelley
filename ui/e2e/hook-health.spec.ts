import { test, expect, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

type HookHealth = {
  episode_id: number;
  revision: number;
  active: boolean;
  dismissed: boolean;
  failures: number;
  last_failure_at: string;
  conversation_id: string;
};

const failed = (overrides: Partial<HookHealth> = {}): HookHealth => ({
  episode_id: 1,
  revision: 1,
  active: true,
  dismissed: false,
  failures: 1,
  last_failure_at: "2026-11-14T23:00:00Z",
  conversation_id: "hook-chat",
  ...overrides,
});

const healthy = (revision = 0): HookHealth =>
  failed({
    episode_id: 0,
    revision,
    active: false,
    dismissed: false,
    failures: 0,
    last_failure_at: "",
    conversation_id: "",
  });

async function mockHookStream(page: Page, current: () => HookHealth) {
  await page.route(
    (url) => url.pathname === "/api/stream2",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: [
          { hook_health: current() },
          { heartbeat: true },
        ]
          .map((frame) => `data: ${JSON.stringify(frame)}\n\n`)
          .join(""),
      });
    },
  );
}

test.describe("End-of-turn hook health", () => {
  test("keeps the mobile action below the full hook name and failure count", async ({ page }) => {
    let current = failed();
    await mockHookStream(page, () => current);

    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 844 });
      for (const failures of [1, 111]) {
        current = failed({ failures });
        await page.goto("/new");
        const notice = page.getByTestId("hook-health-notice");
        await expect(notice).toBeVisible();
        await page.evaluate(() => document.fonts.ready);
        const layout = await notice.evaluate((element) => {
          const title = element.querySelector<HTMLElement>("[data-testid=hook-health-title]")!;
          const text = element.querySelector<HTMLElement>(".resource-notice-text")!;
          const action = element.querySelector<HTMLElement>(
            "[data-testid=hook-health-troubleshoot]",
          )!;
          const range = document.createRange();
          range.selectNodeContents(title);
          return {
            titleLines: range.getClientRects().length,
            textBottom: text.getBoundingClientRect().bottom,
            actionTop: action.getBoundingClientRect().top,
            width: element.clientWidth,
            scrollWidth: element.scrollWidth,
          };
        });
        expect(layout.titleLines).toBe(1);
        expect(layout.actionTop).toBeGreaterThanOrEqual(layout.textBottom);
        expect(layout.scrollWidth).toBeLessThanOrEqual(layout.width);
      }
    }

    await page.setViewportSize({ width: 1280, height: 844 });
    await expect(page.getByTestId("hook-health-notice")).toHaveCSS("display", "flex");
  });

  test("aggregates failures, recovers, dismisses one episode, and re-arms", async ({ page, request }) => {
    let current = healthy();
    await mockHookStream(page, () => current);
    const conversation = await createConversationViaAPIWithDetails(request, "echo: hook health");

    let dismissAttempts = 0;
    await page.route("**/api/hooks/end-of-turn/dismiss", async (route) => {
      expect(route.request().postDataJSON()).toEqual({ episode_id: current.episode_id });
      dismissAttempts += 1;
      if (dismissAttempts === 1) {
        await route.fulfill({ status: 500, body: "failed" });
        return;
      }
      current = { ...current, revision: current.revision + 1, dismissed: true };
      await route.fulfill({ json: current });
    });

    await page.goto(`/c/${conversation.slug}`);
    const notice = page.getByTestId("hook-health-notice");
    await expect(notice).toHaveCount(0);
    current = failed();
    await page.reload();
    await expect(notice).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId("hook-health-title")).toHaveText("Hook end-of-turn failed");
    await expect(notice).toContainText("1 consecutive failure");

    current = failed({ revision: 2, failures: 111 });
    await page.reload();
    await expect(notice).toContainText("111 consecutive failures", { timeout: 30000 });
    await expect(page.getByTestId("hook-health-notice")).toHaveCount(1);

    current = healthy(3);
    await page.reload();
    await expect(notice).toHaveCount(0, { timeout: 30000 });
    current = failed({ episode_id: 2, revision: 1, failures: 1 });
    await page.reload();
    await expect(notice).toBeVisible({ timeout: 30000 });
    await expect(notice).toContainText("1 consecutive failure");

    // The warning is global, not attached to the current conversation.
    const other = await createConversationViaAPIWithDetails(request, "echo: another chat");
    await page.goto(`/c/${other.slug}`);
    await expect(notice).toBeVisible();

    await page.getByTestId("hook-health-dismiss").click();
    await expect(notice).toBeVisible();
    await expect.poll(() => dismissAttempts).toBe(1);
    await page.getByTestId("hook-health-dismiss").click();
    await expect.poll(() => dismissAttempts).toBe(2);
    await expect(notice).toHaveCount(0);

    await page.reload();
    await expect(notice).toHaveCount(0);
    current = failed({ episode_id: 2, revision: 4, failures: 2, dismissed: true });
    await page.reload();
    await expect(notice).toHaveCount(0);
    current = healthy(5);
    await page.reload();
    await expect(notice).toHaveCount(0);
    current = failed({ episode_id: 3, revision: 1, failures: 1 });
    await page.reload();
    await expect(page.getByTestId("hook-health-notice")).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId("hook-health-notice")).toContainText("1 consecutive failure");
  });

  test("troubleshooting starts a safe new chat; failed requests preserve the current composer", async ({
    page,
    request,
  }) => {
    const status = failed({
      conversation_id: "affected-conversation",
      failures: 2,
    });
    await mockHookStream(page, () => status);
    const conversation = await createConversationViaAPIWithDetails(request, "echo: diagnose hook");
    const sentRequests: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "POST") sentRequests.push(new URL(req.url()).pathname);
    });

    await page.route("**/api/conversations/new", async (route) => {
      await route.fulfill({
        status: 500,
        contentType: "text/plain",
        body: "chat creation failed",
      });
    });
    await page.goto(`/c/${conversation.slug}`);
    const notice = page.getByTestId("hook-health-notice");
    await expect(notice).toBeVisible({ timeout: 30000 });
    const input = page.getByTestId("message-input");
    await input.fill("keep my current unsent note");
    await page.getByRole("button", { name: "Troubleshoot with Shelley", exact: true }).click();
    await expect(notice).toBeVisible();
    await expect(input).toHaveValue("keep my current unsent note");
    await expect(page.getByText("Failed to start conversation: chat creation failed")).toBeVisible();

    await page.unroute("**/api/conversations/new");
    let chatBody = "";
    await page.route("**/api/conversations/new", async (route) => {
      chatBody = route.request().postData() ?? "";
      await route.fallback();
    });
    await page.getByRole("button", { name: "Troubleshoot with Shelley", exact: true }).click();
    await expect.poll(() => chatBody).not.toBe("");
    const safePrompt = JSON.parse(chatBody) as { message?: string };
    expect(safePrompt.message).toContain("end-of-turn");
    expect(safePrompt.message).toContain("Consecutive failed invocations: 2");
    expect(safePrompt.message).toContain("Latest failure (UTC): 2026-11-14T23:00:00Z");
    expect(safePrompt.message).toContain("Latest affected conversation ID: affected-conversation");
    expect(safePrompt.message).toMatch(/read-only|inspect/i);
    expect(safePrompt.message).toMatch(/redact|secret/i);
    expect(safePrompt.message).not.toMatch(/stderr|credential-value|webhook-token/i);
    await expect(page).not.toHaveURL(`/c/${conversation.slug}`);
    await expect(
      page.locator(".messages-container .message-user").filter({ hasText: /end-of-turn/ }).first(),
    ).toBeVisible({ timeout: 30000 });
    expect(sentRequests.filter((path) => path === "/api/conversations/new")).toHaveLength(2);
    expect(sentRequests.some((path) => /\/chat$/.test(path))).toBe(false);
    await page.goto(`/c/${conversation.slug}`);
    await expect(input).toHaveValue("keep my current unsent note");
  });
});
