<!-- Vue port of components/ThinkingContent.tsx. Collapsible chain-of-thought,
     default expanded. Preserves: .thinking-content, .thinking-content-wrapper,
     data-testid thinking-content, .thinking-clickable-area, .thinking-emoji 💭,
     .thinking-text, .thinking-toggle, .thinking-toggle-button. -->
<template>
  <div class="thinking-content thinking-content-wrapper" data-testid="thinking-content">
    <div class="thinking-clickable-area" @click="isExpanded = !isExpanded">
      <span class="thinking-emoji">💭</span>
      <div class="thinking-text">{{ isExpanded ? thinking : preview }}</div>
      <button
        class="thinking-toggle thinking-toggle-button"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <svg
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
          xmlns="http://www.w3.org/2000/svg"
          class="tool-chevron"
          :class="{ 'tool-chevron-expanded': isExpanded }"
        >
          <path
            d="M4.5 3L7.5 6L4.5 9"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";

const props = defineProps<{ thinking: string }>();

const isExpanded = ref(true);

// Truncate thinking for display - get first 80 chars
const truncateThinking = (text: string, maxLen = 80) => {
  if (!text) return "";
  const firstLine = text.split("\n")[0];
  if (firstLine.length <= maxLen) return firstLine;
  return firstLine.substring(0, maxLen) + "...";
};

const preview = computed(() => truncateThinking(props.thinking));
</script>
