import { test, expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

async function setTags(
  request: APIRequestContext,
  conversationId: string,
  tags: string[],
): Promise<void> {
  const resp = await request.post(`/api/conversation/${conversationId}/tags`, { data: { tags } });
  expect(resp.ok()).toBeTruthy();
}

async function renameConversation(
  request: APIRequestContext,
  conversationId: string,
  slug: string,
): Promise<void> {
  const resp = await request.post(`/api/conversation/${conversationId}/rename`, {
    data: { slug },
  });
  expect(resp.ok()).toBeTruthy();
}

async function getGitRoot(request: APIRequestContext): Promise<string> {
  const resp = await request.get("/api/git/diffs?cwd=.");
  expect(resp.ok()).toBeTruthy();
  const data = await resp.json();
  expect(data.gitRoot).toBeTruthy();
  return data.gitRoot;
}

// Every spec shares one server, so other tests' conversations are in the
// drawer too. Scope assertions to the rows we created by conversation id.
function row(page: Page, conversationId: string) {
  return page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
}

async function openRowActions(page: Page, conversationRow: Locator) {
  await conversationRow.hover();
  await conversationRow.getByRole("button", { name: "Actions" }).click();
  const menu = page.getByTestId("conversation-actions-menu");
  await expect(menu).toBeVisible();
  return menu;
}

async function openDrawer(page: Page): Promise<void> {
  const drawer = page.locator(".drawer");
  if (!(await drawer.evaluate((el) => el.classList.contains("open")))) {
    await page.locator('button[aria-label="Open conversations"]').click();
  }
  await expect(page.locator(".drawer.open")).toBeVisible();
}

const panel = (page: Page) => page.getByTestId("tag-filter-panel");
const option = (page: Page, tag: string) =>
  page.locator(`[data-testid="tag-filter-option"][data-tag="${tag}"]`);
const searchBox = (page: Page) => page.locator(".drawer-search-input");

async function openSearch(page: Page) {
  if ((await searchBox(page).count()) === 0) {
    await page.locator('button[aria-label="Search conversations..."]').click();
  }
  await expect(searchBox(page)).toBeVisible();
  return searchBox(page);
}

// These tests share one server and one drawer, so run them in order rather
// than letting a sibling's tags appear mid-assertion.
test.describe.configure({ mode: "serial" });

test.describe("Tag filter", () => {
  test("tag: opens a dropdown, completes, ANDs, and narrows the offers", async ({
    page,
    request,
  }) => {
    // alpha: tf-alpha, tf-shared   beta: tf-beta, tf-shared   gamma: tf-gamma
    // tf-alpha and tf-beta never co-occur, so tf-beta must not be offered
    // once tf-alpha is selected.
    const alpha = await createConversationViaAPIWithDetails(request, "tag filter alpha", {
      cwd: "/tmp",
    });
    const beta = await createConversationViaAPIWithDetails(request, "tag filter beta", {
      cwd: "/tmp",
    });
    const gamma = await createConversationViaAPIWithDetails(request, "tag filter gamma", {
      cwd: "/tmp",
    });
    await setTags(request, alpha.conversationId, ["tf-alpha", "tf-shared"]);
    await setTags(request, beta.conversationId, ["tf-beta", "tf-shared"]);
    await setTags(request, gamma.conversationId, ["tf-gamma"]);

    await page.goto(`/c/${gamma.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    const search = await openSearch(page);

    // No dropdown until the `tag:` cue is typed.
    await expect(panel(page)).toHaveCount(0);
    await search.fill("tag:");
    await expect(panel(page)).toBeVisible();
    // Counts are the size of the result set each tag would produce.
    await expect(option(page, "tf-shared")).toContainText("2");
    await expect(option(page, "tf-alpha")).toContainText("1");

    // Typing after the prefix narrows the dropdown by substring...
    await search.fill("tag:sha");
    await expect(option(page, "tf-shared")).toBeVisible();
    await expect(option(page, "tf-alpha")).toHaveCount(0);

    // ...and clicking one completes the term in place, leaving a trailing
    // space so the next keystroke starts fresh.
    await option(page, "tf-shared").click();
    await expect(search).toHaveValue("tag:tf-shared ");

    // The list is filtered.
    await expect(row(page, alpha.conversationId)).toBeVisible();
    await expect(row(page, beta.conversationId)).toBeVisible();
    await expect(row(page, gamma.conversationId)).toHaveCount(0);

    // A second tag ANDs, and the offers narrow: tf-gamma never co-occurs with
    // tf-shared, so it is not on offer.
    await search.fill("tag:tf-shared tag:");
    await expect(panel(page)).toBeVisible();
    await expect(option(page, "tf-gamma")).toHaveCount(0);
    await expect(option(page, "tf-shared")).toHaveCount(0); // already selected
    await expect(option(page, "tf-alpha")).toBeVisible();

    await option(page, "tf-alpha").click();
    await expect(row(page, alpha.conversationId)).toBeVisible();
    await expect(row(page, beta.conversationId)).toHaveCount(0);

    // Clearing the search clears the filter with it: one input, one state.
    await search.fill("");
    await expect(row(page, alpha.conversationId)).toBeVisible();
    await expect(row(page, beta.conversationId)).toBeVisible();
    await expect(row(page, gamma.conversationId)).toBeVisible();
  });

  test("keyboard drives the dropdown, and Escape peels one layer at a time", async ({
    page,
    request,
  }) => {
    const kb = await createConversationViaAPIWithDetails(request, "keyboard tag conversation", {
      cwd: "/tmp",
    });
    await setTags(request, kb.conversationId, ["kb-only"]);

    await page.goto(`/c/${kb.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);

    await search.fill("tag:kb-on");
    await expect(option(page, "kb-only")).toBeVisible();
    // Enter takes the highlighted entry.
    await page.keyboard.press("Enter");
    await expect(search).toHaveValue("tag:kb-only ");
    await expect(panel(page)).toHaveCount(0);

    // Escape closes the dropdown first, keeping the query intact...
    await search.fill("tag:kb-only tag:");
    await expect(panel(page)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(panel(page)).toHaveCount(0);
    await expect(search).toHaveValue("tag:kb-only tag:");
    // ...and the dropdown does not spring back until the term changes.
    await expect(panel(page)).toHaveCount(0);

    // A second Escape clears the query, a third closes the search box.
    await page.keyboard.press("Escape");
    await expect(search).toHaveValue("");
    await page.keyboard.press("Escape");
    await expect(searchBox(page)).toHaveCount(0);
  });

  test("tag terms and free text are independent predicates", async ({ page, request }) => {
    const hit = await createConversationViaAPIWithDetails(request, "searchable kumquat marker", {
      cwd: "/tmp",
    });
    const other = await createConversationViaAPIWithDetails(request, "searchable kumquat other", {
      cwd: "/tmp",
    });
    await setTags(request, hit.conversationId, ["sf-keep"]);
    await setTags(request, other.conversationId, ["sf-drop"]);

    await page.goto(`/c/${hit.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);

    // Text and tag in one query: both must hold.
    await search.fill("kumquat tag:sf-keep ");
    await expect(row(page, hit.conversationId)).toBeVisible();
    await expect(row(page, other.conversationId)).toHaveCount(0);

    // Order does not matter -- they are ANDed predicates, not a pipeline.
    await search.fill("tag:sf-keep kumquat");
    await expect(row(page, hit.conversationId)).toBeVisible();
    await expect(row(page, other.conversationId)).toHaveCount(0);

    // A search that matches nothing is a search miss, NOT a filter miss: the
    // filter is only to blame when clearing it would bring rows back.
    await search.fill("zzz-no-such-conversation-zzz tag:sf-keep ");
    await expect(page.getByTestId("tag-filter-empty")).toHaveCount(0);
    await expect(page.locator(".drawer-empty-state")).toContainText("No matching conversations");

    // A search that DOES match, whose hits the tag then removes, IS a filter
    // miss -- and its clear action keeps the text but drops the tags.
    await search.fill("kumquat marker tag:sf-drop ");
    await expect(page.getByTestId("tag-filter-empty")).toBeVisible();
    await page.getByTestId("tag-filter-empty-clear").click();
    // The free text survives; only the tag terms are dropped.
    await expect(search).toHaveValue("kumquat marker ");
    await expect(row(page, hit.conversationId)).toBeVisible();
  });

  test("conversation row actions open from one menu button", async ({ page, request }) => {
    const conversation = await createConversationViaAPIWithDetails(request, "row actions menu", {
      cwd: "/tmp",
    });
    const inactiveConversation = await createConversationViaAPIWithDetails(
      request,
      "inactive row actions menu",
      { cwd: "/tmp" },
    );

    await page.goto(`/c/${conversation.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    const conversationRow = row(page, conversation.conversationId);
    await expect(conversationRow.getByRole("button", { name: "Rename" })).toHaveCount(0);
    await expect(conversationRow.getByRole("button", { name: "Edit tags" })).toHaveCount(0);
    await expect(conversationRow.getByRole("button", { name: "Archive" })).toHaveCount(0);

    const header = conversationRow.locator(".drawer-conversation-header-row");
    const trigger = header.getByRole("button", { name: "Actions" });
    const inactiveTrigger = row(page, inactiveConversation.conversationId)
      .locator(".drawer-conversation-header-row")
      .getByRole("button", { name: "Actions" });
    await expect(trigger).toBeVisible();
    await expect(trigger.locator("svg")).toBeVisible();
    const headerOverflowTrigger = page
      .locator(".chat-overflow-menu-wrapper")
      .getByRole("button", { name: "More options" });
    await expect(headerOverflowTrigger).toBeVisible();
    expect(await trigger.locator("svg path").getAttribute("d")).toBe(
      await headerOverflowTrigger.locator("svg path").getAttribute("d"),
    );
    await expect(inactiveTrigger).toBeVisible();
    for (const button of [trigger, inactiveTrigger]) {
      const styles = await button.evaluate((el) => {
        const style = getComputedStyle(el);
        return {
          opacity: style.opacity,
          backgroundColor: style.backgroundColor,
          borderRadius: Number.parseFloat(style.borderRadius),
          borderWidth: Number.parseFloat(style.borderWidth),
          boxShadow: style.boxShadow,
          width: Number.parseFloat(style.width),
          height: Number.parseFloat(style.height),
        };
      });
      expect(styles.opacity).toBe("1");
      expect(styles.backgroundColor).toBe("rgba(0, 0, 0, 0)");
      expect(styles.borderWidth).toBe(0);
      expect(styles.boxShadow).toBe("none");
      expect(styles.width).toBe(28);
      expect(styles.height).toBe(28);
      expect(styles.borderRadius).toBe(0);
    }
    await inactiveTrigger.scrollIntoViewIfNeeded();
    const inactiveBox = await inactiveTrigger.boundingBox();
    expect(inactiveBox).not.toBeNull();
    await inactiveTrigger.hover({ position: { x: 2, y: 2 } });
    await expect(page.locator(".p-tooltip-text")).toHaveText("Actions");
    expect(await inactiveTrigger.boundingBox()).toEqual(inactiveBox);
    await inactiveTrigger.hover({ position: { x: 26, y: 26 } });
    expect(await inactiveTrigger.boundingBox()).toEqual(inactiveBox);
    await trigger.focus();
    await page.keyboard.press("Tab");
    await page.keyboard.press("Shift+Tab");
    await expect(trigger).toBeFocused();
    const activeFocus = await trigger.evaluate((el) => {
      const style = getComputedStyle(el);
      return { color: style.outlineColor, width: Number.parseFloat(style.outlineWidth) };
    });
    expect(activeFocus.color).toBe("rgb(255, 255, 255)");
    expect(activeFocus.width).toBeGreaterThanOrEqual(2);
    await expect(
      conversationRow.locator(".conversation-meta").getByRole("button", { name: "Actions" }),
    ).toHaveCount(0);

    const menu = await openRowActions(page, conversationRow);
    await expect(trigger).toHaveAttribute("aria-expanded", "true");
    await expect(menu.getByRole("menuitem")).toHaveText(["Archive", "Rename", "Edit tags"]);
    const menuList = menu.getByRole("menu");
    await expect(menuList).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(menuList).toHaveAttribute("aria-activedescendant", /.+/);

    const triggerBox = await trigger.boundingBox();
    const menuBox = await menu.boundingBox();
    expect(triggerBox).not.toBeNull();
    expect(menuBox).not.toBeNull();
    expect(menuBox!.y).toBeGreaterThan(triggerBox!.y + triggerBox!.height / 2);

    await page.keyboard.press("Escape");
    await expect(menu).toBeHidden();
    await expect(trigger).toHaveAttribute("aria-expanded", "false");

    await trigger.click();
    await menu.getByRole("menuitem", { name: "Archive" }).click();
    await expect(conversationRow).toHaveCount(0);
  });

  test("renaming a search hit refetches its FTS membership", async ({ page, request }) => {
    const conversation = await createConversationViaAPIWithDetails(request, "rename search hit", {
      cwd: "/tmp",
    });
    const suffix = conversation.conversationId.slice(0, 8);
    const oldSlug = `fts-old-${suffix}`;
    const newSlug = `fts-new-${suffix}`;
    await renameConversation(request, conversation.conversationId, oldSlug);

    await page.goto(`/c/${oldSlug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);
    await search.fill(oldSlug);

    const result = row(page, conversation.conversationId);
    await expect(result).toBeVisible();
    const actionsMenu = await openRowActions(page, result);
    await actionsMenu.getByRole("menuitem", { name: "Rename" }).click();
    const input = result.locator(".drawer-rename-input");
    await input.fill(newSlug);
    await input.press("Enter");

    await expect(result).toHaveCount(0);
    await search.fill(newSlug);
    await expect(row(page, conversation.conversationId)).toBeVisible();
  });

  test("row tag chips write into the query and compose with git-repo grouping", async ({
    page,
    request,
  }) => {
    const gitRoot = await getGitRoot(request);
    const inRepo = await createConversationViaAPIWithDetails(request, "chip filter in repo", {
      cwd: gitRoot,
    });
    const outOfRepo = await createConversationViaAPIWithDetails(request, "chip filter elsewhere", {
      cwd: "/tmp",
    });
    await setTags(request, inRepo.conversationId, ["chip-tag"]);
    await setTags(request, outOfRepo.conversationId, ["chip-other"]);

    await page.goto(`/c/${inRepo.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    // Discovery path: click the #chip-tag chip on the row itself. It opens the
    // search box and writes the term, so the mechanism is immediately visible.
    const chip = row(page, inRepo.conversationId)
      .getByTestId("conversation-tag-chip")
      .filter({ hasText: "chip-tag" });
    await expect(chip).toBeVisible();
    await chip.click();

    await expect(searchBox(page)).toHaveValue("tag:chip-tag ");
    await expect(row(page, inRepo.conversationId)).toBeVisible();
    await expect(row(page, outOfRepo.conversationId)).toHaveCount(0);
    // Clicking a chip must not also navigate away from the current chat.
    await expect(page).toHaveURL(new RegExp(`/c/${inRepo.slug}$`));
    await expect(chip).toHaveAttribute("aria-pressed", "true");

    // A tag-only query is a filter, not a search: the list keeps its grouping.
    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Git Repo" }).click();
    const group = page.locator(".conversation-group").filter({
      has: row(page, inRepo.conversationId),
    });
    await expect(group).toHaveCount(1);
    await expect(group.locator(".conversation-group-label")).not.toHaveText("Other");

    // Clicking the same chip again removes its term.
    await chip.click();
    await expect(searchBox(page)).toHaveValue("");
    await expect(row(page, outOfRepo.conversationId)).toBeVisible();
  });

  test("a tag containing a space is quoted, and round-trips", async ({ page, request }) => {
    // The server's normalizeTags allows spaces, so `tag:in progress` unquoted
    // would parse as the tag `in` plus the word `progress`.
    const spaced = await createConversationViaAPIWithDetails(request, "spaced tag conversation", {
      cwd: "/tmp",
    });
    const plain = await createConversationViaAPIWithDetails(request, "spaced tag other", {
      cwd: "/tmp",
    });
    await setTags(request, spaced.conversationId, ["sp tag"]);
    await setTags(request, plain.conversationId, ["sp-plain"]);

    await page.goto(`/c/${spaced.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    // The chip writes the quoted term…
    const chip = row(page, spaced.conversationId)
      .getByTestId("conversation-tag-chip")
      .filter({ hasText: "sp tag" });
    await chip.click();
    await expect(searchBox(page)).toHaveValue('tag:"sp tag" ');
    await expect(row(page, spaced.conversationId)).toBeVisible();
    await expect(row(page, plain.conversationId)).toHaveCount(0);

    // …and clicking it again removes the whole quoted term.
    await chip.click();
    await expect(searchBox(page)).toHaveValue("");

    // Completing from the dropdown quotes too, and typing inside an open
    // quote — space included — keeps narrowing instead of committing.
    const search = searchBox(page);
    await search.fill('tag:"sp t');
    await expect(option(page, "sp tag")).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(search).toHaveValue('tag:"sp tag" ');
    await expect(row(page, spaced.conversationId)).toBeVisible();
    await expect(row(page, plain.conversationId)).toHaveCount(0);

    // A tag containing a quote mark is backslash-escaped, and round-trips
    // through its chip the same way.
    const quoted = 'sp" quote';
    await setTags(request, spaced.conversationId, ["sp tag", quoted]);
    await search.fill("");
    const quoteChip = row(page, spaced.conversationId).locator(
      `[data-testid="conversation-tag-chip"][data-tag='${quoted}']`,
    );
    await quoteChip.click();
    await expect(searchBox(page)).toHaveValue('tag:"sp\\" quote" ');
    await expect(row(page, spaced.conversationId)).toBeVisible();
    await expect(row(page, plain.conversationId)).toHaveCount(0);
    await quoteChip.click();
    await expect(searchBox(page)).toHaveValue("");
  });

  test("is:untagged filters to untagged, in its own namespace", async ({ page, request }) => {
    const tagged = await createConversationViaAPIWithDetails(request, "untagged probe tagged", {
      cwd: "/tmp",
    });
    const bare = await createConversationViaAPIWithDetails(request, "untagged probe bare", {
      cwd: "/tmp",
    });
    // A tag literally named "untagged" — the collision that `is:` avoids.
    const literal = await createConversationViaAPIWithDetails(request, "untagged probe literal", {
      cwd: "/tmp",
    });
    await setTags(request, tagged.conversationId, ["ut-real"]);
    await setTags(request, bare.conversationId, []);
    await setTags(request, literal.conversationId, ["untagged"]);

    await page.goto(`/c/${bare.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);

    // Offered in the dropdown alongside the tags, and completing it inserts
    // the whole `is:untagged` token.
    await search.fill("tag:");
    await expect(page.getByTestId("tag-filter-panel")).toBeVisible();
    // Matched by test id, not by text: this very test creates a tag named
    // "untagged", so "the option reading Untagged" is genuinely ambiguous.
    await page.getByTestId("tag-filter-untagged-option").click();
    await expect(search).toHaveValue("is:untagged ");

    await expect(row(page, bare.conversationId)).toBeVisible();
    await expect(row(page, tagged.conversationId)).toHaveCount(0);
    // The conversation tagged #untagged HAS a tag, so it is excluded.
    await expect(row(page, literal.conversationId)).toHaveCount(0);

    // ...and that tag stays addressable on its own terms, meaning the
    // opposite: only the conversation carrying it.
    await search.fill("tag:untagged ");
    await expect(row(page, literal.conversationId)).toBeVisible();
    await expect(row(page, bare.conversationId)).toHaveCount(0);

    // Contradictory query: nothing has a tag and no tags.
    await search.fill("is:untagged tag:ut-real ");
    await expect(page.getByTestId("tag-filter-empty")).toBeVisible();
    // Its clear action drops both terms.
    await page.getByTestId("tag-filter-empty-clear").click();
    await expect(search).toHaveValue("");
  });

  test("tag groups are ordered by first tag, then second", async ({ page, request }) => {
    // The four sets from the ordering rule: `#gо-a #gо-b`, `#gо-b`,
    // `#gо-b #gо-c`, `#gо-d`-style. Created in a deliberately wrong order so
    // the assertion cannot pass by accident of creation time.
    const bd = await createConversationViaAPIWithDetails(request, "order tags bd", { cwd: "/tmp" });
    const b = await createConversationViaAPIWithDetails(request, "order tags b", { cwd: "/tmp" });
    const ab = await createConversationViaAPIWithDetails(request, "order tags ab", { cwd: "/tmp" });
    const bc = await createConversationViaAPIWithDetails(request, "order tags bc", { cwd: "/tmp" });
    await setTags(request, bd.conversationId, ["ord-b", "ord-d"]);
    await setTags(request, b.conversationId, ["ord-b"]);
    await setTags(request, ab.conversationId, ["ord-a", "ord-b"]);
    await setTags(request, bc.conversationId, ["ord-b", "ord-c"]);

    await page.goto(`/c/${b.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Tags", exact: true }).click();

    // Scope to our own groups: the shared server has other tagged rows.
    const ours = page.locator(".conversation-group").filter({ hasText: /#ord-/ });
    await expect(ours.locator(".conversation-group-label")).toHaveText([
      "#ord-a #ord-b",
      "#ord-b",
      "#ord-b #ord-c",
      "#ord-b #ord-d",
    ]);
  });

  test("group by tag buckets whole tag sets, with untagged last", async ({ page, request }) => {
    const both = await createConversationViaAPIWithDetails(request, "group tags both", {
      cwd: "/tmp",
    });
    const bothAgain = await createConversationViaAPIWithDetails(request, "group tags both again", {
      cwd: "/tmp",
    });
    const single = await createConversationViaAPIWithDetails(request, "group tags single", {
      cwd: "/tmp",
    });
    const none = await createConversationViaAPIWithDetails(request, "group tags none", {
      cwd: "/tmp",
    });
    // Same set, opposite order: these must land in ONE group.
    await setTags(request, both.conversationId, ["gt-red", "gt-blue"]);
    await setTags(request, bothAgain.conversationId, ["gt-blue", "gt-red"]);
    await setTags(request, single.conversationId, ["gt-red"]);
    await setTags(request, none.conversationId, []);

    await page.goto(`/c/${both.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Tags", exact: true }).click();

    // Order-insensitive: one group holds both conversations, labelled with the
    // sorted set.
    const pairGroup = page.locator(".conversation-group").filter({
      has: row(page, both.conversationId),
    });
    await expect(pairGroup).toHaveCount(1);
    await expect(pairGroup.locator(".conversation-group-label")).toHaveText("#gt-blue #gt-red");
    // The narrow drawer truncates long labels, so the hover title carries the
    // full readable tag set (not the internal NUL-joined key).
    await expect(pairGroup.locator(".conversation-group-label")).toHaveAttribute(
      "title",
      "#gt-blue #gt-red",
    );
    await expect(
      pairGroup.locator(`[data-conversation-id="${bothAgain.conversationId}"]`),
    ).toHaveCount(1);

    // A subset is its own group, not folded into the pair.
    const singleGroup = page.locator(".conversation-group").filter({
      has: row(page, single.conversationId),
    });
    await expect(singleGroup.locator(".conversation-group-label")).toHaveText("#gt-red");
    await expect(
      singleGroup.locator(`[data-conversation-id="${both.conversationId}"]`),
    ).toHaveCount(0);

    // Untagged conversations collect under their own heading.
    const noneGroup = page.locator(".conversation-group").filter({
      has: row(page, none.conversationId),
    });
    await expect(noneGroup.locator(".conversation-group-label")).toHaveText("Untagged");

    // Every conversation appears exactly once overall -- the property a
    // tag-per-group layout would break.
    await expect(page.locator(`[data-conversation-id="${both.conversationId}"]`)).toHaveCount(1);

    // Grouping and filtering are orthogonal: they compose.
    const search = await openSearch(page);
    await search.fill("tag:gt-blue ");
    await expect(row(page, both.conversationId)).toBeVisible();
    await expect(row(page, single.conversationId)).toHaveCount(0);
    await expect(row(page, none.conversationId)).toHaveCount(0);
    await expect(pairGroup.locator(".conversation-group-label")).toHaveText("#gt-blue #gt-red");
  });

  test("the row tag editor offers a dropdown of matching tags", async ({ page, request }) => {
    // `ac-terminal-work` and `workbench` exist on donors; typing `work` on
    // target should offer both (substring match), ranked prefix-first, with
    // Enter committing the highlighted offer.
    const donor = await createConversationViaAPIWithDetails(request, "autocomplete donor", {
      cwd: "/tmp",
    });
    const donor2 = await createConversationViaAPIWithDetails(request, "autocomplete donor 2", {
      cwd: "/tmp",
    });
    const target = await createConversationViaAPIWithDetails(request, "autocomplete target", {
      cwd: "/tmp",
    });
    await setTags(request, donor.conversationId, ["ac-terminal-work", "workbench"]);
    // `spare-tag` is never added to target, so its offer keeps the dropdown
    // openable at the end of the test.
    await setTags(request, donor2.conversationId, ["ac-terminal-work", "spare-tag"]);

    await page.goto(`/c/${target.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    const targetRow = row(page, target.conversationId);
    const actionsMenu = await openRowActions(page, targetRow);
    await actionsMenu.getByRole("menuitem", { name: "Edit tags" }).click();
    const input = targetRow.locator(".conversation-tag-inline-input");
    await expect(input).toBeFocused();

    // Focusing the empty input offers the whole vocabulary (shared with other
    // tests' tags, so only presence is asserted here; ranking is below).
    const menu = page.getByTestId("tag-editor-menu");
    await expect(menu).toBeVisible();
    const options = menu.getByTestId("tag-editor-option");
    await expect(menu.locator('[data-tag="ac-terminal-work"]')).toBeVisible();

    // A mid-word substring matches; the prefix hit ranks first even though
    // the mid-string one is more used.
    await input.pressSequentially("work");
    await expect(options).toHaveCount(2);
    await expect(options.nth(0)).toHaveAttribute("data-tag", "workbench");
    await expect(options.nth(1)).toHaveAttribute("data-tag", "ac-terminal-work");

    // Arrow keys move the highlight; Enter commits the highlighted offer.
    await input.press("ArrowDown");
    await input.press("Enter");
    await expect(
      targetRow.locator(".conversation-tag").filter({ hasText: "ac-terminal-work" }),
    ).toBeVisible();
    // The editor stays open, cleared and focused, for the next tag.
    await expect(input).toBeFocused();
    await expect(input).toHaveValue("");

    // The tag now on the row is excluded from its own suggestions.
    await input.pressSequentially("work");
    await expect(options).toHaveCount(1);
    await expect(options.first()).toHaveAttribute("data-tag", "workbench");

    // Clicking an offer commits it too.
    await options.first().click();
    await expect(
      targetRow.locator(".conversation-tag").filter({ hasText: "workbench" }),
    ).toBeVisible();

    // Text matching nothing closes the menu; Enter saves it literally.
    await input.pressSequentially("brand-new-tag");
    await expect(menu).toHaveCount(0);
    await input.press("Enter");
    await expect(
      targetRow.locator(".conversation-tag").filter({ hasText: "brand-new-tag" }),
    ).toBeVisible();

    // Escape peels one layer: first the open menu, then the editor.
    await expect(menu).toBeVisible(); // spare-tag is still on offer
    await input.press("Escape");
    await expect(menu).toHaveCount(0);
    await expect(input).toBeFocused();
    await input.press("Escape");
    await expect(targetRow.locator(".conversation-tag-inline-input")).toHaveCount(0);
  });

  test("the remove button on an overlong tag stays clickable", async ({ page, request }) => {
    // A tag longer than the chip's max-width used to push the remove button
    // into the chip's hidden overflow, where it could not be clicked.
    const long = "overlong-" + "x".repeat(120);
    const conv = await createConversationViaAPIWithDetails(request, "overlong tag removal", {
      cwd: "/tmp",
    });
    await setTags(request, conv.conversationId, [long]);

    await page.goto(`/c/${conv.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    const convRow = row(page, conv.conversationId);
    const actionsMenu = await openRowActions(page, convRow);
    await actionsMenu.getByRole("menuitem", { name: "Edit tags" }).click();
    await convRow.locator(`button[aria-label="Remove tag ${long}"]`).click();
    await expect(convRow.locator(".conversation-tag-removable")).toHaveCount(0);
  });
});
