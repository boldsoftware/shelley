<!-- GPT-Live's persistent call controls. It handles the spoken
     conversation and brings in Shelley only when it delegates work: Shelley
     hears Live's events and says what to tell Live. -->
<template>
  <div
    v-if="phase !== 'idle' || error"
    class="recording-panel live-call"
    :data-call="error ? 'failed' : phase === 'connecting' ? 'connecting' : muted ? 'muted' : 'live'"
    data-testid="live-bar"
  >
    <div class="recording-panel-main">
      <div class="recording-status" role="status" aria-live="polite">
        <span
          :class="[
            'recording-status-dot',
            phase === 'connecting' ? 'starting' : error ? 'error' : 'live',
          ]"
          aria-hidden="true"
        />
        <button
          v-if="!error"
          type="button"
          class="live-call-origin"
          :disabled="!viewingElsewhere || !origin.conversation.value || !onReturn"
          :aria-label="viewingElsewhere ? `Return to call in ${originTitle}` : undefined"
          :title="viewingElsewhere ? `Return to call in ${originTitle}` : undefined"
          data-testid="live-call-origin"
          @click="returnToCall"
        >
          <span class="live-call-label">
            {{ phase === "connecting" ? "Connecting…" : muted ? "Muted" : "On call" }}
          </span>
          <span v-if="viewingElsewhere" class="live-call-origin-name">↩ {{ originTitle }}</span>
        </button>
        <span v-else-if="error" class="recording-error" :title="error" role="alert" data-testid="live-error">
          {{ error }}
        </span>
        <time
          v-if="phase === 'live'"
          class="recording-timer"
          :datetime="`PT${elapsedSeconds}S`"
          data-testid="live-timer"
        >{{ formattedElapsed }}</time>
      </div>
      <div
        v-if="phase !== 'idle'"
        class="recording-waveform"
        :class="{ 'recording-waveform-preroll': phase === 'connecting' }"
        aria-hidden="true"
        data-testid="live-meter"
      >
        <span
          v-for="(level, index) in waveformLevels"
          :key="index"
          class="recording-waveform-bar"
          :style="{ height: `${Math.max(4, level * 20)}px` }"
        />
      </div>
    </div>
    <div class="recording-actions">
      <button
        v-if="phase === 'live'"
        type="button"
        class="btn btn-secondary live-mute-btn"
        aria-label="Mute microphone"
        :aria-pressed="muted"
        data-testid="live-mute"
        @click="toggleMute"
      >
        <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <rect x="9" y="2" width="6" height="12" rx="3" />
          <path d="M5 10a7 7 0 0 0 14 0M12 17v4m-4 0h8" />
          <path v-if="muted" d="M3 3l18 18" />
        </svg>
        <span class="recording-action-label">Mute</span>
      </button>
      <button
        v-if="phase === 'live'"
        type="button"
        class="btn btn-primary recording-stop-btn"
        aria-label="End voice call"
        data-testid="live-stop"
        @click="stop()"
      >
        <svg fill="currentColor" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path d="M6.62 10.79a15.05 15.05 0 0 0 6.59 6.59l2.2-2.2a1 1 0 0 1 1.02-.24c1.12.37 2.33.57 3.57.57a1 1 0 0 1 1 1V20a1 1 0 0 1-1 1C10.61 21 3 13.39 3 4a1 1 0 0 1 1-1h3.5a1 1 0 0 1 1 1c0 1.25.2 2.45.57 3.57a1 1 0 0 1-.25 1.02l-2.2 2.2Z" transform="rotate(135 12 12)" />
        </svg>
        <span class="recording-action-label">End call</span>
      </button>
      <button
        v-if="phase === 'connecting'"
        type="button"
        class="btn btn-secondary"
        aria-label="Cancel voice call"
        data-testid="live-stop"
        @click="stop()"
      >
        <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
          <path d="M6 6l12 12M18 6 6 18" />
        </svg>
        <span class="recording-action-label">Cancel</span>
      </button>
      <template v-if="phase === 'idle' && error">
        <button type="button" class="btn btn-primary" aria-label="Retry voice call" data-testid="live-retry" @click="start()">
          <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
            <path d="M4 4v5h5M20 20v-5h-5M5.6 9A7 7 0 0 1 18.4 9M18.4 15A7 7 0 0 1 5.6 15" />
          </svg>
          <span class="recording-action-label">Retry</span>
        </button>
        <button type="button" class="btn btn-secondary" aria-label="Dismiss voice call error" @click="error = null">
          <svg fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
            <path d="M6 6l12 12M18 6 6 18" />
          </svg>
          <span class="recording-action-label">Dismiss</span>
        </button>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { api, type LiveConversationSettings } from "../../services/api";
import { LiveSession, type LiveEvent } from "../../services/liveSession";
import { LiveTranscript, readLiveDeliveryID } from "../../services/liveTranscript";
import { parseQueuedMessages, type Conversation } from "../../types";
import { useLiveCallConversation, type LiveCallDestination } from "../composables/liveCallConversation";
import { startRecordingMeter } from "./recordingMeter";

const props = defineProps<{
  conversationId: string | null;
  disabled: boolean;
  captureConversation: () => LiveCallDestination;
  getConversation: (id: string) => Conversation | undefined;

  onReturn?: (conversation: Conversation) => void;
}>();
const emit = defineEmits<{ (e: "phase-change", phase: "idle" | "connecting" | "live"): void }>();
defineExpose({
  start,
  stop,
  returnToCall,
  transcript: computed(() => transcript.value.turns),
  transcriptConversationId: computed(() => origin.conversationId.value),
  transcriptTitle: computed(() => originTitle.value),
});

const phase = ref<"idle" | "connecting" | "live">("idle");
watch(phase, (value) => emit("phase-change", value), { flush: "sync" });
const error = ref<string | null>(null);
const muted = ref(false);
const origin = useLiveCallConversation(props.getConversation);
const transcript = ref(new LiveTranscript());
const savedDeliveryIDs = computed(() =>
  [
    ...origin.messages.value.filter((message) => message.type === "user"),
    ...parseQueuedMessages(origin.conversation.value?.queued_messages),
  ].map(readLiveDeliveryID).filter((id): id is string => id !== undefined),
);
function reconcileTranscript() {
  transcript.value.reconcile(savedDeliveryIDs.value);
}
watch(savedDeliveryIDs, reconcileTranscript);
const viewingElsewhere = computed(() =>
  phase.value !== "idle" && props.conversationId !== origin.conversationId.value,
);
const originTitle = computed(() => origin.conversation.value?.slug || "New chat");
function returnToCall() {
  if (origin.conversation.value) props.onReturn?.(origin.conversation.value);
}

const METER_BARS = 56;
const waveformLevels = ref<number[]>(Array.from({ length: METER_BARS }, () => 0.15));
const elapsedSeconds = ref(0);
const formattedElapsed = computed(() => {
  const minutes = Math.floor(elapsedSeconds.value / 60).toString().padStart(2, "0");
  const seconds = (elapsedSeconds.value % 60).toString().padStart(2, "0");
  return `${minutes}:${seconds}`;
});

let session: LiveSession | null = null;
let connecting: AbortController | null = null;
let settings: LiveConversationSettings | undefined;
let instructions: EventSource | null = null;
let stopMeter: (() => void) | null = null;
let timerID: ReturnType<typeof setInterval> | null = null;

// What Shelley reads of Live's events; it decides what speech becomes a task.
const RELAYED = new Set([
  "session.input_transcript.delta",
  "session.output_transcript.delta",
  "session.delegation.created",
  "session.closed",
]);
let outbox: LiveEvent[] = [];
let relaying = false;
let inFlightRelay: Promise<void> | null = null;

function onEvent(event: LiveEvent) {
  if (event.type === "session.input_transcript.delta" || event.type === "session.output_transcript.delta") {
    const text = typeof event.delta === "string" ? event.delta : event.delta == null ? "" : JSON.stringify(event.delta);
    transcript.value.append(
      event.type === "session.input_transcript.delta",
      text,
      event.start_ms as number,
      event.end_ms as number,
    );
  }
  if (event.type === "error") {
    error.value = String((event.error as { message?: string } | undefined)?.message ?? "Live session error");
  } else if (RELAYED.has(event.type)) {
    outbox.push(event);
    void relay();
  }
}

// Events reach Shelley in order: one request at a time, batching those
// that arrive meanwhile.
async function relay() {
  const live = session;
  const conversationId = origin.conversationId.value;
  if (relaying || !live || !conversationId) return;
  relaying = true;
  try {
    while (session === live && outbox.length) {
      const events = outbox;
      outbox = [];
      inFlightRelay = api.sendLiveEvents(conversationId, live.id, events);
      await inFlightRelay;
    }
  } catch (err) {
    if (session !== live) return;
    // Without Shelley's side of the call nothing the user asks can reach it.
    stop();
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (session === live) {
      relaying = false;
      inFlightRelay = null;
    }
  }
}

interface Instruction {
  type: string;
  event?: LiveEvent;
  message?: string | null;
  delivery_id?: string;
  through?: number;
  fragments?: number[];
}

function follow(live: LiveSession, conversationId: string) {
  const stream = new EventSource(api.liveCallInstructionsURL(conversationId, live.id));
  instructions = stream;
  stream.onmessage = (e) => {
    if (session !== live) return;
    const instruction = JSON.parse(e.data) as Instruction;
    switch (instruction.type) {
      case "handoff":
        transcript.value.handoff(instruction.through!, instruction.delivery_id!, instruction.fragments);
        reconcileTranscript();
        break;
      case "send":
        try {
          live.send(instruction.event!);
        } catch (err) {
          error.value = err instanceof Error ? err.message : String(err);
        }
        break;
      case "error":
        error.value = instruction.message ?? null;
        break;
      case "stop":
        stop();
        error.value = instruction.message ?? null;
        break;
    }
  };
  // EventSource reconnects by itself (resuming after the last instruction);
  // it gives up only when Shelley no longer has the call.
  stream.onerror = () => {
    if (session !== live || stream.readyState !== EventSource.CLOSED) return;
    stop();
    error.value = "Lost the call's connection to Shelley.";
  };
}

async function start() {
  if (phase.value !== "idle" || props.disabled) return;
  error.value = null;
  let destination: LiveCallDestination;
  try {
    destination = props.captureConversation();
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    return;
  }
  phase.value = "connecting";
  settings = destination.settings;
  const abort = new AbortController();
  connecting = abort;
  if (destination.conversationId) origin.bind(destination.conversationId, destination.conversation);
  let started: LiveSession | undefined;
  try {
    started = await LiveSession.start(
      async () => {
        if (destination.conversationId) return destination.conversationId;
        // For /new, create the draft only once the microphone and peer are
        // ready, so a denied permission leaves no empty conversation behind.
        const id = await destination.prepareConversation();
        abort.signal.throwIfAborted();
        origin.bind(id);
        return id;
      },
      (event) => {
        if (session === started) onEvent(event);
      },
      (err) => {
        if (session !== started) return;
        reset();
        if (err) error.value = err.message;
      },
      abort.signal,
      () => settingsForDraft(),
    );
    session = started;
    follow(started, origin.conversationId.value!);
    phase.value = "live";
    const startedAt = performance.now();
    timerID = setInterval(() => {
      elapsedSeconds.value = Math.floor((performance.now() - startedAt) / 1000);
    }, 1000);
    // The meter is feedback, not transport: a device without an audio graph
    // still calls.
    try {
      stopMeter = startRecordingMeter(started.microphoneStream, METER_BARS, (levels) => {
        waveformLevels.value = levels;
      });
    } catch {
      // The call still works without the level meter.
    }
  } catch (err) {
    if (abort.signal.aborted) return; // stop() already reset the panel
    reset();
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (connecting === abort) connecting = null;
  }
}

// A draft's selection travels with its call, whose delegations promote the
// draft with it; a conversation under way has its own settings.
function settingsForDraft() {
  return origin.conversation.value?.is_draft !== false ? settings : undefined;
}

function reset() {
  transcript.value = new LiveTranscript();
  stopMeter?.();
  stopMeter = null;
  if (timerID !== null) clearInterval(timerID);
  timerID = null;
  elapsedSeconds.value = 0;
  waveformLevels.value = Array.from({ length: METER_BARS }, () => 0.15);
  instructions?.close();
  instructions = null;
  const conversationId = origin.conversationId.value;
  if (session && conversationId) {
    void finishCall(conversationId, session.id, outbox, inFlightRelay);
  }
  session = null;
  origin.release();
  outbox = [];
  relaying = false;
  inFlightRelay = null;
  muted.value = false;
  phase.value = "idle";
}

async function finishCall(conversationId: string, sessionId: string, events: LiveEvent[], pending: Promise<void> | null) {
  const failures: string[] = [];
  try {
    await pending;
  } catch (err) {
    failures.push(err instanceof Error ? err.message : String(err));
  }
  try {
    if (!failures.length && events.length) await api.sendLiveEvents(conversationId, sessionId, events);
  } catch (err) {
    failures.push(`Could not send the call's final voice events: ${err instanceof Error ? err.message : String(err)}`);
  }
  try {
    await api.endLiveCall(conversationId, sessionId);
  } catch (err) {
    failures.push(`Could not end the call: ${err instanceof Error ? err.message : String(err)}`);
  }
  if (failures.length) error.value = failures.join(" ");
}

function stop() {
  connecting?.abort();
  connecting = null;
  session?.close();
  reset();
  error.value = null;
}

function toggleMute() {
  if (!session) return;
  session.setMuted(!session.muted);
  muted.value = session.muted;
}

// Navigation never ends a call; losing its conversation does.
watch(origin.invalidated, (invalidated) => {
  if (invalidated) stop();
});
window.addEventListener("pagehide", stop);
onBeforeUnmount(() => {
  window.removeEventListener("pagehide", stop);
  stop();
});
</script>

<style scoped>
.live-call {
  margin-bottom: 0.5rem;
}
.live-call .recording-panel-main {
  gap: 0.625rem;
}
.live-call:not([data-call="failed"]) .recording-status {
  flex-shrink: 0;
}
.live-call .recording-waveform {
  flex: 1 1 222px;
  width: auto;
  min-width: 80px;
  max-width: 222px;
  justify-content: flex-start;
  gap: 2px;
  overflow: hidden;
}
.live-call .recording-waveform-bar {
  flex: 0 0 2px;
}
.live-call .recording-error {
  white-space: normal;
  overflow-wrap: anywhere;
}
.live-call[data-call="failed"] {
  min-height: 46px;
  height: auto;
  border-color: var(--error-border);
}
.live-call .recording-waveform-bar,
.live-call .recording-status-dot.live {
  background: var(--green-600);
}
.live-call .recording-waveform::before {
  content: none;
}
.live-call[data-call="muted"] .recording-waveform-bar {
  background: var(--gray-400);
}
.live-call[data-call="muted"] .recording-status-dot.live,
.live-call[data-call="muted"] .live-call-label,
.live-call[data-call="muted"] .live-mute-btn {
  color: var(--warning-text);
}
.live-call[data-call="muted"] .recording-status-dot.live {
  background: var(--warning-text);
}
.live-call[data-call="muted"] .live-mute-btn {
  color: var(--warning-text);
  border-color: var(--warning-border);
  background: var(--warning-bg);
}
.live-call-label {
  flex: 0 0 auto;
  white-space: nowrap;
}
.live-call-origin {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  min-width: 0;
  max-width: 8rem;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  overflow: hidden;
  cursor: pointer;
}
.live-call-origin-name {
  max-width: 100%;
  font-size: 0.6875rem;
  line-height: 1.2;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.live-call-origin:disabled {
  cursor: default;
}
.live-call-origin:enabled:hover {
  text-decoration: underline;
}
.live-call .recording-actions {
  gap: 0.375rem;
}
@container recording-panel (width < 20rem) {
  .live-call .recording-waveform {
    display: flex;
  }
  .live-call-label {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  .live-call-origin {
    max-width: 3.5rem;
  }
}
</style>
