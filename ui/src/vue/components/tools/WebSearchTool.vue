<!-- Vue port of components/WebSearchTool.tsx (incl. inlined WebSearchResultItem).
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji 🔍, .tool-command,
     .web-search-query, .tool-success, .tool-toggle, .web-search-results,
     .web-search-result, .web-search-result-title, .web-search-result-meta,
     .web-search-result-url, .web-search-result-age,
     data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div
      class="tool-header"
      :class="{ 'tool-header--static': !hasDetails }"
      @click="toggleExpanded"
    >
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🔍</span>
        <span class="tool-command ellipsis-start" :title="hasQueries ? queryPreview : undefined">
          Web Search<span v-if="hasQueries">: </span
          ><span v-if="hasQueries" class="web-search-query-preview">{{ queryPreview }}</span>
        </span>
        <span v-if="isComplete && hasResults" class="tool-success">
          {{ resultCount }} result{{ resultCount !== 1 ? "s" : "" }}
        </span>
      </div>
      <button
        v-if="hasDetails"
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>
    <div v-if="isExpanded" class="web-search-details">
      <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
      <div v-if="hasQueries" class="web-search-queries">
        <div class="tool-label">{{ queries.length === 1 ? "Query" : "Queries" }}</div>
        <pre v-for="searchQuery in queries" :key="searchQuery" class="tool-code">{{
          searchQuery
        }}</pre>
      </div>
      <div v-if="hasResults" class="web-search-results">
        <div v-for="(result, index) in results" :key="index" class="web-search-result">
          <a
            :href="result.URL || ''"
            target="_blank"
            rel="noopener noreferrer"
            class="web-search-result-title"
          >
            {{ result.Title || "Untitled" }}
          </a>
          <div class="web-search-result-meta">
            <span class="web-search-result-url">{{ result.URL || "" }}</span>
            <span v-if="result.PageAge" class="web-search-result-age">{{ result.PageAge }}</span>
          </div>
        </div>
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
  searchResults?: LLMContent[];
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = ref(false);

// Anthropic sends {"query": "..."}; OpenAI Responses sends {"queries": [...]}
const queries = computed(() => {
  let values: string[] = [];
  const ti = props.toolInput;
  if (ti && typeof ti === "object") {
    const t = ti as { query?: string; queries?: string[] };
    if (typeof t.query === "string") values = [t.query];
    else if (Array.isArray(t.queries)) values = t.queries;
  }
  return values.filter((value) => value.trim() !== "");
});

const results = computed<LLMContent[]>(() => props.searchResults || props.toolResult || []);
// OpenAI's server-side search doesn't deliver structured results to us;
// the citations are attached to the assistant's message text instead.
// So "complete with 0 results" is normal for OpenAI — only mark running
// based on the isRunning flag.
const isComplete = computed(() => !props.isRunning);
const resultCount = computed(() => results.value.length);
const hasQueries = computed(() => queries.value.length > 0);
const queryPreview = computed(() => queries.value.join(" / "));
const hasResults = computed(() => resultCount.value > 0);
const hasDetails = computed(() => !!props.isRunning || hasQueries.value || hasResults.value);

function toggleExpanded() {
  if (hasDetails.value) isExpanded.value = !isExpanded.value;
}
</script>
