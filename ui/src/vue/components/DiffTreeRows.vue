<!-- Renders the flat row list produced by DiffFileTree.vue. Split out so the
     tree itself stays recursion-free. Preserves the diff-tree-row /
     diff-tree-icon / diff-tree-label / diff-tree-decoration / diff-tree-status
     class contract, role="treeitem", aria-expanded/aria-selected, the
     paddingLeft depth formula, and the chevron/file SVG icons. Emits
     "select" (file realPath), "toggle" (dir pathsCovered), and "selected-row"
     (the <button> el of the selected file, so the parent can scroll it into
     view). -->
<template>
  <template v-for="row in rows" :key="row.key">
    <button
      v-if="row.kind === 'dir'"
      type="button"
      class="diff-tree-row"
      :style="{ paddingLeft: `calc(0.375rem + ${row.depth} * 0.85rem)` }"
      :title="row.label"
      role="treeitem"
      :aria-expanded="row.isOpen"
      @click="emit('toggle', row.pathsCovered)"
    >
      <span class="diff-tree-icon" v-html="row.isOpen ? CHEVRON_OPEN : CHEVRON_CLOSED" />
      <span class="diff-tree-label">{{ row.label }}</span>
    </button>
    <button
      v-else
      type="button"
      :ref="(el) => setFileRef(el, row.isSelected)"
      :class="`diff-tree-row${row.isSelected ? ' active' : ''}`"
      :style="{ paddingLeft: `calc(0.375rem + ${row.depth} * 0.85rem)` }"
      :title="row.label"
      role="treeitem"
      :aria-selected="row.isSelected"
      @click="emit('select', row.realPath)"
    >
      <span class="diff-tree-icon" v-html="FILE_ICON" />
      <span class="diff-tree-label">{{ row.label }}</span>
      <span v-if="row.decoration" class="diff-tree-decoration" :title="row.decorationTitle">{{
        row.decoration
      }}</span>
      <span
        v-if="row.statusInfo"
        :class="`diff-tree-status ${row.statusInfo.cls}`"
        :aria-label="row.statusInfo.label"
        >{{ row.statusInfo.letter }}</span
      >
    </button>
  </template>
</template>

<script setup lang="ts">
import type { ComponentPublicInstance } from "vue";
import type { RenderedRow } from "./diffFileTree";

defineProps<{
  rows: RenderedRow[];
  selectedRealPath: string | null;
}>();
const emit = defineEmits<{
  (e: "select", realPath: string): void;
  (e: "toggle", pathsCovered: string[]): void;
  (e: "selected-row", el: HTMLButtonElement | null): void;
}>();

const CHEVRON_OPEN =
  '<svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M3 5l5 5 5-5H3z" /></svg>';
const CHEVRON_CLOSED =
  '<svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M5 3l5 5-5 5V3z" /></svg>';
const FILE_ICON =
  '<svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.2" d="M3.5 1.5h6l3 3v10h-9v-13z M9.5 1.5v3h3" /></svg>';

// Report the selected file row's element to the parent so it can keep it in
// view. `:ref` fires per row on each render; we only care about the selected
// one.
function setFileRef(el: Element | ComponentPublicInstance | null, isSelected: boolean) {
  if (isSelected) emit("selected-row", (el as HTMLButtonElement) ?? null);
}
</script>
