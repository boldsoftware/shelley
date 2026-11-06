<!-- Slack mrkdwn, rendered by utils/slackMrkdwn.ts, which escapes all of its
     text, with mentions named from the conversation's Slack events; or Block
     Kit's plain text, with emoji. -->
<template>
  <div v-if="plain" class="slack-text">{{ emojify(text) }}</div>
  <div v-else class="slack-text" v-html="html" />
</template>

<script setup lang="ts">
import { computed } from "vue";
import { emojify, renderMrkdwn } from "../../utils/slackMrkdwn";
import { useSlackDirectory } from "./slackDirectory";

const props = defineProps<{ text: string; plain?: boolean }>();
const directory = useSlackDirectory();
const html = computed(() => renderMrkdwn(props.text, directory.value));
</script>

<style scoped>
.slack-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.slack-text :deep(blockquote) {
  margin: 0.25rem 0;
  padding-left: 0.625rem;
  border-left: 3px solid var(--border);
  color: var(--text-secondary);
}
.slack-text :deep(.inline-code-block) {
  white-space: pre-wrap;
}
.slack-text :deep(.slack-mention) {
  padding: 0 0.125rem;
  border-radius: 0.25rem;
  background: var(--blue-bg);
  color: var(--blue-text);
  font-weight: 500;
}
</style>
