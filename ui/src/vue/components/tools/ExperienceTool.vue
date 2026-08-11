<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">{{ emoji }}</span>
        <span class="tool-command">{{ title }}</span>
        <span v-if="isComplete && hasError" class="tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="tool-success">✓</span>
      </div>
      <button class="tool-toggle" :aria-label="isExpanded ? 'Collapse' : 'Expand'" :aria-expanded="isExpanded">
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
          <path d="M4.5 3L7.5 6L4.5 9" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
    </div>
    <div v-if="isExpanded" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">Input:<span v-if="executionTime" class="tool-time">{{ executionTime }}</span></div>
        <pre class="tool-code">{{ formattedInput }}</pre>
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
  toolName?: string;
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();
const isExpanded = useToolExpanded();
const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
const input = computed(() => (typeof props.toolInput === "object" && props.toolInput !== null ? props.toolInput as Record<string, unknown> : {}));
const action = computed(() => typeof input.value.action === "string" ? input.value.action : "");
const emoji = computed(() => props.toolName === "memory" ? "🧠" : props.toolName === "dream" ? "💭" : "📋");
const title = computed(() => {
  if (props.toolName === "memory") return action.value === "save" ? "Save memory" : "Search memory";
  if (props.toolName === "dream") return "Consolidate experience";
  return action.value === "read" ? "Restore checkpoint" : "Save checkpoint";
});
const formattedInput = computed(() => JSON.stringify(props.toolInput ?? {}, null, 2));
const resultText = computed(() => props.toolResult?.map((r) => r.Text).filter(Boolean).join("") || "");
</script>
