<!-- Vue port of components/ChangeDirTool.tsx.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji, .tool-command,
     .tool-toggle, .tool-details, .tool-section, .tool-label, .tool-code,
     .tool-error, .tool-success, data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📂</span>
        <span class="tool-command">cd {{ path || "..." }}</span>
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
        <div class="tool-label">
          Path:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <div :class="`tool-code ${hasError ? 'error' : ''}`">{{ path || "(no path)" }}</div>
      </div>
      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">Result:</div>
        <div :class="`tool-code ${hasError ? 'error' : ''}`">{{ resultText || "(no output)" }}</div>
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

const path = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "path" in ti &&
    typeof (ti as { path: unknown }).path === "string"
  ) {
    return (ti as { path: string }).path;
  }
  return "";
});

const resultText = computed(
  () =>
    props.toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
