<!-- Fallback renderer for tools without their own component: the tool name in
     the header, the raw input and output in the details. -->
<template>
  <ToolCard
    emoji="⚙️"
    :complete="complete"
    :is-running="isRunning"
    :tool-invoked-at="toolInvokedAt"
    :summary-class="{ 'generic-tool-summary--complete': complete }"
  >
    <template #summary>
      <span class="generic-tool-copy">
        <span class="tool-command">{{ toolName }}</span>
        <GenericToolWarning v-if="complete && warnMissingUi" :tool-name="toolName" />
      </span>
    </template>
    <ToolSection v-if="toolInput !== undefined" label="Input" :text="formatData(toolInput)" />

    <div v-if="isRunning" class="tool-section">
      <div class="tool-label">Status:</div>
      <div class="tool-running-text">running...</div>
    </div>

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
import GenericToolWarning from "./GenericToolWarning.vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { toolComplete, type ToolProps } from "./toolProps";

const props = withDefaults(defineProps<ToolProps & { toolName: string; warnMissingUi?: boolean }>(), {
  warnMissingUi: true,
});

const formatData = (data: unknown): string => {
  if (data === undefined || data === null) return "";
  if (typeof data === "string") return data;
  try {
    return JSON.stringify(data, null, 2);
  } catch {
    return String(data);
  }
};

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0
    ? props.toolResult.map((result) => result.Text || formatData(result)).join("\n")
    : "",
);

const complete = computed(() => toolComplete(props));
</script>
