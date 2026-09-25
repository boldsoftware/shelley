<template>
  <article class="commit-tour-chunk" :data-tour-file="fileLabel">
    <div class="commit-tour-chunk-header">
      <button
        v-if="entry.trivial"
        type="button"
        class="commit-tour-chunk-toggle"
        :aria-expanded="expanded"
        @click="emit('update:expanded', !expanded)"
      >
        <ToolChevron :expanded="expanded" />
        <span class="tour-trivial-label">trivial</span>
        <code :title="chunkLabel">{{ chunkLabel }}</code>
        <span class="commit-tour-chunk-stats">
          <span class="commit-tour-additions">+{{ chunkStats.additions }}</span>
          <span class="commit-tour-deletions">−{{ chunkStats.deletions }}</span>
        </span>
      </button>
      <template v-else>
        <code :title="chunkLabel">{{ chunkLabel }}</code>
        <span class="commit-tour-chunk-stats">
          <span class="commit-tour-additions">+{{ chunkStats.additions }}</span>
          <span class="commit-tour-deletions">−{{ chunkStats.deletions }}</span>
        </span>
      </template>
      <!-- aria-disabled rather than disabled while loading: disabling the
           focused button would drop keyboard focus to the body mid-action.
           toggleFullFile ignores clicks while a load is in flight. -->
      <button
        v-if="expanded && canShowFullFile"
        v-tooltip.top="fullFile ? 'Show changes only' : 'Show full file'"
        type="button"
        class="commit-tour-full-file-btn"
        :class="{ active: fullFile }"
        :aria-label="fullFile ? `Show changes only in ${fileLabel}` : `Show full ${fileLabel}`"
        :aria-busy="fileLoading"
        :aria-disabled="fileLoading"
        @click="toggleFullFile"
      >
        <span v-if="fileLoading" class="spinner spinner-small" aria-hidden="true"></span>
        <svg
          v-else
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M12 22v-6M12 8V2M4 12H2M10 12H8M16 12h-2M22 12h-2" />
          <path v-if="fullFile" d="M15 19l-3-3-3 3M15 5l-3 3-3-3" />
          <path v-else d="M15 19l-3 3-3-3M15 5l-3-3-3 3" />
        </svg>
      </button>
      <button
        v-tooltip.top="'Comment on this chunk'"
        type="button"
        class="commit-tour-comment-btn"
        :aria-label="`Comment on ${fileLabel} chunk`"
        @click="openChunkComment"
      >
        💬
      </button>
    </div>

    <div v-if="expanded" class="commit-tour-chunk-body">
      <MarkdownContent v-if="entry.comment" class="commit-tour-comment" :text="entry.comment" />
      <div v-if="isBinary" class="commit-tour-placeholder">
        <span>Binary file</span>
        <pre>{{ entry.patch }}</pre>
      </div>
      <div v-else-if="!isHunk" class="commit-tour-placeholder">
        <pre>{{ entry.patch || "No textual diff." }}</pre>
      </div>
      <template v-else>
        <div v-if="fileError" class="commit-tour-file-error" role="alert">
          Couldn't load the full file: {{ fileError }}
        </div>
        <!-- Re-keying on mode swaps in a fresh FileDiff instance: with the
             worker pool, a reused instance first repaints its cached previous
             diff, which would make the scroll anchoring in handlePostRender
             measure the wrong render. -->
        <div
          :key="showingFullFile ? 'full-file' : 'patch'"
          ref="diffHostEl"
          class="commit-tour-diff-host"
          :style="rendered ? undefined : { minHeight: placeholderHeight }"
          @pointerdown.capture="rememberPointerDown"
          @click="handleSeparatorClick"
        ></div>
        <pre v-if="diffError" class="commit-tour-raw-diff">{{ entry.patch }}</pre>
      </template>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import type {
  FileDiffMetadata,
  FileDiffOptions,
  PostRenderPhase,
  ThemeTypes,
  ThemesType,
} from "@pierre/diffs";
import { DIFFS_TAG_NAME, getSingularPatch, parseDiffFromFile } from "@pierre/diffs";
import type { GitTourPatchEntry } from "../../services/api";
import type { GitFileDiff } from "../../types";
import { useFileDiffInstance } from "../composables/fileDiffInstance";
import {
  patchLineText,
  type TourCommentTarget,
  type TourDiffSide,
} from "../composables/tourComments";
import { useNearViewport } from "../composables/nearViewport";
import {
  fileLineText,
  fullFileNames,
  patchFitsContents,
  pickChangeAnchor,
  type ChangeLineAnchor,
  type ChangeRowGeometry,
} from "./commitTourFullFile";
import { analyzeTourPatch } from "./commitTourPatch";
import MarkdownContent from "./MarkdownContent.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{
  entry: GitTourPatchEntry;
  expanded: boolean;
  themeType: ThemeTypes;
  sideBySide: boolean;
  overflow: "scroll" | "wrap";
  // Fetches both sides of the file as of the toured commit. Absent when the
  // host cannot resolve file contents; the full-file toggle is then hidden.
  loadFile?: (oldPath: string | null, newPath: string | null) => Promise<GitFileDiff>;
}>();
const emit = defineEmits<{
  (e: "update:expanded", expanded: boolean): void;
  (e: "comment", target: TourCommentTarget): void;
  (e: "line-comment", target: TourCommentTarget): void;
  // Fired just before the chunk changes height on its own (mode switch), so
  // the parent can drop any pending navigation that would fight the reflow.
  (e: "layout-change"): void;
}>();

const DIFF_THEMES: ThemesType = { dark: "github-dark", light: "github-light" };
const MAX_CLICK_MOVEMENT_SQUARED = 25;
// Injected into the diff's shadow root so a patch-only diff's "N unmodified
// lines" separators look clickable; handleSeparatorClick makes them open the
// full file. @pierre/diffs' own expanders (its loadDiffFiles option) are
// deliberately not used: they hydrate the chunk's sub-patch in place and treat
// everything outside its hunks as unchanged, so the commit's other hunks in
// the same file would render as misaligned context.
const SEPARATOR_CSS =
  "[data-separator=line-info] [data-separator-content]:hover{cursor:pointer;text-decoration:underline}";
const diffHostEl = ref<HTMLElement | null>(null);
const nearViewport = useNearViewport(diffHostEl);
let pointerDownPos: { x: number; y: number } | null = null;

const patchInfo = computed(() => analyzeTourPatch(props.entry.patch));
const paths = computed(() => ({
  old: patchInfo.value.oldPath,
  new: patchInfo.value.newPath,
  newFile: patchInfo.value.newFile,
  deletedFile: patchInfo.value.deletedFile,
  label: patchInfo.value.fileLabel,
}));
const fileLabel = computed(() => patchInfo.value.fileLabel);
const hunkRanges = computed(() => patchInfo.value.hunkRanges);
const chunkLabel = computed(() => patchInfo.value.label);
const chunkStats = computed(() => ({
  additions: patchInfo.value.additions,
  deletions: patchInfo.value.deletions,
}));
const isHunk = computed(() => patchInfo.value.isHunk);
const isBinary = computed(() => patchInfo.value.isBinary);

const patchDiff = computed<FileDiffMetadata | null>(() => {
  if (!props.expanded || !nearViewport.value || !isHunk.value) return null;
  try {
    return getSingularPatch(props.entry.patch);
  } catch (error) {
    console.warn("Commit tour diff parse error:", error);
    return null;
  }
});

// Full-file mode swaps the chunk's patch for a diff of the whole file at this
// commit, rendered with every unchanged line visible. That is the same view
// the Files tab shows, so it includes any other hunks the commit made to this
// file, not just this chunk's.
const fullFile = ref(false);
// shallowRef: the diff is posted to the highlighter worker, and a deep
// reactive proxy cannot be structured-cloned.
const fullFileDiff = shallowRef<FileDiffMetadata | null>(null);
const fileContents = shallowRef<GitFileDiff | null>(null);
const fileLoading = ref(false);
const fileError = ref<string | null>(null);
let loadGeneration = 0;
let pendingAnchor: ChangeLineAnchor | null = null;

const canShowFullFile = computed(
  () =>
    props.loadFile !== undefined &&
    isHunk.value &&
    !isBinary.value &&
    !paths.value.newFile &&
    !paths.value.deletedFile &&
    (paths.value.old !== null || paths.value.new !== null),
);
const showingFullFile = computed(() => fullFile.value && fullFileDiff.value !== null);
const fileDiff = computed<FileDiffMetadata | null>(() =>
  showingFullFile.value && patchDiff.value ? fullFileDiff.value : patchDiff.value,
);

function resetFullFile() {
  loadGeneration++;
  fullFile.value = false;
  fullFileDiff.value = null;
  fileContents.value = null;
  fileLoading.value = false;
  fileError.value = null;
  pendingAnchor = null;
}

// The parent keys chunks by position, so a new tour can hand this instance a
// different entry; file contents belong to the old one.
watch(() => props.entry, resetFullFile);
// Let an in-flight load fall through to its generation check on unmount.
onBeforeUnmount(() => {
  loadGeneration++;
});

function findScrollParent(start: HTMLElement | null): HTMLElement | null {
  for (let node = start?.parentElement ?? null; node; node = node.parentElement) {
    const overflowY = getComputedStyle(node).overflowY;
    if (overflowY === "auto" || overflowY === "scroll") return node;
  }
  return null;
}

function diffShadowRoot(): ShadowRoot | null {
  return diffHostEl.value?.querySelector(DIFFS_TAG_NAME)?.shadowRoot ?? null;
}

// Switching modes inserts or removes context above the change, which would
// otherwise shove it off screen. Note where a changed line sits now so
// handlePostRender can put it back there.
function rememberAnchor() {
  pendingAnchor = null;
  const root = diffShadowRoot();
  const scroller = findScrollParent(diffHostEl.value);
  if (!root || !scroller) return;
  const viewport = scroller.getBoundingClientRect();
  const rows: ChangeRowGeometry[] = [];
  for (const row of root.querySelectorAll<HTMLElement>('[data-line][data-line-type^="change-"]')) {
    const rect = row.getBoundingClientRect();
    rows.push({
      type: row.dataset.lineType ?? "",
      line: row.dataset.line ?? "",
      top: rect.top,
      bottom: rect.bottom,
    });
  }
  pendingAnchor = pickChangeAnchor(rows, viewport.top, viewport.bottom);
}

function handlePostRender(node: HTMLElement, _instance: unknown, phase: PostRenderPhase) {
  // FileDiff also emits on cleanUp(), for the outgoing instance whose
  // container has already left the document; only the live render counts.
  if (!pendingAnchor || phase === "unmount" || node.parentElement !== diffHostEl.value) return;
  const anchor = pendingAnchor;
  pendingAnchor = null;
  const scroller = findScrollParent(diffHostEl.value);
  const match = node.shadowRoot?.querySelector<HTMLElement>(
    `[data-line="${anchor.line}"][data-line-type="${anchor.type}"]`,
  );
  if (!scroller || !match) return;
  scroller.scrollTop += match.getBoundingClientRect().top - anchor.top;
}

function switchMode(full: boolean) {
  emit("layout-change");
  rememberAnchor();
  fullFile.value = full;
}

async function toggleFullFile() {
  if (fullFile.value) {
    switchMode(false);
    return;
  }
  if (fileLoading.value || !props.loadFile) return;
  fileError.value = null;
  if (!fullFileDiff.value) {
    const generation = ++loadGeneration;
    fileLoading.value = true;
    try {
      const contents = await props.loadFile(paths.value.old, paths.value.new);
      if (generation !== loadGeneration) return;
      if (!patchFitsContents(hunkRanges.value, contents)) {
        throw new Error("the file at this commit does not contain this change");
      }
      const names = fullFileNames({
        oldPath: paths.value.old,
        newPath: paths.value.new,
        fileLabel: fileLabel.value,
      });
      fullFileDiff.value = parseDiffFromFile(
        { name: names.old, contents: contents.oldContent },
        { name: names.new, contents: contents.newContent },
      );
      fileContents.value = contents;
    } catch (error) {
      if (generation !== loadGeneration) return;
      fileError.value = error instanceof Error ? error.message : String(error);
      return;
    } finally {
      if (generation === loadGeneration) fileLoading.value = false;
    }
  }
  switchMode(true);
}

// A patch-only diff renders its collapsed context as inert separators;
// clicking one is the natural way to ask for that context.
function handleSeparatorClick(event: MouseEvent) {
  if (!canShowFullFile.value || fullFile.value || fileLoading.value) return;
  const onSeparator = event
    .composedPath()
    .some((node) => node instanceof HTMLElement && node.hasAttribute("data-separator"));
  if (onSeparator) void toggleFullFile();
}

function rememberPointerDown(event: PointerEvent) {
  if (!event.isPrimary || event.button !== 0) {
    pointerDownPos = null;
    return;
  }
  pointerDownPos = { x: event.clientX, y: event.clientY };
}

function isPlainLineClick(event: PointerEvent): boolean {
  const start = pointerDownPos;
  pointerDownPos = null;
  if (start) {
    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    if (dx * dx + dy * dy > MAX_CLICK_MOVEMENT_SQUARED) return false;
  }
  return window.getSelection()?.isCollapsed !== false;
}

function openChunkComment() {
  const hunk = hunkRanges.value[0]?.line || "file change";
  emit("comment", {
    where: `${fileLabel.value} chunk`,
    reference: `${fileLabel.value} (${hunk})`,
  });
}

function openLineComment(side: TourDiffSide, lineNumber: number) {
  const selectedText =
    showingFullFile.value && fileContents.value
      ? fileLineText(fileContents.value, side, lineNumber)
      : patchLineText(props.entry.patch, side, lineNumber);
  if (selectedText === null) return;
  const path = (side === "deletions" ? paths.value.old : paths.value.new) || fileLabel.value;
  const sideLabel = side === "deletions" ? "old" : "new";
  emit("line-comment", {
    where: `${path} line ${lineNumber} (${sideLabel})`,
    reference: `${path}:${lineNumber}`,
    selectedText,
    quoteCode: true,
  });
}

const diffOptions = computed<FileDiffOptions<undefined, undefined>>(() => ({
  diffStyle:
    props.sideBySide && !paths.value.newFile && !paths.value.deletedFile ? "split" : "unified",
  theme: DIFF_THEMES,
  themeType: props.themeType,
  disableFileHeader: true,
  overflow: props.overflow,
  expandUnchanged: showingFullFile.value,
  unsafeCSS: canShowFullFile.value ? SEPARATOR_CSS : undefined,
  onPostRender: handlePostRender,
  lineHoverHighlight: "line",
  onLineClick: ({ annotationSide, lineNumber, event }) => {
    if (isPlainLineClick(event)) openLineComment(annotationSide, lineNumber);
  },
  onLineNumberClick: ({ annotationSide, lineNumber }) => {
    pointerDownPos = null;
    openLineComment(annotationSide, lineNumber);
  },
}));

const placeholderHeight = computed(
  () => `${Math.min(Math.max(props.entry.patch.split("\n").length, 4) * 18, 1600)}px`,
);
const diffError = computed(
  () => props.expanded && nearViewport.value && isHunk.value && fileDiff.value == null,
);

const { rendered } = useFileDiffInstance(diffHostEl, () => {
  if (!fileDiff.value) return null;
  return { fileDiff: fileDiff.value, options: diffOptions.value };
});
</script>

<style scoped>
.commit-tour-chunk {
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: 0.5rem;
  background: var(--bg-base);
}

.commit-tour-chunk-header {
  min-height: 2.5rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--bg-secondary);
  color: var(--text-primary);
}

.commit-tour-chunk-toggle {
  min-width: 0;
  flex: 1;
  align-self: stretch;
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: -0.5rem 0 -0.5rem -0.75rem;
  padding: 0.5rem 0.75rem;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.commit-tour-chunk-toggle:hover {
  background: var(--bg-tertiary);
}

.commit-tour-full-file-btn {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.75rem;
  height: 1.75rem;
  padding: 0;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
}

.commit-tour-full-file-btn:hover:not([aria-disabled="true"]) {
  background: var(--bg-tertiary);
  color: var(--text-primary);
}

.commit-tour-full-file-btn.active {
  color: var(--primary);
}

.commit-tour-full-file-btn[aria-disabled="true"] {
  cursor: progress;
}

.commit-tour-full-file-btn .spinner {
  width: 0.875rem;
  height: 0.875rem;
}

.commit-tour-file-error {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--border-color);
  color: var(--red-text, #dc2626);
  font-size: 0.8125rem;
}

.commit-tour-comment-btn {
  flex: 0 0 auto;
  width: 1.75rem;
  height: 1.75rem;
  padding: 0;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  opacity: 0;
  cursor: pointer;
}

.commit-tour-chunk-header:hover .commit-tour-comment-btn,
.commit-tour-comment-btn:focus-visible {
  opacity: 1;
}

.commit-tour-comment-btn:hover {
  background: var(--bg-tertiary);
}

.commit-tour-chunk-header code {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono, monospace);
  font-size: 0.8125rem;
}

.commit-tour-chunk-stats {
  display: inline-flex;
  gap: 0.375rem;
  margin-left: auto;
  flex: 0 0 auto;
  font-family: var(--font-mono, monospace);
  font-size: 0.75rem;
  font-weight: 600;
}

.commit-tour-additions {
  color: var(--green-text, #16a34a);
}

.commit-tour-deletions {
  color: var(--red-text, #dc2626);
}

.commit-tour-chunk-body {
  min-width: 0;
}

.commit-tour-comment {
  padding: 0.875rem 1rem;
  border-top: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
  background: var(--bg-secondary);
}

.commit-tour-comment :deep(> :first-child) {
  margin-top: 0;
}

.commit-tour-comment :deep(> :last-child) {
  margin-bottom: 0;
}

.commit-tour-diff-host,
.commit-tour-diff-host :deep(diffs-container) {
  display: block;
  min-width: 0;
}

.commit-tour-diff-host :deep(diffs-container) {
  contain: content;
}

.commit-tour-placeholder,
.commit-tour-raw-diff {
  margin: 0;
  padding: 0.875rem 1rem;
  color: var(--text-secondary);
  background: var(--bg-base);
  font-size: 0.75rem;
}

.commit-tour-placeholder > span {
  display: block;
  margin-bottom: 0.5rem;
  font-weight: 600;
}

.commit-tour-placeholder pre,
.commit-tour-raw-diff {
  margin: 0;
  overflow-x: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--font-mono, monospace);
}

@media (max-width: 767px) {
  .commit-tour-chunk {
    border-right: 0;
    border-left: 0;
    border-radius: 0;
  }

  .commit-tour-chunk-header {
    padding-right: 0.625rem;
    padding-left: 0.625rem;
  }

  .commit-tour-comment-btn {
    opacity: 1;
  }

  .commit-tour-chunk-toggle {
    margin-left: -0.625rem;
    padding-left: 0.625rem;
  }

  .commit-tour-comment,
  .commit-tour-placeholder,
  .commit-tour-raw-diff {
    padding: 0.75rem;
  }
}
</style>
