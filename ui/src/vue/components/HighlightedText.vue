<template>
  <span>
    <template v-for="(segment, index) in highlightSegments(text, positions)" :key="index">
      <mark v-if="segment.hit" class="grp-hit">{{ segment.text }}</mark>
      <template v-else>{{ segment.text }}</template>
    </template>
  </span>
</template>

<script setup lang="ts">
import { highlightSegments } from "../../utils/fileSearch";

defineProps<{ text: string; positions?: number[] }>();
</script>
