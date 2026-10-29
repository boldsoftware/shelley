<!-- A project action run in the transcript: a header line (command, status,
     duration) and the command's output. Live while it runs: the card polls
     its run. A report message renders it from its user_data until the run
     loads. -->
<template>
  <div
    class="project-action-card"
    role="article"
    :aria-label="`Project action ${label}`"
    data-testid="project-action-card"
    :data-action-run="runId"
  >
    <div class="tac-head">
      <i class="pi pi-bolt tac-bolt" aria-hidden="true" />
      <strong class="tac-label" :title="commandText">{{ label }}</strong>
      <span
        :class="['tac-pill', `tac-tone-${overall.tone}`]"
        role="status"
        data-testid="project-action-status"
      >
        <i :class="overall.icon" aria-hidden="true" /> {{ overall.label }}
      </span>
      <span v-if="duration" class="tac-time" title="Command duration">{{ duration }}</span>
      <Button
        v-if="status === 'running'"
        label="Stop"
        icon="pi pi-stop"
        text
        size="small"
        severity="danger"
        data-testid="project-action-stop"
        :loading="stopping"
        @click="stop"
      />
      <span v-if="note" class="tac-note project-muted" data-testid="project-action-report-note">{{
        note
      }}</span>
    </div>

    <AnsiText v-if="liveTail" class-name="tac-pre tac-tail" :text="liveTail" />
    <p v-if="extraError" class="error-line tac-small" role="alert">{{ extraError }}</p>
    <p v-if="requestError" class="error-line tac-small" role="alert">{{ requestError }}</p>
    <details v-if="output" class="tac-details">
      <summary>Output · {{ outputLines }} lines</summary>
      <AnsiText class-name="tac-pre" :text="output" />
    </details>
    <details v-if="text" class="tac-details">
      <summary>Message sent to the agent</summary>
      <pre class="tac-pre">{{ text }}</pre>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import Button from "primevue/button";
import { workspaceApi, type ActionRun } from "../../services/workspace";
import type { ActionReportSource } from "../../utils/messageSource";
import { stripAnsi } from "../../utils/ansi";
import { errorMessage } from "../../utils/errorMessage";
import { elapsed, runStatus } from "../../utils/actionStatus";
import { useWorkspace } from "../composables/workspace";
import AnsiText from "./tools/AnsiText.vue";

const props = defineProps<{
  runId: string;
  /** The report message's snapshot of the run, if this card renders one. */
  report?: ActionReportSource | null;
  /** What the report told the agent. */
  text?: string;
}>();

const workspace = useWorkspace();
const run = ref<ActionRun | null>(null);
// The workspace's summary says when the run changes; it lists recent runs only.
const summary = computed(() => workspace.data.value?.runs.find((r) => r.id === props.runId));
const requestError = ref("");
let poll: ReturnType<typeof setTimeout> | null = null;
let disposed = false;
// load fetches the run, and again every second while it runs.
async function load() {
  if (poll) clearTimeout(poll);
  poll = null;
  try {
    // A hidden page skips beats.
    if (!document.hidden || !run.value) run.value = await workspaceApi.run(props.runId);
  } catch {
    // The next beat, or workspace change, retries.
  }
  if (!disposed && (run.value?.status ?? summary.value?.status) === "running")
    poll = setTimeout(() => void load(), 1000);
}
watch(
  () => [summary.value?.status, summary.value?.report],
  () => void load(),
  { immediate: true },
);
onUnmounted(() => {
  disposed = true;
  if (poll) clearTimeout(poll);
});

const stopping = ref(false);
async function stop() {
  stopping.value = true;
  requestError.value = "";
  try {
    await workspaceApi.stopRun(props.runId);
  } catch (err) {
    requestError.value = errorMessage(err);
  } finally {
    stopping.value = false;
  }
}

const label = computed(() => run.value?.label ?? props.report?.label ?? "");
const commandText = computed(() => run.value?.command ?? props.report?.command ?? label.value);
const status = computed(
  () =>
    (run.value?.status ??
      summary.value?.status ??
      props.report?.status ??
      "failed") as ActionRun["status"],
);
const overall = computed(() =>
  runStatus({ status: status.value, exit_code: run.value?.exit_code ?? props.report?.exitCode ?? undefined }),
);

// The duration ticks while the command runs.
const now = ref(Date.now());
const ticking = computed(() => status.value === "running");
let timer: ReturnType<typeof setInterval> | null = null;
watch(
  ticking,
  (on) => {
    if (timer) clearInterval(timer);
    timer = on ? setInterval(() => (now.value = Date.now()), 1000) : null;
  },
  { immediate: true },
);
onUnmounted(() => timer && clearInterval(timer));

const duration = computed(() =>
  run.value ? elapsed(run.value.started_at, run.value.finished_at, now.value) : "",
);

const output = computed(() => run.value?.output ?? "");
const outputLines = computed(() => output.value.trimEnd().split("\n").length);
// While the command runs, and after it fails, its last lines stay in view.
const liveTail = computed(() => {
  if (status.value === "succeeded") return "";
  return stripAnsi(output.value)
    .split("\n")
    .map((line) => line.slice(line.lastIndexOf("\r") + 1))
    .filter((line) => line.trim())
    .slice(-6)
    .join("\n");
});
const extraError = computed(() => {
  const error = run.value?.error ?? "";
  return /^exit status \d+$/.test(error) ? "" : error;
});

// Says what the agent has heard, or will hear, about this run.
const note = computed(() => {
  const report = run.value?.report;
  if (!report) return props.report ? "Sent to the agent" : "";
  if (report === "sent" || props.report) return "Sent to the agent";
  if (report === "pending")
    return status.value === "running"
      ? "Reports to the agent when it finishes"
      : "Sending to the agent…";
  return status.value === "succeeded" ? "" : "Not reported to the agent";
});
</script>

<style scoped>
.project-action-card {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  min-width: 0;
  font-size: var(--font-size-13);
}
.tac-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.25rem 0.6rem;
  min-width: 0;
}
.tac-bolt {
  color: var(--text-tertiary);
}
.tac-label {
  font-family: var(--font-mono);
  overflow-wrap: anywhere;
}
.tac-pill {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.05rem 0.5rem;
  border: 1px solid var(--border-color);
  border-radius: 999px;
  font-size: var(--font-size-12);
  font-weight: 600;
  white-space: nowrap;
}
.tac-tone-success {
  color: var(--success-text);
  background: var(--success-bg);
  border-color: var(--success-border);
}
.tac-tone-danger {
  color: var(--error-text);
  background: var(--error-bg);
  border-color: var(--error-border);
}
.tac-tone-warning {
  color: var(--warning-text);
  background: var(--warning-bg);
  border-color: var(--warning-border);
}
.tac-tone-progress {
  color: var(--blue-text);
  background: var(--blue-bg);
  border-color: var(--blue-border);
}
.tac-time {
  color: var(--text-secondary);
  font-variant-numeric: tabular-nums;
  font-size: var(--font-size-12);
  white-space: nowrap;
}
.tac-note {
  margin-left: auto;
  font-size: var(--font-size-12);
}
.tac-small {
  margin: 0;
  font-size: var(--font-size-12);
}
.tac-details {
  font-size: var(--font-size-12);
}
.tac-details summary {
  cursor: pointer;
  color: var(--text-secondary);
}
:deep(.tac-pre) {
  margin: 0.25rem 0 0;
  padding: 0.5rem 0.6rem;
  max-height: min(40vh, 24rem);
  overflow: auto;
  background: var(--code-bg);
  border-radius: 0.3rem;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
  font-size: var(--font-size-12);
}
:deep(.tac-tail) {
  margin: 0;
}
</style>
