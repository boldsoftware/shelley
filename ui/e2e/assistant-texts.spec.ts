import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";

// In workspace mode, texted messages go to the Assistant, marked "via text".
test("a message texted to the Assistant is marked via text", async ({ page, request }) => {
  const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(on.ok()).toBeTruthy();
  try {
    await page.goto("/");
    await expect(page.locator("h1.header-title")).toHaveText("Assistant");
    const text = `texted ${randomUUID()}`;
    const texted = await request.post(`/api/debug/channels/debug-${randomUUID()}/receive`, {
      headers: { "X-Shelley-Request": "1" },
      data: { text },
    });
    expect(texted.ok()).toBeTruthy();
    const message = page.getByTestId("message").filter({ hasText: text });
    await expect(message.getByTestId("message-via-text")).toHaveText("via text");
  } finally {
    const off = await request.delete("/feature-flags", { data: { name: "workspace" } });
    expect(off.ok()).toBeTruthy();
  }
});
