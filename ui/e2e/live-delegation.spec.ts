import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test("only a Live delegation sends work to the existing Shelley conversation", async ({
  page,
  request,
}) => {
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: ready");
  const submitted: Array<{ id: string; transcript: string; voice_context: string }> = [];

  await page.clock.install();
  await page.addInitScript(() => {
    const track = { kind: "audio", enabled: true, stop() {} };
    Object.defineProperty(navigator.mediaDevices, "getUserMedia", {
      value: async () => ({
        getAudioTracks: () => [track],
        getTracks: () => [track],
      }),
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
  });
  await page.route(`**/api/conversation/${conversationId}/live-session`, (route) =>
    route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({ session: { id: "live_test" }, transport: { sdp: "v=0" } }),
    }),
  );
  await page.route(`**/api/conversation/${conversationId}/live-message`, async (route) => {
    submitted.push(route.request().postDataJSON());
    const chat = await request.post(`/api/conversation/${conversationId}/chat`, {
      data: { message: "echo: delegated result", model: "predictable" },
    });
    expect(chat.ok()).toBe(true);
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      body: JSON.stringify({
        status: "accepted",
        message: "echo: delegated result",
        task: "echo: delegated result",
      }),
    });
  });

  await page.goto(`/c/${conversationId}`);
  const call = page.getByTestId("live-button");
  await expect(call).toHaveAttribute("aria-label", "Call Shelley");
  const idleColor = await call.evaluate((el) => getComputedStyle(el).color);
  expect(idleColor).toBe("rgb(255, 255, 255)");
  await expect(call.locator("svg")).toHaveAttribute("fill", "currentColor");
  await expect(call.locator("svg")).toHaveAttribute("stroke", "none");
  await call.click();
  await expect(page.getByText("Listening", { exact: true })).toBeVisible();
  await expect(call).toHaveAttribute("aria-label", "End call with Shelley");
  expect(await call.evaluate((el) => getComputedStyle(el).color)).toBe(idleColor);
  const muteColor = await page
    .getByTestId("live-mute")
    .evaluate((el) => getComputedStyle(el).color);
  expect(await page.getByTestId("live-stop").evaluate((el) => getComputedStyle(el).color)).toBe(
    muteColor,
  );
  const emit = (event: Record<string, unknown>) =>
    page.evaluate((value) => (window as any).__mockLive.emit(value), event);

  await emit({
    type: "session.input_transcript.delta",
    delta: "Hi there.",
    start_ms: 0,
    end_ms: 500,
  });
  await emit({
    type: "session.output_transcript.delta",
    delta: "Hi, I'm GPT-Live.",
    start_ms: 600,
    end_ms: 1200,
  });
  await page.clock.fastForward(10_000);
  expect(submitted).toHaveLength(0);

  await emit({
    type: "session.input_transcript.delta",
    delta: "Please inspect README",
    start_ms: 11_000,
    end_ms: 11_800,
  });
  await emit({
    type: "session.delegation.created",
    offset_ms: 11_800,
    delegation: { id: "delegation_one", target: "client" },
  });
  await emit({
    type: "session.input_transcript.delta",
    delta: " and explain it.",
    start_ms: 11_900,
    end_ms: 12_700,
  });
  await page.clock.fastForward(1_300);
  await expect.poll(() => submitted.length).toBe(1);
  expect(submitted[0].id).toContain("delegation_one");
  expect(submitted[0].transcript).toBe("Please inspect README and explain it.");
  expect(submitted[0].voice_context).toContain("USER: Hi there.");
  expect(submitted[0].voice_context).toContain("LIVE: Hi, I'm GPT-Live.");
  await page.clock.fastForward(5_000);
  expect(submitted).toHaveLength(1);
  await expect
    .poll(() =>
      page.evaluate(() =>
        (window as any).__mockLive.sent.some(
          (event: { type: string; delegation_id: string }) =>
            event.type === "session.commentary.append" && event.delegation_id === "delegation_one",
        ),
      ),
    )
    .toBe(true);
  expect(await page.getByTestId("live-send").count()).toBe(0);
  await call.click();
  await expect(call).toHaveAttribute("aria-label", "Call Shelley");
  await expect(page.getByTestId("live-panel")).toHaveCount(0);
  expect(
    await page.evaluate(() =>
      (window as any).__mockLive.sent.some(
        (event: { type: string }) => event.type === "session.close",
      ),
    ),
  ).toBe(true);
});
