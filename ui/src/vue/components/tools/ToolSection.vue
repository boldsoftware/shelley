<!-- A labelled block of a tool card's details: "<label> (Error): <time>"
     over the text. -->
<template>
  <div class="tool-section">
    <div class="tool-label">
      {{ label }}{{ hasError ? " (Error)" : "" }}:
      <span v-if="executionTime" class="tool-time">{{ executionTime }}</span>
    </div>
    <pre :class="['tool-code', { error: hasError }]">{{ text }}</pre>
  </div>
</template>

<script setup lang="ts">
defineProps<{ label: string; text: string | number; hasError?: boolean; executionTime?: string }>();
</script>
