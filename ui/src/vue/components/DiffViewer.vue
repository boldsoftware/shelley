<!-- Vue port of components/DiffViewer.tsx. Monaco diff modal with a file tree
     (DiffFileTree.vue), commit/range selection (CommitPicker.vue +
     RangeToggle.vue), comments, edit/auto-save, and vim (useMonacoVim +
     VimToggle.vue). PRESERVES EXACTLY the selectors the
     diff-viewer-find.spec.ts e2e depends on: .diff-viewer-overlay,
     .diff-viewer-editor, select.diff-viewer-select, .monaco-editor,
     .find-widget.visible, the "Find" textbox, and the Ctrl+F-opens-find /
     Escape-closes-find-not-viewer behavior, plus all diff-viewer-* /
     diff-tree-* class, role, aria-label, and visible-text contracts.

     Emits (React callback props):
       close             -> onClose
       comment-text-change -> onCommentTextChange(text)
       cwd-change        -> onCwdChange(cwd) -->
<template>
  <div v-if="isOpen" class="diff-viewer-overlay">
    <div class="diff-viewer-container">
      <!-- Toast notifications -->
      <div
        v-if="saveStatus !== 'idle'"
        :class="`diff-viewer-toast diff-viewer-toast-${saveStatus}`"
      >
        <template v-if="saveStatus === 'saving'">💾 Saving...</template>
        <template v-else-if="saveStatus === 'saved'">✅ Saved</template>
        <template v-else-if="saveStatus === 'error'">❌ Error saving</template>
      </div>
      <div
        v-if="amendStatus !== 'idle'"
        :class="`diff-viewer-toast diff-viewer-toast-${amendStatus}`"
      >
        <template v-if="amendStatus === 'saving'">💾 Amending...</template>
        <template v-else-if="amendStatus === 'saved'">✅ Amended</template>
        <template v-else-if="amendStatus === 'error'">❌ Error amending</template>
      </div>
      <div v-if="showKeyboardHint" class="diff-viewer-toast diff-viewer-toast-hint">
        ⌨️ Use . , for next/prev change, &lt; &gt; for files
      </div>

      <!-- Mobile header -->
      <div v-if="isMobile" class="diff-viewer-header diff-viewer-header-mobile">
        <div class="diff-viewer-mobile-selectors">
          <CommitPicker
            :diffs="diffs"
            :selected-diff="selectedDiff"
            :selected-to="selectedTo"
            :is-mobile="isMobile"
            @change="onCommitChange"
          />
          <div class="diff-viewer-file-selector-wrapper">
            <select
              :value="selectedFile || ''"
              class="diff-viewer-select"
              :disabled="files.length === 0"
              @change="selectedFile = ($event.target as HTMLSelectElement).value || null"
            >
              <option value="">{{ files.length === 0 ? "No files" : "Choose file..." }}</option>
              <option v-for="file in files" :key="file.path" :value="file.path">
                {{ fileOptionLabel(file) }}
              </option>
            </select>
            <span v-if="fileIndexIndicator" class="diff-viewer-file-index">{{
              fileIndexIndicator
            }}</span>
          </div>
        </div>
        <button
          class="diff-viewer-dir-btn"
          :title="`Git directory: ${cwd}\nClick to change`"
          @click="showDirPicker = true"
        >
          <span v-html="DIR_ICON" />
        </button>
        <button class="diff-viewer-close" title="Close (Esc)" @click="emit('close')">×</button>
      </div>

      <!-- Desktop header -->
      <div v-else class="diff-viewer-header">
        <div class="diff-viewer-header-row">
          <button
            v-if="layout === 'sidebar'"
            type="button"
            class="btn-icon diff-viewer-collapse-btn"
            aria-label="Hide sidebar"
            title="Hide sidebar"
            @click="setLayout('header')"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="20" height="20">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M13 5l7 7-7 7M5 5l7 7-7 7"
              />
            </svg>
          </button>
          <button
            v-else
            type="button"
            class="btn-icon diff-viewer-expand-btn"
            aria-label="Show sidebar"
            title="Show sidebar"
            @click="setLayout('sidebar')"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="20" height="20">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M11 19l-7-7 7-7m8 14l-7-7 7-7"
              />
            </svg>
          </button>

          <div v-if="layout === 'header'" class="diff-viewer-selectors-row">
            <div class="diff-viewer-selector-group">
              <label class="diff-viewer-selector-label">Commits</label>
              <CommitPicker
                :diffs="diffs"
                :selected-diff="selectedDiff"
                :selected-to="selectedTo"
                :is-mobile="isMobile"
                @change="onCommitChange"
              />
            </div>
            <div class="diff-viewer-selector-group">
              <label class="diff-viewer-selector-label">Commit messages and changed files</label>
              <div class="diff-viewer-file-selector-wrapper">
                <select
                  :value="selectedFile || ''"
                  class="diff-viewer-select"
                  :disabled="files.length === 0"
                  @change="selectedFile = ($event.target as HTMLSelectElement).value || null"
                >
                  <option value="">{{ files.length === 0 ? "No files" : "Choose file..." }}</option>
                  <option v-for="file in files" :key="file.path" :value="file.path">
                    {{ fileOptionLabel(file) }}
                  </option>
                </select>
                <span v-if="fileIndexIndicator" class="diff-viewer-file-index">{{
                  fileIndexIndicator
                }}</span>
              </div>
            </div>
          </div>
          <div v-else class="diff-viewer-header-title" :title="currentTitleTooltip ?? undefined">
            {{ currentTitleText ?? "\u00a0" }}
          </div>

          <div class="diff-viewer-controls-row">
            <div class="diff-viewer-nav-buttons">
              <button
                class="diff-viewer-nav-btn"
                :disabled="!hasPrevFile"
                title="Previous file (<)"
                @click="goToPreviousFile"
              >
                <span v-html="PREV_FILE_ICON" />
              </button>
              <button
                class="diff-viewer-nav-btn"
                :disabled="!fileDiff"
                title="Previous change (,)"
                @click="goToPreviousChange"
              >
                <span v-html="PREV_CHANGE_ICON" />
              </button>
              <button
                class="diff-viewer-nav-btn"
                :disabled="!fileDiff"
                title="Next change (.)"
                @click="goToNextChange"
              >
                <span v-html="NEXT_CHANGE_ICON" />
              </button>
              <button
                class="diff-viewer-nav-btn"
                :disabled="!hasNextFile"
                title="Next file (>)"
                @click="goToNextFile()"
              >
                <span v-html="NEXT_FILE_ICON" />
              </button>
            </div>
            <div class="diff-viewer-mode-toggle">
              <button
                :class="`diff-viewer-mode-btn ${mode === 'comment' ? 'active' : ''}`"
                title="Comment mode"
                @click="mode = 'comment'"
              >
                💬
              </button>
              <button
                :class="`diff-viewer-mode-btn ${mode === 'edit' ? 'active' : ''}`"
                title="Edit mode"
                @click="mode = 'edit'"
              >
                ✏️
              </button>
            </div>
            <VimToggle :enabled="vimEnabled" @change="setVimEnabled" />
            <button
              class="diff-viewer-dir-btn"
              :title="`Git directory: ${cwd}\nClick to change`"
              @click="showDirPicker = true"
            >
              <span v-html="DIR_ICON" />
            </button>
            <button class="diff-viewer-close" title="Close (Esc)" @click="emit('close')">×</button>
          </div>
        </div>
      </div>

      <!-- Error banner -->
      <div v-if="error" class="diff-viewer-error">{{ error }}</div>

      <!-- Main content -->
      <div
        :class="`diff-viewer-content${!isMobile && layout === 'sidebar' ? ' diff-viewer-content-sidebar' : ''}`"
      >
        <aside v-if="!isMobile && layout === 'sidebar'" class="diff-viewer-sidebar">
          <div class="diff-viewer-sidebar-section diff-viewer-sidebar-commits">
            <div class="diff-viewer-sidebar-label"><span>Commits</span></div>
            <div class="diff-viewer-sidebar-range">
              <RangeToggle
                :selected-diff="selectedDiff"
                :selected-to="selectedTo"
                @change="onCommitChange"
              />
            </div>
            <div class="diff-viewer-sidebar-commits-scroll">
              <ul class="diff-viewer-commit-list" role="listbox" aria-label="Commits">
                <li v-if="sidebarCommits.length === 0" class="diff-viewer-file-list-empty">
                  No commits
                </li>
                <li v-for="(d, idx) in sidebarCommits" :key="d.id">
                  <button
                    type="button"
                    :class="commitItemClass(d, idx)"
                    :title="d.id === 'working' ? 'Working changes' : `${d.message}\n${d.id}`"
                    role="option"
                    :aria-selected="selectedDiff === d.id"
                    @click="onCommitListClick(d)"
                  >
                    <div class="diff-viewer-commit-list-line1">
                      <span class="diff-viewer-commit-list-subject">{{
                        d.id === "working" ? "Working Changes" : d.message
                      }}</span>
                    </div>
                    <div
                      v-if="d.id !== 'working' && ((d.refs ?? []).length > 0 || d.isMergeBase)"
                      class="diff-viewer-commit-list-refs"
                    >
                      <span v-for="ref in d.refs ?? []" :key="ref" :class="commitRefClass(ref)">{{
                        ref
                      }}</span>
                      <span
                        v-if="d.isMergeBase && !(d.refs ?? []).some((r) => r.includes('/'))"
                        class="diff-viewer-commit-list-ref mergebase"
                        title="Merge-base with @{upstream}"
                        >merge-base</span
                      >
                    </div>
                  </button>
                </li>
              </ul>
            </div>
          </div>
          <div class="diff-viewer-sidebar-section diff-viewer-sidebar-files">
            <div class="diff-viewer-sidebar-label">
              <span>Commit Messages and Files</span>
              <span v-if="fileIndexIndicator" class="diff-viewer-file-index">{{
                fileIndexIndicator
              }}</span>
            </div>
            <div class="diff-viewer-sidebar-files-scroll">
              <div class="diff-viewer-file-list" aria-label="Files">
                <div v-if="files.length === 0" class="diff-viewer-file-list-empty">No files</div>
                <div v-else class="diff-viewer-file-tree-wrap">
                  <DiffFileTree
                    :entries="treeEntries"
                    :selected-real-path="selectedFile"
                    @select="(p: string) => (selectedFile = p)"
                  />
                </div>
              </div>
            </div>
          </div>
        </aside>
        <div class="diff-viewer-main">
          <div v-if="loading && !fileDiff" class="diff-viewer-loading">
            <div class="spinner"></div>
            <span>Loading...</span>
          </div>
          <div v-if="!loading && !monacoLoaded && !error" class="diff-viewer-loading">
            <div class="spinner"></div>
            <span>Loading editor...</span>
          </div>
          <div v-if="!loading && monacoLoaded && !fileDiff && !error" class="diff-viewer-empty">
            <p>Select a diff and file to view changes.</p>
            <p class="diff-viewer-hint">Click on line numbers to add comments.</p>
          </div>
          <div
            ref="editorContainerRef"
            class="diff-viewer-editor"
            :style="{ display: fileDiff && monacoLoaded ? 'block' : 'none' }"
          />
          <div
            v-if="!isMobile && vimEnabled && fileDiff && monacoLoaded"
            ref="vimStatusRef"
            class="monaco-vim-status"
          />
        </div>
      </div>

      <!-- Mobile floating nav buttons -->
      <div v-if="isMobile" class="diff-viewer-mobile-nav">
        <button
          :class="`diff-viewer-mobile-nav-btn diff-viewer-mobile-mode-btn ${mode === 'comment' ? 'active' : ''}`"
          :title="mode === 'comment' ? 'Comment mode (tap to switch)' : 'Edit mode (tap to switch)'"
          @click="mode = mode === 'comment' ? 'edit' : 'comment'"
        >
          {{ mode === "comment" ? "💬" : "✏️" }}
        </button>
        <button
          class="diff-viewer-mobile-nav-btn"
          :disabled="!hasPrevFile"
          title="Previous file (<)"
          @click="goToPreviousFile"
        >
          <span v-html="PREV_FILE_ICON" />
        </button>
        <button
          class="diff-viewer-mobile-nav-btn"
          :disabled="!fileDiff"
          title="Previous change (,)"
          @click="goToPreviousChange"
        >
          <span v-html="PREV_CHANGE_ICON" />
        </button>
        <button
          class="diff-viewer-mobile-nav-btn"
          :disabled="!fileDiff"
          title="Next change (.)"
          @click="goToNextChange"
        >
          <span v-html="NEXT_CHANGE_ICON" />
        </button>
        <button
          class="diff-viewer-mobile-nav-btn"
          :disabled="!hasNextFile"
          title="Next file (>)"
          @click="goToNextFile()"
        >
          <span v-html="NEXT_FILE_ICON" />
        </button>
      </div>

      <!-- Comment dialog -->
      <div v-if="showCommentDialog" class="diff-viewer-comment-dialog">
        <h4>
          Add Comment (Line{{
            showCommentDialog.startLine !== showCommentDialog.endLine
              ? `s ${showCommentDialog.startLine}-${showCommentDialog.endLine}`
              : ` ${showCommentDialog.line}`
          }}, {{ showCommentDialog.side === "left" ? "old" : "new" }})
        </h4>
        <pre v-if="showCommentDialog.selectedText" class="diff-viewer-selected-text">{{
          showCommentDialog.selectedText
        }}</pre>
        <textarea
          ref="commentInputRef"
          v-model="commentText"
          placeholder="Enter your comment..."
          class="diff-viewer-comment-input"
        />
        <div class="diff-viewer-comment-actions">
          <button
            class="diff-viewer-btn diff-viewer-btn-secondary"
            @click="showCommentDialog = null"
          >
            Cancel
          </button>
          <button
            class="diff-viewer-btn diff-viewer-btn-primary"
            :disabled="!commentText.trim()"
            @click="handleAddComment"
          >
            Add Comment
          </button>
        </div>
      </div>
    </div>

    <!-- Directory picker -->
    <DirectoryPickerModal
      :is-open="showDirPicker"
      :initial-path="cwd"
      folders-only
      @close="showDirPicker = false"
      @select="onDirSelect"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from "vue";
import type * as Monaco from "monaco-editor";
import { api } from "../../services/api";
import { loadMonaco } from "../../services/monaco";
import { isDarkModeActive } from "../../services/theme";
import { useVimEnabled, useMonacoVim } from "../composables/monacoVim";
import VimToggle from "./VimToggle.vue";
import CommitPicker from "./CommitPicker.vue";
import RangeToggle from "./RangeToggle.vue";
import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import DiffFileTree from "./DiffFileTree.vue";
import { COMMIT_MESSAGES_DIR, treeRealPathOrder, type DiffFileTreeEntry } from "./diffFileTree";
import type { GitDiffInfo, GitFileInfo, GitFileDiff, GitCommitMessage } from "../../types";

const props = defineProps<{
  cwd: string;
  isOpen: boolean;
  initialCommit?: string;
}>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "comment-text-change", text: string): void;
  (e: "cwd-change", cwd: string): void;
}>();

type ViewMode = "comment" | "edit";

const COMMIT_MSG_PREFIX = "commit-message:";
const MOBILE_LINE_DECORATIONS_WIDTH = 8;
const DESKTOP_LINE_DECORATIONS_WIDTH = 10;
const MOBILE_SCROLLBAR_SIZE = 8;
const DESKTOP_VERTICAL_SCROLLBAR_SIZE = 14;
const DESKTOP_HORIZONTAL_SCROLLBAR_SIZE = 10;
const MOBILE_OVERVIEW_RULER_LANES = 1;
const DESKTOP_OVERVIEW_RULER_LANES = 3;

function isCommitMessageFile(path: string): boolean {
  return path.startsWith(COMMIT_MSG_PREFIX);
}
function commitHashFromPath(path: string): string {
  return path.slice(COMMIT_MSG_PREFIX.length);
}
function formatCommitMessage(msg: GitCommitMessage): string {
  let text = msg.subject;
  if (msg.body) text += "\n\n" + msg.body;
  return text;
}
function truncateWithEllipsis(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return text.slice(0, Math.max(0, maxLength - 3)) + "...";
}

// SVG icon markup (rendered via v-html into <span>). Mirrors the React icon
// components: two-chevron file nav + single-chevron change nav + dir folder.
const PREV_FILE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M8 2L2 8l6 6V2z" /><path d="M14 2L8 8l6 6V2z" /></svg>';
const PREV_CHANGE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M10 2L4 8l6 6V2z" /></svg>';
const NEXT_CHANGE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M6 2l6 6-6 6V2z" /></svg>';
const NEXT_FILE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M2 2l6 6-6 6V2z" /><path d="M8 2l6 6-6 6V2z" /></svg>';
const DIR_ICON =
  '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>';

// --- Reactive state (mirrors React useState) ---
const diffs = ref<GitDiffInfo[]>([]);
const gitRoot = ref<string | null>(null);
const showDirPicker = ref(false);
const selectedDiff = ref<string | null>(null);
const selectedTo = ref<"working" | "self">("working");
const files = ref<GitFileInfo[]>([]);
const selectedFile = ref<string | null>(null);
const fileDiff = ref<GitFileDiff | null>(null);
const loading = ref(false);
const error = ref<string | null>(null);
const monacoLoaded = ref(false);
const currentChangeIndex = ref(-1);
const saveStatus = ref<"idle" | "saving" | "saved" | "error">("idle");
const showCommentDialog = ref<{
  line: number;
  side: "left" | "right";
  selectedText?: string;
  startLine?: number;
  endLine?: number;
} | null>(null);
const commentText = ref("");
const mode = ref<ViewMode>("comment");
const commitMessages = ref<GitCommitMessage[]>([]);
const amendStatus = ref<"idle" | "saving" | "saved" | "error">("idle");
const showKeyboardHint = ref(false);
const isMobile = ref(window.innerWidth < 768);
const [vimEnabledRef, setVimEnabledFn] = useVimEnabled();
const vimEnabled = vimEnabledRef;
function setVimEnabled(v: boolean) {
  setVimEnabledFn(v);
}

const layout = ref<"header" | "sidebar">(
  (() => {
    try {
      const v = localStorage.getItem("diff-viewer-layout");
      return v === "sidebar" ? "sidebar" : "header";
    } catch {
      return "header";
    }
  })(),
);
function setLayout(v: "header" | "sidebar") {
  layout.value = v;
  try {
    localStorage.setItem("diff-viewer-layout", v);
  } catch {
    // ignore
  }
}

// The vim adapter attaches to the modified (right-hand) code editor.
// Must be shallowRef, not ref: a deep reactive proxy over Monaco's internal
// object graph makes vim mode peg the main thread and hang the page (vim
// drives the editor on every keystroke). shallowRef tracks the create/dispose
// swap without proxying internals.
const modifiedEditor = shallowRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
const vimStatusRef = ref<HTMLDivElement | null>(null);

// --- Non-reactive refs (mirror React useRef) ---
let monacoMod: typeof Monaco | null = null;
const editorContainerRef = ref<HTMLDivElement | null>(null);
const commentInputRef = ref<HTMLTextAreaElement | null>(null);
let diffEditor: Monaco.editor.IStandaloneDiffEditor | null = null;
let saveTimeout: number | null = null;
let amendTimeout: number | null = null;
let hasShownKeyboardHint = false;
let keyboardHintTimer: number | null = null;
const navOrderRef = ref<string[]>([]);
let isMobileVal = isMobile.value;
let modeVal: ViewMode = mode.value;
let cwdVal = props.cwd;
let commitMessagesVal: GitCommitMessage[] = [];
let currentFileIsHeadCommit = false;
let hoverDecorations: string[] = [];
let touchScrolled = false;
let touchStartPos: { x: number; y: number } | null = null;
let scheduleSaveFn: (() => void) | null = null;

// Keep mirror values in sync.
watch(commitMessages, (v) => (commitMessagesVal = v), { immediate: true });
watch(
  () => props.cwd,
  (v) => (cwdVal = v),
  { immediate: true },
);

// vim adapter — only in edit mode (read-only comment mode would double-handle).
useMonacoVim(
  () => modifiedEditor.value,
  () => vimStatusRef.value,
  () => !isMobile.value && vimEnabled.value && mode.value === "edit",
  () => emit("close"),
);

// Keep modeRef in sync + update editor readOnly when mode changes.
watch([mode, selectedFile, selectedDiff, selectedTo], () => {
  modeVal = mode.value;
  if (diffEditor && selectedFile.value && !isCommitMessageFile(selectedFile.value)) {
    const isWorkingView = selectedDiff.value === "working" || selectedTo.value === "working";
    const readOnly = mode.value === "comment" || !isWorkingView;
    diffEditor.updateOptions({ readOnly });
    diffEditor.getModifiedEditor().updateOptions({ readOnly });
  }
});

// Track viewport size.
function handleResize() {
  isMobile.value = window.innerWidth < 768;
}

// Focus comment input when dialog opens.
watch(showCommentDialog, (v) => {
  if (v) {
    setTimeout(() => commentInputRef.value?.focus(), 50);
  }
});

// Load Monaco when viewer opens.
watch(
  [() => props.isOpen, monacoLoaded],
  () => {
    if (props.isOpen && !monacoLoaded.value) {
      loadMonaco()
        .then((monaco) => {
          monacoMod = monaco;
          monacoLoaded.value = true;
        })
        .catch((err) => {
          console.error("Failed to load Monaco:", err);
          error.value = "Failed to load diff editor";
        });
    }
  },
  { immediate: true },
);

// Show keyboard hint toast on first open (desktop only).
watch([() => props.isOpen, isMobile, fileDiff], () => {
  if (props.isOpen && !isMobile.value && !hasShownKeyboardHint && fileDiff.value) {
    hasShownKeyboardHint = true;
    showKeyboardHint.value = true;
  }
});

// Auto-hide keyboard hint after 6 seconds.
watch(showKeyboardHint, (v) => {
  if (keyboardHintTimer) {
    clearTimeout(keyboardHintTimer);
    keyboardHintTimer = null;
  }
  if (v) {
    keyboardHintTimer = window.setTimeout(() => (showKeyboardHint.value = false), 6000);
  }
});

// Load diffs when viewer opens; reset state when it closes.
watch(
  [() => props.isOpen, () => props.cwd, () => props.initialCommit],
  () => {
    if (props.isOpen && props.cwd) {
      loadDiffs();
    } else if (!props.isOpen) {
      fileDiff.value = null;
      selectedFile.value = null;
      files.value = [];
      selectedDiff.value = null;
      selectedTo.value = "working";
      diffs.value = [];
      error.value = null;
      showCommentDialog.value = null;
      commentText.value = "";
      commitMessages.value = [];
      amendStatus.value = "idle";
      if (amendTimeout) {
        clearTimeout(amendTimeout);
        amendTimeout = null;
      }
    }
  },
  { immediate: true },
);

// Load files when diff (or its `to` bound) is selected.
watch([selectedDiff, selectedTo, () => props.cwd], () => {
  if (selectedDiff.value && props.cwd) {
    loadFiles(selectedDiff.value);
  }
});

// Load file diff when file is selected.
watch([selectedDiff, selectedFile, selectedTo, () => props.cwd], () => {
  if (selectedDiff.value && selectedFile.value && props.cwd) {
    loadFileDiff(selectedDiff.value, selectedFile.value);
    currentChangeIndex.value = -1;
  }
});

// --- Create the Monaco diff editor ONCE per isOpen+monacoLoaded change. ---
// Recreating on file switch / viewport flip leaks monaco keybinding
// contributions, so model swaps + option updates happen in separate watchers.
function createEditor() {
  if (!props.isOpen || !monacoLoaded.value || !editorContainerRef.value || !monacoMod) return;
  if (diffEditor) return;
  const monaco = monacoMod;
  const initMobile = isMobileVal;
  diffEditor = monaco.editor.createDiffEditor(editorContainerRef.value, {
    theme: isDarkModeActive() ? "vs-dark" : "vs",
    readOnly: true,
    originalEditable: false,
    automaticLayout: true,
    renderSideBySide: !initMobile,
    enableSplitViewResizing: true,
    renderIndicators: true,
    renderMarginRevertIcon: false,
    lineNumbers: initMobile ? "off" : "on",
    minimap: { enabled: false },
    scrollBeyondLastLine: true,
    wordWrap: "on",
    glyphMargin: !initMobile,
    lineDecorationsWidth: initMobile
      ? MOBILE_LINE_DECORATIONS_WIDTH
      : DESKTOP_LINE_DECORATIONS_WIDTH,
    lineNumbersMinChars: initMobile ? 0 : 3,
    scrollbar: {
      verticalScrollbarSize: initMobile ? MOBILE_SCROLLBAR_SIZE : DESKTOP_VERTICAL_SCROLLBAR_SIZE,
      horizontalScrollbarSize: initMobile
        ? MOBILE_SCROLLBAR_SIZE
        : DESKTOP_HORIZONTAL_SCROLLBAR_SIZE,
    },
    overviewRulerLanes: initMobile ? MOBILE_OVERVIEW_RULER_LANES : DESKTOP_OVERVIEW_RULER_LANES,
    quickSuggestions: false,
    suggestOnTriggerCharacters: false,
    lightbulb: { enabled: false },
    codeLens: false,
    contextmenu: false,
    links: false,
    folding: !initMobile,
    padding: initMobile ? { bottom: 80 } : undefined,
  });

  const modEditor = diffEditor.getModifiedEditor();
  modifiedEditor.value = modEditor;

  const openCommentDialog = (lineNumber: number) => {
    const model = modEditor.getModel();
    const selection = modEditor.getSelection();
    let selectedText = "";
    let startLine = lineNumber;
    let endLine = lineNumber;
    if (selection && !selection.isEmpty() && model) {
      selectedText = model.getValueInRange(selection);
      startLine = selection.startLineNumber;
      endLine = selection.endLineNumber;
    } else if (model) {
      selectedText = model.getLineContent(lineNumber) || "";
    }
    showCommentDialog.value = { line: startLine, side: "right", selectedText, startLine, endLine };
  };

  // Desktop: open comment dialog on mousedown.
  modEditor.onMouseDown((e: Monaco.editor.IEditorMouseEvent) => {
    if (isMobileVal) return;
    const isLineClick =
      e.target.type === monaco.editor.MouseTargetType.CONTENT_TEXT ||
      e.target.type === monaco.editor.MouseTargetType.CONTENT_EMPTY;
    if (isLineClick && modeVal === "comment") {
      const position = e.target.position;
      if (position) openCommentDialog(position.lineNumber);
    }
  });

  // Mobile: track tap-without-scroll.
  const editorDom = editorContainerRef.value;
  const onTouchStart = (e: TouchEvent) => {
    touchScrolled = false;
    const t = e.touches[0];
    touchStartPos = { x: t.clientX, y: t.clientY };
  };
  const onTouchMove = (e: TouchEvent) => {
    if (touchScrolled || !touchStartPos) return;
    const t = e.touches[0];
    const dx = t.clientX - touchStartPos.x;
    const dy = t.clientY - touchStartPos.y;
    if (dx * dx + dy * dy > 100) touchScrolled = true;
  };
  const onTouchEnd = () => {
    touchStartPos = null;
  };
  editorDom.addEventListener("touchstart", onTouchStart, { passive: true });
  editorDom.addEventListener("touchmove", onTouchMove, { passive: true });
  editorDom.addEventListener("touchend", onTouchEnd, { passive: true });
  touchCleanup = () => {
    editorDom.removeEventListener("touchstart", onTouchStart);
    editorDom.removeEventListener("touchmove", onTouchMove);
    editorDom.removeEventListener("touchend", onTouchEnd);
  };

  modEditor.onMouseUp((e: Monaco.editor.IEditorMouseEvent) => {
    if (!isMobileVal) return;
    if (modeVal !== "comment") return;
    if (touchScrolled) return;
    const isLineClick =
      e.target.type === monaco.editor.MouseTargetType.CONTENT_TEXT ||
      e.target.type === monaco.editor.MouseTargetType.CONTENT_EMPTY;
    if (isLineClick) {
      const position = e.target.position;
      if (position) openCommentDialog(position.lineNumber);
    }
  });

  // Hover highlighting with comment indicator (comment mode only).
  let lastHoveredLine = -1;
  modEditor.onMouseMove((e: Monaco.editor.IEditorMouseEvent) => {
    if (modeVal !== "comment") {
      if (hoverDecorations.length > 0) {
        hoverDecorations = modEditor.deltaDecorations(hoverDecorations, []);
      }
      return;
    }
    const position = e.target.position;
    const lineNumber = position?.lineNumber ?? -1;
    if (lineNumber === lastHoveredLine) return;
    lastHoveredLine = lineNumber;
    if (lineNumber > 0) {
      hoverDecorations = modEditor.deltaDecorations(hoverDecorations, [
        {
          range: new monaco.Range(lineNumber, 1, lineNumber, 1),
          options: {
            isWholeLine: true,
            className: "diff-viewer-line-hover",
            glyphMarginClassName: "diff-viewer-comment-glyph",
          },
        },
      ]);
    } else {
      hoverDecorations = modEditor.deltaDecorations(hoverDecorations, []);
    }
  });

  modEditor.onMouseLeave(() => {
    lastHoveredLine = -1;
    hoverDecorations = modEditor.deltaDecorations(hoverDecorations, []);
  });

  // Single content change listener; branches on current file context.
  modEditor.onDidChangeModelContent(() => {
    if (currentFileIsHeadCommit) {
      if (amendTimeout) clearTimeout(amendTimeout);
      amendStatus.value = "saving";
      amendTimeout = window.setTimeout(async () => {
        const model = modEditor.getModel();
        if (!model) return;
        const newMessage = model.getValue();
        try {
          await api.amendGitMessage(cwdVal, newMessage);
          amendStatus.value = "saved";
          setTimeout(() => (amendStatus.value = "idle"), 2000);
        } catch {
          amendStatus.value = "error";
          setTimeout(() => (amendStatus.value = "idle"), 3000);
        }
      }, 1500);
    } else {
      scheduleSaveFn?.();
    }
  });
}
let touchCleanup: (() => void) | null = null;

function disposeEditor() {
  if (!diffEditor) return;
  touchCleanup?.();
  touchCleanup = null;
  const model = diffEditor.getModel();
  diffEditor.dispose();
  model?.original.dispose();
  model?.modified.dispose();
  diffEditor = null;
  modifiedEditor.value = null;
}

// Create/dispose the editor when isOpen + monacoLoaded change.
watch(
  [() => props.isOpen, monacoLoaded],
  () => {
    if (props.isOpen && monacoLoaded.value) {
      nextTick(() => createEditor());
    } else {
      disposeEditor();
    }
  },
  { immediate: true, flush: "post" },
);

// Apply mobile-dependent layout options without recreating the editor.
watch(isMobile, (mob) => {
  isMobileVal = mob;
  if (!diffEditor) return;
  diffEditor.updateOptions({
    renderSideBySide: !mob,
    lineNumbers: mob ? "off" : "on",
    glyphMargin: !mob,
    lineDecorationsWidth: mob ? MOBILE_LINE_DECORATIONS_WIDTH : DESKTOP_LINE_DECORATIONS_WIDTH,
    lineNumbersMinChars: mob ? 0 : 3,
    scrollbar: {
      verticalScrollbarSize: mob ? MOBILE_SCROLLBAR_SIZE : DESKTOP_VERTICAL_SCROLLBAR_SIZE,
      horizontalScrollbarSize: mob ? MOBILE_SCROLLBAR_SIZE : DESKTOP_HORIZONTAL_SCROLLBAR_SIZE,
    },
    overviewRulerLanes: mob ? MOBILE_OVERVIEW_RULER_LANES : DESKTOP_OVERVIEW_RULER_LANES,
    folding: !mob,
    padding: mob ? { bottom: 80 } : {},
  });
});

// Swap models into the existing editor when fileDiff changes.
let diffUpdateDisposable: Monaco.IDisposable | null = null;
watch(
  [monacoLoaded, fileDiff],
  () => {
    if (!monacoLoaded.value || !fileDiff.value || !diffEditor || !monacoMod) return;
    const monaco = monacoMod;
    const fd = fileDiff.value;

    const isCommitMsg = isCommitMessageFile(fd.path);
    const commitHash = isCommitMsg ? commitHashFromPath(fd.path) : null;
    const isHeadCommit =
      isCommitMsg && commitMessagesVal.some((m) => m.hash === commitHash && m.isHead);
    currentFileIsHeadCommit = isHeadCommit;

    let language = "plaintext";
    if (!isCommitMsg) {
      const ext = "." + (fd.path.split(".").pop()?.toLowerCase() || "");
      const languages = monaco.languages.getLanguages();
      for (const lang of languages) {
        if (lang.extensions?.includes(ext)) {
          language = lang.id;
          break;
        }
      }
    }

    const timestamp = Date.now();
    const originalUri = monaco.Uri.file(`original-${timestamp}-${fd.path}`);
    const modifiedUri = monaco.Uri.file(`modified-${timestamp}-${fd.path}`);
    const originalModel = monaco.editor.createModel(fd.oldContent, language, originalUri);
    const modifiedModel = monaco.editor.createModel(fd.newContent, language, modifiedUri);

    const prev = diffEditor.getModel();
    diffEditor.setModel({ original: originalModel, modified: modifiedModel });
    prev?.original.dispose();
    prev?.modified.dispose();

    const isWorkingView = selectedDiff.value === "working" || selectedTo.value === "working";
    const readOnly = isCommitMsg ? !isHeadCommit : modeVal === "comment" || !isWorkingView;
    diffEditor.updateOptions({ readOnly });
    diffEditor.getModifiedEditor().updateOptions({ readOnly });

    let hasScrolledToFirstChange = false;
    const scrollToFirstChange = () => {
      if (hasScrolledToFirstChange || !diffEditor) return;
      const changes = diffEditor.getLineChanges();
      if (changes && changes.length > 0) {
        hasScrolledToFirstChange = true;
        const firstChange = changes[0];
        const targetLine = firstChange.modifiedStartLineNumber || 1;
        const editor = diffEditor.getModifiedEditor();
        editor.revealLineInCenter(targetLine);
        editor.setPosition({ lineNumber: targetLine, column: 1 });
        currentChangeIndex.value = 0;
      }
    };
    scrollToFirstChange();
    diffUpdateDisposable?.dispose();
    diffUpdateDisposable = diffEditor.onDidUpdateDiff(scrollToFirstChange);
  },
  { flush: "post" },
);

// --- Data loaders ---
async function loadDiffs() {
  try {
    loading.value = true;
    error.value = null;
    const response = await api.getGitDiffs(props.cwd);
    diffs.value = response.diffs;
    gitRoot.value = response.gitRoot;

    if (props.initialCommit) {
      const matchingDiff = response.diffs.find(
        (d) => d.id === props.initialCommit || d.id.startsWith(props.initialCommit!),
      );
      if (matchingDiff) {
        selectedDiff.value = matchingDiff.id;
        selectedTo.value = "self";
        return;
      }
    }

    if (response.diffs.length > 0) {
      const working = response.diffs.find((d) => d.id === "working");
      const commitsOnly = response.diffs.filter((d) => d.id !== "working");
      const mbIdx = commitsOnly.findIndex((d) => d.isMergeBase);
      let topOfBranch: GitDiffInfo | undefined;
      if (mbIdx > 0) topOfBranch = commitsOnly[mbIdx - 1];
      if (topOfBranch) {
        selectedDiff.value = topOfBranch.id;
        selectedTo.value = "working";
      } else if (working && working.filesCount > 0) {
        selectedDiff.value = "working";
      } else if (commitsOnly.length > 0) {
        selectedDiff.value = commitsOnly[0].id;
        selectedTo.value = "self";
      }
    }
  } catch (err) {
    const errStr = String(err);
    if (errStr.toLowerCase().includes("not a git repository")) {
      error.value = `Not a git repository: ${props.cwd}`;
    } else {
      error.value = `Failed to load diffs: ${errStr}`;
    }
  } finally {
    loading.value = false;
  }
}

async function loadFiles(diffId: string) {
  try {
    loading.value = true;
    error.value = null;
    const toArg = diffId === "working" ? undefined : selectedTo.value;
    const filesData = await api.getGitDiffFiles(diffId, props.cwd, toArg);

    let msgs: GitCommitMessage[] = [];
    if (diffId !== "working") {
      try {
        msgs = await api.getGitCommitMessages(props.cwd, diffId, toArg);
        commitMessages.value = msgs;
      } catch {
        commitMessages.value = [];
      }
    } else {
      commitMessages.value = [];
    }

    const commitFileEntries: GitFileInfo[] = msgs.map((msg) => ({
      path: COMMIT_MSG_PREFIX + msg.hash,
      status: "added" as const,
      additions: formatCommitMessage(msg).split("\n").length,
      deletions: 0,
      isGenerated: false,
    }));

    const allFiles = [...commitFileEntries, ...(filesData || [])];
    files.value = allFiles;
    if (allFiles.length > 0) {
      selectedFile.value = allFiles[0].path;
    } else {
      selectedFile.value = null;
      fileDiff.value = null;
    }
  } catch (err) {
    error.value = `Failed to load files: ${err}`;
  } finally {
    loading.value = false;
  }
}

async function loadFileDiff(diffId: string, filePath: string) {
  try {
    loading.value = true;
    error.value = null;
    if (isCommitMessageFile(filePath)) {
      const hash = commitHashFromPath(filePath);
      const msg = commitMessages.value.find((m) => m.hash === hash);
      if (msg) {
        fileDiff.value = { path: filePath, oldContent: "", newContent: formatCommitMessage(msg) };
      } else {
        error.value = "Commit message not found";
      }
      return;
    }
    const toArg = diffId === "working" ? undefined : selectedTo.value;
    const diffData = await api.getGitFileDiff(diffId, filePath, props.cwd, toArg);
    fileDiff.value = diffData;
  } catch (err) {
    error.value = `Failed to load file diff: ${err}`;
  } finally {
    loading.value = false;
  }
}

function handleAddComment() {
  if (!showCommentDialog.value || !commentText.value.trim() || !selectedFile.value) return;
  const line = showCommentDialog.value.line;
  const codeSnippet = showCommentDialog.value.selectedText?.split("\n")[0]?.trim() || "";
  const truncatedCode = truncateWithEllipsis(codeSnippet, 60);
  let fileRef = selectedFile.value;
  if (isCommitMessageFile(selectedFile.value)) {
    const hash = commitHashFromPath(selectedFile.value);
    const msg = commitMessages.value.find((m) => m.hash === hash);
    fileRef = msg
      ? `commit ${hash.slice(0, 8)} (${truncateWithEllipsis(msg.subject, 40)})`
      : `commit ${hash.slice(0, 8)}`;
  }
  const commentBlock = `> ${fileRef}:${line}: ${truncatedCode}\n${commentText.value}\n\n`;
  emit("comment-text-change", commentBlock);
  showCommentDialog.value = null;
  commentText.value = "";
}

// --- Navigation ---
function goToNextFile(): boolean {
  const order = navOrderRef.value;
  if (order.length === 0 || !selectedFile.value) return false;
  const idx = order.indexOf(selectedFile.value);
  if (idx >= 0 && idx < order.length - 1) {
    selectedFile.value = order[idx + 1];
    currentChangeIndex.value = -1;
    return true;
  }
  return false;
}

function goToPreviousFile(): boolean {
  const order = navOrderRef.value;
  if (order.length === 0 || !selectedFile.value) return false;
  const idx = order.indexOf(selectedFile.value);
  if (idx > 0) {
    selectedFile.value = order[idx - 1];
    currentChangeIndex.value = -1;
    return true;
  }
  return false;
}

function goToNextChange() {
  if (!diffEditor) return;
  const changes = diffEditor.getLineChanges();
  if (!changes || changes.length === 0) {
    goToNextFile();
    return;
  }
  const modEditor = diffEditor.getModifiedEditor();
  const visibleRanges = modEditor.getVisibleRanges();
  const viewBottom = visibleRanges.length > 0 ? visibleRanges[0].endLineNumber : 0;
  let nextIdx = -1;
  for (let i = 0; i < changes.length; i++) {
    const changeLine = changes[i].modifiedStartLineNumber || 1;
    if (changeLine > viewBottom) {
      nextIdx = i;
      break;
    }
  }
  if (nextIdx === -1) {
    if (goToNextFile()) return;
    return;
  }
  const change = changes[nextIdx];
  const targetLine = change.modifiedStartLineNumber || 1;
  modEditor.revealLineInCenter(targetLine);
  modEditor.setPosition({ lineNumber: targetLine, column: 1 });
  currentChangeIndex.value = nextIdx;
}

function goToPreviousChange() {
  if (!diffEditor) return;
  const changes = diffEditor.getLineChanges();
  if (!changes || changes.length === 0) {
    goToPreviousFile();
    return;
  }
  const modEditor = diffEditor.getModifiedEditor();
  const prevIdx = currentChangeIndex.value <= 0 ? -1 : currentChangeIndex.value - 1;
  if (prevIdx < 0) {
    if (goToPreviousFile()) return;
    const change = changes[0];
    const targetLine = change.modifiedStartLineNumber || 1;
    modEditor.revealLineInCenter(targetLine);
    modEditor.setPosition({ lineNumber: targetLine, column: 1 });
    currentChangeIndex.value = 0;
    return;
  }
  const change = changes[prevIdx];
  const targetLine = change.modifiedStartLineNumber || 1;
  modEditor.revealLineInCenter(targetLine);
  modEditor.setPosition({ lineNumber: targetLine, column: 1 });
  currentChangeIndex.value = prevIdx;
}

// --- Save (edit mode) ---
async function saveCurrentFile() {
  const isWorkingView = selectedDiff.value === "working" || selectedTo.value === "working";
  if (
    !diffEditor ||
    !selectedFile.value ||
    isCommitMessageFile(selectedFile.value) ||
    !fileDiff.value ||
    modeVal !== "edit" ||
    !gitRoot.value ||
    !isWorkingView
  ) {
    return;
  }
  const modEditor = diffEditor.getModifiedEditor();
  const model = modEditor.getModel();
  if (!model) return;
  const content = model.getValue();
  const fullPath = gitRoot.value + "/" + selectedFile.value;
  try {
    saveStatus.value = "saving";
    const response = await fetch("/api/write-file", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: fullPath, content }),
    });
    if (response.ok) {
      saveStatus.value = "saved";
      setTimeout(() => (saveStatus.value = "idle"), 2000);
    } else {
      saveStatus.value = "error";
      setTimeout(() => (saveStatus.value = "idle"), 3000);
    }
  } catch (err) {
    console.error("Failed to save:", err);
    saveStatus.value = "error";
    setTimeout(() => (saveStatus.value = "idle"), 3000);
  }
}

function scheduleSave() {
  if (modeVal !== "edit") return;
  if (saveTimeout) clearTimeout(saveTimeout);
  saveTimeout = window.setTimeout(() => {
    saveCurrentFile();
    saveTimeout = null;
  }, 1000);
}
scheduleSaveFn = scheduleSave;

function saveImmediately() {
  if (saveTimeout) {
    clearTimeout(saveTimeout);
    saveTimeout = null;
  }
  saveCurrentFile();
}

// --- Theme observer ---
let themeObserver: MutationObserver | null = null;
watch(
  monacoLoaded,
  () => {
    if (!monacoMod) return;
    themeObserver?.disconnect();
    const updateMonacoTheme = () => {
      const theme = isDarkModeActive() ? "vs-dark" : "vs";
      monacoMod?.editor.setTheme(theme);
    };
    themeObserver = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        if (mutation.attributeName === "class") updateMonacoTheme();
      }
    });
    themeObserver.observe(document.documentElement, { attributes: true });
  },
  { immediate: true },
);

// --- Keyboard shortcuts (capture phase) ---
function handleKeyDown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    // If Monaco's find widget is open, let Monaco close it.
    const findWidget = editorContainerRef.value?.querySelector(".find-widget.visible");
    if (findWidget) return;
    // If a nested overlay (commit/dir picker) is open, let it handle Escape.
    if (
      document.querySelector(".commit-picker-popover") ||
      document.querySelector(".commit-picker-modal")
    ) {
      return;
    }
    // If vim mode is in a non-normal mode, let monaco-vim handle Escape.
    const vimFocused =
      editorContainerRef.value?.contains(document.activeElement) ||
      vimStatusRef.value?.contains(document.activeElement);
    if (
      !isMobile.value &&
      vimEnabled.value &&
      vimFocused &&
      (vimStatusRef.value?.textContent ?? "").trim() !== ""
    ) {
      return;
    }
    if (showCommentDialog.value) {
      showCommentDialog.value = null;
    } else {
      emit("close");
    }
    return;
  }
  if ((e.ctrlKey || e.metaKey) && e.key === "s") {
    e.preventDefault();
    saveImmediately();
    return;
  }
  // Route Ctrl/Cmd+F to Monaco's find widget instead of browser find.
  if ((e.ctrlKey || e.metaKey) && e.key === "f") {
    if (diffEditor) {
      e.preventDefault();
      e.stopPropagation();
      const modEditor = diffEditor.getModifiedEditor();
      modEditor.focus();
      modEditor.trigger("keyboard", "actions.find", null);
    }
    return;
  }
  // When Monaco's find widget is open, let all non-modifier keys pass through.
  const findWidget = editorContainerRef.value?.querySelector(".find-widget.visible");
  if (findWidget) return;
  // Intercept PageUp/PageDown to scroll the diff editor.
  if (e.key === "PageUp" || e.key === "PageDown") {
    if (diffEditor) {
      e.preventDefault();
      e.stopPropagation();
      const modEditor = diffEditor.getModifiedEditor();
      modEditor.trigger("keyboard", e.key === "PageUp" ? "cursorPageUp" : "cursorPageDown", null);
    }
    return;
  }
  // Comment mode navigation (only when comment dialog is closed).
  if (mode.value === "comment" && !showCommentDialog.value) {
    if (e.key === ".") {
      e.preventDefault();
      goToNextChange();
      return;
    } else if (e.key === ",") {
      e.preventDefault();
      goToPreviousChange();
      return;
    } else if (e.key === ">") {
      e.preventDefault();
      goToNextFile();
      return;
    } else if (e.key === "<") {
      e.preventDefault();
      goToPreviousFile();
      return;
    }
  }
  if (!e.ctrlKey) return;
  if (e.key === "j") {
    e.preventDefault();
    goToNextFile();
  } else if (e.key === "k") {
    e.preventDefault();
    goToPreviousFile();
  }
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      window.addEventListener("keydown", handleKeyDown, true);
    } else {
      window.removeEventListener("keydown", handleKeyDown, true);
    }
  },
  { immediate: true },
);

// --- Tree entries + nav order (computed; stable hook order) ---
const treeEntries = computed<DiffFileTreeEntry[]>(() => {
  const msgByHash = new Map(commitMessages.value.map((m) => [m.hash, m]));
  const subjectCounts = new Map<string, number>();
  for (const f of files.value) {
    if (!isCommitMessageFile(f.path)) continue;
    const hash = commitHashFromPath(f.path);
    const msg = msgByHash.get(hash);
    const subject = msg ? msg.subject : hash.slice(0, 8);
    subjectCounts.set(subject, (subjectCounts.get(subject) ?? 0) + 1);
  }
  return files.value.map((f) => {
    if (isCommitMessageFile(f.path)) {
      const hash = commitHashFromPath(f.path);
      const msg = msgByHash.get(hash);
      const subject = msg ? msg.subject : hash.slice(0, 8);
      const shortHash = hash.slice(0, 8);
      const collides = (subjectCounts.get(subject) ?? 0) > 1;
      const leaf = collides ? `${subject} (${shortHash})` : subject;
      return {
        realPath: f.path,
        treePath: [COMMIT_MESSAGES_DIR, leaf],
        decoration: msg?.isHead ? "HEAD" : undefined,
        decorationTitle: msg?.isHead ? `${hash} (HEAD)` : undefined,
      };
    }
    return { realPath: f.path, treePath: f.path.split("/"), status: f.status };
  });
});

const navOrder = computed(() => treeRealPathOrder(treeEntries.value));
watch(navOrder, (v) => (navOrderRef.value = v), { immediate: true });

// Title for the sidebar layout's header.
const currentTitleText = computed<string | null>(() => {
  const sf = selectedFile.value;
  if (!sf) return null;
  if (isCommitMessageFile(sf)) {
    const hash = commitHashFromPath(sf);
    const msg = commitMessages.value.find((m) => m.hash === hash);
    const subject = msg ? msg.subject : hash.slice(0, 8);
    return msg?.isHead ? `${subject} — HEAD` : subject;
  }
  return sf;
});
const currentTitleTooltip = computed<string | null>(() => {
  const sf = selectedFile.value;
  if (!sf) return null;
  if (isCommitMessageFile(sf)) {
    const hash = commitHashFromPath(sf);
    const msg = commitMessages.value.find((m) => m.hash === hash);
    const subject = msg ? msg.subject : hash.slice(0, 8);
    return msg?.isHead ? `${hash} (HEAD)\n\n${subject}` : `${hash}\n\n${subject}`;
  }
  return sf;
});

const currentFileIndex = computed(() => navOrder.value.indexOf(selectedFile.value ?? ""));
const hasNextFile = computed(
  () => currentFileIndex.value >= 0 && currentFileIndex.value < navOrder.value.length - 1,
);
const hasPrevFile = computed(() => currentFileIndex.value > 0);

const fileIndexIndicator = computed(() =>
  navOrder.value.length > 1 && currentFileIndex.value >= 0
    ? `(${currentFileIndex.value + 1}/${navOrder.value.length})`
    : null,
);

function getStatusSymbol(status: string): string {
  switch (status) {
    case "added":
      return "+";
    case "deleted":
      return "-";
    case "modified":
      return "~";
    default:
      return "";
  }
}

function fileOptionLabel(file: GitFileInfo): string {
  if (isCommitMessageFile(file.path)) {
    const hash = commitHashFromPath(file.path);
    const msg = commitMessages.value.find((m) => m.hash === hash);
    const label = msg ? `📝 ${truncateWithEllipsis(msg.subject, 50)}` : `📝 ${hash.slice(0, 8)}`;
    return label + (msg?.isHead ? " [HEAD]" : "");
  }
  let label = `${getStatusSymbol(file.status)} ${file.path}`;
  if (file.additions > 0) label += ` (+${file.additions})`;
  if (file.deletions > 0) label += ` (-${file.deletions})`;
  if (file.isGenerated) label += " [generated]";
  return label;
}

// --- Sidebar commit list ---
const sidebarCommits = computed<GitDiffInfo[]>(() => {
  const list: GitDiffInfo[] = [];
  const working = diffs.value.find((d) => d.id === "working");
  if (working) list.push(working);
  const commitsOnly = diffs.value.filter((d) => d.id !== "working");
  const mergeBaseIdx = commitsOnly.findIndex((d) => d.isMergeBase);
  if (mergeBaseIdx >= 0) {
    list.push(...commitsOnly.slice(0, Math.min(mergeBaseIdx + 1, 50)));
  } else {
    list.push(...commitsOnly.slice(0, 10));
  }
  return list;
});

const sidebarSelIdx = computed(() =>
  selectedDiff.value ? sidebarCommits.value.findIndex((d) => d.id === selectedDiff.value) : -1,
);
function inSidebarRange(idx: number): boolean {
  if (sidebarSelIdx.value < 0) return false;
  if (selectedDiff.value === "working") return idx === sidebarSelIdx.value;
  if (selectedTo.value === "self") return idx === sidebarSelIdx.value;
  return idx >= 0 && idx <= sidebarSelIdx.value;
}
function commitItemClass(d: GitDiffInfo, idx: number): string {
  const isWorking = d.id === "working";
  const isSelected = selectedDiff.value === d.id;
  const inRange = inSidebarRange(idx);
  return [
    "diff-viewer-commit-list-item",
    isSelected && "selected",
    inRange && "in-range",
    isWorking && "working",
  ]
    .filter(Boolean)
    .join(" ");
}
function commitRefClass(ref: string): string {
  return `diff-viewer-commit-list-ref${ref === "HEAD" ? " head" : ""}${
    ref.includes("/") ? " remote" : ""
  }`;
}
function onCommitListClick(d: GitDiffInfo) {
  if (d.id === "working") {
    selectedDiff.value = "working";
  } else {
    selectedDiff.value = d.id;
  }
}

// --- Event handlers from child components ---
function onCommitChange(diff: string, to: "working" | "self") {
  selectedDiff.value = diff;
  selectedTo.value = to;
}
function onDirSelect(path: string) {
  emit("cwd-change", path);
  showDirPicker.value = false;
}

// --- Lifecycle ---
onMounted(() => {
  window.addEventListener("resize", handleResize);
});
onUnmounted(() => {
  window.removeEventListener("resize", handleResize);
  window.removeEventListener("keydown", handleKeyDown, true);
  themeObserver?.disconnect();
  diffUpdateDisposable?.dispose();
  if (saveTimeout) clearTimeout(saveTimeout);
  if (amendTimeout) clearTimeout(amendTimeout);
  if (keyboardHintTimer) clearTimeout(keyboardHintTimer);
  disposeEditor();
});
</script>
