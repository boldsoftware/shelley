<template>
  <article class="diff-commit-message">
    <div class="diff-commit-message-toolbar">
      <div class="diff-commit-message-eyebrow">
        <span>Commit message</span>
        <code>{{ message.hash.slice(0, 8) }}</code>
        <span v-if="message.isHead" class="diff-commit-head-badge">HEAD</span>
      </div>
      <div class="diff-commit-message-actions">
        <button type="button" @click="emit('raw')">Raw</button>
        <button type="button" @mousedown.prevent @click="comment">Comment</button>
        <button v-if="message.isHead" type="button" @click="emit('edit')">Edit message</button>
      </div>
    </div>
    <div ref="contentRef" class="diff-commit-message-content">
      <h2>{{ message.subject }}</h2>
      <MarkdownContent v-if="message.body" :text="message.body" file-preview reflow />
    </div>
    <slot />
  </article>
</template>

<script setup lang="ts">
import { ref } from "vue";
import type { GitCommitMessage } from "../../types";
import MarkdownContent from "./MarkdownContent.vue";

const props = defineProps<{ message: GitCommitMessage }>();
const emit = defineEmits<{
  (e: "raw"): void;
  (e: "edit"): void;
  (e: "comment", text: string): void;
}>();
const contentRef = ref<HTMLElement | null>(null);

function comment() {
  const selection = window.getSelection();
  const content = contentRef.value;
  const selected = selection && content && selection.anchorNode && selection.focusNode &&
    content.contains(selection.anchorNode) && content.contains(selection.focusNode)
    ? selection.toString()
    : "";
  emit("comment", selected || [props.message.subject, props.message.body].filter(Boolean).join("\n\n"));
}
</script>

<style scoped>
.diff-commit-message {
  color: var(--text-primary);
  background: var(--bg-base);
  padding: 20px 24px;
}
.diff-commit-message-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px;
}
.diff-commit-message-eyebrow, .diff-commit-message-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.diff-commit-message-eyebrow {
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
}
.diff-commit-message-eyebrow code { font-size: 12px; font-weight: 400; }
.diff-commit-head-badge {
  padding: 2px 5px;
  border: 1px solid var(--border-color);
  border-radius: 3px;
  font-size: 10px;
}
.diff-commit-message-actions button {
  padding: 4px 8px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: var(--bg-base);
  color: var(--text-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.diff-commit-message-actions button:hover { background: var(--bg-tertiary); color: var(--text-primary); }
.diff-commit-message-content h2 {
  margin: 20px 0 12px;
  font-size: 18px;
  line-height: 1.35;
  font-weight: 500;
  overflow-wrap: anywhere;
}
.diff-commit-message-content :deep(.markdown-content) { font-size: 14px; line-height: 1.55; }
@media (max-width: 600px) {
  .diff-commit-message { padding: 16px; }
}
</style>
