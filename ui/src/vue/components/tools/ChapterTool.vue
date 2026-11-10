<template>
  <ToolCard emoji="§" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">Chapter: {{ title || "…" }}</span>
    </template>
    <ToolSection
      v-if="complete"
      label="Result"
      :text="resultText(toolResult)"
      :has-error="hasError"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { inputString, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();
const title = computed(() => inputString(props.toolInput, "title"));
const complete = computed(() => toolComplete(props));

</script>
