<!-- Vue port of components/WebSearchTool.tsx (incl. inlined WebSearchResultItem).
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji 🔍, .tool-command,
     .web-search-query, .tool-success, .tool-toggle, .web-search-results,
     .web-search-result, .web-search-result-title, .web-search-result-meta,
     .web-search-result-url, .web-search-result-age,
     data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🔍</span>
        <span class="tool-command"
          >Web Search{{ query ? ": " : ""
          }}<span v-if="query" class="web-search-query">{{ query }}</span></span
        >
        <span v-if="isComplete && showCount" class="tool-success">
          {{ resultCount }} result{{ resultCount !== 1 ? "s" : "" }}
        </span>
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
          :style="{
            transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)',
            transition: 'transform 0.2s',
          }"
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
    <div v-if="isExpanded && results.length > 0" class="web-search-results">
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
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  searchResults?: LLMContent[];
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

// Anthropic sends {"query": "..."}; OpenAI Responses sends {"queries": [...]}
const query = computed(() => {
  let queries: string[] = [];
  const ti = props.toolInput;
  if (ti && typeof ti === "object") {
    const t = ti as { query?: string; queries?: string[] };
    if (typeof t.query === "string") queries = [t.query];
    else if (Array.isArray(t.queries)) queries = t.queries;
  }
  return queries.join(" / ");
});

const results = computed<LLMContent[]>(() => props.searchResults || props.toolResult || []);
// OpenAI's server-side search doesn't deliver structured results to us;
// the citations are attached to the assistant's message text instead.
// So "complete with 0 results" is normal for OpenAI — only mark running
// based on the isRunning flag.
const isComplete = computed(() => !props.isRunning);
const resultCount = computed(() => results.value.length);
const showCount = computed(() => resultCount.value > 0);
</script>
