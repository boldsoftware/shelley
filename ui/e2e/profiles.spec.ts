import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

// The model picker is the conversation's settings knob: model, reasoning,
// tools (its Edit…) and profiles (pills, its Edit…), all of which a live
// conversation can change, each change marked in the transcript.
test.describe("Settings knob", () => {
  async function openKnob(page: Page) {
    await page.locator(".model-picker-inline .p-select-label").click();
    await expect(page.locator(".model-picker-panel")).toBeVisible();
  }
  async function editTools(page: Page) {
    const panel = page.locator(".model-picker-panel");
    await panel.getByTestId("model-picker-tools").getByRole("button", { name: /^Edit/ }).click();
    await expect(panel).toBeHidden();
  }

  test("Tools' Edit… changes a live conversation's tools", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: tools later",
    );
    await page.goto(`/c/${slug}`);
    await openKnob(page);
    await editTools(page);
    const dialog = page.getByRole("dialog");
    await dialog
      .getByRole("radiogroup", { name: "browser", exact: true })
      .getByRole("radio", { name: "Off" })
      .click();
    await dialog.getByRole("button", { name: "Apply to this conversation" }).click();
    await expect(dialog).toBeHidden();

    await expect(page.getByTestId("message-modelchange")).toHaveAttribute(
      "aria-label",
      "Tools turned off: browser.",
    );
    const body = await (await request.get(`/api/conversation/${conversationId}`)).json();
    expect(JSON.parse(body.conversation.conversation_options).tool_overrides).toEqual({
      browser: "off",
    });
  });

  test("saves changed settings as a profile, edits it, and switches", async ({ page, request }) => {
    const name = `E2E ${Date.now()}`;
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: profiles",
    );
    try {
      await page.goto(`/c/${slug}`);
      const panel = page.locator(".model-picker-panel");
      const markers = page.getByTestId("message-modelchange");
      await openKnob(page);
      await editTools(page);
      const dialog = page.getByRole("dialog");
      await dialog
        .getByRole("radiogroup", { name: "browser", exact: true })
        .getByRole("radio", { name: "Off" })
        .click();
      await dialog.getByRole("button", { name: "Apply to this conversation" }).click();
      await expect(markers.last()).toHaveAttribute("aria-label", "Tools turned off: browser.");

      // The picker says what changed and offers to keep it: update the
      // profile, save it as a new one, or revert.
      await openKnob(page);
      await expect(panel.getByTestId("model-picker-tools")).toContainText("browser off");
      const changed = panel.getByTestId("model-picker-profile-changed");
      await expect(changed).toContainText("Differs from Default: browser off");
      await changed.getByRole("button", { name: "Save as new…" }).click();
      await changed.getByLabel("New profile name").fill(name);
      await changed.getByLabel("New profile name").press("Enter");
      await expect(markers.last()).toHaveAttribute("aria-label", `Profile changed to ${name}.`);
      await expect(changed).toBeHidden();
      const pills = panel.getByTestId("model-picker-profiles");
      await expect(pills.getByRole("radio", { name })).toHaveAttribute("aria-checked", "true");

      // Its Edit… opens the profiles; give this one a system prompt.
      await pills.getByRole("button", { name: /^Edit/ }).click();
      await dialog
        .getByTestId("profile-row")
        .filter({ hasText: name })
        .getByRole("button", {
          name: "Edit",
        })
        .click();
      await expect(dialog.locator(".modal-title")).toHaveText(`Profile: ${name}`);
      await dialog.getByRole("radio", { name: "Custom" }).click();
      const prompt = dialog.getByLabel("System prompt template");
      const save = dialog.getByRole("button", { name: "Save" });
      await prompt.fill("Be brief in {{.WorkingDir}}.");
      await expect(prompt).toHaveAccessibleDescription("Line 1: unknown variable .WorkingDir");
      await expect(save).toBeDisabled();
      await prompt.fill("Be brief in {{.WorkingDirectory}}.");
      await expect(save).toBeEnabled();
      await save.click();
      await expect(dialog.locator(".modal-title")).toHaveText("Profiles");
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();

      // The conversation still has the old prompt; Revert takes the profile's.
      await openKnob(page);
      await expect(changed).toContainText(`Differs from ${name}: system prompt`);
      await changed.getByRole("button", { name: "Revert" }).click();
      await expect(markers.last()).toHaveAttribute("aria-label", "System prompt changed.");
      await expect(changed).toBeHidden();
      await expect(panel).toBeVisible();
      // Revert took its button away; focus went to the profile in use, in
      // reach of the panel's Escape.
      await expect(pills.getByRole("radio", { name })).toBeFocused();
      await expect(markers.last()).toContainText("new system prompt");
      const body = await (await request.get(`/api/conversation/${conversationId}`)).json();
      const prompts = body.messages.filter((m: { type: string }) => m.type === "system");
      expect(JSON.parse(prompts.at(-1).llm_data).Content[0].Text).toMatch(/^Be brief in \/.+\.\n$/);

      await pills.getByRole("radio", { name: "Default" }).click();
      await expect(markers.last()).toHaveAttribute(
        "aria-label",
        /^Profile changed to Default; tools turned on: browser; system prompt changed\.$/,
      );
    } finally {
      await request.delete(`/api/profiles/${encodeURIComponent(name)}`);
    }
  });

  test("settings whose profile was deleted can be saved as a new one", async ({
    page,
    request,
  }) => {
    const name = `E2E gone ${Date.now()}`;
    const kept = `${name} kept`;
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: orphaned",
    );
    expect((await request.post("/api/profiles", { data: { name } })).ok()).toBeTruthy();
    try {
      const switched = await request.post(`/api/conversation/${conversationId}/settings`, {
        data: { profile: name },
      });
      expect(switched.ok()).toBeTruthy();
      await page.goto(`/c/${slug}`);
      const panel = page.locator(".model-picker-panel");
      const changed = panel.getByTestId("model-picker-profile-changed");

      // Turning on a tool that's on anyway changes nothing.
      await openKnob(page);
      await expect(changed).toBeHidden();
      await editTools(page);
      const dialog = page.getByRole("dialog");
      await dialog
        .getByRole("radiogroup", { name: "browser", exact: true })
        .getByRole("radio", { name: "On", exact: true })
        .click();
      await dialog.getByRole("button", { name: "Apply to this conversation" }).click();
      await expect(dialog).toBeHidden();
      await expect
        .poll(async () => {
          const r = await request.get(`/api/conversation/${conversationId}/settings`);
          return (await r.json()).tool_overrides;
        })
        .toEqual({ browser: "on" });
      await openKnob(page);
      await expect(changed).toBeHidden();

      // Delete it in the profiles editor, which asks first.
      await page
        .getByTestId("model-picker-profiles")
        .getByRole("button", { name: /^Edit/ })
        .click();
      const row = dialog.getByTestId("profile-row").filter({ hasText: name });
      await row.getByRole("button", { name: "Delete", exact: true }).click();
      await row.getByRole("button", { name: `Delete “${name}”` }).click();
      await expect(row).toHaveCount(0);
      await dialog.getByRole("button", { name: "Close modal" }).click();
      await openKnob(page);
      await expect(changed).toContainText(`“${name}” was deleted`);
      await expect(changed.getByRole("button", { name: "Revert" })).toHaveCount(0);
      await changed.getByRole("button", { name: "Save as new…" }).click();
      await changed.getByLabel("New profile name").fill(kept);
      await changed.getByLabel("New profile name").press("Enter");
      await expect(page.getByTestId("message-modelchange").last()).toHaveAttribute(
        "aria-label",
        `Profile changed to ${kept}.`,
      );
      await expect(changed).toBeHidden();
    } finally {
      await request.delete(`/api/profiles/${encodeURIComponent(name)}`);
      await request.delete(`/api/profiles/${encodeURIComponent(kept)}`);
    }
  });

  test("a profile picked in the composer starts the conversation", async ({ page, request }) => {
    const name = `E2E composer ${Date.now()}`;
    const created = await request.post("/api/profiles", {
      data: { name, model: "predictable", tool_overrides: { browser: "off" } },
    });
    expect(created.ok()).toBeTruthy();
    try {
      await page.goto("/new");
      await page.locator(".model-picker.p-select").click();
      await page
        .locator("[data-testid=model-picker-profiles]")
        .getByRole("radio", { name })
        .click();
      await page.keyboard.press("Escape");
      await page.getByTestId("message-input").fill("echo: from a profile");
      // The first send creates the conversation, or promotes its autosaved
      // draft.
      const sent = page.waitForResponse(
        (r) =>
          r.request().method() === "POST" &&
          /\/api\/conversation(s\/new|\/[^/]+\/chat)$/.test(new URL(r.url()).pathname),
      );
      await page.getByTestId("send-button").click();
      const response = await sent;
      const id =
        new URL(response.url()).pathname.match(/\/conversation\/([^/]+)\/chat$/)?.[1] ??
        (await response.json()).conversation_id;
      const settings = await (await request.get(`/api/conversation/${id}/settings`)).json();
      expect(settings).toMatchObject({
        profile: name,
        model: "predictable",
        thinking_level: "",
        tool_overrides: { browser: "off" },
      });
    } finally {
      await request.delete(`/api/profiles/${encodeURIComponent(name)}`);
      await page.evaluate(() => localStorage.removeItem("shelley.profile"));
    }
  });
});
