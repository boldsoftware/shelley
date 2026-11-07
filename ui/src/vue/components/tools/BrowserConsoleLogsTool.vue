<!-- browser console_logs and clear_console_logs actions. -->
<template>
  <ToolCard emoji="📋" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">{{ displayText }}</span>
    </template>
    <ToolSection
      v-if="complete"
      label="Output"
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
import { firstResultText, inputString, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const displayText = computed(() => {
  const clear = inputString(props.toolInput, "action") === "clear_console_logs";
  if (props.isRunning) return clear ? "clearing console..." : "fetching console logs...";
  return clear ? "clear console" : "console logs";
});
</script>
