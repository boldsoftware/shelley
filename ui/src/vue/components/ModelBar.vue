<!-- Vue port of components/ModelBar.tsx. Preserves the model-bar / -summary /
     -icon / -label / -name class contract and the "Reasoning effort" title. -->
<template>
  <div v-if="model" class="model-bar">
    <div class="model-bar-summary">
      <span class="model-bar-icon">🤖</span>
      <span class="model-bar-label">Model</span>
      <span class="model-bar-name">{{ displayName }}</span>
      <span class="model-bar-label" title="Reasoning effort">Reasoning</span>
      <span class="model-bar-name">{{ effectiveReasoning }}</span>
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

// Resolve the models[] entry for this bar. The bar's `model` is often the
// provider API name recorded in usage data (e.g. "claude-opus-4-8"), while the
// models list is keyed by Shelley id (e.g. "claude-opus-4.8"). Match exactly
// first, then tolerate the dot/dash spelling difference so both the display
// name and the reasoning default resolve.
const modelObj = computed(() => {
  const want = props.model;
  if (!want) return undefined;
  const norm = (s: string) => s.replace(/\./g, "-");
  return (
    props.models.find((m) => m.id === want) ||
    props.models.find((m) => norm(m.id) === norm(want))
  );
});

const displayName = computed(() => modelObj.value?.display_name || props.model);

// The reasoning badge is always shown so a conversation never hides how much
// thinking it actually uses. An explicit per-conversation thinking_level wins;
// otherwise fall back to the selected model's default_reasoning_level (what the
// service applies to un-overridden requests). If neither is known — e.g. a
// provider with a dynamic default Shelley can't name — show "default".
const effectiveReasoning = computed(
  () => props.thinkingLevel || modelObj.value?.default_reasoning_level || "default",
);
</script>
