<!-- QueuedGhostMessage renders a queued user message as a ghost/pending item at
     the bottom of the conversation. Derived from conversation.queued_messages
     (NOT a messages row); offers a per-message cancel affordance. -->
<template>
  <div
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
      <div
        class="queued-message-badge"
        :data-testid="queued.inject ? 'injected-badge' : 'queued-badge'"
      >
        <span class="queued-message-badge-label">
          <svg
            fill="currentColor"
            viewBox="0 0 24 24"
            width="14"
            height="14"
            style="margin-right: 4px; vertical-align: middle"
          >
            <path
              d="M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z"
            />
          </svg>
          {{ queued.inject ? "Next, after the running tool" : "Queued" }}
        </span>
        <button
          v-if="onSendNow"
          class="queued-message-badge-action queued-message-badge-send"
          :disabled="sendNowPending"
          data-testid="send-queued-now"
          v-tooltip.top="'Interrupt the current response and send this now'"
          @click.stop="onSendNow(queued.id)"
        >
          Send now
        </button>
        <button
          v-if="onCancel"
          class="queued-message-badge-action"
          data-testid="cancel-queued"
          v-tooltip.top="'Cancel queued message'"
          @click.stop="onCancel(queued.id)"
        >
          Cancel
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { type QueuedMessage, queuedMessageText } from "../../types";
import { messageSource } from "../../utils/messageSource";
import BackgroundJobNoticeCard from "./BackgroundJobNoticeCard.vue";
import ConversationMessageAuthor from "./ConversationMessageAuthor.vue";

const props = defineProps<{
  queued: QueuedMessage;
  onSendNow?: (id: string) => void;
  sendNowPending?: boolean;
  onCancel?: (id: string) => void;
}>();

const text = computed(() => queuedMessageText(props.queued));
const source = computed(() => messageSource(props.queued.user_data));
const jobNotice = computed(() =>
  source.value && "backgroundJobId" in source.value ? source.value : null,
);
</script>
