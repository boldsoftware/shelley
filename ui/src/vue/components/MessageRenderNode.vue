<!-- Recursive renderer for one node of ChatInterface.vue's render model.
     Mirrors the per-item branches of renderMessages() in ChatInterface.tsx
     (timestamps, day separators, token markers, messages, tool cards,
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
  <template v-else-if="node.kind === 'message' && node.item.message">
    <MessageComponent
      :message="node.item.message"
      :on-open-diff-viewer="onOpenDiffViewer"
      :can-request-tour="canRequestTour"
      :on-comment-text-change="onCommentTextChange"
      :on-fork="conversationId ? onFork : undefined"
    />
  </template>
  <template v-else-if="node.kind === 'btw'">
    <BtwInline
      v-for="exchange in node.exchanges"
      :key="exchange.exchange_id"
      :exchange="exchange"
    />
  </template>
  <CoalescedToolCall
    v-else-if="node.kind === 'tool-call'"
    :tool-name="node.item.toolName || 'Unknown Tool'"
    :tool-input="node.item.toolInput"
    :tool-result="node.item.toolResult"
    :tool-error="node.item.toolError"
    :tool-invoked-at="node.item.toolInvokedAt"
    :tool-start-time="node.item.toolStartTime"
    :tool-end-time="node.item.toolEndTime"
    :has-result="node.item.hasResult"
    :tool-interrupted="node.item.toolInterrupted"
    :display="node.item.display"
    :on-comment-text-change="onCommentTextChange"
    :tool-use-id="node.item.toolUseId"
  />
  <CarriedBand v-else-if="node.kind === 'carried-band'" :count="node.count">
    <MessageRenderNode
      v-for="child in node.children"
      :key="child.key"
      :node="child"
      :conversation-id="conversationId"
      :on-open-diff-viewer="onOpenDiffViewer"
      :can-request-tour="canRequestTour"
      :on-comment-text-change="onCommentTextChange"
      :on-fork="onFork"
    />
  </CarriedBand>
</template>

<script setup lang="ts">
import type { RenderNode } from "./renderNode";
import MessageComponent from "./Message.vue";
import MessageTimestamp from "./MessageTimestamp.vue";
import CoalescedToolCall from "./CoalescedToolCall.vue";
import CarriedBand from "./CarriedBand.vue";
import BtwInline from "./BtwInline.vue";

defineProps<{
  node: RenderNode;
  conversationId: string | null;
  onOpenDiffViewer: (commit: string, cwd?: string) => void;
  canRequestTour: boolean;
  onCommentTextChange: (text: string) => void;
  onFork: (messageId: string) => void;
}>();
</script>
