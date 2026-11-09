<!-- Vue port of components/DiffFileTree.tsx. File tree for the diff viewer
     sidebar. Preserves the diff-tree-* class contract, role="tree"/"treeitem",
     aria-label "Files"/"Filter files", and the status letters. Emits
     "select" with the realPath (React onSelect prop). With `ordered`, files
     render in entry order (commit highlights rank them) below the
     commit-messages folder, each under its folders (see buildRows in
     ./diffFileTree.ts). -->
<template>
  <div class="diff-tree">
    <div class="diff-tree-search">
      <input
        type="text"
        class="diff-tree-search-input"
        placeholder="Search…"
        :value="query"
        aria-label="Filter files"
        @input="query = ($event.target as HTMLInputElement).value"
      />
    </div>
    <div class="diff-tree-scroll" role="tree" aria-label="Files">
      <DiffTreeRows
        :rows="renderedRows"
        :selected-real-path="selectedRealPath"
        @select="onSelect"
        @toggle="toggle"
        @selected-row="(el: HTMLButtonElement | null) => (selectedRowEl = el)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, nextTick } from "vue";
import DiffTreeRows from "./DiffTreeRows.vue";
import { buildRows, type DiffFileTreeEntry, type RenderedRow } from "./diffFileTree";

const props = defineProps<{
  entries: DiffFileTreeEntry[];
  selectedRealPath: string | null;
  ordered?: boolean;
}>();
const emit = defineEmits<{ (e: "select", realPath: string): void }>();

function onSelect(realPath: string) {
  emit("select", realPath);
}

// Directories the user has closed; everything else, including directories
// that appear later (new commit, range flip), is open.
const collapsed = ref<Set<string>>(new Set());

// Toggle a chain of directory paths together (flattened chains share one
// open/closed surface).
function toggle(paths: string[]) {
  const prev = collapsed.value;
  const allOpen = paths.every((p) => !prev.has(p));
  const next = new Set(prev);
  if (allOpen) for (const p of paths) next.add(p);
  else for (const p of paths) next.delete(p);
  collapsed.value = next;
}

// Search: case-insensitive substring match on the full path (see
// matchTreePaths).
const query = ref("");

const renderedRows = computed<RenderedRow[]>(() =>
  buildRows(props.entries, !!props.ordered, collapsed.value, query.value, props.selectedRealPath),
);

// Keep the selected row in view when the parent moves the selection.
const selectedRowEl = ref<HTMLButtonElement | null>(null);
watch(
  () => props.selectedRealPath,
  () => {
    nextTick(() => {
      selectedRowEl.value?.scrollIntoView({ block: "nearest" });
    });
  },
);
</script>
