import { expect, test, type APIRequestContext, type Page, type Route } from "@playwright/test";
import { createConversationViaAPIWithDetails, testWorkingDirectory } from "./helpers";

async function installMediaMocks(page: Page, screenCapture = true) {
  await page.addInitScript((screenCaptureAvailable) => {
    const mock = {
      displayRequests: 0,
      synchronousDisplayRequests: 0,
      microphoneRequests: 0,
      audioSources: 0,
      stoppedTracks: 0,
      displayError: "",
      meterPeak: 4,
      recorderStarts: 0,
      recorderStops: 0,
      dataOnlyOnStop: false,
      endDisplay: () => {},
    };

    class MockTrack extends EventTarget {
      kind: string;
      constructor(kind: string) {
        super();
        this.kind = kind;
      }
      stop() {
        mock.stoppedTracks++;
      }
    }

    class MockStream {
      tracks: MockTrack[];
      constructor(tracks: MockTrack[] = []) {
        this.tracks = tracks;
      }
      getTracks() {
        return this.tracks;
      }
      getAudioTracks() {
        return this.tracks.filter((track) => track.kind === "audio");
      }
      getVideoTracks() {
        return this.tracks.filter((track) => track.kind === "video");
      }
    }

    class MockMediaRecorder {
      static isTypeSupported() {
        return true;
      }
      state: RecordingState = "inactive";
      mimeType: string;
      ondataavailable: ((event: BlobEvent) => void) | null = null;
      onstop: (() => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      constructor(stream: MediaStream, options?: MediaRecorderOptions) {
        const hasVideo = stream.getVideoTracks().length > 0;
        this.mimeType = options?.mimeType ?? (hasVideo ? "video/webm" : "audio/webm");
      }
      start(timeslice?: number) {
        if (timeslice !== 1000) throw new Error(`unexpected timeslice ${timeslice}`);
        mock.recorderStarts++;
        this.state = "recording";
        if (mock.dataOnlyOnStop) return;
        queueMicrotask(() => this.emitChunk("first", true));
        queueMicrotask(() => this.emitChunk("second"));
      }
      stop() {
        mock.recorderStops++;
        this.state = "inactive";
        if (mock.dataOnlyOnStop) this.emitChunk("encodedlast", true);
        else this.emitChunk("last");
        queueMicrotask(() => this.onstop?.());
      }
      emitChunk(value: string, withWebMHeader = false) {
        const event = new Event("dataavailable") as BlobEvent;
        Object.defineProperty(event, "data", {
          value: new Blob(
            [withWebMHeader ? new Uint8Array([0x1a, 0x45, 0xdf, 0xa3]) : new Uint8Array(), value],
            { type: this.mimeType },
          ),
        });
        this.ondataavailable?.(event);
      }
    }

    class MockAudioContext {
      state: AudioContextState = "running";
      createMediaStreamDestination() {
        return { stream: new MockStream([new MockTrack("audio")]) };
      }
      createMediaStreamSource() {
        mock.audioSources++;
        return { connect() {}, disconnect() {} };
      }
      createAnalyser() {
        return {
          fftSize: 64,
          smoothingTimeConstant: 0,
          disconnect() {},
          getByteTimeDomainData(samples: Uint8Array) {
            const profile = [0.1, 0.25, 0.45, 0.7, 1, 0.65, 0.85, 0.5];
            for (let index = 0; index < samples.length; index++) {
              const multiplier = profile[Math.floor(index / 4) % profile.length] ?? 0.1;
              const peak = Math.max(1, Math.round(mock.meterPeak * multiplier));
              samples[index] = 128 + (index % 2 === 0 ? peak : -peak);
            }
          },
        };
      }
      async resume() {}
      async close() {}
    }

    // eventPhase returns to NONE after dispatch, unlike userActivation which
    // can remain active across awaits. Native events run microtasks between
    // listeners, so clearing a capture-phase flag in a microtask is too early.
    let interactionEvent: Event | null = null;
    const markInteractionTurn = (event: Event) => {
      interactionEvent = event;
    };
    document.addEventListener("keydown", markInteractionTurn, { capture: true });
    document.addEventListener("click", markInteractionTurn, { capture: true });

    const mediaSources = new WeakMap<HTMLMediaElement, unknown>();
    Object.defineProperty(HTMLMediaElement.prototype, "srcObject", {
      configurable: true,
      get() {
        return mediaSources.get(this);
      },
      set(value) {
        mediaSources.set(this, value);
      },
    });
    Object.defineProperty(window, "MediaStream", { value: MockStream, configurable: true });
    Object.defineProperty(window, "MediaRecorder", {
      value: MockMediaRecorder,
      configurable: true,
    });
    Object.defineProperty(window, "AudioContext", {
      value: MockAudioContext,
      configurable: true,
    });
    const mediaDevices: {
      getUserMedia: () => Promise<MockStream>;
      getDisplayMedia?: () => Promise<MockStream>;
    } = {
      async getUserMedia() {
        mock.microphoneRequests++;
        return new MockStream([new MockTrack("audio")]);
      },
    };
    if (screenCaptureAvailable) {
      mediaDevices.getDisplayMedia = async () => {
        mock.displayRequests++;
        if (interactionEvent && interactionEvent.eventPhase !== Event.NONE) {
          mock.synchronousDisplayRequests++;
        }
        if (mock.displayError) throw new DOMException(mock.displayError, "NotAllowedError");
        const video = new MockTrack("video");
        mock.endDisplay = () => video.dispatchEvent(new Event("ended"));
        return new MockStream([video, new MockTrack("audio")]);
      };
    }
    Object.defineProperty(navigator, "mediaDevices", {
      configurable: true,
      value: mediaDevices,
    });
    Object.defineProperty(window, "__recordingMock", { value: mock, configurable: true });
  }, screenCapture);
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function fulfillJSON(route: Route, json: Record<string, unknown>, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(json) });
}

interface QueuedMessageFixture {
  id: string;
  llm: { Role: number; Content: Array<{ ID: string; Type: number; Text: string }> };
  created_at: string;
  model: string;
  kind?: "transcription";
  state?: "working" | "ready" | "failed";
  transcription?: {
    media_path: string;
    context?: string;
  };
  error?: string;
}

function queuedMessage(
  id: string,
  text: string,
  extra: Partial<QueuedMessageFixture> = {},
): QueuedMessageFixture {
  return {
    id,
    llm: { Role: 0, Content: [{ ID: "", Type: 2, Text: text }] },
    created_at: "2026-09-12T00:00:00Z",
    model: "predictable",
    ...extra,
  };
}

async function openQueuedConversation(
  page: Page,
  request: APIRequestContext,
  queued: QueuedMessageFixture[],
  distilling = false,
): Promise<string> {
  const { conversationId, slug } = await createConversationViaAPIWithDetails(
    request,
    "echo: recording queue seed",
  );
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route("**/api/conversations/snapshot", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as {
      conversations?: Array<{ conversation_id: string; queued_messages?: string }>;
    };
    const conversation = body.conversations?.find(
      (candidate) => candidate.conversation_id === conversationId,
    );
    if (conversation) conversation.queued_messages = JSON.stringify(queued);
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/conversation/*", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || !/^\/api\/conversation\/[^/]+$/.test(url.pathname)) {
      await route.fallback();
      return;
    }
    const response = await route.fetch();
    const body = (await response.json()) as {
      conversation?: { queued_messages?: string };
      messages?: Array<Record<string, unknown>>;
    };
    if (body.conversation) body.conversation.queued_messages = JSON.stringify(queued);
    if (distilling) {
      body.messages = [
        ...(body.messages ?? []),
        {
          message_id: "distill-in-progress",
          conversation_id: conversationId,
          sequence_id: 999,
          type: "agent",
          user_data: JSON.stringify({
            distill_status: "in_progress",
            distill_method: "compact",
          }),
          created_at: "2026-09-12T00:00:00Z",
          generation: 1,
        },
      ];
    }
    await route.fulfill({ response, json: body });
  });
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
  return slug;
}

async function createDraftViaAPI(request: APIRequestContext, draft: string): Promise<string> {
  const response = await request.post("/api/conversations/draft", {
    data: { draft, model: "predictable", cwd: testWorkingDirectory() },
  });
  expect(response.ok()).toBeTruthy();
  const conversation = (await response.json()) as { conversation_id: string };
  return conversation.conversation_id;
}

async function selectConversationFromDrawer(page: Page, conversationId: string) {
  const row = page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
  const openDrawer = page.getByRole("button", { name: "Open conversations" });
  if (await openDrawer.isVisible()) await openDrawer.click();
  await expect(row).toBeVisible();
  await row.click();
  await expect(row).toHaveClass(/active/);
}

async function pasteAttachment(page: Page, filename: string, contents = "attachment") {
  await page.getByTestId("message-input").evaluate(
    (input, file) => {
      const transfer = new DataTransfer();
      transfer.items.add(new File([file.contents], file.filename, { type: "text/plain" }));
      input.dispatchEvent(
        new ClipboardEvent("paste", {
          clipboardData: transfer,
          bubbles: true,
          cancelable: true,
        }),
      );
    },
    { filename, contents },
  );
  await expect(page.locator(".message-attachment-ready")).toHaveCount(1);
}

function recordingPaletteItem(page: Page, title: "Record audio" | "Record audio and screen") {
  return page.locator(".command-palette-item", {
    has: page.getByText(title, { exact: true }),
  });
}

async function recordingShortcut(page: Page, mode: "microphone" | "screen") {
  const modifier = await page.evaluate(() =>
    navigator.platform.toUpperCase().includes("MAC") ? "Meta" : "Control",
  );
  await page.keyboard.press(`${modifier}+${mode === "screen" ? "Alt+" : ""}Shift+KeyM`);
}

async function openRecordingPalette(page: Page) {
  await page.keyboard.press("ControlOrMeta+k");
  const search = page.locator(".command-palette-input");
  await expect(search).toBeVisible();
  await search.fill("Record audio");
  return search;
}

function floatingAncestor(page: Page) {
  return page
    .getByTestId("recording-panel")
    .locator('xpath=ancestor::*[@data-testid="recording-floating"]');
}

test.describe("media recording composer", () => {
  test.beforeEach(async ({ page }) => {
    await installMediaMocks(page);
  });

  test("offers exact palette actions and starts audio by click and screen by Enter", async ({
    page,
    request,
  }) => {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "platform", { configurable: true, value: "Linux x86_64" });
    });
    const conversation = await createConversationViaAPIWithDetails(
      request,
      "echo: recording palette actions",
    );
    await page.goto(`/c/${conversation.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });

    const search = await openRecordingPalette(page);
    const audio = recordingPaletteItem(page, "Record audio");
    const screen = recordingPaletteItem(page, "Record audio and screen");
    await expect(audio).toBeVisible();
    await expect(audio.locator(".command-palette-item-shortcut")).toHaveText("Ctrl+Shift+M");
    await expect(screen).toBeVisible();
    await expect(screen.locator(".command-palette-item-shortcut")).toHaveText("Ctrl+Alt+Shift+M");

    await audio.click();
    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "microphone");
    await expect.poll(() => page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(1);
    await page.getByTestId("recording-cancel-button").click();
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);

    await openRecordingPalette(page);
    await search.fill("audio and screen");
    await expect(screen).toBeVisible();
    await expect(screen).toHaveClass(/selected/);
    await search.press("Enter");

    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "screen");
    await expect(page.getByTestId("recording-preview")).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(() => ({
          display: window.__recordingMock.displayRequests,
          synchronousDisplay: window.__recordingMock.synchronousDisplayRequests,
          microphone: window.__recordingMock.microphoneRequests,
          starts: window.__recordingMock.recorderStarts,
        })),
      )
      .toEqual({ display: 1, synchronousDisplay: 1, microphone: 2, starts: 2 });

    await openRecordingPalette(page);
    await expect(recordingPaletteItem(page, "Record audio")).toHaveCount(0);
    await expect(recordingPaletteItem(page, "Record audio and screen")).toHaveCount(0);
    await page.keyboard.press("Escape");
    await page.getByTestId("recording-cancel-button").click();
  });

  test("uses Command shortcuts and matching palette hints on Macs", async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, "platform", { configurable: true, value: "MacIntel" });
    });
    await page.goto("/new");
    await expect(page.getByTestId("voice-button")).toBeEnabled();
    await page.keyboard.press("Meta+k");
    const search = page.locator(".command-palette-input");
    await expect(search).toBeVisible();
    await search.fill("Record audio");
    await expect(recordingPaletteItem(page, "Record audio").locator("kbd")).toHaveText("⌘⇧M");
    await expect(recordingPaletteItem(page, "Record audio and screen").locator("kbd")).toHaveText(
      "⌘⌥⇧M",
    );
    await search.press("Escape");
    for (const mode of ["microphone", "screen"] as const) {
      await recordingShortcut(page, mode);
      await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", mode);
      await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "recording");
      await page.getByTestId("recording-cancel-button").click();
      await expect(page.getByTestId("voice-button")).toBeEnabled();
    }
    expect(await page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(2);
    expect(await page.evaluate(() => window.__recordingMock.synchronousDisplayRequests)).toBe(1);
  });

  test("ignores repeated, composing, and consumed recording shortcuts", async ({ page }) => {
    await page.goto("/new");
    await expect(page.getByTestId("voice-button")).toBeEnabled();
    await page.evaluate(() => {
      const mac = navigator.platform.toUpperCase().includes("MAC");
      const options = {
        key: "M",
        code: "KeyM",
        metaKey: mac,
        ctrlKey: !mac,
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      };
      for (const state of [{ repeat: true }, { isComposing: true }]) {
        document.dispatchEvent(new KeyboardEvent("keydown", { ...options, ...state }));
      }
      const consumed = new KeyboardEvent("keydown", options);
      consumed.preventDefault();
      document.dispatchEvent(consumed);
    });
    await expect(page.getByTestId("voice-button")).toBeEnabled();
    expect(await page.evaluate(() => window.__recordingMock.microphoneRequests)).toBe(0);
    await recordingShortcut(page, "microphone");
    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "microphone");
    await page.getByTestId("recording-cancel-button").click();
  });

  test("does not start recording behind another dialog", async ({ page }) => {
    await page.goto("/new");
    await expect(page.getByTestId("voice-button")).toBeEnabled();
    const search = await openRecordingPalette(page);
    await search.fill("Notification Settings");
    await page.getByText("Notification Settings", { exact: true }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await recordingShortcut(page, "microphone");
    await recordingShortcut(page, "screen");
    // Cmd-K can also open above an existing modal: neither its shortcuts nor
    // its action rows may start an inaccessible recorder behind that modal.
    for (const mode of ["microphone", "screen"] as const) {
      await openRecordingPalette(page);
      await recordingShortcut(page, mode);
      await expect(page.locator(".command-palette-input")).toBeVisible();
      const paletteInput = page.locator(".command-palette-input");
      await paletteInput.fill(mode === "screen" ? "Record audio and screen" : "Record audio");
      await paletteInput.press("Enter");
      await expect(page.getByRole("dialog")).toBeVisible();
      await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    }
    expect(await page.evaluate(() => window.__recordingMock.microphoneRequests)).toBe(0);
    expect(await page.evaluate(() => window.__recordingMock.displayRequests)).toBe(0);
  });

  test("requests the screen in the shortcut turn before a delayed draft and blocks duplicates", async ({
    page,
  }) => {
    const draftStarted = deferred();
    const releaseDraftResponse = deferred();
    let draftRequests = 0;
    let displayRequestsWhenDraftStarted = 0;
    await page.route("**/api/conversations/draft", async (route) => {
      draftRequests++;
      displayRequestsWhenDraftStarted = await page.evaluate(
        () => window.__recordingMock.displayRequests,
      );
      const response = await route.fetch();
      draftStarted.resolve();
      await releaseDraftResponse.promise;
      await route.fulfill({ response });
    });

    await page.goto("/new");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await recordingShortcut(page, "screen");
    await draftStarted.promise;

    expect(displayRequestsWhenDraftStarted).toBe(1);
    expect(
      await page.evaluate(() => ({
        display: window.__recordingMock.displayRequests,
        synchronousDisplay: window.__recordingMock.synchronousDisplayRequests,
        microphone: window.__recordingMock.microphoneRequests,
        starts: window.__recordingMock.recorderStarts,
      })),
    ).toEqual({ display: 1, synchronousDisplay: 1, microphone: 0, starts: 0 });

    try {
      await openRecordingPalette(page);
      await expect(recordingPaletteItem(page, "Record audio")).toHaveCount(0);
      await expect(recordingPaletteItem(page, "Record audio and screen")).toHaveCount(0);
      await page.keyboard.press("Escape");

      await recordingShortcut(page, "microphone");
      await recordingShortcut(page, "screen");
      expect(draftRequests).toBe(1);
      expect(await page.evaluate(() => window.__recordingMock.displayRequests)).toBe(1);

      releaseDraftResponse.resolve();
      await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "screen");
      await expect
        .poll(() =>
          page.evaluate(() => ({
            display: window.__recordingMock.displayRequests,
            microphone: window.__recordingMock.microphoneRequests,
            starts: window.__recordingMock.recorderStarts,
          })),
        )
        .toEqual({ display: 1, microphone: 1, starts: 1 });

      await recordingShortcut(page, "microphone");
      await recordingShortcut(page, "screen");
      expect(
        await page.evaluate(() => ({
          display: window.__recordingMock.displayRequests,
          microphone: window.__recordingMock.microphoneRequests,
          starts: window.__recordingMock.recorderStarts,
        })),
      ).toEqual({ display: 1, microphone: 1, starts: 1 });
      await page.getByTestId("recording-cancel-button").click();
    } finally {
      releaseDraftResponse.resolve();
    }
  });

  test("cancels selected screen capture while draft creation is pending", async ({ page }) => {
    const draftStarted = deferred();
    const releaseDraft = deferred();
    await page.route("**/api/conversations/draft", async (route) => {
      const response = await route.fetch();
      draftStarted.resolve();
      await releaseDraft.promise;
      await route.fulfill({ response });
    });
    await page.goto("/new");
    await expect(page.getByTestId("voice-button")).toBeEnabled();
    await recordingShortcut(page, "screen");
    await draftStarted.promise;
    try {
      await page.getByTestId("recording-pending-cancel-button").click({ timeout: 5000 });
      await expect(page.getByTestId("voice-button")).toBeEnabled();
      await expect.poll(() => page.evaluate(() => window.__recordingMock.stoppedTracks)).toBe(2);
      expect(await page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(0);
      const response = page.waitForResponse("**/api/conversations/draft");
      releaseDraft.resolve();
      await response;
      await expect(page).toHaveURL(/\/c\//);
      await expect(page.getByTestId("recording-panel")).toHaveCount(0);
      await recordingShortcut(page, "microphone");
      await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "microphone");
      await expect.poll(() => page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(1);
      expect(await page.evaluate(() => window.__recordingMock.displayRequests)).toBe(1);
      await page.getByTestId("recording-cancel-button").click();
    } finally {
      releaseDraft.resolve();
    }
  });

  for (const menu of ["palette", "file menu"] as const) {
    test(`does not record plain typing after the ${menu} consumes Escape`, async ({ page }) => {
      await page.goto("/new");
      const input = page.getByTestId("message-input");
      await expect(page.getByTestId("voice-button")).toBeEnabled();
      const prefix = menu === "file menu" ? "@example" : "";
      if (menu === "palette") await openRecordingPalette(page);
      else {
        await input.fill(prefix);
        await expect(page.getByTestId("file-completion-menu")).toBeVisible();
      }
      await page.keyboard.press("Control+m");
      await page.keyboard.press("Escape");
      await expect(page.locator(".command-palette-input")).toHaveCount(0);
      await expect(page.getByTestId("file-completion-menu")).toHaveCount(0);
      await input.focus();
      await page.keyboard.press("a");
      await expect(input).toHaveValue(`${prefix}a`);
      await expect(page.getByTestId("recording-panel")).toHaveCount(0);
      expect(await page.evaluate(() => window.__recordingMock.microphoneRequests)).toBe(0);
      expect(await page.evaluate(() => window.__recordingMock.displayRequests)).toBe(0);
    });
  }

  test("preserves the composer after a rejected chooser and retries screen capture in a click turn", async ({
    page,
    request,
  }) => {
    const conversation = await createConversationViaAPIWithDetails(
      request,
      "echo: screen chooser retry",
    );
    await page.goto(`/c/${conversation.slug}`);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30_000 });
    await input.fill("Keep this draft through screen failures.");
    await page.evaluate(() => {
      window.__recordingMock.displayError = "Screen sharing was cancelled";
    });

    await recordingShortcut(page, "screen");
    await expect(page.getByTestId("recording-error")).toHaveText("Screen sharing was cancelled");
    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "screen");
    await expect(page.getByTestId("recording-preserved-text")).toHaveText(
      "Keep this draft through screen failures.",
    );
    expect(
      await page.evaluate(() => ({
        display: window.__recordingMock.displayRequests,
        synchronousDisplay: window.__recordingMock.synchronousDisplayRequests,
        microphone: window.__recordingMock.microphoneRequests,
        starts: window.__recordingMock.recorderStarts,
      })),
    ).toEqual({ display: 1, synchronousDisplay: 1, microphone: 0, starts: 0 });

    await page.evaluate(() => {
      window.__recordingMock.displayError = "";
    });
    await page.getByTestId("recording-retry-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording screen + microphone…");
    expect(
      await page.evaluate(() => ({
        display: window.__recordingMock.displayRequests,
        synchronousDisplay: window.__recordingMock.synchronousDisplayRequests,
        microphone: window.__recordingMock.microphoneRequests,
        starts: window.__recordingMock.recorderStarts,
      })),
    ).toEqual({ display: 2, synchronousDisplay: 2, microphone: 1, starts: 1 });

    await page.getByTestId("recording-cancel-button").click();
    await expect(input).toHaveValue("Keep this draft through screen failures.");
    await expect(input).toBeEnabled();
  });

  test("releases a selected screen and preserves the draft when destination creation fails", async ({
    page,
  }) => {
    let draftBody: Record<string, unknown> | null = null;
    await page.route("**/api/conversations/draft", async (route) => {
      draftBody = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({ status: 500, body: "draft unavailable" });
    });
    await page.goto("/new");
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30_000 });
    await input.fill("Do not lose this failed recording draft.");

    await recordingShortcut(page, "screen");

    await expect
      .poll(() => draftBody)
      .toMatchObject({
        draft: "Do not lose this failed recording draft.",
      });
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    await expect(input).toHaveValue("Do not lose this failed recording draft.");
    await expect(input).toBeEnabled();
    await expect
      .poll(() => page.evaluate(() => window.__recordingMock.stoppedTracks))
      .toBeGreaterThanOrEqual(2);
    expect(
      await page.evaluate(() => ({
        display: window.__recordingMock.displayRequests,
        synchronousDisplay: window.__recordingMock.synchronousDisplayRequests,
        microphone: window.__recordingMock.microphoneRequests,
        starts: window.__recordingMock.recorderStarts,
      })),
    ).toEqual({ display: 1, synchronousDisplay: 1, microphone: 0, starts: 0 });
  });

  test("hides recording palette actions while an attachment uploads", async ({ page, request }) => {
    const conversation = await createConversationViaAPIWithDetails(
      request,
      "echo: recording action upload guard",
    );
    const releaseUpload = deferred();
    await page.route("**/api/upload", async (route) => {
      await releaseUpload.promise;
      await route.continue();
    });
    await page.goto(`/c/${conversation.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await page.evaluate(() => {
      const transfer = new DataTransfer();
      transfer.items.add(new File(["pending"], "pending.txt", { type: "text/plain" }));
      document
        .querySelector(".message-input-container")
        ?.dispatchEvent(
          new DragEvent("drop", { bubbles: true, cancelable: true, dataTransfer: transfer }),
        );
    });
    await expect(page.locator(".message-attachment-uploading")).toHaveCount(1);

    try {
      await openRecordingPalette(page);
      await expect(recordingPaletteItem(page, "Record audio")).toHaveCount(0);
      await expect(recordingPaletteItem(page, "Record audio and screen")).toHaveCount(0);
      await page.keyboard.press("Escape");
    } finally {
      releaseUpload.resolve();
    }
    await expect(page.locator(".message-attachment-ready")).toHaveCount(1);
    await openRecordingPalette(page);
    await expect(recordingPaletteItem(page, "Record audio")).toBeVisible();
    await expect(recordingPaletteItem(page, "Record audio and screen")).toBeVisible();
  });

  test("hides recording palette actions in an archived conversation", async ({ page, request }) => {
    const archived = await createConversationViaAPIWithDetails(
      request,
      "echo: archived recording action guard",
    );
    const response = await request.post(`/api/conversation/${archived.conversationId}/archive`);
    expect(response.ok()).toBeTruthy();

    await page.goto("/new");
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    const openDrawer = page.getByRole("button", { name: "Open conversations" });
    if (await openDrawer.isVisible()) await openDrawer.click();
    await page.getByRole("button", { name: "View archived" }).click();
    await page
      .locator(`.conversation-item[data-conversation-id="${archived.conversationId}"]`)
      .click();
    await expect(page.getByTestId("message-input")).toBeHidden();

    await openRecordingPalette(page);
    await expect(recordingPaletteItem(page, "Record audio")).toHaveCount(0);
    await expect(recordingPaletteItem(page, "Record audio and screen")).toHaveCount(0);
  });

  for (const originKind of ["draft", "conversation"] as const) {
    test(`keeps one live recorder when switching from an existing ${originKind} and returning`, async ({
      page,
      request,
    }) => {
      const other = await createConversationViaAPIWithDetails(
        request,
        `echo: floating recorder destination from ${originKind}`,
      );
      const originalText = `Preserve the ${originKind} recording context.`;
      let originalId: string;
      let originalUrl: string;
      let originalPath: RegExp;
      if (originKind === "draft") {
        originalId = await createDraftViaAPI(request, originalText);
        originalUrl = `/c/${originalId}`;
        originalPath = new RegExp(`${originalUrl}$`);
      } else {
        const original = await createConversationViaAPIWithDetails(
          request,
          "echo: floating recorder origin",
        );
        originalId = original.conversationId;
        originalUrl = `/c/${original.slug}`;
        originalPath = new RegExp(`${originalUrl}$`);
      }
      let uploadCount = 0;
      await page.route("**/api/upload/raw?filename=*", async (route) => {
        uploadCount++;
        await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
      });

      await page.goto(originalUrl);
      const originalInput = page.getByTestId("message-input");
      await expect(originalInput).toBeVisible({ timeout: 30_000 });
      if (originKind === "draft") await expect(originalInput).toHaveValue(originalText);
      else await originalInput.fill(originalText);

      await page.getByTestId("voice-button").click();
      const panel = page.getByTestId("recording-panel");
      await expect(panel).toBeVisible();
      await panel.evaluate((element) => element.setAttribute("data-instance-marker", "original"));
      await expect.poll(() => page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(1);

      await selectConversationFromDrawer(page, other.conversationId);
      await expect(page).toHaveURL(new RegExp(`/c/${other.slug}$`));
      await expect(panel).toHaveAttribute("data-instance-marker", "original");
      await expect(floatingAncestor(page)).toHaveCount(1);
      await expect(page.getByTestId("recording-return-button")).toBeVisible();
      await expect(page.getByTestId("voice-button")).toBeDisabled();
      expect(
        await page.evaluate(() => ({
          starts: window.__recordingMock.recorderStarts,
          stops: window.__recordingMock.recorderStops,
        })),
      ).toEqual({ starts: 1, stops: 0 });

      await page.getByTestId("recording-return-button").click();
      await expect(page).toHaveURL(originalPath);
      await expect(panel).toHaveAttribute("data-instance-marker", "original");
      await expect(floatingAncestor(page)).toHaveCount(0);
      await expect(page.getByTestId("recording-return-button")).toHaveCount(0);
      expect(
        await page.evaluate(() => ({
          starts: window.__recordingMock.recorderStarts,
          stops: window.__recordingMock.recorderStops,
        })),
      ).toEqual({ starts: 1, stops: 0 });

      await expect(page.getByTestId("recording-preserved-text")).toHaveText(originalText);
      await page.getByTestId("recording-cancel-button").click();
      await expect(panel).toHaveCount(0);
      expect(uploadCount).toBe(0);
      expect(await page.evaluate(() => window.__recordingMock.recorderStops)).toBe(1);
    });
  }

  test("keeps one screen capture and preview alive across navigation and return", async ({
    page,
    request,
  }) => {
    const original = await createConversationViaAPIWithDetails(
      request,
      "echo: floating screen origin",
    );
    const viewed = await createConversationViaAPIWithDetails(
      request,
      "echo: floating screen destination",
    );
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto(`/c/${original.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-screen-button").click();

    const panel = page.getByTestId("recording-panel");
    const preview = page.getByTestId("recording-preview");
    await expect(panel).toHaveAttribute("data-mode", "screen");
    await expect(preview).toBeVisible();
    await panel.evaluate((element) => element.setAttribute("data-instance-marker", "screen"));
    await preview.evaluate((element) => {
      element.setAttribute("data-instance-marker", "screen-preview");
      window.__recordingPreviewSource = (element as HTMLVideoElement).srcObject;
    });
    const recorderStarts = await page.evaluate(() => window.__recordingMock.recorderStarts);
    expect(recorderStarts).toBeGreaterThan(0);
    expect(await page.evaluate(() => window.__recordingMock.displayRequests)).toBe(1);

    await selectConversationFromDrawer(page, viewed.conversationId);
    await expect(panel).toHaveAttribute("data-instance-marker", "screen");
    await expect(preview).toHaveAttribute("data-instance-marker", "screen-preview");
    expect(
      await preview.evaluate(
        (element) => (element as HTMLVideoElement).srcObject === window.__recordingPreviewSource,
      ),
    ).toBe(true);
    await expect(floatingAncestor(page)).toHaveCount(1);
    expect(
      await page.evaluate(() => ({
        starts: window.__recordingMock.recorderStarts,
        displayRequests: window.__recordingMock.displayRequests,
      })),
    ).toEqual({ starts: recorderStarts, displayRequests: 1 });

    await page.getByTestId("recording-return-button").click();
    await expect(page).toHaveURL(new RegExp(`/c/${original.slug}$`));
    await expect(panel).toHaveAttribute("data-instance-marker", "screen");
    await expect(preview).toHaveAttribute("data-instance-marker", "screen-preview");
    expect(
      await preview.evaluate(
        (element) => (element as HTMLVideoElement).srcObject === window.__recordingPreviewSource,
      ),
    ).toBe(true);
    await expect(floatingAncestor(page)).toHaveCount(0);
    expect(
      await page.evaluate(() => ({
        starts: window.__recordingMock.recorderStarts,
        displayRequests: window.__recordingMock.displayRequests,
      })),
    ).toEqual({ starts: recorderStarts, displayRequests: 1 });

    await page.getByTestId("recording-cancel-button").click();
    expect(uploadCount).toBe(0);
  });

  test("keeps the active recorder floating while an archived conversation is viewed", async ({
    page,
    request,
  }) => {
    const original = await createConversationViaAPIWithDetails(
      request,
      "echo: recorder beside archived view origin",
    );
    const archived = await createConversationViaAPIWithDetails(
      request,
      "echo: recorder beside archived view destination",
    );
    const archiveResponse = await request.post(
      `/api/conversation/${archived.conversationId}/archive`,
    );
    expect(archiveResponse.ok()).toBeTruthy();
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto(`/c/${original.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await page.getByTestId("voice-button").click();
    const panel = page.getByTestId("recording-panel");
    await expect(panel).toBeVisible();
    await panel.evaluate((element) => element.setAttribute("data-instance-marker", "archived"));

    const openDrawer = page.getByRole("button", { name: "Open conversations" });
    if (await openDrawer.isVisible()) await openDrawer.click();
    await page.getByRole("button", { name: "View archived" }).click();
    await page
      .locator(`.conversation-item[data-conversation-id="${archived.conversationId}"]`)
      .click();

    await expect(page).toHaveURL(new RegExp(`/c/${archived.slug}$`));
    await expect(page.getByTestId("message-input")).toBeHidden();
    await expect(panel).toBeVisible();
    await expect(panel).toHaveAttribute("data-instance-marker", "archived");
    await expect(floatingAncestor(page)).toHaveCount(1);
    await expect(page.getByTestId("recording-return-button")).toBeVisible();

    await page.getByTestId("recording-return-button").click();
    await expect(page).toHaveURL(new RegExp(`/c/${original.slug}$`));
    await expect(floatingAncestor(page)).toHaveCount(0);
    await page.getByTestId("recording-cancel-button").click();
    expect(uploadCount).toBe(0);
  });

  test("restores the source draft after return and keeps the viewed draft separate", async ({
    page,
    request,
  }) => {
    const original = await createConversationViaAPIWithDetails(
      request,
      "echo: composer restore recording origin",
    );
    const viewedSeed = "Seeded text in the viewed draft.";
    const viewedEdit = "Edited and read text in the viewed draft.";
    const viewedId = await createDraftViaAPI(request, viewedSeed);
    const originalText = "Restore this source text after cancellation.";
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto(`/c/${original.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await page.getByTestId("message-input").fill(originalText);
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-panel")).toBeVisible();

    await selectConversationFromDrawer(page, viewedId);
    const viewedInput = page.getByTestId("message-input");
    await expect(viewedInput).toHaveValue(viewedSeed);
    await viewedInput.fill(viewedEdit);
    await expect(viewedInput).toHaveValue(viewedEdit);

    await page.getByTestId("recording-return-button").click();
    await expect(page).toHaveURL(new RegExp(`/c/${original.slug}$`));
    await page.getByTestId("recording-cancel-button").click();
    await expect(page.getByTestId("message-input")).toHaveValue(originalText);

    await selectConversationFromDrawer(page, viewedId);
    await expect(page.getByTestId("message-input")).toHaveValue(viewedEdit);
    expect(uploadCount).toBe(0);
  });

  test("stops a floating recording into its original conversation without touching the viewed composer", async ({
    page,
    request,
  }) => {
    const original = await createConversationViaAPIWithDetails(
      request,
      "echo: floating stop origin",
    );
    const viewed = await createConversationViaAPIWithDetails(
      request,
      "echo: floating stop destination",
    );
    const chatRequests: Array<{ url: string; body: Record<string, unknown> }> = [];
    let rawUploadCount = 0;
    await page.route(/\/api\/upload$/, (route) =>
      fulfillJSON(route, { path: "/tmp/shelley-uploads/viewed-draft.txt" }),
    );
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      rawUploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/floating.webm" });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      chatRequests.push({
        url: route.request().url(),
        body: route.request().postDataJSON() as Record<string, unknown>,
      });
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto(`/c/${original.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
    await page.getByTestId("message-input").fill("Original spoken context.");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");

    await selectConversationFromDrawer(page, viewed.conversationId);
    const viewedInput = page.getByTestId("message-input");
    await viewedInput.fill("Do not disturb this viewed draft.");
    await pasteAttachment(page, "viewed-draft.txt");
    await expect(page.getByTestId("voice-button")).toBeDisabled();
    await expect(floatingAncestor(page)).toHaveCount(1);

    await page.getByTestId("recording-stop-button").click();
    await expect.poll(() => chatRequests.length).toBe(1);
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);

    expect(new URL(chatRequests[0]!.url).pathname).toBe(
      `/api/conversation/${original.conversationId}/chat`,
    );
    expect(chatRequests[0]!.body).toMatchObject({
      message: "/transcription /tmp/shelley-uploads/floating.webm\nOriginal spoken context.",
      model: "predictable",
    });
    expect(chatRequests[0]!.body).not.toHaveProperty("transcription_context");
    expect(rawUploadCount).toBe(1);
    await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));
    await expect(viewedInput).toHaveValue("Do not disturb this viewed draft.");
    await expect(page.locator(".message-attachment-name")).toHaveText("viewed-draft.txt");
    await expect(page.locator(".message-attachment-ready")).toHaveCount(1);
  });

  test("discarding while floating saves a trailing source edit without touching the viewed draft", async ({
    page,
    request,
  }) => {
    const originalSeed = "Original saved draft before the final edit.";
    const originalText = "Keep the original saved draft's final edit.";
    const viewedText = "Keep the viewed saved draft.";
    const originalId = await createDraftViaAPI(request, originalSeed);
    const viewedId = await createDraftViaAPI(request, viewedText);
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto(`/c/${originalId}`);
    const originalInput = page.getByTestId("message-input");
    await expect(originalInput).toHaveValue(originalSeed, { timeout: 30_000 });

    // Leave the edit inside the autosave debounce, then switch immediately.
    // The trailing save must retain the source id instead of reading the newly
    // viewed draft from mutable composer mirrors.
    await originalInput.fill(originalText);
    await page.getByTestId("voice-button").click();
    await selectConversationFromDrawer(page, viewedId);
    await expect(page.getByTestId("message-input")).toHaveValue(viewedText);
    await expect(floatingAncestor(page)).toHaveCount(1);

    await expect
      .poll(async () => {
        const [originalResponse, viewedResponse] = await Promise.all([
          request.get(`/api/conversation-by-slug/${originalId}`),
          request.get(`/api/conversation-by-slug/${viewedId}`),
        ]);
        const original = (await originalResponse.json()) as { draft: string };
        const viewed = (await viewedResponse.json()) as { draft: string };
        return [original.draft, viewed.draft];
      })
      .toEqual([originalText, viewedText]);

    await page.getByTestId("recording-cancel-button").click();
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveValue(viewedText);
    await selectConversationFromDrawer(page, originalId);
    await expect(page.getByTestId("message-input")).toHaveValue(originalText);
    await selectConversationFromDrawer(page, viewedId);
    await expect(page.getByTestId("message-input")).toHaveValue(viewedText);
    expect(uploadCount).toBe(0);
  });

  test("gives a blank new-conversation recording a saved return target", async ({
    page,
    request,
  }) => {
    const viewed = await createConversationViaAPIWithDetails(
      request,
      "echo: blank recording destination",
    );
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto("/new");
    const createDraftResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/api/conversations/draft",
      { timeout: 10_000 },
    );
    await page.getByTestId("voice-button").click();
    const draftResponse = await createDraftResponse;
    expect(draftResponse.status()).toBe(201);
    expect(draftResponse.request().postDataJSON()).toMatchObject({ draft: "" });
    const draft = (await draftResponse.json()) as { conversation_id: string };
    await expect(page).toHaveURL(new RegExp(`/c/${draft.conversation_id}$`));

    const panel = page.getByTestId("recording-panel");
    await expect(panel).toBeVisible();
    await panel.evaluate((element) => element.setAttribute("data-instance-marker", "blank-new"));
    await selectConversationFromDrawer(page, viewed.conversationId);
    await expect(floatingAncestor(page)).toHaveCount(1);
    await expect(page.getByTestId("voice-button")).toBeDisabled();

    await page.getByTestId("recording-return-button").click();
    await expect(page).toHaveURL(new RegExp(`/c/${draft.conversation_id}$`));
    await expect(panel).toHaveAttribute("data-instance-marker", "blank-new");
    await expect(floatingAncestor(page)).toHaveCount(0);
    await page.getByTestId("recording-cancel-button").click();
    expect(uploadCount).toBe(0);
  });

  test("does not let delayed blank-recording draft creation hijack a later selection", async ({
    page,
    request,
  }) => {
    const viewed = await createConversationViaAPIWithDetails(
      request,
      "echo: delayed blank recording destination",
    );
    const draftCreated = deferred();
    const releaseDraftResponse = deferred();
    let createdDraftId = "";
    await page.route("**/api/conversations/draft", async (route) => {
      const response = await route.fetch();
      const draft = (await response.json()) as { conversation_id: string };
      createdDraftId = draft.conversation_id;
      draftCreated.resolve();
      await releaseDraftResponse.promise;
      await route.fulfill({ response, json: draft });
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await draftCreated.promise;
    expect(createdDraftId).not.toBe("");
    expect(await page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(0);

    try {
      await selectConversationFromDrawer(page, viewed.conversationId);
      await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));
      await expect(page.getByTestId("voice-button")).toBeDisabled();

      releaseDraftResponse.resolve();
      await expect(page.getByTestId("recording-panel")).toBeVisible();
      await expect(floatingAncestor(page)).toHaveCount(1);
      await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));
      await expect.poll(() => page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(1);

      await page.getByTestId("recording-return-button").click();
      await expect(page).toHaveURL(new RegExp(`/c/${createdDraftId}$`));
      await expect(floatingAncestor(page)).toHaveCount(0);
      await page.getByTestId("recording-cancel-button").click();
    } finally {
      releaseDraftResponse.resolve();
    }
  });

  for (const { label, message } of [
    {
      label: "normal send",
      message: "Send this message to the draft that created it.",
    },
    {
      label: "explicit transcription command",
      message:
        "/transcription /tmp/shelley-uploads/existing-recording.webm\nKeep this context with it.",
    },
  ]) {
    test(`${label} waits for its delayed new-conversation draft without following later navigation`, async ({
      page,
      request,
    }) => {
      const viewed = await createConversationViaAPIWithDetails(
        request,
        `echo: delayed ${label} destination`,
      );
      const draftCreated = deferred();
      const releaseDraftResponse = deferred();
      let createdDraftId = "";
      const chatRequests: Array<{ url: string; body: Record<string, unknown> }> = [];

      await page.route("**/api/conversations/draft", async (route) => {
        const response = await route.fetch();
        const draft = (await response.json()) as { conversation_id: string };
        createdDraftId = draft.conversation_id;
        draftCreated.resolve();
        await releaseDraftResponse.promise;
        await route.fulfill({ response, json: draft });
      });
      await page.route("**/api/conversation/*/chat", async (route) => {
        chatRequests.push({
          url: route.request().url(),
          body: route.request().postDataJSON() as Record<string, unknown>,
        });
        if (label === "normal send") await route.continue();
        else await fulfillJSON(route, { status: "queued" }, 202);
      });

      await page.goto("/new");
      await page.getByTestId("message-input").fill(message);
      await draftCreated.promise;
      expect(createdDraftId).not.toBe("");

      await page.getByTestId("send-button").click();
      try {
        await selectConversationFromDrawer(page, viewed.conversationId);
        await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));

        releaseDraftResponse.resolve();
        await expect.poll(() => chatRequests.length).toBe(1);
        expect(new URL(chatRequests[0]!.url).pathname).toBe(
          `/api/conversation/${createdDraftId}/chat`,
        );
        expect(chatRequests[0]!.body).toMatchObject({ message });
        await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));
        await expect
          .poll(() =>
            page.evaluate((id) => localStorage.getItem(`shelley-draft:${id}`), createdDraftId),
          )
          .toBeNull();
        if (label === "normal send") {
          await selectConversationFromDrawer(page, createdDraftId);
          await expect(page.getByTestId("message-input")).toHaveValue("");
        }
      } finally {
        releaseDraftResponse.resolve();
      }
    });
  }

  test("moves a delayed new-conversation recording draft after navigation and clears its source", async ({
    page,
    request,
  }) => {
    const viewed = await createConversationViaAPIWithDetails(
      request,
      "echo: delayed nonempty recording destination",
    );
    const initialText = "Initial text while the draft request starts.";
    const recordedText = "Latest text owned by the delayed recording draft.";
    const draftCreated = deferred();
    const releaseDraftResponse = deferred();
    let createdDraftId = "";
    let draftRequest: Record<string, unknown> | null = null;
    const chatRequests: Array<{ url: string; body: Record<string, unknown> }> = [];

    await page.route("**/api/conversations/draft", async (route) => {
      draftRequest = route.request().postDataJSON() as Record<string, unknown>;
      const response = await route.fetch();
      const draft = (await response.json()) as { conversation_id: string };
      createdDraftId = draft.conversation_id;
      draftCreated.resolve();
      await releaseDraftResponse.promise;
      await route.fulfill({ response, json: draft });
    });
    await page.route("**/api/upload/raw?filename=*", (route) =>
      fulfillJSON(route, { path: "/tmp/shelley-uploads/delayed-new.webm" }),
    );
    await page.route("**/api/conversation/*/chat", async (route) => {
      chatRequests.push({
        url: route.request().url(),
        body: route.request().postDataJSON() as Record<string, unknown>,
      });
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto("/new");
    const input = page.getByTestId("message-input");
    await input.fill(initialText);
    await draftCreated.promise;
    expect(draftRequest).toMatchObject({ draft: initialText });
    expect(createdDraftId).not.toBe("");

    await input.fill(recordedText);
    await page.getByTestId("voice-button").click();
    expect(await page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(0);

    try {
      await selectConversationFromDrawer(page, viewed.conversationId);
      await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));

      releaseDraftResponse.resolve();
      await expect(page.getByTestId("recording-panel")).toBeVisible();
      await expect(floatingAncestor(page)).toHaveCount(1);
      await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));

      await page.getByTestId("recording-stop-button").click();
      await expect.poll(() => chatRequests.length).toBe(1);
      await expect(page.getByTestId("recording-panel")).toHaveCount(0);
      expect(new URL(chatRequests[0]!.url).pathname).toBe(
        `/api/conversation/${createdDraftId}/chat`,
      );
      expect(chatRequests[0]!.body).toMatchObject({
        message: `/transcription /tmp/shelley-uploads/delayed-new.webm\n${recordedText}`,
        model: "predictable",
      });
      await expect(page).toHaveURL(new RegExp(`/c/${viewed.slug}$`));

      await page.locator("button.btn-new").click();
      await expect(page).toHaveURL(/\/new$/);
      await expect(page.getByTestId("message-input")).toHaveValue("");
    } finally {
      releaseDraftResponse.resolve();
    }
  });

  test("a delayed recording draft does not consume a newer new-conversation composer", async ({
    page,
    request,
  }) => {
    const viewed = await createConversationViaAPIWithDetails(
      request,
      "echo: delayed recording before newer new conversation",
    );
    const recordedText = "Text owned by the older delayed draft.";
    const newerText = "Text entered in the newer new-conversation session.";
    const oldDraftCreated = deferred();
    const releaseOldDraftResponse = deferred();
    const releaseOtherDraftResponses = deferred();
    let oldDraftId = "";
    let draftRequestCount = 0;

    await page.route("**/api/conversations/draft", async (route) => {
      draftRequestCount++;
      const response = await route.fetch();
      const draft = (await response.json()) as { conversation_id: string };
      if (draftRequestCount === 1) {
        oldDraftId = draft.conversation_id;
        oldDraftCreated.resolve();
        await releaseOldDraftResponse.promise;
      } else {
        await releaseOtherDraftResponses.promise;
      }
      await route.fulfill({ response, json: draft });
    });

    await page.goto("/new");
    await page.getByTestId("message-input").fill(recordedText);
    await oldDraftCreated.promise;
    await page.getByTestId("voice-button").click();
    expect(oldDraftId).not.toBe("");
    expect(await page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(0);

    try {
      await selectConversationFromDrawer(page, viewed.conversationId);
      await page.locator("button.btn-new").click();
      await expect(page).toHaveURL(/\/new$/);
      const newerInput = page.getByTestId("message-input");
      await newerInput.fill(newerText);

      releaseOldDraftResponse.resolve();
      await expect(page.getByTestId("recording-panel")).toBeVisible();
      await expect(floatingAncestor(page)).toHaveCount(1);
      await expect(page).toHaveURL(/\/new$/);
      await expect(newerInput).toHaveValue(newerText);

      await page.getByTestId("recording-return-button").click();
      await expect(page).toHaveURL(new RegExp(`/c/${oldDraftId}$`));
      await page.getByTestId("recording-cancel-button").click();
      await expect(page.getByTestId("message-input")).toHaveValue(recordedText);

      await page.locator("button.btn-new").click();
      await expect(page).toHaveURL(/\/new$/);
      await expect(page.getByTestId("message-input")).toHaveValue(newerText);
    } finally {
      releaseOldDraftResponse.resolve();
      releaseOtherDraftResponses.resolve();
    }
  });

  test("starts microphone recording inline and submits only the transcription command", async ({
    page,
  }) => {
    let uploadedFilename = "";
    let uploadedBody = Buffer.alloc(0);
    const chatBodies: Record<string, unknown>[] = [];
    const transcriptionRequested = deferred();
    const releaseAcceptance = deferred();

    await page.route("**/api/upload/raw?filename=*", async (route) => {
      expect(route.request().method()).toBe("POST");
      uploadedFilename = new URL(route.request().url()).searchParams.get("filename") ?? "";
      uploadedBody = route.request().postDataBuffer() ?? Buffer.alloc(0);
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/recording.webm" });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      chatBodies.push(route.request().postDataJSON() as Record<string, unknown>);
      transcriptionRequested.resolve();
      await releaseAcceptance.promise;
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/new");
    const inputBeforeRecording = page.getByTestId("message-input");
    await inputBeforeRecording.fill("Keep this note with the recording.");
    await inputBeforeRecording.evaluate((input) => {
      const file = new File([new Blob(["image"], { type: "image/png" })], "context.png", {
        type: "image/png",
      });
      const transfer = new DataTransfer();
      transfer.items.add(file);
      input.dispatchEvent(
        new ClipboardEvent("paste", { clipboardData: transfer, bubbles: true, cancelable: true }),
      );
    });
    await expect(page.locator(".message-attachment-ready")).toHaveCount(1);
    await expect(page.getByTestId("voice-video-icon")).toBeVisible();
    const composerHeight = (await inputBeforeRecording.boundingBox())?.height;
    await page.getByTestId("voice-button").click();

    await expect(page.getByTestId("recording-panel")).toBeVisible();
    const recordingHeight = (await page.getByTestId("recording-panel").boundingBox())?.height;
    expect(recordingHeight).toBeLessThanOrEqual(46);
    expect(recordingHeight).toBeLessThan(composerHeight ?? Number.POSITIVE_INFINITY);
    await expect(page.getByRole("dialog", { name: "Record media" })).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(1);
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "preroll");
    await expect(page.getByTestId("recording-waveform")).toHaveClass(/recording-waveform-preroll/);
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "recording");
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await expect(page.getByTestId("recording-preserved-text")).toHaveText(
      "Keep this note with the recording.",
    );
    await expect(page.getByTestId("recording-waveform")).toBeVisible();
    const waveformHeights = await page
      .locator(".recording-waveform-bar")
      .evaluateAll((bars) => bars.map((bar) => getComputedStyle(bar).height));
    expect(new Set(waveformHeights).size).toBeGreaterThan(1);
    await page.evaluate(() => {
      window.__recordingMock.meterPeak = 48;
    });
    await expect
      .poll(async () =>
        Math.max(
          ...(await page
            .locator(".recording-waveform-bar")
            .evaluateAll((bars) =>
              bars.map((bar) => Number.parseFloat(getComputedStyle(bar).height)),
            )),
        ),
      )
      .toBeGreaterThan(Math.max(...waveformHeights.map(Number.parseFloat)) * 2);
    await expect.poll(() => page.evaluate(() => window.__recordingMock.microphoneRequests)).toBe(1);

    await page.getByTestId("recording-stop-button").click();
    await transcriptionRequested.promise;
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible();

    releaseAcceptance.resolve();
    await expect(input).toBeEnabled();
    await expect(input).toBeFocused();
    await input.fill("I can keep typing while the server transcribes.");

    expect(chatBodies).toHaveLength(1);
    expect(chatBodies[0]?.message).toMatch(
      /^\/transcription \/tmp\/shelley-uploads\/recording\.webm\nKeep this note with the recording\. \[\/tmp\/shelley-uploads\/[^\]]+\.png\]$/,
    );
    expect(chatBodies[0]).toMatchObject({ model: "predictable" });
    expect(chatBodies[0]).not.toHaveProperty("transcription_context");
    await expect(page.getByTestId("message-attachments")).toHaveCount(0);
    await expect(page.getByTestId("transcription-task")).toHaveCount(0);
    await expect(input).toHaveValue("I can keep typing while the server transcribes.");
    expect(uploadedFilename).toMatch(/^rec-\d{8}-\d{6}\.webm$/);
    expect([...uploadedBody.subarray(0, 4)]).toEqual([0x1a, 0x45, 0xdf, 0xa3]);
    expect(uploadedBody.subarray(4).toString()).toBe("firstsecondlast");
    expect(await page.evaluate(() => window.__recordingMock.stoppedTracks)).toBeGreaterThan(0);
  });

  test("fits the recording controls in a narrow desktop composer", async ({ page }) => {
    await page.setViewportSize({ width: 660, height: 800 });
    await page.goto("/new");
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible({ timeout: 30_000 });
    await input.fill("Keep this long note with the recording so the status has to truncate.");
    await page.getByTestId("voice-button").click();
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "recording");

    const box = async (selector: string) => {
      const rect = await page.locator(selector).boundingBox();
      if (!rect) throw new Error(`${selector} has no box`);
      return { left: rect.x, right: rect.x + rect.width, width: rect.width };
    };
    const panel = await box(".recording-panel");
    const main = await box(".recording-panel-main");
    const waveform = await box('[data-testid="recording-waveform"]');
    const preserved = await box('[data-testid="recording-preserved-text"]');
    const timer = await box('[data-testid="recording-timer"]');
    const screen = await box('[data-testid="recording-screen-button"]');
    const cancel = await box('[data-testid="recording-cancel-button"]');
    expect(waveform.left).toBeGreaterThan(panel.left);
    expect(waveform.right).toBeLessThanOrEqual(preserved.left);
    expect(preserved.width).toBeGreaterThan(40);
    expect(preserved.right).toBeLessThanOrEqual(timer.left);
    expect(timer.width).toBeGreaterThan(30);
    expect(timer.right).toBeLessThanOrEqual(main.right);
    expect(main.right).toBeLessThanOrEqual(screen.left);
    expect(cancel.right).toBeLessThan(panel.right);
    // The secondary action goes icon-only before the primary ones do.
    await expect(
      page.getByTestId("recording-screen-button").locator(".recording-action-label"),
    ).toBeHidden();
    await expect(
      page.getByTestId("recording-stop-button").locator(".recording-action-label"),
    ).toBeVisible();
    await expect(
      page.getByTestId("recording-cancel-button").locator(".recording-action-label"),
    ).toBeVisible();

    await page.getByTestId("recording-cancel-button").click();
    await expect(input).toHaveValue(
      "Keep this long note with the recording so the status has to truncate.",
    );
  });

  test("discards microphone capture and submits screen recording for transcription", async ({
    page,
  }) => {
    const uploadedFilenames: string[] = [];
    let transcriptionBody: Record<string, unknown> | null = null;

    await page.route("**/api/upload/raw?filename=*", async (route) => {
      const filename = new URL(route.request().url()).searchParams.get("filename") ?? "";
      uploadedFilenames.push(filename);
      const path = filename.endsWith(".json")
        ? "/tmp/shelley-uploads/screen.webm.json"
        : "/tmp/shelley-uploads/screen.webm";
      await fulfillJSON(route, { path });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      transcriptionBody = route.request().postDataJSON() as Record<string, unknown>;
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-screen-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "screen");
    await expect(page.getByTestId("recording-preview")).toBeVisible();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording screen + microphone…");
    expect(uploadedFilenames).toHaveLength(0);
    expect(
      await page.evaluate(() => ({
        display: window.__recordingMock.displayRequests,
        microphone: window.__recordingMock.microphoneRequests,
        sources: window.__recordingMock.audioSources,
      })),
    ).toEqual({ display: 1, microphone: 2, sources: 4 });

    await page.getByTestId("recording-stop-button").dispatchEvent("pointerdown", {
      pointerType: "touch",
      button: 0,
    });
    await expect.poll(() => transcriptionBody).not.toBeNull();
    await expect(page.getByTestId("message-input")).toHaveValue("");
    expect(transcriptionBody).toMatchObject({
      message: "/transcription /tmp/shelley-uploads/screen.webm",
      model: "predictable",
    });
    expect(uploadedFilenames).toHaveLength(2);
    expect(uploadedFilenames[0]).toMatch(/^rec-\d{8}-\d{6}\.webm$/);
    expect(uploadedFilenames[1]).toBe("screen.webm.json");
  });

  test("uses the recorder lifetime when screen sharing ends during pre-roll", async ({ page }) => {
    const captureStartedAt = new Date("2026-09-17T12:00:00Z");
    await page.clock.setFixedTime(captureStartedAt);
    let metadata: { duration_ms?: number } | null = null;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      const filename = new URL(route.request().url()).searchParams.get("filename") ?? "";
      if (filename.endsWith(".json")) {
        metadata = JSON.parse(route.request().postData() ?? "{}") as { duration_ms?: number };
      }
      await fulfillJSON(route, {
        path: filename.endsWith(".json")
          ? "/tmp/shelley-uploads/preroll.webm.json"
          : "/tmp/shelley-uploads/preroll.webm",
      });
    });
    await page.route("**/api/conversation/*/chat", (route) =>
      fulfillJSON(route, { status: "queued" }, 202),
    );

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "recording");
    await page.getByTestId("recording-screen-button").click();
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "preroll");
    await page.clock.setFixedTime(new Date(captureStartedAt.getTime() + 1000));
    await page.evaluate(() => {
      window.__recordingMock.endDisplay();
    });

    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    expect(metadata).toEqual(
      expect.objectContaining({
        duration_ms: 1000,
      }),
    );
  });

  test("renders durable transcription queue states in exact order after reload", async ({
    page,
    request,
  }) => {
    const queued = [
      queuedMessage("q-first", "echo: first queued message"),
      queuedMessage("q-working", "", {
        kind: "transcription",
        state: "working",
        transcription: {
          media_path: "/tmp/shelley-uploads/working.webm",
          context: "Keep this image [/tmp/shelley-uploads/context.png]",
        },
      }),
      queuedMessage("q-ready", "Finished spoken words.", {
        kind: "transcription",
        state: "ready",
        transcription: {
          media_path: "/tmp/shelley-uploads/ready.webm",
        },
      }),
      queuedMessage("q-failed", "", {
        kind: "transcription",
        state: "failed",
        transcription: {
          media_path: "/tmp/shelley-uploads/failed.webm",
        },
        error: "transcription unavailable",
      }),
    ];
    await page.route("**/api/conversation/*/send-queued?queued_id=*", (route) =>
      fulfillJSON(route, {}, 202),
    );
    const slug = await openQueuedConversation(page, request, queued);

    const items = page.locator('[data-testid="queued-ghost"], [data-testid="transcription-task"]');
    await expect(items).toHaveCount(4);
    await expect(items.nth(0)).toContainText("first queued message");
    await expect(items.nth(1)).toContainText("Transcribing…");
    await expect(items.nth(1)).toContainText("working.webm");
    await expect(items.nth(1)).toContainText("Keep this image");
    await expect(items.nth(2)).toContainText("Finished spoken words.");
    await expect(items.nth(2)).toContainText("Queued");
    await expect(items.nth(3)).toContainText("Recording failed");
    await expect(items.nth(3)).toContainText("transcription unavailable");
    await expect(page.getByTestId("queued-ghost")).toHaveCount(2);
    await expect(page.getByTestId("transcription-task")).toHaveCount(2);
    await expect(page.getByTestId("send-queued-now")).toHaveCount(1);
    const sendNowRequest = page.waitForRequest((req) =>
      req.url().includes("/send-queued?queued_id=q-first"),
    );
    await page.getByTestId("send-queued-now").click();
    expect((await sendNowRequest).method()).toBe("POST");

    await page.reload();
    await expect(page).toHaveURL(new RegExp(`/c/${slug}$`));
    await expect(items).toHaveCount(4);
    await expect(items.nth(0)).toContainText("first queued message");
    await expect(items.nth(1)).toContainText("working.webm");
    await expect(items.nth(2)).toContainText("Finished spoken words.");
    await expect(items.nth(3)).toContainText("failed.webm");
  });

  test("hides Send now on queued messages while compaction is in progress", async ({
    page,
    request,
  }) => {
    await openQueuedConversation(
      page,
      request,
      [queuedMessage("q-compacting", "queued during compaction")],
      true,
    );

    await expect(page.getByTestId("distill-in-progress")).toContainText("Compacting");
    await expect(page.getByTestId("queued-badge")).toBeVisible();
    await expect(page.getByTestId("send-queued-now")).toHaveCount(0);
    await expect(page.getByTestId("cancel-queued")).toBeVisible();
  });

  test("uses queued-message cancel and retry RPCs for transcription cards", async ({
    page,
    request,
  }) => {
    const queued = [
      queuedMessage("q-working", "", {
        kind: "transcription",
        state: "working",
        transcription: {
          media_path: "/tmp/shelley-uploads/working.webm",
          context: "Keep working draft",
        },
      }),
      queuedMessage("q-failed", "", {
        kind: "transcription",
        state: "failed",
        transcription: {
          media_path: "/tmp/shelley-uploads/failed.webm",
          context: "Keep failed draft",
        },
        error: "transcription unavailable",
      }),
    ];
    await page.route("**/api/conversation/*/cancel-queued?queued_id=*", (route) =>
      fulfillJSON(route, {}),
    );
    await page.route("**/api/conversation/*/retry-queued?queued_id=*", (route) =>
      fulfillJSON(route, {}),
    );
    await openQueuedConversation(page, request, queued);

    const stopRequest = page.waitForRequest((req) =>
      req.url().includes("/cancel-queued?queued_id=q-working"),
    );
    await page.getByTestId("transcription-stop-button").click();
    expect((await stopRequest).method()).toBe("POST");
    const input = page.getByTestId("message-input");
    await expect(input).toHaveValue("Keep working draft");

    await input.fill("");
    const retryRequest = page.waitForRequest((req) =>
      req.url().includes("/retry-queued?queued_id=q-failed"),
    );
    await page.getByTestId("transcription-retry-button").click();
    expect((await retryRequest).method()).toBe("POST");
    await expect(input).toHaveValue("");

    const cancelRequest = page.waitForRequest((req) =>
      req.url().includes("/cancel-queued?queued_id=q-failed"),
    );
    await page.getByTestId("transcription-cancel-button").click();
    expect((await cancelRequest).method()).toBe("POST");
    await expect(input).toHaveValue("Keep failed draft");
  });

  test("keeps the original draft when durable acceptance fails", async ({ page }) => {
    await page.route("**/api/upload/raw?filename=*", (route) =>
      fulfillJSON(route, { path: "/tmp/shelley-uploads/preserved.webm" }),
    );
    await page.route("**/api/conversation/*/chat", (route) =>
      route.fulfill({ status: 500, body: "transcription unavailable" }),
    );

    await page.goto("/new");
    await page.getByTestId("message-input").fill("Keep this draft");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-stop-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveValue("Keep this draft");
    await expect(page.getByTestId("transcription-task")).toHaveCount(0);
  });

  test("keeps microphone recording when screen selection fails", async ({ page }) => {
    let uploadCount = 0;
    let transcriptionBody: Record<string, unknown> | null = null;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/kept.webm" });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      transcriptionBody = route.request().postDataJSON() as Record<string, unknown>;
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.evaluate(() => {
      window.__recordingMock.displayError = "Screen sharing was cancelled";
    });
    await page.getByTestId("recording-screen-button").click();

    await expect(page.getByTestId("recording-error")).toHaveText("Screen sharing was cancelled");
    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "microphone");
    await expect(page.getByTestId("recording-stop-button")).toBeVisible();
    expect(uploadCount).toBe(0);

    await page.getByTestId("recording-stop-button").click();
    await expect.poll(() => transcriptionBody).not.toBeNull();
    await expect(page.getByTestId("message-input")).toHaveValue("");
    expect(uploadCount).toBe(1);
    expect(transcriptionBody).toMatchObject({
      message: "/transcription /tmp/shelley-uploads/kept.webm",
      model: "predictable",
    });
  });

  test("cancels an active inline recording without uploading and restores the composer", async ({
    page,
  }) => {
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-cancel-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    expect(uploadCount).toBe(0);
    await expect(page.getByTestId("message-input")).toHaveValue("");
    expect(await page.evaluate(() => window.__recordingMock.stoppedTracks)).toBeGreaterThan(0);
  });

  test("cancels captured pre-roll without uploading", async ({ page }) => {
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect.poll(() => page.evaluate(() => window.__recordingMock.recorderStarts)).toBe(1);
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "preroll");
    await page.getByTestId("recording-cancel-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    expect(uploadCount).toBe(0);
    expect(await page.evaluate(() => window.__recordingMock.stoppedTracks)).toBeGreaterThan(0);
  });

  test("accepts encoded data emitted when a short recording stops", async ({ page }) => {
    let uploadedBody = Buffer.alloc(0);
    const uploadStarted = deferred();
    const releaseUpload = deferred();
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadedBody = route.request().postDataBuffer() ?? Buffer.alloc(0);
      uploadStarted.resolve();
      await releaseUpload.promise;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/short.webm" });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto("/new");
    await page.evaluate(() => {
      window.__recordingMock.dataOnlyOnStop = true;
    });
    await page.getByTestId("voice-button").click();
    await expect(page.locator(".recording-status")).toHaveAttribute("data-state", "preroll");
    await page.getByTestId("recording-stop-button").click();
    await uploadStarted.promise;
    await expect(page.getByTestId("recording-status")).toHaveText("Finishing recording…");
    releaseUpload.resolve();
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    expect([...uploadedBody.subarray(0, 4)]).toEqual([0x1a, 0x45, 0xdf, 0xa3]);
    expect(uploadedBody.subarray(4).toString()).toBe("encodedlast");
  });
});

test("keeps audio recording available without screen capture", async ({ page }) => {
  await installMediaMocks(page, false);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/new");
  await expect(page.getByTestId("voice-microphone-icon")).toBeVisible();
  await expect(page.getByTestId("voice-video-icon")).toHaveCount(0);
  await openRecordingPalette(page);
  await expect(recordingPaletteItem(page, "Record audio")).toBeVisible();
  await expect(recordingPaletteItem(page, "Record audio and screen")).toHaveCount(0);
});

test("hides recording palette actions when media recording is unavailable", async ({ page }) => {
  await installMediaMocks(page);
  await page.addInitScript(() => {
    Reflect.deleteProperty(window, "MediaRecorder");
  });
  await page.goto("/new");
  await expect(page.getByTestId("voice-button")).toHaveCount(0);
  await openRecordingPalette(page);
  await expect(recordingPaletteItem(page, "Record audio")).toHaveCount(0);
  await expect(recordingPaletteItem(page, "Record audio and screen")).toHaveCount(0);
});

declare global {
  interface Window {
    __recordingPreviewSource: MediaProvider | null;
    __recordingMock: {
      displayRequests: number;
      synchronousDisplayRequests: number;
      microphoneRequests: number;
      audioSources: number;
      stoppedTracks: number;
      displayError: string;
      meterPeak: number;
      recorderStarts: number;
      recorderStops: number;
      dataOnlyOnStop: boolean;
      endDisplay: () => void;
    };
  }
}
