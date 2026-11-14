import { test, expect } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

async function getGitRoot(request: import("@playwright/test").APIRequestContext): Promise<string> {
  const resp = await request.get("/api/git/diffs?cwd=.");
  expect(resp.ok()).toBeTruthy();
  const data = await resp.json();
  expect(data.gitRoot).toBeTruthy();
  return data.gitRoot;
}

test.describe("Conversation grouping", () => {
  test("active conversation should not be grouped under Other when grouped by git repo", async ({
    page,
    request,
  }) => {
    const gitRoot = await getGitRoot(request);
    await createConversationViaAPIWithDetails(request, "Hello from conversation A", {
      cwd: gitRoot,
    });
    const active = await createConversationViaAPIWithDetails(request, "Hello from conversation B", {
      cwd: gitRoot,
    });

    await page.goto(`/c/${active.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    // Open drawer and enable grouping by git repo.
    await page.locator('button[aria-label="Open conversations"]').click();
    await expect(page.locator(".drawer.open")).toBeVisible();
    await page.locator('button[aria-label="View options"]').click();
    await page.getByRole("button", { name: "Git Repo" }).click();

    // The group containing the active conversation should not be "Other".
    const activeGroup = page.locator(".conversation-group").filter({
      has: page.locator(".conversation-item.active"),
    });
    await expect(activeGroup).toHaveCount(1);

    const activeGroupLabel = (
      await activeGroup.locator(".conversation-group-label").innerText()
    ).trim();
    expect(activeGroupLabel).not.toBe("Other");
  });
});
