<!-- write_secret (claudetool/secret.go): a secret, as a pill, written to a
     file. The value is never in the input, the result or the display. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🔑</span>
        <span class="tool-command">
          <SecretPill v-if="input.id" :id="input.id" /> → {{ target }}
        </span>
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
import SecretPill from "../SecretPill.vue";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}>();

const isExpanded = ref(false);
const input = computed(
  () => (props.toolInput ?? {}) as { id?: string; path?: string; ssh_target?: string },
);
const shown = computed(() => (props.display ?? null) as { machine?: string; path?: string } | null);
// Once written, the display's machine and absolute path; before, the input's.
const target = computed(() => {
  if (shown.value?.path) return `${shown.value.machine}:${shown.value.path}`;
  return `${input.value.ssh_target ? `${input.value.ssh_target}:` : ""}${input.value.path ?? "..."}`;
});
const resultText = computed(
  () =>
    props.toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") ?? "",
);
const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
