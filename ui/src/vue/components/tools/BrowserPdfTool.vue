<!-- browser pdf action: the header links the saved PDF; expanding previews it
     in the browser's own PDF viewer. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📄</span>
        <span class="tool-command">pdf</span>
        <a
          v-if="pdf"
          class="tool-tag pdf-tool-link"
          :href="pdf.url"
          target="_blank"
          rel="noopener"
          @click.stop
          >{{ pdf.path }}</a
        >
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
      <div v-if="isComplete && pdf" class="tool-section">
        <iframe class="pdf-tool-preview" :src="pdf.url" :title="pdf.path" />
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
  display?: unknown;
}>();

const isExpanded = ref(false);

const pdf = computed(() => {
  const d = props.display as Record<string, unknown> | null | undefined;
  if (d?.type !== "pdf" || typeof d.url !== "string" || typeof d.path !== "string") {
    return undefined;
  }
  return { url: d.url, path: d.path };
});

const output = computed(() => props.toolResult?.[0]?.Text ?? "");

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
