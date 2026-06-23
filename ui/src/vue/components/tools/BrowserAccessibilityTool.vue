<!-- Vue port of components/BrowserAccessibilityTool.tsx. Preserves the exact
     DOM classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">♿</span>
        <span class="tool-command">{{ summary }}</span>
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
        <div class="tool-label">Action:</div>
        <pre class="tool-code">{{ action || "(none)" }}</pre>
      </div>

      <div v-if="input.name" class="tool-section">
        <div class="tool-label">Name:</div>
        <pre class="tool-code">{{ input.name }}</pre>
      </div>

      <div v-if="input.role" class="tool-section">
        <div class="tool-label">Role:</div>
        <pre class="tool-code">{{ input.role }}</pre>
      </div>

      <div v-if="input.selector" class="tool-section">
        <div class="tool-label">Selector:</div>
        <pre class="tool-code">{{ input.selector }}</pre>
      </div>

      <div v-if="input.depth !== undefined" class="tool-section">
        <div class="tool-label">Depth:</div>
        <pre class="tool-code">{{ input.depth }}</pre>
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

interface AccessibilityInput {
  action?: string;
  depth?: number;
  name?: string;
  role?: string;
  selector?: string;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const input = computed<AccessibilityInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as AccessibilityInput)
    : {},
);

const action = computed(() => input.value.action || "");

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

const summary = computed(() => {
  const i = input.value;
  const summaryParts: string[] = [action.value];
  if (i.name) summaryParts.push(`name="${i.name}"`);
  if (i.role) summaryParts.push(`role=${i.role}`);
  if (i.selector) summaryParts.push(i.selector);
  if (i.depth !== undefined) summaryParts.push(`depth=${i.depth}`);
  return summaryParts.filter(Boolean).join(" ") || "accessibility";
});
</script>
