<!-- Vue port of components/GenericTool.tsx. Fallback tool renderer.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji, .tool-command,
     .tool-toggle, .tool-details, .tool-section, .tool-label, .tool-code,
     .tool-error, .tool-success, data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">⚙️</span>
        <span class="tool-command">{{ toolName }}</span>
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
      <div v-if="toolInput !== undefined" class="tool-section">
        <div class="tool-label">Input:</div>
        <pre class="tool-code">{{ formatData(toolInput) }}</pre>
      </div>

      <div v-if="isRunning" class="tool-section">
        <div class="tool-label">Status:</div>
        <div class="tool-running-text">running...</div>
      </div>

      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Output{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <pre :class="`tool-code ${hasError ? 'error' : ''}`">{{ output || "(no output)" }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";

const props = defineProps<{
  toolName: string;
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const formatData = (data: unknown): string => {
  if (data === undefined || data === null) return "";
  if (typeof data === "string") return data;
  try {
    return JSON.stringify(data, null, 2);
  } catch {
    return String(data);
  }
};

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0
    ? props.toolResult.map((result) => result.Text || formatData(result)).join("\n")
    : "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
