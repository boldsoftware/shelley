<!-- send_to_thread / send_to_channel / add_reaction: the Slack Assistant posting to Slack. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">💬</span>
        <span class="tool-command">→ {{ where }}: {{ summary || "..." }}</span>
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
      <div class="tool-section">
        <div class="tool-label">
          Slack message:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <div class="tool-code">{{ input.text || summary || "(empty)" }}</div>
      </div>
      <div v-if="input.files.length" class="tool-section">
        <div class="tool-label">Files:</div>
        <div class="tool-code">{{ input.files.join("\n") }}</div>
      </div>
      <div v-if="isComplete" class="tool-section">
        <div class="tool-label">{{ hasError ? "Error:" : "Result:" }}</div>
        <div class="tool-code" :class="{ error: hasError }">{{ resultText }}</div>
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
}>();

const isExpanded = ref(false);

const input = computed(() => {
  const ti = props.toolInput;
  const field = (k: string) =>
    typeof ti === "object" && ti !== null && typeof (ti as Record<string, unknown>)[k] === "string"
      ? ((ti as Record<string, unknown>)[k] as string)
      : "";
  const files =
    typeof ti === "object" && ti !== null && Array.isArray((ti as Record<string, unknown>).files)
      ? ((ti as Record<string, unknown>).files as unknown[]).filter(
          (f): f is string => typeof f === "string",
        )
      : [];
  return {
    channel: field("channel"),
    threadTS: field("thread_ts"),
    ts: field("ts"),
    emoji: field("emoji"),
    text: field("text"),
    files,
  };
});

const where = computed(() => {
  const { channel, threadTS, ts } = input.value;
  if (threadTS) return `${channel} thread ${threadTS}`;
  if (ts) return `${channel} message ${ts}`;
  return channel || "Slack";
});
const summary = computed(() => {
  const { emoji, text, files } = input.value;
  if (emoji) return `:${emoji}:`;
  const line = text.trim().split(/\r?\n/)[0] ?? "";
  return files.length ? `${line} (+${files.length} file${files.length > 1 ? "s" : ""})` : line;
});

const resultText = computed(
  () =>
    props.toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);
</script>
