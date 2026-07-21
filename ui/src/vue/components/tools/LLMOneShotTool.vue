<!-- Vue port of components/LLMOneShotTool.tsx.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji 🤖, .tool-name,
     .tool-command, .tool-toggle, .tool-details, .tool-section, .tool-label,
     .tool-code, .tool-time, .tool-error, .tool-success,
     data-testid tool-call-running/completed. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">🤖</span>
        <span class="tool-name">llm_one_shot</span>
        <span v-if="isComplete && hasError" class="tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="tool-success">✓</span>
        <span class="tool-command">{{ summary }}</span>
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

    <!-- Images are always visible, even when the card is collapsed. -->
    <div v-if="displayImages.length" class="tool-section llm-one-shot-images">
      <div v-for="img in displayImages" :key="img.url" class="screenshot-tool-image-container">
        <a :href="img.url" target="_blank" rel="noopener noreferrer">
          <img
            :src="img.url"
            :alt="`Image: ${img.path || 'attachment'}`"
            class="tool-image-responsive"
            :width="img.width || undefined"
            :height="img.height || undefined"
          />
        </a>
      </div>
    </div>

    <div v-if="isExpanded" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">Prompt files:</div>
        <pre class="tool-code">{{ promptFiles.join("\n") || "(none)" }}</pre>
      </div>

      <div v-if="model" class="tool-section">
        <div class="tool-label">Model:</div>
        <pre class="tool-code">{{ model }}</pre>
      </div>

      <div v-if="input.system_prompt" class="tool-section">
        <div class="tool-label">System prompt:</div>
        <pre class="tool-code">{{ input.system_prompt }}</pre>
      </div>

      <div v-if="input.output_file" class="tool-section">
        <div class="tool-label">Output file:</div>
        <pre class="tool-code">{{ input.output_file }}</pre>
      </div>

      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Result{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <pre :class="`tool-code ${hasError ? 'error' : ''}`">{{ resultText || "(no output)" }}</pre>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";

interface LLMOneShotInput {
  prompt_files?: string[] | string;
  output_file?: string;
  model?: string;
  system_prompt?: string;
}

interface LLMOneShotDisplayImage {
  url: string;
  path?: string;
  width?: number;
  height?: number;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown; // Display data from the tool_result Content
}>();

const isExpanded = useToolExpanded();

const input = computed<LLMOneShotInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as LLMOneShotInput)
    : {},
);

const promptFiles = computed(() => {
  const pf = input.value.prompt_files;
  if (Array.isArray(pf)) return pf;
  if (typeof pf === "string" && pf) return [pf];
  return [];
});
const model = computed(() => input.value.model || "");

const displayImages = computed<LLMOneShotDisplayImage[]>(() => {
  const d = props.display;
  if (typeof d !== "object" || d === null) return [];
  const images = (d as { images?: unknown }).images;
  if (!Array.isArray(images)) return [];
  return images.filter(
    (img): img is LLMOneShotDisplayImage =>
      typeof img === "object" && img !== null && typeof (img as { url?: unknown }).url === "string",
  );
});

const resultText = computed(
  () =>
    props.toolResult
      ?.filter((r) => r.Type === 2)
      .map((r) => r.Text)
      .join("\n") || "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

const summary = computed(() => {
  const parts: string[] = [];
  if (promptFiles.value.length) parts.push(promptFiles.value.join(", "));
  if (model.value) parts.push(`model: ${model.value}`);
  return parts.join(" · ") || "llm_one_shot";
});
</script>
