<!-- Vue port of components/ThinkingLevelPicker.tsx. Preserves the model-picker
     class contract (shared with ModelPicker) plus the thinking-level-picker
     modifier. Closes on outside click / Escape; opens upward when short on
     space below. -->
<template>
  <div class="model-picker thinking-level-picker" ref="containerRef">
    <button
      class="model-picker-trigger"
      :disabled="disabled"
      type="button"
      :title="`Reasoning effort: ${current.label}`"
      @click="!disabled && (isOpen = !isOpen)"
    >
      <span class="model-picker-value">{{ current.label }}</span>
      <svg
        :class="`model-picker-chevron ${isOpen ? 'open' : ''}`"
        width="12"
        height="12"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
      >
        <path d="M6 9l6 6 6-6" />
      </svg>
    </button>

    <div v-if="isOpen" :class="`model-picker-dropdown ${openUpward ? 'open-upward' : ''}`">
      <div class="model-picker-options">
        <button
          v-for="level in THINKING_LEVELS"
          :key="level.value"
          :class="`model-picker-option ${level.value === value ? 'selected' : ''}`"
          type="button"
          @click="select(level.value)"
        >
          <div class="model-picker-option-content">
            <span class="model-picker-option-name">{{ level.label }}</span>
          </div>
          <svg
            v-if="level.value === value"
            class="model-picker-option-check"
            width="14"
            height="14"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <path d="M20 6L9 17l-5-5" />
          </svg>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { THINKING_LEVELS, DEFAULT_THINKING_LEVEL, type ThinkingLevel } from "./thinkingLevel";

const props = withDefaults(
  defineProps<{
    value: ThinkingLevel;
    disabled?: boolean;
  }>(),
  { disabled: false },
);
const emit = defineEmits<{ (e: "change", level: ThinkingLevel): void }>();

const isOpen = ref(false);
const openUpward = ref(false);
const containerRef = ref<HTMLDivElement | null>(null);

const current = computed(
  () =>
    THINKING_LEVELS.find((l) => l.value === props.value) ||
    THINKING_LEVELS.find((l) => l.value === DEFAULT_THINKING_LEVEL)!,
);

function select(level: ThinkingLevel) {
  emit("change", level);
  isOpen.value = false;
}

function handleClickOutside(e: MouseEvent) {
  if (containerRef.value && !containerRef.value.contains(e.target as Node)) {
    isOpen.value = false;
  }
}
function handleKeyDown(e: KeyboardEvent) {
  if (e.key === "Escape") isOpen.value = false;
}

function detach() {
  document.removeEventListener("mousedown", handleClickOutside);
  document.removeEventListener("keydown", handleKeyDown);
}

watch(isOpen, (open) => {
  detach();
  if (open) {
    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleKeyDown);
    if (containerRef.value) {
      const rect = containerRef.value.getBoundingClientRect();
      const spaceBelow = window.innerHeight - rect.bottom;
      const dropdownHeight = 260;
      openUpward.value = spaceBelow < dropdownHeight && rect.top > spaceBelow;
    }
  }
});

onUnmounted(detach);
</script>
