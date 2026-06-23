<!-- Vue port of components/BrowserScreencastTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. Note: the
     React version uses local useState(true) for expand (NOT the tool-detail
     context), so this mirrors that with a plain ref(true). -->
<template>
  <div
    class="screencast-tool"
    :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'"
  >
    <div class="screencast-tool-header" @click="isExpanded = !isExpanded">
      <div class="screencast-tool-summary">
        <span class="screencast-tool-emoji" :class="{ running: isRunning }">{{ emoji }}</span>
        <span class="screencast-tool-label">{{ label }}</span>
        <span v-if="isComplete && hasError" class="screencast-tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="screencast-tool-success">✓</span>
      </div>
      <button
        class="screencast-tool-toggle"
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

    <div v-if="isExpanded" class="screencast-tool-details">
      <div v-if="isRunning" class="screencast-tool-section">
        <div class="screencast-tool-status">
          <template v-if="action === 'screencast_start'">Starting screencast recording...</template>
          <template v-if="action === 'screencast_stop'">Stopping screencast...</template>
          <template v-if="action === 'screencast_status'">Checking screencast status...</template>
        </div>
      </div>

      <div v-if="isComplete && !hasError && videoUrl" class="screencast-tool-section">
        <div v-if="executionTime" class="screencast-tool-meta">
          <span>{{ executionTime }}</span>
        </div>
        <div class="screencast-tool-video-container">
          <video controls preload="metadata" class="screencast-tool-video">
            <source :src="videoUrl" type="video/mp4" />
            Your browser does not support the video tag.
          </video>
        </div>
      </div>

      <div v-if="isComplete && !hasError && !videoUrl && output" class="screencast-tool-section">
        <div v-if="executionTime" class="screencast-tool-meta">
          <span>{{ executionTime }}</span>
        </div>
        <pre class="screencast-tool-output">{{ output }}</pre>
      </div>

      <div v-if="isComplete && hasError" class="screencast-tool-section">
        <div v-if="executionTime" class="screencast-tool-meta">
          <span>{{ executionTime }}</span>
        </div>
        <pre class="screencast-tool-error-message">{{
          output || "Screencast operation failed"
        }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}>();

const isExpanded = ref(true);

function getInputField(input: unknown, field: string): string | undefined {
  if (typeof input === "object" && input !== null && field in input) {
    const val = (input as Record<string, unknown>)[field];
    return typeof val === "string" ? val : undefined;
  }
  return undefined;
}

const action = computed(() => getInputField(props.toolInput, "action") || "screencast");

const emoji = computed(() => {
  switch (action.value) {
    case "screencast_start":
      return "🔴";
    case "screencast_stop":
      return "🎬";
    case "screencast_status":
      return "📊";
    default:
      return "🎬";
  }
});

const label = computed(() => {
  switch (action.value) {
    case "screencast_start":
      return "recording";
    case "screencast_stop":
      return "screencast";
    case "screencast_status":
      return "screencast status";
    default:
      return "screencast";
  }
});

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const videoUrl = computed<string | undefined>(() => {
  const display = props.display;
  if (display && typeof display === "object" && display !== null) {
    const d = display as Record<string, unknown>;
    if (d.type === "screencast") {
      if (typeof d.url === "string") {
        return d.url;
      } else if (typeof d.path === "string") {
        return `/api/read?path=${encodeURIComponent(d.path as string)}`;
      }
    }
  }
  return undefined;
});

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
