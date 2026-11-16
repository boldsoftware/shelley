import { test, expect, type APIRequestContext, type Page } from "@playwright/test";

declare global {
  interface Window {
    fragmentScrolls: string[];
  }
}

async function seed(request: APIRequestContext) {
  const response = await request.post("/debug/loremipsum?json=1", {
    form: { size: "12", model: "predictable" },
  });
  expect(response.ok()).toBeTruthy();
  const { conversation_id: id } = await response.json();
  const transcript = await request.get(`/api/conversation/${id}`);
  expect(transcript.ok()).toBeTruthy();
  const { messages, conversation } = await transcript.json();
  const users = messages.filter(
    (m: { type: string; llm_data: string }) =>
      m.type === "user" && JSON.stringify(m.llm_data).includes("Turn "),
  );
  return {
    id: id as string,
    slug: conversation.slug as string,
    first: users[0].message_id as string,
    second: users[1].message_id as string,
  };
}

function fragment(id: string) {
  return `m-${id.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8)}`;
}

async function setFragment(page: Page, hash: string) {
  await page.evaluate(
    (hash) =>
      new Promise<void>((resolve) => {
        window.addEventListener("hashchange", () => resolve(), { once: true });
        window.location.hash = hash;
      }),
    hash,
  );
}

function scrolls(page: Page) {
  return page.evaluate(() => window.fragmentScrolls);
}

async function expectFollowingAtBottom(page: Page) {
  // Leave a few pixels for fractional layout and scroll-anchor rounding.
  await expect
    .poll(() =>
      page
        .locator(".messages-container")
        .evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop),
    )
    .toBeLessThan(5);
  await expect(page.locator(".scroll-to-bottom-button")).toBeHidden();
}

async function append(page: Page, request: APIRequestContext, id: string, frames = false) {
  const marker = "fragment regression append";
  const response = await request.post(`/api/conversation/${id}/chat`, {
    data: { message: `echo: ${marker}`, model: "predictable" },
  });
  expect(response.ok()).toBeTruthy();
  let lastId = "";
  await expect(async () => {
    const response = await request.get(`/api/conversation/${id}`);
    expect(response.ok()).toBeTruthy();
    const { messages } = await response.json();
    const last = messages.at(-1);
    expect(last.end_of_turn).toBe(true);
    expect(JSON.stringify(last.llm_data)).toContain(marker);
    lastId = last.message_id;
  }).toPass();
  // The chat view publishes store updates on an animation frame. A paused
  // clock never delivers one, so tick a single frame per poll: that is far
  // below the one-second retry window the paused tests rely on.
  await expect(async () => {
    if (frames) await page.clock.runFor(16);
    await expect(page.locator(`[data-message-id="${lastId}"]`)).toBeAttached({ timeout: 250 });
  }).toPass();
}

test.beforeEach(async ({ page }) => {
  // Count every jump (including transient ones) and remove smooth-scroll timing
  // from assertions. Real scroll/layout and Vue mounting still run normally.
  await page.addInitScript(() => {
    window.fragmentScrolls = [];
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function (options) {
      const id = this.getAttribute("data-message-id");
      if (id) window.fragmentScrolls.push(id);
      original.call(
        this,
        typeof options === "object" ? { ...options, behavior: "instant" } : options,
      );
    };
  });
});

for (const navigation of ["URL", "TOC"] as const) {
  test(`${navigation} jump is not replayed after scrolling away and appending`, async ({
    page,
    request,
  }) => {
    const conversation = await seed(request);
    await page.goto(
      `/c/${conversation.slug}${navigation === "URL" ? `#${fragment(conversation.first)}` : ""}`,
    );
    await expect(page.locator(".toc-button")).toBeVisible();
    if (navigation === "TOC") {
      await page.locator(".toc-button").click();
      await page.locator(".toc-entry-user").first().click();
    }
    await expect.poll(() => scrolls(page)).toEqual([conversation.first]);
    const container = page.locator(".messages-container");
    await container.evaluate((el) => {
      el.scrollTop += 1200;
    });
    await expect(page.locator(`[data-message-id="${conversation.first}"]`)).not.toBeInViewport();
    await append(page, request, conversation.id);
    expect(await scrolls(page)).toEqual([conversation.first]);
    await expect(page.locator(`[data-message-id="${conversation.first}"]`)).not.toBeInViewport();
  });
}

test("changed fragments resolve and cancel the preceding retry", async ({ page, request }) => {
  await page.clock.install();
  const conversation = await seed(request);
  await page.goto(`/c/${conversation.slug}`);
  await expect(page.locator(".toc-button")).toBeVisible();
  const target = page.locator(`[data-message-id="${conversation.first}"]`);
  // Simulate a target whose DOM mount has not landed yet.
  await target.evaluate((el) => {
    el.setAttribute("data-delayed-message-id", el.getAttribute("data-message-id")!);
    el.removeAttribute("data-message-id");
  });
  await setFragment(page, fragment(conversation.first));
  await page.clock.runFor(100);
  await setFragment(page, fragment(conversation.second));
  await expect.poll(() => scrolls(page)).toEqual([conversation.second]);
  await page.locator("[data-delayed-message-id]").evaluate((el) => {
    el.setAttribute("data-message-id", el.getAttribute("data-delayed-message-id")!);
  });
  await page.clock.runFor(1100);
  expect(await scrolls(page)).toEqual([conversation.second]);
  await setFragment(page, fragment(conversation.first));
  await expect.poll(() => scrolls(page)).toEqual([conversation.second, conversation.first]);
});

for (const retry of ["pending", "exhausted"] as const) {
  test(`messages arriving with ${retry} retries resolve the delayed target exactly once`, async ({
    page,
    request,
  }) => {
    await page.clock.install();
    const conversation = await seed(request);
    await page.goto(`/c/${conversation.slug}`);
    await expect(page.locator(".toc-button")).toBeVisible();
    // Freeze before creating the retry, so network delivery cannot exhaust it.
    await page.clock.pauseAt(await page.evaluate(() => Date.now() + 60_000));
    await page.locator(`[data-message-id="${conversation.first}"]`).evaluate((el) => {
      el.setAttribute("data-delayed-message-id", el.getAttribute("data-message-id")!);
      el.removeAttribute("data-message-id");
    });
    await setFragment(page, fragment(conversation.first));
    if (retry === "exhausted") await page.clock.runFor(1100);
    // Hold the pending chain across real network delivery: appends must not
    // create overlapping retries that later replay an already successful jump.
    expect(await scrolls(page)).toEqual([]);
    await page.locator("[data-delayed-message-id]").evaluate((el) => {
      el.setAttribute("data-message-id", el.getAttribute("data-delayed-message-id")!);
    });
    await append(page, request, conversation.id, true);
    await page.clock.runFor(1100);
    expect(await scrolls(page)).toEqual([conversation.first]);
  });
}

test("conversation changes resolve after loading, even with the same fragment", async ({
  page,
  request,
}) => {
  await page.clock.install();
  const first = await seed(request);
  const second = await seed(request);
  // Give the second transcript the same target ID to isolate conversation
  // identity from fragment identity. No production state/hooks are changed.
  let release = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let loading = false;
  await page.route(new RegExp(`/api/conversation/${second.id}$`), async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.messages.find((m: { message_id: string }) => m.message_id === second.first).message_id =
      first.first;
    loading = true;
    await gate;
    await route.fulfill({ response, json: body });
  });
  try {
    await page.goto(`/c/${first.slug}#${fragment(first.first)}`);
    await expect.poll(() => scrolls(page)).toEqual([first.first]);
    await page.evaluate(
      (url) => {
        history.pushState(null, "", url);
        window.dispatchEvent(new PopStateEvent("popstate"));
      },
      `/c/${second.slug}#${fragment(first.first)}`,
    );
    await expect.poll(() => loading).toBe(true);
    expect(await scrolls(page)).toEqual([first.first]);
    release();
    await expect.poll(() => scrolls(page)).toEqual([first.first, first.first]);
    // Router navigation uses pushState (no hashchange). A fragmentless visit
    // must not resurrect either conversation's previous jump.
    await page.evaluate((url) => {
      history.pushState(null, "", url);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, `/c/${first.slug}`);
    await expect(page.locator(`[data-message-id="${first.second}"]`)).toBeAttached();
    await append(page, request, first.id);
    await page.clock.runFor(1100);
    expect(await scrolls(page)).toEqual([first.first, first.first]);
  } finally {
    release();
  }
});

test("invalid anchors leave follow enabled; removing a fragment cancels retries", async ({
  page,
  request,
}) => {
  await page.clock.install();
  const conversation = await seed(request);
  await page.goto(`/c/${conversation.slug}`);
  await expect(page.locator(".toc-button")).toBeVisible();
  await expectFollowingAtBottom(page);
  for (const hash of ["m-", "t-", "unrelated", "m-%21", "m-deadbeef"]) {
    await setFragment(page, hash);
    await page.clock.runFor(1100);
    expect(await scrolls(page)).toEqual([]);
    await expectFollowingAtBottom(page);
  }
  // Follow must remain armed, not merely leave the current position unchanged.
  await append(page, request, conversation.id);
  await expectFollowingAtBottom(page);
  await expect
    .poll(() =>
      page
        .locator(".messages-container")
        .evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop),
    )
    .toBeLessThan(5);

  await page.locator(`[data-message-id="${conversation.first}"]`).evaluate((el) => {
    el.setAttribute("data-delayed-message-id", el.getAttribute("data-message-id")!);
    el.removeAttribute("data-message-id");
  });
  await setFragment(page, fragment(conversation.first));
  await page.clock.runFor(100);
  await setFragment(page, "");
  await page.locator("[data-delayed-message-id]").evaluate((el) => {
    el.setAttribute("data-message-id", el.getAttribute("data-delayed-message-id")!);
  });
  await page.clock.runFor(1100);
  expect(await scrolls(page)).toEqual([]);
});

test("fresh conversation keeps bottom restoration through startup clamps", async ({
  page,
  request,
}) => {
  const conversation = await seed(request);
  await page.goto(`/c/${conversation.slug}`);
  await expect(page.locator(".toc-button")).toBeVisible();

  const container = page.locator(".messages-container");
  await expectFollowingAtBottom(page);
  // A first visit still means "restore to bottom". Force a layout clamp's
  // scroll event ahead of ResizeObserver without any wheel/touch gesture;
  // the temporary pixel offset must not be persisted as user navigation.
  const scrollKey = `shelley_scroll_${conversation.id}`;
  const savedAfterStartupClamp = await container.evaluate(async (element, key) => {
    const list = element.querySelector(".messages-list");
    const sentinel = element.querySelector(".messages-bottom-sentinel");
    if (!list || !sentinel) throw new Error("message list sentinel not found");
    const spacer = document.createElement("div");
    spacer.style.height = "600px";
    list.insertBefore(spacer, sentinel);
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    element.scrollTop = element.scrollHeight;
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    spacer.remove();
    void element.scrollTop;
    element.dispatchEvent(new Event("scroll"));
    window.dispatchEvent(new Event("beforeunload"));
    return localStorage.getItem(key);
  }, scrollKey);
  expect(savedAfterStartupClamp).toBe("bottom");
  await expectFollowingAtBottom(page);
});

test("scroll-to-bottom button clears the current fragment", async ({ page, request }) => {
  const conversation = await seed(request);
  await page.goto(`/c/${conversation.slug}`);
  await expect(page.locator(".toc-button")).toBeVisible();

  const container = page.locator(".messages-container");
  await expectFollowingAtBottom(page);
  // Release the production bottom pin as an upward wheel gesture would, then
  // set the deterministic destination instead of relying on wheel hit-testing.
  await container.dispatchEvent("wheel", { deltaY: -10000 });
  await container.evaluate((element) => {
    element.scrollTop = 0;
  });
  await expect.poll(() => container.evaluate((element) => element.scrollTop)).toBe(0);
  await expect(page.locator(".scroll-to-bottom-button")).toBeVisible();
  await page.evaluate(() => history.replaceState(null, "", `${window.location.pathname}#current`));
  await page.locator(".scroll-to-bottom-button").click();

  await expect.poll(() => page.evaluate(() => window.location.hash)).toBe("");
  await expect(page.locator(".scroll-to-bottom-button")).toBeHidden();
});
