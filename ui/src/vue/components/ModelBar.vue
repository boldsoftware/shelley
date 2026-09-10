<!-- Compact one-line generation metadata rendered inside the shared context card. -->
<template>
  <div v-if="model" class="model-bar">
    <div class="model-bar-summary">
      <span class="model-bar-icon" aria-hidden="true">🤖</span>
      <span class="model-bar-label">Model:</span>
      <span class="model-bar-name" :title="modelTitle">{{ displayName }}</span>
      <span class="model-bar-comma" aria-hidden="true">,</span>
      <span class="model-bar-name model-bar-reasoning">{{ effectiveReasoning }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { prettyModelLabels } from "../../utils/modelNames";
import type { Model } from "../../types";

const props = withDefaults(
  defineProps<{
    model?: string | null;
    // Every distinct model the generation actually ran, first-seen order (the
    // first is the starting model). When it holds more than one, the bar shows
    // "Mixed" instead of a single name.
    modelsUsed?: string[];
    models?: Model[];
    thinkingLevel?: string | null;
  }>(),
  { models: () => [], modelsUsed: () => [] },
);

// Resolve the models[] entry for this bar. The bar's `model` is often the
// provider API name recorded in usage data (e.g. "claude-opus-4-8"), while the
// models list is keyed by Shelley id (e.g. "claude-opus-4.8"). Match exactly
// first, then tolerate the dot/dash spelling difference so both the display
// name and the reasoning default resolve.
const norm = (s: string) => s.replace(/\./g, "-");
const labels = computed(() => prettyModelLabels(props.models));
function resolveDisplayName(want: string | null | undefined): string | null | undefined {
  if (!want) return want;
  const obj =
    props.models.find((m) => m.id === want) || props.models.find((m) => norm(m.id) === norm(want));
  if (!obj) return want;
  return labels.value.get(obj.id) || want;
}

const modelObj = computed(() => {
  const want = props.model;
  if (!want) return undefined;
  return (
    props.models.find((m) => m.id === want) || props.models.find((m) => norm(m.id) === norm(want))
  );
});

// A generation that ran more than one model (via a mid-generation /model
// switch) shows "Mixed"; the full list is on hover. Otherwise show the single
// model's display name.
const isMixed = computed(() => props.modelsUsed.length > 1);
const displayName = computed(() => (isMixed.value ? "Mixed" : resolveDisplayName(props.model)));
const modelTitle = computed(() =>
  isMixed.value
    ? props.modelsUsed.map((m) => resolveDisplayName(m)).join(" \u2192 ")
    : (resolveDisplayName(props.model) ?? undefined),
);

// The reasoning badge is always shown so a conversation never hides how much
// thinking it actually uses. An explicit per-conversation thinking_level wins;
// otherwise fall back to the selected model's default_reasoning_level (what the
// service applies to un-overridden requests). If neither is known — e.g. a
// provider with a dynamic default Shelley can't name — show "default".
const effectiveReasoning = computed(
  () => props.thinkingLevel || modelObj.value?.default_reasoning_level || "default",
);
</script>
