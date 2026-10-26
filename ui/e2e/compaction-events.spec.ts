import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import type { InPlaceCompaction } from "../src/generated-types";
import type { LLMMessage, Message } from "../src/types";
import { createConversationViaAPIWithDetails } from "./helpers";

const index = "## Index\n#4–#12  earlier exploration\n#15–#20  implementation";
const fullSummary = "Kept the decisions and current work.\nOriginal messages are still visible.";
const compaction: InPlaceCompaction = {
  squishes: [
    { from_sequence_id: 4, to_sequence_id: 12, summary: fullSummary },
    { from_sequence_id: 15, to_sequence_id: 20, summary: "Preserved the implementation plan." },
  ],
  trims: [
    { sequence_id: 7, tool_use_id: "first-output" },
    { sequence_id: 18, tool_use_id: "second-output" },
  ],
  hidden_sequence_ids: [21],
  hidden_tool_use_ids: ["compact-call"],
};

async function replayCompaction(
  page: Page,
  request: APIRequestContext,
  data: InPlaceCompaction = compaction,
) {
  const { conversationId, slug } = await createConversationViaAPIWithDetails(
    request,
    "echo: compaction events fixture",
  );
  const response = await request.get(`/api/conversation/${conversationId}`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  const base = body.messages.at(-1) as Message;
  const message = (
    type: Message["type"],
    sequence: number,
    llm: LLMMessage | null,
    userData?: unknown,
  ): Message => ({
    ...base,
    message_id: randomUUID(),
    sequence_id: base.sequence_id + sequence,
    type,
    llm_data: llm ? JSON.stringify(llm) : null,
    user_data: userData ? JSON.stringify(userData) : null,
    end_of_turn: false,
  });
  body.conversation.agent_working = false;
  body.messages.push(
    message(
      "user",
      1,
      {
        Role: 1,
        Content: [
          { ID: "size", Type: 2, Text: "Context is 170k." },
          { ID: "index", Type: 2, Text: index },
        ],
      },
      { context_nudge: true },
    ),
    message("agent", 2, {
      Role: 2,
      Content: [
        {
          ID: "compact-call",
          Type: 5,
          ToolName: "compact_in_place",
          ToolInput: {
            action: "compact",
            collapse: [{ from: "4", to: "12", note: fullSummary }],
            trim: ["7"],
          },
        },
      ],
    }),
    message("tool", 3, {
      Role: 1,
      Content: [
        {
          ID: "compact-result",
          Type: 6,
          ToolUseID: "compact-call",
          ToolResult: [{ ID: "result", Type: 2, Text: "Compacted: 2 collapsed, 2 trimmed" }],
        },
      ],
    }),
    message("inplacecompaction", 4, null, data),
  );
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route(`**/api/conversation/${conversationId}`, (route) =>
    route.fulfill({ json: body }),
  );
  await page.goto(`/c/${slug}`);
}

for (const width of [390, 1280]) {
  test(`compaction TOC entry navigates to its status at ${width}px`, async ({ page, request }) => {
    await page.setViewportSize({ width, height: 844 });
    await replayCompaction(page, request);
    const result = page.getByTestId("message-inplacecompaction");
    const messageId = await result.getAttribute("data-message-id");
    expect(messageId).toBeTruthy();
    const fragment = `m-${messageId!.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8)}`;
    const tocButton = page.getByRole("button", {
      name: "Conversation table of contents",
      exact: true,
    });
    await tocButton.click();
    const toc = page.locator(".toc-popover");
    const entry = toc.locator(".toc-entry-compaction");
    await expect(entry).toHaveCount(1);
    await expect(entry).toHaveAccessibleName("Compacted context");
    await expect(toc.locator(".toc-entry-user")).toHaveCount(1);
    await expect(toc).not.toContainText("Context is 170k.");
    await entry.click();
    await expect(toc).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`#${fragment}$`));
    await expect(result).toHaveClass(/message-highlight/);
    await expect(result).toBeInViewport();
    await expect(result).not.toHaveAttribute("open");

    await page.reload();
    await expect(result).toHaveClass(/message-highlight/);
    await expect(result).toBeInViewport();
  });
}

test("compaction notices open their index and audit table directly", async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await replayCompaction(page, request);
  const nudge = page.getByTestId("message-context-nudge");
  const notice = nudge.locator(":scope > summary");
  await expect(notice).toHaveText("📏 Compaction threshold reached · 170k tokens");
  await expect(nudge.locator(".msg-context-nudge-index")).toBeHidden();
  // Hit the row's empty left edge, not just its text or chevron.
  await notice.click({ position: { x: 5, y: 10 } });
  await expect(nudge.locator(".msg-context-nudge-index")).toBeVisible();
  await expect(nudge.locator(".msg-context-nudge-index")).toHaveText(index);
  await notice.focus();
  await page.keyboard.press("Enter");
  await expect(nudge.locator(".msg-context-nudge-index")).toBeHidden();

  const tool = page.locator(".tool").filter({ hasText: "Compacted: 2 collapsed, 2 trimmed" });
  await expect(tool).toBeVisible();
  await expect(tool.locator(".tool-details")).toBeHidden();

  const result = page.getByTestId("message-inplacecompaction");
  const status = result.locator(":scope > summary");
  const table = result.getByRole("table", { name: "Compacted messages" });
  await expect(status).toHaveText("Compacted context · 2 groups, 2 tool outputs");
  await expect(table).toBeHidden();
  await status.click({ position: { x: 5, y: 10 } });
  await expect(table).toBeVisible();
  await expect(table.getByRole("columnheader")).toHaveText(["Messages", "Summary"]);
  await expect(table.locator("thead")).toBeVisible();
  await expect(table.locator("tbody tr").first()).toHaveCSS("display", "table-row");
  await expect(table.locator("tbody tr")).toHaveCount(2);
  await expect(result.locator("details")).toHaveCount(0);
  await expect(table.locator(".msg-compaction-summary-text").first()).toBeVisible();
  await expect(table.locator(".msg-compaction-summary-text").first()).toHaveText(fullSummary);
  await expect(table.locator(".msg-compaction-summary-text").nth(1)).toBeVisible();
  await expect(result).toHaveAttribute("open", "");
  await expect(result.locator(".msg-compaction-ids")).toBeVisible();
  await expect(result.locator(".msg-compaction-ids")).toHaveText("#7, #18");
  const bookkeeping = result.getByText(
    "Hid 1 context nudge and 1 compaction call from the agent.",
    { exact: false },
  );
  await expect(bookkeeping).toBeVisible();
  await status.focus();
  await page.keyboard.press("Enter");
  await expect(table).toBeHidden();
  await expect(result.locator(".msg-compaction-ids")).toBeHidden();
  await expect(bookkeeping).toBeHidden();
});

for (const { name, data, headline } of [
  {
    name: "singular counts and long summaries",
    data: {
      squishes: [{ from_sequence_id: 4, to_sequence_id: 12, summary: "long-summary-".repeat(200) }],
      trims: [{ sequence_id: 7, tool_use_id: "first-output" }],
    },
    headline: "Compacted context · 1 group, 1 tool output",
  },
  {
    name: "trims without summaries",
    data: { trims: [{ sequence_id: 7, tool_use_id: "first-output" }] },
    headline: "Compacted context · 1 tool output",
  },
  {
    name: "skips",
    data: { hidden_sequence_ids: [21] },
    headline: "Compaction skipped",
  },
]) {
  test(`compaction event handles ${name} on mobile`, async ({ page, request }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await replayCompaction(page, request, data);
    const result = page.getByTestId("message-inplacecompaction");
    await expect(result.locator(":scope > summary")).toHaveText(headline);
    await result.locator(":scope > summary").click();
    await expect(result.locator("details")).toHaveCount(0);
    if (data.squishes) {
      const table = result.locator(".msg-compaction-table");
      await expect(table.locator("thead")).toHaveCSS("clip-path", "inset(50%)");
      await expect(table.getByRole("columnheader")).toHaveText(["Messages", "Summary"]);
      const cells = table.locator("tbody tr").first().locator("td");
      await expect(cells.first()).toHaveCSS("display", "block");
      await expect(cells.nth(1)).toHaveCSS("display", "block");
      const header = await cells.first().boundingBox();
      const content = await cells.nth(1).boundingBox();
      expect(header).not.toBeNull();
      expect(content).not.toBeNull();
      expect(content!.y).toBeGreaterThanOrEqual(header!.y + header!.height);
      expect(content!.x).toBe(header!.x);
      expect(content!.width).toBe(header!.width);
      await expect(result.locator(".msg-compaction-summary-text")).toBeVisible();
      await expect(result.locator(".msg-compaction-summary-text")).toHaveText(
        data.squishes[0].summary,
      );
    } else {
      await expect(result.getByRole("table")).toHaveCount(0);
    }
    if (data.trims) {
      await expect(result.locator(".msg-compaction-ids")).toBeVisible();
      await expect(result.locator(".msg-compaction-ids")).toHaveText("#7");
    }
    if (data.hidden_sequence_ids) {
      await expect(
        result.getByText("Hid 1 context nudge from the agent.", { exact: false }),
      ).toBeVisible();
    }
    const overflows = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflows).toBe(false);
  });
}
