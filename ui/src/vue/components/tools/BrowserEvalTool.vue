<!-- Vue port of components/BrowserEvalTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">⚡</span>
        <span class="tool-command" :title="expression">{{ displayExpression }}</span>
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="tool-error" />
        <ToolStatusIcon v-if="isComplete && !hasError" state="ok" class="tool-success" />
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
      <div class="tool-section">
        <div class="tool-label">Expression:</div>
        <pre class="tool-code">{{ expression }}</pre>
      </div>

      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Result{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <pre :class="`tool-code ${hasError ? 'error' : ''}`">{{ result || "(no result)" }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const expression = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "expression" in ti &&
    typeof (ti as { expression?: unknown }).expression === "string"
  ) {
    return (ti as { expression: string }).expression;
  }
  return typeof ti === "string" ? ti : "";
});

const result = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const displayExpression = computed(() => {
  const text = expression.value;
  const maxLen = 300;
  return text.length <= maxLen ? text : text.substring(0, maxLen) + "...";
});

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
