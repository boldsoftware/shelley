<!-- compact_in_place tool: "index" lists the context, "compact" records an
     in-place compaction, "skip" one that compacts nothing. The header says which; the body shows the output. -->
<template>
  <ToolCard emoji="🗜️" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">{{ summary }}</span>
    </template>
    <ToolSection
      v-if="complete"
      label="Result"
      :text="result || '(no output)'"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { inputFields, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const input = computed(() =>
  inputFields<{ action: string; trim: unknown[]; collapse: unknown[] }>(props.toolInput),
);
const result = computed(() => resultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const summary = computed(() => {
  if (input.value.action === "skip") return "compact: skip";
  if (input.value.action !== "compact") return "compact: index";
  if (complete.value && !props.hasError) return result.value.split(";")[0];
  const collapse = input.value.collapse?.length ?? 0;
  const trim = input.value.trim?.length ?? 0;
  return `compact: ${collapse} collapse, ${trim} trim`;
});
</script>
