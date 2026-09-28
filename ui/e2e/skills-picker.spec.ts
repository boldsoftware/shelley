import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createConversationViaAPIWithDetails, withTempDir } from "./helpers";

const skills = [
  {
    name: "draft-review",
    description: "Review a draft for clarity",
    activate: "shelley skill cat draft-review",
    origin: "File",
    source_path: "/project/.skills/draft-review/SKILL.md",
  },
  {
    name: "test-design",
    description: "Design regression tests",
    activate: "shelley skill cat test-design",
    origin: "Built into Shelley",
  },
];

function instruction(name: string) {
  return `Use the \`${name}\` skill for this task.\nLoad its instructions with \`shelley skill cat ${name}\`.\n\n`;
}

async function mockCatalog(page: Page) {
  await page.route("**/api/skills?*", (route) => route.fulfill({ json: { skills } }));
}

function sentMessages(page: Page) {
  const messages: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      /\/api\/(conversations\/new|conversation\/[^/]+\/(chat|queue))$/.test(
        new URL(request.url()).pathname,
      )
    ) {
      messages.push(request.postDataJSON()?.message ?? "");
    }
  });
  return messages;
}

test("slash menu opens skills, selection inserts an instruction without sending", async ({
  page,
}) => {
  await mockCatalog(page);
  await page.goto("/new");
  const input = page.getByTestId("message-input");
  const sent = sentMessages(page);
  await input.fill("/ski");
  await expect(
    page.getByTestId("slash-command-menu").getByText("/skills", { exact: true }),
  ).toBeVisible();
  await input.press("Tab");
  const picker = page.getByTestId("skill-picker");
  await expect(picker).toBeVisible();
  await expect(picker.getByRole("option")).toHaveCount(2);
  await expect(picker).toContainText("Review a draft for clarity");
  await expect(picker).toContainText("File");
  await input.press("ArrowDown");
  await input.press("Enter");
  await expect(input).toHaveValue(instruction("test-design"));
  await expect(picker).toBeHidden();
  await expect(input).toBeFocused();
  expect(sent).toEqual([]);
});

test("filters descriptions, preserves surrounding draft and supports multiple selections", async ({
  page,
}) => {
  await mockCatalog(page);
  await page.goto("/new");
  const input = page.getByTestId("message-input");
  const prefix = "Please help.\n";
  const suffix = "\nKeep my examples.";
  await input.fill(`${prefix}/skills clarity${suffix}`);
  await input.evaluate((element, caret) => {
    const textarea = element as HTMLTextAreaElement;
    textarea.setSelectionRange(caret, caret);
    textarea.dispatchEvent(new Event("select", { bubbles: true }));
  }, `${prefix}/skills clarity`.length);
  const picker = page.getByTestId("skill-picker");
  await expect(picker.getByRole("option")).toHaveCount(1);
  await picker.getByRole("option").click();
  await expect(input).toHaveValue(prefix + instruction("draft-review") + suffix);
  await input.press("ControlOrMeta+End");
  await input.press("Shift+Enter");
  await input.pressSequentially("/skills test-design");
  await expect(picker.getByRole("option")).toHaveCount(1);
  await input.press("Tab");
  await expect(input).toHaveValue(
    prefix + instruction("draft-review") + suffix + "\n" + instruction("test-design"),
  );
});

test("Escape dismisses without changing text and typing reopens", async ({ page }) => {
  await mockCatalog(page);
  await page.goto("/new");
  const input = page.getByTestId("message-input");
  await input.fill("/skills");
  const picker = page.getByTestId("skill-picker");
  await expect(picker).toBeVisible();
  await input.press("Escape");
  await expect(picker).toBeHidden();
  await expect(input).toHaveValue("/skills");
  await input.pressSequentially(" clarity");
  await expect(picker.getByRole("option")).toHaveCount(1);
});

test("loading, errors and empty matches never submit the picker command", async ({ page }) => {
  let release!: () => void;
  const blocked = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/skills?*", async (route) => {
    await blocked;
    await route.fulfill({ status: 503, body: "Catalog unavailable" });
  });
  await page.goto("/new");
  const sent = sentMessages(page);
  const input = page.getByTestId("message-input");
  await input.fill("/skills");
  const picker = page.getByTestId("skill-picker");
  await expect(picker).toContainText(/loading/i);
  await input.press("Enter");
  await input.press("Tab");
  await expect(input).toHaveValue("/skills");
  release();
  await expect(picker).toContainText("Catalog unavailable");
  await input.press("Enter");
  expect(sent).toEqual([]);

  await page.unroute("**/api/skills?*");
  await mockCatalog(page);
  await input.fill("");
  await input.fill("/skills nonexistent");
  await expect(picker).toContainText(/no.*skills/i);
  await input.press("Enter");
  await expect(input).toHaveValue("/skills nonexistent");
  expect(sent).toEqual([]);
});

test("real conversation catalog keeps its snapshot and sends the selected instruction", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-skills-picker-", async (cwd) => {
    const skillDir = join(cwd, ".skills", "draft-review");
    mkdirSync(skillDir, { recursive: true });
    writeFileSync(
      join(skillDir, "SKILL.md"),
      "---\nname: draft-review\ndescription: Review a draft for clarity\n---\nRead the user's draft.\n",
    );
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: skill fixture",
      { cwd },
    );
    // A new file must not silently change an existing conversation's catalog.
    const addedDir = join(cwd, ".skills", "newly-added");
    mkdirSync(addedDir, { recursive: true });
    writeFileSync(
      join(addedDir, "SKILL.md"),
      "---\nname: newly-added\ndescription: Not in this conversation yet\n---\nHidden until prompt refresh.\n",
    );
    await page.goto(`/c/${slug}`);
    const input = page.getByTestId("message-input");
    await input.fill("/skills newly-added");
    await expect(page.getByTestId("skill-picker")).toContainText(/no.*skills/i);
    await input.fill("/skills draft-review");
    await expect(page.getByTestId("skill-picker").getByRole("option")).toHaveCount(1);
    await input.press("Enter");
    await expect(input).toHaveValue(instruction("draft-review"));
    await input.pressSequentially("Please review this draft.");
    const expected = instruction("draft-review") + "Please review this draft.";
    const posted = page.waitForRequest(
      (req) =>
        req.method() === "POST" &&
        new URL(req.url()).pathname === `/api/conversation/${conversationId}/chat`,
    );
    await page.getByTestId("send-button").click();
    expect((await posted).postDataJSON().message).toBe(expected);
    await expect(input).toHaveValue("");
  });
});

test("attachments and failed-send retries retain the selected instruction", async ({
  page,
  request,
}) => {
  const { slug } = await createConversationViaAPIWithDetails(request, "echo: attachments fixture");
  await mockCatalog(page);
  await page.goto(`/c/${slug}`);
  await page.locator('input[type="file"].message-input-hidden').setInputFiles({
    name: "draft.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("A test draft."),
  });
  const attachment = page.locator(".message-attachment");
  await expect(attachment).toContainText("draft.txt");
  await expect(page.locator(".message-attachment-overlay")).toHaveCount(0);
  const input = page.getByTestId("message-input");
  await input.fill("/skills draft-review");
  await expect(page.getByTestId("skill-picker").getByRole("option")).toHaveCount(1);
  await input.press("Enter");
  await expect(attachment).toContainText("draft.txt");
  await page.route("**/api/conversation/*/chat", (route) =>
    route.fulfill({ status: 503, body: "Try again" }),
  );
  await page.getByTestId("send-button").click();
  await expect(input).toHaveValue(instruction("draft-review"));
  await expect(attachment).toContainText("draft.txt");
  await expect(page.getByTestId("send-button")).toBeEnabled();
  await page.unroute("**/api/conversation/*/chat");
});

test("skill picker stays inside the mobile viewport", async ({ page }) => {
  await mockCatalog(page);
  await page.goto("/new");
  await page.getByTestId("message-input").fill("/skills");
  const picker = page.getByTestId("skill-picker");
  await expect(picker.getByRole("option")).toHaveCount(2);
  const bounds = await picker.boundingBox();
  expect(bounds).not.toBeNull();
  const viewport = page.viewportSize()!;
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width);
  expect(bounds!.y).toBeGreaterThanOrEqual(0);
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(viewport.height);
});

test("existing file and model completion still work after dismissing skills", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-skill-completion-", async (cwd) => {
    writeFileSync(join(cwd, "fixture.md"), "File completion fixture.\n");
    const { slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: completion fixture",
      { cwd },
    );
    await mockCatalog(page);
    await page.goto(`/c/${slug}`);
    const input = page.getByTestId("message-input");
    await input.fill("/skills");
    await expect(page.getByTestId("skill-picker").getByRole("option")).toHaveCount(2);
    await input.press("Escape");
    await input.fill("@fixture");
    const files = page.getByTestId("file-completion-menu");
    await expect(files.getByRole("option")).toHaveCount(1);
    await input.press("Tab");
    await expect(input).toHaveValue("@fixture.md ");
    await expect(page.getByTestId("skill-picker")).toBeHidden();
    await input.fill("/model pred");
    const models = page.getByRole("listbox", { name: "Model options" });
    await expect(models).toBeVisible();
    await input.press("Tab");
    await expect(input).toHaveValue("/model predictable ");
  });
});

test("a promoted lazy draft switches to its saved skill catalog without navigation", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-promoted-skills-", async (cwd) => {
    const original = join(cwd, ".skills", "draft-review");
    mkdirSync(original, { recursive: true });
    writeFileSync(
      join(original, "SKILL.md"),
      "---\nname: draft-review\ndescription: Review a draft\n---\nReview it.\n",
    );
    await page.addInitScript((dir) => localStorage.setItem("shelley_selected_cwd", dir), cwd);
    await page.goto("/new");
    const input = page.getByTestId("message-input");
    const draftCreated = page.waitForResponse(
      (res) =>
        res.request().method() === "POST" &&
        new URL(res.url()).pathname === "/api/conversations/draft",
    );
    await input.fill("echo: promote this skill draft");
    const draft = await (await draftCreated).json();
    expect(draft.is_draft).toBe(true);
    await page.getByTestId("send-button").click();
    await expect(input).toHaveValue("");
    await expect(async () => {
      const state = await (await request.get(`/api/conversation/${draft.conversation_id}`)).json();
      expect(state.conversation.is_draft).toBeFalsy();
      expect(
        state.messages.some(
          (m: { type: string; end_of_turn?: boolean }) => m.type === "agent" && m.end_of_turn,
        ),
      ).toBe(true);
    }).toPass();
    const added = join(cwd, ".skills", "newly-added");
    mkdirSync(added, { recursive: true });
    writeFileSync(
      join(added, "SKILL.md"),
      "---\nname: newly-added\ndescription: Added after promotion\n---\nNot in snapshot.\n",
    );
    const catalogRequest = page.waitForRequest(
      (req) => new URL(req.url()).pathname === "/api/skills",
    );
    await input.fill("/skills newly-added");
    expect(new URL((await catalogRequest).url()).searchParams.get("conversation_id")).toBe(
      draft.conversation_id,
    );
    await expect(page.getByTestId("skill-picker")).toContainText(/no.*skills/i);
    await input.fill("/skills draft-review");
    await expect(page.getByTestId("skill-picker").getByRole("option")).toHaveCount(1);
  });
});
