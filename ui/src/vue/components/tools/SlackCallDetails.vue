<!-- A Slack tool call's details: its input, verbatim, and its result. -->
<template>
  <div class="tool-details slack-call-details" data-testid="slack-call-details">
    <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
    <div class="tool-section">
      <div class="tool-label">Input:</div>
      <div class="tool-code">{{ JSON.stringify(toolInput, null, 2) }}</div>
    </div>
    <div v-if="!isRunning" class="tool-section">
      <div class="tool-label">
        {{ hasError ? "Error:" : "Result:" }}
        <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
      </div>
      <div class="tool-code" :class="{ error: hasError }">{{ resultText || "(none)" }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import RunningToolTime from "./RunningToolTime.vue";

defineProps<{
  toolInput?: unknown;
  resultText: string;
  hasError?: boolean;
  executionTime?: string;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
}>();
</script>

<style scoped>
.slack-call-details {
  padding: 0;
  font-size: var(--font-size-13);
}
</style>
