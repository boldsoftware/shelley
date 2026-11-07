<!-- The collapsible card a tool call renders in: an emoji and a summary
     (the summary slot) in a header that toggles the details (the default
     slot); the always slot sits between them, shown even when collapsed. Keeps the .tool / .tool-header / .tool-summary / .tool-emoji /
     .tool-toggle / .tool-details classes and the tool-call-running /
     tool-call-completed testids the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="complete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="expanded = !expanded">
      <div :class="['tool-summary', summaryClass]">
        <span class="tool-emoji" :class="{ running: isRunning }">{{ emoji }}</span>
        <slot name="summary" />
      </div>
      <button
        class="tool-toggle"
        :aria-label="expanded ? 'Collapse' : 'Expand'"
        :aria-expanded="expanded"
      >
        <ToolChevron :expanded="expanded" />
      </button>
    </div>

    <slot name="always" />

    <div v-if="expanded" class="tool-details">
      <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";

defineProps<{
  emoji: string;
  complete: boolean;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  summaryClass?: string | Record<string, boolean>;
}>();

const expanded = ref(false);
</script>
