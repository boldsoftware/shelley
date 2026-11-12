import { expect, test } from "@playwright/test";
import { setPageFeatureFlag } from "./helpers";

// While this Shelley archives (server/archiver.go), each message to the
// Assistant may be temporary (server/temporary.go): the toggle beside Send
// says who can read what it sends, turns off once a message is sent, and the
// transcript marks the temporary message and its reply.
test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

test("a temporary message to the Assistant", async ({ page, request }) => {
  const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(on.ok()).toBeTruthy();
  try {
    // The page's flag only: the store is never reached.
    await setPageFeatureFlag(page, "archiver", "https://archive-store.int.exe.xyz/");
    await page.goto("/");
    await expect(page.locator("h1.header-title")).toHaveText("Assistant");
    const toggle = page.getByTestId("temporary-toggle");
    const input = page.getByTestId("message-input");
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    await toggle.hover();
    await expect(page.locator(".p-tooltip")).toContainText("your team can read them");

    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator(".message-input-container.temporary-on")).toBeVisible();
    await expect(input).toHaveAttribute("placeholder", /Temporary message/);
    await input.hover();
    await toggle.hover();
    await expect(page.locator(".p-tooltip")).toContainText("Turns off once sent");
    await input.fill('message_user: {"text":"The plan: cake at 3."}');
    await page.screenshot({ path: "test-results/temporary-message-composer.png" });

    await input.press("Enter");
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    await expect(input).not.toHaveAttribute("placeholder", /Temporary/);
    const asked = page.getByTestId("message").filter({ hasText: "cake at 3" });
    await expect(asked.getByTestId("message-temporary")).toBeVisible();
    const reply = page.getByTestId("message-user-bubble").filter({ hasText: "cake at 3" });
    await expect(reply).toHaveClass(/message-temporary/);

    await input.fill('message_user: {"text":"Hello again."}');
    await input.press("Enter");
    const normalReply = page.getByTestId("message-user-bubble").filter({ hasText: "Hello again" });
    await expect(normalReply).toBeVisible();
    await expect(normalReply).not.toHaveClass(/message-temporary/);
    const normal = page.getByTestId("message").filter({ hasText: "Hello again" });
    await expect(normal).not.toHaveClass(/message-temporary/);
    await expect(normal.getByTestId("message-temporary")).toHaveCount(0);
    await expect(reply).toHaveClass(/message-temporary/);
    await page.screenshot({ path: "test-results/temporary-message-transcript.png" });
  } finally {
    const off = await request.delete("/feature-flags", { data: { name: "workspace" } });
    expect(off.ok()).toBeTruthy();
  }
});

// On a phone the composer's controls sit above the input at fixed slots: the
// toggle gets its own, overlapping neither attach nor record nor Send.
test.describe("on a phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });

  test("the Temporary toggle overlaps no other control", async ({ page, request }) => {
    const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
    expect(on.ok()).toBeTruthy();
    try {
      await setPageFeatureFlag(page, "archiver", "https://archive-store.int.exe.xyz/");
      await page.goto("/");
      const input = page.getByTestId("message-input");
      await input.fill("echo: hi");
      await page.getByTestId("send-button").click();
      await expect(page.getByTestId("message").filter({ hasText: "echo: hi" })).toBeVisible();
      const toggle = page.getByTestId("temporary-toggle");
      await expect(toggle).toBeVisible();
      const controls = page.locator(
        ".message-input-form :is(.message-attach-btn, .message-record, .message-send-wrapper, .toc-button)",
      );
      const boxes = [];
      for (const control of await controls.all()) {
        if (await control.isVisible()) boxes.push((await control.boundingBox())!);
      }
      expect(boxes.length).toBeGreaterThanOrEqual(3);
      for (let i = 0; i < boxes.length; i++) {
        for (let j = i + 1; j < boxes.length; j++) {
          const [a, b] = [boxes[i], boxes[j]];
          const overlap =
            a.x < b.x + b.width - 1 &&
            b.x < a.x + a.width - 1 &&
            a.y < b.y + b.height - 1 &&
            b.y < a.y + a.height - 1;
          expect(overlap, `controls ${i} and ${j} overlap`).toBe(false);
        }
      }
      await page.screenshot({ path: "test-results/temporary-message-phone.png" });
    } finally {
      const off = await request.delete("/feature-flags", { data: { name: "workspace" } });
      expect(off.ok()).toBeTruthy();
    }
  });
});
