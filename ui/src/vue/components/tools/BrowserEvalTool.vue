<!-- browser eval action: the expression in the header, it and its result in
     the details. -->
<template>
  <ToolCard emoji="⚡" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">{{ displayExpression }}</span>
    </template>
    <ToolSection label="Expression" :text="expression" />
    <ToolSection
      v-if="complete"
      label="Result"
      :text="result || '(no result)'"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, inputString, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const expression = computed(() =>
  typeof props.toolInput === "string" ? props.toolInput : inputString(props.toolInput, "expression"),
);
const result = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const displayExpression = computed(() => {
  const text = expression.value;
  const maxLen = 300;
  return text.length <= maxLen ? text : text.substring(0, maxLen) + "...";
});
</script>
