<!-- Vue port of components/BashTool.tsx. Preserves the exact DOM classes and
     data-testid contract the e2e tests rely on (.bash-tool,
     .bash-tool-command, .bash-tool-code, .bash-tool-details,
     .bash-tool-header, tool-call-running/completed). -->
<template>
  <div class="bash-tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="bash-tool-header" @click="isExpanded = !isExpanded">
      <div class="bash-tool-summary">
        <span
          v-if="commandRunning"
          class="bash-tool-running-dot"
          role="img"
          aria-label="Command running"
          title="Command running"
          data-testid="bash-tool-running-indicator"
        />
        <span class="bash-tool-emoji" :class="{ running: isRunning }">🛠️</span>
        <HighlightedCode
          v-if="command"
          class="bash-tool-command"
          :source="displayCommand"
          language="shellscript"
        />
        <span v-else class="bash-tool-command">Output</span>
        <span
          v-if="displayData?.workingDir && !background"
          class="bash-tool-cwd"
          :title="displayData.workingDir"
        >
          in <PathText :text="displayData.workingDir" />
        </span>
        <span v-if="returnState" class="bash-tool-background" data-testid="bash-tool-finished-job">
          {{ returnLabels[returnState] }}
        </span>
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
      <RunningToolTime v-if="isRunning" :start-time="toolInvokedAt" />
      <div v-if="displayData?.workingDir" class="bash-tool-section">
        <div class="bash-tool-label">Working Directory:</div>
        <pre class="bash-tool-code bash-tool-code-cwd">{{ displayData.workingDir }}</pre>
      </div>
      <div v-if="command" class="bash-tool-section">
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

      <div v-if="isComplete && (!background || output)" class="bash-tool-section">
        <div class="bash-tool-label">
          {{ outputLabel }}:
          <span v-if="executionTime && !background" class="bash-tool-time">
            {{ executionTime }}
          </span>
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
import PathText from "../PathText.vue";
import { computed, inject, nextTick, ref, watch } from "vue";
import type { LLMContent } from "../../../types";
import HighlightedCode from "../HighlightedCode.vue";
import AnsiText from "./AnsiText.vue";
import ToolChevron from "./ToolChevron.vue";
import RunningToolTime from "./RunningToolTime.vue";
import { isCancelledToolResult } from "../../utils/toolStatus";
import { backgroundOutput, type BackgroundJobDisplay } from "../../../utils/backgroundOutput";
import { BackgroundJobsKey } from "../../composables/backgroundJobs";

interface BashDisplayData {
  workingDir: string;
  exitCode?: number;
  background?: BackgroundJobDisplay;
}

type ReturnState = "finished" | "failed" | "lost" | "running" | "unknown";
const returnLabels: Record<ReturnState, string> = {
  finished: "Finished",
  failed: "Failed",
  lost: "Lost",
  running: "Still running",
  unknown: "Background update",
};

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
  streamingOutput?: string;
  // A completion notice is a separate card from the original tool call.
  returnState?: ReturnState;
}>();

/** Max lines shown in the streaming preview before "Show more" is needed. */
const PREVIEW_LINES = 5;

// Details panel — collapsed by default.
const isExpanded = ref(false);
// Streaming preview — expanded to show full streaming output.
const previewExpanded = ref(false);
const previewRef = ref<InstanceType<typeof AnsiText> | null>(null);
const expandedStreamRef = ref<InstanceType<typeof AnsiText> | null>(null);

// Collapse details when the tool completes.
watch(
  () => props.isRunning,
  (running, prevRunning) => {
    if (prevRunning && !running) {
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

const rawOutput = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const background = computed(() => displayData.value?.background ?? null);
const backgroundJobs = inject(BackgroundJobsKey, null);
const commandRunning = computed(
  () =>
    !!props.isRunning ||
    (!!background.value &&
      !!backgroundJobs?.jobs.value?.some((job) => job.job_id === background.value?.jobId)),
);
const output = computed(() =>
  background.value ? backgroundOutput(rawOutput.value, background.value) : rawOutput.value,
);

const isCancelled = computed(() => props.hasError && isCancelledToolResult(output.value));

const outputLabel = computed(() => {
  if (isCancelled.value) return "Output (cancelled)";
  if (props.returnState) return "Output";
  if (background.value) return "Output so far";
  const exitCode = displayData.value?.exitCode;
  if (typeof exitCode === "number") return `Output (exit code ${exitCode})`;
  return props.hasError ? "Output (Error)" : "Output";
});

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

<style scoped>
.bash-tool-running-dot {
  width: 7px;
  height: 7px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--primary);
  opacity: 0.75;
  animation: bash-running-pulse 3s ease-in-out infinite;
}

@keyframes bash-running-pulse {
  0%,
  100% {
    opacity: 0.65;
  }
  50% {
    opacity: 0.85;
  }
}
</style>
