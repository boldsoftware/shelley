<!-- Retained for historical conversations; keyword_search is no longer an executable tool. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header keyword-search-tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🔍</span>
        <span class="tool-command ellipsis-start" :title="fullText">{{ displayText }}</span>
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
  if (typeof ti !== "object" || ti === null || !("search_terms" in ti)) return [];
  const terms = ti.search_terms;
  // The retired tool also accepted a bare string as one term, not a comma-separated list.
  if (typeof terms === "string") return [terms];
  return Array.isArray(terms) ? terms : [];
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
