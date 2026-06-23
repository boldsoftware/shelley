<!-- Vue port of the DiffstatList subcomponent in components/GitGraphViewer.tsx.
     Compact git diff --stat style list. Each row: +/- counts, a tiny
     green/red bar scaled by the biggest row, and the path. -->
<template>
  <ul class="git-graph-diffstat-list">
    <li v-for="f in rows" :key="f.path" class="git-graph-diffstat-row" :title="f.path">
      <span class="git-graph-diffstat-path">{{ f.path }}</span>
      <span class="git-graph-diffstat-counts">
        <span v-if="f.binary" class="git-graph-diffstat-binary">bin</span>
        <template v-else>
          <span v-if="f.additions > 0" class="git-graph-diffstat-ins">+{{ f.additions }}</span>
          <span v-if="f.deletions > 0" class="git-graph-diffstat-del">−{{ f.deletions }}</span>
        </template>
      </span>
      <span class="git-graph-diffstat-bar" aria-hidden="true">
        <span class="git-graph-diffstat-ins">{{ "+".repeat(f.adds) }}</span>
        <span class="git-graph-diffstat-del">{{ "−".repeat(f.dels) }}</span>
      </span>
    </li>
  </ul>
</template>

<script setup lang="ts">
import { computed } from "vue";

interface DiffFile {
  path: string;
  additions: number;
  deletions: number;
  binary: boolean;
}

const props = defineProps<{ files: DiffFile[] }>();

// 40 chars max wide for the bar; cap smaller for short paths.
const BAR_CHARS = 24;

const rows = computed(() => {
  const maxTotal = Math.max(
    1,
    ...props.files.map((f) => (f.binary ? 0 : f.additions + f.deletions)),
  );
  return props.files.map((f) => {
    const total = f.additions + f.deletions;
    const scale = total === 0 ? 0 : Math.max(1, Math.round((total / maxTotal) * BAR_CHARS));
    const adds =
      total === 0
        ? 0
        : Math.max(f.additions > 0 ? 1 : 0, Math.round((f.additions / Math.max(1, total)) * scale));
    const dels = Math.max(0, scale - adds);
    return { ...f, adds, dels };
  });
});
</script>
