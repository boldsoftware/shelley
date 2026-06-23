<!-- Vue port of components/VimToggle.tsx. Compact toggle for Monaco's vim
     emulation. Preserves the .vim-toggle class, aria-pressed, and the
     title toggle. Desktop only - the parent decides whether to render it.
     Emits "change" with the new enabled value (React onChange prop). -->
<template>
  <button
    type="button"
    :class="`vim-toggle ${enabled ? 'active' : ''}`"
    :title="enabled ? 'Disable Vim mode' : 'Enable Vim mode'"
    :aria-pressed="enabled"
    @click="emit('change', !enabled)"
  >
    Vim
  </button>
</template>

<script setup lang="ts">
defineProps<{
  enabled: boolean;
}>();
const emit = defineEmits<{ (e: "change", enabled: boolean): void }>();
</script>
