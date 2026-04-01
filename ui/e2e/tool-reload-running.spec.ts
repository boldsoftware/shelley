import { test, expect } from "@playwright/test";
import { unblockFIFO, makeFIFO } from "./fifo-helpers";

async function startBlockedBashTool(page: import("@playwright/test").Page) {
  const fifoPath = makeFIFO("shelley-tool-reload-");

  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  const messageInput = page.getByTestId("message-input");
  const sendButton = page.getByTestId("send-button");
  const toolCommandSnippet = `cat ${fifoPath} >/dev/null`;
  const command = `bash: printf 'before-reload\\n'; ${toolCommandSnippet}`;

  await messageInput.fill(command);
  await sendButton.click();

  const runningTool = page.getByTestId("tool-call-running").filter({ hasText: toolCommandSnippet });
  await expect(runningTool).toBeVisible({ timeout: 30000 });
  await expect(runningTool).toContainText("before-reload", { timeout: 10000 });
  await expect(page).toHaveURL(/\/c\//, { timeout: 30000 });

  return { fifoPath, toolCommandSnippet };
}

async function startTwoBlockedBashTools(page: import("@playwright/test").Page) {
  const firstFifoPath = makeFIFO("shelley-tool-reload-multi-first-");
  const secondFifoPath = makeFIFO("shelley-tool-reload-multi-second-");

  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  const messageInput = page.getByTestId("message-input");
  const sendButton = page.getByTestId("send-button");
  const firstCommand = `printf 'first-start\\n'; cat ${firstFifoPath} >/dev/null # slow_ok`;
  const secondCommand = `printf 'second-start\\n'; cat ${secondFifoPath} >/dev/null # slow_ok`;
  const firstToolSnippet = `cat ${firstFifoPath} >/dev/null`;
  const secondToolSnippet = `cat ${secondFifoPath} >/dev/null`;
  const command = `multi_bash: ${firstCommand} ||| ${secondCommand}`;

  await messageInput.fill(command);
  await sendButton.click();

  const firstTool = page.getByTestId("tool-call-running").filter({ hasText: firstToolSnippet });
  const secondTool = page.getByTestId("tool-call-running").filter({ hasText: secondToolSnippet });

  await expect(firstTool).toBeVisible({ timeout: 30000 });
  await expect(secondTool).toBeVisible({ timeout: 30000 });
  await expect(firstTool).toContainText("first-start", { timeout: 10000 });
  await expect(secondTool).toContainText("second-start", { timeout: 10000 });
  await expect(page).toHaveURL(/\/c\//, { timeout: 30000 });

  return { firstFifoPath, secondFifoPath, firstToolSnippet, secondToolSnippet };
}

test.describe("Reload during in-flight tool calls", () => {
  test("reload keeps a visible running indicator when no more output arrives after reload", async ({
    page,
  }) => {
    const { fifoPath, toolCommandSnippet } = await startBlockedBashTool(page);

    try {
      await page.reload();
      await page.waitForLoadState("domcontentloaded");
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

      const runningTool = page
        .getByTestId("tool-call-running")
        .filter({ hasText: toolCommandSnippet });
      await expect(runningTool).toBeVisible({ timeout: 10000 });
      await expect(
        page.getByTestId("tool-call-completed").filter({ hasText: toolCommandSnippet }),
      ).toHaveCount(0);

      // Bug: after reload, the tool card is reconstructed from tool_use without any
      // post-reload progress, so it no longer looks visibly "running".
      await expect(runningTool).toContainText("running");
    } finally {
      await unblockFIFO(fifoPath);
      await expect(
        page.getByTestId("tool-call-completed").filter({ hasText: toolCommandSnippet }),
      ).toBeVisible({ timeout: 30000 });
    }
  });

  test("reload keeps a visible running indicator even when the tool later succeeds without post-reload output", async ({
    page,
  }) => {
    const { fifoPath, toolCommandSnippet } = await startBlockedBashTool(page);

    await page.reload();
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    const runningTool = page
      .getByTestId("tool-call-running")
      .filter({ hasText: toolCommandSnippet });
    await expect(runningTool).toBeVisible({ timeout: 10000 });
    await expect(runningTool).toContainText("running");

    await unblockFIFO(fifoPath);

    await expect(
      page.getByTestId("tool-call-completed").filter({ hasText: toolCommandSnippet }),
    ).toBeVisible({ timeout: 30000 });
    await expect(
      page.getByTestId("tool-call-running").filter({ hasText: toolCommandSnippet }),
    ).toHaveCount(0);
  });
});

test.describe("Reload during multi-tool in-flight batches", () => {
  test("reload keeps both blocked tools visibly running when no more output arrives after reload", async ({
    page,
  }) => {
    const { firstFifoPath, secondFifoPath, firstToolSnippet, secondToolSnippet } =
      await startTwoBlockedBashTools(page);

    try {
      await page.reload();
      await page.waitForLoadState("domcontentloaded");
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

      const firstTool = page.getByTestId("tool-call-running").filter({ hasText: firstToolSnippet });
      const secondTool = page
        .getByTestId("tool-call-running")
        .filter({ hasText: secondToolSnippet });
      await expect(firstTool).toBeVisible({ timeout: 10000 });
      await expect(secondTool).toBeVisible({ timeout: 10000 });
      await expect(firstTool).toContainText("running");
      await expect(secondTool).toContainText("running");
    } finally {
      await unblockFIFO(firstFifoPath).catch(() => {});
      await unblockFIFO(secondFifoPath).catch(() => {});
    }
  });

  test("reload preserves running state for both tools until each later succeeds", async ({
    page,
  }) => {
    const { firstFifoPath, secondFifoPath, firstToolSnippet, secondToolSnippet } =
      await startTwoBlockedBashTools(page);

    await page.reload();
    await page.waitForLoadState("domcontentloaded");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

    const firstTool = page.getByTestId("tool-call-running").filter({ hasText: firstToolSnippet });
    const secondTool = page.getByTestId("tool-call-running").filter({ hasText: secondToolSnippet });
    await expect(firstTool).toBeVisible({ timeout: 10000 });
    await expect(secondTool).toBeVisible({ timeout: 10000 });

    await unblockFIFO(firstFifoPath);
    await expect(
      page.getByTestId("tool-call-completed").filter({ hasText: firstToolSnippet }),
    ).toBeVisible({ timeout: 30000 });
    await expect(secondTool).toBeVisible({ timeout: 10000 });
    await expect(secondTool).toContainText("running");

    await unblockFIFO(secondFifoPath);
    await expect(
      page.getByTestId("tool-call-completed").filter({ hasText: secondToolSnippet }),
    ).toBeVisible({ timeout: 30000 });
  });
});

async function startMixedCompletionBatch(page: import("@playwright/test").Page) {
  const secondFifoPath = makeFIFO("shelley-tool-reload-mixed-");

  await page.goto("/");
  await page.waitForLoadState("domcontentloaded");

  const messageInput = page.getByTestId("message-input");
  const sendButton = page.getByTestId("send-button");
  // Embed the unique FIFO path in the first command's output so the snippet
  // is unique across parallel/sequential test runs sharing the same server.
  const firstCommand = `printf 'first-done:${secondFifoPath}\\n'`;
  const secondCommand = `printf 'second-start\n'; cat ${secondFifoPath} >/dev/null # slow_ok`;
  const firstToolSnippet = `first-done:${secondFifoPath}`;
  const secondToolSnippet = `cat ${secondFifoPath} >/dev/null`;
  const command = `multi_bash: ${firstCommand} ||| ${secondCommand}`;

  await messageInput.fill(command);
  await sendButton.click();

  const secondTool = page.getByTestId("tool-call-running").filter({ hasText: secondToolSnippet });
  await expect(secondTool).toBeVisible({ timeout: 30000 });
  await expect(secondTool).toContainText("second-start", { timeout: 10000 });
  await expect(page).toHaveURL(/\/c\//, { timeout: 30000 });

  return { secondFifoPath, firstToolSnippet, secondToolSnippet };
}

test.describe("Live multi-tool progress without reload", () => {
  test("marks the first tool completed while the second tool is still running", async ({
    page,
  }) => {
    const { secondFifoPath, firstToolSnippet, secondToolSnippet } =
      await startMixedCompletionBatch(page);

    try {
      const firstToolCompleted = page
        .getByTestId("tool-call-completed")
        .filter({ hasText: firstToolSnippet });
      const firstToolRunning = page
        .getByTestId("tool-call-running")
        .filter({ hasText: firstToolSnippet });
      const secondToolRunning = page
        .getByTestId("tool-call-running")
        .filter({ hasText: secondToolSnippet });

      await expect(secondToolRunning).toBeVisible({ timeout: 30000 });
      await expect(firstToolCompleted).toBeVisible({ timeout: 30000 });
      await expect(firstToolCompleted).toContainText("✓");
      await expect(firstToolRunning).toHaveCount(0);
      await expect(secondToolRunning).toContainText("running");
    } finally {
      await unblockFIFO(secondFifoPath);
      await expect(
        page.getByTestId("tool-call-completed").filter({ hasText: secondToolSnippet }),
      ).toBeVisible({ timeout: 30000 });
    }
  });
});
