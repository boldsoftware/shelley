<!-- Vue port of components/BrowserResizeTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📐</span>
        <span class="tool-command ellipsis-start">resize {{ displaySize }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <div v-if="isExpanded" class="tool-details">
      <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
      <div class="tool-section">
        <div class="tool-label">Dimensions:</div>
        <div class="tool-code">{{ width }} × {{ height }} pixels</div>
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
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = ref(false);

const width = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "width" in ti &&
    typeof (ti as { width: unknown }).width === "number"
  ) {
    return (ti as { width: number }).width;
  }
  return 0;
});

const height = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "height" in ti &&
    typeof (ti as { height: unknown }).height === "number"
  ) {
    return (ti as { height: number }).height;
  }
  return 0;
});

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
const displaySize = computed(() =>
  width.value > 0 && height.value > 0 ? `${width.value}×${height.value}` : "...",
);
</script>
