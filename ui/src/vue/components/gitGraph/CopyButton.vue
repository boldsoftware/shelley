<!-- Vue port of the CopyButton subcomponent in components/GitGraphViewer.tsx. -->
<template>
  <button
    type="button"
    class="git-graph-copy-btn"
    :title="title || `Copy ${label}`"
    @click="onClick"
  >
    {{ copied ? "copied" : label }}
  </button>
</template>

<script setup lang="ts">
import { onUnmounted, ref } from "vue";
import { copyText } from "../gitGraphLayout";

const props = defineProps<{ value: string; label: string; title?: string }>();

const copied = ref(false);
let timer: ReturnType<typeof setTimeout> | null = null;

onUnmounted(() => {
  if (timer) clearTimeout(timer);
});

async function onClick() {
  if (await copyText(props.value)) {
    copied.value = true;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => (copied.value = false), 1100);
  }
}
</script>
