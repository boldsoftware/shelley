<template>
  <article class="patch-file-diff">
    <header class="patch-file-diff-header">
      <span :class="`patch-file-status patch-file-status-${status}`" aria-hidden="true"></span>
      <code :title="path">{{ path }}</code>
      <span class="patch-file-stats">
        <span v-if="additions" class="patch-file-additions">+{{ additions }}</span>
        <span v-if="deletions" class="patch-file-deletions">−{{ deletions }}</span>
      </span>
    </header>
    <div
      ref="diffHostEl"
      class="patch-tool-diff-host"
      :style="rendered ? undefined : { minHeight: placeholderHeight }"
    ></div>
    <pre v-if="diffError" class="patch-tool-raw-diff">{{ diff }}</pre>
  </article>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { FileDiffMetadata, FileDiffOptions, ThemeTypes, ThemesType } from "@pierre/diffs";
import { getSingularPatch } from "@pierre/diffs";
import { useFileDiffInstance } from "../../composables/fileDiffInstance";
import { useNearViewport } from "../../composables/nearViewport";

const props = defineProps<{
  path: string;
  diff: string;
  status: "added" | "deleted" | "modified";
  additions: number;
  deletions: number;
  sideBySide: boolean;
  themeType: ThemeTypes;
}>();

const DIFF_THEMES: ThemesType = { dark: "github-dark", light: "github-light" };
const diffHostEl = ref<HTMLElement | null>(null);
const nearViewport = useNearViewport(diffHostEl);

const fileDiff = computed<FileDiffMetadata | null>(() => {
  try {
    return getSingularPatch(props.diff);
  } catch (error) {
    console.warn("PatchTool file diff parse error:", error);
    return null;
  }
});

const diffOptions = computed<FileDiffOptions<undefined>>(() => ({
  diffStyle: props.sideBySide ? "split" : "unified",
  theme: DIFF_THEMES,
  themeType: props.themeType,
  disableFileHeader: true,
}));

const placeholderHeight = computed(
  () => `${Math.min(Math.max(props.diff.split("\n").length, 4) * 20, 2000)}px`,
);
const diffError = computed(() => nearViewport.value && fileDiff.value == null);

const { rendered } = useFileDiffInstance(diffHostEl, () => {
  if (!nearViewport.value || !fileDiff.value) return null;
  return { fileDiff: fileDiff.value, options: diffOptions.value };
});
</script>
