import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { testWorkingDirectory } from "./helpers";

// Workspace mode (the "workspace" feature flag): a pinned Assistant, the
// drawer always grouped by project, and task wording.

test("workspace mode pins the Assistant and always groups by project", async ({
  page,
  request,
}) => {
  const name = `Workspace ${randomUUID().slice(0, 8)}`;
  const project = await request.post("/api/projects", {
    headers: { "X-Shelley-Request": "1" },
    data: { name, instructions: "", cwd: testWorkingDirectory(), actions: [] },
  });
  expect(project.ok()).toBeTruthy();
  const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(on.ok()).toBeTruthy();
  try {
    // A grouping chosen outside workspace mode doesn't apply in it.
    await page.addInitScript(() => localStorage.setItem("shelley-group-by", "none"));
    await page.goto("/");
    await expect(page.locator("h1.header-title")).toHaveText("Assistant");
    await page.getByRole("button", { name: "Open tasks", exact: true }).click();
    await expect(page.getByTestId("assistant-row")).toBeVisible();
    await expect(page.getByRole("region", { name, exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Group conversations" })).toHaveCount(0);
  } finally {
    const off = await request.delete("/feature-flags", { data: { name: "workspace" } });
    expect(off.ok()).toBeTruthy();
  }
});

test("workspace mode doesn't seed a user: filter that hides API-started tasks", async ({
  page,
  request,
}) => {
  const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(on.ok()).toBeTruthy();
  try {
    const project = `seed-${randomUUID().slice(0, 8)}`;
    const made = await request.post("/api/projects", {
      headers: { "X-Shelley-Request": "1" },
      data: { name: project, instructions: "", cwd: testWorkingDirectory() },
    });
    expect(made.ok()).toBeTruthy();
    const ids: string[] = [];
    for (const message of ["echo: shared", "echo: api started"]) {
      const created = await request.post("/api/conversations/new", {
        headers: { "X-Shelley-Request": "1" },
        data: { project, message },
      });
      expect(created.ok()).toBeTruthy();
      ids.push((await created.json()).conversation_id);
    }
    const [shared, apiStarted] = ids;
    // Several people wrote in one task, which seeds user:<me> in stock mode;
    // the API-started task has no human author.
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await page.route("**/api/conversations/snapshot", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      for (const c of body.conversations) {
        c.participants =
          c.conversation_id === shared
            ? [
                { email: "me@example.com", message_count: 1 },
                { email: "colleague@example.com", message_count: 1 },
              ]
            : [];
      }
      await route.fulfill({ response, json: body });
    });
    // The stream would replace the patched list with the real one.
    await page.route("**/api/stream2**", (route) => route.abort());
    await page.goto("/");
    await page.getByRole("button", { name: "Open tasks", exact: true }).click();
    await expect(page.locator(`[data-conversation-id="${shared}"]`)).toBeVisible();
    await expect(page.locator(`[data-conversation-id="${apiStarted}"]`)).toBeVisible();
    await expect(page.getByRole("button", { name: "Search tasks..." })).not.toHaveClass(
      /search-toggle-active/,
    );
  } finally {
    const off = await request.delete("/feature-flags", { data: { name: "workspace" } });
    expect(off.ok()).toBeTruthy();
  }
});
