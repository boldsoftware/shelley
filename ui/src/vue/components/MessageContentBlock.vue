<!-- Sub-component of Message.vue: renders a single LLMContent block. Mirrors
     the renderContent() switch in components/Message.tsx — the central
     tool-dispatch mapping ToolName -> tool component, plus text/thinking/web
     search / unknown rendering. Preserves all classes/testids/text. -->
<template>
  <!-- message_role_user / message_role_assistant: unexpected, show as text -->
  <div
    v-if="ct === 'message_role_user' || ct === 'message_role_assistant'"
    class="msg-unexpected-role"
  >
    <div class="msg-unexpected-role-text">[Unexpected message role content: {{ ct }}]</div>
    <div class="msg-unexpected-content">{{ content.Text || JSON.stringify(content) }}</div>
  </div>

  <!-- text -->
  <template v-else-if="ct === 'text'">
    <MarkdownContent v-if="renderMd" :text="content.Text || ''" :message-id="messageId" />
    <div v-else class="whitespace-pre-wrap break-words">
      <InlineText :text="content.Text || ''" />
    </div>
  </template>

  <!-- tool_use / tool_result: dispatch to the right tool component -->
  <component :is="toolDispatch.is" v-else-if="toolDispatch" v-bind="toolDispatch.props" />

  <!-- server_tool_use: web search -->
  <WebSearchTool
    v-else-if="ct === 'server_tool_use'"
    :tool-input="content.ToolInput"
    :is-running="!searchResults"
    :search-results="searchResults"
  />

  <!-- web_search_tool_result -->
  <div
    v-else-if="
      ct === 'web_search_tool_result' && content.ToolResult && content.ToolResult.length > 0
    "
    class="web-search-results"
  >
    <div v-for="(result, index) in content.ToolResult" :key="index" class="web-search-result">
      <a
        :href="result.URL || ''"
        target="_blank"
        rel="noopener noreferrer"
        class="web-search-result-title"
        >{{ result.Title || "Untitled" }}</a
      >
      <div class="web-search-result-meta">
        <span class="web-search-result-url">{{ result.URL || "" }}</span>
        <span v-if="result.PageAge" class="web-search-result-age">{{ result.PageAge }}</span>
      </div>
    </div>
  </div>

  <!-- web_search_result -->
  <div v-else-if="ct === 'web_search_result'" class="web-search-result">
    <a
      :href="content.URL || ''"
      target="_blank"
      rel="noopener noreferrer"
      class="web-search-result-title"
      >{{ content.Title || "Untitled" }}</a
    >
    <div class="web-search-result-meta">
      <span class="web-search-result-url">{{ content.URL || "" }}</span>
      <span v-if="content.PageAge" class="web-search-result-age">{{ content.PageAge }}</span>
    </div>
  </div>

  <!-- redacted_thinking -->
  <div v-else-if="ct === 'redacted_thinking'" class="text-tertiary italic text-sm">
    [Thinking content hidden]
  </div>

  <!-- thinking -->
  <ThinkingContent v-else-if="ct === 'thinking' && thinkingText" :thinking="thinkingText" />

  <!-- unknown content type -->
  <div v-else-if="ct === 'unknown'" class="msg-unknown-content">
    <div class="text-xs text-secondary msg-unknown-content-label">
      Unknown content type: {{ ct }} (value: {{ content.Type }})
    </div>
    <div v-if="content.MediaType" class="msg-media-section">
      <div class="text-xs text-secondary msg-media-type-label">
        Media Type: {{ content.MediaType }}
      </div>
      <img
        v-if="content.MediaType.startsWith('image/') && content.DisplayImageURL"
        :src="content.DisplayImageURL"
        alt="Tool output image"
        class="rounded border msg-media-image"
      />
    </div>
    <div v-if="displayText" class="text-sm whitespace-pre-wrap break-words">{{ displayText }}</div>
    <details v-if="!displayText && hasOtherData" class="text-xs">
      <summary class="text-secondary msg-raw-content-summary">Show raw content</summary>
      <pre class="msg-raw-content-pre">{{ JSON.stringify(content, null, 2) }}</pre>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../types";
import { getContentType } from "../utils/messageContent";
import MarkdownContent from "./MarkdownContent.vue";
import InlineText from "./InlineText.vue";
import ThinkingContent from "./tools/ThinkingContent.vue";
import WebSearchTool from "./tools/WebSearchTool.vue";
import BashTool from "./tools/BashTool.vue";
import PatchTool from "./tools/PatchTool.vue";
import ScreenshotTool from "./tools/ScreenshotTool.vue";
import BrowserTool from "./tools/BrowserTool.vue";
import BrowserNavigateTool from "./tools/BrowserNavigateTool.vue";
import BrowserEvalTool from "./tools/BrowserEvalTool.vue";
import BrowserResizeTool from "./tools/BrowserResizeTool.vue";
import BrowserConsoleLogsTool from "./tools/BrowserConsoleLogsTool.vue";
import GenericTool from "./tools/GenericTool.vue";
import KeywordSearchTool from "./tools/KeywordSearchTool.vue";
import ReadImageTool from "./tools/ReadImageTool.vue";
import ChangeDirTool from "./tools/ChangeDirTool.vue";
import SubagentTool from "./tools/SubagentTool.vue";
import LLMOneShotTool from "./tools/LLMOneShotTool.vue";
import OutputIframeTool from "./tools/OutputIframeTool.vue";
import BrowserEmulateTool from "./tools/BrowserEmulateTool.vue";
import BrowserNetworkTool from "./tools/BrowserNetworkTool.vue";
import BrowserAccessibilityTool from "./tools/BrowserAccessibilityTool.vue";
import BrowserProfileTool from "./tools/BrowserProfileTool.vue";

interface ToolInfo {
  name: string;
  input: unknown;
}

const props = defineProps<{
  content: LLMContent;
  renderMd: boolean;
  messageId?: string;
  toolProgress?: Record<string, import("../../types").ToolProgress>;
  toolUseMap: Record<string, ToolInfo>;
  serverToolResultMap: Record<string, LLMContent[]>;
  onCommentTextChange?: (text: string) => void;
}>();

const ct = computed(() => getContentType(props.content.Type));

const thinkingText = computed(() => props.content.Thinking || props.content.Text || "");

const searchResults = computed(() =>
  props.content.ID ? props.serverToolResultMap[props.content.ID] : undefined,
);

const displayText = computed(() => props.content.Text || props.content.Data || "");
const hasOtherData = computed(() =>
  Object.keys(props.content).some(
    (key) => key !== "Type" && key !== "ID" && props.content[key as keyof LLMContent],
  ),
);

// Map of tool names -> component used by both tool_use and tool_result.
function componentForTool(toolName: string) {
  switch (toolName) {
    case "bash":
    case "shell":
      return BashTool;
    case "patch":
      return PatchTool;
    case "browser":
      return BrowserTool;
    case "screenshot":
    case "browser_take_screenshot":
      return ScreenshotTool;
    case "change_dir":
      return ChangeDirTool;
    case "keyword_search":
      return KeywordSearchTool;
    case "read_image":
      return ReadImageTool;
    case "subagent":
      return SubagentTool;
    case "llm_one_shot":
      return LLMOneShotTool;
    case "output_iframe":
      return OutputIframeTool;
    case "browser_emulate":
      return BrowserEmulateTool;
    case "browser_network":
      return BrowserNetworkTool;
    case "browser_accessibility":
      return BrowserAccessibilityTool;
    case "browser_profile":
      return BrowserProfileTool;
    case "browser_navigate":
      return BrowserNavigateTool;
    case "browser_eval":
      return BrowserEvalTool;
    case "browser_resize":
      return BrowserResizeTool;
    case "browser_recent_console_logs":
    case "browser_clear_console_logs":
      return BrowserConsoleLogsTool;
    default:
      return GenericTool;
  }
}

function execTime(start?: string | null, end?: string | null): string {
  if (start && end) {
    const s = new Date(start).getTime();
    const e = new Date(end).getTime();
    const diffMs = e - s;
    if (diffMs < 1000) return `${diffMs}ms`;
    return `${(diffMs / 1000).toFixed(1)}s`;
  }
  return "";
}

const toolDispatch = computed<{ is: unknown; props: Record<string, unknown> } | null>(() => {
  const c = props.content;
  if (ct.value === "tool_use") {
    const name = c.ToolName || "Unknown Tool";
    const comp = componentForTool(name);
    const base: Record<string, unknown> = { toolInput: c.ToolInput, isRunning: true };
    if (name === "bash" || name === "shell") {
      base.streamingOutput =
        c.ID && props.toolProgress ? props.toolProgress[c.ID]?.output : undefined;
    }
    if (name === "patch") {
      base.onCommentTextChange = props.onCommentTextChange;
    }
    if (
      comp === GenericTool ||
      name === "browser_recent_console_logs" ||
      name === "browser_clear_console_logs"
    ) {
      base.toolName = name;
    }
    return { is: comp, props: base };
  }
  if (ct.value === "tool_result") {
    const hasError = c.ToolError;
    const toolUseId = c.ToolUseID;
    const executionTime = execTime(c.ToolUseStartTime, c.ToolUseEndTime);
    const toolInfo = toolUseId ? props.toolUseMap[toolUseId] : undefined;
    const rawToolName =
      (toolInfo && typeof toolInfo === "object" && toolInfo.name) || c.ToolName || "Unknown Tool";
    const toolInput = toolInfo && typeof toolInfo === "object" ? toolInfo.input : undefined;
    const toolName = rawToolName;
    const comp = componentForTool(toolName);
    const base: Record<string, unknown> = {
      toolInput,
      isRunning: false,
      toolResult: c.ToolResult,
      hasError,
      executionTime,
    };
    // Components that also take `display`.
    if (
      toolName === "patch" ||
      toolName === "browser" ||
      toolName === "output_iframe" ||
      toolName === "screenshot" ||
      toolName === "browser_take_screenshot" ||
      toolName === "llm_one_shot"
    ) {
      base.display = c.Display;
    }
    if (toolName === "patch") {
      base.onCommentTextChange = props.onCommentTextChange;
    }
    if (toolName === "subagent") {
      base.displayData = c.Display as { slug?: string; conversation_id?: string };
    }
    if (
      comp === GenericTool ||
      toolName === "browser_recent_console_logs" ||
      toolName === "browser_clear_console_logs"
    ) {
      base.toolName = toolName;
    }
    return { is: comp, props: base };
  }
  return null;
});
</script>
