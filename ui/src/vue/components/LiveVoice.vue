<!-- GPT-Live's call controls for this conversation. It handles the spoken
     conversation and brings in Shelley only when it delegates work. -->
<template>
  <div class="live-voice" data-testid="live-voice">
    <button
      type="button"
      class="live-btn"
      :class="{ active: phase !== 'idle' }"
      :disabled="disabled && phase === 'idle'"
      :aria-label="phase === 'idle' ? 'Call Shelley' : 'End call with Shelley'"
      :aria-expanded="phase !== 'idle' || !!error"
      :title="
        disabled && phase === 'idle'
          ? 'Open a conversation to call Shelley'
          : phase === 'idle'
            ? 'Call Shelley'
            : 'End call with Shelley'
      "
      data-testid="live-button"
      @click="onButton"
    >
      <svg
        fill="currentColor"
        stroke="none"
        viewBox="0 0 24 24"
        width="18"
        height="18"
        aria-hidden="true"
      >
        <path
          d="M6.62 10.79a15.05 15.05 0 0 0 6.59 6.59l2.2-2.2a1 1 0 0 1 1.02-.24c1.12.37 2.33.57 3.57.57a1 1 0 0 1 1 1V20a1 1 0 0 1-1 1C10.61 21 3 13.39 3 4a1 1 0 0 1 1-1h3.5a1 1 0 0 1 1 1c0 1.25.2 2.45.57 3.57a1 1 0 0 1-.25 1.02l-2.2 2.2Z"
        />
      </svg>
    </button>

    <div
      v-if="phase !== 'idle' || error"
      class="live-panel"
      role="region"
      aria-label="Voice session"
      data-testid="live-panel"
    >
      <div class="live-panel-head">
        <span class="live-status">
          <template v-if="phase === 'connecting'">Connecting…</template>
          <template v-else-if="phase === 'live'">{{ muted ? "Muted" : "Listening" }}</template>
          <template v-else>Voice session ended</template>
        </span>
        <span class="live-head-actions">
          <button
            v-if="phase === 'live'"
            type="button"
            class="live-action"
            :aria-pressed="muted"
            data-testid="live-mute"
            @click="toggleMute"
          >
            {{ muted ? "Unmute" : "Mute" }}
          </button>
          <button
            v-if="phase !== 'idle'"
            type="button"
            class="live-action"
            data-testid="live-stop"
            @click="stop()"
          >
            End call
          </button>
          <button v-else type="button" class="live-action" @click="error = null">Dismiss</button>
        </span>
      </div>

      <p v-if="error" class="live-error" role="alert" data-testid="live-error">{{ error }}</p>

      <div v-if="phase === 'live'" class="live-captions" aria-live="polite">
        <p v-if="caption.user" class="live-line">
          <span class="live-who">You</span>{{ caption.user }}
        </p>
        <p v-if="caption.assistant" class="live-line">
          <span class="live-who">Live</span>{{ caption.assistant }}
        </p>
        <p v-if="!caption.user && !caption.assistant" class="live-hint">
          Talk naturally. GPT-Live brings in Shelley when work is needed.
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from "vue";
import { LiveSession, type LiveEvent } from "../../services/liveSession";
import type { Message } from "../../types";
import { isHumanUserMessage } from "../../utils/conversationView";
import {
  delegatedSpeech,
  formatVoiceContext,
  isLikelyVoiceEcho,
  messageText,
  truncateCommentary,
  type TranscriptFragment,
  type VoiceFragment,
} from "../../utils/liveTranscript";

interface PendingDelegation {
  id: string;
  offsetMs: number;
  settleTimer: ReturnType<typeof setTimeout> | null;
  deadlineTimer: ReturnType<typeof setTimeout>;
}

interface SubmittedDelegation {
  id: string;
  message: string;
  afterSeq: number;
  userSeq: number | null;
  finished: boolean;
}

const props = defineProps<{
  conversationId: string | null;
  disabled: boolean;
  messages: Message[];
  // The server interprets speech with its small model and delivers it to the
  // current conversation, even while the agent is working.
  sendMessage: (
    id: string,
    transcript: string,
    voiceContext: string,
    offsetMs: number,
    conversationId: string,
  ) => Promise<{ status: "accepted" | "queued"; message: string; task: string }>;
}>();

const phase = ref<"idle" | "connecting" | "live">("idle");
const error = ref<string | null>(null);
const muted = ref(false);
const caption = reactive({ user: "", assistant: "" });

// Live provides no transcript turn boundary; settle after late deltas, with
// a hard deadline for an interrupted utterance. Neither timer triggers a
// backend request unless Live explicitly created a client delegation.
const TRANSCRIPT_QUIET_MS = 1200;
const DELEGATION_DEADLINE_MS = 5000;
const MAX_DELEGATIONS_PER_MINUTE = 12;
let session: LiveSession | null = null;
let connecting: AbortController | null = null;
let userFragments: TranscriptFragment[] = [];
let voiceFragments: VoiceFragment[] = [];
let claimedUserCount = 0;
let pending = new Map<string, PendingDelegation>();
let seenDelegations = new Set<string>();
let submitted: SubmittedDelegation[] = [];
let sessionKey = "";
let lastSeenSequence = 0;
let sendChain: Promise<void> = Promise.resolve();
let sendTimes: number[] = [];
let inflightRequests = 0;
let commentarySeq = 0;
const lastEnd = { user: 0, assistant: 0 };

// Shown caption is the current utterance; a pause starts a fresh one.
function appendCaption(who: "user" | "assistant", event: LiveEvent) {
  const delta = String(event.delta ?? "");
  const start = Number(event.start_ms ?? 0);
  if (start - lastEnd[who] > 2000) caption[who] = "";
  lastEnd[who] = Number(event.end_ms ?? start);
  caption[who] = (caption[who] + delta).replace(/\s+/g, " ").trimStart().slice(-280);
}

function onEvent(event: LiveEvent) {
  switch (event.type) {
    case "session.input_transcript.delta": {
      appendCaption("user", event);
      const fragment = {
        text: String(event.delta ?? ""),
        startMs: Number(event.start_ms ?? 0),
        endMs: Number(event.end_ms ?? 0),
      };
      userFragments.push(fragment);
      if (pending.size === 0 && userFragments.length > 200) {
        const drop = userFragments.length - 200;
        userFragments.splice(0, drop);
        claimedUserCount = Math.max(0, claimedUserCount - drop);
      }
      voiceFragments.push({ ...fragment, speaker: "user" });
      if (voiceFragments.length > 1000) voiceFragments.shift();
      for (const task of pending.values()) scheduleSettle(task);
      break;
    }
    case "session.output_transcript.delta": {
      appendCaption("assistant", event);
      voiceFragments.push({
        speaker: "assistant",
        text: String(event.delta ?? ""),
        startMs: Number(event.start_ms ?? 0),
        endMs: Number(event.end_ms ?? 0),
      });
      if (voiceFragments.length > 1000) voiceFragments.shift();
      break;
    }
    case "session.delegation.created":
      onDelegation(event);
      break;
    case "error":
      error.value = String(
        (event.error as { message?: string } | undefined)?.message ?? "Live session error",
      );
      break;
  }
}

function onDelegation(event: LiveEvent) {
  const delegation = event.delegation as { id?: string; target?: string } | undefined;
  if (!delegation?.id || delegation.target !== "client") return;
  if (seenDelegations.has(delegation.id)) return;
  const id = delegation.id;
  seenDelegations.add(id);
  const offsetMs = Number(event.offset_ms ?? 0);
  if (!Number.isFinite(offsetMs) || offsetMs < 0) {
    error.value = "Live returned an invalid delegation timestamp.";
    return;
  }
  const task: PendingDelegation = {
    id,
    offsetMs,
    settleTimer: null,
    deadlineTimer: setTimeout(() => commitDelegation(id), DELEGATION_DEADLINE_MS),
  };
  pending.set(task.id, task);
  const last = userFragments.at(-1);
  if (last && userFragments.length > claimedUserCount && last.endMs + 2500 >= offsetMs) {
    scheduleSettle(task);
  }
}

function scheduleSettle(task: PendingDelegation) {
  if (task.settleTimer) clearTimeout(task.settleTimer);
  task.settleTimer = setTimeout(() => commitDelegation(task.id), TRANSCRIPT_QUIET_MS);
}

// Commentary can be spoken; thinking keeps Live current without interrupting
// every conversation update. Both contain only user-visible Shelley text.
function appendContext(
  type: "session.commentary.append" | "session.thinking.append",
  content: string,
  delegationId: string | null = null,
) {
  try {
    session?.send({
      type,
      event_id: `shelley_${++commentarySeq}`,
      delegation_id: delegationId,
      content: truncateCommentary(content),
    });
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

function commitDelegation(id: string) {
  const task = pending.get(id);
  if (!task) return;
  if (task.settleTimer) clearTimeout(task.settleTimer);
  clearTimeout(task.deadlineTimer);
  pending.delete(id);

  const speech = delegatedSpeech(userFragments.slice(claimedUserCount), task.offsetMs);
  claimedUserCount = userFragments.length;
  if (pending.size === 0 && claimedUserCount > 100) {
    userFragments = [];
    claimedUserCount = 0;
  }
  if (!speech || isLikelyVoiceEcho(speech, caption.assistant)) {
    appendContext(
      "session.thinking.append",
      "No clear new request was captured. Ask the user to clarify.",
      id,
    );
    return;
  }
  const conversationId = props.conversationId;
  if (!conversationId || !session) return;

  const now = Date.now();
  sendTimes = sendTimes.filter((sentAt) => now - sentAt < 60_000);
  if (sendTimes.length >= MAX_DELEGATIONS_PER_MINUTE) {
    stop();
    error.value =
      "Voice delegation paused to prevent a feedback loop. Start voice again to continue.";
    return;
  }
  sendTimes.push(now);
  const voiceContext = formatVoiceContext(voiceFragments);
  const requestID = `${sessionKey}:${id}`;
  const originSessionKey = sessionKey;
  // Preserve delegation order even when the previous rewrite is still running.
  sendChain = sendChain.then(async () => {
    const stillConnected = () => sessionKey === originSessionKey && phase.value === "live";
    if (stillConnected()) inflightRequests++;
    const afterSeq = props.messages.reduce((max, m) => Math.max(max, m.sequence_id), 0);
    try {
      const result = await props.sendMessage(
        requestID,
        speech,
        voiceContext,
        task.offsetMs,
        conversationId,
      );
      if (!stillConnected()) return;
      submitted.push({
        id,
        message: result.message,
        afterSeq,
        userSeq: null,
        finished: false,
      });
      appendContext(
        "session.thinking.append",
        result.status === "queued"
          ? `Shelley's task is queued behind earlier work: ${result.task}`
          : `Shelley accepted this task: ${result.task}`,
        id,
      );
    } catch (err) {
      if (!stillConnected()) return;
      const detail = err instanceof Error ? err.message : String(err);
      error.value = detail;
      appendContext(
        "session.commentary.append",
        `Shelley could not start that task: ${detail}`,
        id,
      );
    } finally {
      if (stillConnected()) {
        inflightRequests--;
        syncConversation();
      }
    }
  });
}

// Keep Live informed of real Shelley work, including an agent turn already
// running when voice starts. Only work accepted for a delegation is scoped
// to that ID; ordinary conversation never becomes a backend task.
function syncConversation() {
  if (!session || phase.value !== "live" || inflightRequests > 0) return;
  const unseen = props.messages
    .filter((m) => m.sequence_id > lastSeenSequence)
    .sort((a, b) => a.sequence_id - b.sequence_id);
  for (const row of unseen) {
    lastSeenSequence = row.sequence_id;
    const text = messageText(row);
    if (!text) continue;
    if (row.type === "user" && isHumanUserMessage(row)) {
      const task = submitted.find(
        (d) => d.userSeq === null && row.sequence_id > d.afterSeq && d.message === text,
      );
      if (task) {
        task.userSeq = row.sequence_id;
      } else {
        appendContext("session.thinking.append", `New typed message to Shelley: ${text}`);
      }
      continue;
    }
    const active = submitted
      .filter((d) => !d.finished && d.userSeq !== null && row.sequence_id > d.userSeq)
      .at(-1);
    if (row.type === "agent" && row.end_of_turn) {
      appendContext("session.commentary.append", `Shelley replies: ${text}`, active?.id);
      if (active) {
        for (const task of submitted) {
          if (task.userSeq !== null && task.userSeq <= row.sequence_id) task.finished = true;
        }
      }
    } else if (row.type === "agent") {
      appendContext("session.thinking.append", `Shelley's progress: ${text}`, active?.id);
    } else if (row.type === "error") {
      appendContext("session.commentary.append", `Shelley reported an error: ${text}`, active?.id);
      if (active) active.finished = true;
    }
  }
}

watch(() => [props.messages, props.messages.length], syncConversation);

function onButton() {
  if (phase.value === "idle") void start();
  else stop();
}

async function start() {
  const id = props.conversationId;
  if (!id) return;
  error.value = null;
  phase.value = "connecting";
  sessionKey = crypto.randomUUID();
  lastSeenSequence = props.messages.reduce((max, m) => Math.max(max, m.sequence_id), 0);
  const abort = new AbortController();
  connecting = abort;
  let closedEarly: Error | undefined;
  try {
    const started = await LiveSession.start(
      id,
      onEvent,
      (err) => {
        if (phase.value !== "live") {
          // Died around session.started; start() may already have resolved.
          closedEarly = err ?? new Error("Live session closed");
          return;
        }
        reset();
        if (err) error.value = err.message;
      },
      abort.signal,
    );
    if (closedEarly) throw closedEarly;
    session = started;
    phase.value = "live";
    syncConversation();
  } catch (err) {
    // Stop while connecting aborts; that already reset the panel.
    if (abort.signal.aborted) return;
    reset();
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (connecting === abort) connecting = null;
  }
}

function reset() {
  session = null;
  for (const task of pending.values()) {
    if (task.settleTimer) clearTimeout(task.settleTimer);
    clearTimeout(task.deadlineTimer);
  }
  pending = new Map();
  seenDelegations = new Set();
  userFragments = [];
  voiceFragments = [];
  claimedUserCount = 0;
  submitted = [];
  sessionKey = "";
  lastSeenSequence = 0;
  sendTimes = [];
  inflightRequests = 0;
  lastEnd.user = lastEnd.assistant = 0;
  caption.user = caption.assistant = "";
  muted.value = false;
  phase.value = "idle";
}

function stop() {
  connecting?.abort();
  connecting = null;
  session?.close();
  reset();
}

function toggleMute() {
  if (!session) return;
  session.setMuted(!session.muted);
  muted.value = session.muted;
}

watch(
  () => props.conversationId,
  () => stop(),
);
function onPageHide() {
  stop();
}
window.addEventListener("pagehide", onPageHide);
onBeforeUnmount(() => {
  window.removeEventListener("pagehide", onPageHide);
  stop();
});
</script>

<style scoped>
.live-voice {
  position: relative;
}
.live-btn {
  width: 2rem;
  height: 2rem;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  background: #1f2937;
  border: 1px solid #1f2937;
  cursor: pointer;
}
.live-btn:hover:not(:disabled) {
  background: #374151;
  border-color: #374151;
}
.live-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.live-btn.active svg {
  transform: rotate(135deg);
}
.live-panel {
  position: absolute;
  top: calc(100% + 0.5rem);
  right: 0;
  z-index: 50;
  width: min(22rem, calc(100vw - 1.5rem));
  max-height: min(70vh, 32rem);
  overflow-y: auto;
  padding: 0.75rem;
  display: flex;
  flex-direction: column;
  gap: 0.625rem;
  background: var(--bg-base);
  border: 1px solid var(--border);
  border-radius: 0.75rem;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18);
  font-size: 0.875rem;
  color: var(--text-primary);
}
.live-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}
.live-status {
  font-weight: 600;
}
.live-head-actions {
  display: flex;
  gap: 0.375rem;
}
.live-action {
  padding: 0.25rem 0.625rem;
  border-radius: 0.375rem;
  border: 1px solid var(--border);
  background: var(--bg-secondary);
  color: var(--text-primary);
  font: inherit;
  cursor: pointer;
}
.live-action:hover {
  background: var(--bg-hover);
}

.live-error {
  margin: 0;
  padding: 0.5rem 0.625rem;
  border-radius: 0.5rem;
  background: var(--error-bg);
  border: 1px solid var(--error-border);
  color: var(--error-text);
}
.live-captions {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.live-line,
.live-hint {
  margin: 0;
  line-height: 1.4;
}
.live-hint {
  color: var(--text-secondary);
  font-size: 0.8125rem;
}
.live-who {
  display: inline-block;
  min-width: 2.5rem;
  margin-right: 0.25rem;
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--text-secondary);
  text-transform: uppercase;
}

@media (max-width: 767px) {
  .live-voice {
    flex: 0 0 auto;
  }
  .live-panel {
    position: fixed;
    top: auto;
    right: 0.75rem;
    bottom: calc(5.5rem + env(safe-area-inset-bottom, 0px));
    max-height: 60vh;
  }
}
</style>
