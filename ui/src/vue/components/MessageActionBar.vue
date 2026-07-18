<!-- Vue port of components/MessageActionBar.tsx. Floating copy/fork/details
     bar shown on message hover/tap. Preserves .message-action-bar,
     .message-action-bar-wrapper, the data-action-bar marker, and the
     .message-action-button(-success) classes and titles. -->
<template>
  <div class="message-action-bar message-action-bar-wrapper" data-action-bar>
    <button
      v-if="onCopy"
      aria-label="Copy"
      data-tooltip="Copy"
      :class="`message-action-button${copyFeedback ? ' message-action-button-success' : ''}`"
      @click="handleCopy"
    >
      <svg
        v-if="copyFeedback"
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <polyline points="20 6 9 17 4 12"></polyline>
      </svg>
      <svg
        v-else
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
        <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
      </svg>
    </button>
    <button
      v-if="onFork"
      aria-label="Fork conversation from here"
      data-tooltip="Fork conversation from here"
      class="message-action-button"
      @click="handleFork"
    >
      <svg
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <path d="M12 21 L12 13"></path>
        <path d="M12 13 C 12 10, 9 9, 5 5"></path>
        <path d="M12 13 C 12 10, 15 9, 19 5"></path>
        <polyline points="5 9 5 5 9 5"></polyline>
        <polyline points="19 9 19 5 15 5"></polyline>
      </svg>
    </button>
    <button
      v-if="onShowUsage"
      aria-label="Details"
      data-tooltip="Details"
      class="message-action-button"
      @click="handleShowUsage"
    >
      <svg
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <circle cx="12" cy="12" r="10"></circle>
        <line x1="12" y1="16" x2="12" y2="12"></line>
        <line x1="12" y1="8" x2="12.01" y2="8"></line>
      </svg>
    </button>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";

const props = defineProps<{
  onCopy?: () => void;
  onShowUsage?: () => void;
  onFork?: () => void;
}>();

const copyFeedback = ref(false);

function handleCopy(e: MouseEvent) {
  e.stopPropagation();
  if (props.onCopy) {
    props.onCopy();
    copyFeedback.value = true;
    setTimeout(() => (copyFeedback.value = false), 1500);
  }
}

function handleShowUsage(e: MouseEvent) {
  e.stopPropagation();
  props.onShowUsage?.();
}

function handleFork(e: MouseEvent) {
  e.stopPropagation();
  props.onFork?.();
}
</script>
