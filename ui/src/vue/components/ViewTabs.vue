<!-- The tabs in the title bar of a project's or a VM's view: its web page,
     its settings and, for a VM, its terminal. -->
<template>
  <nav class="view-tabs" :aria-label="label">
    <button
      v-for="tab in tabs"
      :key="tab.key"
      type="button"
      class="view-tab"
      :class="{ active: tab.key === active, yields: tab.yields }"
      :title="tab.tooltip"
      :aria-current="tab.key === active ? 'page' : undefined"
      :data-testid="`view-tab-${tab.key}`"
      @click="emit('show', tab.key)"
    >
      <i :class="`pi ${tab.icon}`" aria-hidden="true" />
      <span class="view-tab-title">{{ tab.title }}</span>
    </button>
  </nav>
</template>

<script setup lang="ts" generic="K extends string">
export interface ViewTab<T extends string> {
  key: T;
  /** A PrimeIcons class, e.g. pi-globe. */
  icon: string;
  title: string;
  tooltip?: string;
  /** On a narrow screen, this tab's title (e.g. a page's host) gives way. */
  yields?: boolean;
}

defineProps<{ tabs: ViewTab<K>[]; active: K; label: string }>();
const emit = defineEmits<{ show: [key: K] }>();
</script>

<style scoped>
.view-tabs {
  display: flex;
  align-items: stretch;
  gap: 0.25rem;
  min-width: 0;
  flex: 0 1 auto;
}
.view-tab {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  flex: none;
  max-width: 14rem;
  /* No taller than the title row's buttons. */
  height: 1.875rem;
  padding: 0 0.25rem;
  border: none;
  border-bottom: 2px solid transparent;
  background: none;
  color: var(--text-secondary);
  font: inherit;
  font-size: var(--font-size-13);
  white-space: nowrap;
  cursor: pointer;
}
.view-tab.yields {
  flex: 0 1 auto;
  min-width: 2rem;
}
.view-tab.active {
  border-bottom-color: var(--primary, currentColor);
  color: var(--text-primary);
}
.view-tab i {
  font-size: 0.7rem;
}
.view-tab-title {
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
