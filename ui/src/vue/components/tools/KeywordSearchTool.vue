<!-- Retained for historical conversations; keyword_search is no longer an executable tool. -->
<template>
  <ToolCard emoji="🔍" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">{{ displayText }}</span>
    </template>
    <ToolSection v-if="query" label="Query" :text="query" />
    <ToolSection v-if="searchTerms.length > 0" label="Search Terms" :text="searchTerms.join(', ')" />
    <ToolSection
      v-if="complete"
      label="Results"
      :text="output || '(no output)'"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, inputFields, inputString, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const query = computed(() => inputString(props.toolInput, "query"));

const searchTerms = computed<string[]>(() => {
  const terms = inputFields(props.toolInput).search_terms;
  // The retired tool also accepted a bare string as one term, not a comma-separated list.
  if (typeof terms === "string") return [terms];
  return Array.isArray(terms) ? terms : [];
});

const output = computed(() => firstResultText(props.toolResult));

const truncateSearchTerms = (terms: string[], maxLen = 300) => {
  const joined = terms.join(", ");
  if (joined.length <= maxLen) return joined;
  return joined.substring(0, maxLen) + "...";
};

const displayText = computed(() => query.value || truncateSearchTerms(searchTerms.value));
const complete = computed(() => toolComplete(props));
</script>
