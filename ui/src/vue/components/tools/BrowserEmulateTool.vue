<!-- Vue port of components/BrowserEmulateTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📱</span>
        <span class="tool-command">{{ summary }}</span>
        <span v-if="isComplete && hasError" class="tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="tool-success">✓</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <svg
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
          xmlns="http://www.w3.org/2000/svg"
          class="tool-chevron"
          :class="{ 'tool-chevron-expanded': isExpanded }"
        >
          <path
            d="M4.5 3L7.5 6L4.5 9"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>
    </div>

    <div v-if="isExpanded" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">Action:</div>
        <pre class="tool-code">{{ action || "(none)" }}</pre>
      </div>

      <div v-if="device" class="tool-section">
        <div class="tool-label">Device:</div>
        <pre class="tool-code">{{ device }}</pre>
      </div>

      <div v-if="input.width !== undefined && input.height !== undefined" class="tool-section">
        <div class="tool-label">Dimensions:</div>
        <pre class="tool-code">{{ input.width }} × {{ input.height }}</pre>
      </div>

      <div v-if="input.media" class="tool-section">
        <div class="tool-label">Media:</div>
        <pre class="tool-code">{{ input.media }}</pre>
      </div>

      <div v-if="isComplete && output" class="tool-section">
        <div class="tool-label">
          Output{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <pre :class="`tool-code ${hasError ? 'error' : ''}`">{{ output }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";

interface EmulateInput {
  action?: string;
  device?: string;
  width?: number;
  height?: number;
  mobile?: boolean;
  touch?: boolean;
  device_scale_factor?: number;
  enabled?: boolean;
  media?: string;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const input = computed<EmulateInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as EmulateInput)
    : {},
);

const action = computed(() => input.value.action || "");
const device = computed(() => input.value.device || "");

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

const summary = computed(() => {
  const i = input.value;
  const summaryParts: string[] = [action.value];
  if (device.value) summaryParts.push(device.value);
  if (i.width && i.height) summaryParts.push(`${i.width}×${i.height}`);
  if (i.media) summaryParts.push(i.media);
  if (i.enabled !== undefined) summaryParts.push(i.enabled ? "on" : "off");
  return summaryParts.filter(Boolean).join(" ") || "emulate";
});
</script>
