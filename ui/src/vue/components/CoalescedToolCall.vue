<!-- Vue port of the CoalescedToolCall inner component from ChatInterface.tsx.
     Dispatches a single tool call to its specialized component (TOOL_COMPONENTS),
     falling back to a generic running/completed card. Preserves the
     tool-running / tool-result-details class + testid contract. -->
<template>
  <component :is="toolComponent" v-if="toolComponent" v-bind="toolComponentProps" />

  <!-- Fallback: running state -->
  <div v-else-if="!hasResult" class="message message-tool" data-testid="tool-call-running">
    <div class="message-content">
      <div class="tool-running">
        <div class="tool-running-header">
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-tool-icon">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
            />
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
            />
          </svg>
          <span class="tool-name">Tool: {{ toolName }}</span>
          <span class="tool-status-running">(running)</span>
        </div>
        <div class="tool-input">
          {{ typeof toolInput === "string" ? toolInput : JSON.stringify(toolInput, null, 2) }}
        </div>
      </div>
    </div>
  </div>

  <!-- Fallback: completed state -->
  <div v-else class="message message-tool" data-testid="tool-call-completed">
    <div class="message-content">
      <details
        :class="`tool-result-details ${toolError ? 'error' : ''}`"
        :open="inToolDetail || undefined"
      >
        <summary class="tool-result-summary">
          <div class="tool-result-meta">
            <div class="flex items-center space-x-2">
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-tool-icon">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
                />
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
                />
              </svg>
              <span class="text-sm font-medium text-blue">{{ toolName }}</span>
              <span :class="`tool-result-status text-xs ${toolError ? 'error' : 'success'}`">
                {{ toolError ? "\u2717" : "\u2713" }} {{ summary }}
              </span>
            </div>
            <div class="tool-result-time">
              <span v-if="executionTime">{{ executionTime }}</span>
            </div>
          </div>
        </summary>
        <div class="tool-result-content">
          <div class="tool-result-section">
            <div class="tool-result-label">Input:</div>
            <div class="tool-result-data">
              <template v-if="toolInput">
                {{ typeof toolInput === "string" ? toolInput : JSON.stringify(toolInput, null, 2) }}
              </template>
              <span v-else class="text-secondary italic">No input data</span>
            </div>
          </div>
          <div :class="`tool-result-section output ${toolError ? 'error' : ''}`">
            <div class="tool-result-label">Output{{ toolError ? " (Error)" : "" }}:</div>
            <div class="space-y-2">
              <div v-for="(result, idx) in toolResult" :key="idx">
                <div v-if="result.Type === 2" class="whitespace-pre-wrap break-words">
                  {{ result.Text || "" }}
                </div>
                <div v-else class="text-secondary text-sm italic">
                  [Content type {{ result.Type }}]
                </div>
              </div>
            </div>
          </div>
        </div>
      </details>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../types";
import { useInToolDetail } from "../composables/toolDetail";
import BashTool from "./tools/BashTool.vue";
import PatchTool from "./tools/PatchTool.vue";
import ScreenshotTool from "./tools/ScreenshotTool.vue";
import ReadImageTool from "./tools/ReadImageTool.vue";
import BrowserTool from "./tools/BrowserTool.vue";
import BrowserNavigateTool from "./tools/BrowserNavigateTool.vue";
import BrowserEvalTool from "./tools/BrowserEvalTool.vue";
import BrowserResizeTool from "./tools/BrowserResizeTool.vue";
import BrowserConsoleLogsTool from "./tools/BrowserConsoleLogsTool.vue";
import BrowserEmulateTool from "./tools/BrowserEmulateTool.vue";
import BrowserNetworkTool from "./tools/BrowserNetworkTool.vue";
import BrowserAccessibilityTool from "./tools/BrowserAccessibilityTool.vue";
import BrowserProfileTool from "./tools/BrowserProfileTool.vue";
import KeywordSearchTool from "./tools/KeywordSearchTool.vue";
import ChangeDirTool from "./tools/ChangeDirTool.vue";
import SubagentTool from "./tools/SubagentTool.vue";
import LLMOneShotTool from "./tools/LLMOneShotTool.vue";
import OutputIframeTool from "./tools/OutputIframeTool.vue";
import ReadContextFileTool from "./tools/ReadContextFileTool.vue";
import WebSearchTool from "./tools/WebSearchTool.vue";

const props = defineProps<{
  toolName: string;
  toolInput?: unknown;
  toolResult?: LLMContent[];
  toolError?: boolean;
  toolStartTime?: string | null;
  toolEndTime?: string | null;
  hasResult?: boolean;
  display?: unknown;
  onCommentTextChange?: (text: string) => void;
  streamingOutput?: string;
}>();

const inToolDetail = useInToolDetail();

// Map tool names to their specialized components (mirrors TOOL_COMPONENTS).
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const TOOL_COMPONENTS: Record<string, any> = {
  bash: BashTool,
  shell: BashTool,
  patch: PatchTool,
  browser: BrowserTool,
  screenshot: ScreenshotTool,
  read_image: ReadImageTool,
  keyword_search: KeywordSearchTool,
  change_dir: ChangeDirTool,
  subagent: SubagentTool,
  output_iframe: OutputIframeTool,
  read_context_file: ReadContextFileTool,
  llm_one_shot: LLMOneShotTool,
  browser_emulate: BrowserEmulateTool,
  browser_network: BrowserNetworkTool,
  browser_accessibility: BrowserAccessibilityTool,
  browser_profile: BrowserProfileTool,
  web_search: WebSearchTool,
  browser_take_screenshot: ScreenshotTool,
  browser_navigate: BrowserNavigateTool,
  browser_eval: BrowserEvalTool,
  browser_resize: BrowserResizeTool,
  browser_recent_console_logs: BrowserConsoleLogsTool,
  browser_clear_console_logs: BrowserConsoleLogsTool,
};

const executionTime = computed(() => {
  if (props.hasResult && props.toolStartTime && props.toolEndTime) {
    const diffMs = new Date(props.toolEndTime).getTime() - new Date(props.toolStartTime).getTime();
    return diffMs < 1000 ? `${diffMs}ms` : `${(diffMs / 1000).toFixed(1)}s`;
  }
  return "";
});

const toolComponent = computed(() => TOOL_COMPONENTS[props.toolName] || null);

const toolComponentProps = computed<Record<string, unknown>>(() => {
  const base: Record<string, unknown> = {
    toolInput: props.toolInput,
    isRunning: !props.hasResult,
    toolResult: props.toolResult,
    hasError: props.toolError,
    executionTime: executionTime.value,
    display: props.display,
  };
  if (props.toolName === "patch" && props.onCommentTextChange) {
    base.onCommentTextChange = props.onCommentTextChange;
  }
  if (props.streamingOutput !== undefined) {
    base.streamingOutput = props.streamingOutput;
  }
  if (props.toolName === "subagent") {
    base.displayData = props.display;
  }
  return base;
});

const summary = computed(() => {
  const results = props.toolResult;
  if (!results || results.length === 0) return "No output";
  const first = results[0];
  if (first.Type === 2 && first.Text) {
    const text = first.Text.trim();
    if (text.length <= 50) return text;
    return text.substring(0, 47) + "...";
  }
  return `${results.length} result${results.length > 1 ? "s" : ""}`;
});
</script>
