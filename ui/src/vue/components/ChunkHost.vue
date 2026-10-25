<!-- One message chunk of the transcript: either the real content or (below
     the tail-first mount floor) a fixed-height placeholder. See chunkMount.ts
     and the tail-first block in ChatInterface.vue for the design. -->
<template>
  <div v-if="mounted" class="messages-chunk" :class="{ 'messages-chunk--live': chunk.live }">
    <MessageRenderNode
      v-for="node in chunk.nodes"
      :key="node.key"
      :node="node"
      :conversation-id="conversationId"
      :on-open-diff-viewer="onOpenDiffViewer"
      :on-comment-text-change="onCommentTextChange"
      :on-fork="onFork"
    />
  </div>
  <PendingChunk v-else @reveal="reveal" />
</template>

<script setup lang="ts">
import { computed, inject } from "vue";
import type { RenderChunk } from "./renderNode";
import { chunkMountKey } from "./chunkMount";
import MessageRenderNode from "./MessageRenderNode.vue";
import PendingChunk from "./PendingChunk.vue";

const props = defineProps<{
  chunk: RenderChunk;
  conversationId: string | null;
  onOpenDiffViewer: (commit: string, cwd?: string) => void;
  onCommentTextChange: (text: string) => void;
  onFork: (messageId: string) => void;
}>();

const mount = inject(chunkMountKey, null);
// Mounted when: above the swept-top watermark (background sweep), at/after
// the tail floor, individually revealed (near-viewport / jump target), or a
// live tail chunk (which always renders — it holds the streaming tail the
// initial floor is sized to include). Mounting is effectively sticky within
// a conversation; all state resets on switch, when this tree is torn down.
const mounted = computed(
  () =>
    !mount ||
    props.chunk.live === true ||
    props.chunk.globalIndex >= mount.floor.value ||
    props.chunk.globalIndex < mount.sweptTop.value ||
    mount.revealed.has(props.chunk.key),
);

function reveal() {
  mount?.reveal(props.chunk.globalIndex);
}
</script>
