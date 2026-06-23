<!-- Vue port of the RangeToggle named export from components/CommitPicker.tsx.
     The "Single Commit" vs "Through working tree" segmented control, used
     inside CommitPicker and the diff viewer sidebar. Preserves the
     commit-picker-range-* class contract and the role="radiogroup"
     aria-label "Diff range" + role="radio" semantics. -->
<template>
  <div class="commit-picker-range-toggle" role="radiogroup" aria-label="Diff range">
    <button
      v-for="o in opts"
      :key="o.value"
      type="button"
      :class="`commit-picker-range-btn${selectedTo === o.value ? ' active' : ''}`"
      :disabled="disabled"
      role="radio"
      :aria-checked="selectedTo === o.value"
      @click="pick(o.value)"
    >
      <span class="commit-picker-range-btn-radio" aria-hidden="true" />
      <span class="commit-picker-range-btn-label">{{ o.label }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{
  selectedDiff: string | null;
  selectedTo: "working" | "self";
}>();
const emit = defineEmits<{
  (e: "change", selectedDiff: string, selectedTo: "working" | "self"): void;
}>();

const disabled = computed(() => props.selectedDiff === null || props.selectedDiff === "working");

const opts: { value: "self" | "working"; label: string }[] = [
  { value: "self", label: "Single commit" },
  { value: "working", label: "Through working tree" },
];

function pick(value: "self" | "working") {
  if (props.selectedDiff && props.selectedDiff !== "working") {
    emit("change", props.selectedDiff, value);
  }
}
</script>
