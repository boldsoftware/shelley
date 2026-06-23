<!-- Vue port of components/ReadImageTool.tsx. Default expanded. Reuses the
     .screenshot-tool DOM contract (same as the React original).
     Preserves: .screenshot-tool, .screenshot-tool-header, .screenshot-tool-summary,
     .screenshot-tool-emoji 🖼️, .screenshot-tool-filename, .screenshot-tool-toggle,
     .screenshot-tool-details, .screenshot-tool-section, .screenshot-tool-label,
     .screenshot-tool-time, .screenshot-tool-image-container, .tool-image-responsive,
     .screenshot-tool-error, .screenshot-tool-success, .screenshot-tool-error-message,
     data-testid tool-call-running/completed. -->
<template>
  <div
    class="screenshot-tool"
    :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'"
  >
    <div class="screenshot-tool-header" @click="isExpanded = !isExpanded">
      <div class="screenshot-tool-summary">
        <span class="screenshot-tool-emoji" :class="{ running: isRunning }">🖼️</span>
        <span class="screenshot-tool-filename" :title="filename">{{ filename }}</span>
        <span v-if="isComplete && hasError" class="screenshot-tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="screenshot-tool-success">✓</span>
      </div>
      <button
        class="screenshot-tool-toggle"
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

    <div v-if="isExpanded" class="screenshot-tool-details">
      <div v-if="isComplete && !hasError && imageUrl" class="screenshot-tool-section">
        <div v-if="executionTime" class="screenshot-tool-label">
          <span>Image:</span>
          <span class="screenshot-tool-time">{{ executionTime }}</span>
        </div>
        <div class="screenshot-tool-image-container">
          <a :href="imageUrl" target="_blank" rel="noopener noreferrer">
            <img
              :src="imageUrl"
              :alt="`Image: ${filename}`"
              class="tool-image-responsive"
              :width="imageWidth || undefined"
              :height="imageHeight || undefined"
            />
          </a>
        </div>
      </div>

      <div v-if="isComplete && hasError" class="screenshot-tool-section">
        <div class="screenshot-tool-label">
          <span>Error:</span>
          <span v-if="executionTime" class="screenshot-tool-time">{{ executionTime }}</span>
        </div>
        <pre class="screenshot-tool-error-message">{{
          toolResult && toolResult[0]?.Text ? toolResult[0].Text : "Image read failed"
        }}</pre>
      </div>

      <div v-if="isRunning" class="screenshot-tool-section">
        <div class="screenshot-tool-label">Reading image...</div>
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
}>();

// Default to expanded.
const isExpanded = ref(true);

const getStringField = (input: unknown, field: string): string | undefined => {
  if (
    typeof input === "object" &&
    input !== null &&
    field in input &&
    typeof (input as Record<string, unknown>)[field] === "string"
  ) {
    return (input as Record<string, string>)[field];
  }
  return undefined;
};

const filename = computed(
  () => getStringField(props.toolInput, "path") || getStringField(props.toolInput, "id") || "image",
);

// Build image URL from the tool result's image content.
// The server replaces inline base64 data with a URL to /api/message/{id}/image/...
const imageContent = computed(() =>
  props.toolResult && props.toolResult.length >= 2 ? props.toolResult[1] : undefined,
);
const imageUrl = computed(() => imageContent.value?.DisplayImageURL);
const imageWidth = computed(() => imageContent.value?.DisplayWidth);
const imageHeight = computed(() => imageContent.value?.DisplayHeight);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
