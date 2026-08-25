<!-- Placeholder for a not-yet-mounted message chunk (tail-first rendering,
     see isChunkMounted in ChatInterface.vue). Holds the same 4000px the real
     chunk would occupy before its first layout (the .messages-chunk
     contain-intrinsic-size estimate), so swapping placeholder for content is
     height-neutral for everything below it — which is what keeps the scroll
     position stable while the background sweep mounts history above the
     viewport. Scrolling the placeholder toward the viewport reveals it early
     via the shared near-viewport observer. printReveal so printing includes
     the real messages, not blank bands (best-effort: the mount is async, as
     with tool-card placeholders).
-->
<template>
  <div ref="el" class="messages-chunk-pending" data-testid="pending-chunk" aria-hidden="true" />
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { whenNearViewport } from "../composables/nearViewport";

const emit = defineEmits<{ (e: "reveal"): void }>();
const el = ref<HTMLElement | null>(null);
let cancel: (() => void) | null = null;

onMounted(() => {
  if (!el.value) return;
  cancel = whenNearViewport(el.value, () => emit("reveal"), { printReveal: true });
});
onBeforeUnmount(() => {
  cancel?.();
  cancel = null;
});
</script>
