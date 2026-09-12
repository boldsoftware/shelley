import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPI, withTempDir } from "./helpers";

const menuFor = (page: Page) => page.getByTestId("file-completion-menu");

async function openConversation(
  page: Page,
  request: APIRequestContext,
  cwd: string,
): Promise<Locator> {
  const slug = await createConversationViaAPI(request, "Hello", { cwd });
  await page.goto(`/c/${slug}`);
  const input = page.getByTestId("message-input");
  await expect(input).toBeVisible({ timeout: 30000 });
  return input;
}

async function setComposer(input: Locator, value: string, caret = value.length): Promise<void> {
  await input.focus();
  await input.evaluate(
    (element, next) => {
      const textarea = element as HTMLTextAreaElement;
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLTextAreaElement.prototype,
        "value",
      )?.set;
      if (!setter) throw new Error("textarea value setter unavailable");
      setter.call(textarea, next.value);
      textarea.setSelectionRange(next.caret, next.caret);
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    },
    { value, caret },
  );
}

function findFilesResponse(searchDir: string, query: string, paths: string[]) {
  return {
    dir: searchDir,
    search_dir: searchDir,
    query,
    match_query: query,
    matches: paths.map((path) => ({ path })),
    total: paths.length,
    truncated: false,
  };
}

test.describe("@ filename completion", () => {
  test("shows an accessible file menu and inserts an absolute JSON string", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-", async (cwd) => {
      mkdirSync(join(cwd, "src"));
      writeFileSync(join(cwd, "src", "alpha.ts"), "export const alpha = 1;\n");

      const input = await openConversation(page, request, cwd);
      await setComposer(input, "Open @alpha");

      const menu = menuFor(page);
      await expect(menu).toBeVisible({ timeout: 10000 });
      await expect(menu).toHaveAttribute("role", "listbox");
      await expect(menu).toHaveAttribute("aria-label", "Files and folders");
      await expect(menu.getByRole("option", { name: "src/alpha.ts", exact: true })).toBeVisible();

      await input.press("Enter");
      const expected = `Open ${JSON.stringify(join(cwd, "src", "alpha.ts"))} `;
      await expect(input).toHaveValue(expected);
      expect(await input.inputValue()).not.toContain("@");
      await expect(menu).toHaveCount(0);
      await expect(page.getByTestId("message-attachments")).toHaveCount(0);
    });
  });

  test("uses the selected cwd on /new and supports quoted queries with spaces", async ({
    page,
  }) => {
    await withTempDir("shelley completion spaced-", async (cwd) => {
      writeFileSync(join(cwd, "My Document.md"), "notes\n");
      await page.addInitScript(
        (selectedCwd) => localStorage.setItem("shelley_selected_cwd", selectedCwd),
        cwd,
      );
      await page.goto("/new");

      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });
      await setComposer(input, 'Read @"My Doc');

      const option = menuFor(page).getByRole("option", {
        name: "My Document.md",
        exact: true,
      });
      await expect(option).toBeVisible({ timeout: 10000 });
      await input.press("Tab");

      await expect(input).toHaveValue(`Read ${JSON.stringify(join(cwd, "My Document.md"))} `);
      await expect(page.getByTestId("message-attachments")).toHaveCount(0);
    });
  });

  test("replaces a token at a middle cursor without disturbing surrounding text", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-middle-", async (cwd) => {
      writeFileSync(join(cwd, "alpha.md"), "alpha\n");
      const input = await openConversation(page, request, cwd);
      const draft = "before @alphabet after";
      await setComposer(input, draft, draft.indexOf("@alp") + "@alp".length);

      const option = menuFor(page).getByRole("option", { name: "alpha.md", exact: true });
      await expect(option).toBeVisible({ timeout: 10000 });
      await option.click();
      await expect(input).toBeFocused();

      await expect(input).toHaveValue(`before ${JSON.stringify(join(cwd, "alpha.md"))} after`);
      expect(await input.evaluate((el) => (el as HTMLTextAreaElement).selectionStart)).toBe(
        `before ${JSON.stringify(join(cwd, "alpha.md"))}`.length,
      );
    });
  });

  test("requires a start or whitespace boundary and stops at unquoted spaces", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-boundary-", async (cwd) => {
      writeFileSync(join(cwd, "alpha.md"), "alpha\n");
      const input = await openConversation(page, request, cwd);
      const menu = menuFor(page);

      await setComposer(input, "email foo@alpha");
      await expect(menu).toHaveCount(0);

      await setComposer(input, "prefix@alpha");
      await expect(menu).toHaveCount(0);

      await setComposer(input, "@alpha");
      await expect(menu.getByRole("option", { name: "alpha.md", exact: true })).toBeVisible({
        timeout: 10000,
      });

      await setComposer(input, "look @alpha later");
      await expect(menu).toHaveCount(0);
    });
  });

  test("arrow selection wraps and resolves paths against response.search_dir", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-arrows-", async (cwd) => {
      const searchDir = join(cwd, "re-rooted scope");
      const paths = ["first.txt", "second.txt", "third.txt"];
      await page.route("**/api/find-files?*", async (route) => {
        const query = new URL(route.request().url()).searchParams.get("q") ?? "";
        await route.fulfill({ json: findFilesResponse(searchDir, query, paths) });
      });

      const input = await openConversation(page, request, cwd);
      await setComposer(input, "@pick");
      await expect(menuFor(page).getByRole("option")).toHaveCount(3);

      await input.press("ArrowUp");
      await input.press("Enter");
      await expect(input).toHaveValue(`${JSON.stringify(join(searchDir, "third.txt"))} `);

      await setComposer(input, "@pick");
      await expect(menuFor(page).getByRole("option")).toHaveCount(3);
      await input.press("ArrowUp");
      await input.press("ArrowDown");
      await input.press("Tab");
      await expect(input).toHaveValue(`${JSON.stringify(join(searchDir, "first.txt"))} `);
    });
  });

  test("Escape dismisses without blur and Shift+Enter still inserts a newline", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-escape-", async (cwd) => {
      writeFileSync(join(cwd, "alpha.md"), "alpha\n");
      const input = await openConversation(page, request, cwd);
      await setComposer(input, "@alpha");
      await expect(menuFor(page)).toBeVisible({ timeout: 10000 });

      await input.press("Escape");
      await expect(menuFor(page)).toHaveCount(0);
      await expect(input).toBeFocused();
      await expect(input).toHaveValue("@alpha");

      await input.press("Shift+Enter");
      await expect(input).toHaveValue("@alpha\n");
    });
  });

  test("a superseded query cannot replace the current results", async ({ page, request }) => {
    await withTempDir("shelley-completion-stale-", async (cwd) => {
      let releaseSlow!: () => void;
      let markSlowRequested!: () => void;
      let markSlowSettled!: () => void;
      const slowReleased = new Promise<void>((resolve) => (releaseSlow = resolve));
      const slowRequested = new Promise<void>((resolve) => (markSlowRequested = resolve));
      const slowSettled = new Promise<void>((resolve) => (markSlowSettled = resolve));

      await page.route("**/api/find-files?*", async (route) => {
        const query = new URL(route.request().url()).searchParams.get("q") ?? "";
        if (query === "slow") {
          markSlowRequested();
          await slowReleased;
          await route
            .fulfill({ json: findFilesResponse(cwd, query, ["slow.txt"]) })
            .catch(() => {});
          markSlowSettled();
          return;
        }
        if (query === "fresh") {
          await route.fulfill({ json: findFilesResponse(cwd, query, ["fresh.txt"]) });
          return;
        }
        await route.continue();
      });

      const input = await openConversation(page, request, cwd);
      await setComposer(input, "@slow");
      await slowRequested;
      await setComposer(input, "@fresh");

      const options = menuFor(page).getByRole("option");
      await expect(options).toHaveCount(1);
      await expect(options.first()).toHaveText("fresh.txt");

      releaseSlow();
      await slowSettled;
      await expect(options).toHaveCount(1);
      await expect(options.first()).toHaveText("fresh.txt");
    });
  });

  test("settled empty and error states leave normal composer keys available", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-empty-", async (cwd) => {
      await page.route("**/api/find-files?*", async (route) => {
        const query = new URL(route.request().url()).searchParams.get("q") ?? "";
        if (query === "none") {
          await route.fulfill({ json: findFilesResponse(cwd, query, []) });
          return;
        }
        if (query === "broken") {
          await route.fulfill({
            status: 503,
            contentType: "text/plain",
            body: "completion unavailable",
          });
          return;
        }
        await route.continue();
      });

      const input = await openConversation(page, request, cwd);
      const menu = menuFor(page);

      await setComposer(input, "echo: tab @none");
      await expect(menu.getByRole("status")).toHaveText("No matching files or folders", {
        timeout: 10000,
      });
      await expect(menu.getByRole("option")).toHaveCount(0);
      await input.press("Tab");
      await expect(input).not.toBeFocused();
      await expect(input).toHaveValue("echo: tab @none");

      await setComposer(input, "echo: newline @none");
      await expect(menu.getByRole("status")).toHaveText("No matching files or folders", {
        timeout: 10000,
      });
      await input.press("Shift+Enter");
      await expect(input).toHaveValue("echo: newline @none\n");

      await setComposer(input, "echo: sent @broken");
      await expect(menu.getByRole("status")).toHaveText(
        "Failed to find files: completion unavailable",
        { timeout: 10000 },
      );
      await expect(menu.getByRole("option")).toHaveCount(0);
      await input.press("Control+Enter");

      await expect(input).toHaveValue("");
      await expect(page.getByText("echo: sent @broken", { exact: true })).toBeVisible({
        timeout: 30000,
      });
      await expect(
        page.locator(".messages-container").getByText("sent @broken", { exact: true }),
      ).toBeVisible({ timeout: 30000 });
    });
  });

  test("a draft cwd pick reaches completion before its persistence echo", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-completion-draft-cwd-", async (root) => {
      const oldCwd = join(root, "old");
      const newCwd = join(root, "new");
      mkdirSync(oldCwd);
      mkdirSync(newCwd);
      writeFileSync(join(newCwd, "draft-target.md"), "target\n");

      const draftResponse = await request.post("/api/conversations/draft", {
        data: { draft: "unfinished draft", model: "predictable", cwd: oldCwd },
      });
      expect(draftResponse.status()).toBe(201);
      const draft = (await draftResponse.json()) as { conversation_id: string };

      await page.addInitScript(
        (selectedCwd) => localStorage.setItem("shelley_selected_cwd", selectedCwd),
        oldCwd,
      );

      let releaseUpdate!: () => void;
      let markUpdateRequested!: () => void;
      let markUpdateSettled!: () => void;
      const updateReleased = new Promise<void>((resolve) => (releaseUpdate = resolve));
      const updateRequested = new Promise<void>((resolve) => (markUpdateRequested = resolve));
      const updateSettled = new Promise<void>((resolve) => (markUpdateSettled = resolve));
      await page.route(`**/api/conversation/${draft.conversation_id}/draft`, async (route) => {
        if (route.request().method() !== "PUT") {
          await route.continue();
          return;
        }
        const fields = route.request().postDataJSON() as { cwd?: string };
        if (fields.cwd !== newCwd) {
          await route.continue();
          return;
        }
        markUpdateRequested();
        await updateReleased;
        await route.continue().catch(() => {});
        markUpdateSettled();
      });

      await page.goto(`/c/${draft.conversation_id}`);
      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });
      await expect(input).toHaveValue("unfinished draft");

      await page.locator(".status-field-cwd .status-chip:visible").click();
      const picker = page.locator(".modal.directory-picker-modal");
      await expect(picker).toBeVisible();
      await picker.locator(".directory-picker-input").fill(`${newCwd}/`);
      await expect(picker.locator(".directory-picker-current-path")).toHaveText(newCwd);

      await picker.getByRole("button", { name: "Select" }).click();
      await updateRequested;

      try {
        const completionRequest = page.waitForRequest((candidate) => {
          const url = new URL(candidate.url());
          return url.pathname === "/api/find-files" && url.searchParams.get("q") === "draft-target";
        });
        await setComposer(input, "Use @draft-target");
        const findRequest = await completionRequest;
        expect(new URL(findRequest.url()).searchParams.get("dir")).toBe(newCwd);
        await expect(
          menuFor(page).getByRole("option", { name: "draft-target.md", exact: true }),
        ).toBeVisible({ timeout: 10000 });
      } finally {
        releaseUpdate();
        await updateSettled;
      }
    });
  });
});

test.describe("@ folder completion", () => {
  test("lists files and empty folders together and inserts the folder with Tab", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-folder-completion-", async (cwd) => {
      mkdirSync(join(cwd, "My Notes"));
      writeFileSync(join(cwd, "My Notes.md"), "file contents must not be attached\n");
      const input = await openConversation(page, request, cwd);
      const contentRequests: string[] = [];
      page.on("request", (req) => {
        if (/\/api\/(read-file|upload)(?:\?|$)/.test(req.url())) contentRequests.push(req.url());
      });
      await setComposer(input, 'Read @"My Notes');
      const menu = menuFor(page);
      const folder = menu.getByRole("option", { name: "My Notes/ (folder)", exact: true });
      await expect(folder).toBeVisible();
      await expect(folder).toHaveAttribute("data-kind", "folder");
      await expect(menu.getByRole("option", { name: "My Notes.md", exact: true })).toBeVisible();
      // Hover changes the highlight without taking focus from the textarea.
      await folder.hover();
      await input.press("Tab");
      await expect(input).toHaveValue(`Read ${JSON.stringify(join(cwd, "My Notes") + "/")} `);
      await expect(page.getByTestId("message-attachments")).toHaveCount(0);
      expect(contentRequests).toEqual([]);
    });
  });

  test("a trailing slash browses nested folders and mouse insertion preserves text", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-folder-nested-", async (cwd) => {
      mkdirSync(join(cwd, "docs", "Final Notes"), { recursive: true });
      writeFileSync(join(cwd, "docs", "readme.md"), "readme\n");
      const input = await openConversation(page, request, cwd);
      const draft = "Compare @./docs/ with another folder";
      await setComposer(input, draft, "Compare @./docs/".length);
      const menu = menuFor(page);
      await expect(menu.getByRole("option", { name: "readme.md", exact: true })).toBeVisible();
      await menu.getByRole("option", { name: "Final Notes/ (folder)", exact: true }).click();
      const expectedPrefix = `Compare ${JSON.stringify(join(cwd, "docs", "Final Notes") + "/")}`;
      await expect(input).toHaveValue(expectedPrefix + " with another folder");
      await expect(input).toBeFocused();
      expect(await input.evaluate((el) => (el as HTMLTextAreaElement).selectionStart)).toBe(
        expectedPrefix.length,
      );
    });
  });

  test("an exact absolute folder path offers the folder itself outside the cwd", async ({
    page,
    request,
  }) => {
    await withTempDir("shelley-folder-cwd-", async (cwd) => {
      await withTempDir("shelley-folder-elsewhere-", async (elsewhere) => {
        const target = join(elsewhere, "Empty Folder");
        mkdirSync(target);
        const input = await openConversation(page, request, cwd);
        await setComposer(input, `Inspect @${JSON.stringify(target)}`);
        const folder = menuFor(page).getByRole("option", {
          name: "Empty Folder/ (folder)",
          exact: true,
        });
        await expect(folder).toBeVisible();
        await input.press("Enter");
        await expect(input).toHaveValue(`Inspect ${JSON.stringify(target + "/")} `);
      });
    });
  });
});
