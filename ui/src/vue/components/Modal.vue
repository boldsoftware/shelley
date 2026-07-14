<!-- Vue port of components/Modal.tsx, now backed by PrimeVue Dialog for focus
     trapping, mask/stacking, and accessible role="dialog" semantics. The
     #container slot reproduces the legacy markup so the DOM/ARIA contract is
     unchanged: classes (.modal-overlay on the mask, .modal, .modal-header,
     .modal-title, .modal-title-right, .modal-body, .btn-icon) and the
     aria-label "Close modal". PrimeVue owns backdrop-click closing; Escape is
     handled by a shared stack (modalEscapeStack) so that when modals are
     stacked only the topmost one closes. Use the #title-right slot for
     titleRight and the #footer slot for a pinned footer row. -->
<template>
  <Dialog
    :visible="isOpen"
    modal
    :dismissable-mask="true"
    :close-on-escape="false"
    :show-header="false"
    append-to="body"
    :pt="{ root: { class: 'modal-dialog-root' }, mask: { class: 'modal-overlay' } }"
    :aria-labelledby="titleId"
    @update:visible="onVisibleChange"
    @show="onShow"
  >
    <template #container>
      <div ref="panelRef" :class="['modal', className]" role="document" tabindex="-1">
        <div class="modal-header">
          <h2 :id="titleId" class="modal-title">{{ title }}</h2>
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
        <div v-if="$slots.footer" class="modal-footer">
          <slot name="footer" />
        </div>
      </div>
    </template>
  </Dialog>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, useId, watch } from "vue";
import Dialog from "primevue/dialog";
import { popModalEscape, pushModalEscape } from "../composables/modalEscapeStack";

const props = defineProps<{
  isOpen: boolean;
  title: string;
  className?: string;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

// Route Escape through a shared stack so that with stacked modals only the
// topmost one closes. PrimeVue's per-dialog Escape is disabled (close-on-escape
// above) because it isn't stacking-aware. Register while open, unregister on
// close/unmount.
const requestClose = () => emit("close");
watch(
  () => props.isOpen,
  (open) => {
    popModalEscape(requestClose);
    if (open) pushModalEscape(requestClose);
  },
  { immediate: true },
);
onBeforeUnmount(() => popModalEscape(requestClose));

const panelRef = ref<HTMLDivElement | null>(null);

// Give the dialog an accessible name. Without an explicit aria-labelledby,
// PrimeVue defaults it to a generated "<id>_header" element that the #container
// slot replaces and never renders, leaving the dialog unnamed. Point it at the
// real .modal-title instead.
const titleId = useId();

// PrimeVue drives visibility internally; relay every request to close
// (dismissable mask click; Escape is handled by the stack above) up to the
// parent, which owns isOpen.
function onVisibleChange(visible: boolean) {
  if (!visible) emit("close");
}

// The #container slot replaces PrimeVue's default header/content, so its
// built-in focus() has nothing to target. Move focus into the panel ourselves
// so the FocusTrap directive engages and screen readers announce the dialog —
// unless a child already claimed focus (e.g. an autofocused filter input).
function onShow() {
  nextTick(() => {
    const panel = panelRef.value;
    if (!panel) return;
    if (panel.contains(document.activeElement)) return;
    panel.focus();
  });
}
</script>
