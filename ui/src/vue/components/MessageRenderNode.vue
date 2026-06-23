<!-- Recursive renderer for one node of ChatInterface.vue's render model.
     Mirrors the per-item branches of renderMessages() in ChatInterface.tsx
     (timestamps, day separators, token markers, messages, tool pills/cards,
     and the collapsible carried band). -->
<template>
  <div
    v-if="node.kind === 'day-separator'"
    class="message-day-separator"
    data-testid="message-day-separator"
  >
    <span>{{ node.label }}</span>
  </div>
  <MessageTimestamp v-else-if="node.kind === 'timestamp'" :created-at="node.createdAt" />
  <div
    v-else-if="node.kind === 'token-marker'"
    class="context-token-marker"
    data-testid="context-token-marker"
    :title="`Context size: ${node.ctx.toLocaleString()} tokens`"
  >
    <span>{{ node.label }}</span>
  </div>
  <MessageComponent
    v-else-if="node.kind === 'message' && node.item.message"
    :message="node.item.message"
    :on-open-diff-viewer="onOpenDiffViewer"
    :on-comment-text-change="onCommentTextChange"
    :on-cancel-queued="isQueuedMessage(node.item.message) ? onCancelQueued : undefined"
    :tool-progress="toolProgress"
    :on-fork="conversationId ? onFork : undefined"
  />
  <ToolPillsRow
    v-else-if="node.kind === 'tool-pills'"
    :items="node.items"
    :on-comment-text-change="onCommentTextChange"
    :tool-progress="toolProgress"
  />
  <CoalescedToolCall
    v-else-if="node.kind === 'tool-call'"
    :tool-name="node.item.toolName || 'Unknown Tool'"
    :tool-input="node.item.toolInput"
    :tool-result="node.item.toolResult"
    :tool-error="node.item.toolError"
    :tool-start-time="node.item.toolStartTime"
    :tool-end-time="node.item.toolEndTime"
    :has-result="node.item.hasResult"
    :display="node.item.display"
    :on-comment-text-change="onCommentTextChange"
    :streaming-output="node.item.toolUseId ? toolProgress[node.item.toolUseId]?.output : undefined"
  />
  <CarriedBand v-else-if="node.kind === 'carried-band'" :count="node.count">
    <MessageRenderNode
      v-for="child in node.children"
      :key="child.key"
      :node="child"
      :tool-progress="toolProgress"
      :conversation-id="conversationId"
      :on-open-diff-viewer="onOpenDiffViewer"
      :on-comment-text-change="onCommentTextChange"
      :on-cancel-queued="onCancelQueued"
      :on-fork="onFork"
    />
  </CarriedBand>
</template>

<script setup lang="ts">
import { type ToolProgress, isQueuedMessage } from "../../types";
import type { RenderNode } from "./renderNode";
import MessageComponent from "./Message.vue";
import MessageTimestamp from "./MessageTimestamp.vue";
import ToolPillsRow from "./ToolPillsRow.vue";
import CoalescedToolCall from "./CoalescedToolCall.vue";
import CarriedBand from "./CarriedBand.vue";

defineProps<{
  node: RenderNode;
  toolProgress: Record<string, ToolProgress>;
  conversationId: string | null;
  onOpenDiffViewer: (commit: string, cwd?: string) => void;
  onCommentTextChange: (text: string) => void;
  onCancelQueued: () => void;
  onFork: (messageId: string) => void;
}>();
</script>
