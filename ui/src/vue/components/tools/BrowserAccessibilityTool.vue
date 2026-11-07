<!-- browser accessibility_* actions: the query in the header, its fields and
     the tree in the details. -->
<template>
  <ToolCard
    emoji="🌳"
    :complete="complete"
    :is-running="isRunning"
    :tool-invoked-at="toolInvokedAt"
  >
    <template #summary>
      <span class="tool-command">{{ summary }}</span>
    </template>
    <ToolSection label="Action" :text="input.action || '(none)'" />
    <ToolSection v-if="input.name" label="Name" :text="input.name" />
    <ToolSection v-if="input.role" label="Role" :text="input.role" />
    <ToolSection v-if="input.selector" label="Selector" :text="input.selector" />
    <ToolSection v-if="input.depth !== undefined" label="Depth" :text="input.depth" />
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
  inputFields<{ action: string; depth: number; name: string; role: string; selector: string }>(
    props.toolInput,
  ),
);
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const summary = computed(() => {
  const i = input.value;
  const summaryParts: string[] = [i.action ?? ""];
  if (i.name) summaryParts.push(`name="${i.name}"`);
  if (i.role) summaryParts.push(`role=${i.role}`);
  if (i.selector) summaryParts.push(i.selector);
  if (i.depth !== undefined) summaryParts.push(`depth=${i.depth}`);
  return summaryParts.filter(Boolean).join(" ") || "accessibility";
});
</script>
