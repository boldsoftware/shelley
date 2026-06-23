<!-- Renders text that may contain ANSI escape sequences. If ANSI codes are
     detected, renders sanitized HTML with inline styles; otherwise plain
     text. Vue port of components/AnsiText.tsx. The <pre> element is exposed
     via a `preRef` template ref for callers that auto-scroll. -->
<template>
  <pre v-if="html" ref="preEl" :class="className" v-html="html" />
  <pre v-else ref="preEl" :class="className">{{ text }}</pre>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { ansiToHtml } from "../../../utils/ansi";

const props = defineProps<{ text: string; className?: string }>();
const preEl = ref<HTMLPreElement | null>(null);
const html = computed(() => ansiToHtml(props.text));

defineExpose({ preEl });
</script>
