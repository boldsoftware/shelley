<!-- Vue port of components/BrowserProfileTool.tsx. Preserves the exact DOM
     classes, data-testid, and aria contracts the e2e tests rely on. -->
<template>
  <div class="tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji" :class="{ running: isRunning }">📊</span>
        <span class="tool-command">{{ summary }}</span>
        <ToolStatusIcon v-if="isComplete && hasError" state="error" class="tool-error" />
        <ToolStatusIcon v-if="isComplete && !hasError" state="ok" class="tool-success" />
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
      <div class="tool-section">
        <div class="tool-label">Action:</div>
        <pre class="tool-code">{{ action || "(none)" }}</pre>
      </div>

      <div v-if="input.categories" class="tool-section">
        <div class="tool-label">Categories:</div>
        <pre class="tool-code">{{ input.categories }}</pre>
      </div>

      <div v-if="isComplete && output" class="tool-section">
        <div class="tool-label">
          Output{{ hasError ? " (Error)" : "" }}:
          <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
        </div>
        <pre :class="`tool-code ${hasError ? 'error' : ''}`">{{ output }}</pre>
      </div>

      <div v-if="isComplete && savedFilePath && !hasError" class="tool-section">
        <div class="tool-label">Profile/Trace file:</div>
        <div class="profile-file-wrapper">
          <code class="profile-file-path">{{ savedFilePath }}</code>
          <button class="profile-copy-button" @click="handleCopyPath">
            {{ copied ? "✓ Copied" : "📋 Copy path" }}
          </button>
          <a
            v-if="action === 'cpu_stop' || action === 'trace_stop'"
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { LLMContent } from "../../../types";
import { useToolExpanded } from "../../composables/toolDetail";
import ToolChevron from "./ToolChevron.vue";
import ToolStatusIcon from "./ToolStatusIcon.vue";

interface ProfileInput {
  action?: string;
  categories?: string;
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}>();

const isExpanded = useToolExpanded();
const copied = ref(false);

const input = computed<ProfileInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as ProfileInput)
    : {},
);

const action = computed(() => input.value.action || "");

const output = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

// Detect file paths in output (for cpu_stop, trace_stop results)
const savedFilePath = computed<string | null>(() => {
  const filePathMatch = output.value.match(/([^\s]+\.json)/i);
  return filePathMatch ? filePathMatch[1] : null;
});

const summary = computed(() => action.value || "profile");

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
