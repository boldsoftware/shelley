<!-- browser navigate action: the URL in the header, a link to it and the
     output in the details. -->
<template>
  <ToolCard emoji="🌐" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">{{ displayUrl }}</span>
    </template>
    <div class="tool-section">
      <div class="tool-label">URL:</div>
      <div class="tool-code">
        <a :href="externalUrl" target="_blank" rel="noopener noreferrer">{{ externalUrl }}</a>
      </div>
    </div>
    <ToolSection
      v-if="complete && output"
      label="Output"
      :text="output"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { localhostLinkOptionsFromInit, rewriteLocalhostLink } from "../../../utils/linkify";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, inputString, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const url = computed(() =>
  typeof props.toolInput === "string" ? props.toolInput : inputString(props.toolInput, "url"),
);
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

const externalUrl = computed(() => rewriteLocalhostLink(url.value, localhostLinkOptionsFromInit()));

const displayUrl = computed(() => {
  const u = externalUrl.value;
  const maxLen = 300;
  return u.length <= maxLen ? u : u.substring(0, maxLen) + "...";
});
</script>
