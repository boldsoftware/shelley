<!-- Vue port of components/ThinkingContent.tsx. Collapsible chain-of-thought,
     default collapsed. Preserves: .thinking-content, .thinking-content-wrapper,
     data-testid thinking-content, .thinking-clickable-area, .thinking-emoji 💭,
     .thinking-text, .thinking-toggle, .thinking-toggle-button. -->
<template>
  <div class="thinking-content thinking-content-wrapper" data-testid="thinking-content">
    <div class="thinking-clickable-area" @click="isExpanded = !isExpanded">
      <span class="thinking-emoji">💭</span>
      <div class="thinking-text" :class="{ 'thinking-text-collapsed': !isExpanded }">
        {{ isExpanded ? thinking : preview }}
      </div>
      <button
        class="thinking-toggle thinking-toggle-button"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import ToolChevron from "./ToolChevron.vue";

const props = defineProps<{ thinking: string }>();

const isExpanded = ref(false);

// Collapsed preview: first line only, capped to keep the DOM light.
// Visual truncation (ellipsis at the edge of the line) is done in CSS
// via .thinking-text-collapsed.
const preview = computed(() => {
  if (!props.thinking) return "";
  const firstLine = props.thinking.split("\n", 1)[0];
  return firstLine.length > 500 ? firstLine.substring(0, 500) : firstLine;
});
</script>
