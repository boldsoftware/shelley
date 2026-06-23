<!-- Vue port of components/BrowserNavigateTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🌐</span>
        <span class="tool-command" :title="url">{{ displayUrl }}</span>
        <span v-if="isComplete && hasError" class="tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="tool-success">✓</span>
      </div>
      <button
        class="tool-toggle"
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

    <div v-if="isExpanded" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">URL:</div>
        <div class="tool-code">
          <a :href="url" target="_blank" rel="noopener noreferrer">{{ url }}</a>
        </div>
      </div>

      <div v-if="isComplete && output" class="tool-section">
        <div class="tool-label">
          Output{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <pre :class="`tool-code ${hasError ? 'error' : ''}`">{{ output }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const url = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "url" in ti &&
    typeof (ti as { url: unknown }).url === "string"
  ) {
    return (ti as { url: string }).url;
  }
  return typeof ti === "string" ? ti : "";
});

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const displayUrl = computed(() => {
  const u = url.value;
  const maxLen = 300;
  return u.length <= maxLen ? u : u.substring(0, maxLen) + "...";
});

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
