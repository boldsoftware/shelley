<template>
  <article class="diff-commit-message">
    <div v-if="!isMobile" class="diff-commit-message-toolbar">
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
      <div v-if="isMobile" class="diff-commit-message-mobile-meta">
        <div class="diff-commit-message-eyebrow">
          <code>{{ message.hash.slice(0, 8) }}</code>
          <span v-if="message.isHead" class="diff-commit-head-badge">HEAD</span>
        </div>
        <div class="diff-commit-message-actions">
          <button type="button" @mousedown.prevent @click="comment">Comment</button>
        </div>
      </div>
      <div v-if="message.body && isMobile && !isRange" ref="bodyRef" class="diff-commit-message-body" :class="{ 'is-collapsed': !expanded, 'has-overflow': hasMore }">
        <MarkdownContent :text="message.body" file-preview reflow />
      </div>
      <MarkdownContent v-else-if="message.body" :text="message.body" file-preview reflow />
      <button v-if="isMobile && !isRange && hasMore" type="button" class="diff-commit-message-expand" :aria-expanded="expanded" @click="expanded = !expanded">
        {{ expanded ? "Show less" : "Show more" }}
      </button>
      <div v-if="isMobile && (!hasMore || expanded || isRange)" class="diff-commit-message-links">
        <a v-if="message.githubUrl" :href="message.githubUrl" target="_blank" rel="noopener noreferrer">
          <OctocatIcon :size="14" /> Open on GitHub
        </a>
        <button type="button" @click="emit('raw')">Open raw commit</button>
      </div>
      <div v-if="selectedText" class="diff-commit-selection-action">
        <button type="button" @pointerdown="selectionActionPressed = true" @mousedown.prevent @touchstart.stop @click.stop="commentSelection">Comment selection</button>
      </div>
    </div>
    <slot />
  </article>
</template>

<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import type { GitCommitMessage } from "../../types";
import MarkdownContent from "./MarkdownContent.vue";
import OctocatIcon from "./gitGraph/OctocatIcon.vue";

const props = defineProps<{ message: GitCommitMessage; isMobile?: boolean; isRange?: boolean }>();
const emit = defineEmits<{
  (e: "raw"): void;
  (e: "edit"): void;
  (e: "comment", text: string): void;
}>();
const contentRef = ref<HTMLElement | null>(null);
const bodyRef = ref<HTMLElement | null>(null);
const expanded = ref(false);
const hasMore = ref(false);
const selectedText = ref("");
let selectionActionPressed = false;
let bodyObserver: ResizeObserver | undefined;

function updateSelection() {
  if (!props.isMobile) return;
  const selection = window.getSelection();
  const content = contentRef.value;
  const inMessage = (node: Node | null) => {
    const element = node instanceof Element ? node : node?.parentElement;
    return !!element && !!content?.contains(element) && !!element.closest("h2, .markdown-content");
  };
  if (selection && inMessage(selection.anchorNode) && inMessage(selection.focusNode) && selection.toString().trim()) {
    selectedText.value = selection.toString().trim();
  } else if (!selectionActionPressed) {
    selectedText.value = "";
  }
}

function commentSelection() {
  if (selectedText.value) emit("comment", selectedText.value);
  selectedText.value = "";
  selectionActionPressed = false;
  window.getSelection()?.removeAllRanges();
}

function measureBody() {
  if (expanded.value) return;
  const body = bodyRef.value;
  hasMore.value = !!props.isMobile && !!body && body.scrollHeight > body.clientHeight + 1;
}

function observeBody() {
  bodyObserver?.disconnect();
  const body = bodyRef.value;
  if (body) {
    bodyObserver ??= new ResizeObserver(measureBody);
    bodyObserver.observe(body);
    for (const child of body.children) bodyObserver.observe(child);
  }
  measureBody();
}

onMounted(() => {
  document.addEventListener("selectionchange", updateSelection);
  void nextTick(observeBody);
});
watch(() => [props.message.hash, props.message.body, props.isMobile], () => {
  expanded.value = false;
  selectedText.value = "";
  selectionActionPressed = false;
  void nextTick(observeBody);
}, { flush: "post" });
watch(expanded, (isExpanded) => {
  if (!isExpanded) void nextTick(measureBody);
});
onUnmounted(() => {
  document.removeEventListener("selectionchange", updateSelection);
  bodyObserver?.disconnect();
});

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
.diff-commit-message-mobile-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 14px;
  color: var(--text-secondary);
  font-size: 11px;
}
.diff-commit-message-mobile-meta .diff-commit-message-eyebrow { gap: 6px; font-size: 11px; }
.diff-commit-message-mobile-meta .diff-commit-head-badge { border: 0; padding: 0; font-size: 10px; }
.diff-commit-message-mobile-meta .diff-commit-message-actions { gap: 12px; flex-wrap: wrap; }
.diff-commit-message-mobile-meta .diff-commit-message-actions button {
  min-height: 32px;
  padding: 4px 9px;
  border: 1px solid var(--diff-line);
  border-radius: 5px;
  background: var(--bg-base);
  color: var(--text-secondary);
  font: 11px/18px system-ui, sans-serif;
}
.diff-commit-message-links {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 16px;
}
.diff-commit-message-links a,
.diff-commit-message-links button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-height: 32px;
  padding: 4px 9px;
  border: 1px solid var(--diff-line);
  border-radius: 5px;
  background: var(--bg-base);
  color: var(--text-secondary);
  font: 11px/18px system-ui, sans-serif;
  text-decoration: none;
  cursor: pointer;
}
.diff-commit-selection-action {
  position: fixed;
  z-index: 52;
  right: 16px;
  bottom: calc(64px + env(safe-area-inset-bottom));
  left: 16px;
  display: flex;
  justify-content: center;
  pointer-events: none;
}
.diff-commit-selection-action button {
  min-height: 44px;
  padding: 10px 20px;
  border: 0;
  border-radius: 8px;
  background: var(--diff-accent);
  color: #fff;
  font: 600 13px/20px system-ui, sans-serif;
  box-shadow: 0 4px 16px #14243a40;
  pointer-events: auto;
}
@media (max-width: 767px) {
  .diff-commit-message-body.is-collapsed {
    max-height: max(120px, calc(100dvh - 375px));
    overflow: hidden;
  }
  .diff-commit-message-body.is-collapsed.has-overflow {
    mask-image: linear-gradient(to bottom, black 90%, transparent);
  }
  .diff-commit-message-expand {
    display: block;
    padding: 8px 0;
    border: 0;
    background: transparent;
    color: var(--diff-accent);
    font: 600 12px/20px system-ui, sans-serif;
    cursor: pointer;
  }
}
@media (max-width: 600px) {
  .diff-commit-message { padding: 16px; }
}
</style>
