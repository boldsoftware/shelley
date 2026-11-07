<!-- change_dir tool: "cd <path>" in the header, the path and result in the
     details. -->
<template>
  <ToolCard emoji="📂" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command change-dir-command">cd <PathText :text="path || '...'" /></span>
    </template>
    <div class="tool-section">
      <div class="tool-label">
        Path:
        <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
      </div>
      <div :class="['tool-code', { error: hasError }]">{{ path || "(no path)" }}</div>
    </div>
    <ToolSection
      v-if="complete"
      label="Result"
      :text="result || '(no output)'"
      :has-error="hasError"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import PathText from "../PathText.vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { inputString, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const path = computed(() => inputString(props.toolInput, "path"));
const result = computed(() => resultText(props.toolResult));
const complete = computed(() => toolComplete(props));
</script>
