<!-- Vue port of components/SubagentTool.tsx.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji ⚡, .tool-name,
     .tool-badge, .cli-agent-badge, .subagent-model-badge, .tool-error,
     .tool-success, .tool-command, .tool-toggle, .tool-details, .tool-section,
     .tool-label, .tool-code, .tool-time, .subagent-link,
     data-testid tool-call-running/completed.

     Subagent navigation: the React original navigates client-side by pushing
     `/c/{slug}` onto window.history and dispatching a popstate event (no parent
     callback prop). This port replicates that behavior verbatim in onLinkClick;
     it does not introduce a new prop or emit. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">⚡</span>
        <span class="tool-name">subagent</span>
        <span v-if="cliAgentLabel" class="tool-badge cli-agent-badge">{{ cliAgentLabel }}</span>
        <span v-if="isComplete && hasError" class="tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="tool-success">✓</span>
        <span class="tool-command" :title="prompt">{{ commandText }}</span>
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
        <div class="tool-label">
          Prompt to '{{ slug }}':
          <span v-if="model" class="tool-badge subagent-model-badge">{{ model }}</span>
          <span v-if="!wait" class="tool-badge">fire-and-forget</span>
          <span v-if="timeout !== 60" class="tool-badge">timeout: {{ timeout }}s</span>
        </div>
        <div class="tool-code">{{ prompt || "(no prompt)" }}</div>
      </div>

      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">
          Response:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <div :class="`tool-code ${hasError ? 'error' : ''}`">
          {{ resultText || "(no response)" }}
        </div>
      </div>

      <div v-if="displayData?.conversation_id" class="tool-section">
        <div class="tool-label">Conversation:</div>
        <div class="tool-code">
          <a :href="`/c/${slug}`" class="subagent-link" @click="onLinkClick">
            View subagent conversation →
          </a>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";

interface SubagentInput {
  slug?: string;
  prompt?: string;
  model?: string;
  timeout_seconds?: number;
  wait?: boolean;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  displayData?: { slug?: string; conversation_id?: string; cli_agent?: string; status?: string };
}>();

const isExpanded = useToolExpanded();

const input = computed<SubagentInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as SubagentInput)
    : {},
);

const slug = computed(() => input.value.slug || props.displayData?.slug || "subagent");
const prompt = computed(() => input.value.prompt || "");
const model = computed(() => input.value.model || "");
const wait = computed(() => input.value.wait !== false);
const timeout = computed(() => input.value.timeout_seconds || 60);

// Detect CLI agent backend from display data
const cliAgentLabel = computed(() => {
  const cliAgent = props.displayData?.cli_agent; // "claude-cli" or "codex-cli"
  return cliAgent === "claude-cli" ? "Claude CLI" : cliAgent === "codex-cli" ? "Codex CLI" : null;
});

// Extract result text
const resultText = computed(
  () =>
    props.toolResult
      ?.filter((r) => r.Type === 2) // ContentTypeText
      .map((r) => r.Text)
      .join("\n") || "",
);

// Truncate prompt for display
const truncateText = (text: string, maxLen = 60) => {
  if (!text) return "";
  const firstLine = text.split("\n")[0];
  if (firstLine.length <= maxLen) return firstLine;
  return firstLine.substring(0, maxLen) + "...";
};

const displayPrompt = computed(() => truncateText(prompt.value));
const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

// Mirror the React JSX text exactly:
//   Subagent '{slug}'{model ? ` (${model})` : ""}{" "}
//   {isRunning ? (wait ? "running..." : "started") : ""}
//   {displayPrompt && !isRunning && ` ${displayPrompt}`}
const commandText = computed(() => {
  let s = `Subagent '${slug.value}'`;
  if (model.value) s += ` (${model.value})`;
  s += " ";
  s += props.isRunning ? (wait.value ? "running..." : "started") : "";
  if (displayPrompt.value && !props.isRunning) s += ` ${displayPrompt.value}`;
  return s;
});

function onLinkClick(e: MouseEvent) {
  // Let the browser handle cmd/ctrl/shift/middle-click (open in new tab/window).
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
  e.preventDefault();
  // Navigate to the subagent conversation
  window.history.pushState({}, "", `/c/${slug.value}`);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
</script>
