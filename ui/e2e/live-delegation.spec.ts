import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

async function mockVoiceBrowser(page: Page, denyMicrophone = false, holdMicrophone = false) {
  await page.clock.install();
  await page.addInitScript(({ denied, held }) => {
    (window as any).__voiceTracksStopped = 0;
    const track = {
      kind: "audio", enabled: true,
      stop() { (window as any).__voiceTracksStopped++; },
    };
    Object.defineProperty(navigator.mediaDevices, "getUserMedia", {
      value: async () => {
        if (denied) throw new DOMException("Microphone denied", "NotAllowedError");
        if (held) await new Promise<void>((resolve) => {
          (window as any).__releaseVoiceMicrophone = resolve;
        });
        return { getAudioTracks: () => [track], getTracks: () => [track] };
      },
    });
    class Channel {
      readyState = "open";
      onmessage: ((event: { data: string }) => void) | null = null;
      onclose: (() => void) | null = null;
      sent: Array<Record<string, unknown>> = [];
      send(raw: string) {
        this.sent.push(JSON.parse(raw));
      }
      emit(event: Record<string, unknown>) {
        this.onmessage?.({ data: JSON.stringify(event) });
      }
      close() {
        this.readyState = "closed";
        this.onclose?.();
      }
    }
    class Peer {
      iceGatheringState = "complete";
      connectionState = "connected";
      localDescription: { type: string; sdp: string } | null = null;
      channel = new Channel();
      addTrack() {}
      createDataChannel() {
        (window as any).__mockLive = this.channel;
        return this.channel;
      }
      createOffer() {
        return Promise.resolve({ type: "offer", sdp: "v=0\r\nm=audio test" });
      }
      setLocalDescription(description: { type: string; sdp: string }) {
        this.localDescription = description;
        return Promise.resolve();
      }
      setRemoteDescription() {
        queueMicrotask(() =>
          this.channel.emit({ type: "session.started", session: { id: "live_test" } }),
        );
        return Promise.resolve();
      }
      addEventListener() {}
      close() {
        this.channel.close();
      }
    }
    Object.defineProperty(window, "RTCPeerConnection", { value: Peer });
  }, { denied: denyMicrophone, held: holdMicrophone });
}

async function navigateWithinApp(page: Page, path: string) {
  await page.evaluate((url) => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await expect(page).toHaveURL(new RegExp(`${path}$`));
}

type Live = { emit(event: Record<string, unknown>): void; sent: Array<Record<string, unknown>> };

const emit = (page: Page, event: Record<string, unknown>) =>
  page.evaluate((value) => ((window as any).__mockLive as Live).emit(value), event);
const notes = (page: Page) =>
  page.evaluate(() => JSON.stringify(((window as any).__mockLive as Live).sent));

// Live delegates a client task after this speech; Shelley settles the
// transcript and submits it.
async function speakAndDelegate(page: Page, id: string, speech: string) {
  await emit(page, { type: "session.input_transcript.delta", delta: speech, start_ms: 0, end_ms: 900 });
  await emit(page, { type: "session.delegation.created", offset_ms: 900, delegation: { id, target: "client" } });
}

const conversationJSON = async (request: APIRequestContext, id: string) =>
  JSON.stringify(await (await request.get(`/api/conversation/${id}`)).json());

test("a delegated question reaches the chat, and its reply reaches Live", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: ready");
  await mockVoiceBrowser(page);
  const sessions: Array<Record<string, unknown>> = [];
  const relayed: string[] = [];
  page.on("request", (req) => {
    if (req.url().endsWith(`/api/conversation/${conversationId}/live-session`)) sessions.push(req.postDataJSON());
    if (req.url().endsWith("/events")) relayed.push(...req.postDataJSON().events.map((e: { type: string }) => e.type));
  });
  await page.goto(`/c/${conversationId}`);
  const call = page.getByTestId("live-call-header");
  await expect(call).toHaveAttribute("aria-label", "Start voice conversation");
  await call.click();
  const bar = page.getByTestId("live-bar");
  await expect(bar).toContainText("On call");
  await expect(call).toHaveAttribute("aria-label", "End voice call");
  await expect(call).toHaveCSS("color", await page.getByTestId("live-stop").evaluate((el) => getComputedStyle(el).backgroundColor));
  await expect(page.getByTestId("live-timer")).toBeVisible();
  // The strip sits by the composer, with a wide meter and no captions.
  const barBox = await bar.boundingBox();
  const composerBox = await page.locator(".message-input-container").boundingBox();
  expect(barBox && composerBox && barBox.y >= composerBox.y).toBe(true);
  expect((await page.getByTestId("live-meter").boundingBox())?.width).toBeGreaterThanOrEqual(110);
  await expect(page.getByTestId("live-transcript")).toHaveCount(0);

  await emit(page, { type: "session.input_transcript.delta", delta: "Hello ", start_ms: 0, end_ms: 400 });
  await emit(page, { type: "session.output_transcript.delta", delta: "Hey there!", start_ms: 500, end_ms: 900 });
  await emit(page, { type: "session.input_transcript.delta", delta: "echo: why is this so slow", start_ms: 1_200, end_ms: 1_900 });
  await emit(page, { type: "session.started" }); // Shelley doesn't need it
  for (let i = 0; i < 2; i++) { // Live may repeat an event
    await emit(page, { type: "session.delegation.created", offset_ms: 1_900, delegation: { id: "captured_question", target: "client" } });
  }
  const message = page.getByTestId("message").filter({ hasText: "Spoken request:\nHello echo: why is this so slow" });
  await expect(message).toHaveCount(1);
  await expect(message).toContainText("Call #1 Voice Exchange [Captions 1-3] (for context only):\nUSER: Hello\nLIVE: Hey there!\nUSER: echo: why is this so slow");
  expect(sessions).toHaveLength(1);
  expect(sessions[0]).not.toHaveProperty("conversation_options");
  expect(relayed).not.toContain("session.started");
  await expect.poll(() => notes(page)).toContain("The backend accepted this task: Hello echo: why is this so slow");
  await expect.poll(() => notes(page)).toContain('"delegation_id":"captured_question","content":"Backend reply: edit predictable.go');

  await call.click();
  await expect(bar).toBeHidden();
  await expect(call).toHaveAttribute("aria-label", "Start voice conversation");
  expect(await notes(page)).toContain("session.close");
  expect(await page.evaluate(() => (window as any).__voiceTracksStopped)).toBe(1);
  await expect(message).toHaveCount(1);
});

test("sent exchanges stay frozen while new Live speech waits for the next handoff", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: transcript ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  const before = await conversationJSON(request, conversationId);
  const exchange = page.getByTestId("live-exchange");

  await emit(page, { type: "session.input_transcript.delta", delta: "Hello.", start_ms: 0, end_ms: 400 });
  await emit(page, { type: "session.output_transcript.delta", delta: "Hi, I'm ", start_ms: 500, end_ms: 900 });
  await expect(exchange).toContainText("Hi, I'm ");
  await emit(page, { type: "session.output_transcript.delta", delta: "here.", start_ms: 900, end_ms: 1100 });
  await expect(exchange).toContainText("Hi, I'm here.");
  await expect(exchange).toContainText("Not sent to Shelley");
  await expect(exchange).not.toHaveClass(/message-user/);
  expect(await conversationJSON(request, conversationId)).toBe(before);

  await emit(page, { type: "session.input_transcript.delta", delta: "echo: check the transcript", start_ms: 1500, end_ms: 2200 });
  await emit(page, { type: "session.delegation.created", offset_ms: 2200, delegation: { id: "transcript", target: "client" } });
  await emit(page, { type: "session.output_transcript.delta", delta: "Okay, let", start_ms: 2300, end_ms: 2600 });
  await expect(exchange).toContainText("Okay, let");
  await emit(page, { type: "session.output_transcript.delta", delta: " me check.", start_ms: 2600, end_ms: 3000 });
  const message = page.getByTestId("message").filter({ hasText: "Spoken request:\nHello.echo: check the transcript" });
  await expect(message).toHaveCount(1);
  await expect(message).toContainText("LIVE: Okay, let me check.");
  await expect(exchange).toBeHidden();

  // Later speech stays unsent; it belongs to the next handoff, not this bubble.
  await emit(page, { type: "session.output_transcript.delta", delta: " I'm still here while that runs.", start_ms: 3500, end_ms: 4500 });
  await expect(exchange).toContainText("I'm still here while that runs.");
  await expect(exchange).toContainText("Not sent to Shelley");
  await expect(message).not.toContainText("I'm still here while that runs.");
  const afterLate = JSON.parse(await conversationJSON(request, conversationId));
  expect(afterLate.messages.filter((row: any) => row.type === "user").length).toBe(
    JSON.parse(before).messages.filter((row: any) => row.type === "user").length + 1,
  );
  await emit(page, { type: "session.input_transcript.delta", delta: "echo: now lint", start_ms: 5000, end_ms: 6000 });
  await emit(page, { type: "session.delegation.created", offset_ms: 6000, delegation: { id: "next-transcript", target: "client" } });
  const nextMessage = page.getByTestId("message").filter({ hasText: "Spoken request:\necho: now lint" });
  await expect(nextMessage).toHaveCount(1);
  await expect(nextMessage).toContainText("LIVE: I'm still here while that runs.");
  await expect(exchange).toBeHidden();
  await expect(message).not.toContainText("I'm still here while that runs.");
  const history = JSON.parse(await conversationJSON(request, conversationId));
  expect(history.messages.some((row: any) => row.type === "livetranscript")).toBe(false);
  await page.getByTestId("live-stop").click();
  await page.reload();
  await expect(message).toHaveCount(1);
  await expect(message).not.toContainText("I'm still here while that runs.");
  await expect(nextMessage).toContainText("LIVE: I'm still here while that runs.");
});

test("hangup does not append unsent captions to a sent request", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: final transcript ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  await speakAndDelegate(page, "hangup", "echo: save the whole transcript");
  const message = page.getByTestId("message").filter({ hasText: "Spoken request:\necho: save the whole transcript" });
  await expect(message).toHaveCount(1);

  let held = false;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/live-session/*/events", async (route) => {
    if (route.request().postDataJSON().events.some((event: any) => event.delta === "The live chat")) {
      held = true;
      await gate;
    }
    await route.continue();
  });
  await emit(page, { type: "session.output_transcript.delta", delta: "The live chat", start_ms: 1000, end_ms: 1800 });
  await expect.poll(() => held).toBe(true);
  await emit(page, { type: "session.output_transcript.delta", delta: " panel doesn't get too tall.", start_ms: 1800, end_ms: 3000 });
  await expect(page.getByTestId("live-exchange")).toContainText("The live chat panel doesn't get too tall.");
  await expect(message).not.toContainText("The live chat");
  await page.getByTestId("live-stop").click();
  release();
  await expect(page.getByTestId("live-bar")).toHaveCount(0);
  await expect(message).not.toContainText("The live chat");
  await page.reload();
  await expect(message).not.toContainText("The live chat");
  const history = JSON.parse(await conversationJSON(request, conversationId));
  expect(history.messages.some((row: any) => row.type === "livetranscript")).toBe(false);
});

test("Live groups overlapping thoughts and keeps the exchange bounded with optional scrollback", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: overlap ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  const exchange = page.getByTestId("live-exchange");
  await emit(page, { type: "session.input_transcript.delta", delta: "The transcript keeps", start_ms: 0, end_ms: 1200 });
  await emit(page, { type: "session.output_transcript.delta", delta: "Mm-hmm.", start_ms: 900, end_ms: 1300 });
  await emit(page, { type: "session.input_transcript.delta", delta: " splitting up.", start_ms: 1300, end_ms: 2500 });
  await expect(exchange.locator(".live-exchange-turn")).toHaveCount(2);
  await expect(exchange.locator(".live-exchange-turn").first()).toContainText("The transcript keeps splitting up.");

  const region = exchange.getByRole("region", { name: "Live exchange transcript" });
  await emit(page, {
    type: "session.output_transcript.delta",
    delta: "A long reply with room to scroll back. ".repeat(80),
    start_ms: 4000, end_ms: 20000,
  });
  await expect.poll(() => region.evaluate((el) => el.scrollHeight - el.clientHeight)).toBeGreaterThan(0);
  expect(await region.evaluate((el) => el.clientHeight)).toBeLessThanOrEqual(192);
  await expect.poll(() => region.evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(1);
  await region.evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });
  const latest = exchange.getByRole("button", { name: "Latest" });
  await expect(latest).toBeVisible();
  await emit(page, { type: "session.output_transcript.delta", delta: " The newest words.", start_ms: 20000, end_ms: 21000 });
  await expect(exchange).toContainText("The newest words.");
  expect(await region.evaluate((el) => el.scrollTop)).toBe(0);
  await latest.click();
  await expect.poll(() => region.evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(1);
  await page.getByTestId("live-stop").click();
});

test("Live keeps a paused thought together without moving an answer before its question", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: boundaries ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  const turns = page.getByTestId("live-exchange").locator(".live-exchange-turn");
  await emit(page, { type: "session.input_transcript.delta", delta: "This is", start_ms: 0, end_ms: 1000 });
  await emit(page, { type: "session.output_transcript.delta", delta: "Mm.", start_ms: 900, end_ms: 1200 });
  await emit(page, { type: "session.input_transcript.delta", delta: " still one thought", start_ms: 3500, end_ms: 4500 });
  await emit(page, { type: "session.input_transcript.delta", delta: "\n.", start_ms: 4500, end_ms: 4500 });
  await expect(turns).toHaveCount(2);
  await expect(turns.first().locator("p")).toHaveText("This is still one thought.");

  await emit(page, { type: "session.output_transcript.delta", delta: "Which panel?", start_ms: 5000, end_ms: 6000 });
  await emit(page, { type: "session.input_transcript.delta", delta: "Sidebar.", start_ms: 6200, end_ms: 7000 });
  await expect(turns).toHaveCount(4);
  await expect(turns.nth(2)).toContainText("Which panel?");
  await expect(turns.nth(3)).toContainText("Sidebar.");
  await expect(turns.first()).not.toContainText("Sidebar.");
  await page.getByTestId("live-stop").click();
});

test("late punctuation neither amends a sent request nor creates another task", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: punctuation ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  await speakAndDelegate(page, "punctuation", "echo: keep the period");
  const message = page.getByTestId("message").filter({ hasText: "Spoken request:\necho: keep the period" });
  await expect(message).toHaveCount(1);
  await emit(page, { type: "session.input_transcript.delta", delta: ".", start_ms: 850, end_ms: 900 });
  await emit(page, { type: "session.input_transcript.delta", delta: "Next thought.", start_ms: 5000, end_ms: 6000 });
  const exchange = page.getByTestId("live-exchange");
  await expect(exchange).toContainText("Next thought.");
  await expect(exchange).not.toContainText(".Next thought.");
  await expect(exchange).not.toContainText(". Next thought.");
  await page.getByTestId("live-stop").click();
  const history = JSON.parse(await conversationJSON(request, conversationId));
  expect(history.messages.some((row: any) => row.type === "livetranscript")).toBe(false);
  expect(JSON.stringify(history)).not.toContain("Spoken request (continued)");
  await expect(message).not.toContainText("USER: echo: keep the period.");
  await page.reload();
  await expect(message).not.toContainText("USER: echo: keep the period.");
});

test("a transcript backlog drains in order at hangup", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: backlog ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const offsets: number[] = [];
  await page.route("**/live-session/*/events", async (route) => {
    offsets.push(...route.request().postDataJSON().events.map((e: { start_ms: number }) => e.start_ms));
    await gate;
    await route.continue();
  });
  await page.evaluate(() => {
    const live = (window as any).__mockLive as Live;
    for (let i = 0; i < 4; i++) {
      live.emit({ type: "session.input_transcript.delta", delta: "声".repeat(16000), start_ms: i, end_ms: i + 1 });
    }
  });
  await expect.poll(() => offsets.length).toBe(1);
  const hangup = page.waitForResponse((r) => r.request().method() === "DELETE" && r.url().includes("/live-session/"));
  await page.getByTestId("live-stop").click();
  release();
  expect((await hangup).ok()).toBe(true);
  expect(offsets).toEqual([0, 1, 2, 3]);
  await expect(page.getByTestId("live-error")).toHaveCount(0);
  const saved = await request.get(`/api/conversation/${conversationId}/call/1/transcript`);
  expect(await saved.text()).toContain("声".repeat(64000));
});

for (const endManually of [false, true]) {
  test(`a failed pending relay still ends the call after ${endManually ? "manual hangup" : "automatic stop"}`, async ({ page, request }) => {
    const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: relay failure ready");
    await mockVoiceBrowser(page);
    await page.goto(`/c/${conversationId}`);
    await page.getByTestId("live-call-header").click();
    await expect(page.getByTestId("live-bar")).toContainText("On call");
    let held = false;
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    await page.route("**/live-session/*/events", async (route) => {
      held = true;
      await gate;
      await route.fulfill({ status: 503, body: "Relay failed for test" });
    });
    const hangup = page.waitForResponse((response) =>
      response.request().method() === "DELETE" && response.url().includes("/live-session/"));
    await emit(page, { type: "session.input_transcript.delta", delta: "Already received words", start_ms: 0, end_ms: 1000 });
    await expect.poll(() => held).toBe(true);
    if (endManually) await page.getByTestId("live-stop").click();
    release();
    expect((await hangup).ok()).toBe(true);
    await expect(page.getByTestId("live-error")).toContainText("Failed to relay the call to Shelley");
    await expect(page.getByTestId("live-error")).toContainText("Relay failed for test");
    await expect(page.getByTestId("live-stop")).toHaveCount(0);
    expect(await page.evaluate(() => (window as any).__voiceTracksStopped)).toBeGreaterThan(0);
  });
}

test("a failed hangup remains visible alongside a failed final transcript", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: hangup failure ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  await page.route("**/live-session/*/events", (route) =>
    route.fulfill({ status: 503, body: "Relay failed for test" }));
  await page.route("**/live-session/*", async (route) => {
    if (route.request().method() === "DELETE") {
      await route.fulfill({ status: 503, body: "Hangup failed for test" });
    } else {
      await route.continue();
    }
  });
  await emit(page, { type: "session.input_transcript.delta", delta: "Already received words", start_ms: 0, end_ms: 1000 });
  await expect(page.getByTestId("live-error")).toContainText("Failed to relay the call to Shelley");
  await expect(page.getByTestId("live-error")).toContainText("Could not end the call");
  await expect(page.getByTestId("live-error")).toContainText("Hangup failed for test");
});

test("Shelley's errors about a call show on its strip", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  await emit(page, { type: "session.delegation.created", offset_ms: -1, delegation: { id: "bad", target: "client" } });
  await expect(page.getByTestId("live-error")).toHaveText("Live returned an invalid delegation timestamp.");
  await page.getByTestId("live-stop").click();
});

test("a call stays pinned to its original chat while browsing other chats", async ({ page, request }) => {
  const origin = await createConversationViaAPIWithDetails(request, "echo: original voice chat");
  const other = await createConversationViaAPIWithDetails(request, "echo: other chat");
  await mockVoiceBrowser(page);
  const sessions: string[] = [];
  page.on("request", (req) => {
    if (req.url().endsWith("/live-session")) sessions.push(new URL(req.url()).pathname.split("/").at(-2)!);
  });
  await page.goto(`/c/${origin.slug}`);
  await page.getByTestId("live-call-header").click();
  const bar = page.getByTestId("live-bar");
  await expect(bar).toContainText("On call");
  await emit(page, { type: "session.output_transcript.delta", delta: "Still in the original call.", start_ms: 0, end_ms: 500 });
  await expect(page.getByTestId("live-exchange")).toContainText("Still in the original call.");

  await navigateWithinApp(page, `/c/${other.slug}`);
  await expect(page.locator(".header-title")).toHaveText(other.slug);
  await expect(bar).toBeVisible();
  await expect(page.getByTestId("live-exchange")).toContainText("Still in the original call.");
  await expect(page.getByTestId("live-exchange")).toContainText(origin.slug);
  await expect(page.getByTestId("live-call-header")).toHaveAttribute("aria-label", "End voice call");
  expect((await request.post(`/api/conversation/${other.conversationId}/chat`, {
    data: { message: "echo: unrelated private reply", model: "predictable" },
  })).ok()).toBe(true);
  await expect(page.getByTestId("message").filter({ hasText: "unrelated private reply" }).last()).toBeVisible();
  expect((await request.post(`/api/conversation/${origin.conversationId}/chat`, {
    data: { message: "echo: original background reply", model: "predictable" },
  })).ok()).toBe(true);
  await expect.poll(() => notes(page)).toContain("Backend reply: original background reply");
  expect(await notes(page)).not.toContain("unrelated private reply");

  await navigateWithinApp(page, "/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await speakAndDelegate(page, "off_view_task", "echo: delegated to the original chat");
  await expect.poll(() => conversationJSON(request, origin.conversationId)).toContain("delegated to the original chat");
  expect(await conversationJSON(request, other.conversationId)).not.toContain("delegated to the original chat");
  await expect.poll(() => notes(page)).toContain('"delegation_id":"off_view_task","content":"Backend reply: edit predictable.go');
  await expect(page).toHaveURL(/\/new$/);
  expect(sessions).toEqual([origin.conversationId]);
  const returnToCall = page.getByTestId("live-call-origin");
  await expect(returnToCall).toContainText(origin.slug);
  await returnToCall.click();
  await expect(page.locator(".header-title")).toHaveText(origin.slug);
  await expect(bar).toBeVisible();
  await expect(returnToCall).toBeDisabled();

  // The strip and its controls fit a phone, in an archive too.
  expect((await request.post(`/api/conversation/${other.conversationId}/archive`)).ok()).toBe(true);
  await navigateWithinApp(page, `/c/${other.slug}`);
  await expect(page.locator(".header-title")).toHaveText(`${other.slug} (archived)`);
  await page.setViewportSize({ width: 320, height: 640 });
  const panel = await bar.boundingBox();
  const stop = await page.getByTestId("live-stop").boundingBox();
  expect(panel && stop && stop.x + stop.width <= panel.x + panel.width).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expect(page.getByTestId("live-meter")).toBeVisible();
  await page.getByTestId("live-mute").click();
  await expect(bar).toContainText("Muted");
  await page.getByTestId("live-stop").click();
  await expect(bar).toBeHidden();
  expect(await page.evaluate(() => (window as any).__voiceTracksStopped)).toBe(1);
});

test("a call on /new starts one blank draft with the composer's selection, wherever the user browses meanwhile", async ({ page, request }) => {
  const other = await createConversationViaAPIWithDetails(request, "echo: browsing during permission");
  const profile = `VoicePersonal-${crypto.randomUUID()}`;
  expect((await request.post("/api/profiles", {
    data: { name: profile, model: "predictable", system_prompt: "You are a personal reflection partner." },
  })).ok()).toBe(true);
  await page.addInitScript((name) => localStorage.setItem("shelley.profile", name), profile);
  await mockVoiceBrowser(page, false, true);
  let drafts = 0;
  let startedId = "";
  let sessionSettings: Record<string, any> | null = null;
  await page.route("**/api/conversations/draft", (route) => {
    drafts++;
    return route.continue();
  });
  page.on("request", (req) => {
    if (req.url().endsWith("/live-session")) {
      startedId = new URL(req.url()).pathname.split("/").at(-2)!;
      sessionSettings = req.postDataJSON();
    }
  });
  const profilesLoaded = page.waitForResponse((response) => response.url().endsWith("/api/profiles") && response.ok());
  await page.goto("/new");
  await profilesLoaded;
  const call = page.getByTestId("live-call-header");
  await call.click();
  await expect(page.getByTestId("live-bar")).toContainText("Connecting");
  expect(drafts).toBe(0); // nothing is created before the microphone is granted
  await navigateWithinApp(page, `/c/${other.slug}`);
  await expect(page.locator(".header-title")).toHaveText(other.slug);
  await page.evaluate(() => (window as any).__releaseVoiceMicrophone());
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  expect(startedId).not.toBe("");
  expect(startedId).not.toBe(other.conversationId);
  // The call's tasks promote the draft with them.
  expect(sessionSettings).toMatchObject({
    model: "predictable",
    conversation_options: expect.objectContaining({ profile, tool_overrides: expect.any(Object) }),
  });
  expect((await (await request.get(`/api/conversation/${startedId}`)).json()).conversation.is_draft).toBe(true);

  await speakAndDelegate(page, "first_task", "echo: started by voice");
  await expect.poll(async () => (await (await request.get(`/api/conversation/${startedId}`)).json()).conversation)
    .toMatchObject({ is_draft: false, model: "predictable" });
  expect(JSON.parse((await (await request.get(`/api/conversation/${startedId}`)).json()).conversation.conversation_options).profile).toBe(profile);
  expect(drafts).toBe(1);
  await expect(page.locator(".header-title")).toHaveText(other.slug);
  await expect.poll(() => notes(page)).toContain('"delegation_id":"first_task","content":"Backend reply: edit predictable.go');

  // A conversation under way has its own settings; the next call sends none.
  await page.getByTestId("live-call-origin").click();
  await expect(page.getByTestId("message").filter({ hasText: "Spoken request:\necho: started by voice" })).toHaveCount(1);
  await page.getByTestId("live-stop").click();
  await call.click();
  await expect(page.getByTestId("live-bar")).toContainText("Connecting");
  await page.evaluate(() => (window as any).__releaseVoiceMicrophone());
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  expect(sessionSettings).not.toHaveProperty("conversation_options");
  await call.click();
});

test("Live exchange collapse keeps listening and stays closed across handoffs", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: collapse ready");
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  const before = await conversationJSON(request, conversationId);
  await emit(page, { type: "session.input_transcript.delta", delta: "echo: collapse me", start_ms: 0, end_ms: 1000 });
  const exchange = page.getByTestId("live-exchange");
  const toggle = page.getByTestId("live-exchange-toggle");
  const region = exchange.getByRole("region", { name: "Live exchange transcript" });
  await toggle.click();
  await expect(exchange).toHaveJSProperty("open", false);
  await expect(region).toBeHidden();
  await emit(page, { type: "session.output_transcript.delta", delta: "Still listening.", start_ms: 1100, end_ms: 1800 });
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  expect(await conversationJSON(request, conversationId)).toBe(before);
  await emit(page, { type: "session.delegation.created", offset_ms: 1800, delegation: { id: "collapsed", target: "client" } });
  await expect(page.getByTestId("message").filter({ hasText: "Spoken request:\necho: collapse me" })).toContainText("LIVE: Still listening.");
  await expect(exchange).toBeHidden();
  await emit(page, { type: "session.output_transcript.delta", delta: "More words while collapsed. ".repeat(80), start_ms: 2000, end_ms: 12000 });
  await expect(exchange).toBeVisible();
  await expect(exchange).toHaveJSProperty("open", false);
  await expect(region).toBeHidden();
  await toggle.focus();
  await page.keyboard.press("Space");
  await expect(region).toBeVisible();
  await expect(region).toContainText("More words while collapsed.");
  await expect.poll(() => region.evaluate((el) => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(1);
  await page.getByTestId("live-stop").click();
});

test("Live delivery remains visible and replyable in brief view", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: brief ready", {
    conversationOptions: { tool_overrides: { message_user: "on" } },
  });
  await mockVoiceBrowser(page);
  await page.goto(`/c/${conversationId}`);
  await page.getByTestId("live-call-header").click();
  await expect(page.getByTestId("live-bar")).toContainText("On call");
  const before = JSON.parse(await conversationJSON(request, conversationId));
  const turns = before.messages.filter((row: any) => row.end_of_turn).length;
  await emit(page, { type: "session.input_transcript.delta", delta: "Review the build", start_ms: 0, end_ms: 1000 });
  await emit(page, { type: "session.delegation.created", offset_ms: 1000, delegation: { id: "brief", target: "client" } });
  const asked = page.getByTestId("message").filter({ hasText: "Spoken request:\nReview the build" });
  await expect(asked).toBeVisible();
  await expect.poll(async () => {
    const history = JSON.parse(await conversationJSON(request, conversationId));
    return history.messages.filter((row: any) => row.end_of_turn).length;
  }).toBeGreaterThan(turns);
  const history = JSON.parse(await conversationJSON(request, conversationId));
  const spoken = history.messages.find((row: any) => row.user_data?.includes("live_delivery_id"));
  expect(spoken).toBeTruthy();
  const reply = await request.post(`/api/conversation/${conversationId}/chat`, {
    data: { model: "predictable", message: `message_user: ${JSON.stringify({ text: "On it.", reply_to: spoken.sequence_id, reaction: "👍", end_turn: true })}` },
  });
  expect(reply.ok()).toBeTruthy();
  const bubble = page.getByTestId("message-user-bubble").filter({ hasText: "On it." });
  await expect(bubble.getByTestId("message-user-quote")).toContainText("Review the build");
  await expect(asked.getByTestId("message-reactions")).toHaveText("👍");
  await page.getByTestId("live-stop").click();
});
