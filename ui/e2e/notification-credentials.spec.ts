import { test, expect, type Page, type Response } from "@playwright/test";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { createConversationViaAPI } from "./helpers";

async function openNotifications(page: Page) {
  await page.keyboard.press("ControlOrMeta+k");
  await page.locator(".command-palette-input").fill("notification");
  await page.locator(".command-palette-item").first().click();
  await expect(page.getByRole("button", { name: "+ Add Channel" })).toBeVisible();
}

async function savedResponse(page: Page, action: () => Promise<unknown>): Promise<Response> {
  const response = page.waitForResponse((r) =>
    r.url().includes("/api/notification-channels") && ["POST", "PUT"].includes(r.request().method()),
  );
  await action();
  const result = await response;
  expect(result.ok()).toBeTruthy();
  const body = await result.text();
  for (const secret of ["fake-webhook", "fake-token", "fake-password", "replacement-token"]) {
    expect(body).not.toContain(secret);
  }
  return result;
}

test("notification credentials stay server-side through create, edit, toggle, test and clear", async ({ page, request }) => {
  const received: { path?: string; authorization?: string }[] = [];
  const downstream = createServer((req, res) => {
    received.push({ path: req.url, authorization: req.headers.authorization });
    req.resume();
    res.writeHead(204).end();
  });
  await new Promise<void>((resolve) => downstream.listen(0, "127.0.0.1", resolve));
  const url = `http://127.0.0.1:${(downstream.address() as AddressInfo).port}`;
  try {
    const slug = await createConversationViaAPI(request, "Hello");
    await page.goto(`/c/${slug}`);
    await openNotifications(page);
    await page.getByRole("button", { name: "+ Add Channel" }).click();
    await page.locator(".provider-btn", { hasText: /^Discord Webhook$/ }).click();
    const name = page.locator(".form-group").filter({ has: page.locator("label", { hasText: /^Display Name$/ }) }).locator("input");
    await name.fill("Secret Discord");
    const webhook = page.locator("#config-webhook_url");
    await expect(webhook).toHaveAttribute("type", "password");
    await expect(webhook).toHaveAttribute("placeholder", "https://discord.com/api/webhooks/...");
    await expect(page.getByRole("button", { name: "Add Channel", exact: true })).toBeDisabled();
    await webhook.fill(`${url}/fake-webhook`);
    const discord = await (await savedResponse(page, () => page.getByRole("button", { name: "Add Channel", exact: true }).click())).json();
    expect(discord.config).toEqual({});
    expect(discord.configured_secrets).toEqual(["webhook_url"]);
    const discordCard = page.locator(".model-card").filter({ hasText: "Secret Discord" });
    await expect(discordCard).toBeVisible();
    await discordCard.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(webhook).toHaveValue("");
    await expect(webhook).toHaveAttribute("placeholder", "Leave blank to keep the saved webhook on the server");
    await expect(page.getByLabel("Clear saved credential", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
    await name.fill("Edited Discord");
    const edit = await savedResponse(page, () => page.getByRole("button", { name: "Save", exact: true }).click());
    expect(edit.request().postDataJSON().config).not.toHaveProperty("webhook_url");
    const editedCard = page.locator(".model-card").filter({ hasText: "Edited Discord" });
    await savedResponse(page, () => editedCard.getByRole("button", { name: "On", exact: true }).click());
    await expect(editedCard.getByRole("button", { name: "Off", exact: true })).toBeVisible();
    await savedResponse(page, () => editedCard.getByRole("button", { name: "Off", exact: true }).click());
    await editedCard.getByRole("button", { name: "Edit", exact: true }).click();
    await webhook.fill(`${url}/unsaved-webhook`);
    const testResponse = page.waitForResponse((r) => r.url().endsWith(`/${discord.channel_id}/test`));
    await page.getByRole("button", { name: "Test", exact: true }).click();
    expect((await (await testResponse).json()).success).toBe(true);
    expect(received.at(-1)?.path).toBe("/fake-webhook");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await editedCard.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(webhook).toHaveValue("");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();

    await page.getByRole("button", { name: "+ Add Channel" }).click();
    await page.locator(".provider-btn", { hasText: /^ntfy$/ }).click();
    await name.fill("Secret ntfy");
    await page.locator("#config-server").fill(url);
    await page.locator("#config-topic").fill("local-topic");
    await page.locator("#config-token").fill("fake-token");
    await page.locator("#config-username").fill("reader");
    await page.locator("#config-password").fill("fake-password");
    const ntfy = await (await savedResponse(page, () => page.getByRole("button", { name: "Add Channel", exact: true }).click())).json();
    expect(ntfy.configured_secrets).toEqual(["token", "password"]);
    const ntfyCard = page.locator(".model-card").filter({ hasText: "Secret ntfy" });
    await ntfyCard.getByRole("button", { name: "Edit", exact: true }).click();
    for (const key of ["token", "password"]) {
      await expect(page.locator(`#config-${key}`)).toHaveValue("");
      await expect(page.locator(`#config-${key}`)).toHaveAttribute("type", "password");
      await expect(page.locator(`#config-${key}`)).toHaveAttribute("placeholder", `Leave blank to keep the saved ${key} on the server`);
    }
    const tokenGroup = page.locator(".form-group").filter({ has: page.locator("#config-token") });
    const clearToken = tokenGroup.getByLabel("Clear saved credential", { exact: true });
    await clearToken.check();
    await expect(page.locator("#config-token")).toBeDisabled();
    await expect(page.locator("#config-token")).toHaveAttribute("placeholder", "");
    await clearToken.uncheck();
    await expect(page.locator("#config-token")).toBeEnabled();
    await expect(page.locator("#config-token")).toHaveAttribute("placeholder", "Leave blank to keep the saved token on the server");
    const preserved = await savedResponse(page, () => page.getByRole("button", { name: "Save", exact: true }).click());
    expect(preserved.request().postDataJSON().config).not.toHaveProperty("token");
    expect(preserved.request().postDataJSON().config).not.toHaveProperty("password");
    await ntfyCard.getByRole("button", { name: "Edit", exact: true }).click();
    await page.locator("#config-token").fill("replacement-token");
    const replaced = await (await savedResponse(page, () => page.getByRole("button", { name: "Save", exact: true }).click())).json();
    expect(replaced.configured_secrets).toEqual(["token", "password"]);
    await ntfyCard.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.locator("#config-token")).toHaveValue("");
    await page.locator("#config-password").fill("unsaved-password");
    await page.getByRole("button", { name: "Close modal", exact: true }).click();
    await openNotifications(page);
    await ntfyCard.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.locator("#config-password")).toHaveValue("");
    await tokenGroup.getByLabel("Clear saved credential", { exact: true }).check();
    const passwordGroup = page.locator(".form-group").filter({ has: page.locator("#config-password") });
    await passwordGroup.getByLabel("Clear saved credential", { exact: true }).check();
    for (const key of ["token", "password"]) {
      await expect(page.locator(`#config-${key}`)).toBeDisabled();
      await expect(page.locator(`#config-${key}`)).toHaveAttribute("placeholder", "");
    }
    const cleared = await savedResponse(page, () => page.getByRole("button", { name: "Save", exact: true }).click());
    expect(cleared.request().postDataJSON().config).toMatchObject({ token: "", password: "" });
    expect((await cleared.json()).configured_secrets).toEqual([]);
    const list = await request.get("/api/notification-channels");
    const listBody = await list.text();
    for (const secret of ["fake-webhook", "fake-token", "fake-password", "replacement-token"]) {
      expect(listBody).not.toContain(secret);
    }
    for (const id of [discord.channel_id, ntfy.channel_id]) {
      const get = await request.get(`/api/notification-channels/${id}`);
      const config = (await get.json()).config;
      for (const key of ["webhook_url", "token", "password"]) expect(config).not.toHaveProperty(key);
      await request.delete(`/api/notification-channels/${id}`);
    }
  } finally {
    await new Promise<void>((resolve, reject) => downstream.close((err) => err ? reject(err) : resolve()));
  }
});
