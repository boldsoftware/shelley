<!-- The expand/collapse chevron in every tool card header. Was copy-pasted
     inline in each of them, which is how they drifted apart (PatchTool's icons
     were redrawn on a 24-unit grid while its neighbours stayed on 12). One
     component so a restyle lands everywhere at once.

     Keeps the .tool-chevron / .tool-chevron-expanded classes: the rotation on
     expand lives in styles.css, and e2e tests select on them. -->
<template>
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    class="tool-chevron"
    :class="{ 'tool-chevron-expanded': expanded }"
    aria-hidden="true"
  >
    <path d="m9 6 6 6-6 6" />
  </svg>
</template>

<script setup lang="ts">
defineProps<{ expanded: boolean }>();
</script>
