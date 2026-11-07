<!-- browser pdf action: the header links the saved PDF; expanding previews it
     in the browser's own PDF viewer. -->
<template>
  <ToolCard emoji="📄" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">pdf</span>
      <a
        v-if="pdf"
        class="tool-tag pdf-tool-link"
        :href="pdf.url"
        target="_blank"
        rel="noopener"
        @click.stop
        >{{ pdf.path }}</a
      >
    </template>
    <div v-if="complete && pdf" class="tool-section">
      <iframe class="pdf-tool-preview" :src="pdf.url" :title="pdf.path" />
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
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const pdf = computed(() => {
  const d = props.display as Record<string, unknown> | null | undefined;
  if (d?.type !== "pdf" || typeof d.url !== "string" || typeof d.path !== "string") {
    return undefined;
  }
  return { url: d.url, path: d.path };
});
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));
</script>
