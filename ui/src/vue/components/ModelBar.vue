<!-- Vue port of components/ModelBar.tsx. Preserves the model-bar / -summary /
     -icon / -label / -name class contract and the "Reasoning effort" title. -->
<template>
  <div v-if="model" class="model-bar">
    <div class="model-bar-summary">
      <span class="model-bar-icon">🤖</span>
      <span class="model-bar-label">Model</span>
      <span class="model-bar-name">{{ displayName }}</span>
      <template v-if="thinkingLevel">
        <span class="model-bar-label" title="Reasoning effort">Reasoning</span>
        <span class="model-bar-name">{{ thinkingLevel }}</span>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Model } from "../../types";

const props = withDefaults(
  defineProps<{
    model?: string | null;
    models?: Model[];
    thinkingLevel?: string | null;
  }>(),
  { models: () => [] },
);

const displayName = computed(() => {
  const modelObj = props.models.find((m) => m.id === props.model);
  return modelObj?.display_name || props.model;
});
</script>
