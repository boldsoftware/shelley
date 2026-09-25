import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";
import type { AttachedIntegration } from "../src/services/api";

const gh: AttachedIntegration = {
  name: "gh",
  type: "github",
  url: "https://github.int.example.test",
  help: "git clone https://github.int.example.test/owner/project.git",
  details: {
    repositories: [
      {
        name: "owner/project",
        url: "https://github.com/owner/project",
        clone_command: "git clone https://github.int.example.test/owner/project.git",
      },
    ],
  },
};
const llm: AttachedIntegration = {
  name: "llm",
  type: "llm",
  url: "https://llm.int.example.test",
  details: {
    model_counts: [
      { provider: "anthropic", mode: "managed" },
      { provider: "openai", mode: "api_key", chat: 3 },
    ],
  },
};
const reflection: AttachedIntegration = {
  name: "reflection",
  type: "reflection",
  url: "https://reflection.int.example.test",
  help: "curl https://reflection.int.example.test/",
};
const notify: AttachedIntegration = {
  name: "notify",
  type: "notify",
  url: "https://notify.int.example.test",
};
const slackEntries: AttachedIntegration[] = [
  {
    name: "slack-hook",
    type: "slack",
    url: "https://slack-hook.int.example.test",
  },
  {
    name: "slack-hook",
    type: "slack",
    team: true,
    url: "https://slack-hook.team.example.test",
  },
];

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function fixture(page: Page, entries = [gh, llm, notify, reflection]) {
  const detailRequests: string[] = [];
  await page.addInitScript(() => {
    let init: Record<string, unknown>;
    Object.defineProperty(window, "__SHELLEY_INIT__", {
      configurable: true,
      get: () => init,
      set: (value) => {
        init = { ...value, is_exe_dev: true };
      },
    });
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: async (text: string) => {
          (window as unknown as { copiedIntegrationText: string }).copiedIntegrationText = text;
        },
      },
    });
  });
  await page.route("**/api/integrations**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() === "POST") {
      await route.fulfill({ status: 503, body: "Integration sends must be mocked" });
      return;
    }
    const name = url.searchParams.get("details");
    if (name) {
      detailRequests.push(name);
      await route.fulfill({
        json: {
          integrations: entries.filter(
            (entry) =>
              entry.name === name && !!entry.team === (url.searchParams.get("team") === "true"),
          ),
        },
      });
    } else {
      await route.fulfill({
        json: { integrations: entries.map(({ details: _details, ...entry }) => entry) },
      });
    }
  });
  return detailRequests;
}

async function openPanel(page: Page) {
  await page.keyboard.press("Control+k");
  const search = page.locator(".command-palette-input");
  await expect(search).toBeVisible();
  await search.fill("integration");
  await page.locator(".command-palette-item").first().click();
  await expect(page.locator(".integrations-modal")).toBeVisible();
}

async function select(page: Page, name: string) {
  await page.locator(".integrations-tabs button").first().click();
  await page
    .locator(".integrations-name-btn")
    .filter({ hasText: new RegExp(`^${name} `) })
    .click();
  await expect(page.locator(".integrations-detail-head strong")).toHaveText(name);
}

test("keeps the Slack selector visible with a single hook", async ({ page }) => {
  await fixture(page, [slackEntries[0]]);
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "slack-hook");
  await expect(page.locator("#slack-hook")).toBeVisible();
  await expect(page.locator("#slack-hook option")).toHaveText(["slack-hook · Personal"]);
  await expect(
    page.getByRole("button", { name: "Test integration (webhook only)", exact: true }),
  ).toBeVisible();
});

test("offers an explicit webhook-only test and explains bot failures", async ({ page }) => {
  const bot: AttachedIntegration = {
    ...slackEntries[1],
    help: 'curl --json \'{"channel":"C123","text":"hello"}\' https://slack-hook.team.example.test/api/chat.postMessage',
  };
  await fixture(page, [slackEntries[0], bot]);
  let sends = 0;
  await page.route("**/api/integrations/slack/test", (route) => {
    sends++;
    expect(route.request().postDataJSON()).toEqual({
      name: "slack-hook",
      team: true,
      message: "Hello bot",
    });
    return route.fulfill({
      status: 422,
      body: "This is a Slack bot. This test only supports incoming webhooks.",
    });
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await page.locator(".integrations-name-btn").first().click();
  const button = page.getByRole("button", { name: "Test integration (webhook only)", exact: true });
  await expect(button).toBeEnabled();
  await expect(page.locator(".slack-access")).toHaveText(
    "For incoming webhooks only. Slack bots are not supported by this test.",
  );
  await page.locator("#slack-hook").selectOption("team:slack-hook");
  await expect(button).toBeEnabled();
  await page.locator(".integrations-guide summary").click();
  await expect(page.locator(".integrations-guide pre")).toHaveText(bot.help!);
  expect(sends).toBe(0);
  await page.locator("#slack-message").fill("Hello bot");
  await button.click();
  await expect(page.locator(".slack-integration").getByRole("alert")).toHaveText(
    "This is a Slack bot. This test only supports incoming webhooks.",
  );
  expect(sends).toBe(1);
});

test("refreshes Slack destinations and returns to the list when the selection is detached", async ({
  page,
}) => {
  const entries = [slackEntries[0]];
  await fixture(page, entries);
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "slack-hook");
  await page.locator("#slack-message").fill("Keep this draft");
  entries.push(slackEntries[1]);
  const refresh = page
    .locator(".integrations-modal")
    .getByRole("button", { name: "Refresh", exact: true });
  await refresh.click();
  await expect(page.locator("#slack-hook option")).toHaveText([
    "slack-hook · Personal",
    "slack-hook · Team",
  ]);
  await expect(page.locator("#slack-message")).toHaveValue("Keep this draft");
  await page.locator("#slack-hook").selectOption("team:slack-hook");
  entries.splice(1, 1);
  await refresh.click();
  await expect(page.locator(".integrations-name-btn")).toHaveCount(1);
  await expect(page.locator(".integrations-detail")).toHaveCount(0);
  await page.locator(".integrations-name-btn").click();
  await expect(page.locator("#slack-hook option")).toHaveText(["slack-hook · Personal"]);
});

test("switches same-name Slack hooks by scope and locks the submitted destination", async ({
  page,
}) => {
  await fixture(page, slackEntries);
  const sent = deferred();
  let payload: unknown;
  await page.route("**/api/integrations/slack/test", async (route) => {
    payload = route.request().postDataJSON();
    await sent.promise;
    await route.fulfill({ status: 204 });
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await page.locator(".integrations-name-btn").first().click();
  const input = page.locator("#slack-message");
  const selector = page.locator("#slack-hook");
  await expect(selector).toHaveValue("personal:slack-hook");
  await input.fill("Keep this draft");
  await selector.selectOption("team:slack-hook");
  await expect(selector).toHaveValue("team:slack-hook");
  await expect(input).toHaveValue("Keep this draft");
  await expect(page.locator(".integrations-detail-head")).toContainText("slack · Team");
  await page.locator(".integrations-advanced summary").click();
  await expect(page.locator(".integrations-copy-line code").first()).toHaveText(
    slackEntries[1].url,
  );
  await page.getByRole("button", { name: "Test integration (webhook only)", exact: true }).click();
  await expect(selector).toBeDisabled();
  await expect(input).toBeDisabled();
  await expect(
    page.locator(".integrations-modal").getByRole("button", { name: "Refresh", exact: true }),
  ).toBeDisabled();
  await expect(page.locator(".integrations-tabs button").first()).toBeDisabled();
  await expect
    .poll(() => payload)
    .toEqual({
      name: "slack-hook",
      team: true,
      message: "Keep this draft",
    });
  sent.resolve();
  await expect(page.locator(".slack-integration").getByRole("status")).toHaveText(
    "Message sent to slack-hook.",
  );
  await expect(selector).toBeEnabled();
  await page.route("**/api/integrations/slack/test", (route) =>
    route.fulfill({ status: 502, body: "Slack rejected the test" }),
  );
  await page.getByRole("button", { name: "Test integration (webhook only)", exact: true }).click();
  await expect(page.locator(".slack-integration").getByRole("alert")).toContainText(
    "Slack rejected the test",
  );
});

test("parses the GitHub repository from reflection help and copies its clone command", async ({
  page,
}) => {
  const detailRequests = await fixture(page);
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await expect(page.locator(".integrations-name-btn")).toHaveCount(4);
  expect(detailRequests).toEqual([]);
  await select(page, "gh");
  const table = page.locator(".integrations-gh-table");
  await expect(table.getByRole("link", { name: "owner/project" })).toHaveAttribute(
    "href",
    "https://github.com/owner/project",
  );
  await table.getByRole("button", { name: "Copy", exact: true }).click();
  await expect
    .poll(() =>
      page.evaluate(
        () => (window as unknown as { copiedIntegrationText: string }).copiedIntegrationText,
      ),
    )
    .toBe(gh.help);
  await expect(page.locator(".integrations-guide")).not.toHaveAttribute("open");
  expect(detailRequests).toEqual(["gh"]);
  await expect(page.locator(".integrations-modal").getByRole("tab")).toHaveCount(0);
  const attached = page.locator(".integrations-tabs button").first();
  await attached.focus();
  await page.keyboard.press("Enter");
  await expect(page.locator(".integrations-name-btn")).toHaveCount(4);
});

for (const team of [false, true]) {
  test(`copies an edit command with intact SSH arguments (${team ? "team" : "personal"})`, async ({
    page,
  }) => {
    await fixture(page, [{ ...gh, team }]);
    await page.goto("/new");
    await expect(page.getByTestId("message-input")).toBeVisible();
    await openPanel(page);
    await select(page, "gh");
    await page.locator(".integrations-advanced summary").click();
    await page.getByRole("button", { name: "Copy edit command", exact: true }).click();
    await expect(page.locator(".integrations-copy-line").nth(1).getByRole("button")).toHaveText(
      "Copied",
    );
    const command = await page.evaluate(
      () => (window as unknown as { copiedIntegrationText: string }).copiedIntegrationText,
    );
    const sshArgs = execFileSync(
      "sh",
      ["-c", `ssh() { printf '%s\\0' "$@"; }\n${command}`],
      { encoding: "utf8" },
    ).split("\0");
    expect(sshArgs).toEqual([
      "exe.dev",
      `integrations edit 'gh'${team ? " --team" : ""} --comment='<new comment>'`,
      "",
    ]);
    const editArgs = execFileSync(
      "sh",
      ["-c", `integrations() { printf '%s\\0' "$@"; }\n${sshArgs[1]}`],
      { encoding: "utf8" },
    ).split("\0");
    expect(editArgs).toEqual(["edit", "gh", ...(team ? ["--team"] : []), "--comment=<new comment>", ""]);
  });
}

test("shows what reflection reports without account access", async ({ page }) => {
  await fixture(page);
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "llm");
  await expect(page.locator(".integrations-providers-table tbody tr")).toHaveText([
    /anthropic\s*exe\.dev managed\s*—/,
    /openai\s*api_key\s*3 chat/,
  ]);
  await select(page, "reflection");
  await expect(page.locator(".integrations-guide")).toContainText(reflection.help!);
  await page.locator(".integrations-advanced summary").click();
  await expect(page.locator(".integrations-advanced")).toContainText(reflection.url);
  await select(page, "notify");
  await expect(page.locator(".integrations-modal").getByRole("alert")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Send test notification", exact: true }),
  ).toBeEnabled();
});

test("ignores a stale list response after reopening", async ({ page }) => {
  await fixture(page);
  const oldResponse = deferred();
  let requests = 0;
  await page.route("**/api/integrations", async (route) => {
    requests++;
    if (requests === 1) {
      await oldResponse.promise;
      await route.fulfill({ json: { integrations: [gh] } });
    } else {
      await route.fulfill({ json: { integrations: [llm] } });
    }
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await expect.poll(() => requests).toBe(1);
  await page.locator(".integrations-modal").getByRole("button", { name: "Close modal" }).click();
  await openPanel(page);
  await expect(page.locator(".integrations-name-btn")).toHaveText(["llm →"]);
  const response = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/integrations");
  oldResponse.resolve();
  await (await response).finished();
  await page.evaluate(() => new Promise(requestAnimationFrame));
  await expect(page.locator(".integrations-name-btn")).toHaveText(["llm →"]);
});

test("clears abandoned detail loading without applying its late result", async ({ page }) => {
  await fixture(page);
  const oldDetail = deferred();
  let requested = false;
  await page.route("**/api/integrations?details=gh*", async (route) => {
    requested = true;
    await oldDetail.promise;
    await route.fulfill({ json: { integrations: [gh] } });
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "gh");
  await expect.poll(() => requested).toBe(true);
  await page.locator(".integrations-modal").getByRole("button", { name: "Close modal" }).click();
  await openPanel(page);
  await expect(page.locator(".integrations-name-btn")).toHaveCount(4);
  await expect(
    page.locator(".integrations-modal").getByRole("button", { name: "Refresh", exact: true }),
  ).toBeEnabled();
  const response = page.waitForResponse(
    (r) => new URL(r.url()).searchParams.get("details") === "gh",
  );
  oldDetail.resolve();
  await (await response).finished();
  await select(page, "llm");
  await expect(page.locator(".integrations-providers-table")).toBeVisible();
  await expect(page.locator(".integrations-guide")).toHaveCount(0);
});

test("keeps a notification send locked after reopening and discards its stale result", async ({
  page,
}) => {
  await fixture(page);
  const sent = deferred();
  let message: string | undefined;
  let sendCount = 0;
  await page.route("**/api/integrations/notify/test", async (route) => {
    sendCount++;
    message = route.request().postDataJSON().message;
    await sent.promise;
    await route.fulfill({ status: 204 });
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "notify");
  const input = page.locator("#integrations-test-message");
  await input.fill("My notification test");
  await page.getByRole("button", { name: "Send test notification", exact: true }).click();
  await expect(input).toBeDisabled();
  await expect.poll(() => message).toBe("My notification test");
  await page.locator(".integrations-modal").getByRole("button", { name: "Close modal" }).click();
  await openPanel(page);
  await page
    .locator(".integrations-name-btn")
    .filter({ hasText: /^notify / })
    .click();
  await expect(input).toBeDisabled();
  await expect(page.locator(".integrations-notify button")).toBeDisabled();
  await page
    .locator(".integrations-notify form")
    .evaluate((form) =>
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })),
    );
  expect(sendCount).toBe(1);
  const response = page.waitForResponse("**/api/integrations/notify/test");
  sent.resolve();
  await response;
  await page.evaluate(() => new Promise(requestAnimationFrame));
  await expect(input).toBeEnabled();
  await expect(page.locator(".integrations-notify").getByRole("status")).toHaveCount(0);
  await page.route("**/api/integrations/notify/test", (route) => route.fulfill({ status: 204 }));
  await page.getByRole("button", { name: "Send test notification", exact: true }).click();
  await expect(page.locator(".integrations-notify").getByRole("status")).toContainText(/accepted/i);
  await page.route("**/api/integrations/notify/test", (route) =>
    route.fulfill({ status: 502, body: "Gateway rejected test" }),
  );
  await page.getByRole("button", { name: "Send test notification", exact: true }).click();
  await expect(page.locator(".integrations-notify").getByRole("alert")).toContainText(
    "Gateway rejected test",
  );
});

test("keeps a Slack send locked after reopening and releases it after a hidden failure", async ({
  page,
}) => {
  await fixture(page, [slackEntries[0]]);
  const sent = deferred();
  let sendCount = 0;
  await page.route("**/api/integrations/slack/test", async (route) => {
    sendCount++;
    await sent.promise;
    await route.fulfill({ status: 502, body: "Old send failed" });
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "slack-hook");
  const input = page.locator("#slack-message");
  await input.fill("Keep this draft");
  await page.getByRole("button", { name: "Test integration (webhook only)", exact: true }).click();
  await expect.poll(() => sendCount).toBe(1);
  await page.keyboard.press("Escape");
  await expect(page.locator(".integrations-modal")).toBeHidden();
  await openPanel(page);
  await page.locator(".integrations-name-btn").click();
  await expect(input).toBeDisabled();
  await expect(input).toHaveValue("Keep this draft");
  await expect(page.locator("#slack-hook")).toBeDisabled();
  await page
    .locator(".slack-integration form")
    .evaluate((form) =>
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })),
    );
  expect(sendCount).toBe(1);
  await page.keyboard.press("Escape");
  const response = page.waitForResponse("**/api/integrations/slack/test");
  sent.resolve();
  await response;
  await openPanel(page);
  await page.locator(".integrations-name-btn").click();
  await expect(input).toBeEnabled();
  await expect(page.locator(".slack-integration").getByRole("alert")).toHaveCount(0);
  await page.route("**/api/integrations/slack/test", (route) => route.fulfill({ status: 204 }));
  await page.getByRole("button", { name: "Test integration (webhook only)", exact: true }).click();
  await expect(page.locator(".slack-integration").getByRole("status")).toHaveText(
    "Message sent to slack-hook.",
  );
});

test("uses the selected language for the panel and command", async ({ page }) => {
  await fixture(page, [gh, llm, notify, reflection, slackEntries[0]]);
  await page.addInitScript(() => localStorage.setItem("shelley-locale", "es"));
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await expect(page.locator(".integrations-modal .modal-title")).not.toHaveText("VM integrations");
  await expect(page.locator(".integrations-modal .modal-title")).toContainText(/integraciones/i);
  await select(page, "notify");
  await expect(page.locator(".integrations-notify button")).not.toHaveText(
    "Send test notification",
  );
  await page.route("**/api/integrations/slack/test", (route) =>
    route.fulfill({
      status: 422,
      body: "This is a Slack bot. This test only supports incoming webhooks.",
    }),
  );
  await select(page, "slack-hook");
  await page
    .getByRole("button", { name: "Probar integración (solo webhook)", exact: true })
    .click();
  await expect(page.locator(".slack-integration").getByRole("alert")).toHaveText(
    "Esta integración es un bot de Slack. Esta prueba solo admite webhooks entrantes.",
  );
});

test("locks refresh and tabs while a notification is sending", async ({ page }) => {
  await fixture(page);
  const sent = deferred();
  let requested = false;
  await page.route("**/api/integrations/notify/test", async (route) => {
    requested = true;
    await sent.promise;
    await route.fulfill({ status: 204 });
  });
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await openPanel(page);
  await select(page, "notify");
  await page.getByRole("button", { name: "Send test notification", exact: true }).click();
  await expect.poll(() => requested).toBe(true);
  const refresh = page
    .locator(".integrations-modal")
    .getByRole("button", { name: "Refresh", exact: true });
  await expect(refresh).toBeDisabled();
  await expect(page.locator(".integrations-tabs button").first()).toBeDisabled();
  sent.resolve();
  await expect(page.locator(".integrations-notify").getByRole("status")).toContainText(/accepted/i);
  await expect(refresh).toBeEnabled();
});
