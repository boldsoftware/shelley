import {
  clearConversationQuery,
  createConversationViaAPIWithDetails,
  fillConversationQuery,
} from "./helpers";
import { expect, test, type Locator, type Page } from "@playwright/test";
import type { ConversationWithState } from "../src/types";

test.use({
  viewport: { width: 1280, height: 720 },
  isMobile: false,
  hasTouch: false,
});

for (const width of [390, 1280]) {
  test(`drawer header starts a new conversation at ${width}px`, async ({ page, request }) => {
    await page.setViewportSize({ width, height: 800 });
    const { slug } = await createConversationViaAPIWithDetails(request, "echo: drawer new");
    await page.goto(`/c/${slug}`);
    if (width < 768) {
      await page.getByRole("button", { name: "Open conversations", exact: true }).click();
    }

    const actions = page.locator(".drawer-header-actions");
    const labels = [
      "Search conversations...",
      "Group conversations",
      "New Conversation",
      width < 768 ? "Close conversations" : "Collapse sidebar",
    ];
    const buttons = actions.getByRole("button");
    await expect(buttons).toHaveCount(labels.length);
    for (const [index, label] of labels.entries()) {
      await expect(buttons.nth(index)).toHaveAccessibleName(label);
    }
    await actions.getByRole("button", { name: "New Conversation", exact: true }).click();
    await expect(page).toHaveURL(/\/new$/);
    await expect(page.getByTestId("message-input")).toBeEmpty();
    if (width < 768) await expect(page.locator(".drawer")).not.toHaveClass(/\bopen\b/);
  });
}

test("right-clicking a conversation row opens its actions at the pointer", async ({
  page,
  request,
}) => {
  const first = await createConversationViaAPIWithDetails(request, "context menu first");
  const second = await createConversationViaAPIWithDetails(request, "context menu second");
  // Other specs' conversations share this drawer, sit anywhere among ours
  // (rows in a recency bucket order by random id), and keep arriving at its
  // top. Unfiltered, our rows can start below the fold, and reaching them
  // scrolls the list, which closes any open menu. A tag of our own keeps just
  // these two rows on screen and still.
  const tag = `ctx-menu-${first.conversationId}`;
  for (const { conversationId } of [first, second]) {
    const resp = await request.post(`/api/conversation/${conversationId}/tags`, {
      data: { tags: [tag] },
    });
    expect(resp.ok()).toBeTruthy();
  }
  await page.goto("/new");
  await page.getByRole("button", { name: "Search conversations..." }).click();
  await fillConversationQuery(queryEditor(page), `tag:${tag} `);
  const firstRow = page.locator(`[data-conversation-id="${first.conversationId}"]`);
  const secondRow = page.locator(`[data-conversation-id="${second.conversationId}"]`);
  const menu = page.getByTestId("conversation-context-menu");
  await expect(page.locator(".conversation-item:visible")).toHaveCount(2);
  await expect(firstRow).toBeInViewport({ ratio: 1 });
  await expect(secondRow).toBeInViewport({ ratio: 1 });

  // The menu opens where the pointer is, in place of the browser's own.
  const box = (await secondRow.locator(".conversation-preview").boundingBox())!;
  await page.mouse.click(box.x + 20, box.y + 5, { button: "right" });
  await expect(menu).toBeVisible();
  await expect(menu.getByRole("menuitem")).toHaveText(["Archive", "Rename", "Edit tags"]);
  const menuBox = (await menu.boundingBox())!;
  expect(Math.abs(menuBox.x - (box.x + 20))).toBeLessThan(4);
  expect(Math.abs(menuBox.y - (box.y + 5))).toBeLessThan(4);
  await expect(secondRow).toHaveClass(/context-menu-target/);
  await expect(page).not.toHaveURL(new RegExp(`/c/${second.slug}$`));

  // Right-clicking another row moves the one menu there.
  await firstRow.click({ button: "right" });
  await expect(menu).toHaveCount(1);
  await expect(firstRow).toHaveClass(/context-menu-target/);
  await expect(secondRow).not.toHaveClass(/context-menu-target/);

  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  await expect(firstRow).not.toHaveClass(/context-menu-target/);

  // A right-click anywhere else closes it.
  await firstRow.click({ button: "right" });
  await expect(menu).toBeVisible();
  await page.getByTestId("message-input").click({ button: "right" });
  await expect(menu).toHaveCount(0);
  await expect(firstRow).not.toHaveClass(/context-menu-target/);

  // Opening it closes any row's "…" menu, so only one action menu shows.
  await secondRow.getByRole("button", { name: "Actions" }).click();
  const dotsMenu = page.getByTestId("conversation-actions-menu");
  await expect(dotsMenu).toBeVisible();
  await firstRow.click({ button: "right" });
  await expect(menu).toBeVisible();
  await expect(dotsMenu).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);

  // Archive acts on the row that was right-clicked.
  await firstRow.click({ button: "right" });
  await menu.getByRole("menuitem", { name: "Archive" }).click();
  await expect(menu).toHaveCount(0);
  await expect(firstRow).toHaveCount(0);
  await expect(secondRow).toBeVisible();
  const archived = await (await request.get("/api/conversations/archived")).json();
  expect(
    archived.some((c: { conversation_id: string }) => c.conversation_id === first.conversationId),
  ).toBe(true);

  // Rename opens the inline editor, whose text field keeps the native menu.
  await secondRow.click({ button: "right" });
  await menu.getByRole("menuitem", { name: "Rename" }).click();
  const input = secondRow.locator(".drawer-rename-input");
  await expect(input).toBeFocused();
  await input.click({ button: "right" });
  await expect(menu).toHaveCount(0);
  await input.press("Escape");
});

test("double-clicking a conversation name renames it and Escape cancels", async ({
  page,
  request,
}) => {
  const conversation = await createConversationViaAPIWithDetails(request, "rename from name");
  await page.goto("/new");
  const row = page.locator(`[data-conversation-id="${conversation.conversationId}"]`);
  const title = row.locator(".conversation-title");
  const input = row.locator(".drawer-rename-input");

  // Single-click still opens the conversation; double-click also opens its editor.
  await title.click();
  await expect(page).toHaveURL(new RegExp(`/c/${conversation.slug}$`));
  await expect(input).toHaveCount(0);
  await title.dblclick();
  await expect(input).toBeFocused();
  await expect(input).toHaveValue(conversation.slug);
  await input.fill("discarded-name");
  await input.press("Escape");
  await expect(title).toHaveText(conversation.slug);

  await page.goto("/new");
  await title.dblclick();
  await expect(input).toBeFocused();
  const newSlug = `renamed-${conversation.conversationId.toLowerCase()}`;
  await input.fill(newSlug);
  await input.press("Enter");
  await expect(title).toHaveText(newSlug);
  await page.reload();
  await expect(title).toHaveText(newSlug);
  await title.click();
  await expect(page).toHaveURL(new RegExp(`/c/${newSlug}$`));
});

function conversation(
  id: string,
  isDraft = false,
  participants?: Array<string | [string, number]>,
): ConversationWithState {
  return {
    conversation_id: id,
    slug: isDraft ? null : id,
    user_initiated: true,
    created_at: "2026-07-23T12:00:00Z",
    updated_at: "2026-07-23T12:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: "predictable",
    conversation_options: "{}",
    current_generation: 1,
    agent_working: false,
    tags: "[]",
    is_draft: isDraft,
    draft: isDraft ? "unfinished message" : "",
    queued_messages: "[]",
    turn_interrupted: false,
    external_conversation_id: null,
    external_endpoint: null,
    working: false,
    subagent_count: 0,
    preview: "Preview",
    max_sequence_id: 0,
    participants: participants?.map((participant) => ({
      email: typeof participant === "string" ? participant : participant[0],
      message_count: typeof participant === "string" ? 1 : participant[1],
    })),
  };
}

async function stubConversationList(page: Page, conversations: ConversationWithState[]) {
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations, hash: `test-${conversations.length}` } }),
  );
  await page.route("**/api/conversations/search**", (route) =>
    route.fulfill({ json: conversations }),
  );
  await page.route("**/api/stream2**", (route) => route.abort());
}

function queryEditor(page: Page): Locator {
  return page.getByTestId("conversation-query-editor");
}

async function expectQuery(editor: Locator, raw: string): Promise<void> {
  await expect(editor).toHaveAttribute("data-query-value", raw);
}

function queryToken(editor: Locator, kind: "tag" | "user" | "untagged" | "unattributed") {
  return editor.locator(`[data-conversation-query-token="${kind}"]`);
}

function exactQueryToken(editor: Locator, kind: "tag" | "user", raw: string) {
  return queryToken(editor, kind).filter({ hasText: raw });
}

test.describe("conversation drawer startup and app bar", () => {
  test("shows a pause mark when a restart interrupted a conversation", async ({ page }) => {
    const interrupted = conversation("interrupted");
    interrupted.turn_interrupted = true;
    const running = conversation("running");
    running.working = true;
    running.agent_working = true;
    await stubConversationList(page, [interrupted, running]);

    await page.goto("/new");

    const interruptedRow = page.locator('[data-conversation-id="interrupted"]');
    await expect(interruptedRow.locator(".drawer-interrupted-indicator")).toBeVisible();
    await expect(interruptedRow.locator(".drawer-working-indicator")).toHaveCount(0);

    const runningRow = page.locator('[data-conversation-id="running"]');
    await expect(runningRow.locator(".drawer-working-indicator")).toBeVisible();
    await expect(runningRow.locator(".drawer-interrupted-indicator")).toHaveCount(0);
  });

  test("shows a running-task gear count that opens the conversation", async ({ page }) => {
    const parent = conversation("parent");
    parent.running_background_jobs = 1;
    await stubConversationList(page, [parent]);

    await page.goto("/new");
    const row = page.locator('[data-conversation-id="parent"]');
    await expect(row.locator(".drawer-working-indicator")).toBeVisible();
    const runningWork = row.getByTestId("conversation-running-work");
    await expect(runningWork).toHaveText("1");
    await expect(runningWork).toHaveAttribute("title", "1 task running");
    await expect(runningWork).toHaveAttribute("aria-label", "1 task running");
    await expect(runningWork.locator("svg")).toBeVisible();

    // The compact indicator is part of the ordinary row target; it no longer
    // expands job logs in the drawer.
    await runningWork.click();
    await expect(page).toHaveURL(/\/c\/parent$/);
    await expect(page.getByTestId("background-job")).toHaveCount(0);
  });

  test("shows running and total subagent counts independently", async ({ page }) => {
    const activeParent = conversation("active-parent");
    activeParent.subagent_count = 3;
    const activeChild = conversation("active-child");
    activeChild.parent_conversation_id = activeParent.conversation_id;
    activeChild.working = true;
    activeChild.running_background_jobs = 1;
    const waitingChild = conversation("waiting-child");
    waitingChild.parent_conversation_id = activeParent.conversation_id;
    waitingChild.running_background_jobs = 2;
    const activeIdleChild = conversation("active-idle-child");
    activeIdleChild.parent_conversation_id = activeParent.conversation_id;

    const idleParent = conversation("idle-parent");
    idleParent.subagent_count = 2;
    const idleChild = conversation("idle-child");
    idleChild.parent_conversation_id = idleParent.conversation_id;
    const secondIdleChild = conversation("second-idle-child");
    secondIdleChild.parent_conversation_id = idleParent.conversation_id;
    await stubConversationList(page, [
      activeParent,
      activeChild,
      waitingChild,
      activeIdleChild,
      idleParent,
      idleChild,
      secondIdleChild,
    ]);

    await page.goto("/new");
    const activeBadge = page.locator(
      '[data-conversation-id="active-parent"] .subagent-count-badge',
    );
    await expect(activeBadge).toContainText("2/3");
    await expect(activeBadge.getByTestId("subagent-badge-running")).toBeVisible();

    const idleBadge = page.locator('[data-conversation-id="idle-parent"] .subagent-count-badge');
    await expect(idleBadge).toHaveText("2");
    await expect(idleBadge.getByTestId("subagent-badge-running")).toHaveCount(0);

    activeChild.working = false;
    activeChild.running_background_jobs = 0;
    await page.reload();
    await expect(activeBadge).toContainText("1/3");
    await expect(activeBadge.getByTestId("subagent-badge-running")).toBeVisible();

    waitingChild.running_background_jobs = 0;
    await page.reload();
    await expect(activeBadge).toHaveText("3");
    await expect(activeBadge.getByTestId("subagent-badge-running")).toHaveCount(0);
  });

  test("truncates long slugs to one line with an ellipsis", async ({ page }) => {
    const long = conversation(
      "unexpected-conversation-navigation-redirect-with-an-even-longer-tail",
    );
    const short = conversation("short");
    await stubConversationList(page, [long, short]);
    await page.goto("/new");

    const longTitle = page.locator(
      `[data-conversation-id="${long.conversation_id}"] .conversation-title`,
    );
    const shortTitle = page.locator('[data-conversation-id="short"] .conversation-title');
    await expect(longTitle).toHaveText(long.slug!);
    await expect(longTitle).toHaveAttribute("title", long.slug!);

    const [longBox, shortBox] = await Promise.all([
      longTitle.boundingBox(),
      shortTitle.boundingBox(),
    ]);
    // Same height as a one-line slug: no wrapping.
    expect(longBox!.height).toBe(shortBox!.height);
    // Overflowing content is clipped with an ellipsis rather than wrapped.
    const overflow = await longTitle.evaluate((el) => ({
      clipped: el.scrollWidth > el.clientWidth,
      textOverflow: getComputedStyle(el).textOverflow,
    }));
    expect(overflow).toEqual({ clipped: true, textOverflow: "ellipsis" });
  });

  test("keeps commit hashes behind a compact copy icon", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    const withCommit = conversation("with-commit");
    withCommit.git_commit = "424904d";
    withCommit.git_subject = "Make git graph file paths readable";
    const hashOnly = conversation("hash-only");
    hashOnly.git_commit = "abcdef1";
    await stubConversationList(page, [withCommit, hashOnly, conversation("no-commit")]);
    await page.goto("/new");

    const row = page.locator('[data-conversation-id="with-commit"]');
    const copy = row.getByRole("button", { name: "Copy commit hash 424904d", exact: true });
    await expect(copy).toBeVisible();
    await expect(row.locator(".drawer-git-info")).toHaveText(withCommit.git_subject);
    await expect(copy.locator("circle")).toHaveCount(3);
    await expect(copy.locator("path")).toHaveAttribute("d", "M6 16.6V7.4M6 16.6C8 11 12 6 14.6 5");
    const before = await copy.boundingBox();
    expect(before!.width).toBeLessThanOrEqual(24);
    const restingColor = await copy.evaluate((el) => getComputedStyle(el).color);
    await copy.hover();
    await expect(page.getByRole("tooltip")).toContainText("Click to copy 424904d");
    // The tooltip can be in the DOM before Chromium applies :hover to the
    // icon, so wait for the hover color rather than sampling it once.
    await expect(copy).not.toHaveCSS("color", restingColor);
    await copy.click();

    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("424904d");
    await expect(copy).toHaveClass(/drawer-git-copy-copied/);
    await expect(copy.locator("circle")).toHaveCount(0);
    await expect(copy).toBeFocused();
    expect(await copy.boundingBox()).toEqual(before);
    await expect(page).toHaveURL(/\/new$/);

    const hashOnlyCopy = page
      .locator('[data-conversation-id="hash-only"]')
      .getByRole("button", { name: "Copy commit hash abcdef1", exact: true });
    await expect(hashOnlyCopy).toBeVisible();
    await hashOnlyCopy.focus();
    await hashOnlyCopy.press("Enter");
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("abcdef1");
    await expect(page).toHaveURL(/\/new$/);
    await expect(page.locator('[data-conversation-id="no-commit"] .drawer-git-info')).toHaveCount(
      0,
    );
  });

  test("highlights matching slug text in drawer and command-palette searches", async ({ page }) => {
    const slugHit = conversation("Pelican-project-pelican");
    const messageHit = conversation("message-only");
    slugHit.tags = messageHit.tags = '["birds"]';
    messageHit.search_snippet = "A \x02pelican\x03 by the bay";
    const conversations = [slugHit, messageHit];
    await stubConversationList(page, conversations);
    await page.goto("/new");

    const title = page.locator(
      '[data-conversation-id="Pelican-project-pelican"] .conversation-title',
    );
    const messageTitle = page.locator('[data-conversation-id="message-only"] .conversation-title');
    await expect(title).toHaveText("Pelican-project-pelican");
    await expect(title.locator("mark")).toHaveCount(0);
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await fillConversationQuery(editor, "PELICAN tag:birds ");
    await expect(title.locator("mark")).toHaveText(["Pelican", "pelican"]);
    await expect(title).toHaveText("Pelican-project-pelican");
    await expect(messageTitle.locator("mark")).toHaveCount(0);
    await expect(page.locator(".conversation-snippet mark")).toHaveText(["pelican"]);

    await fillConversationQuery(editor, "project");
    await expect(title.locator("mark")).toHaveText(["project"]);
    await clearConversationQuery(editor);
    await expect(title.locator("mark")).toHaveCount(0);

    await page.keyboard.press("ControlOrMeta+k");
    const paletteInput = page.locator(".command-palette-input");
    await paletteInput.fill("PELICAN");
    const paletteTitle = page.locator(".command-palette-item-title").filter({
      hasText: "Pelican-project-pelican",
    });
    await expect(paletteTitle.locator("mark")).toHaveText(["Pelican", "pelican"]);
    await expect(paletteTitle).toHaveText("Pelican-project-pelican");
    await expect(
      page
        .locator(".command-palette-item-title")
        .filter({ hasText: "message-only" })
        .locator("mark"),
    ).toHaveCount(0);
    await paletteInput.fill("conversation");
    const actions = page.locator(".command-palette-item").filter({
      has: page.locator(".command-palette-item-badge"),
    });
    await expect(actions.first()).toBeVisible();
    await expect(actions.locator("mark")).toHaveCount(0);
    await paletteInput.fill("");
    await expect(paletteTitle.locator("mark")).toHaveCount(0);
  });

  test("drawer clears stale matches while the next query is pending", async ({ page }) => {
    await stubConversationList(page, [conversation("recent")]);
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    await page.route("**/api/conversations/search**", async route => {
      if (new URL(route.request().url()).searchParams.get("q") === "pending") await gate;
      await route.fulfill({ json: [conversation("needle-match")] });
    });
    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await fillConversationQuery(editor, "needle");
    await expect(page.locator('[data-conversation-id="needle-match"]')).toBeVisible();
    await fillConversationQuery(editor, "pending");
    await expect(page.locator('[data-conversation-id="needle-match"]')).toHaveCount(0);
    await expect(page.getByText("Searching...", { exact: true })).toBeVisible();
    release();
    await clearConversationQuery(editor);
  });

  test("Ask Shelley waits for results and immediately closes the mobile drawer", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const match = conversation("needle-match");
    match.search_snippet = "Supporting passage";
    match.search_snippet_message_id = "source-message";
    const chat = conversation("ask-shelley-fixture");
    chat.conversation_options = JSON.stringify({ kind: "search" });
    await stubConversationList(page, [conversation("recent")]);
    let releaseResults!: () => void;
    const resultsGate = new Promise<void>(resolve => { releaseResults = resolve; });
    let releaseChat!: () => void;
    const chatGate = new Promise<void>(resolve => { releaseChat = resolve; });
    let searchPayload: unknown;
    await page.route("**/api/conversations/search?*", async route => {
      await resultsGate;
      await route.fulfill({ json: [match] });
    });
    await page.route("**/api/conversations/search/enhance", async route => {
      searchPayload = route.request().postDataJSON();
      await chatGate;
      await route.fulfill({ status: 202, json: { conversation: chat } });
    });
    await page.route("**/api/conversation/ask-shelley-fixture**", route =>
      route.fulfill({ json: { conversation: chat, messages: [] } }),
    );
    await page.goto("/new");
    await page.getByRole("button", { name: "Open conversations", exact: true }).click();
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await fillConversationQuery(queryEditor(page), "needle");
    const ask = page.getByRole("button", { name: "Ask Shelley", exact: true });
    await expect(ask).toBeDisabled();
    releaseResults();
    await expect(ask).toBeEnabled();
    await ask.click();
    await expect(page.locator(".drawer")).not.toHaveClass(/\bopen\b/);
    await expect(page).toHaveURL(/\/new$/);
    releaseChat();
    await expect(page).toHaveURL(/\/c\/ask-shelley-fixture$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(searchPayload).toEqual({
      q: "needle", archived: false,
      matches: [{ conversation_id: match.conversation_id, slug: match.slug, snippet: match.search_snippet, message_id: match.search_snippet_message_id }],
    });
  });

  test("drawer searches only the current archive view", async ({ page }) => {
    const active = conversation("needle-active");
    const archived = conversation("needle-archived");
    archived.archived = true;
    await stubConversationList(page, [conversation("recent")]);
    await page.route("**/api/conversations/archived**", (route) =>
      route.fulfill({ json: [archived] }),
    );
    const scopes: boolean[] = [];
    await page.route("**/api/conversations/search**", (route) => {
      const archiveScope = new URL(route.request().url()).searchParams.get("archived") === "true";
      scopes.push(archiveScope);
      return route.fulfill({ json: [archiveScope ? archived : active] });
    });
    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    const titles = page.locator(".drawer .conversation-title");
    await fillConversationQuery(editor, "needle");
    await expect(titles).toHaveText(["needle-active"]);

    await page.getByRole("button", { name: "View Archived", exact: true }).click();
    await expect(titles).toHaveText(["needle-archived"]);
    await expect(editor).toHaveText("needle");

    await page.getByRole("button", { name: "Back to conversations", exact: true }).click();
    await expect(titles).toHaveText(["needle-active"]);
    expect(scopes).toEqual([false, true, false]);
  });

  test("drawer cancels superseded and cleared searches", async ({ page }) => {
    const recent = conversation("recent");
    const hit = conversation("needle-active");
    await stubConversationList(page, [recent]);
    const searches: string[] = [];
    await page.route("**/api/conversations/search**", (route) => {
      const query = new URL(route.request().url()).searchParams.get("q")!;
      searches.push(query);
      if (query === "slow" || query === "cleared") return;
      return route.fulfill({ json: [hit] });
    });
    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);

    const slowRequest = page.waitForRequest(
      (request) => new URL(request.url()).searchParams.get("q") === "slow",
    );
    await fillConversationQuery(editor, "slow");
    const slow = await slowRequest;
    const slowCancelled = page.waitForEvent("requestfailed", (request) => request === slow);
    await fillConversationQuery(editor, "needle");
    await slowCancelled;
    await expect(page.locator('[data-conversation-id="needle-active"]')).toBeVisible();

    const pendingRequest = page.waitForRequest(
      (request) => new URL(request.url()).searchParams.get("q") === "cleared",
    );
    await fillConversationQuery(editor, "cleared");
    const pending = await pendingRequest;
    const pendingCancelled = page.waitForEvent("requestfailed", (request) => request === pending);
    await clearConversationQuery(editor);
    await pendingCancelled;
    await expect(page.locator('[data-conversation-id="recent"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="needle-active"]')).toHaveCount(0);
    expect(searches).toEqual(["slow", "needle", "cleared"]);
  });

  test("command palette uses indexed search and cancels superseded requests", async ({ page }) => {
    const recent = conversation("recent");
    const slugHit = conversation("needle-active");
    const messageHit = conversation("message-only");
    await stubConversationList(page, [recent]);
    const searches: string[] = [];
    const legacySearches: string[] = [];
    await page.route("**/api/conversations?**", (route) => {
      legacySearches.push(route.request().url());
      return route.fulfill({ json: [] });
    });
    await page.route("**/api/conversations/search**", (route) => {
      const query = new URL(route.request().url()).searchParams.get("q")!;
      searches.push(query);
      if (query === "slow" || query === "cleared") return;
      return route.fulfill({ json: [slugHit, messageHit] });
    });
    await page.goto("/new");
    await page.keyboard.press("ControlOrMeta+k");
    const input = page.locator(".command-palette-input");
    const titles = page.locator(".command-palette-item-title");
    await expect(titles.filter({ hasText: "recent" })).toBeVisible();

    const slowRequest = page.waitForRequest(
      (request) => new URL(request.url()).searchParams.get("q") === "slow",
    );
    await input.fill("slow");
    const slow = await slowRequest;
    const slowCancelled = page.waitForEvent("requestfailed", (request) => request === slow);
    await input.fill(" needle ");
    await slowCancelled;
    await expect(titles).toHaveText(["needle-active", "message-only"]);
    await expect(titles.first().locator("mark")).toHaveText(["needle"]);

    const pendingRequest = page.waitForRequest(
      (request) => new URL(request.url()).searchParams.get("q") === "cleared",
    );
    await input.fill("cleared");
    const pending = await pendingRequest;
    const pendingCancelled = page.waitForEvent("requestfailed", (request) => request === pending);
    await input.fill("");
    await pendingCancelled;
    await expect(titles.filter({ hasText: "recent" })).toBeVisible();
    await expect(titles.filter({ hasText: "needle-active" })).toHaveCount(0);
    expect(searches).toEqual(["slow", "needle", "cleared"]);
    expect(legacySearches).toEqual([]);
  });

  test("single-user lists have no participant filter", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com"]);
    mine.subagent_count = 1;
    const subagent = conversation("subagent", false, ["other@example.com"]);
    subagent.parent_conversation_id = mine.conversation_id;
    await stubConversationList(page, [mine, subagent, conversation("draft", true)]);

    await page.goto("/new");

    await expect(page.locator(".drawer-search-input")).toHaveCount(0);
    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await expect(searchToggle).not.toHaveClass(/search-toggle-active/);
    await expect(page.locator(".conversation-participant-badge")).toHaveCount(0);
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await searchToggle.click();
    await expect(page.getByRole("button", { name: "Add user filter" })).toHaveCount(0);
  });

  test("multi-user lists seed the current-user query once", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [
      conversation("mine", false, [
        ["collaborator@example.com", 2],
        ["me@example.com", 3],
      ]),
      conversation("triple", false, [
        "me@example.com",
        "collaborator@example.com",
        "third@example.com",
      ]),
      conversation("other", false, ["other@example.com"]),
      conversation("draft", true),
    ]);

    await page.goto("/new");

    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await expect(queryEditor(page)).toHaveCount(0);
    await expect(searchToggle).toHaveClass(/search-toggle-active/);
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    // A draft has no messages, hence no participants; it still belongs to
    // whoever is typing it and must survive the seeded current-user filter.
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="other"]')).not.toBeVisible();

    const mineBadge = page.locator('[data-conversation-id="mine"] .conversation-participant-badge');
    await expect(mineBadge).toContainText("2");
    await expect(mineBadge.locator(".conversation-participant-badge-front-filled")).toHaveCount(1);
    await mineBadge.click();
    await expect(page.locator(".p-tooltip")).toContainText("collaborator@example.com");
    await expect(page.locator(".p-tooltip strong")).toHaveText("me@example.com");
    await expect(
      page.locator(".p-tooltip .conversation-participant-tooltip-identity-current"),
    ).toHaveCount(1);
    await expect(page.locator(".p-tooltip .conversation-participant-tooltip-identity")).toHaveCount(
      2,
    );
    const tooltipRows = page.locator(".p-tooltip .conversation-participant-tooltip-row");
    await expect(tooltipRows.nth(0)).toContainText("me@example.com");
    await expect(tooltipRows.nth(1)).toContainText("collaborator@example.com");
    await expect(
      page
        .locator(".p-tooltip .conversation-participant-tooltip-row")
        .filter({ hasText: "collaborator@example.com" })
        .locator("strong"),
    ).toHaveCount(0);
    await expect(
      page
        .locator(".p-tooltip .conversation-participant-tooltip-row")
        .filter({ hasText: "me@example.com" })
        .locator(".conversation-participant-tooltip-count"),
    ).toHaveText("3");

    await searchToggle.click();
    const editor = queryEditor(page);
    await expect(editor).toHaveAttribute("autocapitalize", "none");
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await expectQuery(editor, "user:me@example.com ");
    await expect(exactQueryToken(editor, "user", "user:me@example.com")).toBeVisible();
    await expect(page.getByText("@ Participating", { exact: true })).toHaveCount(0);

    const addUserFilter = page.getByRole("button", { name: "Add user filter" });
    const addTagFilter = page.getByRole("button", { name: "Add tag filter" });
    await addUserFilter.click();
    await expectQuery(editor, "user:me@example.com user:");
    await addTagFilter.click();
    await expectQuery(editor, "user:me@example.com tag:");
    await addUserFilter.click();
    await expectQuery(editor, "user:me@example.com user:");
    await page.keyboard.press("Escape");

    const visibleIds = () =>
      page
        .locator("[data-conversation-id]:visible")
        .evaluateAll((rows) => rows.map((row) => row.getAttribute("data-conversation-id")));
    const defaultOrder = await visibleIds();
    await addUserFilter.click();
    const userPanel = page.getByTestId("user-filter-panel");
    await expect(userPanel.getByRole("option", { name: /other@example\.com/ })).toHaveCount(0);
    await userPanel.getByRole("option", { name: /third@example\.com/ }).click();
    await expectQuery(editor, "user:me@example.com user:third@example.com ");
    await expect(queryToken(editor, "user")).toHaveCount(2);
    await expect(exactQueryToken(editor, "user", "user:third@example.com")).toBeVisible();
    await fillConversationQuery(editor, "user:me@example.com ");
    await expectQuery(editor, "user:me@example.com ");
    await expect.poll(visibleIds).toEqual(defaultOrder);

    await searchToggle.click();
    await expect(queryEditor(page)).toHaveCount(0);
    await expect(searchToggle).toHaveClass(/search-toggle-active/);
    await searchToggle.click();
    await expectQuery(editor, "user:me@example.com ");

    await clearConversationQuery(editor);
    await expect(page.locator('[data-conversation-id="other"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await expect(
      page
        .locator('[data-conversation-id="other"]')
        .getByRole("button", { name: "other@example.com" }),
    ).toContainText("1");
    await expect(queryToken(editor, "user")).toHaveCount(0);
    await addUserFilter.click();
    await expectQuery(editor, "user:");
    await expect(userPanel).toBeVisible();
    // Click empty header space (left of the actions) to dismiss the panel.
    await page.locator(".drawer-header").click({ position: { x: 24, y: 24 } });
    await expect(userPanel).toHaveCount(0);
    await addUserFilter.click();
    await expect(userPanel).toBeVisible();
    await userPanel.getByRole("option", { name: /me@example\.com/ }).click();
    await expectQuery(editor, "user:me@example.com ");
    await expect(page.locator('[data-conversation-id="other"]')).not.toBeVisible();
    await clearConversationQuery(editor);
    await searchToggle.click();
    await expect(queryEditor(page)).toHaveCount(0);
    await expect(searchToggle).not.toHaveClass(/search-toggle-active/);
  });

  test("user picker disables exhausted options and reports no matches", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [
      conversation("shared", false, ["me@example.com", "other@example.com"]),
    ]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    const addUserFilter = page.getByRole("button", { name: "Add user filter" });
    const userPanel = page.getByTestId("user-filter-panel");
    await addUserFilter.click();
    await expectQuery(editor, "user:me@example.com user:");
    await editor.pressSequentially("missing");
    await expectQuery(editor, "user:me@example.com user:missing");
    await expect(userPanel.getByText("No matching users")).toBeVisible();
    await fillConversationQuery(editor, "user:me@example.com ");
    await expectQuery(editor, "user:me@example.com ");
    await addUserFilter.click();
    await userPanel.getByRole("option", { name: /other@example\.com/ }).click();
    await expect(addUserFilter).toBeDisabled();
    await addUserFilter.locator("..").hover();
    await expect(page.locator(".p-tooltip")).toContainText("All users added");
  });

  test("token query editing completes, searches, deletes, and wraps safely", async ({
    page,
    context,
  }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com", "other@example.com"]);
    const other = conversation("other", false, ["other@example.com"]);
    const alpine = conversation("alpine", false, ["third@example.com"]);
    mine.tags = JSON.stringify(["alpha", "in progress", "alpine"]);
    alpine.tags = JSON.stringify(["alpine"]);
    await stubConversationList(page, [mine, other, alpine]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await page.locator(".drawer").evaluate((element: HTMLElement) => {
      element.style.width = "600px";
    });

    const editor = queryEditor(page);
    const tagPanel = page.getByTestId("tag-filter-panel");
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await expect(editor).toHaveAttribute("autocapitalize", "none");
    await expect(editor.locator("p")).toHaveCount(1);
    await expect(exactQueryToken(editor, "user", "user:me@example.com")).toBeVisible();

    // Typing a structured cue opens autocomplete; clicking replaces that
    // exact partial with an atom and one ordinary trailing space.
    await exactQueryToken(editor, "user", "user:me@example.com")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("tag:al");
    await expect(tagPanel).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpha"]')).toBeVisible();
    await tagPanel.locator('[data-tag="alpha"]').click();
    await expectQuery(editor, "user:me@example.com tag:alpha ");
    await expect(exactQueryToken(editor, "tag", "tag:alpha")).toBeVisible();

    // A second typed cue creates a second independent token.
    await page.keyboard.type("tag:alpi");
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await tagPanel.locator('[data-tag="alpine"]').click();
    const committed = "user:me@example.com tag:alpha tag:alpine ";
    await expectQuery(editor, committed);
    await expect(queryToken(editor, "tag")).toHaveCount(2);

    // Free text after atoms remains outside them and drives ordinary search.
    await page.keyboard.type("notes");
    await expectQuery(editor, `${committed}notes`);
    await page.keyboard.press("Escape");
    await expectQuery(editor, committed);

    // Copy addresses the serialized query rather than delete-button chrome.
    await editor.focus();
    await page.keyboard.press("ControlOrMeta+A");
    await page.keyboard.press("ControlOrMeta+C");
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(committed);
    await page.keyboard.press("ArrowRight");

    // The explicit delete button removes one token and its separator.
    await exactQueryToken(editor, "tag", "tag:alpine")
      .getByRole("button", { name: "Remove tag:alpine" })
      .click();
    await expectQuery(editor, "user:me@example.com tag:alpha ");
    await expect(queryToken(editor, "tag")).toHaveCount(1);

    // Re-add it, then prove body clicks place the caret after the separator:
    // typing appends text and Backspace removes only that latest character.
    await exactQueryToken(editor, "tag", "tag:alpha")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("tag:alpi");
    await tagPanel.locator('[data-tag="alpine"]').click();
    await exactQueryToken(editor, "tag", "tag:alpine")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("Z");
    await expectQuery(editor, `${committed}Z`);
    await page.keyboard.press("Backspace");
    await expectQuery(editor, committed);

    // Plain structured-looking text is not an atom, so Backspace removes only
    // its trailing separator rather than deleting the whole visible term.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.keyboard.type("tag:missing ");
    await page.keyboard.press("Backspace");
    await expectQuery(editor, "tag:missing");
    await expect(queryToken(editor, "tag")).toHaveCount(0);

    // Pasted line breaks become query separators without creating a second,
    // unserialized ProseMirror paragraph.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.evaluate(() => navigator.clipboard.writeText("alpha\nbeta"));
    await page.keyboard.press("ControlOrMeta+V");
    await expectQuery(editor, "alpha beta");
    await expect(editor.locator("p")).toHaveCount(1);

    // Extra spaces are deleted normally before Backspace reaches the atom.
    await page.locator(".drawer-search-clear").click();
    await page.getByRole("button", { name: "Add user filter" }).click();
    await page
      .getByTestId("user-filter-panel")
      .getByRole("option", { name: /me@example\.com/ })
      .click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="alpha"]').click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="alpine"]').click();
    await expectQuery(editor, committed);
    await page.keyboard.press("Space");
    await expectQuery(editor, `${committed} `);
    await page.keyboard.press("Backspace");
    await expectQuery(editor, committed);

    // Removing the first of adjacent atoms preserves an insertion slot before
    // the second; text typed there remains separate and deletes normally.
    await exactQueryToken(editor, "tag", "tag:alpha")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.press("Backspace");
    const middleSlot = "user:me@example.com  tag:alpine ";
    await expectQuery(editor, middleSlot);
    await expect(exactQueryToken(editor, "tag", "tag:alpha")).toHaveCount(0);
    await page.keyboard.type("Z");
    await expectQuery(editor, "user:me@example.com Z tag:alpine ");
    await page.keyboard.press("Backspace");
    await expectQuery(editor, middleSlot);

    // Space completion also creates an atom and leaves typing outside it.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.keyboard.type("tag:alpi");
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await page.keyboard.press("Space");
    await expectQuery(editor, "tag:alpine ");
    await expect(exactQueryToken(editor, "tag", "tag:alpine")).toBeVisible();
    await page.keyboard.type("followup");
    await expectQuery(editor, "tag:alpine followup");

    // A space inside an open quoted tag stays literal and keeps autocomplete.
    await page.locator(".drawer-search-clear").click();
    await editor.click();
    await page.keyboard.type('tag:"in');
    await expect(tagPanel.locator('[data-tag="in progress"]')).toBeVisible();
    await page.keyboard.press("Space");
    await expectQuery(editor, 'tag:"in ');
    await expect(tagPanel).toBeVisible();

    // Atom nodes wrap only as whole units, never within a token.
    await page.locator(".drawer-search-clear").click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="in progress"]').click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await tagPanel.locator('[data-tag="alpha"]').click();
    await page.locator(".drawer").evaluate((element: HTMLElement) => {
      element.style.width = "250px";
    });
    const tokenLayout = await queryToken(editor, "tag").evaluateAll((tokens) =>
      tokens.map((token) => {
        const prefix = token.querySelector<HTMLElement>(".conversation-query-token-prefix");
        const value = token.querySelector<HTMLElement>(".conversation-query-token-value");
        if (!prefix || !value) throw new Error("token segments missing");
        const tokenRect = token.getBoundingClientRect();
        return {
          height: tokenRect.height,
          prefixTop: prefix.getBoundingClientRect().top,
          valueTop: value.getBoundingClientRect().top,
          whiteSpace: getComputedStyle(token).whiteSpace,
        };
      }),
    );
    expect(tokenLayout).toHaveLength(2);
    for (const token of tokenLayout) {
      expect(token.height).toBeLessThan(30);
      expect(token.prefixTop).toBe(token.valueTop);
      expect(token.whiteSpace).toBe("nowrap");
    }
  });

  test("user and tag filters never hide subagent chats", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const parent = conversation("parent", false, ["me@example.com", "other@example.com"]);
    parent.tags = JSON.stringify(["keep"]);
    parent.subagent_count = 1;
    const subagent = conversation("subagent", false, ["third@example.com"]);
    subagent.parent_conversation_id = parent.conversation_id;
    subagent.tags = JSON.stringify(["different"]);
    await stubConversationList(page, [parent, subagent]);

    await page.goto("/new");
    await expect(page.locator('[data-conversation-id="parent"]')).toBeVisible();
    await page.getByRole("button", { name: "Expand subagents" }).click();
    const subagentRow = page.locator(".subagent-item").filter({ hasText: "subagent" });
    await expect(subagentRow).toBeVisible();

    await page.getByRole("button", { name: "Search conversations..." }).click();
    await page.getByRole("button", { name: "Add tag filter" }).click();
    await page.getByTestId("tag-filter-panel").locator('[data-tag="keep"]').click();
    await expectQuery(queryEditor(page), "user:me@example.com tag:keep ");
    await expect(subagentRow).toBeVisible();
  });

  test("Backspace on a pill ahead of a partial term keeps query and list in sync", async ({
    page,
  }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com", "other@example.com"]);
    mine.tags = JSON.stringify(["alpha"]);
    const other = conversation("other", false, ["other@example.com"]);
    const alpine = conversation("alpine", false, ["third@example.com"]);
    alpine.tags = JSON.stringify(["alpine"]);
    await stubConversationList(page, [mine, other, alpine]);
    // Vue reports a throwing computed via console.error; the aborted stream
    // stub's resource failure is the one expected error.
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error" && !message.text().startsWith("Failed to load resource")) {
        errors.push(message.text());
      }
    });

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    const tagPanel = page.getByTestId("tag-filter-panel");
    const count = page.locator(".drawer-filter-result-count");
    // The new conversation's draft row counts too: drafts pass the
    // participant scope.
    await expectQuery(editor, "user:me@example.com ");
    await expect(count).toHaveText("2");

    // A trailing partial tag, then the caret moved back to sit right after
    // the user pill's separator (a pill body click lands it there).
    const userPill = exactQueryToken(editor, "user", "user:me@example.com").locator(
      ".conversation-query-token-value",
    );
    await userPill.click();
    await page.keyboard.type("tag:al");
    await expectQuery(editor, "user:me@example.com tag:al");
    await expect(tagPanel.locator('[data-tag="alpha"]')).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpine"]')).toHaveCount(0);
    await expect(count).toHaveText("2");
    await userPill.click();

    // Removing the pill shifts the partial left. The active edit must follow
    // the new offsets: the participant scope is gone, so the dropdown now
    // offers tags from the whole list, and once it closes every row is back.
    await page.keyboard.press("Backspace");
    await expectQuery(editor, " tag:al");
    await expect(queryToken(editor, "user")).toHaveCount(0);
    await expect(tagPanel.locator('[data-tag="alpha"]')).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(tagPanel).toHaveCount(0);
    await expectQuery(editor, " tag:al");
    await expect(count).toHaveText("4");
    await expect(page.locator('[data-conversation-id="other"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="alpine"]')).toBeVisible();
    expect(errors).toEqual([]);
  });

  test("an exactly typed tag or user outranks more popular superstrings", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const exact = conversation("exact", false, ["me@example.com", "other@example.com"]);
    exact.tags = JSON.stringify(["alpha"]);
    const more = ["more-1", "more-2", "more-3"].map((id) => {
      const row = conversation(id, false, ["me@example.com", "other@example.com.au"]);
      row.tags = JSON.stringify(["alpha-more"]);
      return row;
    });
    await stubConversationList(page, [exact, ...more]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await expectQuery(editor, "user:me@example.com ");

    // `alpha-more` has three conversations to `alpha`'s one, but the typed
    // value is exactly `alpha`, so Space must commit that and not the more
    // popular superstring.
    const tagPanel = page.getByTestId("tag-filter-panel");
    await exactQueryToken(editor, "user", "user:me@example.com")
      .locator(".conversation-query-token-value")
      .click();
    await page.keyboard.type("tag:alph");
    await expect(tagPanel.getByRole("option").first()).toHaveAttribute("data-tag", "alpha-more");
    await page.keyboard.type("a");
    await expect(tagPanel.getByRole("option").first()).toHaveAttribute("data-tag", "alpha");
    await expect(tagPanel.locator('[aria-selected="true"]')).toHaveAttribute("data-tag", "alpha");
    await page.keyboard.press("Space");
    await expectQuery(editor, "user:me@example.com tag:alpha ");
    await expect(exactQueryToken(editor, "tag", "tag:alpha")).toBeVisible();

    // Same rule for emails, committed with Enter: `other@example.com.au` is
    // on three conversations, `other@example.com` on one.
    await fillConversationQuery(editor, "user:me@example.com ");
    await page.getByRole("button", { name: "Add user filter" }).click();
    await expectQuery(editor, "user:me@example.com user:");
    const userPanel = page.getByTestId("user-filter-panel");
    await expect(userPanel.getByRole("option").first()).toHaveText(/^other@example\.com\.au\s*3$/);
    await page.keyboard.type("other@example.com");
    await expect(userPanel.getByRole("option")).toHaveCount(2);
    await expect(userPanel.getByRole("option").first()).toHaveText(/^other@example\.com\s*1$/);
    await page.keyboard.press("Enter");
    await expectQuery(editor, "user:me@example.com user:other@example.com ");
  });

  test("multiplayer capability follows the active list, not search results", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [conversation("mine", false, ["me@example.com"])]);
    // A search hit from an archived multi-user conversation must not flip the
    // participant UI on mid-session.
    const foreign = conversation("foreign", false, ["me@example.com", "other@example.com"]);
    foreign.archived = true;
    await page.route("**/api/conversations/search**", (route) =>
      route.fulfill({ json: [foreign] }),
    );

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await expectQuery(editor, "");
    await expect(page.getByRole("button", { name: "Add user filter" })).toHaveCount(0);

    await fillConversationQuery(editor, "foreign");
    await expect(page.locator('[data-conversation-id="foreign"]')).toBeVisible();
    await expect(page.getByRole("button", { name: "Add user filter" })).toHaveCount(0);
    await expect(page.locator(".conversation-participant-badge")).toHaveCount(0);
    await clearConversationQuery(editor);
    await expectQuery(editor, "");
  });

  test("participant UI appears without a current user when a chat has several", async ({
    page,
  }) => {
    await stubConversationList(page, [
      conversation("shared", false, ["alice@example.com", "bob@example.com"]),
      conversation("solo", false, ["alice@example.com"]),
    ]);

    await page.goto("/new");
    await expect(
      page.locator('[data-conversation-id="shared"] .conversation-participant-badge'),
    ).toContainText("2");
    // No current email means nothing to seed the query with.
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await expectQuery(queryEditor(page), "");
    await expect(page.getByRole("button", { name: "Add user filter" })).toBeEnabled();
    await page.getByRole("button", { name: "Group conversations" }).click();
    await expect(page.getByRole("button", { name: "Participants", exact: true })).toBeVisible();
  });

  test("group by participants is offered only for multi-participant lists", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    // Two participants exist across the list, but no conversation has more
    // than one: there is nothing to group by.
    await stubConversationList(page, [
      conversation("mine", false, ["me@example.com"]),
      conversation("other", false, ["other@example.com"]),
    ]);

    await page.goto("/new");
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    await page.getByRole("button", { name: "Group conversations" }).click();
    await expect(page.getByRole("button", { name: "Tags", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Participants", exact: true })).toHaveCount(0);
  });

  test("group by participants buckets whole participant sets, current user first", async ({
    page,
  }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "adam@poyo.co" });
    await stubConversationList(page, [
      conversation("zed-pair", false, ["zed@example.com", "yan@example.com"]),
      conversation("mine", false, ["adam@poyo.co"]),
      // Same set, opposite order and casing: these must land in ONE group.
      conversation("shared", false, ["aaaa@bbb.com", "adam@poyo.co"]),
      conversation("shared-again", false, ["ADAM@POYO.CO", "aaaa@bbb.com"]),
      conversation("alice-pair", false, ["alice@example.com", "bob@example.com"]),
      conversation("none", false, []),
    ]);

    await page.goto("/new");
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    // Drop the seeded current-user filter so every group is on screen.
    await page.getByRole("button", { name: "Search conversations..." }).click();
    const editor = queryEditor(page);
    await expectQuery(editor, "user:adam@poyo.co ");
    await clearConversationQuery(editor);
    await expect(page.locator('[data-conversation-id="none"]')).toBeVisible();

    await page.getByRole("button", { name: "Group conversations" }).click();
    await page.getByRole("button", { name: "Participants", exact: true }).click();

    await expect(page.locator(".conversation-group-label")).toHaveText([
      "adam@poyo.co",
      "aaaa@bbb.com, adam@poyo.co",
      "alice@example.com, bob@example.com",
      "yan@example.com, zed@example.com",
      "Unattributed",
    ]);
    const sharedGroup = page.locator(".conversation-group").filter({
      has: page.locator('[data-conversation-id="shared"]'),
    });
    await expect(sharedGroup).toHaveCount(1);
    await expect(sharedGroup.locator('[data-conversation-id="shared-again"]')).toHaveCount(1);
    await expect(sharedGroup.locator(".conversation-group-label")).toHaveAttribute(
      "title",
      "aaaa@bbb.com, adam@poyo.co",
    );
    await expect(page.locator('[data-conversation-id="shared"]')).toHaveCount(1);
  });

  test("starts expanded even when the only item is a draft", async ({ page }) => {
    await stubConversationList(page, [conversation("draft", true)]);

    await page.goto("/new");

    await expect(page.locator(".drawer")).not.toHaveClass(/collapsed/);
    await expect(page.getByRole("button", { name: "Collapse sidebar" })).toBeVisible();
  });

  test("starts expanded with an untitled header aligned to the app bar", async ({
    page,
  }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await expect(page.getByRole("button", { name: "Collapse sidebar" })).toBeVisible();
    await expect(drawer.locator(".drawer-title")).toHaveCount(0);
    await expect(drawer.getByText("Conversations", { exact: true })).toHaveCount(0);

    const metrics = await page.evaluate(() => {
      function rect(selector: string) {
        const element = document.querySelector<HTMLElement>(selector);
        if (!element) throw new Error(`Missing ${selector}`);
        return element.getBoundingClientRect();
      }
      return {
        drawerHeader: rect(".drawer-header"),
        drawerActions: rect(".drawer-header-actions"),
        chatHeader: rect(".header"),
      };
    });

    expect(metrics.drawerHeader.top).toBe(metrics.chatHeader.top);
    expect(metrics.drawerHeader.bottom).toBe(metrics.chatHeader.bottom);
    // Actions stay pinned to the right edge (inside the header's 1rem padding).
    expect(metrics.drawerHeader.right - metrics.drawerActions.right).toBe(16);

    await page.getByRole("button", { name: "View Archived" }).click();
    await expect(drawer.locator(".drawer-title")).toHaveText("Archived");
  });

  test("manual collapse survives reload with a single conversation", async ({ page }) => {
    await stubConversationList(page, [conversation("only")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(drawer).toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Expand sidebar" }).click();
    await expect(drawer).not.toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).not.toHaveClass(/collapsed/);
  });

  test("manual collapse survives reload with many conversations", async ({ page }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(drawer).toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).toHaveClass(/collapsed/);
  });
});

async function swipe(
  page: Page,
  selector: string,
  start: { x: number; y: number },
  end: { x: number; y: number },
) {
  await page.evaluate(
    ({ selector, start, end }) => {
      const target = document.querySelector(selector);
      if (!(target instanceof Element)) throw new Error(`Missing ${selector}`);

      const makeTouch = ({ x, y }: { x: number; y: number }) =>
        new Touch({
          identifier: 1,
          target,
          clientX: x,
          clientY: y,
          screenX: x,
          screenY: y,
          pageX: x,
          pageY: y,
        });
      const startTouch = makeTouch(start);
      const endTouch = makeTouch(end);

      target.dispatchEvent(
        new TouchEvent("touchstart", {
          bubbles: true,
          cancelable: true,
          touches: [startTouch],
          targetTouches: [startTouch],
          changedTouches: [startTouch],
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchmove", {
          bubbles: true,
          cancelable: true,
          touches: [endTouch],
          targetTouches: [endTouch],
          changedTouches: [endTouch],
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchend", {
          bubbles: true,
          cancelable: true,
          touches: [],
          targetTouches: [],
          changedTouches: [endTouch],
        }),
      );
    },
    { selector, start, end },
  );
}

async function swipePatchDiff(page: Page) {
  await page.evaluate(() => {
    const mainContent = document.querySelector(".main-content");
    if (!(mainContent instanceof HTMLElement)) throw new Error("Missing main content");

    const diffsContainer = document.createElement("diffs-container");
    const shadowRoot = diffsContainer.shadowRoot ?? diffsContainer.attachShadow({ mode: "open" });
    const scrollContainer = document.createElement("div");
    const diffLine = document.createElement("span");
    diffsContainer.style.cssText = "display:block;width:120px";
    scrollContainer.style.cssText = "overflow-x:auto;width:120px";
    diffLine.style.cssText = "display:block;width:320px";
    scrollContainer.append(diffLine);
    shadowRoot.append(scrollContainer);
    mainContent.append(diffsContainer);
    Object.defineProperties(scrollContainer, {
      clientWidth: { value: 120 },
      scrollWidth: { value: 320 },
    });

    if (scrollContainer.scrollWidth <= scrollContainer.clientWidth) {
      throw new Error("Patch diff test fixture is not horizontally scrollable");
    }

    const makeTouch = (x: number, y: number) =>
      new Touch({
        identifier: 1,
        target: diffLine,
        clientX: x,
        clientY: y,
        screenX: x,
        screenY: y,
        pageX: x,
        pageY: y,
      });
    const startTouch = makeTouch(160, 300);
    const endTouch = makeTouch(220, 304);

    diffLine.dispatchEvent(
      new TouchEvent("touchstart", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [startTouch],
        targetTouches: [startTouch],
        changedTouches: [startTouch],
      }),
    );
    diffLine.dispatchEvent(
      new TouchEvent("touchmove", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [endTouch],
        targetTouches: [endTouch],
        changedTouches: [endTouch],
      }),
    );
    diffLine.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [],
        targetTouches: [],
        changedTouches: [endTouch],
      }),
    );
  });
}

test.describe("mobile drawer swipe", () => {
  test.use({
    viewport: { width: 393, height: 851 },
    isMobile: true,
    hasTouch: true,
  });

  test("opens and closes with opposite page swipes", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    const drawer = page.locator(".drawer");
    const input = page.getByTestId("message-input");
    await expect(drawer).not.toHaveClass(/open/);
    await input.focus();
    await expect(input).toBeFocused();

    await swipe(page, ".main-content", { x: 180, y: 300 }, { x: 256, y: 304 });
    await expect(drawer).toHaveClass(/open/);
    await expect(input).not.toBeFocused();

    await swipe(page, ".backdrop", { x: 380, y: 300 }, { x: 328, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);
  });

  test("keeps filter text lowercase-ready", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    await swipe(page, ".main-content", { x: 180, y: 300 }, { x: 256, y: 304 });
    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await searchToggle.click();
    await expect(page.locator(".p-tooltip")).toHaveCount(0);
    await page.getByRole("button", { name: "Add tag filter" }).click();

    const filterText = page.locator(".drawer-search-input");
    await expectQuery(filterText, "tag:");
    await expect(filterText).toHaveAttribute("autocapitalize", "none");
    await expect(filterText).toContainText("tag:");
    await expect(queryToken(filterText, "tag")).toHaveCount(0);
  });

  test("leaves the system edge, short movement, and vertical scrolling alone", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await swipe(page, ".main-content", { x: 12, y: 300 }, { x: 112, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);

    await swipe(page, ".main-content", { x: 160, y: 300 }, { x: 224, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);

    await swipe(page, ".main-content", { x: 64, y: 200 }, { x: 92, y: 300 });
    await expect(drawer).not.toHaveClass(/open/);
  });

  test("leaves horizontal patch diff scrolling alone", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    await swipePatchDiff(page);

    await expect(page.locator(".drawer")).not.toHaveClass(/open/);
  });

  test("does not open while a diff viewer modal is shown", async ({ page }) => {
    const first = conversation("first");
    first.cwd = "/tmp";
    await stubConversationList(page, [first]);
    await page.goto("/new");
    // The shortcut is the app's; a key pressed before it has mounted is lost.
    await expect(page.getByTestId("message-input")).toBeVisible();

    await page.keyboard.press("ControlOrMeta+Shift+D");
    await expect(page.locator(".diff-viewer-overlay")).toBeVisible();

    await swipe(page, ".diff-viewer-header", { x: 180, y: 100 }, { x: 256, y: 104 });

    await expect(page.locator(".drawer")).not.toHaveClass(/open/);
  });
});
