import { test, expect, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";
import type { Customization } from "../src/services/customizations";

// These specs exercise workspace mode, which each test turns on before its
// page loads and off again so the worker's other specs see stock Shelley.
test.beforeEach(async ({ request }) => {
  const resp = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(resp.ok()).toBeTruthy();
});
test.afterEach(async ({ request }) => {
  const resp = await request.delete("/feature-flags", { data: { name: "workspace" } });
  expect(resp.ok()).toBeTruthy();
});

async function openWorkspace(page: Page) {
  // An off-canvas mobile drawer still counts as visible; open it explicitly.
  // isVisible() doesn't wait, so retry until the page has rendered its
  // header (and with it the button, on narrow screens).
  const open = page.getByRole("button", { name: "Open tasks", exact: true });
  const row = page.getByTestId("assistant-row");
  await expect(async () => {
    if (await open.isVisible()) await open.click();
    await expect(row).toBeInViewport({ timeout: 1000 });
  }).toPass();
}

test("installed build restarts on one click", async ({ page, request }) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  let state: Customization = {
    conversation_id: conversation.conversationId,
    commit: "1234567890abcdef",
    artifact_sha256: "abc123",
    status: "installed",
    error: "",
    updated_at: "",
    running_under_systemd: true,
    finished: false,
  };
  let restartCount = 0;
  await page.route("**/api/customizations", (route) => route.fulfill({ json: [state] }));
  // This test NEVER reaches the real install endpoint.
  await page.route(
    `**/api/conversation/${conversation.conversationId}/customization/restart`,
    async (route) => {
      restartCount++;
      expect(route.request().postDataJSON()).toEqual({
        commit: state.commit,
        artifact_sha256: state.artifact_sha256,
      });
      state = { ...state, status: "restarting" };
      await route.fulfill({ status: 202, json: state });
    },
  );
  await page.goto("/");
  await openWorkspace(page);
  const build = page.getByRole("region", { name: "Shelley build", exact: true });
  await expect(build.getByRole("status")).toHaveText("Installed", { timeout: 12_000 });
  await expect(build).not.toContainText("12345678");
  await build.getByRole("button", { name: "Restart Shelley", exact: true }).dblclick();
  await expect(build).toContainText("Restarting…");
  await expect(build.getByRole("button", { name: "Restart Shelley", exact: true })).toHaveCount(0);
  expect(restartCount).toBe(1);
  await expect(build).not.toContainText("Verification output");
  await page.reload();
  await openWorkspace(page);
  await expect(build).toContainText("Restarting…");
  state = { ...state, status: "running" };
  await expect(build.getByRole("status")).toHaveText("Running", { timeout: 12_000 });
  expect(restartCount).toBe(1);
});

test("stale and failed builds never offer restart; refusal survives refresh", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  let state: Customization = {
    conversation_id: conversation.conversationId,
    commit: "fedcba9876543210",
    artifact_sha256: "def456",
    status: "stale",
    error: "Checkout changed",
    updated_at: "",
    running_under_systemd: true,
    finished: false,
  };
  await page.route("**/api/customizations", (route) => route.fulfill({ json: [state] }));
  await page.route(
    `**/api/conversation/${conversation.conversationId}/customization/restart`,
    (route) => route.fulfill({ status: 409, body: "Wait for running commands" }),
  );
  await page.goto("/");
  await openWorkspace(page);
  const build = page.getByRole("region", { name: "Shelley build", exact: true });
  await expect(build).toContainText("Out of date");
  await expect(build.getByRole("button", { name: "Restart Shelley", exact: true })).toHaveCount(0);
  state = { ...state, status: "failed", error: "Tests failed" };
  await expect(build).toContainText("Tests failed", { timeout: 12_000 });
  await expect(build.getByRole("button", { name: "Restart Shelley", exact: true })).toHaveCount(0);
  state = { ...state, status: "ready", error: "Install failed" };
  await expect(build.getByRole("status")).toHaveText("Not installed", { timeout: 12_000 });
  await expect(build.getByRole("button", { name: "Restart Shelley", exact: true })).toHaveCount(0);
  state = { ...state, status: "installed", error: "" };
  await expect(build.getByRole("status")).toHaveText("Installed", { timeout: 12_000 });
  await build.getByRole("button", { name: "Restart Shelley", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "Wait for running commands" }),
  ).toBeVisible();
  await expect(build.getByRole("status")).toHaveText("Installed", { timeout: 12_000 });
});
