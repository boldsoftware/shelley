<!-- Vue port of components/KeywordSearchTool.tsx.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji 🔍, .tool-command,
     .tool-toggle, .tool-details, .tool-section, .tool-label, .tool-code,
     .tool-time, .tool-error, .tool-success, data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🔍</span>
        <span class="tool-command" :title="fullText">{{ displayText }}</span>
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
      <div v-if="query" class="tool-section">
        <div class="tool-label">Query:</div>
        <pre class="tool-code">{{ query }}</pre>
      </div>

      <div v-if="searchTerms.length > 0" class="tool-section">
        <div class="tool-label">Search Terms:</div>
        <pre class="tool-code">{{ searchTerms.join(", ") }}</pre>
      </div>

      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Results{{ hasError ? " (Error)" : "" }}:
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
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const query = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "query" in ti &&
    typeof (ti as { query: unknown }).query === "string"
  ) {
    return (ti as { query: string }).query;
  }
  return "";
});

const searchTerms = computed<string[]>(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "search_terms" in ti &&
    Array.isArray((ti as { search_terms: unknown }).search_terms)
  ) {
    return (ti as { search_terms: string[] }).search_terms;
  }
  return [];
});

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const truncateSearchTerms = (terms: string[], maxLen = 300) => {
  const joined = terms.join(", ");
  if (joined.length <= maxLen) return joined;
  return joined.substring(0, maxLen) + "...";
};

const fullText = computed(() => query.value || searchTerms.value.join(", "));
const displayText = computed(() => query.value || truncateSearchTerms(searchTerms.value));
const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
