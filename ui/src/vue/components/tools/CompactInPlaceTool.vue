<!-- compact_in_place tool: "index" lists the context, "compact" records an
     in-place compaction, "skip" one that compacts nothing. The header says which; the body shows the output. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🗜️</span>
        <span class="tool-command">{{ summary }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <div v-if="isExpanded" class="tool-details">
      <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Result{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <div :class="`tool-code ${hasError ? 'error' : ''}`">{{ resultText || "(no output)" }}</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = ref(false);

type Input = { action?: string; trim?: unknown[]; collapse?: unknown[] };
const input = computed<Input>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null ? (props.toolInput as Input) : {},
);

const resultText = computed(
  () =>
    props.toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || "",
);

const summary = computed(() => {
  if (input.value.action === "skip") return "compact: skip";
  if (input.value.action !== "compact") return "compact: index";
  if (isComplete.value && !props.hasError) return resultText.value.split(";")[0];
  const collapse = input.value.collapse?.length ?? 0;
  const trim = input.value.trim?.length ?? 0;
  return `compact: ${collapse} collapse, ${trim} trim`;
});

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
