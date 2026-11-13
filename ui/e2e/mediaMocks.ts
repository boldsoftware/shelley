// Fake microphone, screen capture, and MediaRecorder for recording specs.
import type { BrowserContext, Page } from "@playwright/test";
import { installTranscriptionAvailability } from "./helpers";

// page may be a whole context, to cover popups.
export async function installMediaMocks(
  page: Page | BrowserContext,
  screenCapture = true,
  transcription = true,
) {
  await installTranscriptionAvailability(page, transcription);
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
      revokedPreviews: [] as string[],
      endDisplay: () => {},
    };
    const revokeObjectURL = URL.revokeObjectURL;
    URL.revokeObjectURL = (url) => {
      mock.revokedPreviews.push(url);
      revokeObjectURL.call(URL, url);
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
        queueMicrotask(() => this.emitChunk("first", true));
        queueMicrotask(() => this.emitChunk("second"));
      }
      stop() {
        mock.recorderStops++;
        this.state = "inactive";
        this.emitChunk("last");
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

declare global {
  interface Window {
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
      revokedPreviews: string[];
      endDisplay: () => void;
    };
  }
}
