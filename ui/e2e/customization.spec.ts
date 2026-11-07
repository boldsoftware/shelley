import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// These specs exercise workspace mode, which each test turns on before its
// page loads and off again so the worker's other specs see stock Shelley.
// They drive the drawer's full build line, which title-only rows (the
// compact-nav flag, on by default) shrink to a label.
test.beforeEach(async ({ request }) => {
  for (const [name, value] of [
    ["workspace", true],
    ["compact-nav", false],
  ]) {
    const resp = await request.post("/feature-flags", { data: { name, value } });
    expect(resp.ok()).toBeTruthy();
  }
});
test.afterEach(async ({ request }) => {
  for (const name of ["workspace", "compact-nav"]) {
    const resp = await request.delete("/feature-flags", { data: { name } });
    expect(resp.ok()).toBeTruthy();
  }
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

// Reports a step of a task's install-custom run, as the script does.
async function report(
  request: APIRequestContext,
  conversationId: string,
  status: string,
  error = "",
) {
  const resp = await request.post(`/api/conversation/${conversationId}/customization`, {
    headers: { "X-Shelley-Request": "1" },
    data: { status, commit: "1234567890abcdef", error },
  });
  expect(resp.ok()).toBeTruthy();
}

test("a task's install-custom run shows on its row as the stream sends it", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  const id = conversation.conversationId;
  await page.goto("/");
  await openWorkspace(page);
  const build = page.getByRole("region", { name: "Shelley build", exact: true });
  const restart = build.getByRole("button", { name: "Restart Shelley", exact: true });
  await expect(build).toHaveCount(0);

  await report(request, id, "verifying");
  await expect(build.getByRole("status")).toHaveText("Verifying…");
  await expect(restart).toHaveCount(0);

  await report(request, id, "failed", "lint failed; see the log");
  await expect(build.getByRole("status")).toHaveText("Failed");
  await expect(build).toContainText("lint failed; see the log");
  await expect(restart).toHaveCount(0);

  await report(request, id, "installed");
  await expect(build.getByRole("status")).toHaveText("Installed");
  await expect(build).not.toContainText("12345678");
  await expect(restart).toBeVisible();

  // A reload gets the states when the stream connects.
  await page.reload();
  await openWorkspace(page);
  await expect(build.getByRole("status")).toHaveText("Installed");
});

test("title-only rows show the build as a badge", async ({ page, request }) => {
  const resp = await request.post("/feature-flags", { data: { name: "compact-nav", value: true } });
  expect(resp.ok()).toBeTruthy();
  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  await report(request, conversation.conversationId, "verifying");
  await page.goto("/");
  await openWorkspace(page);
  const badge = page.getByRole("status", { name: "Shelley build", exact: true });
  await expect(badge).toHaveText("Verifying…");
  await report(request, conversation.conversationId, "installed");
  await expect(badge).toHaveText("Installed");
});
