<!-- browser profile_* actions: the action in the header; a saved profile or
     trace gets a copy button and, for CPU profiles and traces, a Speedscope
     link. -->
<template>
  <ToolCard emoji="📊" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-command">{{ input.action || "profile" }}</span>
    </template>
    <ToolSection label="Action" :text="input.action || '(none)'" />
    <ToolSection v-if="input.categories" label="Categories" :text="input.categories" />
    <ToolSection
      v-if="complete && output"
      label="Output"
      :text="output"
      :has-error="hasError"
      :execution-time="executionTime"
    />

    <div v-if="complete && savedFilePath && !hasError" class="tool-section">
      <div class="tool-label">Profile/Trace file:</div>
      <div class="profile-file-wrapper">
        <code class="profile-file-path">{{ savedFilePath }}</code>
        <button class="profile-copy-button" @click="handleCopyPath">
          {{ copied ? "✓ Copied" : "📋 Copy path" }}
        </button>
        <a
          v-if="input.action === 'cpu_stop' || input.action === 'trace_stop'"
          :href="speedscopeUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="profile-speedscope-link"
          @click.stop
        >
          🔥 Open in Speedscope
        </a>
      </div>
    </div>
  </ToolCard>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { firstResultText, inputFields, toolComplete, type ToolProps } from "./toolProps";

const props = defineProps<ToolProps>();

const copied = ref(false);

const input = computed(() => inputFields<{ action: string; categories: string }>(props.toolInput));
const output = computed(() => firstResultText(props.toolResult));
const complete = computed(() => toolComplete(props));

// Detect file paths in output (for cpu_stop, trace_stop results)
const savedFilePath = computed<string | null>(() => {
  const filePathMatch = output.value.match(/([^\s]+\.json)/i);
  return filePathMatch ? filePathMatch[1] : null;
});

const speedscopeUrl = computed(() =>
  savedFilePath.value
    ? `https://www.speedscope.app/#profileURL=${encodeURIComponent(window.location.origin + "/api/read?path=" + encodeURIComponent(savedFilePath.value))}`
    : "",
);

function handleCopyPath(e: MouseEvent) {
  e.stopPropagation();
  if (savedFilePath.value) {
    navigator.clipboard.writeText(savedFilePath.value).then(() => {
      copied.value = true;
      setTimeout(() => (copied.value = false), 2000);
    });
  }
}
</script>
