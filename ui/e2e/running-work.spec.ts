import { test, expect, type APIRequestContext } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// These jobs and subagents sleep for minutes; stop them so a failed run does
// not leave them on the shared server.
async function stopWork(request: APIRequestContext, conversationIds: string[]) {
  for (const id of conversationIds) {
    const subagents = await (await request.get(`/api/conversation/${id}/subagents`)).json();
    for (const { conversation_id } of subagents ?? []) {
      await request.post(`/api/conversation/${conversation_id}/cancel`);
    }
    const jobs = await (await request.get(`/api/conversation/${id}/background-jobs`)).json();
    for (const { job_id } of jobs ?? []) {
      await request.post(`/api/conversation/${id}/background-jobs/${job_id}/kill`);
    }
  }
}

test.describe("running work", () => {
  test("uses the full preview width for a lone agent name", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: ready",
    );
    const agentSlug = "background-worker-alpha";
    await page.route("**/api/stream2*", (route) => route.abort());
    await page.route("**/api/conversations/snapshot", async (route) => {
      const response = await route.fetch();
      const snapshot = await response.json();
      const parent = snapshot.conversations.find(
        (c: { conversation_id: string }) => c.conversation_id === conversationId,
      );
      expect(parent).toBeTruthy();
      snapshot.conversations.push({
        ...parent,
        conversation_id: "label-preview-agent",
        slug: agentSlug,
        parent_conversation_id: conversationId,
        user_initiated: false,
        working: true,
        agent_working: true,
        running_background_jobs: 0,
      });
      await route.fulfill({ response, json: snapshot });
    });
    await page.goto(`/c/${slug}`);
    const preview = page.getByTestId("running-work-toggle").locator(".running-work-name");
    await expect(preview).toHaveText(agentSlug);
    await expect
      .poll(() => preview.evaluate((el) => el.scrollWidth - el.clientWidth))
      .toBeLessThanOrEqual(1);
    expect(
      await preview.evaluate(
        (el) => el.scrollWidth > el.closest<HTMLElement>(".running-work-summary")!.clientWidth / 2,
      ),
    ).toBe(true);
  });

  test("fits agent-first work previews to the available width", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: ready",
    );
    await page.route("**/api/stream2*", (route) => route.abort());
    await page.route("**/api/conversation/preview-agent-*/background-jobs", (route) =>
      route.fulfill({
        json: [
          {
            job_id: `job-${route.request().url().split("/").at(-2)}`,
            command: "printf preview-ready; tail -f /dev/null",
            tail: "preview-ready",
            started_at: "2026-10-17T22:00:00Z",
          },
        ],
      }),
    );
    await page.route("**/api/conversations/snapshot", async (route) => {
      const response = await route.fetch();
      const snapshot = await response.json();
      const parent = snapshot.conversations.find(
        (c: { conversation_id: string }) => c.conversation_id === conversationId,
      );
      expect(parent).toBeTruthy();
      for (let i = 1; i <= 3; i++) {
        snapshot.conversations.push({
          ...parent,
          conversation_id: `preview-agent-${i}`,
          slug: ["demo-index", "demo-assets", "demo-build"][i - 1],
          parent_conversation_id: conversationId,
          user_initiated: false,
          working: false,
          agent_working: false,
          running_background_jobs: 1,
        });
      }
      await route.fulfill({ response, json: snapshot });
    });
    await page.goto(`/c/${slug}`);
    const toggle = page.getByTestId("running-work-toggle");
    await expect(toggle.locator(".running-work-name")).toHaveCount(2);
    await expect(toggle.locator(".running-work-name")).toHaveText(["demo-index", "demo-assets"]);
    await expect(toggle.locator(".running-work-more")).toHaveText("and 4 more");
    await expect(toggle).toContainText("3 tasks · 3 subagents");

    await page.setViewportSize({ width: 1600, height: 900 });
    await expect(toggle.locator(".running-work-name")).toHaveText([
      "demo-index",
      "demo-assets",
      "demo-build",
      "printf preview-ready; tail -f /dev/null",
      "printf preview-ready; tail -f /dev/null",
      "printf preview-ready; tail -f /dev/null",
    ]);
    await expect(toggle.locator(".running-work-more")).toHaveCount(0);
    await expect
      .poll(() =>
        toggle.locator(".running-work-names").evaluate((el) => el.scrollWidth - el.clientWidth),
      )
      .toBeLessThanOrEqual(1);

    await page.setViewportSize({ width: 390, height: 700 });
    await expect(toggle.locator(".running-work-name")).toHaveText(["demo-index", "demo-assets"]);
    await expect(toggle.locator(".running-work-more")).toHaveText("and 4 more");
    await expect
      .poll(() =>
        toggle.locator(".running-work-names").evaluate((el) => el.scrollWidth - el.clientWidth),
      )
      .toBeLessThanOrEqual(1);
  });

  test("keeps a subagent visible while its background task runs", async ({ page, request }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "subagent: backgroundhelper bash-bg: printf agent-ready; tail -f /dev/null",
    );
    let subagentId: string | undefined;
    try {
      await expect
        .poll(
          async () => {
            const subagents = await (
              await request.get(`/api/conversation/${conversationId}/subagents`)
            ).json();
            subagentId = subagents?.[0]?.conversation_id;
            if (!subagentId) return false;
            const response = await request.get(`/api/conversation/${subagentId}`);
            const { conversation } = await response.json();
            const parent = await (
              await request.get(`/api/conversation/${conversationId}`)
            ).json();
            const jobs = await (
              await request.get(`/api/conversation/${subagentId}/background-jobs`)
            ).json();
            return !parent.conversation.agent_working && !conversation.agent_working && jobs.length === 1;
          },
          { timeout: 30000 },
        )
        .toBe(true);

      await page.goto(`/c/${slug}`);
      const strip = page.getByLabel("Running work", { exact: true });
      const toggle = strip.getByTestId("running-work-toggle");
      await expect(toggle).toContainText("1 subagent", { timeout: 30000 });
      await expect(toggle).toContainText("1 task");
      await expect(toggle.locator(".running-work-name")).toHaveText([
        "backgroundhelper",
        "printf agent-ready; tail -f /dev/null",
      ]);
      await expect
        .poll(() =>
          toggle
            .locator(".running-work-name")
            .first()
            .evaluate((el) => el.scrollWidth - el.clientWidth),
        )
        .toBeLessThanOrEqual(1);
      await expect(toggle.locator(".running-work-more")).toHaveCount(0);
      const working = page.getByTestId("agent-thinking");
      const parentDot = page.locator(
        `[data-conversation-id="${conversationId}"] .drawer-working-indicator`,
      );
      const favicon = page.locator('link[rel="icon"]');
      await expect(working).toBeVisible();
      await expect(parentDot).toBeVisible();
      await expect(favicon).toHaveAttribute("href", /opacity%3D%220\.35%22/);
      await expect(page.locator(".status-ready")).toHaveCount(0);
      await expect(page.locator(".status-stop-button")).toHaveCount(0);
      await expect(page.locator(".status-readout-model")).toBeEnabled();
      await page.setViewportSize({ width: 390, height: 800 });
      await expect(page.locator(".status-bar-active > .working-indicator")).toBeHidden();
      await page.getByRole("button", { name: "More options", exact: true }).click();
      const model = page.locator(".model-picker-menu");
      await expect(model).toHaveClass(/p-disabled/);
      await expect(model).toHaveCSS("opacity", "0.5");
      await page.keyboard.press("Escape");
      await page.setViewportSize({ width: 1280, height: 800 });
      await toggle.click();
      const group = strip.getByRole("group", { name: "Work by backgroundhelper" });
      const row = group.locator(".running-work-agent");
      const job = group.getByTestId("background-job");
      await expect(row).toContainText("backgroundhelper");
      await expect(row).toContainText("Waiting on 1 background task");
      await expect(row.locator(".running-work-agent-time")).toContainText("Running");
      await expect(job).toContainText("printf agent-ready; tail -f /dev/null");
      await expect(strip.locator("details, summary")).toHaveCount(0);
      await expect(job.getByTestId("background-job-tail")).toContainText("agent-ready");

      await toggle.click();
      await expect(toggle).toContainText("1 subagent");
      await expect(toggle.locator(".running-work-name")).toHaveText([
        "backgroundhelper",
        "printf agent-ready; tail -f /dev/null",
      ]);
      await expect(toggle.locator(".running-work-more")).toHaveCount(0);
      await toggle.click();
      await expect(row).toContainText("Waiting on 1 background task");
      await expect(job.getByTestId("background-job-tail")).toBeVisible();
      expect(
        (
          await request.post(`/api/conversation/${conversationId}/chat`, {
            data: {
              message: "bash-bg: printf parent-ready; tail -f /dev/null",
              model: "predictable",
            },
          })
        ).ok(),
      ).toBe(true);
      await expect(toggle).toContainText("2 tasks");
      await expect(strip.getByTestId("background-job")).toHaveCount(2);
      await expect(group.getByTestId("background-job")).toHaveCount(1);
      const stopped = page.waitForResponse(
        (response) =>
          response.url().includes(`/api/conversation/${subagentId}/background-jobs/`) &&
          response.url().endsWith("/kill") &&
          response.request().method() === "POST",
      );
      await job.getByTestId("background-job-kill").click();
      expect((await stopped).ok()).toBe(true);
      await expect(job.locator(".running-command-time")).toHaveText("Finished");
      await expect(job.getByTestId("background-job-kill")).toHaveCount(0);
      await expect(job.getByTestId("background-job-tail")).toContainText("agent-ready");
      await expect(row.locator(".running-work-agent-time")).toHaveText("Finished", {
        timeout: 15000,
      });
      await expect(toggle).toContainText("1 task");
      await expect(working).toBeVisible();
      await expect(parentDot).toBeVisible();
      await expect(favicon).toHaveAttribute("href", /opacity%3D%220\.35%22/);
      const parentJob = strip.getByTestId("background-job").filter({ hasText: "parent-ready" });
      await expect(parentJob.locator(".running-command-time")).toContainText("Running");
      await stopWork(request, [conversationId]);
      await expect(toggle).toContainText("No running work");
      await expect(working).toHaveCount(0);
      await expect(parentDot).toHaveCount(0);
      await expect(page.locator(".status-ready")).toBeVisible();
      await expect(favicon).not.toHaveAttribute("href", /opacity%3D%220\.35%22/);
      await toggle.click();
      await expect(strip).toHaveCount(0);
    } finally {
      await stopWork(request, [conversationId, ...(subagentId ? [subagentId] : [])]);
    }
  });

  test("keeps mobile work visible and retains finished jobs while others run", async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width: 390, height: 500 });
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "bash-bg: echo ready; sleep 600",
    );
    try {
      expect(
        (
          await request.post(`/api/conversation/${conversationId}/chat`, {
            data: { message: "bash-bg: sleep 601", model: "predictable" },
          })
        ).ok(),
      ).toBe(true);
      await page.goto(`/c/${slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

      const strip = page.getByLabel("Running work", { exact: true });
      const toggle = strip.getByTestId("running-work-toggle");
      await expect(toggle).toContainText("2 tasks", { timeout: 15000 });
      const previews = toggle.locator(".running-work-name");
      await expect(previews.filter({ hasText: "echo ready; sleep 600" })).toBeVisible();
      await expect(previews.filter({ hasText: "sleep 601" })).toBeVisible();
      await expect(toggle).not.toContainText("Running:");
      expect(
        await strip.evaluate((element) =>
          element.nextElementSibling?.classList.contains("messages-bottom-sentinel"),
        ),
      ).toBe(true);

      const scroller = page.locator(".messages-container");
      await scroller.evaluate((element) => {
        element.scrollTop = element.scrollHeight;
      });
      await expect
        .poll(() =>
          scroller.evaluate(
            (element) => element.scrollHeight - element.clientHeight - element.scrollTop,
          ),
        )
        .toBeLessThanOrEqual(2);
      await toggle.evaluate((element) => {
        element.dispatchEvent(new Event("touchstart", { bubbles: true }));
        (element as HTMLElement).click();
      });
      const items = strip.getByTestId("running-work-items");
      const jobs = items.getByTestId("background-job");
      await expect(jobs).toHaveCount(2);
      await expect(jobs.filter({ hasText: "echo ready" })).toBeVisible();
      await expect(jobs.filter({ hasText: "sleep 601" })).toBeVisible();
      await expect
        .poll(() =>
          strip.evaluate((element) => {
            const container = element.closest(".messages-container")!.getBoundingClientRect();
            const panel = element.getBoundingClientRect();
            return panel.bottom > container.top && panel.bottom <= container.bottom;
          }),
        )
        .toBe(true);

      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await scroller.evaluate((element) => {
        element.dispatchEvent(new WheelEvent("wheel", { deltaY: -20, bubbles: true }));
        element.scrollTop -= 20;
      });
      const readingPosition = await scroller.evaluate((element) => element.scrollTop);
      await toggle.evaluate((element) => (element as HTMLElement).click());
      await expect(items).toBeVisible();
      await expect
        .poll(async () =>
          Math.abs((await scroller.evaluate((element) => element.scrollTop)) - readingPosition),
        )
        .toBeLessThanOrEqual(2);

      const echo = jobs.filter({ hasText: "echo ready" });
      await expect(echo.getByTestId("background-job-tail")).toContainText("ready");
      await expect(items.locator("details, summary")).toHaveCount(0);
      const liveJobs = await (
        await request.get(`/api/conversation/${conversationId}/background-jobs`)
      ).json();
      const job = liveJobs.find((job: { command: string }) => job.command.includes("echo ready"));
      expect(
        (
          await request.post(
            `/api/conversation/${conversationId}/background-jobs/${job.job_id}/kill`,
          )
        ).ok(),
      ).toBe(true);
      await expect(toggle).toContainText("1 task", { timeout: 15000 });
      await expect(echo.locator(".running-command-time")).toHaveText("Finished");
      await expect(echo.getByTestId("background-job-kill")).toHaveCount(0);
      await expect(echo.getByTestId("background-job-tail")).toContainText("ready");
      await expect(
        jobs.filter({ hasText: "sleep 601" }).locator(".running-command-time"),
      ).toContainText("Running");
    } finally {
      await stopWork(request, [conversationId]);
    }
  });

  test("keeps a finished subagent row and opens its conversation", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "hello there",
    );
    try {
      await page.goto(`/c/${slug}`);
      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });

      await input.fill("subagent: statushelper bash: sleep 120");
      await page.getByTestId("send-button").click();

      const strip = page.getByLabel("Running work", { exact: true });
      await expect(strip.getByTestId("running-work-toggle")).toContainText("statushelper", {
        timeout: 30000,
      });
      await strip.getByTestId("running-work-toggle").click();
      const row = strip.locator(".running-work-agent");
      await expect(row).toContainText("statushelper");
      await expect(row).toContainText("sleep", { timeout: 30000 });
      const [subagent] = await (
        await request.get(`/api/conversation/${conversationId}/subagents`)
      ).json();
      expect(
        (await request.post(`/api/conversation/${subagent.conversation_id}/cancel`)).ok(),
      ).toBe(true);
      await expect(row.locator(".running-work-agent-time")).toHaveText("Finished", {
        timeout: 15000,
      });
      await expect(strip.getByTestId("running-work-toggle")).toHaveAttribute(
        "aria-expanded",
        "true",
      );
      await row.click();
      await expect(page).toHaveURL(/\/c\/statushelper/, { timeout: 10000 });
    } finally {
      await stopWork(request, [conversationId]);
    }
  });

  test("discards finished subagent rows when the panel closes", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "hello there",
    );
    try {
      await page.goto(`/c/${slug}`);
      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });
      await input.fill("subagent: finishhelper bash: sleep 120");
      await page.getByTestId("send-button").click();
      const strip = page.getByLabel("Running work", { exact: true });
      const toggle = strip.getByTestId("running-work-toggle");
      await expect(toggle).toContainText("finishhelper", { timeout: 30000 });
      await toggle.click();
      const row = strip.locator(".running-work-agent");
      await expect(row).toContainText("finishhelper");
      const [subagent] = await (
        await request.get(`/api/conversation/${conversationId}/subagents`)
      ).json();
      expect(
        (await request.post(`/api/conversation/${subagent.conversation_id}/cancel`)).ok(),
      ).toBe(true);
      await expect(row.locator(".running-work-agent-time")).toHaveText("Finished", {
        timeout: 15000,
      });
      await toggle.click();
      await expect(strip).toHaveCount(0);
    } finally {
      await stopWork(request, [conversationId]);
    }
  });
});
