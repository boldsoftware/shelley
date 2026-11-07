<!-- browser resize action: the new viewport size. -->
<template>
  <ToolCard emoji="📐" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">resize {{ displaySize }}</span>
    </template>
    <div class="tool-section">
      <div class="tool-label">Dimensions:</div>
      <div class="tool-code">{{ width }} × {{ height }} pixels</div>
    </div>
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

const input = computed(() => inputFields(props.toolInput));
const width = computed(() => (typeof input.value.width === "number" ? input.value.width : 0));
const height = computed(() => (typeof input.value.height === "number" ? input.value.height : 0));
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));
const displaySize = computed(() =>
  width.value > 0 && height.value > 0 ? `${width.value}×${height.value}` : "...",
);
</script>
