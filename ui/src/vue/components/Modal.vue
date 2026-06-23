<!-- Vue port of components/Modal.tsx. Teleports to <body>, closes on Escape or
     backdrop click. Preserves classes (.modal-overlay, .modal, .modal-header,
     .modal-title, .modal-title-right, .modal-body, .btn-icon) and the
     aria-label "Close modal". Use the #title-right slot for titleRight. -->
<template>
  <Teleport to="body">
    <div v-if="isOpen" class="modal-overlay" @click="onBackdrop">
      <div :class="['modal', className]">
        <div class="modal-header">
          <h2 class="modal-title">{{ title }}</h2>
          <div v-if="$slots['title-right']" class="modal-title-right">
            <slot name="title-right" />
          </div>
          <button class="btn-icon" aria-label="Close modal" @click="emit('close')">
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>
        <div class="modal-body">
          <slot />
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { toRef } from "vue";
import { useEscapeClose } from "../composables/escapeClose";

const props = defineProps<{
  isOpen: boolean;
  title: string;
  className?: string;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

useEscapeClose(toRef(props, "isOpen"), () => emit("close"));

function onBackdrop(e: MouseEvent) {
  if (e.target === e.currentTarget) emit("close");
}
</script>
