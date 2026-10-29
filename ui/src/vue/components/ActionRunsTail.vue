<!-- Project action runs not yet in the transcript as a report message: the
     running one, one whose report is on its way, or one that sends none (an
     archiving success, or an action that does not report). They follow the
     messages they came after; a run older than the latest message has either
     become its report message or was never reported. -->
<template>
  <div
    v-for="run in runs"
    :key="run.id"
    class="message message-tool message-tool-card project-action-run-tail"
    data-testid="project-action-run-tail"
  >
    <div class="message-content">
      <ActionRunCard :run-id="run.id" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message } from "../../types";
import { actionReportSource } from "../../utils/messageSource";
import { useWorkspace } from "../composables/workspace";
import ActionRunCard from "./ActionRunCard.vue";

const props = defineProps<{ conversationId: string | null; messages: Message[] }>();
const workspace = useWorkspace();
const runs = computed(() => {
  const own = (workspace.data.value?.runs ?? []).filter(
    (run) => run.conversation_id === props.conversationId,
  );
  if (!own.length) return [];
  const reported = new Set<string>();
  let latest = 0;
  for (const message of props.messages) {
    const report = actionReportSource(message.user_data);
    if (report) reported.add(report.runId);
    latest = Math.max(latest, Date.parse(message.created_at) || 0);
  }
  return own
    .filter(
      (run) =>
        !reported.has(run.id) &&
        (run.status === "running" || Date.parse(run.started_at) >= latest - 1000),
    )
    .sort((a, b) => a.started_at.localeCompare(b.started_at));
});
</script>
