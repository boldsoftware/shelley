<!-- Visually hidden live region for VoiceOver / screen readers.
     Announces agent working/idle and stream connection faults without
     redesigning the visual status bar.

     Hidden from the accessibility tree when empty so Safari VO Shift+Tab
     does not land on a stale "status" artifact from an earlier turn. -->
<template>
  <div
    ref="rootRef"
    class="sr-only"
    id="turn-completion-summary"
    tabindex="-1"
    :aria-live="politeness"
    aria-atomic="true"
    :aria-hidden="announcement ? undefined : true"
    data-testid="status-announcer"
  >
    {{ announcement }}
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import { A11Y_ANNOUNCE_EVENT, type A11yAnnouncementDetail } from "../../services/a11yAnnouncer";
import {
  agentAnnouncement,
  errorAnnouncement,
  streamAnnouncement,
  type Announcement,
  type Politeness,
  type StreamStatus,
} from "./statusAnnouncer";

const props = withDefaults(
  defineProps<{
    agentWorking: boolean;
    streamStatus?: StreamStatus;
    error?: string | null;
    /** Tool cards that finished during the turn that just ended. */
    toolsCompleted?: number;
    /** Plain-text preview of the assistant response that just completed. */
    assistantPreview?: string;
  }>(),
  {
    streamStatus: "connected",
    error: null,
    toolsCompleted: 0,
    assistantPreview: "",
  },
);

const rootRef = ref<HTMLElement | null>(null);
const announcement = ref("");
const politeness = ref<Politeness>("polite");
let announcementVersion = 0;
let suspended = false;
let pending: Announcement | null = null;
/** Clears spoken text after VO has had a chance to read it, so reverse-tab
 *  navigation does not re-encounter turn artifacts minutes later. Kept long
 *  enough for browser-notification hash focus (#turn-completion-summary). */
const CLEAR_AFTER_MS = 8000;
let clearTimer: ReturnType<typeof setTimeout> | null = null;

function clearClearTimer() {
  if (clearTimer !== null) {
    clearTimeout(clearTimer);
    clearTimer = null;
  }
}

function scheduleClear(version: number) {
  clearClearTimer();
  clearTimer = setTimeout(() => {
    clearTimer = null;
    // Do not wipe while the user is focused on the summary (notification click).
    if (document.activeElement?.id === "turn-completion-summary") return;
    if (version === announcementVersion) announcement.value = "";
  }, CLEAR_AFTER_MS);
}

function apply(next: Announcement | null) {
  if (!next) return;
  if (suspended) {
    pending = next;
    return;
  }

  const version = ++announcementVersion;
  clearClearTimer();
  // Clear then set so repeated identical phrases still fire (VO quirk).
  announcement.value = "";
  politeness.value = next.politeness;
  if (!next.text) return;

  // Microtask is enough for the clear-then-set re-fire; no delay needed there.
  queueMicrotask(() => {
    if (version === announcementVersion) {
      announcement.value = next.text;
      scheduleClear(version);
    }
  });
}

watch(
  () => props.agentWorking,
  (working, wasWorking) => {
    apply(agentAnnouncement(working, wasWorking, props.toolsCompleted, props.assistantPreview));
  },
);

watch(
  () => props.streamStatus,
  (status, prev) => {
    apply(streamAnnouncement(status, prev));
  },
);

watch(
  () => props.error,
  (err, prev) => {
    apply(errorAnnouncement(err, prev));
  },
);

function onA11yAnnouncement(event: Event) {
  const detail = (event as CustomEvent<A11yAnnouncementDetail>).detail;
  if (detail?.text) apply(detail);
}

function onFocusIn(event: FocusEvent) {
  const target = event.target as HTMLElement | null;
  if (target?.closest("[data-a11y-transcript]")) suspended = true;
}

function onFocusOut() {
  queueMicrotask(() => {
    const active = document.activeElement as HTMLElement | null;
    if (active?.closest("[data-a11y-transcript]")) return;
    if (!suspended) return;
    suspended = false;
    if (pending) {
      const catchUp = pending;
      pending = null;
      apply({ ...catchUp, text: `While you were reviewing: ${catchUp.text}` });
    }
  });
}

function onSummaryBlur() {
  // After notification hash-focus, clear once the user tabs away.
  if (!announcement.value) return;
  const version = announcementVersion;
  queueMicrotask(() => {
    if (
      version === announcementVersion &&
      document.activeElement?.id !== "turn-completion-summary"
    ) {
      clearClearTimer();
      announcement.value = "";
    }
  });
}

onMounted(() => {
  window.addEventListener(A11Y_ANNOUNCE_EVENT, onA11yAnnouncement);
  document.addEventListener("focusin", onFocusIn);
  document.addEventListener("focusout", onFocusOut);
  rootRef.value?.addEventListener("blur", onSummaryBlur);
});
onUnmounted(() => {
  clearClearTimer();
  window.removeEventListener(A11Y_ANNOUNCE_EVENT, onA11yAnnouncement);
  document.removeEventListener("focusin", onFocusIn);
  document.removeEventListener("focusout", onFocusOut);
  rootRef.value?.removeEventListener("blur", onSummaryBlur);
});
</script>
