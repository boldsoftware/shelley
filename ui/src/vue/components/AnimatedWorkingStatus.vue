<!-- Vue port of the AnimatedWorkingStatus inner component from
     ChatInterface.tsx. Letter-by-letter bold animation; drops the "Agent "
     prefix on narrow viewports. -->
<template>
  <span class="status-message animated-working">
    <span v-for="(char, idx) in chars" :key="idx" :class="idx === boldIndex ? 'bold-letter' : ''">{{
      char
    }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";

const isNarrow = ref(
  typeof window !== "undefined" ? window.matchMedia("(max-width: 600px)").matches : false,
);

const mq = typeof window !== "undefined" ? window.matchMedia("(max-width: 600px)") : null;
const onChange = (e: MediaQueryListEvent) => {
  isNarrow.value = e.matches;
};
mq?.addEventListener("change", onChange);

const text = computed(() => (isNarrow.value ? "working..." : "Agent working..."));
const chars = computed(() => text.value.split(""));
const boldIndex = ref(0);

let interval: ReturnType<typeof setInterval> | null = null;
watch(
  text,
  () => {
    boldIndex.value = 0;
    if (interval) clearInterval(interval);
    interval = setInterval(() => {
      boldIndex.value = (boldIndex.value + 1) % text.value.length;
    }, 100);
  },
  { immediate: true },
);

onUnmounted(() => {
  if (interval) clearInterval(interval);
  mq?.removeEventListener("change", onChange);
});
</script>
