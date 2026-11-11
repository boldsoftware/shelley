<!-- archive_store (claudetool/archive_store.go): research in the Shelley
     archive store. The summary is the action and what it asks for; the
     result is the tool's text, which says who wrote what. -->
<template>
  <ToolCard
    emoji="🗄️"
    :complete="complete"
    :is-running="isRunning"
    :tool-invoked-at="toolInvokedAt"
  >
    <template #summary>
      <span class="tool-command">{{ summary }}</span>
    </template>
    <ToolSection
      v-if="complete"
      label="Result"
      :text="result || '(no output)'"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { inputFields, resultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const input = computed(() =>
  inputFields<{
    action: string;
    query: string;
    agent_replies: boolean;
    conversation: string;
    author: string;
    project: string;
    since: string;
    until: string;
    expand: number;
  }>(props.toolInput),
);
const summary = computed(() => {
  const i = input.value;
  const what =
    i.action === "search"
      ? `"${i.query ?? ""}"${i.agent_replies ? " in agent replies" : ""}`
      : (i.conversation ?? "");
  const filters = [
    i.author && `author ${i.author}`,
    i.project && `project ${i.project}`,
    i.since && `since ${i.since}`,
    i.until && `until ${i.until}`,
    i.action === "search" && i.conversation && `in ${i.conversation}`,
    i.expand && `expand #${i.expand}`,
  ].filter(Boolean);
  return [i.action ?? "archive", what, filters.join(", ")].filter(Boolean).join(" ");
});
const result = computed(() => resultText(props.toolResult));
const complete = computed(() => toolComplete(props));
</script>
