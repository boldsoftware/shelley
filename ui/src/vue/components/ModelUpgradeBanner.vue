<template>
  <div class="model-upgrade" :class="{ 'has-detail': detail }" :data-testid="`${testid}-banner`">
    <span class="model-upgrade-text" role="status">
      <strong class="model-upgrade-title">{{ title }}</strong>
      <span v-if="detail" class="model-upgrade-detail" :title="detail">{{ detail }}</span>
    </span>
    <button
      v-if="action"
      type="button"
      class="model-upgrade-switch"
      :data-testid="`${testid}-action`"
      :disabled="busy"
      :title="busy ? 'Wait for this turn to finish' : undefined"
      @click="emit('action')"
    >
      {{ action }}
    </button>
    <button
      type="button"
      class="btn-icon"
      aria-label="Dismiss"
      :data-testid="`${testid}-dismiss`"
      @click="emit('dismiss')"
    >
      ×
    </button>
  </div>
</template>

<script setup lang="ts">
// A strip above the composer offering one action. Two callers use it: the
// model upgrade itself, and the one-off offer to upgrade other conversations.
defineProps<{
  title: string;
  detail?: string;
  // Omitted when the strip only reports something.
  action?: string;
  testid: string;
  busy?: boolean;
}>();
const emit = defineEmits<{ action: []; dismiss: [] }>();
</script>

<style scoped>
.model-upgrade {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.4rem 0.75rem;
  background: var(--bg-secondary, #f8fafc);
  border-top: 1px solid var(--border, #e5e7eb);
  font-size: 0.85rem;
}
.model-upgrade-text {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.model-upgrade-detail {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.75rem;
  opacity: 0.7;
}
@media (max-width: 640px) {
  /* Two rows: the title across the top, then the detail beside the buttons.
     The detail wraps onto extra lines rather than being cut off. */
  .model-upgrade {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    align-items: center;
    gap: 0.15rem 0.5rem;
    padding: 0.4rem 0.6rem;
  }
  .model-upgrade-text {
    display: contents;
  }
  /* With no detail line, the title sits beside the buttons and wraps there. */
  .has-detail .model-upgrade-title {
    grid-column: 1 / -1;
  }
  /* Long ids wrap onto another line rather than being cut off. */
  .model-upgrade-detail {
    white-space: normal;
    overflow-wrap: anywhere;
    overflow: visible;
    text-overflow: clip;
  }
}
.model-upgrade-switch:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.model-upgrade-switch {
  flex: none;
  padding: 0.3rem 0.8rem;
  border-radius: 0.4rem;
  font-weight: 600;
  background: var(--primary, #2563eb);
  color: #fff;
}
</style>
