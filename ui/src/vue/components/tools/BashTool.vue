<!-- Vue port of components/BashTool.tsx. Preserves the exact DOM classes and
     data-testid contract the e2e tests rely on (.bash-tool,
     .bash-tool-command, .bash-tool-code, .bash-tool-details,
     .bash-tool-header, .bash-tool-cancelled, tool-call-running/completed). -->
<template>
  <div class="bash-tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="bash-tool-header" @click="isExpanded = !isExpanded">
      <div class="bash-tool-summary">
        <span class="bash-tool-emoji" :class="{ running: isRunning }">🛠️</span>
        <HighlightedCode
          class="bash-tool-command"
          :source="displayCommand"
          language="shellscript"
          :title="command"
        />
        <span v-if="displayData?.workingDir" class="bash-tool-cwd" :title="displayData.workingDir">
          in {{ displayData.workingDir }}
        </span>
        <span v-if="isComplete && isCancelled" class="bash-tool-cancelled">✗ cancelled</span>
        <ToolStatusIcon
          v-if="isComplete && hasError && !isCancelled"
          state="error"
          class="bash-tool-error"
        />
        <ToolStatusIcon v-if="isComplete && !hasError" state="ok" class="bash-tool-success" />
      </div>
      <button
        class="bash-tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>

    <!-- Streaming preview — shown below header while running, outside details. -->
    <div v-if="isRunning && streamingOutput && !isExpanded" class="bash-tool-preview">
      <AnsiText ref="previewRef" class-name="bash-tool-preview-code" :text="visibleStreaming" />
      <button
        v-if="hasMoreLines && !previewExpanded"
        class="bash-tool-preview-more"
        @click.stop="previewExpanded = true"
      >
        Show all {{ lineCount }} lines
      </button>
    </div>

    <div v-if="isExpanded" class="bash-tool-details">
      <div v-if="displayData?.workingDir" class="bash-tool-section">
        <div class="bash-tool-label">Working Directory:</div>
        <pre class="bash-tool-code bash-tool-code-cwd">{{ displayData.workingDir }}</pre>
      </div>
      <div class="bash-tool-section">
        <div class="bash-tool-label">Command:</div>
        <HighlightedCode
          tag="pre"
          class="bash-tool-code"
          :source="command"
          language="shellscript"
        />
      </div>

      <div v-if="isRunning && streamingOutput" class="bash-tool-section">
        <div class="bash-tool-label">Output (streaming):</div>
        <AnsiText
          ref="expandedStreamRef"
          class-name="bash-tool-code bash-tool-streaming"
          :text="streamingOutput"
        />
      </div>

      <div v-if="isComplete" class="bash-tool-section">
        <div class="bash-tool-label">
          Output{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="bash-tool-time">{{ executionTime }}</span>
        </div>
        <AnsiText
          :class-name="`bash-tool-code ${hasError ? 'error' : ''}`"
          :text="output || '(no output)'"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import type { LLMContent } from "../../../types";
import HighlightedCode from "../HighlightedCode.vue";
import AnsiText from "./AnsiText.vue";
import { useToolExpanded, useInToolDetail } from "../../composables/toolDetail";
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";
import { isCancelledToolResult } from "../../utils/toolStatus";

interface BashDisplayData {
  workingDir: string;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
  streamingOutput?: string;
}>();

/** Max lines shown in the streaming preview before "Show more" is needed. */
const PREVIEW_LINES = 5;

// Details panel — collapsed by default (expanded inside the detail modal).
const isExpanded = useToolExpanded();
// Streaming preview — expanded to show full streaming output.
const previewExpanded = ref(false);
const previewRef = ref<InstanceType<typeof AnsiText> | null>(null);
const expandedStreamRef = ref<InstanceType<typeof AnsiText> | null>(null);
const inToolDetail = useInToolDetail();

// Collapse details when the tool completes (skip inside the detail modal).
watch(
  () => props.isRunning,
  (running, prevRunning) => {
    if (prevRunning && !running && !inToolDetail) {
      isExpanded.value = false;
      previewExpanded.value = false;
    }
  },
);

// Auto-scroll streaming output to bottom (whichever ref is active).
watch(
  () => props.streamingOutput,
  async (out) => {
    if (!out) return;
    await nextTick();
    const el = previewRef.value?.preEl ?? expandedStreamRef.value?.preEl;
    if (el) el.scrollTop = el.scrollHeight;
  },
);

const displayData = computed<BashDisplayData | null>(() => {
  const d = props.display;
  if (
    d &&
    typeof d === "object" &&
    "workingDir" in d &&
    typeof (d as BashDisplayData).workingDir === "string"
  ) {
    return d as BashDisplayData;
  }
  return null;
});

const command = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "command" in ti &&
    typeof (ti as { command: unknown }).command === "string"
  ) {
    return (ti as { command: string }).command;
  }
  return typeof ti === "string" ? ti : "";
});

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const isCancelled = computed(() => props.hasError && isCancelledToolResult(output.value));

const displayCommand = computed(() => {
  const cmd = command.value;
  const maxLen = 300;
  return cmd.length <= maxLen ? cmd : cmd.substring(0, maxLen) + "...";
});

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

const visibleStreaming = computed(() => {
  if (!props.streamingOutput) return "";
  const lines = props.streamingOutput.split("\n");
  return previewExpanded.value ? props.streamingOutput : lines.slice(-PREVIEW_LINES).join("\n");
});
const hasMoreLines = computed(
  () => !!props.streamingOutput && props.streamingOutput.split("\n").length > PREVIEW_LINES,
);
const lineCount = computed(() =>
  props.streamingOutput ? props.streamingOutput.split("\n").length : 0,
);
</script>
