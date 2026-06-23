<!-- Vue port of components/BrowserTool.tsx. A router that dispatches to the
     browser sub-tool component based on the `action` field of toolInput,
     mirroring the React switch statement. -->
<template>
  <BrowserNavigateTool v-if="action === 'navigate'" v-bind="props" />
  <BrowserEvalTool v-else-if="action === 'eval'" v-bind="props" />
  <BrowserResizeTool v-else-if="action === 'resize'" v-bind="props" />
  <ScreenshotTool v-else-if="action === 'screenshot'" v-bind="props" />
  <BrowserConsoleLogsTool
    v-else-if="action === 'console_logs'"
    tool-name="browser_recent_console_logs"
    v-bind="props"
  />
  <BrowserConsoleLogsTool
    v-else-if="action === 'clear_console_logs'"
    tool-name="browser_clear_console_logs"
    v-bind="props"
  />
  <BrowserScreencastTool
    v-else-if="
      action === 'screencast_start' ||
      action === 'screencast_stop' ||
      action === 'screencast_status'
    "
    v-bind="props"
  />
  <!-- Folded-in families: emulate/network/accessibility/profile are exposed as
       `<family>_<sub>` actions on the single browser tool. Dispatch to the
       specialized component, rewriting `action` to the bare sub-action so the
       component (written for the old standalone tools) renders cleanly. -->
  <BrowserEmulateTool
    v-else-if="family === 'emulate'"
    v-bind="props"
    :tool-input="withSubAction('emulate')"
  />
  <BrowserNetworkTool
    v-else-if="family === 'network'"
    v-bind="props"
    :tool-input="withSubAction('network')"
  />
  <BrowserAccessibilityTool
    v-else-if="family === 'accessibility'"
    v-bind="props"
    :tool-input="withSubAction('accessibility')"
  />
  <BrowserProfileTool
    v-else-if="family === 'profile'"
    v-bind="props"
    :tool-input="withSubAction('profile')"
  />
  <GenericTool v-else :tool-name="`browser (${action || 'unknown'})`" v-bind="props" />
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import BrowserNavigateTool from "./BrowserNavigateTool.vue";
import BrowserEvalTool from "./BrowserEvalTool.vue";
import BrowserResizeTool from "./BrowserResizeTool.vue";
import BrowserConsoleLogsTool from "./BrowserConsoleLogsTool.vue";
import BrowserScreencastTool from "./BrowserScreencastTool.vue";
import BrowserEmulateTool from "./BrowserEmulateTool.vue";
import BrowserNetworkTool from "./BrowserNetworkTool.vue";
import BrowserAccessibilityTool from "./BrowserAccessibilityTool.vue";
import BrowserProfileTool from "./BrowserProfileTool.vue";
import ScreenshotTool from "./ScreenshotTool.vue";
import GenericTool from "./GenericTool.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}>();

function getAction(toolInput: unknown): string {
  if (
    typeof toolInput === "object" &&
    toolInput !== null &&
    "action" in toolInput &&
    typeof (toolInput as Record<string, unknown>).action === "string"
  ) {
    return (toolInput as Record<string, unknown>).action as string;
  }
  return "";
}

const action = computed(() => getAction(props.toolInput));
const family = computed(() => action.value.split("_", 1)[0]);

// The specialized sub-components were written for the old standalone tools,
// where `action` was just the bare sub-action (e.g. "device" not
// "emulate_device"). Rewrite the input's `action` to that bare sub-action so
// they render clean summaries. Mirrors React's withSubAction.
function withSubAction(familyName: string): unknown {
  const a = action.value;
  const prefix = `${familyName}_`;
  if (!a.startsWith(prefix)) return props.toolInput;
  const base = props.toolInput as Record<string, unknown>;
  return { ...base, action: a.slice(prefix.length) };
}
</script>
