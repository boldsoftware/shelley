<!-- Inline recording takeover for the message composer. Starts with the
     requested media immediately, buffers one-second chunks, and can discard and
     restart the capture with screen/window video plus microphone audio. -->
<template>
  <div
    class="recording-panel"
    :data-mode="mode"
    data-testid="recording-panel"
  >
    <div class="recording-panel-main">
      <video
        v-if="mode === 'screen' && displayStream"
        ref="previewElement"
        class="recording-preview"
        autoplay
        muted
        playsinline
        data-testid="recording-preview"
      />
      <div
        v-if="state === 'preroll' || state === 'recording'"
        :class="[
          'recording-waveform',
          { 'recording-waveform-preroll': state === 'preroll' },
        ]"
        aria-hidden="true"
        data-testid="recording-waveform"
      >
        <span
          v-for="(level, index) in waveformLevels"
          :key="index"
          class="recording-waveform-bar"
          :style="{ height: `${Math.max(2, level * 20)}px` }"
        />
      </div>
      <div
        class="recording-status"
        :data-state="state"
        role="status"
        aria-live="polite"
      >
        <span :class="['recording-status-dot', state]" aria-hidden="true" />
        <span
          v-if="errorMessage"
          class="recording-error"
          role="alert"
          data-testid="recording-error"
        >
          {{ errorMessage }}
        </span>
        <span v-else class="recording-status-label" data-testid="recording-status">{{ statusText }}</span>
        <span
          v-if="preservedText"
          class="recording-preserved-text"
          :title="preservedText"
          data-testid="recording-preserved-text"
        >{{ preservedText }}</span>
        <time
          v-if="state === 'preroll' || state === 'recording' || state === 'stopping'"
          class="recording-timer"
          data-testid="recording-timer"
          :datetime="`PT${Math.floor(elapsedMs / 1000)}S`"
          >{{ formattedElapsed }}</time
        >
      </div>
    </div>

    <div class="recording-actions">
      <button
        v-if="state === 'recording' && mode === 'microphone' && screenCaptureAvailable"
        type="button"
        class="btn btn-secondary recording-screen-btn"
        :aria-label="t('recordingScreenAction')"
        data-testid="recording-screen-button"
        @click="restartWithScreen"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="18" height="18">
          <rect x="3" y="5" width="13" height="14" rx="2" />
          <path stroke-linecap="round" stroke-linejoin="round" d="m16 10 5-3v10l-5-3z" />
        </svg>
        <span class="recording-action-label">{{ t("recordingScreenAction") }}</span>
      </button>
      <button
        v-if="state === 'preroll' || state === 'recording'"
        type="button"
        class="btn btn-primary recording-stop-btn"
        :aria-label="t('recordingStop')"
        data-testid="recording-stop-button"
        @pointerdown="handleStopPointerDown"
        @click="stopRecording"
      >
        <svg fill="currentColor" viewBox="0 0 24 24" width="12" height="12" aria-hidden="true">
          <rect x="5" y="5" width="14" height="14" rx="1" />
        </svg>
        <span class="recording-action-label">{{ t("recordingStop") }}</span>
      </button>
      <button
        v-if="state === 'error'"
        type="button"
        class="btn btn-primary"
        :aria-label="t('retry')"
        data-testid="recording-retry-button"
        @click="retry"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h5M20 20v-5h-5M5.6 9A7 7 0 0 1 18.4 9M18.4 15A7 7 0 0 1 5.6 15" />
        </svg>
        <span class="recording-action-label">{{ t("retry") }}</span>
      </button>
      <button
        type="button"
        class="btn btn-secondary"
        :disabled="state === 'stopping'"
        :aria-label="t('cancel')"
        data-testid="recording-cancel-button"
        @click="cancelRecording"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 6l12 12M18 6 6 18" />
        </svg>
        <span class="recording-action-label">{{ t("cancel") }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "../composables/i18n";
import type { RecordingMode } from "./recordingDestination";

type RecordingState = "starting" | "preroll" | "recording" | "stopping" | "error";

const props = defineProps<{
  initialMode: RecordingMode;
  initialScreen?: Promise<MediaStream>;
  preservedText?: string;
  onComplete: (path: string) => void;
}>();
const emit = defineEmits<{
  (e: "close"): void;
}>();
const { t } = useI18n();

const mode = ref<RecordingMode>(props.initialMode);
const state = ref<RecordingState>("starting");
const errorMessage = ref("");
const elapsedMs = ref(0);
const displayStream = ref<MediaStream | null>(null);
const previewElement = ref<HTMLVideoElement | null>(null);
const waveformLevels = ref<number[]>(Array.from({ length: 16 }, () => 0.15));
const screenCaptureAvailable =
  typeof navigator !== "undefined" && typeof navigator.mediaDevices?.getDisplayMedia === "function";
const minimumEncodedRecordingBytes = 8;

let microphoneStream: MediaStream | null = null;
let recordingStream: MediaStream | null = null;
let audioContext: AudioContext | null = null;
let meterContext: AudioContext | null = null;
let meterSource: MediaStreamAudioSourceNode | null = null;
let meterAnalyser: AnalyserNode | null = null;
let meterFrame: number | null = null;
let recorder: MediaRecorder | null = null;
let recordedChunks: Blob[] = [];
let startedAt = 0;
let timerId: number | null = null;
let prerollTimerId: number | null = null;
let resolvePreroll: (() => void) | null = null;
let requestController: AbortController | null = null;
let resolveRecorderStop: (() => void) | null = null;
let recorderStopPromise: Promise<void> | null = null;
let discarding = false;
let disposed = false;
let failureInProgress = false;

const statusText = computed(() => {
  if (state.value === "starting") return t("recordingStarting");
  if (state.value === "preroll" || state.value === "recording") {
    return mode.value === "screen" ? t("recordingScreenInProgress") : t("recordingInProgress");
  }
  if (state.value === "stopping") return t("recordingStopping");
  return t("recordingFailed");
});
const formattedElapsed = computed(() => {
  const totalSeconds = Math.floor(elapsedMs.value / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  return `${String(minutes).padStart(2, "0")}:${String(totalSeconds % 60).padStart(2, "0")}`;
});

function stopAudioMeter() {
  if (meterFrame !== null) cancelAnimationFrame(meterFrame);
  meterFrame = null;
  meterSource?.disconnect();
  meterAnalyser?.disconnect();
  meterSource = null;
  meterAnalyser = null;
  const context = meterContext;
  meterContext = null;
  if (context) void context.close();
  waveformLevels.value = waveformLevels.value.map(() => 0.15);
}

function startAudioMeter(stream: MediaStream) {
  stopAudioMeter();
  if (stream.getAudioTracks().length === 0 || typeof AudioContext !== "function") return;

  const context = new AudioContext();
  if (typeof context.createAnalyser !== "function") {
    void context.close();
    return;
  }
  const analyser = context.createAnalyser();
  analyser.fftSize = 64;
  analyser.smoothingTimeConstant = 0.7;
  const source = context.createMediaStreamSource(stream);
  source.connect(analyser);
  meterContext = context;
  meterSource = source;
  meterAnalyser = analyser;
  if (context.state === "suspended") void context.resume();

  const samples = new Uint8Array(analyser.fftSize);
  const draw = () => {
    if (meterAnalyser !== analyser) return;
    analyser.getByteTimeDomainData(samples);
    waveformLevels.value = waveformLevels.value.map((level, index, levels) => {
      const start = Math.floor((index / levels.length) * samples.length);
      const end = Math.floor(((index + 1) / levels.length) * samples.length);
      let peak = 0;
      for (let sampleIndex = start; sampleIndex < end; sampleIndex++) {
        peak = Math.max(peak, Math.abs((samples[sampleIndex] ?? 128) - 128));
      }
      const target = Math.max(0.1, Math.min(1, Math.pow(peak / 128, 0.65) * 2.3));
      return target >= level ? target : Math.max(0.1, level * 0.8 + target * 0.2);
    });
    meterFrame = requestAnimationFrame(draw);
  };
  draw();
}

watch(
  [previewElement, displayStream],
  ([element, stream]) => {
    if (element) element.srcObject = stream;
  },
  { flush: "post" },
);

function updateElapsed() {
  if (startedAt > 0) elapsedMs.value = Date.now() - startedAt;
}

function stopTimer() {
  if (timerId !== null) window.clearInterval(timerId);
  timerId = null;
  updateElapsed();
}

function cancelPreroll() {
  if (prerollTimerId !== null) window.clearTimeout(prerollTimerId);
  prerollTimerId = null;
  const resolve = resolvePreroll;
  resolvePreroll = null;
  resolve?.();
}

function waitForPreroll(): Promise<void> {
  return new Promise((resolve) => {
    resolvePreroll = resolve;
    prerollTimerId = window.setTimeout(() => {
      prerollTimerId = null;
      resolvePreroll = null;
      resolve();
    }, 500);
  });
}

async function cleanupMedia() {
  cancelPreroll();
  stopTimer();
  stopAudioMeter();
  const tracks = new Set<MediaStreamTrack>();
  for (const stream of [microphoneStream, displayStream.value, recordingStream]) {
    stream?.getTracks().forEach((track) => tracks.add(track));
  }
  tracks.forEach((track) => track.stop());
  microphoneStream = null;
  displayStream.value = null;
  recordingStream = null;
  recorder = null;
  resolveRecorderStop = null;
  recorderStopPromise = null;
  if (audioContext) {
    const context = audioContext;
    audioContext = null;
    await context.close();
  }
}

function bestMimeType(recordingMode: RecordingMode): string | undefined {
  const choices =
    recordingMode === "screen"
      ? ["video/webm;codecs=vp9,opus", "video/webm;codecs=vp8,opus", "video/webm", "video/mp4"]
      : ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"];
  return choices.find((mimeType) => MediaRecorder.isTypeSupported(mimeType));
}

function extensionForMimeType(mimeType: string): string {
  return mimeType.includes("mp4") ? ".mp4" : ".webm";
}

function recordingFilename(extension: string): string {
  const stamp = new Date()
    .toISOString()
    .replace(/[-:]/g, "")
    .replace("T", "-")
    .slice(0, 15);
  return `rec-${stamp}${extension}`;
}

async function responseError(response: Response, action: string): Promise<Error> {
  const detail = (await response.text()).trim();
  if (!detail) return new Error(`${action}: ${response.statusText}`);
  try {
    const payload = JSON.parse(detail) as { message?: unknown };
    if (typeof payload.message === "string" && payload.message.trim()) {
      return new Error(`${action}: ${payload.message.trim()}`);
    }
  } catch {
    // Use the plain response body below.
  }
  return new Error(`${action}: ${detail}`);
}

async function createScreenStream(screen: MediaStream): Promise<MediaStream> {
  microphoneStream = await navigator.mediaDevices.getUserMedia({ audio: true });
  if (discarding) throw new DOMException("Recording cancelled", "AbortError");

  const microphoneTracks = microphoneStream.getAudioTracks();
  const displayAudioTracks = screen.getAudioTracks();
  if (typeof AudioContext === "function" && displayAudioTracks.length > 0) {
    audioContext = new AudioContext();
    const destination = audioContext.createMediaStreamDestination();
    audioContext.createMediaStreamSource(new MediaStream(microphoneTracks)).connect(destination);
    audioContext.createMediaStreamSource(new MediaStream(displayAudioTracks)).connect(destination);
    if (audioContext.state === "suspended") await audioContext.resume();
    return new MediaStream([...screen.getVideoTracks(), ...destination.stream.getAudioTracks()]);
  }
  return new MediaStream([...screen.getVideoTracks(), ...microphoneTracks]);
}

async function createRecordingStream(recordingMode: RecordingMode): Promise<MediaStream> {
  if (recordingMode === "screen") return createScreenStream(displayStream.value!);
  microphoneStream = await navigator.mediaDevices.getUserMedia({ audio: true });
  return microphoneStream;
}

function afterNextPaint(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => window.setTimeout(resolve, 0));
  });
}

function collectChunk(blob: Blob) {
  if (blob.size === 0) return;
  recordedChunks.push(blob);
}

async function stopRecorder() {
  const current = recorder;
  if (!current || current.state === "inactive") return;
  current.stop();
  await recorderStopPromise;
}

async function discardCapture() {
  discarding = true;
  requestController?.abort();
  requestController = null;
  try {
    await stopRecorder();
  } finally {
    recordedChunks = [];
    await cleanupMedia();
  }
}

async function presentFailure(error: Error) {
  if (discarding || disposed || failureInProgress) return;
  failureInProgress = true;
  state.value = "stopping";
  try {
    await discardCapture();
  } finally {
    if (!disposed) {
      errorMessage.value = error.message;
      state.value = "error";
    }
    failureInProgress = false;
  }
}

function onDisplayEnded() {
  if (state.value === "preroll" || state.value === "recording") void stopRecording();
  else if (state.value === "starting") void presentFailure(new Error(t("recordingScreenEnded")));
}

async function startRecording(
  recordingMode: RecordingMode,
  selectedScreen?: MediaStream | Promise<MediaStream>,
) {
  mode.value = recordingMode;
  discarding = false;
  errorMessage.value = "";
  state.value = "starting";
  elapsedMs.value = 0;
  startedAt = 0;
  recordedChunks = [];

  try {
    if (recordingMode === "screen") {
      // Initial capture is requested by the composer; retries request it here
      // before yielding, while still in the Retry button's click handler.
      displayStream.value = await (
        selectedScreen ?? navigator.mediaDevices.getDisplayMedia({ video: true, audio: true })
      );
      if (discarding || disposed) {
        await cleanupMedia();
        return;
      }
      const video = displayStream.value.getVideoTracks()[0];
      if (!video || video.readyState === "ended") throw new Error(t("recordingScreenEnded"));
      video.addEventListener("ended", onDisplayEnded, { once: true });
    }
    await afterNextPaint();
    if (discarding || disposed) return;

    recordingStream = await createRecordingStream(recordingMode);
    if (discarding || disposed) {
      await cleanupMedia();
      return;
    }

    startAudioMeter(recordingStream);
    const selectedMimeType = bestMimeType(recordingMode);
    recorder = selectedMimeType
      ? new MediaRecorder(recordingStream, { mimeType: selectedMimeType })
      : new MediaRecorder(recordingStream);

    recorderStopPromise = new Promise<void>((resolve) => {
      resolveRecorderStop = resolve;
    });
    recorder.ondataavailable = (event) => collectChunk(event.data);
    recorder.onstop = () => resolveRecorderStop?.();
    recorder.onerror = (event) => {
      const cause = (event as Event & { error?: DOMException }).error;
      void presentFailure(cause ?? new Error(t("recordingFailed")));
    };
    recorder.start(1000);
    startedAt = Date.now();
    state.value = "preroll";
    await waitForPreroll();
    if (discarding || disposed || state.value !== "preroll") return;
    timerId = window.setInterval(updateElapsed, 250);
    updateElapsed();
    state.value = "recording";
  } catch (error) {
    requestController = null;
    if (discarding || disposed) {
      await cleanupMedia();
      return;
    }
    await presentFailure(error instanceof Error ? error : new Error(String(error)));
  }
}

function completeRecording(path: string) {
  props.onComplete(path);
  emit("close");
}

async function uploadRecording(filename: string, body: Blob): Promise<string> {
  requestController = new AbortController();
  const response = await fetch(`/api/upload/raw?filename=${encodeURIComponent(filename)}`, {
    method: "POST",
    headers: { "Content-Type": body.type || "application/octet-stream" },
    body,
    signal: requestController.signal,
  });
  requestController = null;
  if (!response.ok) throw await responseError(response, t("recordingFailed"));
  const uploaded = (await response.json()) as { path?: unknown };
  if (typeof uploaded.path !== "string" || uploaded.path.length === 0) {
    throw new Error(t("recordingInvalidResponse"));
  }
  return uploaded.path;
}

async function uploadVideoMetadata(path: string) {
  const filename = `${path.split("/").pop()}.json`;
  const body = new Blob(
    [JSON.stringify({ path, duration_ms: Math.round(elapsedMs.value) })],
    { type: "application/json" },
  );
  await uploadRecording(filename, body);
}

async function validateRecording(recording: Blob, mimeType: string) {
  if (recording.size < minimumEncodedRecordingBytes) throw new Error(t("recordingTooShort"));
  if (!mimeType.includes("webm")) return;
  const header = new Uint8Array(await recording.slice(0, 4).arrayBuffer());
  if (
    header[0] !== 0x1a ||
    header[1] !== 0x45 ||
    header[2] !== 0xdf ||
    header[3] !== 0xa3
  ) {
    throw new Error(t("recordingTooShort"));
  }
}

function handleStopPointerDown(event: PointerEvent) {
  if (event.pointerType === "mouse") return;
  event.preventDefault();
  void stopRecording();
}

async function stopRecording() {
  if ((state.value !== "preroll" && state.value !== "recording") || !recorder) return;
  state.value = "stopping";
  stopTimer();
  const mimeType = recorder.mimeType || "application/octet-stream";
  try {
    await stopRecorder();
    updateElapsed();
    const recording = new Blob(recordedChunks, { type: mimeType });
    await validateRecording(recording, mimeType);
    const filename = recordingFilename(extensionForMimeType(mimeType));
    const path = await uploadRecording(filename, recording);
    if (mode.value === "screen") await uploadVideoMetadata(path);
    recordedChunks = [];
    await cleanupMedia();
    completeRecording(path);
  } catch (error) {
    requestController = null;
    if (discarding || disposed) return;
    await presentFailure(error instanceof Error ? error : new Error(String(error)));
  }
}

async function restartWithScreen() {
  if (state.value !== "recording" || mode.value !== "microphone") return;

  // getDisplayMedia must be entered directly from the click handler. Keep the
  // microphone recording alive while the browser chooser is open; if the user
  // cancels or the browser rejects screen capture, their existing recording is
  // untouched.
  let screen: MediaStream;
  try {
    screen = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: true });
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error);
    return;
  }
  if (disposed) {
    screen.getTracks().forEach((track) => track.stop());
    return;
  }

  state.value = "stopping";
  await discardCapture();
  if (!disposed) await startRecording("screen", screen);
  else screen.getTracks().forEach((track) => track.stop());
}

async function retry() {
  if (state.value !== "error") return;
  await startRecording(mode.value);
}

async function cancelRecording() {
  if (state.value === "stopping") return;
  state.value = "stopping";
  await discardCapture();
  if (!disposed) emit("close");
}

onMounted(() => void startRecording(props.initialMode, props.initialScreen));

onBeforeUnmount(() => {
  disposed = true;
  discarding = true;
  requestController?.abort();
  if (recorder?.state !== "inactive") recorder?.stop();
  recordedChunks = [];
  void cleanupMedia();
});
</script>
