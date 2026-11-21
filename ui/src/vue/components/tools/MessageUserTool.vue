<!-- message_user: the agent's side of a chat with the user. A delivered
     message renders as a chat bubble (with the message it replies to, and its
     attachments); a bare reaction as a one-line note (the reaction itself shows
     on the user's message, see Message.vue); a refused call as a tool card. -->
<template>
  <div
    v-if="isRunning"
    class="message-user-note"
    data-testid="tool-call-running"
    data-tool-name="message_user"
  >
    {{ input.attachments?.length ? "Sending attachments…" : "Sending…" }}
  </div>

  <div
    v-else-if="hasError"
    class="tool"
    data-testid="tool-call-completed"
    data-tool-name="message_user"
  >
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji">💬</span>
        <span class="tool-command">message_user failed: {{ resultText }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>
    <div v-if="isExpanded" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">Input:</div>
        <div class="tool-code">{{ JSON.stringify(toolInput, null, 2) }}</div>
      </div>
      <div class="tool-section">
        <div class="tool-label">Error:</div>
        <div class="tool-code error">{{ resultText }}</div>
      </div>
    </div>
  </div>

  <div
    v-else-if="!toolResult?.length"
    class="message-user-note"
    data-testid="tool-call-completed"
    data-tool-name="message_user"
  >
    Not sent.
  </div>

  <div
    v-else-if="!delivered"
    class="message-user-note"
    data-testid="message-user-reaction"
    data-tool-name="message_user"
  >
    Reacted {{ reaction }} to
    <button
      type="button"
      class="message-user-quote-link"
      :disabled="!targetId"
      :title="input.reply_to ? `Message #${input.reply_to}` : display.target_excerpt"
      @click="jumpToTarget"
    >
      “{{ display.target_excerpt }}”
    </button>
  </div>

  <div v-else class="message message-agent-chat" data-testid="message-user-bubble">
    <div class="message-content message-agent-chat-bubble">
      <button
        v-if="display.target_excerpt"
        type="button"
        class="message-user-quote"
        data-testid="message-user-quote"
        :title="
          input.reply_to
            ? `Reply to message #${input.reply_to}: ${display.target_excerpt}`
            : display.target_excerpt
        "
        :disabled="!targetId"
        @click="jumpToTarget"
      >
        {{ display.target_excerpt }}
      </button>
      <div
        v-if="text"
        class="message-agent-chat-text whitespace-pre-wrap"
        data-testid="message-user-text"
      >
        <InlineText :text="text" rewrite-localhost-links />
      </div>
      <div v-if="attachments.length" class="message-user-attachments">
        <template v-for="a in attachments" :key="a.path">
          <a
            v-if="isImageName(a.name) && !brokenImages.has(a.path)"
            class="message-user-image"
            :href="attachmentURL(a.path)"
            target="_blank"
            rel="noopener"
            :title="a.name"
          >
            <img
              :src="attachmentURL(a.path)"
              :alt="a.name"
              loading="lazy"
              @error="brokenImages.add(a.path)"
            />
          </a>
          <a
            v-else
            class="message-user-file"
            data-testid="message-user-file"
            :href="attachmentURL(a.path, true)"
            :download="a.name"
            :title="a.path"
          >
            <i class="pi pi-file" aria-hidden="true" />
            <span class="truncate">{{ a.name }}</span>
            <span class="message-user-file-size">{{ formatSize(a.size) }}</span>
          </a>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { LLMContent } from "../../../types";
import {
  isDeliveredUserMessage,
  messageUserDisplay,
  messageUserInput,
} from "../../../utils/conversationView";
import { messageUserContextKey } from "../messageUserContext";
import InlineText from "../InlineText.vue";
import ToolChevron from "./ToolChevron.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  display?: unknown;
  resultMessageId?: string;
}>();

const isExpanded = ref(false);
const brokenImages = reactive(new Set<string>());

const input = computed(() => messageUserInput(props.toolInput));
const display = computed(() => messageUserDisplay(props.display));
const text = computed(() => input.value.text?.trim() || "");
const reaction = computed(() => input.value.reaction || "");
const attachments = computed(() => display.value.attachments || []);
const delivered = computed(() =>
  isDeliveredUserMessage({
    toolName: "message_user",
    toolInput: props.toolInput,
    hasResult: true,
    toolError: props.hasError,
    display: props.display,
  }),
);
const resultText = computed(() =>
  (props.toolResult || [])
    .map((r) => r.Text || "")
    .join("\n")
    .trim(),
);

function attachmentURL(path: string, download = false): string {
  const q = new URLSearchParams({ path });
  if (download) q.set("download", "1");
  return `/api/message/${encodeURIComponent(props.resultMessageId || "")}/attachment?${q}`;
}

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|avif|bmp)$/i;
function isImageName(name: string): boolean {
  return IMAGE_EXT.test(name);
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

const context = inject(messageUserContextKey, null);
// The rendered message this replies or reacts to, if it is in this
// conversation.
const targetId = computed(() => context?.resolveTarget.value(display.value));

function jumpToTarget() {
  if (targetId.value) context?.jumpTo(targetId.value);
}
</script>
