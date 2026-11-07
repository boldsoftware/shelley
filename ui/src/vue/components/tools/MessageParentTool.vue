<!-- message_parent: a subagent's message to its parent conversation. The
     parent shows the delivered text as a "Message from <slug>" user bubble;
     this is the sender's side. -->
<template>
  <ToolCard emoji="💬" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">→ parent: {{ firstLine || "..." }}</span>
      <span v-if="endTurn" class="tool-command">(final)</span>
    </template>
    <div class="tool-section">
      <div class="tool-label">
        {{ messageLabel }}:
        <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
      </div>
      <div class="tool-code">{{ text || "(empty)" }}</div>
    </div>
    <div v-if="complete && hasError" class="tool-section">
      <div class="tool-label">Error:</div>
      <div class="tool-code error">{{ result }}</div>
    </div>
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import { inputFields, inputString, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const text = computed(() => inputString(props.toolInput, "text"));
const firstLine = computed(() => text.value.trim().split(/\r?\n/)[0] ?? "");
const endTurnValue = computed(() => {
  const v = inputFields(props.toolInput).end_turn;
  return typeof v === "boolean" ? v : null;
});
const endTurn = computed(() => endTurnValue.value === true);
const messageLabel = computed(() =>
  endTurnValue.value === null
    ? "Message"
    : endTurn.value
      ? "Final report (ends turn)"
      : "Progress message",
);

const result = computed(() => resultText(props.toolResult));
const complete = computed(() => toolComplete(props));
</script>
