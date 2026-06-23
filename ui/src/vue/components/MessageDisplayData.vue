<!-- Sub-component of Message.vue: renders one tool display_data entry. Mirrors
     renderDisplayData() in components/Message.tsx — infers patch vs generic
     tool output and renders via PatchTool / GenericTool. Returns nothing for
     screenshot displays (handled by tool_result rendering). -->
<template>
  <PatchTool
    v-if="inferredToolName === 'patch' && typeof display === 'string'"
    :tool-input="{}"
    :is-running="false"
    :tool-result="stringPatchResult"
    :has-error="false"
    :on-comment-text-change="onCommentTextChange"
  />
  <PatchTool
    v-else-if="inferredToolName === 'patch'"
    :tool-input="{}"
    :is-running="false"
    :tool-result="emptyPatchResult"
    :has-error="false"
    :display="display"
    :on-comment-text-change="onCommentTextChange"
  />
  <GenericTool
    v-else-if="!isScreenshot"
    :tool-name="inferredToolName || toolName || 'Tool output'"
    :tool-input="{}"
    :is-running="false"
    :tool-result="genericResult"
    :has-error="false"
  />
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { LLMContent } from "../../types";
import PatchTool from "./tools/PatchTool.vue";
import GenericTool from "./tools/GenericTool.vue";

interface ToolDisplay {
  tool_use_id: string;
  tool_name?: string;
  display: unknown;
}

const props = defineProps<{
  toolDisplay: ToolDisplay;
  toolName?: string;
  onCommentTextChange?: (text: string) => void;
}>();

const display = computed(() => props.toolDisplay.display);

const isScreenshot = computed(() => {
  const d = display.value;
  return !!(
    d &&
    typeof d === "object" &&
    "type" in d &&
    (d as { type: unknown }).type === "screenshot"
  );
});

const inferredToolName = computed<string | undefined>(() => {
  if (props.toolName) return props.toolName;
  const d = display.value;
  // String diffs (very old format)
  if (typeof d === "string" && d.includes("---") && d.includes("+++")) return "patch";
  // Object display with path + diff or oldContent/newContent (legacy structured)
  if (typeof d === "object" && d !== null && "path" in d && ("diff" in d || "oldContent" in d)) {
    return "patch";
  }
  return undefined;
});

const stringPatchResult = computed<LLMContent[]>(() => [
  {
    ID: props.toolDisplay.tool_use_id,
    Type: 6, // tool_result
    Text: display.value as string,
  } as LLMContent,
]);

const emptyPatchResult = computed<LLMContent[]>(() => [
  {
    ID: props.toolDisplay.tool_use_id,
    Type: 6,
    Text: "",
  } as LLMContent,
]);

const genericResult = computed<LLMContent[]>(() => [
  {
    ID: props.toolDisplay.tool_use_id,
    Type: 6,
    Text: JSON.stringify(display.value, null, 2),
  } as LLMContent,
]);
</script>
