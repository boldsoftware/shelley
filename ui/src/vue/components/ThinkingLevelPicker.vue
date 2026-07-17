<!-- Reasoning-effort picker, migrated from a hand-rolled dropdown to PrimeVue
     <Select>. PrimeVue owns the open/close, outside-click, Escape and viewport
     placement (flip) that the old component reimplemented by hand. Styling is
     entirely PrimeVue's own token system: size="small" for the compact look
     plus the shared statusPickerDt token map (our theme vars). Emits "change"
     with the new level (unchanged prop/event contract with ChatStatusContent). -->
<template>
  <Select
    :model-value="effectiveValue"
    :options="availableLevels"
    option-label="label"
    option-value="value"
    :disabled="disabled || !supported"
    fluid
    size="small"
    :dt="statusPickerDt"
    scroll-height="22rem"
    class="thinking-level-picker"
    :aria-label="`Reasoning effort: ${current.label}`"
    append-to="self"
    :pt="{ overlay: { class: 'thinking-level-picker-panel' } }"
    @update:model-value="select"
  />
</template>

<script setup lang="ts">
import { computed } from "vue";
import Select from "primevue/select";
import { statusPickerDt } from "./statusPickerDt";
import { THINKING_LEVELS, DEFAULT_THINKING_LEVEL, type ThinkingLevel } from "./thinkingLevel";

const props = withDefaults(
  defineProps<{
    value: ThinkingLevel;
    disabled?: boolean;
    supported?: boolean;
    levels?: ThinkingLevel[];
    /** The concrete level the model applies when the conversation carries no
     * explicit override. When known, we DON'T offer a separate "default" entry
     * — the default is just one of the real levels (pre-selected), so the
     * dropdown never shows a redundant "default (medium)" next to "medium". */
    defaultLevel?: string;
  }>(),
  { disabled: false, supported: true, levels: () => [] },
);
const emit = defineEmits<{ (e: "change", level: ThinkingLevel): void }>();

// The model's default as a real, selectable level (null when the provider's
// default can't be named, e.g. a dynamic default Shelley doesn't know).
const modelDefault = computed<ThinkingLevel | null>(() => {
  const d = props.defaultLevel;
  if (!d || d === "default") return null;
  return THINKING_LEVELS.some((l) => l.value === d) ? (d as ThinkingLevel) : null;
});

// What the Select highlights. A stored "default" sentinel resolves to the
// model's concrete default level when we know it, so the picker shows the real
// level (e.g. medium) rather than a made-up "default" row.
const effectiveValue = computed<ThinkingLevel>(() =>
  props.value === "default" && modelDefault.value ? modelDefault.value : props.value,
);

const availableLevels = computed(() => {
  const real = THINKING_LEVELS.filter((l) => l.value !== "default");
  if (!props.supported) {
    // Reasoning unsupported: show only the effective level (picker is disabled).
    const eff = real.find((l) => l.value === effectiveValue.value);
    return eff ? [eff] : [{ value: "default" as ThinkingLevel, label: "default" }];
  }
  const list =
    props.levels.length === 0 ? [...real] : real.filter((l) => props.levels.includes(l.value));
  // Make sure the model's default is always selectable, even if it falls
  // outside the advertised subset (defensive; normally it's included).
  if (modelDefault.value && !list.some((l) => l.value === modelDefault.value)) {
    const def = real.find((l) => l.value === modelDefault.value);
    if (def) list.push(def);
  }
  // Only keep the "default" sentinel when the concrete default is unknown, so
  // users can still defer to the model; otherwise it's just one of the levels.
  return modelDefault.value === null
    ? [{ value: "default" as ThinkingLevel, label: "default" }, ...list]
    : list;
});

const current = computed(
  () =>
    availableLevels.value.find((l) => l.value === effectiveValue.value) ||
    availableLevels.value.find((l) => l.value === DEFAULT_THINKING_LEVEL) ||
    availableLevels.value[0] || { value: "default" as ThinkingLevel, label: "default" },
);

function select(level: ThinkingLevel) {
  emit("change", level);
}
</script>
