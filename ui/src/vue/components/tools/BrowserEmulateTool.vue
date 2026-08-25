<!-- Vue port of components/BrowserEmulateTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📱</span>
        <span class="tool-command">{{ summary }}</span>
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="tool-error" />
        <ToolStatusIcon v-if="isComplete && !hasError" state="ok" class="tool-success" />
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
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";

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
