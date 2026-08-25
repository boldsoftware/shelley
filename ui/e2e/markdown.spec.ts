import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

// All tests create the conversation via the API and then navigate directly to
// it. This avoids the SSE subscribe-vs-publish race that occurs when the
// browser opens a brand-new conversation while the first turn is still being
// recorded (see helpers.ts), which otherwise flakes waitForSelector(".message-agent").
// Split out of the original markdown.spec.ts, which at 47s of test time was
// one of the specs gating the playwright shards (see
// .buildkite/steps/shelley-playwright-shard.py -- files are the sharding unit,
// so a single file cannot be spread across lanes). The two halves are
// independent: every test creates its own conversation via the API.
test.describe("Markdown rendering", () => {
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

  test("highlights bundled languages and aliases without changing plain fences", async ({ page, request }) => {
    const highlightedFences = [
      ["elixir", "answer = 42\n"],
      ["haskell", "answer :: Int\nanswer = 42\n"],
      ["zig", "const answer: i32 = 42;\n"],
      ["Dockerfile", "FROM alpine:latest\n"],
      ["nix", "answer = 42;\n"],
      ["clojure", "(def answer 42)\n"],
      ["clj", "(def alias-answer 42)\n"],
    ] as const;
    const unknownSource = "leave_this_plain();\n";
    const unlabeledSource = "also_plain();\n";
    const markdown = [
      ...highlightedFences.map(([language, source]) => `\`\`\`${language}\n${source}\`\`\``),
      `\`\`\`unknown-language\n${unknownSource}\`\`\``,
      `\`\`\`\n${unlabeledSource}\`\`\``,
    ].join("\n\n");
    const slug = await createConversationViaAPI(request, `markdown: ${markdown}`);
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const blocks = page.locator(".message-agent").last().locator("pre > code");
    await expect(blocks).toHaveCount(highlightedFences.length + 2, { timeout: 30000 });

    for (const [index, [language, source]] of highlightedFences.entries()) {
      const block = blocks.nth(index);
      await expect(block).toHaveClass(`language-${language}`);
      const tokens = block.locator(".shelley-code-token");
      await expect(tokens.first()).toBeAttached({ timeout: 30000 });
      expect(await block.textContent()).toBe(source);
    }

    const unknown = blocks.nth(highlightedFences.length);
    const unlabeled = blocks.nth(highlightedFences.length + 1);
    await expect(unknown).toHaveClass("language-unknown-language");
    expect(await unlabeled.getAttribute("class")).toBeNull();
    expect(await unknown.textContent()).toBe(unknownSource);
    expect(await unlabeled.textContent()).toBe(unlabeledSource);
    await expect(unknown.locator(".shelley-code-token")).toHaveCount(0);
    await expect(unlabeled.locator(".shelley-code-token")).toHaveCount(0);

    const tokens = blocks.nth(0).locator(".shelley-code-token");
    const tokenCount = await tokens.count();
    const lightColor = await tokens.first().evaluate((token) => getComputedStyle(token).color);
    await page.locator("html").evaluate((root) => root.classList.toggle("dark", true));
    await expect
      .poll(() => tokens.first().evaluate((token) => getComputedStyle(token).color))
      .not.toBe(lightColor);
    expect(await tokens.count()).toBe(tokenCount);
  });

  test("defers fence highlighting until blocks come near the viewport", async ({
    page,
    request,
  }) => {
    // Every loremipsum turn carries a ```go fence. In a 17k-message
    // conversation that was 5,000 eagerly-highlighted blocks = 65k token
    // spans = 22% of the DOM, almost all of it dozens of screens above the
    // fold. Highlighting must instead hydrate like tool cards do: only once
    // the block comes within a viewport of view (sticky thereafter).
    const generated = await request.post("/debug/loremipsum?json=1", {
      form: { size: "medium", model: "predictable" },
    });
    expect(generated.ok()).toBeTruthy();
    const { conversation_id: conversationId } = await generated.json();

    await page.goto(`/c/${conversationId}`);
    await expect(page.getByTestId("message").first()).toBeVisible({ timeout: 30000 });

    // Loaded pinned to the bottom: the newest fence highlights. This wait is
    // also the barrier that makes the negative assertions below meaningful —
    // eager highlighting (the old behavior) would have tokenized every block
    // by the time the last one is done.
    const blocks = page.locator(".message-agent pre > code");
    await expect(blocks.last().locator(".shelley-code-token").first()).toBeAttached({
      timeout: 30000,
    });
    expect(await blocks.count()).toBe(50);

    // The first fence, far above the fold, stays a plain text node, and the
    // total token count is bounded by the handful of on-screen blocks (each
    // loremipsum fence is ~13 tokens; 50 eager blocks would be ~650).
    await expect(blocks.first().locator(".shelley-code-token")).toHaveCount(0);
    expect(await page.locator(".shelley-code-token").count()).toBeLessThan(200);

    // Scrolling it into the scrollport hydrates it. The wheel gesture first:
    // without it auto-follow is still armed and its rAF pin (see
    // ChatInterface.vue handleScroll/lastScrollGestureAt) snaps the view
    // straight back to the bottom — webkit loses that race reliably. The
    // shared observer's rootMargin lookahead cannot see through the scroll
    // container's clip, so hydration lands on scrollport entry — invisible
    // for highlighting, which changes colors, never geometry.
    await page.evaluate(() => {
      const container = document.querySelector(".messages-container")!;
      container.dispatchEvent(new WheelEvent("wheel", { deltaY: -200, bubbles: true }));
      document.querySelector(".message-agent pre > code")!.scrollIntoView({ block: "center" });
    });
    await expect(blocks.first().locator(".shelley-code-token").first()).toBeAttached({
      timeout: 30000,
    });
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

  test("coalesces web-search citation blocks into one paragraph with markers", async ({
    page,
    request,
  }) => {
    // The "web search" predictable pattern returns a server-side web-search
    // message: a server_tool_use block, a web_search_tool_result, and many
    // small text blocks where cited quotes carry a Citations array. The UI must
    // merge adjacent text blocks (no stray line breaks) and surface inline
    // citation markers + a numbered Sources list.
    const slug = await createConversationViaAPI(request, "web search");
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState("domcontentloaded");

    const agent = page.locator(".message-agent").last();
    const content = agent.locator('[data-testid="message-content"]');
    await expect(content).toContainText("mid-session model switching", { timeout: 30000 });

    // The sentence that used to be split across blocks now reads continuously
    // (an inline citation marker may sit between the two halves).
    await expect(content).toContainText(/never lose work.*so model switching pairs well/s);

    // Inline citation markers render as superscript source links.
    expect(await content.locator("sup.citation-refs a.citation-ref").count()).toBeGreaterThan(0);

    // A numbered Sources list is appended for the cited run.
    const sources = content.locator("ol.citation-sources li.citation-source");
    expect(await sources.count()).toBeGreaterThan(0);
    await expect(sources.first().locator("a")).toHaveAttribute("href", /^https?:\/\//);
  });
});
