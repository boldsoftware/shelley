<!-- The success/failure glyph in a tool card header. Every card used to inline a
     text "✓"/"✗", which renders in whatever the font happens to supply (and at
     a different weight than the surrounding icons). One stroked SVG on the same
     24-unit grid as ToolChevron and the patch header buttons.

     Callers keep passing their existing .tool-success / .tool-error (etc.)
     class for the colour, so the CSS and e2e selectors are unchanged. -->
<template>
  <span class="tool-status-icon" role="img" :aria-label="label">
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2.5"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path v-if="state === 'ok'" d="m20 6-11 11-5-5" />
      <path v-else d="M18 6 6 18M6 6l12 12" />
    </svg>
  </span>
</template>

<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{
  state: "ok" | "error";
  /** Overrides the default screen-reader label ("Succeeded"/"Failed"). */
  label?: string;
}>();

const label = computed(() => props.label ?? (props.state === "ok" ? "Succeeded" : "Failed"));
</script>
