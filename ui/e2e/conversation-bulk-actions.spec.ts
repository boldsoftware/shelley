import { randomUUID } from "node:crypto";
import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import type { ConversationWithState } from "../src/types";
import type { Workspace } from "../src/services/workspace";
import {
  createConversationViaAPIWithDetails,
  fillConversationQuery,
  testWorkingDirectory,
} from "./helpers";

const row = (page: Page, id: string) =>
  page.locator(`.conversation-item[data-conversation-id="${id}"]`);
const status = (page: Page) => page.locator(".drawer-bulk-status");

async function setTags(request: APIRequestContext, id: string, tags: string[]) {
  const response = await request.post(`/api/conversation/${id}/tags`, { data: { tags } });
  expect(response.ok()).toBeTruthy();
}

async function tagsFor(request: APIRequestContext, id: string): Promise<string[]> {
  const response = await request.get(`/api/conversation/${id}`);
  expect(response.ok()).toBeTruthy();
  const { conversation } = await response.json();
  return JSON.parse(conversation.tags || "[]").sort();
}

async function archivedIds(request: APIRequestContext): Promise<string[]> {
  const response = await request.get("/api/conversations/archived");
  expect(response.ok()).toBeTruthy();
  const conversations: ConversationWithState[] = await response.json();
  return conversations.map((conversation) => conversation.conversation_id);
}

async function openSearch(page: Page, path: string) {
  await page.goto(path);
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await page.getByRole("button", { name: "Search conversations...", exact: true }).click();
  await expect(page.locator(".drawer-search-input")).toBeVisible();
}

async function selectMatches(page: Page, count: number) {
  await page.getByRole("button", { name: "Select conversations", exact: true }).click();
  await page
    .getByRole("checkbox", { name: `Select all ${count} matching conversations`, exact: true })
    .check();
  await expect(page.locator(".drawer-bulk-count")).toHaveText(`${count} selected`);
}

test.describe("Conversation bulk actions", () => {
  test("selection stays in the filtered list; tags preserve untouched values; archive can be undone and restored", async ({
    page,
    request,
  }) => {
    const scope = `bulk-${randomUUID()}`;
    const common = `${scope}-common`;
    const privateTag = `${scope}-private`;
    const added = `${scope}-added`;
    const conversations = [];
    for (let index = 0; index < 3; index++) {
      const conversation = await createConversationViaAPIWithDetails(
        request,
        `echo: bulk ${index}`,
      );
      await setTags(request, conversation.conversationId, [
        scope,
        common,
        ...(index === 0 ? [privateTag] : []),
      ]);
      conversations.push(conversation);
    }
    const outside = await createConversationViaAPIWithDetails(request, "echo: outside bulk filter");
    await openSearch(page, `/c/${outside.slug}`);
    const originalURL = page.url();
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(3);
    await expect(row(page, outside.conversationId)).toBeHidden();
    await page.getByRole("button", { name: "Select conversations", exact: true }).click();

    // Use visual order, not creation order: grouping/sorting can reorder rows.
    const checks = page.locator(".drawer .conversation-item:visible .drawer-selection-check");
    await checks.first().click();
    await checks.last().click({ modifiers: ["Shift"] });
    await expect(page.locator(".drawer-bulk-count")).toHaveText("3 selected");
    for (const conversation of conversations) {
      await expect(
        row(page, conversation.conversationId).locator(".drawer-selection-check"),
      ).toBeChecked();
    }
    await expect(page).toHaveURL(originalURL);
    const all = page.getByRole("checkbox", {
      name: "Select all 3 matching conversations",
      exact: true,
    });
    await all.uncheck();
    await expect(page.locator(".drawer-bulk-count")).toHaveText("0 selected");
    await all.check();

    await page.getByRole("button", { name: "Tag selected conversations", exact: true }).click();
    const tags = page
      .locator(".drawer-bulk-popover")
      .filter({ has: page.getByLabel("New tag", { exact: true }) });
    const privateCheck = tags
      .locator("label")
      .filter({ hasText: `#${privateTag}` })
      .locator("input");
    await expect(privateCheck).toHaveJSProperty("indeterminate", true);
    const commonCheck = tags
      .locator("label")
      .filter({ hasText: `#${common}` })
      .locator("input");
    await expect(commonCheck).toBeChecked();
    await commonCheck.uncheck();
    await tags.getByLabel("New tag", { exact: true }).fill(added);
    await tags.getByRole("button", { name: "Apply tags", exact: true }).click();
    await expect(status(page)).toHaveText("Tags updated on 3 conversations");
    for (const [index, conversation] of conversations.entries()) {
      expect(await tagsFor(request, conversation.conversationId)).toEqual(
        [scope, added, ...(index === 0 ? [privateTag] : [])].sort(),
      );
    }
    expect(await tagsFor(request, outside.conversationId)).toEqual([]);

    await page.getByRole("button", { name: "Archive selected conversations", exact: true }).click();
    await expect(status(page)).toContainText("3 archived");
    for (const conversation of conversations) {
      await expect(row(page, conversation.conversationId)).toBeHidden();
    }
    expect(await archivedIds(request)).toEqual(
      expect.arrayContaining(conversations.map((c) => c.conversationId)),
    );
    expect(await archivedIds(request)).not.toContain(outside.conversationId);
    await status(page).getByRole("button", { name: "Undo", exact: true }).click();
    await expect(status(page)).toHaveText("Undone");
    for (const conversation of conversations) {
      await expect(row(page, conversation.conversationId)).toBeVisible();
      expect(await archivedIds(request)).not.toContain(conversation.conversationId);
    }

    await selectMatches(page, 3);
    await page.getByRole("button", { name: "Archive selected conversations", exact: true }).click();
    await expect(status(page)).toContainText("3 archived");
    await page.getByRole("button", { name: "View Archived", exact: true }).click();
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(3);
    await selectMatches(page, 3);
    await page.getByRole("button", { name: "Restore selected conversations", exact: true }).click();
    await expect(status(page)).toHaveText("3 restored");
    await expect(page.getByRole("button", { name: "View Archived", exact: true })).toBeVisible();
    for (const conversation of conversations) {
      await expect(row(page, conversation.conversationId)).toBeVisible();
      expect(await archivedIds(request)).not.toContain(conversation.conversationId);
    }
  });

  test("workspace failure blocks archive instead of assuming there are no VMs", async ({
    page,
    request,
  }) => {
    const conversation = await createConversationViaAPIWithDetails(
      request,
      "echo: workspace unavailable",
    );
    const scope = `workspace-failure-${randomUUID()}`;
    await setTags(request, conversation.conversationId, [scope]);
    let workspaceRequests = 0;
    await page.route("**/api/workspace", async (route) => {
      workspaceRequests++;
      await route.fulfill({
        status: 500,
        contentType: "text/plain",
        body: "workspace unavailable",
      });
    });
    const archiveRequests: string[] = [];
    await page.route("**/api/conversation/*/archive", async (route) => {
      archiveRequests.push(route.request().url());
      await route.fulfill({ status: 500, body: "Archive must wait for workspace" });
    });
    await openSearch(page, `/c/${conversation.slug}`);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await selectMatches(page, 1);
    const archive = page.getByRole("button", {
      name: "Archive selected conversations",
      exact: true,
    });
    await archive.click();
    await expect(page.locator(".drawer-row-error")).toContainText("workspace unavailable");
    await expect(archive).toBeEnabled();
    expect(workspaceRequests).toBeGreaterThan(0);
    expect(archiveRequests).toEqual([]);
    await expect(
      row(page, conversation.conversationId).locator(".drawer-selection-check"),
    ).toBeChecked();
    await expect(status(page).getByRole("button", { name: "Undo", exact: true })).toBeHidden();
    expect(await archivedIds(request)).not.toContain(conversation.conversationId);
  });

  test("folded tag groups reset selection and stay out of Shift ranges", async ({
    page,
    request,
  }) => {
    const scope = `fold-${randomUUID()}`;
    const conversations = [];
    for (const suffix of ["a", "b", "c"]) {
      const conversation = await createConversationViaAPIWithDetails(
        request,
        `echo: fold ${suffix}`,
      );
      await setTags(request, conversation.conversationId, [scope, `${scope}-${suffix}`]);
      conversations.push(conversation);
    }
    await openSearch(page, `/c/${conversations[1]!.slug}`);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await page.getByRole("button", { name: "View options", exact: true }).click();
    await page.getByRole("button", { name: "Tags", exact: true }).click();
    const groups = page.locator(".conversation-group");
    await expect(groups.locator(".conversation-group-label")).toHaveText(
      ["a", "b", "c"].map((suffix) => `#${scope} #${scope}-${suffix}`),
    );
    const middle = groups.filter({
      has: page.locator(".conversation-group-label").filter({ hasText: `#${scope}-b` }),
    });
    await page.getByRole("button", { name: "Select conversations", exact: true }).click();
    await row(page, conversations[1]!.conversationId).locator(".drawer-selection-check").click();
    await expect(page.locator(".drawer-bulk-count")).toHaveText("1 selected");
    await middle.locator(".conversation-group-header").click();
    await expect(row(page, conversations[1]!.conversationId)).toBeHidden();
    await expect(
      page.getByRole("button", { name: "Select conversations", exact: true }),
    ).toBeVisible();
    await selectMatches(page, 3);
    const all = page.getByRole("checkbox", {
      name: "Select all 3 matching conversations",
      exact: true,
    });
    await all.uncheck();
    await expect(page.locator(".drawer-bulk-count")).toHaveText("0 selected");
    const checks = page.locator(".drawer .conversation-item:visible .drawer-selection-check");
    await expect(checks).toHaveCount(2);
    await checks.first().click();
    await checks.last().click({ modifiers: ["Shift"] });
    await expect(page.locator(".drawer-bulk-count")).toHaveText("2 selected");
    const archived: string[] = [];
    await page.route("**/api/conversation/*/archive", async (route) => {
      archived.push(route.request().url().split("/").at(-2)!);
      await route.continue();
    });
    await page.getByRole("button", { name: "Archive selected conversations", exact: true }).click();
    await expect(status(page)).toContainText("2 archived");
    expect(archived.sort()).toEqual(
      [conversations[0]!.conversationId, conversations[2]!.conversationId].sort(),
    );
    expect(await archivedIds(request)).not.toContain(conversations[1]!.conversationId);
  });

  test("Archived revisits exclude restored rows and include newly archived rows", async ({
    page,
    request,
  }) => {
    const scope = `archive-cache-${randomUUID()}`;
    const conversations = [];
    for (const label of ["restore", "keep archived", "archive later"]) {
      const conversation = await createConversationViaAPIWithDetails(request, `echo: ${label}`);
      await setTags(request, conversation.conversationId, [scope]);
      conversations.push(conversation);
    }
    const [restored, kept, later] = conversations;
    for (const conversation of [restored!, kept!]) {
      const response = await request.post(
        `/api/conversation/${conversation.conversationId}/archive`,
      );
      expect(response.ok()).toBeTruthy();
    }
    await openSearch(page, `/c/${later!.slug}`);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await page.getByRole("button", { name: "View Archived", exact: true }).click();
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(2);
    await page.getByRole("button", { name: "Select conversations", exact: true }).click();
    await row(page, restored!.conversationId).locator(".drawer-selection-check").click();
    await page.getByRole("button", { name: "Restore selected conversations", exact: true }).click();
    await expect(status(page)).toHaveText("1 restored");
    await page.getByRole("button", { name: "View Archived", exact: true }).click();
    await expect(row(page, kept!.conversationId)).toBeVisible();
    await expect(row(page, restored!.conversationId)).toBeHidden();
    await expect(row(page, later!.conversationId)).toBeHidden();
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(1);

    await page.getByRole("button", { name: "Back to conversations", exact: true }).click();
    await expect(row(page, restored!.conversationId)).toBeVisible();
    await page.getByRole("button", { name: "Select conversations", exact: true }).click();
    await row(page, later!.conversationId).locator(".drawer-selection-check").click();
    await page.getByRole("button", { name: "Archive selected conversations", exact: true }).click();
    await expect(status(page)).toContainText("1 archived");
    await page.getByRole("button", { name: "View Archived", exact: true }).click();
    await expect(row(page, kept!.conversationId)).toBeVisible();
    await expect(row(page, later!.conversationId)).toBeVisible();
    await expect(row(page, restored!.conversationId)).toBeHidden();
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(2);
    const persisted = await archivedIds(request);
    expect(persisted).toEqual(
      expect.arrayContaining([kept!.conversationId, later!.conversationId]),
    );
    expect(persisted).not.toContain(restored!.conversationId);
  });

  test("partial archive Undo keeps progress and retries only unfinished restores", async ({
    page,
    request,
  }) => {
    const scope = `undo-retry-${randomUUID()}`;
    const first = await createConversationViaAPIWithDetails(request, "echo: undo first");
    const second = await createConversationViaAPIWithDetails(request, "echo: undo second");
    for (const conversation of [first, second]) {
      await setTags(request, conversation.conversationId, [scope]);
    }
    await openSearch(page, "/new");
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await selectMatches(page, 2);
    await page.getByRole("button", { name: "Archive selected conversations", exact: true }).click();
    await expect(status(page)).toContainText("2 archived");
    const restoredIds: string[] = [];
    await page.route("**/api/conversation/*/unarchive", async (route) => {
      restoredIds.push(route.request().url().split("/").at(-2)!);
      if (restoredIds.length === 2) {
        await route.fulfill({
          status: 500,
          contentType: "text/plain",
          body: "undo restore failure",
        });
      } else {
        await route.continue();
      }
    });
    const undo = status(page).getByRole("button", { name: "Undo", exact: true });
    await undo.click();
    await expect(page.locator(".drawer-row-error")).toContainText("undo restore failure");
    await expect(status(page)).toContainText("1 of 2");
    await expect(undo).toBeVisible();
    await expect(undo).toBeEnabled();
    expect(restoredIds).toHaveLength(2);
    const [completed, unfinished] = restoredIds;
    expect(new Set(restoredIds)).toEqual(new Set([first.conversationId, second.conversationId]));
    const archived = await archivedIds(request);
    expect(archived).not.toContain(completed);
    expect(archived).toContain(unfinished);
    await undo.click();
    await expect(status(page)).toHaveText("Undone");
    expect(restoredIds).toEqual([completed, unfinished, unfinished]);
    for (const conversation of [first, second]) {
      await expect(row(page, conversation.conversationId)).toBeVisible();
      expect(await archivedIds(request)).not.toContain(conversation.conversationId);
    }
  });

  test("cancelling the first VM archive confirmation stops the batch and keeps selection", async ({
    page,
    request,
  }) => {
    const scope = `cancel-archive-${randomUUID()}`;
    const first = await createConversationViaAPIWithDetails(request, "echo: VM archive target");
    const second = await createConversationViaAPIWithDetails(request, "echo: next archive target");
    for (const conversation of [first, second]) {
      await setTags(request, conversation.conversationId, [scope]);
    }
    const path = `/c/${first.slug}`;
    await openSearch(page, path);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(2);
    const firstVisualId = await page
      .locator(".drawer .conversation-item:visible")
      .first()
      .getAttribute("data-conversation-id");
    expect(firstVisualId).toBeTruthy();

    const response = await request.get("/api/workspace");
    expect(response.ok()).toBeTruthy();
    const workspace: Workspace = await response.json();
    await page.route("**/api/workspace", (route) =>
      route.fulfill({
        json: {
          ...workspace,
          vms: [
            {
              conversation_id: firstVisualId,
              vm_name: "bulk-cancel-vm",
              base_vm: "",
              integration: "exe",
              shared: false,
              status: "ready",
              error: "",
              ssh_dest: "",
              https_url: "",
              dir: testWorkingDirectory(),
              conversation_archived: false,
              replaced: false,
            },
          ],
        },
      }),
    );
    const archiveRequests: string[] = [];
    await page.route("**/api/conversation/*/archive", async (route) => {
      archiveRequests.push(route.request().url());
      await route.fulfill({ status: 500, body: "Unexpected archive request after cancellation" });
    });
    await openSearch(page, path);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await expect(page.locator(".drawer .conversation-item:visible").first()).toHaveAttribute(
      "data-conversation-id",
      firstVisualId!,
    );
    await selectMatches(page, 2);
    const archive = page.getByRole("button", {
      name: "Archive selected conversations",
      exact: true,
    });
    await archive.click();
    const confirmation = page.getByRole("dialog", { name: "Archive conversation?", exact: true });
    await expect(confirmation).toContainText("Archiving deletes VM bulk-cancel-vm");
    expect(archiveRequests).toEqual([]);
    await confirmation.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(confirmation).toHaveCount(0);
    await expect(status(page)).toHaveText("0 of 2 archived; cancelled");
    await expect(archive).toBeEnabled();
    await expect(page.locator(".drawer-bulk-count")).toHaveText("2 selected");
    for (const conversation of [first, second]) {
      await expect(
        row(page, conversation.conversationId).locator(".drawer-selection-check"),
      ).toBeChecked();
      expect(await archivedIds(request)).not.toContain(conversation.conversationId);
    }
    expect(archiveRequests).toEqual([]);
  });

  test("moves idle conversations between named projects and undoes the move", async ({
    page,
    request,
  }) => {
    const scope = `move-${randomUUID()}`;
    const source = `${scope}-source`;
    const destination = `${scope}-destination`;
    for (const name of [source, destination]) {
      const response = await request.post("/api/projects", {
        headers: { "X-Shelley-Request": "1" },
        data: { name, instructions: "", cwd: testWorkingDirectory(), actions: [] },
      });
      expect(response.ok()).toBeTruthy();
    }
    const conversations = [];
    for (let index = 0; index < 2; index++) {
      const conversation = await createConversationViaAPIWithDetails(
        request,
        `echo: move ${index}`,
      );
      await setTags(request, conversation.conversationId, [scope]);
      const response = await request.put(
        `/api/conversation/${conversation.conversationId}/project`,
        {
          data: { project: source },
        },
      );
      expect(response.ok()).toBeTruthy();
      conversations.push(conversation);
    }
    const expectProject = async (project: string) => {
      const response = await request.get("/api/workspace");
      expect(response.ok()).toBeTruthy();
      const workspace: { conversations: { conversation_id: string; project: string }[] } =
        await response.json();
      for (const conversation of conversations) {
        expect(
          workspace.conversations.find((c) => c.conversation_id === conversation.conversationId)
            ?.project,
        ).toBe(project);
      }
    };
    await openSearch(page, `/c/${conversations[0]!.slug}`);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await selectMatches(page, 2);
    await page.getByRole("button", { name: "Move to project", exact: true }).click();
    await page
      .locator(".drawer-bulk-popover")
      .getByRole("button", { name: destination, exact: true })
      .click();
    await expect(status(page)).toContainText(`2 moved to ${destination}`);
    await expectProject(destination);
    await status(page).getByRole("button", { name: "Undo", exact: true }).click();
    await expect(status(page)).toHaveText("Undone");
    await expectProject(source);
    await page.reload();
    await expect(
      page.getByLabel("Conversation project", { exact: true }).locator("option:checked"),
    ).toHaveText(source);
  });

  test("a partial tag failure is visible and retry preserves existing tags", async ({
    page,
    request,
  }) => {
    const scope = `failure-${randomUUID()}`;
    const added = `${scope}-added`;
    const first = await createConversationViaAPIWithDetails(request, "echo: first tag target");
    const second = await createConversationViaAPIWithDetails(request, "echo: second tag target");
    for (const conversation of [first, second]) {
      await setTags(request, conversation.conversationId, [scope]);
    }
    let attempted = 0;
    const successful: string[] = [];
    await page.route("**/api/conversation/*/tags", async (route) => {
      attempted++;
      if (attempted === 2) {
        await route.fulfill({ status: 500, contentType: "text/plain", body: "bulk tag failure" });
      } else {
        successful.push(route.request().url().split("/").at(-2)!);
        await route.continue();
      }
    });
    await openSearch(page, `/c/${first.slug}`);
    await fillConversationQuery(page.locator(".drawer-search-input"), `tag:${scope} `);
    await selectMatches(page, 2);
    await page.getByRole("button", { name: "Tag selected conversations", exact: true }).click();
    await page.getByLabel("New tag", { exact: true }).fill(added);
    await page.getByRole("button", { name: "Apply tags", exact: true }).click();
    await expect(page.locator(".drawer-row-error")).toContainText("bulk tag failure");
    await expect(status(page)).toHaveText("Tags updated on 1 of 2 conversations");
    await expect(page.locator(".drawer-bulk-count")).toHaveText("2 selected");
    expect(attempted).toBe(2);
    expect(successful).toHaveLength(1);
    for (const conversation of [first, second]) {
      await expect(
        row(page, conversation.conversationId).locator(".drawer-selection-check"),
      ).toBeChecked();
      expect(await tagsFor(request, conversation.conversationId)).toEqual(
        (successful.includes(conversation.conversationId) ? [scope, added] : [scope]).sort(),
      );
    }
    await expect(page.getByRole("button", { name: "Apply tags", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Apply tags", exact: true }).click();
    await expect(status(page)).toHaveText("Tags updated on 2 conversations");
    await expect(page.locator(".drawer-row-error")).toHaveCount(0);
    for (const conversation of [first, second]) {
      expect(await tagsFor(request, conversation.conversationId)).toEqual([scope, added].sort());
    }
  });

  test("Activity filters updated time for each period and removes its token", async ({
    page,
    request,
  }) => {
    const now = new Date("2026-11-20T12:00:00Z");
    await page.clock.setFixedTime(now);
    const seed = await createConversationViaAPIWithDetails(request, "echo: activity seed");
    const response = await request.get("/api/conversations");
    expect(response.ok()).toBeTruthy();
    const corpus: ConversationWithState[] = await response.json();
    const original = corpus.find(
      (conversation) => conversation.conversation_id === seed.conversationId,
    );
    expect(original).toBeTruthy();
    const conversations = [0.5, 3, 14, 45].map((days, index) => ({
      ...original!,
      conversation_id: index === 0 ? seed.conversationId : `bulk-activity-${index}`,
      slug: index === 0 ? seed.slug : `bulk-activity-${index}`,
      updated_at: new Date(now.getTime() - days * 86_400_000).toISOString(),
      created_at: "2026-01-01T00:00:00Z",
      working: false,
      subagent_count: 0,
    }));
    await page.route("**/api/conversations/snapshot", (route) =>
      route.fulfill({ json: { conversations, hash: "bulk-activity" } }),
    );
    await page.route("**/api/stream2*", (route) => route.abort());
    await openSearch(page, `/c/${seed.slug}`);
    for (const [label, indices] of [
      ["Past day", [0]],
      ["Past week", [0, 1]],
      ["Past month", [0, 1, 2]],
      ["1d+", [1, 2, 3]],
      ["1w+", [2, 3]],
      ["1mo+", [3]],
    ] as const) {
      await page.getByRole("button", { name: "Activity", exact: true }).click();
      await page
        .locator(".drawer-bulk-popover")
        .getByRole("button", { name: label, exact: true })
        .click();
      await expect(page.locator('[data-conversation-query-token="activity"]')).toBeVisible();
      for (const [index, conversation] of conversations.entries()) {
        if ((indices as readonly number[]).includes(index)) {
          await expect(row(page, conversation.conversation_id)).toBeVisible();
        } else {
          await expect(row(page, conversation.conversation_id)).toBeHidden();
        }
      }
    }
    await page.getByRole("button", { name: "Remove activity filter", exact: true }).click();
    await expect(page.locator('[data-conversation-query-token="activity"]')).toHaveCount(0);
    await expect(page.locator(".drawer .conversation-item:visible")).toHaveCount(4);
  });
});
