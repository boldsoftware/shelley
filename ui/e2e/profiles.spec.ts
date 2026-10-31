import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";
import type { Profile } from "../src/types";

test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false });

// The model picker is the conversation's settings knob: model, reasoning,
// tools (its Edit…) and profiles (pills, its Edit…), all of which a live
// conversation can change, each change marked in the transcript.
test.describe("Settings knob", () => {
  // The restore regression temporarily customizes the shared built-in Default.
  test.describe.configure({ mode: "default" });

  async function openKnob(page: Page) {
    await page.locator(".model-picker-inline .p-select-label").click();
    await expect(page.locator(".model-picker-panel")).toBeVisible();
  }
  async function editTools(page: Page) {
    const panel = page.locator(".model-picker-panel");
    await panel.getByTestId("model-picker-tools").getByRole("button", { name: /^Edit/ }).click();
    await expect(panel).toBeHidden();
  }

  test("the pre-installed Assistant starts a text chat with auto compaction", async ({ page }) => {
    await page.goto("/new");
    await page.locator(".model-picker.p-select").click();
    const profiles = page.getByTestId("model-picker-profiles");
    await expect(profiles.getByRole("radio", { name: "Default", exact: true })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await profiles.getByRole("radio", { name: "Assistant", exact: true }).click();
    await editTools(page);
    const dialog = page.getByRole("dialog");
    await expect(
      dialog
        .getByRole("radiogroup", { name: "Auto compaction", exact: true })
        .getByRole("radio", { name: "On", exact: true }),
    ).toHaveAttribute("aria-checked", "true");
    await expect(dialog.getByTestId("auto-compaction").getByRole("combobox")).toHaveValue("130000");
    await expect(
      dialog
        .getByRole("radiogroup", { name: "message_user", exact: true })
        .getByRole("radio", { name: "On", exact: true }),
    ).toHaveAttribute("aria-checked", "true");
    await dialog.getByRole("button", { name: "Close modal" }).click();

    await page
      .getByTestId("message-input")
      .fill('message_user: {"text":"Hey! What can I help with?","end_turn":true}');
    await page.getByTestId("send-button").click();
    await expect(page.getByTestId("message-user-text")).toHaveText("Hey! What can I help with?");
    await expect(
      page.locator(".messages-container").getByText("edit predictable.go", { exact: false }),
    ).toHaveCount(0);
  });

  test("restores a built-in profile only after confirmation, without changing a live conversation", async ({
    page,
    request,
  }) => {
    const name = `E2E restore ${Date.now()}`;
    const list = async (): Promise<Profile[]> => (await request.get("/api/profiles")).json();
    const before = await list();
    const builtin = before.find((p) => p.name === "Default")!;
    expect(builtin).toMatchObject({ builtin: true, user_edited: false });
    const defaultName = before.find((p) => p.default)!.name;
    try {
      expect((await request.post("/api/profiles", { data: { name } })).ok()).toBeTruthy();
      const customized = await request.put("/api/profiles/Default", {
        data: {
          model: "predictable",
          tool_overrides: { browser: "off", compact_in_place: "off", message_user: "off" },
          compact_nudge_tokens: 42000,
          system_prompt: "Keep these conversation settings.",
          voice_prompt: "Speak as a pirate.",
        },
      });
      expect(customized.ok()).toBeTruthy();
      const edited = await customized.json();
      expect(edited.user_edited).toBe(true);
      const { conversationId, slug } = await createConversationViaAPIWithDetails(
        request,
        "echo: restore profile",
      );
      expect(
        (
          await request.post(`/api/conversation/${conversationId}/settings`, {
            data: { profile: "Default" },
          })
        ).ok(),
      ).toBeTruthy();
      const settingsURL = `/api/conversation/${conversationId}/settings`;
      const conversationSettings = await (await request.get(settingsURL)).json();

      await page.goto(`/c/${slug}`);
      async function openProfiles() {
        await openKnob(page);
        await page
          .getByTestId("model-picker-profiles")
          .getByRole("button", { name: /^Edit/ })
          .click();
      }
      await openProfiles();
      const dialog = page.getByRole("dialog");
      const row = dialog.getByTestId("profile-row").filter({
        has: page.locator(".profiles-item-name", { hasText: /^Default$/ }),
      });
      const restore = row.getByRole("button", { name: "Restore defaults", exact: true });
      const confirm = row.getByRole("button", { name: "Restore “Default”", exact: true });
      await expect(
        dialog.getByTestId("profile-row").filter({ hasText: name }).getByRole("button", {
          name: "Restore defaults",
        }),
      ).toHaveCount(0);

      await expect(restore).toBeEnabled();
      await restore.click();
      await expect(row).toContainText("voice prompt");
      expect((await list()).find((p) => p.name === "Default")).toEqual(edited);
      await row.getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(confirm).toHaveCount(0);
      await restore.click();
      await dialog.getByRole("button", { name: "Close modal" }).click();
      await openProfiles();
      await expect(confirm).toHaveCount(0);
      await restore.click();
      const restored = page.waitForResponse(
        (r) => r.url().endsWith("/api/profiles/Default/restore") && r.request().method() === "POST",
      );
      await confirm.click();
      expect((await restored).ok()).toBeTruthy();
      await expect(restore).toBeDisabled();
      const after = await list();
      expect(after.find((p) => p.name === "Default")).toEqual(builtin);
      expect(after.find((p) => p.default)!.name).toBe(defaultName);
      expect(await (await request.get(settingsURL)).json()).toEqual(conversationSettings);

      // Shared picker state reloads, but using the restored settings stays opt-in.
      await dialog.getByRole("button", { name: "Close modal" }).click();
      await openKnob(page);
      await expect(page.getByTestId("model-picker-profile-changed")).toContainText(
        "Differs from Default",
      );
    } finally {
      expect((await request.post("/api/profiles/Default/restore")).ok()).toBeTruthy();
      expect((await request.delete(`/api/profiles/${encodeURIComponent(name)}`)).ok()).toBeTruthy();
    }
  });

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

  test("auto compaction, on by default, is switched from the picker", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: compaction later",
    );
    const options = async () =>
      JSON.parse(
        (await (await request.get(`/api/conversation/${conversationId}`)).json()).conversation
          .conversation_options,
      );
    await page.goto(`/c/${slug}`);
    await openKnob(page);
    const panel = page.locator(".model-picker-panel");
    const section = panel.getByTestId("model-picker-auto-compaction");
    const radio = (name: string) =>
      section
        .getByRole("radiogroup", { name: "Auto compaction", exact: true })
        .getByRole("radio", { name, exact: true });
    const nudge = section.getByRole("combobox");
    const markers = page.getByTestId("message-modelchange");
    const tools = panel.getByTestId("model-picker-tools").locator(".model-picker-section-text");
    await expect(radio("On")).toHaveAttribute("aria-checked", "true");
    await expect(nudge).toHaveValue("200000");

    // The panel stays open through each change; Tools leaves auto compaction
    // to its own section.
    await nudge.selectOption("250000");
    await expect(markers).toHaveCount(1);
    await expect(nudge).toHaveValue("250000");
    await expect(tools).toHaveText("All at their defaults");
    expect((await options()).compact_nudge_tokens).toBe(250000);

    await radio("Off").click();
    await expect(markers.last()).toHaveAttribute(
      "aria-label",
      "Tools turned off: compact_in_place.",
    );
    await expect(radio("Off")).toHaveAttribute("aria-checked", "true");
    await expect(nudge).toBeHidden();
    await expect(tools).toHaveText("All at their defaults");
    expect((await options()).tool_overrides).toEqual({ compact_in_place: "off" });

    await radio("On").click();
    await expect(markers).toHaveCount(3);
    await expect(radio("On")).toHaveAttribute("aria-checked", "true");
    expect((await options()).tool_overrides ?? {}).toEqual({});
  });

  test("a quick second pick of auto compaction undoes the first", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: change my mind",
    );
    const options = async () =>
      JSON.parse(
        (await (await request.get(`/api/conversation/${conversationId}`)).json()).conversation
          .conversation_options,
      );
    await page.goto(`/c/${slug}`);
    await openKnob(page);
    const section = page.getByTestId("model-picker-auto-compaction");
    const radio = (name: string) =>
      section
        .getByRole("radiogroup", { name: "Auto compaction", exact: true })
        .getByRole("radio", { name, exact: true });
    const nudge = section.getByRole("combobox");

    // The first pick's change is held on its way, as by a slow network; the
    // second must still come last.
    const pick = async (first: () => Promise<void>, second: () => Promise<void>) => {
      let release = () => {};
      const held = new Promise<void>((r) => (release = r));
      let posts = 0;
      await page.route(`**/api/conversation/${conversationId}/settings`, async (route) => {
        if (route.request().method() === "POST" && posts++ === 0) await held;
        await route.continue();
      });
      await first();
      await second();
      release();
      await expect.poll(() => posts).toBe(2);
      await page.unrouteAll({ behavior: "wait" });
    };

    await pick(
      () => nudge.selectOption("250000"),
      () => nudge.selectOption("200000"),
    );
    await expect.poll(async () => (await options()).compact_nudge_tokens ?? 0).toBe(0);
    await expect(nudge).toHaveValue("200000");

    await pick(
      () => radio("Off").click(),
      () => radio("On").click(),
    );
    await expect.poll(async () => (await options()).tool_overrides ?? {}).toEqual({});
    await expect(radio("On")).toHaveAttribute("aria-checked", "true");

    // The choice in effect changes nothing.
    const next = page.waitForRequest(
      (r) => r.method() === "POST" && r.url().endsWith(`/conversation/${conversationId}/settings`),
    );
    await radio("On").click();
    await nudge.selectOption("300000");
    expect((await next).postDataJSON()).toEqual({ compact_nudge_tokens: 300000 });
  });

  test("auto compaction in a -no-tools conversation is off until turned on", async ({
    page,
    request,
  }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "echo: no tools",
      { conversationOptions: { disable_all_tools: true } },
    );
    await page.goto(`/c/${slug}`);
    await openKnob(page);
    const section = page.getByTestId("model-picker-auto-compaction");
    await expect(
      section
        .getByRole("radiogroup", { name: "Auto compaction", exact: true })
        .getByRole("radio", { name: "Off", exact: true }),
    ).toHaveAttribute("aria-checked", "true");

    await editTools(page);
    const dialog = page.getByRole("dialog");
    const row = dialog.getByTestId("auto-compaction");
    await expect(row.getByRole("radio", { name: "Off" })).toHaveAttribute("aria-checked", "true");
    await expect(
      dialog
        .getByRole("radiogroup", { name: "browser", exact: true })
        .getByRole("radio", { name: "Default (off)" }),
    ).toHaveAttribute("aria-checked", "true");
    await row.getByRole("radio", { name: "On" }).click();
    await dialog.getByRole("button", { name: "Apply to this conversation" }).click();
    await expect(dialog).toBeHidden();
    const body = await (await request.get(`/api/conversation/${conversationId}`)).json();
    expect(JSON.parse(body.conversation.conversation_options).tool_overrides).toEqual({
      compact_in_place: "on",
    });
  });

  // What a browser kept when 250k was the default: the old default gives way
  // to the new one; another pick, from a profile or the picker, stays.
  for (const [kept, shown] of [
    ["250000", "200000"],
    ["130000", "130000"],
  ]) {
    test(`the composer's nudge kept as ${kept} shows ${shown}`, async ({ page }) => {
      await page.addInitScript((kept) => {
        if (localStorage.getItem("shelley.profileApplied")) return;
        localStorage.setItem("shelley.profileApplied", "Default");
        localStorage.setItem("shelley.compactNudgeTokens", kept);
      }, kept);
      await page.goto("/new");
      await page.locator(".model-picker.p-select").click();
      const nudge = page.getByTestId("model-picker-auto-compaction").getByRole("combobox");
      await expect(nudge).toHaveValue(shown);
      await page.reload();
      await page.locator(".model-picker.p-select").click();
      await expect(nudge).toHaveValue(shown);
    });
  }

  test("auto compaction turned off in the composer starts the conversation without it", async ({
    page,
    request,
  }) => {
    await page.goto("/new");
    await page.locator(".model-picker.p-select").click();
    const section = page.getByTestId("model-picker-auto-compaction");
    const radio = (name: string) =>
      section
        .getByRole("radiogroup", { name: "Auto compaction", exact: true })
        .getByRole("radio", { name, exact: true });
    const profiles = page.getByTestId("model-picker-profiles");

    // Assistant's idle compaction needs it.
    await profiles.getByRole("radio", { name: "Assistant", exact: true }).click();
    await expect(radio("On")).toHaveAttribute("aria-checked", "true");
    await expect(radio("Off")).toBeDisabled();
    await expect(section).toContainText("idle compaction needs it");

    await profiles.getByRole("radio", { name: "Default", exact: true }).click();
    await expect(radio("Off")).toBeEnabled();
    await radio("Off").click();
    await expect(radio("Off")).toHaveAttribute("aria-checked", "true");
    await page.keyboard.press("Escape");
    await page.getByTestId("message-input").fill("echo: no compaction");
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
      profile: "Default",
      tool_overrides: { compact_in_place: "off" },
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
      const contextPrompts = page.getByTestId("generation-context").locator(".system-prompt-view");
      await expect(contextPrompts).toHaveCount(1);

      await pills.getByRole("radio", { name: "Default" }).click();
      await expect(markers.last()).toHaveAttribute(
        "aria-label",
        /^Profile changed to Default; tools turned on: browser; system prompt changed\.$/,
      );
      await expect(contextPrompts).toHaveCount(1);
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

  test("a profile's voice prompt is generated, edited and kept", async ({ page, request }) => {
    const name = `E2E voice ${Date.now()}`;
    const created = await request.post("/api/profiles", {
      data: { name, model: "predictable", system_prompt: "You are Ahab, a terse pirate." },
    });
    expect(created.ok()).toBeTruthy();
    const { slug } = await createConversationViaAPIWithDetails(request, "echo: voice prompt");
    const stored = async () => {
      const profiles: { name: string; voice_prompt: string }[] = await (
        await request.get("/api/profiles")
      ).json();
      return profiles.find((p) => p.name === name)?.voice_prompt;
    };
    try {
      await page.goto(`/c/${slug}`);
      await openKnob(page);
      await page
        .getByTestId("model-picker-profiles")
        .getByRole("button", { name: /^Edit/ })
        .click();
      const dialog = page.getByRole("dialog");
      await dialog
        .getByTestId("profile-row")
        .filter({ hasText: name })
        .getByRole("button", { name: "Edit" })
        .click();
      const voice = dialog.getByLabel("Voice prompt");
      await expect(voice).toHaveValue("");

      // Generating asks the server, from the profile's model and system
      // prompt, and fills the editor; nothing is saved until Save.
      const generated = page.waitForRequest("**/api/profiles/voice-prompt/generate");
      await dialog.getByRole("button", { name: "Generate from system prompt" }).click();
      expect((await generated).postDataJSON()).toEqual({
        model: "predictable",
        system_prompt: "You are Ahab, a terse pirate.",
      });
      await expect(voice).not.toHaveValue("");
      expect(await stored()).toBe("");

      await voice.fill("You are Ahab. Speak like a pirate, briefly.");
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog.locator(".modal-title")).toHaveText("Profiles");
      await expect.poll(stored).toBe("You are Ahab. Speak like a pirate, briefly.");

      // Saving settings into the profile elsewhere keeps its voice prompt.
      const kept = await request.put(`/api/profiles/${encodeURIComponent(name)}`, {
        data: { model: "predictable", system_prompt: "You are Ahab." },
      });
      expect(kept.ok()).toBeTruthy();
      expect(await stored()).toBe("You are Ahab. Speak like a pirate, briefly.");
    } finally {
      await request.delete(`/api/profiles/${encodeURIComponent(name)}`);
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

test("profile keyboard selection stays visible in a scrolling menu", async ({ page, request }) => {
  const prefix = `Scroll${Date.now()}`;
  const names = Array.from({ length: 10 }, (_, i) => `${prefix}-${i}`);
  try {
    for (const name of names) {
      expect((await request.post("/api/profiles", { data: { name } })).ok()).toBeTruthy();
    }
    await page.goto("/new");
    const input = page.getByTestId("message-input");
    await input.fill(`+${prefix}`);
    const menu = page.getByTestId("profile-menu");
    await expect(menu.getByRole("option")).toHaveCount(names.length);
    expect(await menu.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(true);

    const selected = menu.getByRole("option", { selected: true });
    const selectionVisible = () =>
      selected.evaluate((el) => {
        const row = el.getBoundingClientRect();
        const menu = el.closest('[role="listbox"]')!.getBoundingClientRect();
        return row.top >= menu.top && row.bottom <= menu.bottom;
      });
    for (let i = 1; i < names.length; i++) await input.press("ArrowDown");
    await expect(selected).toContainText(names.at(-1)!);
    await expect.poll(selectionVisible).toBe(true);

    await input.press("ArrowDown");
    await expect(selected).toContainText(names[0]);
    await expect.poll(selectionVisible).toBe(true);
    await input.press("Enter");
    await expect(input).toHaveValue(`+${names[0]} `);
  } finally {
    for (const name of names) {
      expect((await request.delete(`/api/profiles/${name}`)).ok()).toBeTruthy();
    }
  }
});
