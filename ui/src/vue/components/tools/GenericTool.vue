<!-- Vue port of components/GenericTool.tsx. Fallback tool renderer.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji, .tool-command,
     .tool-toggle, .tool-details, .tool-section, .tool-label, .tool-code,
     data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div :class="['tool-summary', { 'generic-tool-summary--complete': isComplete }]">
        <span class="tool-emoji" :class="{ running: isRunning }">⚙️</span>
        <span class="generic-tool-copy">
          <span class="tool-command ellipsis-start">{{ toolName }}</span>
          <GenericToolWarning v-if="isComplete" :tool-name="toolName" />
        </span>
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
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";
import GenericToolWarning from "./GenericToolWarning.vue";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";

const props = defineProps<{
  toolName: string;
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = ref(false);

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
