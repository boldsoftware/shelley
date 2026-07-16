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

    <div v-if="isExpanded" class="tool-details">
      <div class="tool-section">
        <div class="tool-label">Prompt file:</div>
        <pre class="tool-code">{{ promptFile || "(none)" }}</pre>
      </div>

      <div v-if="model" class="tool-section">
        <div class="tool-label">Model:</div>
        <pre class="tool-code">{{ model }}</pre>
      </div>

      <div v-if="input.attachments?.length" class="tool-section">
        <div class="tool-label">Attachments:</div>
        <pre class="tool-code">{{ input.attachments.join("\n") }}</pre>
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
  prompt_file?: string;
  output_file?: string;
  model?: string;
  system_prompt?: string;
  attachments?: string[];
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();

const input = computed<LLMOneShotInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as LLMOneShotInput)
    : {},
);

const promptFile = computed(() => input.value.prompt_file || "");
const model = computed(() => input.value.model || "");

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
  if (promptFile.value) parts.push(promptFile.value);
  if (model.value) parts.push(`model: ${model.value}`);
  if (input.value.attachments?.length) {
    parts.push(`${input.value.attachments.length} image attachment${input.value.attachments.length === 1 ? "" : "s"}`);
  }
  return parts.join(" · ") || "llm_one_shot";
});
</script>
