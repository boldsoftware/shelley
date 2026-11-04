import { expect, test } from "@playwright/test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  chooseTerminalMenuItem,
  closeTerminalsIn,
  createConversationViaAPIWithDetails,
  testWorkingDirectory,
} from "./helpers";

// The server takes websocket messages of at most 32 KiB, and a paste is a
// single onData. A bigger paste used to close the socket and leave the
// terminal hung, whatever was reading it.
test("a paste bigger than a websocket message reaches the program", async ({ page, request }) => {
  const lines = Array.from(
    { length: 600 },
    (_, i) => `line ${i}\t"quoted" \\ ünïcødé 😀 ${"x".repeat(40)}`,
  );
  const paste = lines.join("\n") + "\n";
  expect(JSON.stringify({ type: "input", data: paste }).length).toBeGreaterThan(32 << 10);

  const cwd = mkdtempSync(join(testWorkingDirectory(), "terminal-paste-"));
  try {
    writeFileSync(
      join(cwd, "paste.sh"),
      `printf 'paste-ready\\n'\nhead -n ${lines.length} > got\n`,
    );
    const { conversationId } = await createConversationViaAPIWithDetails(
      request,
      "terminal paste test",
      { cwd },
    );
    await page.goto(`/c/${conversationId}`);
    const input = page.getByTestId("message-input");
    await input.fill("!bash paste.sh");
    await page.getByTestId("send-button").click();
    await expect
      .poll(async () => {
        await chooseTerminalMenuItem(page, "Insert all output into input");
        return input.inputValue();
      })
      .toMatch(/paste-ready/);

    await page
      .getByRole("textbox", { name: "Terminal input", exact: true })
      .evaluate((textarea, text) => {
        const clipboardData = new DataTransfer();
        clipboardData.setData("text/plain", text);
        textarea.dispatchEvent(
          new ClipboardEvent("paste", { clipboardData, bubbles: true, cancelable: true }),
        );
      }, paste);

    await expect(page.locator(".terminal-panel-tab-success")).toHaveCount(1);
    expect(readFileSync(join(cwd, "got"), "utf8")).toBe(paste);
  } finally {
    await closeTerminalsIn(request, cwd);
    rmSync(cwd, { recursive: true, force: true });
  }
});
