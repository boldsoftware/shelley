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
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="screenshot-tool-error" />
        <ToolStatusIcon v-if="isComplete && !hasError" state="ok" class="screenshot-tool-success" />
      </div>
      <button
        class="screenshot-tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <div v-if="isExpanded" class="screenshot-tool-details">
      <div v-if="isComplete && !hasError && imageUrl" class="screenshot-tool-section">
        <div v-if="executionTime" class="screenshot-tool-label">
          <span>Image:</span>
          <span class="screenshot-tool-time">{{ executionTime }}</span>
        </div>
        <div class="screenshot-tool-image-container">
          <CommentableImage
            :src="imageUrl"
            :alt="`Image: ${filename}`"
            :path="imagePath"
            :width="imageWidth"
            :height="imageHeight"
            :source-width="sourceSize?.width"
            :source-height="sourceSize?.height"
            :needs-auto-orient="needsAutoOrient"
          />
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
import CommentableImage from "../CommentableImage.vue";
import { displayNeedsAutoOrient, displaySourceSize } from "../../../utils/imageComment";
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown; // Display data from the tool_result Content
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

// The file the tool read, so image comments reference it rather than the
// (downscaled) copy served from the message image endpoint. Display carries it
// absolute; toolInput may hold a relative path the agent couldn't resolve.
const imagePath = computed(() => getStringField(props.display, "path"));
// Dimensions of that file, which exceed the rendered image's when it had to be
// downscaled to fit model limits.
const sourceSize = computed(() => displaySourceSize(props.display));
// An EXIF-rotated source file: its stored pixels are turned relative to both
// sourceSize and what the browser draws, so a crop has to auto-orient first.
const needsAutoOrient = computed(() => displayNeedsAutoOrient(props.display));

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
