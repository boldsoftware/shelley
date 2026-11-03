<!-- Vue port of components/BrowserTool.tsx. A router that dispatches to the
     browser sub-tool component based on the `action` field of toolInput,
     mirroring the React switch statement. -->
<template>
  <GenericTool
    v-if="!action.trim()"
    :tool-name="invalidCallName + retryNote"
    :warn-missing-ui="false"
    v-bind="props"
  />
  <GenericTool
    v-else-if="wrongFieldType"
    :tool-name="`Invalid browser call: wrong field type for ${action}${retryNote}`"
    :warn-missing-ui="false"
    v-bind="props"
  />
  <BrowserNavigateTool v-else-if="action === 'navigate'" v-bind="props" />
  <BrowserEvalTool v-else-if="action === 'eval'" v-bind="props" />
  <BrowserResizeTool v-else-if="action === 'resize'" v-bind="props" />
  <ScreenshotTool v-else-if="action === 'screenshot'" v-bind="props" />
  <BrowserPdfTool v-else-if="action === 'pdf'" v-bind="props" />
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
  <GenericTool
    v-else-if="rejectedAsUnknown"
    :tool-name="`Invalid browser call: unsupported action ${JSON.stringify(action)}${retryNote}`"
    :warn-missing-ui="false"
    v-bind="props"
  />
  <GenericTool v-else :tool-name="`browser (${action})`" v-bind="props" />
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../../types";
import BrowserNavigateTool from "./BrowserNavigateTool.vue";
import BrowserEvalTool from "./BrowserEvalTool.vue";
import BrowserResizeTool from "./BrowserResizeTool.vue";
import BrowserConsoleLogsTool from "./BrowserConsoleLogsTool.vue";
import BrowserScreencastTool from "./BrowserScreencastTool.vue";
import BrowserPdfTool from "./BrowserPdfTool.vue";
import BrowserEmulateTool from "./BrowserEmulateTool.vue";
import BrowserNetworkTool from "./BrowserNetworkTool.vue";
import BrowserAccessibilityTool from "./BrowserAccessibilityTool.vue";
import BrowserProfileTool from "./BrowserProfileTool.vue";
import ScreenshotTool from "./ScreenshotTool.vue";
import GenericTool from "./GenericTool.vue";

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolInvokedAt?: string | null;
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
const familyActions: Record<string, readonly string[]> = {
  emulate: ["help", "device", "custom", "reset", "dark_mode", "media"],
  network: ["help", "enable", "disable", "get_log", "clear", "cookies", "clear_cache"],
  accessibility: ["help", "tree", "query", "node"],
  profile: ["help", "metrics", "cpu_start", "cpu_stop", "trace_start", "trace_stop", "coverage_start", "coverage_stop"],
};
const family = computed(() => {
  const name = action.value.split("_", 1)[0];
  return familyActions[name]?.includes(action.value.slice(name.length + 1)) ? name : "";
});
const invalidCallName = computed(() => {
  const input = props.toolInput;
  if (typeof input !== "object" || input === null || Array.isArray(input)) {
    return "Invalid browser call: input must be a JSON object";
  }
  if ("action" in input && typeof input.action !== "string") {
    return "Invalid browser call: action must be a string";
  }
  return "Invalid browser call: missing action";
});
const resultText = computed(() => props.toolResult?.find((r) => r.Type === 2)?.Text ?? "");
// Only the backend knows its full action list; a successful call with an
// action this file does not know is a missing UI, not an invalid call.
const rejectedAsUnknown = computed(
  () =>
    !!props.hasError &&
    /^(invalid browser call: unknown action|unknown (browser )?action:)/.test(resultText.value),
);
const wrongFieldType = computed(
  () => !!props.hasError && resultText.value.startsWith("invalid browser call: field "),
);
const retryNote = computed(() =>
  props.hasError && resultText.value.startsWith("invalid browser call:")
    ? " · model asked to retry"
    : "",
);

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
