<!-- Renders the flat row list produced by DiffFileTree.vue. Split out so the
     tree itself stays recursion-free. Preserves the diff-tree-row /
     diff-tree-icon / diff-tree-label / diff-tree-decoration / diff-tree-changes /
     diff-tree-status
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
      <PathText v-if="row.isPath" class="diff-tree-label" :text="row.label" />
      <span v-else class="diff-tree-label">{{ row.label }}</span>
    </button>
    <button
      v-else
      type="button"
      :ref="(el) => setFileRef(el, row.isSelected)"
      :class="['diff-tree-row', { active: row.isSelected, 'diff-tree-code-row': row.isPath }]"
      :style="{ paddingLeft: `calc(0.375rem + ${row.depth} * 0.85rem)` }"
      :title="row.label"
      role="treeitem"
      :aria-selected="row.isSelected"
      @click="emit('select', row.realPath)"
    >
      <span class="diff-tree-icon" v-html="row.isPath ? FILE_ICON : MESSAGE_ICON" />
      <PathText v-if="row.isPath" class="diff-tree-label" :text="row.label" :title="row.realPath" />
      <span v-else class="diff-tree-label">{{ row.label }}</span>
      <span v-if="row.decoration" class="diff-tree-decoration" :title="row.decorationTitle">{{
        row.decoration
      }}</span>
      <span
        v-if="row.highlightCount"
        class="diff-tree-highlight-count"
        :title="highlightsLabel(row.highlightCount)"
        :aria-label="highlightsLabel(row.highlightCount)"
        >★{{ row.highlightCount }}</span
      >
      <span
        v-if="(row.additions ?? 0) > 0 || (row.deletions ?? 0) > 0"
        class="diff-tree-changes"
        :aria-label="changesLabel(row.additions, row.deletions)"
      >
        <span v-if="(row.additions ?? 0) > 0" class="diff-tree-changes-added"
          >+{{ row.additions }}</span
        >
        <span v-if="(row.deletions ?? 0) > 0" class="diff-tree-changes-deleted"
          >&minus;{{ row.deletions }}</span
        >
      </span>
      <span
        v-if="row.statusInfo"
        :class="`diff-tree-status ${row.statusInfo.cls}`"
        :aria-label="row.statusInfo.label"
        >{{ row.statusInfo.letter }}</span
      >
      <span
        v-if="row.isPath && ((row.additions ?? 0) > 0 || (row.deletions ?? 0) > 0)"
        class="diff-tree-graph"
        aria-hidden="true"
      >
        <span class="diff-tree-graph-deleted" :style="{ width: `${(row.deletions ?? 0) / changeScale * 50}%` }" />
        <span class="diff-tree-graph-added" :style="{ width: `${(row.additions ?? 0) / changeScale * 50}%` }" />
      </span>
    </button>
  </template>
</template>

<script setup lang="ts">
import PathText from "./PathText.vue";
import type { ComponentPublicInstance } from "vue";
import type { RenderedRow } from "./diffFileTree";

defineProps<{
  rows: RenderedRow[];
  selectedRealPath: string | null;
  changeScale: number;
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
const MESSAGE_ICON =
  '<svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" d="M2 2.5h12v8H8l-3.5 3v-3H2z" /></svg>';

// Report the selected file row's element to the parent so it can keep it in
// view. `:ref` fires per row on each render; we only care about the selected
// one.
function setFileRef(el: Element | ComponentPublicInstance | null, isSelected: boolean) {
  if (isSelected) emit("selected-row", (el as HTMLButtonElement) ?? null);
}

function highlightsLabel(count: number): string {
  return `${count} highlight${count === 1 ? "" : "s"}`;
}

function changesLabel(additions?: number, deletions?: number): string {
  const parts: string[] = [];
  const plural = (n: number) => (n === 1 ? "line" : "lines");
  if ((additions ?? 0) > 0) parts.push(`${additions} ${plural(additions!)} added`);
  if ((deletions ?? 0) > 0) parts.push(`${deletions} ${plural(deletions!)} removed`);
  return parts.join(", ");
}
</script>

<style scoped>
.diff-tree-row {
  border-radius: 0;
  font-size: 13px;
}
.diff-tree-row.active {
  color: var(--text-primary);
  background: color-mix(in srgb, var(--text-secondary) 16%, transparent);
}
.diff-tree-code-row {
  display: grid;
  grid-template-columns: 0.85rem minmax(0, 1fr) repeat(4, auto);
  column-gap: 0.35rem;
  row-gap: 0;
}
.diff-tree-code-row .diff-tree-label { grid-column: 2; }
.diff-tree-code-row .diff-tree-decoration { grid-column: 3; }
.diff-tree-code-row .diff-tree-highlight-count { grid-column: 4; }
.diff-tree-code-row .diff-tree-changes { grid-column: 5; }
.diff-tree-code-row .diff-tree-status { grid-column: 6; }
.diff-tree-code-row > :not(.diff-tree-graph) { grid-row: 1; }
.diff-tree-row.active .diff-tree-changes-added { color: #288349; }
.diff-tree-row.active .diff-tree-changes-deleted { color: #b83f58; }
.diff-tree-graph {
  position: relative;
  grid-column: 2 / -1;
  grid-row: 2;
  justify-self: end;
  width: 104px;
  max-width: 100%;
  height: 6px;
}
.diff-tree-graph::before {
  content: "";
  position: absolute;
  inset: 1px 0;
  background: color-mix(in srgb, var(--text-secondary) 16%, transparent);
  border-radius: 2px;
}
.diff-tree-graph::after {
  content: "";
  position: absolute;
  left: 50%;
  top: 0;
  bottom: 0;
  width: 1px;
  background: var(--text-secondary);
  opacity: 0.5;
}
.diff-tree-graph-added, .diff-tree-graph-deleted {
  position: absolute;
  top: 1.5px;
  height: 3px;
}
.diff-tree-graph-added { left: 50%; background: #288349; }
.diff-tree-graph-deleted { right: 50%; background: #b83f58; }
@media (max-width: 700px) {
  .diff-tree-graph { width: 140px; }
}
</style>
