<!-- Vue port of the RefBadge subcomponent in components/GitGraphViewer.tsx. -->
<template>
  <span :class="cls">{{ label }}</span>
</template>

<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{ name: string }>();

const cls = computed(() => {
  let c = "git-graph-ref";
  if (props.name === "HEAD") {
    c += " git-graph-ref-head";
  } else if (props.name.startsWith("tag: ")) {
    c += " git-graph-ref-tag";
  } else if (props.name.startsWith("origin/") || props.name.includes("/")) {
    c += " git-graph-ref-remote";
  } else {
    c += " git-graph-ref-local";
  }
  return c;
});

const label = computed(() => (props.name.startsWith("tag: ") ? props.name.slice(5) : props.name));
</script>
