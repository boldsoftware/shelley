<!-- Vue port of components/SubagentTool.tsx.
     Preserves: .tool, .tool-header, .tool-summary, .tool-emoji ⚡, .tool-name,
     .tool-badge, .subagent-model-badge, .tool-error,
     .tool-success, .tool-command, .tool-toggle, .tool-details, .tool-section,
     .tool-label, .tool-code, .tool-time, .subagent-link,
     data-testid tool-call-running/completed.

     Live view: while the subagent is working (per the conversation list's
     authoritative working flag, injected from App via subagentLive), a strip
     under the header shows what it's doing right now — streaming text tail,
     running tool headline, or last-message preview — sourced from the same
     /api/stream2 events the rest of the UI already receives. Clicking the
     strip opens the subagent conversation.

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
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="tool-error" />
        <ToolStatusIcon v-if="isComplete && !hasError" state="ok" class="tool-success" />
        <span class="tool-command" :title="prompt">{{ commandText }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <button
      v-if="showLive"
      type="button"
      class="subagent-live"
      data-testid="subagent-live"
      :title="`Open subagent '${liveSlug}'`"
      @click.stop="openSubagent"
    >
      <span class="working-indicator" aria-hidden="true" />
      <span class="subagent-live-slug">{{ liveSlug }}&nbsp;↗</span>
      <span class="subagent-live-activity">{{ activity || "working\u2026" }}</span>
    </button>

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
          <a :href="`/c/${liveSlug}`" class="subagent-link" @click="onLinkClick">
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
import { useSubagentLive, navigateToConversationSlug } from "../../composables/subagentLive";
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";

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
  displayData?: { slug?: string; conversation_id?: string; status?: string };
}>();

const isExpanded = useToolExpanded();

const input = computed<SubagentInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as SubagentInput)
    : {},
);

// Prefer the display data's slug: the server may have suffixed the requested
// slug for uniqueness, and displayData carries the actual one.
const slug = computed(() => props.displayData?.slug || input.value.slug || "subagent");
const prompt = computed(() => input.value.prompt || "");
const model = computed(() => input.value.model || "");
const wait = computed(() => input.value.wait !== false);
const timeout = computed(() => input.value.timeout_seconds || 60);

// Live subagent state (working flag + current activity), joined from the
// conversation list + messageStore via the injected app context.
const { conv, working, activity } = useSubagentLive(
  slug,
  computed(() => props.displayData?.conversation_id),
);
// The subagent can still be working after this tool call completed
// (wait=false, or a wait timeout returned a progress summary), so the strip
// keys off the conversation's working flag, not the tool-call state.
const showLive = computed(() => working.value || (!!props.isRunning && !!conv.value));
const liveSlug = computed(() => conv.value?.slug || slug.value);

function openSubagent() {
  navigateToConversationSlug(liveSlug.value);
}

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
  navigateToConversationSlug(liveSlug.value);
}
</script>
