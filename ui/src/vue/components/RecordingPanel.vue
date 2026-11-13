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
          v-if="capturedRecording && state === 'error'"
          class="recording-status-label"
          data-testid="recording-retained"
        >{{ t("recordingRetained") }}</span>
        <span
          v-if="state === 'preroll' || state === 'recording' || (!errorMessage && !destinationError)"
          class="recording-status-label"
          data-testid="recording-status"
        >{{ statusText }}</span>
        <span
          v-if="errorMessage || destinationError"
          class="recording-error"
          :title="errorMessage || destinationError"
          role="alert"
          data-testid="recording-error"
        >
          {{ errorMessage || destinationError }}
        </span>
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
        :disabled="submitting || (state === 'stopping' && !capturedRecording)"
        :aria-label="capturedRecording ? t('recordingDiscard') : t('cancel')"
        data-testid="recording-cancel-button"
        @click="cancelRecording"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 6l12 12M18 6 6 18" />
        </svg>
        <span class="recording-action-label">{{ capturedRecording ? t("recordingDiscard") : t("cancel") }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { apiFetch } from "../../services/api";
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { useI18n } from "../composables/i18n";
import type { RecordingMode } from "./recordingDestination";
import { startRecordingMeter } from "./recordingMeter";
import { startPageTrace, type PageTrace } from "../composables/bookmarklet";

type RecordingState = "starting" | "preroll" | "recording" | "stopping" | "error";

const props = defineProps<{
  initialMode: RecordingMode;
  initialMicrophone?: Promise<MediaStream>;
  initialScreen?: Promise<MediaStream>;
  preservedText?: string;
  destinationError?: string;
  submitting?: boolean;
  onComplete: (path: string, retry: boolean) => Promise<void>;
  onUploadFailure: (path: string, error: Error) => void;
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
const capturedRecording = shallowRef<{
  // Uploading hands the bytes to the server; keep only their path afterwards.
  body?: Blob;
  filename: string;
  durationMs: number;
  path?: string;
  metadataUploaded: boolean;
  // What the user did on the bookmarklet's page meanwhile (.review.json).
  review?: Blob;
} | null>(null);
const screenCaptureAvailable =
  typeof navigator !== "undefined" && typeof navigator.mediaDevices?.getDisplayMedia === "function";
const minimumEncodedRecordingBytes = 8;

let microphoneStream: MediaStream | null = null;
let recordingStream: MediaStream | null = null;
let audioContext: AudioContext | null = null;
let stopMeter: (() => void) | null = null;
let recorder: MediaRecorder | null = null;
let recordedChunks: Blob[] = [];
let startedAt = 0;
let pageTrace: PageTrace | null = null;
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
  stopMeter?.();
  stopMeter = null;
  waveformLevels.value = waveformLevels.value.map(() => 0.15);
}

function startAudioMeter(stream: MediaStream) {
  stopAudioMeter();
  stopMeter = startRecordingMeter(stream, waveformLevels.value.length, (levels) => {
    waveformLevels.value = levels;
  });
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
    }, 100);
  });
}

async function cleanupMedia() {
  pageTrace?.cancel();
  pageTrace = null;
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
  resolveRecorderStop?.();
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

async function createRecordingStream(
  recordingMode: RecordingMode,
  selectedMicrophone?: MediaStream | Promise<MediaStream>,
): Promise<MediaStream> {
  if (recordingMode === "screen") return createScreenStream(displayStream.value!);
  microphoneStream = await (
    selectedMicrophone ?? navigator.mediaDevices.getUserMedia({ audio: true })
  );
  return microphoneStream;
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
  capturedRecording.value = null;
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
  selectedMicrophone?: MediaStream | Promise<MediaStream>,
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
    recordingStream = await createRecordingStream(recordingMode, selectedMicrophone);
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
    recorder.ondataavailable = (event) => {
      if (!discarding && !disposed) collectChunk(event.data);
    };
    recorder.onstop = () => resolveRecorderStop?.();
    recorder.onerror = (event) => {
      const cause = (event as Event & { error?: DOMException }).error;
      void presentFailure(cause ?? new Error(t("recordingFailed")));
    };
    recorder.start(1000);
    startedAt = Date.now();
    pageTrace = startPageTrace();
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

async function uploadRecording(filename: string, body: Blob): Promise<string> {
  const controller = new AbortController();
  requestController = controller;
  try {
    const response = await apiFetch(
      `/api/upload/raw?filename=${encodeURIComponent(filename)}`,
      t("recordingFailed"),
      {
        method: "POST",
        headers: { "Content-Type": body.type || "application/octet-stream" },
        body,
        signal: controller.signal,
      },
    );
    const uploaded = (await response.json()) as { path?: unknown };
    if (typeof uploaded.path !== "string" || uploaded.path.length === 0) {
      throw new Error(t("recordingInvalidResponse"));
    }
    return uploaded.path;
  } finally {
    if (requestController === controller) requestController = null;
  }
}

async function uploadVideoMetadata(path: string, durationMs: number) {
  const filename = `${path.split("/").pop()}.json`;
  const body = new Blob(
    [JSON.stringify({ path, duration_ms: Math.round(durationMs) })],
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
    if (discarding || disposed) return;
    updateElapsed();
    const recording = new Blob(recordedChunks, { type: mimeType });
    await validateRecording(recording, mimeType);
    if (discarding || disposed) return;
    capturedRecording.value = {
      body: recording,
      filename: recordingFilename(extensionForMimeType(mimeType)),
      durationMs: elapsedMs.value,
      metadataUploaded: false,
      review: pageTrace?.stop(startedAt, elapsedMs.value) ?? undefined,
    };
    recordedChunks = [];
    await cleanupMedia();
  } catch (error) {
    if (discarding || disposed) return;
    await presentFailure(error instanceof Error ? error : new Error(String(error)));
    return;
  }
  await finishRecording(false);
}

async function finishRecording(retry: boolean) {
  const captured = capturedRecording.value;
  if (!captured || discarding || disposed) return;
  state.value = "stopping";
  errorMessage.value = "";
  let submitting = false;
  try {
    if (!captured.path) {
      captured.path = await uploadRecording(captured.filename, captured.body!);
      // A review keeps the bytes until it is uploaded beside them.
      if (!captured.review) captured.body = undefined;
    }
    if (mode.value === "screen" && !captured.metadataUploaded) {
      await uploadVideoMetadata(captured.path!, captured.durationMs);
      captured.metadataUploaded = true;
    }
    if (captured.review) {
      // The server finds a recording's review by name.
      const expected = `${captured.path}.review.json`;
      const uploaded = await uploadRecording(expected.split("/").pop()!, captured.review);
      if (uploaded !== expected) {
        // An earlier attempt stored the review but lost the response, so its
        // name was taken. Retrying uploads a fresh pair.
        captured.path = undefined;
        captured.metadataUploaded = false;
        throw new Error(t("recordingInvalidResponse"));
      }
      captured.review = undefined;
      captured.body = undefined;
    }
    if (discarding || disposed) return;
    submitting = true;
    await props.onComplete(captured.path!, retry);
    if (discarding || disposed) return;
    capturedRecording.value = null;
    emit("close");
  } catch (error) {
    if (discarding || disposed) return;
    const failure = error instanceof Error ? error : new Error(String(error));
    if (captured.path && !submitting) props.onUploadFailure(captured.path, failure);
    errorMessage.value = failure.message;
    state.value = "error";
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
  if (capturedRecording.value) await finishRecording(true);
  else await startRecording(mode.value);
}

async function cancelRecording() {
  if (props.submitting || (state.value === "stopping" && !capturedRecording.value)) return;
  state.value = "stopping";
  await discardCapture();
  if (!disposed) emit("close");
}

onMounted(() => void startRecording(props.initialMode, props.initialScreen, props.initialMicrophone));

onBeforeUnmount(() => {
  disposed = true;
  discarding = true;
  requestController?.abort();
  if (recorder?.state !== "inactive") recorder?.stop();
  recordedChunks = [];
  capturedRecording.value = null;
  void cleanupMedia();
});
</script>
