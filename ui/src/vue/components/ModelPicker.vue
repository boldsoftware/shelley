<!-- Vue port of components/ModelPicker.tsx. Model dropdown with readiness /
     source badges. Preserves the model-picker class contract
     (model-picker-trigger / -value / -chevron / -dropdown / -option /
     -option-content / -option-name / -option-source / -option-badge /
     -option-check / -action / -divider / -manage / -options). Opens toward
     the larger side and caps height to the visible viewport. -->
<template>
  <div class="model-picker" ref="containerRef">
    <button
      class="model-picker-trigger"
      :disabled="disabled"
      type="button"
      :title="displayWithSource"
      @click="!disabled && (isOpen = !isOpen)"
    >
      <span class="model-picker-value">{{ displayName }}</span>
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

    <div
      v-if="isOpen"
      :class="`model-picker-dropdown ${openUpward ? 'open-upward' : ''}`"
      ref="dropdownRef"
      :style="{ maxHeight: `${dropdownMaxHeight}px` }"
    >
      <div class="model-picker-action">
        <button
          class="model-picker-option model-picker-manage"
          type="button"
          @click="handleManageModels"
        >
          <svg
            width="14"
            height="14"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <path d="M12 4v16m-8-8h16" />
          </svg>
          <span>Add / Remove Models...</span>
        </button>
        <div class="model-picker-divider" />
      </div>
      <div class="model-picker-options">
        <button
          v-for="model in models"
          :key="model.id"
          :class="`model-picker-option ${model.id === selectedModel ? 'selected' : ''} ${!model.ready ? 'disabled' : ''}`"
          :disabled="!model.ready"
          type="button"
          @click="model.ready && handleSelect(model.id)"
        >
          <div class="model-picker-option-content">
            <span class="model-picker-option-name">{{ model.display_name || model.id }}</span>
            <span v-if="model.source" class="model-picker-option-source">{{ model.source }}</span>
          </div>
          <span v-if="!model.ready" class="model-picker-option-badge">not ready</span>
          <svg
            v-if="model.id === selectedModel"
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
import type { Model } from "../../types";

const dropdownViewportGutterPx = 8;
const dropdownMaxHeightPx = 500;

const props = withDefaults(
  defineProps<{
    models: Model[];
    selectedModel: string;
    disabled?: boolean;
  }>(),
  { disabled: false },
);
const emit = defineEmits<{
  (e: "selectModel", modelId: string): void;
  (e: "manageModels"): void;
}>();

const isOpen = ref(false);
const openUpward = ref(false);
const dropdownMaxHeight = ref(dropdownMaxHeightPx);
const containerRef = ref<HTMLDivElement | null>(null);
const dropdownRef = ref<HTMLDivElement | null>(null);

const selectedModelObj = computed(() => props.models.find((m) => m.id === props.selectedModel));
const displayName = computed(() => selectedModelObj.value?.display_name || props.selectedModel);
const displayWithSource = computed(() =>
  selectedModelObj.value?.source && selectedModelObj.value.source !== "custom"
    ? `${displayName.value} (${selectedModelObj.value.source})`
    : displayName.value,
);

function handleSelect(modelId: string) {
  emit("selectModel", modelId);
  isOpen.value = false;
}

function handleManageModels() {
  isOpen.value = false;
  emit("manageModels");
}

function handleClickOutside(event: MouseEvent) {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    isOpen.value = false;
  }
}
function handleKeyDown(event: KeyboardEvent) {
  if (event.key === "Escape") isOpen.value = false;
}

function updateDropdownPlacement() {
  if (!containerRef.value) return;
  const rect = containerRef.value.getBoundingClientRect();
  const viewportHeight = window.visualViewport?.height ?? window.innerHeight;
  const spaceAbove = Math.max(0, rect.top - dropdownViewportGutterPx);
  const spaceBelow = Math.max(0, viewportHeight - rect.bottom - dropdownViewportGutterPx);
  const shouldOpenUpward = spaceAbove > spaceBelow;
  const availableSpace = shouldOpenUpward ? spaceAbove : spaceBelow;
  openUpward.value = shouldOpenUpward;
  dropdownMaxHeight.value = Math.min(dropdownMaxHeightPx, availableSpace);
}

function detach() {
  document.removeEventListener("mousedown", handleClickOutside);
  document.removeEventListener("keydown", handleKeyDown);
  window.removeEventListener("resize", updateDropdownPlacement);
  window.visualViewport?.removeEventListener("resize", updateDropdownPlacement);
  window.visualViewport?.removeEventListener("scroll", updateDropdownPlacement);
}

watch(isOpen, (open) => {
  detach();
  if (open) {
    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleKeyDown);
    updateDropdownPlacement();
    window.addEventListener("resize", updateDropdownPlacement);
    window.visualViewport?.addEventListener("resize", updateDropdownPlacement);
    window.visualViewport?.addEventListener("scroll", updateDropdownPlacement);
  }
});

onUnmounted(detach);
</script>
