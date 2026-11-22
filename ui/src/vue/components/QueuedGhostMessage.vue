<!-- QueuedGhostMessage renders a queued user message at the bottom of the
     conversation as an ordinary message, marked only by a small clock that
     opens its actions (send now, cancel). Derived from
     conversation.queued_messages (NOT a messages row). -->
<template>
  <div
    ref="rootRef"
    :class="[
      'message',
      jobNotice
        ? 'message-tool message-tool-card'
        : source
          ? 'message-tool message-conversation'
          : 'message-user',
      'message-queued',
    ]"
    data-testid="queued-ghost"
  >
    <div class="message-content" data-testid="message-content">
      <BackgroundJobNoticeCard v-if="jobNotice" :source="jobNotice" :text="text" />
      <template v-else>
        <ConversationMessageAuthor v-if="source" :source="source" />
        <div class="whitespace-pre-wrap break-words">{{ text }}</div>
      </template>
    </div>
    <!-- Like the composer's queue menu: inline, so it scrolls with the
         message and its listeners go with it. -->
    <div
      ref="anchorRef"
      class="queued-clock-anchor"
      @keydown.escape="closeMenu"
      @focusout="onFocusOut"
    >
      <button
        ref="clockRef"
        type="button"
        class="queued-clock"
        :data-testid="queued.inject ? 'injected-badge' : 'queued-badge'"
        :aria-label="status"
        :aria-expanded="menuOpen"
        :aria-controls="menuOpen ? menuId : undefined"
        @click="menuOpen = !menuOpen"
      >
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <circle cx="12" cy="12" r="9" />
          <polyline points="12 7 12 12 15 14" />
        </svg>
      </button>
      <div
        v-if="menuOpen"
        :id="menuId"
        ref="menuRef"
        :style="{ left: `${menuLeft}px` }"
        :class="['queue-menu', 'queued-menu', { 'queued-menu-below': menuBelow }]"
      >
        <div class="queued-menu-status">{{ status }}</div>
        <button
          v-if="onSendNow"
          type="button"
          class="queue-menu-item"
          :disabled="sendNowPending"
          data-testid="send-queued-now"
          @click="act(() => onSendNow!(queued.id))"
        >
          Interrupt and send now
        </button>
        <button
          v-if="onCancel"
          type="button"
          class="queue-menu-item"
          data-testid="cancel-queued"
          @click="act(() => onCancel!(queued.id))"
        >
          Cancel
        </button>
        <button
          v-if="onCancelAll"
          type="button"
          class="queue-menu-item"
          data-testid="cancel-all-queued"
          @click="act(onCancelAll)"
        >
          Cancel all queued
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from "vue";
import { type QueuedMessage, queuedMessageText } from "../../types";
import { messageSource } from "../../utils/messageSource";
import BackgroundJobNoticeCard from "./BackgroundJobNoticeCard.vue";
import ConversationMessageAuthor from "./ConversationMessageAuthor.vue";

const props = defineProps<{
  queued: QueuedMessage;
  onSendNow?: (id: string) => void;
  sendNowPending?: boolean;
  onCancel?: (id: string) => void;
  onCancelAll?: () => void;
}>();

const rootRef = ref<HTMLDivElement>();
const anchorRef = ref<HTMLDivElement>();
const clockRef = ref<HTMLButtonElement>();
const menuRef = ref<HTMLDivElement>();
const menuOpen = ref(false);
const menuId = useId();
// The menu opens above the clock, or below it where the chat's scroll
// viewport has no room above. It starts at the clock, moved left as far as
// it must to stay within the message's row.
const menuBelow = ref(false);
const menuLeft = ref(0);

const text = computed(() => queuedMessageText(props.queued));
const source = computed(() => messageSource(props.queued.user_data));
const jobNotice = computed(() =>
  source.value && "backgroundJobId" in source.value ? source.value : null,
);
// A running turn takes injected messages and ready transcriptions after its
// current tool call; the rest wait for the turn to end.
const status = computed(() =>
  props.queued.inject || props.queued.kind === "transcription"
    ? "Sends after the running tool"
    : "Sends when the agent finishes",
);

function onOutside(e: MouseEvent) {
  if (!anchorRef.value?.contains(e.target as Node)) menuOpen.value = false;
}

// Tabbing away closes it. Focus that goes nowhere (Safari doesn't focus a
// clicked button) is left to onOutside.
function onFocusOut(e: FocusEvent) {
  const to = e.relatedTarget as Node | null;
  if (to && !anchorRef.value?.contains(to)) menuOpen.value = false;
}

watch(menuOpen, async (open) => {
  if (!open) {
    document.removeEventListener("mousedown", onOutside);
    return;
  }
  document.addEventListener("mousedown", onOutside);
  menuLeft.value = 0;
  await nextTick();
  const menu = menuRef.value;
  const anchor = anchorRef.value;
  const row = rootRef.value;
  if (!menu || !anchor || !row) return; // closed or gone meanwhile
  const clock = anchor.getBoundingClientRect();
  const viewTop = row.closest(".messages-container")?.getBoundingClientRect().top ?? 0;
  menuBelow.value = clock.top - menu.offsetHeight < viewTop;
  menuLeft.value = Math.min(0, row.getBoundingClientRect().right - clock.left - menu.offsetWidth);
  await nextTick();
  menu.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus({ preventScroll: true });
  menu.scrollIntoView({ block: "nearest" });
});

onBeforeUnmount(() => document.removeEventListener("mousedown", onOutside));

function closeMenu() {
  if (!menuOpen.value) return;
  menuOpen.value = false;
  clockRef.value?.focus();
}

function act(run: () => void) {
  closeMenu();
  run();
}
</script>
