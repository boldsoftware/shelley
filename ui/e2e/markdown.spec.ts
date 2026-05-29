import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// All tests create the conversation via the API and then navigate directly to
// it. This avoids the SSE subscribe-vs-publish race that occurs when the
// browser opens a brand-new conversation while the first turn is still being
// recorded (see helpers.ts), which otherwise flakes waitForSelector(".message-agent").
test.describe("Markdown rendering and sanitization", () => {
  test("renders markdown formatting in agent messages", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      "markdown: **bold** and *italic* and `code`",
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    // Markdown should be rendered as HTML elements
    await expect(agent.locator("strong")).toContainText("bold", { timeout: 30000 });
    await expect(agent.locator("em")).toContainText("italic");
    await expect(agent.locator("code")).toContainText("code");
  });

  test("strips script tags from agent messages", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: hello <script>alert("xss")</script> world',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    // The text should be there, but no script element
    await expect(agent).toContainText("hello", { timeout: 30000 });
    await expect(agent).toContainText("world");
    expect(await agent.locator("script").count()).toBe(0);
    // Also confirm the alert text doesn't appear anywhere in the raw HTML
    const html = await agent.innerHTML();
    expect(html).not.toContain("<script");
    expect(html).not.toContain("alert");
  });

  test("strips remote img tags (image tracking)", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      "markdown: ![tracker](https://evil.com/pixel.gif) safe text",
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe text", { timeout: 30000 });
    expect(await agent.locator("img").count()).toBe(0);
    const html = await agent.innerHTML();
    expect(html).not.toContain("<img");
    expect(html).not.toContain("evil.com");
  });

  test("renders local images via the per-message file endpoint", async ({ page, request }) => {
    // The "inline image" predictable pattern writes a tiny PNG into the
    // conversation cwd (/tmp) via bash, then references it with relative-path
    // markdown. The UI should rewrite the src to /api/message/{id}/file and
    // load the bytes from the server.
    const slug = await createConversationViaAPI(request, "inline image", { agentTimeout: 60000 });
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const img = page.locator(".message-agent img").last();
    await expect(img).toBeVisible({ timeout: 30000 });
    const src = await img.getAttribute("src");
    expect(src).toMatch(/^\/api\/message\/[^/]+\/file\?path=/);

    // The browser should successfully fetch the image bytes (naturalWidth > 0
    // only when the image actually loaded).
    await expect
      .poll(async () => img.evaluate((el: HTMLImageElement) => el.naturalWidth), {
        timeout: 15000,
      })
      .toBeGreaterThan(0);
  });

  test("strips iframe tags", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <iframe src="https://evil.com"></iframe> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    expect(await agent.locator("iframe").count()).toBe(0);
  });

  test("strips event handler attributes", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <div onclick="alert(1)">click me</div>',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("click me", { timeout: 30000 });
    const html = await agent.innerHTML();
    expect(html).not.toContain("onclick");
    expect(html).not.toContain("alert");
  });

  test("sanitizes javascript: href in links", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <a href="javascript:alert(document.cookie)">steal cookies</a>',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("steal cookies", { timeout: 30000 });
    const html = await agent.innerHTML();
    expect(html).not.toContain("javascript:");
  });

  test("markdown links open in new tab with noopener", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      "markdown: [example](https://example.com)",
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const link = page.locator(".message-agent").last().locator("a").first();
    await expect(link).toHaveAttribute("href", "https://example.com", { timeout: 30000 });
    await expect(link).toHaveAttribute("target", "_blank");
    await expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  test("user messages never render markdown", async ({ page, request }) => {
    // Send a message with markdown syntax - user messages should show raw text
    const slug = await createConversationViaAPI(request, "**bold** and *italic*");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const user = page.locator(".message-user").last();
    // The raw markdown characters should be visible
    await expect(user).toContainText("**bold**", { timeout: 30000 });
    // User message should NOT have <strong> or <em> — should be plain text
    expect(await user.locator("strong").count()).toBe(0);
    expect(await user.locator("em").count()).toBe(0);
  });

  test("strips SVG with embedded script", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <svg onload="alert(1)"><circle r="50"/></svg> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    const html = await agent.innerHTML();
    expect(html).not.toContain("<svg");
    expect(html).not.toContain("onload");
  });

  test("strips non-checkbox input elements (phishing prevention)", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <input type="text" placeholder="Enter password"> <input type="password"> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    // Text and password inputs should be stripped
    expect(await agent.locator('input[type="text"]').count()).toBe(0);
    expect(await agent.locator('input[type="password"]').count()).toBe(0);
  });

  test("strips form and input[type=submit] (phishing prevention)", async ({ page, request }) => {
    const slug = await createConversationViaAPI(
      request,
      'markdown: <form action="https://evil.com/steal"><button type="submit">Login</button></form> safe',
    );
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    await expect(agent).toContainText("safe", { timeout: 30000 });
    // Inspect just the rendered markdown content; the surrounding action bar
    // legitimately contains <button> (copy/usage) and must be excluded.
    const content = agent.locator('[data-testid="message-content"]');
    const html = await content.innerHTML();
    expect(html).not.toContain("<form");
    expect(html).not.toContain("<button");
    expect(html).not.toContain("evil.com");
  });
});
