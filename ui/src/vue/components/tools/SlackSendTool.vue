<!-- send_to_thread / send_to_channel / add_reaction: the Slack Assistant
     posting to Slack. A post renders as its chat bubble, headed by where it
     went, with its text (or, with blocks, a preview of them: the text is then
     only the notification fallback) and files; a reaction as a one-line note.
     A post or reaction Slack refused says why. The header (or note) expands
     into the call's input and result. -->
<template>
  <div
    v-if="toolName === 'add_reaction'"
    class="slack-reaction"
    :data-testid="isRunning ? 'tool-call-running' : 'slack-reaction'"
    :data-tool-name="toolName"
  >
    <button
      type="button"
      :class="['message-user-note', 'slack-reaction-note', { 'slack-failed': hasError }]"
      :aria-expanded="isExpanded"
      @click="isExpanded = !isExpanded"
    >
      <SlackMark />
      <span class="slack-reaction-text">
        {{ hasError ? "Couldn't react" : isRunning ? "Reacting" : "Reacted" }}
        {{ slackEmoji(input.emoji) }}
        to
        <span :title="target?.text">{{ targetLabel }}</span>
        <template v-if="hasError">: {{ resultText }}</template>
      </span>
      <ToolChevron :expanded="isExpanded" />
    </button>
    <SlackCallDetails
      v-if="isExpanded"
      :tool-input="toolInput"
      :result-text="resultText"
      :has-error="hasError"
      :execution-time="executionTime"
      :is-running="isRunning"
      :tool-invoked-at="toolInvokedAt"
    />
  </div>

  <div
    v-else
    class="message message-agent-chat"
    :data-testid="isRunning ? 'tool-call-running' : 'slack-reply'"
    :data-tool-name="toolName"
  >
    <div
      :class="[
        'message-content',
        'message-agent-chat-bubble',
        'slack-reply',
        { 'slack-failed': hasError },
      ]"
    >
      <button
        type="button"
        class="slack-reply-header"
        :aria-expanded="isExpanded"
        :title="threadTitle"
        @click="isExpanded = !isExpanded"
      >
        <SlackMark />
        <span class="truncate">{{ where }}</span>
        <span v-if="isRunning">· sending…</span>
        <ToolChevron :expanded="isExpanded" />
      </button>
      <SlackBlocks v-if="hasBlocks" :blocks="input.blocks" />
      <SlackText v-else-if="input.text" :text="input.text" />
      <SlackFiles v-if="input.files.length" :paths="input.files" />
      <div v-if="hasError" class="slack-reply-error" data-testid="slack-reply-error">
        Not sent: {{ resultText }}
      </div>
      <SlackCallDetails
        v-if="isExpanded"
        :tool-input="toolInput"
        :result-text="resultText"
        :has-error="hasError"
        :execution-time="executionTime"
        :is-running="isRunning"
        :tool-invoked-at="toolInvokedAt"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";
import { slackKey, slackPlace } from "../../../utils/slack";
import { excerpt, mrkdwnText, slackEmoji } from "../../../utils/slackMrkdwn";
import { useSlackDirectory } from "../slackDirectory";
import SlackBlocks from "../SlackBlocks.vue";
import SlackFiles from "../SlackFiles.vue";
import SlackMark from "../SlackMark.vue";
import SlackText from "../SlackText.vue";
import SlackCallDetails from "./SlackCallDetails.vue";
import ToolChevron from "./ToolChevron.vue";

const props = defineProps<{
  toolName: "send_to_thread" | "send_to_channel" | "add_reaction";
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}>();

const isExpanded = ref(false);
const directory = useSlackDirectory();

const input = computed(() => {
  const ti = (
    typeof props.toolInput === "object" && props.toolInput !== null ? props.toolInput : {}
  ) as Record<string, unknown>;
  const field = (k: string) => (typeof ti[k] === "string" ? (ti[k] as string) : "");
  return {
    channel: field("channel"),
    threadTS: field("thread_ts"),
    ts: field("ts"),
    emoji: field("emoji"),
    text: field("text"),
    blocks: ti.blocks,
    files: Array.isArray(ti.files)
      ? ti.files.filter((f): f is string => typeof f === "string")
      : [],
  };
});

const hasBlocks = computed(
  () => Array.isArray(input.value.blocks) && input.value.blocks.length > 0,
);

const place = computed(() => slackPlace(input.value.channel, directory.value) || "Slack");
const where = computed(() =>
  props.toolName === "send_to_thread" ? `${place.value} · thread` : place.value,
);

function summary(channel: string, ts: string): string | undefined {
  const event = directory.value.messages.get(slackKey(channel, ts));
  if (!event) return undefined;
  const who = event.userName || directory.value.users.get(event.user) || event.user;
  return `${who}: ${mrkdwnText(event.text, directory.value)}`;
}

const threadTitle = computed(() => {
  const { channel, threadTS } = input.value;
  if (props.toolName !== "send_to_thread") return undefined;
  return `The thread of ${excerpt(summary(channel, threadTS) ?? threadTS, 120)}`;
});

const target = computed(() => {
  const { channel, ts } = input.value;
  const event = directory.value.messages.get(slackKey(channel, ts));
  return (
    event && { text: summary(channel, ts) ?? "", excerpt: mrkdwnText(event.text, directory.value) }
  );
});
const targetLabel = computed(() =>
  target.value?.excerpt ? `“${excerpt(target.value.excerpt, 60)}”` : `a message in ${place.value}`,
);

const resultText = computed(
  () =>
    props.toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || "",
);
</script>

<style scoped>
.slack-reply-header {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  min-width: 0;
  padding: 0;
  border: 0;
  background: none;
  color: var(--text-secondary);
  font: inherit;
  font-size: var(--font-size-12);
  text-align: left;
  cursor: pointer;
}
.slack-reply-header:hover {
  color: var(--text-primary);
}
.slack-reply.slack-failed {
  border: 1px dashed var(--error-border);
}
.slack-reply-error {
  color: var(--error-text);
  font-size: var(--font-size-13);
}
.slack-reaction-note {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  max-width: 100%;
  border: 0;
  background: none;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}
.slack-reaction-note:hover {
  color: var(--text-primary);
}
.slack-reaction-note.slack-failed {
  color: var(--error-text);
}
.slack-reaction-text {
  min-width: 0;
  overflow-wrap: anywhere;
}
.slack-reaction > :deep(.slack-call-details) {
  margin: 0 1rem 0.5rem;
}
</style>
