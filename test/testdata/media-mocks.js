// Fake microphone, screen capture and MediaRecorder for recording tests,
// ported from ui/e2e/mediaMocks.ts. window.__MEDIA_MOCK_SCREEN_CAPTURE__ says
// whether screen capture is available; window.__recordingMock counts what
// the page asked for.
(() => {
  const screenCaptureAvailable = window.__MEDIA_MOCK_SCREEN_CAPTURE__;
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
    revokedPreviews: [],
    endDisplay: () => {
    }
  };
  const revokeObjectURL = URL.revokeObjectURL;
  URL.revokeObjectURL = (url) => {
    mock.revokedPreviews.push(url);
    revokeObjectURL.call(URL, url);
  };
  class MockTrack extends EventTarget {
    kind;
    constructor(kind) {
      super();
      this.kind = kind;
    }
    stop() {
      mock.stoppedTracks++;
    }
  }
  class MockStream {
    tracks;
    constructor(tracks = []) {
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
    state = "inactive";
    mimeType;
    ondataavailable = null;
    onstop = null;
    onerror = null;
    constructor(stream, options) {
      const hasVideo = stream.getVideoTracks().length > 0;
      this.mimeType = options?.mimeType ?? (hasVideo ? "video/webm" : "audio/webm");
    }
    start(timeslice) {
      if (timeslice !== 1e3)
        throw new Error(`unexpected timeslice ${timeslice}`);
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
    emitChunk(value, withWebMHeader = false) {
      const event = new Event("dataavailable");
      Object.defineProperty(event, "data", {
        value: new Blob(
          [withWebMHeader ? new Uint8Array([26, 69, 223, 163]) : new Uint8Array(), value],
          { type: this.mimeType }
        )
      });
      this.ondataavailable?.(event);
    }
  }
  class MockAudioContext {
    state = "running";
    createMediaStreamDestination() {
      return { stream: new MockStream([new MockTrack("audio")]) };
    }
    createMediaStreamSource() {
      mock.audioSources++;
      return { connect() {
      }, disconnect() {
      } };
    }
    createAnalyser() {
      return {
        fftSize: 64,
        smoothingTimeConstant: 0,
        disconnect() {
        },
        getByteTimeDomainData(samples) {
          const profile = [0.1, 0.25, 0.45, 0.7, 1, 0.65, 0.85, 0.5];
          for (let index = 0; index < samples.length; index++) {
            const multiplier = profile[Math.floor(index / 4) % profile.length] ?? 0.1;
            const peak = Math.max(1, Math.round(mock.meterPeak * multiplier));
            samples[index] = 128 + (index % 2 === 0 ? peak : -peak);
          }
        }
      };
    }
    async resume() {
    }
    async close() {
    }
  }
  let interactionEvent = null;
  const markInteractionTurn = (event) => {
    interactionEvent = event;
  };
  document.addEventListener("keydown", markInteractionTurn, { capture: true });
  document.addEventListener("click", markInteractionTurn, { capture: true });
  const mediaSources = /* @__PURE__ */ new WeakMap();
  Object.defineProperty(HTMLMediaElement.prototype, "srcObject", {
    configurable: true,
    get() {
      return mediaSources.get(this);
    },
    set(value) {
      mediaSources.set(this, value);
    }
  });
  Object.defineProperty(window, "MediaStream", { value: MockStream, configurable: true });
  Object.defineProperty(window, "MediaRecorder", {
    value: MockMediaRecorder,
    configurable: true
  });
  Object.defineProperty(window, "AudioContext", {
    value: MockAudioContext,
    configurable: true
  });
  const mediaDevices = {
    async getUserMedia() {
      mock.microphoneRequests++;
      return new MockStream([new MockTrack("audio")]);
    }
  };
  if (screenCaptureAvailable) {
    mediaDevices.getDisplayMedia = async () => {
      mock.displayRequests++;
      if (interactionEvent && interactionEvent.eventPhase !== Event.NONE) {
        mock.synchronousDisplayRequests++;
      }
      if (mock.displayError)
        throw new DOMException(mock.displayError, "NotAllowedError");
      const video = new MockTrack("video");
      mock.endDisplay = () => video.dispatchEvent(new Event("ended"));
      return new MockStream([video, new MockTrack("audio")]);
    };
  }
  Object.defineProperty(navigator, "mediaDevices", {
    configurable: true,
    value: mediaDevices
  });
  Object.defineProperty(window, "__recordingMock", { value: mock, configurable: true });
})();
