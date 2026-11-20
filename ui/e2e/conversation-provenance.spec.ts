import { test, expect, type Locator } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { createConversationViaAPIWithDetails } from "./helpers";

const run = promisify(execFile);
const binary = fileURLToPath(new URL("../../bin/shelley", import.meta.url));

async function sendFrom(sender: string, target: string, text: string) {
  await run(
    binary,
    [
      "client",
      "--url",
      `unix://${process.env.TEST_SERVER_SOCKET}`,
      "chat",
      "-c",
      target,
      "-p",
      text,
    ],
    {
      env: { ...process.env, SHELLEY_CONVERSATION_ID: sender },
    },
  );
}

async function expectToolCard(
  message: Locator,
  headline: string,
  background: string,
  tag = "message",
) {
  await expect(message).toHaveClass(/message-tool/);
  await expect(message).not.toHaveClass(/message-user|message-agent/);
  await expect(message).not.toContainText(/<(?:parent|subagent)_message/);
  const card = message.getByTestId("conversation-message-card");
  await expect(card.locator(".tool-tag")).toHaveText(tag);
  await expect(card.getByTestId("conversation-message-card-headline")).toHaveText(headline);
  await expect(card).toHaveCSS("background-color", background);
  await expect(card).toHaveCSS("border-radius", "8px");
  return card;
}

for (const viewport of [
  { width: 390, height: 844 },
  { width: 1280, height: 720 },
]) {
  for (const colorScheme of ["light", "dark"] as const) {
    test(`CLI conversation provenance renders tool cards at ${viewport.width}px (${colorScheme})`, async ({
      page,
      request,
    }) => {
      test.skip(
        !process.env.TEST_SERVER_SOCKET,
        "External test servers need TEST_SERVER_SOCKET for trusted CLI requests",
      );
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme });
      const parent = await createConversationViaAPIWithDetails(
        request,
        "subagent: backend echo: Ready.",
      );
      const response = await request.get(`/api/conversation/${parent.conversationId}/subagents`);
      expect(response.ok()).toBeTruthy();
      const [child] = await response.json();
      expect(child.conversation_id).toBeTruthy();

      await page.goto(`/c/${parent.slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible();
      // Wait for the history, not just the composer: a message that streams in
      // while the page is still loading is painted alone, then remounted (and
      // collapsed again) when the full history arrives.
      await expect(
        page.getByTestId("message").filter({ hasText: "subagent: backend echo: Ready." }),
      ).toBeVisible();
      const background = colorScheme === "dark" ? "rgb(31, 41, 55)" : "rgb(243, 244, 246)";
      const progress = "Backend underway: **API and database** implementation are progressing.";
      const body = `  ${progress}\nNext: tests, preview at https://example.com/api.`;
      await sendFrom(child.conversation_id, parent.conversationId, body);
      let message = page.getByTestId("message").filter({ hasText: "Backend underway" });
      let card = await expectToolCard(message, progress, background);
      await expect(card.getByRole("link", { name: child.slug, exact: true })).toBeVisible();
      // Multi-line messages expand into the verbatim text, like other tool cards'
      // details, with URLs linked.
      await card.getByRole("button", { name: "Expand" }).click();
      const code = card.locator(".tool-details .tool-code");
      await expect(code).toHaveJSProperty("textContent", body);
      await expect(code.getByRole("link", { name: "https://example.com/api" })).toHaveAttribute(
        "href",
        "https://example.com/api",
      );

      // Reload exercises persisted metadata rather than only stream updates.
      await page.reload();
      card = await expectToolCard(message, progress, background);
      const childLink = card.getByRole("link", { name: child.slug, exact: true });
      await expect(childLink).toHaveAttribute("href", `/c/${child.conversation_id}`);
      await expect(childLink).toHaveAttribute("title", "Open subagent conversation");
      await childLink.click();
      await expect(page.locator(".header-title")).toHaveText(child.slug);

      // The parent's delegated prompt shows where it came from.
      message = page.getByTestId("message").filter({ hasText: "echo: Ready." });
      card = await expectToolCard(message, "echo: Ready.", background, "from parent");
      await expect(card.getByRole("link", { name: parent.slug, exact: true })).toBeVisible();
      // A short message is one line: beside the sender on desktop, under it
      // on a phone, never a narrow column squeezed in beside the sender.
      const headline = await card
        .getByTestId("conversation-message-card-headline")
        .evaluate((el) => {
          const range = document.createRange();
          range.selectNodeContents(el);
          const slug = el.parentElement!.querySelector("a")!.getBoundingClientRect();
          return {
            lines: new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size,
            below: el.getBoundingClientRect().top >= slug.bottom,
            clipped: el.scrollWidth > el.clientWidth,
          };
        });
      expect(headline).toEqual({ lines: 1, below: viewport.width < 640, clipped: false });

      // A short single-line message shows in full with nothing to expand.
      const instruction = "Please finish the API tests before the database changes.";
      await sendFrom(parent.conversationId, child.conversation_id, instruction);
      message = page.getByTestId("message").filter({ hasText: instruction });
      card = await expectToolCard(message, instruction, background, "from parent");
      await expect(card.getByRole("button", { name: "Expand" })).toHaveCount(0);
      await expect(card.getByRole("link", { name: parent.slug, exact: true })).toBeVisible();
      // The label follows renames; the stable ID keeps the link unambiguous.
      const renamedSlug = `parent-renamed-${parent.conversationId.toLowerCase()}`;
      const renamed = await request.post(`/api/conversation/${parent.conversationId}/rename`, {
        data: { slug: renamedSlug },
      });
      expect(renamed.ok()).toBeTruthy();
      parent.slug = renamedSlug;
      const parentLink = card.getByRole("link", { name: parent.slug, exact: true });
      await expect(parentLink).toHaveAttribute("href", `/c/${parent.conversationId}`);
      await expect(parentLink).toHaveAttribute("title", "Open parent conversation");
      await parentLink.click();
      await expect(page.locator(".header-title")).toHaveText(parent.slug);
      message = page.getByTestId("message").filter({ hasText: "Backend underway" });
      await expect(message.getByRole("link", { name: child.slug, exact: true })).toBeVisible();

      const human = page
        .getByTestId("message")
        .filter({ hasText: "subagent: backend echo: Ready." })
        .first();
      await expect(human).toHaveClass(/message-user/);
      await expect(human.getByTestId("conversation-message-card")).toHaveCount(0);
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      ).toBe(true);
    });
  }
}

test("queued and transcribing conversation messages share the source header", async ({
  page,
  request,
}) => {
  const parent = await createConversationViaAPIWithDetails(request, "echo: queue provenance");
  const clockStart = new Date();
  const queuedAt = new Date(clockStart.getTime() + 1000);
  await page.clock.install({ time: clockStart });
  await page.clock.pauseAt(queuedAt);
  const source = {
    sender_conversation_id: "child",
    sender_slug: "backend",
    sender_relationship: "subagent",
    Text: "Queued progress",
  };
  const queued = JSON.stringify([
    {
      id: "queued-source",
      created_at: queuedAt.toISOString(),
      model: "predictable",
      llm: { Role: 0, Content: [{ Type: 2, Text: source.Text }] },
      user_data: source,
    },
    ...["working", "failed"].map((state) => ({
      id: `transcription-${state}`,
      created_at: queuedAt.toISOString(),
      model: "predictable",
      kind: "transcription",
      state,
      transcription: {
        media_path: "/tmp/progress.webm",
        context: "Progress report",
      },
      user_data: {
        ...source,
        sender_relationship: "parent",
        sender_slug: parent.slug,
        sender_conversation_id: parent.conversationId,
      },
    })),
  ]);
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route("**/api/conversations/snapshot", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    const conversation = body.conversations.find(
      (c: { conversation_id: string }) => c.conversation_id === parent.conversationId,
    );
    conversation.queued_messages = queued;
    await route.fulfill({ response, json: body });
  });
  await page.route(`**/api/conversation/${parent.conversationId}*`, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.conversation.queued_messages = queued;
    await route.fulfill({ response, json: body });
  });
  await page.goto(`/c/${parent.slug}`);
  const ghost = page.getByTestId("queued-ghost");
  const tasks = page.getByTestId("transcription-task");
  await expect(tasks).toHaveCount(2);
  await expect(ghost).toHaveCount(0);
  await page.clock.runFor(1999);
  await expect(ghost).toHaveCount(0);
  await page.clock.runFor(1);
  await expect(ghost.getByTestId("message-author-conversation")).toHaveText("Message from backend");
  await expect(ghost).toHaveClass(/message-tool/);
  await expect(ghost).toHaveCSS("background-color", "rgb(243, 244, 246)");
  await expect(ghost.getByTestId("message-content")).toHaveCSS("border-top-style", "dashed");
  await expect(ghost).toContainText("Queued progress");

  for (const task of await tasks.all()) {
    await expect(task).toHaveCSS("background-color", "rgb(243, 244, 246)");
    await expect(task.getByTestId("message-author-conversation")).toHaveText(
      `Message from ${parent.slug}`,
    );
  }
});
