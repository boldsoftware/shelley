<!-- browser network_* actions: the action and filter in the header, the
     captured requests in the details. -->
<template>
  <ToolCard
    emoji="📡"
    :complete="complete"
    :is-running="isRunning"
    :tool-invoked-at="toolInvokedAt"
  >
    <template #summary>
      <span class="tool-command">{{ summary }}</span>
    </template>
    <ToolSection label="Action" :text="input.action || '(none)'" />
    <ToolSection v-if="input.filter" label="Filter" :text="input.filter" />
    <ToolSection v-if="input.limit !== undefined" label="Limit" :text="input.limit" />
    <ToolSection
      v-if="complete && output"
      label="Output"
      :text="output"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, inputFields, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const input = computed(() =>
  inputFields<{ action: string; filter: string; limit: number }>(props.toolInput),
);
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const summary = computed(() => {
  const summaryParts: string[] = [input.value.action ?? ""];
  if (input.value.filter) summaryParts.push(`filter: ${input.value.filter}`);
  return summaryParts.filter(Boolean).join(" ") || "network";
});
</script>
