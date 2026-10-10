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
    <div ref="containerRef" class="diff-viewer-container">
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
      <div
        v-if="review.sent.value && !review.error.value"
        class="diff-viewer-toast diff-viewer-toast-saved"
        role="status"
        data-testid="review-recording-sent"
      >
        ✅ Sent — transcribing in the conversation
      </div>
      <div v-if="showKeyboardHint" class="diff-viewer-toast diff-viewer-toast-hint">
        ⌨️ Use . , for next/prev change, &lt; &gt; for files
      </div>

      <!-- Mobile header -->
      <div
        v-if="isMobile"
        class="diff-viewer-header diff-viewer-header-mobile"
        data-review="Toolbar"
      >
        <div class="diff-viewer-header-main">
          <div class="diff-viewer-mobile-selectors" :inert="recordingLocked">
            <CommitPicker
              :diffs="diffs"
              :selected-diff="selectedDiff"
              :selected-to="selectedTo"
              :is-mobile="isMobile"
              @change="onCommitChange"
              @open="loadCommitStats"
            />
            <div v-if="diffView === 'files'" class="diff-viewer-file-selector-wrapper">
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
          <ReviewRecordingStatus
            v-if="recordingLocked"
            :phase="review.phase.value"
            :levels="review.levels"
            :current="review.current"
          />
        </div>
        <ReviewRecordButton
          v-if="canRecord || review.phase.value !== 'idle'"
          :phase="review.phase.value"
          :starts-conversation="startsConversation"
          :elapsed-ms="review.elapsedMs"
          @start="review.start"
          @stop="review.stop"
          @cancel="review.cancel"
        />
        <button
          v-tooltip.top="dirTooltip"
          class="diff-viewer-dir-btn"
          :aria-label="`Git directory: ${cwd}. Click to change`"
          :aria-disabled="recordingLocked"
          @click="openDirPicker"
        >
          <span v-html="DIR_ICON" />
        </button>
        <button
          v-tooltip.top="closeTooltip"
          class="diff-viewer-close"
          :aria-label="closeTooltip"
          :aria-disabled="recordingLocked"
          @click="requestClose"
        >
          ×
        </button>
      </div>

      <!-- Desktop header -->
      <div v-else class="diff-viewer-header" data-review="Toolbar">
        <div class="diff-viewer-header-row">
          <button
            v-if="layout === 'sidebar'"
            v-tooltip.top="'Hide sidebar'"
            type="button"
            class="btn-icon diff-viewer-collapse-btn"
            aria-label="Hide sidebar"
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
            v-tooltip.top="'Show sidebar'"
            type="button"
            class="btn-icon diff-viewer-expand-btn"
            aria-label="Show sidebar"
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

          <div class="diff-viewer-header-main">
            <div
              v-if="layout === 'header'"
              class="diff-viewer-selectors-row"
              :inert="recordingLocked"
            >
              <div class="diff-viewer-selector-group">
                <label class="diff-viewer-selector-label">Commits</label>
                <CommitPicker
                  :diffs="diffs"
                  :selected-diff="selectedDiff"
                  :selected-to="selectedTo"
                  :is-mobile="isMobile"
                  @change="onCommitChange"
                  @open="loadCommitStats"
                />
              </div>
              <div v-if="diffView === 'files'" class="diff-viewer-selector-group">
                <label class="diff-viewer-selector-label">Commit messages and changed files</label>
                <div class="diff-viewer-file-selector-wrapper">
                  <select
                    :value="selectedFile || ''"
                    class="diff-viewer-select"
                    :disabled="files.length === 0"
                    @change="selectedFile = ($event.target as HTMLSelectElement).value || null"
                  >
                    <option value="">
                      {{ files.length === 0 ? "No files" : "Choose file..." }}
                    </option>
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
            <div
              v-else
              class="diff-viewer-header-title"
              :title="currentTitleTooltip ?? undefined"
              :inert="recordingLocked"
            >
              {{ currentTitleText ?? "\u00a0" }}
            </div>
            <ReviewRecordingStatus
              v-if="recordingLocked"
              :phase="review.phase.value"
              :levels="review.levels"
              :current="review.current"
            />
          </div>

          <div class="diff-viewer-controls-row">
            <template v-if="diffView === 'files'">
              <div class="diff-viewer-nav-buttons">
                <button
                  v-tooltip.top="'Previous file (<)'"
                  class="diff-viewer-nav-btn"
                  :disabled="!hasPrevFile"
                  aria-label="Previous file (<)"
                  @click="goToPreviousFile"
                >
                  <span v-html="PREV_FILE_ICON" />
                </button>
                <button
                  v-tooltip.top="'Previous change (,)'"
                  class="diff-viewer-nav-btn"
                  :disabled="!fileDiff"
                  aria-label="Previous change (,)"
                  @click="goToPreviousChange"
                >
                  <span v-html="PREV_CHANGE_ICON" />
                </button>
                <button
                  v-tooltip.top="'Next change (.)'"
                  class="diff-viewer-nav-btn"
                  :disabled="!fileDiff"
                  aria-label="Next change (.)"
                  @click="goToNextChange"
                >
                  <span v-html="NEXT_CHANGE_ICON" />
                </button>
                <button
                  v-tooltip.top="'Next file (>)'"
                  class="diff-viewer-nav-btn"
                  :disabled="!hasNextFile"
                  aria-label="Next file (>)"
                  @click="goToNextFile()"
                >
                  <span v-html="NEXT_FILE_ICON" />
                </button>
              </div>
              <div class="diff-viewer-mode-toggle">
                <button
                  v-tooltip.top="'Comment mode'"
                  :class="`diff-viewer-mode-btn ${mode === 'comment' ? 'active' : ''}`"
                  aria-label="Comment mode"
                  @click="mode = 'comment'"
                >
                  💬
                </button>
                <button
                  v-tooltip.top="'Edit mode'"
                  :class="`diff-viewer-mode-btn ${mode === 'edit' ? 'active' : ''}`"
                  aria-label="Edit mode"
                  @click="mode = 'edit'"
                >
                  ✏️
                </button>
              </div>
              <VimToggle :enabled="vimEnabled" @change="setVimEnabled" />
            </template>
            <ReviewRecordButton
              v-if="canRecord || review.phase.value !== 'idle'"
              :phase="review.phase.value"
              :starts-conversation="startsConversation"
              :elapsed-ms="review.elapsedMs"
              @start="review.start"
              @stop="review.stop"
              @cancel="review.cancel"
            />
            <button
              v-tooltip.top="dirTooltip"
              class="diff-viewer-dir-btn"
              :aria-label="`Git directory: ${cwd}. Click to change`"
              :aria-disabled="recordingLocked"
              @click="openDirPicker"
            >
              <span v-html="DIR_ICON" />
            </button>
            <button
              v-tooltip.top="closeTooltip"
              class="diff-viewer-close"
              :aria-label="closeTooltip"
              :aria-disabled="recordingLocked"
              @click="requestClose"
            >
              ×
            </button>
          </div>
        </div>
      </div>

      <ReviewRecordingBar
        :busy="recordingLocked"
        :error="review.error.value"
        :pending="review.pending.value"
        :conversation-id="recordingConversationId"
        @retry="review.retry"
        @download="review.download"
        @discard="review.discard"
        @dismiss="review.dismissError"
      />

      <div
        v-if="tourAvailable"
        class="diff-viewer-view-switcher"
        role="group"
        aria-label="Diff view"
      >
        <button
          type="button"
          :class="['diff-viewer-view-btn', { active: diffView === 'tour' }]"
          :aria-pressed="diffView === 'tour'"
          @click="diffView = 'tour'"
        >
          Tour
        </button>
        <button
          type="button"
          :class="['diff-viewer-view-btn', { active: diffView === 'files' }]"
          :aria-pressed="diffView === 'files'"
          @click="diffView = 'files'"
        >
          Files
        </button>
      </div>
      <!-- Once built, the tour appears in place of this (see CommitTourAction). -->
      <div v-else-if="isOpen && tourCommit" class="diff-viewer-view-switcher">
        <CommitTourAction
          :cwd="cwd"
          :hash="tourCommit.id"
          :conversation-id="tourConversationId ?? null"
          :navigate="openTourWorker"
          @present="markTour"
        />
      </div>

      <!-- Error banner -->
      <div v-if="error && diffView === 'files'" class="diff-viewer-error">{{ error }}</div>

      <!-- Main content -->
      <div
        :class="`diff-viewer-content${!isMobile && layout === 'sidebar' ? ' diff-viewer-content-sidebar' : ''}`"
      >
        <aside v-if="!isMobile && layout === 'sidebar'" class="diff-viewer-sidebar">
          <div
            class="diff-viewer-sidebar-section diff-viewer-sidebar-commits"
            data-review="Commits"
          >
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
                    :title="
                      d.id === 'working'
                        ? workingChangesStatus(d).description
                        : `${d.message}\n${d.id}`
                    "
                    role="option"
                    :aria-selected="selectedDiff === d.id"
                    @click="onCommitListClick(d)"
                  >
                    <div class="diff-viewer-commit-list-line1">
                      <span class="diff-viewer-commit-list-subject">{{
                        d.id === "working" ? "Working Changes" : d.message
                      }}</span>
                      <span
                        v-if="d.id === 'working'"
                        :class="['working-changes-status', d.filesCount === 0 ? 'clean' : 'dirty']"
                        >{{ workingChangesStatus(d).label }}</span
                      >
                      <span v-if="d.hasTour" class="commit-picker-tour-badge">tour</span>
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
          <div
            class="diff-viewer-sidebar-section diff-viewer-sidebar-files"
            :data-review="diffView === 'tour' ? 'Tour contents' : 'Changed files'"
          >
            <template v-if="diffView === 'tour'">
              <div class="diff-viewer-sidebar-label"><span>Table of Contents</span></div>
              <div ref="tourContentsScrollRef" class="diff-viewer-sidebar-tour-scroll">
                <div v-if="tourLoading" class="diff-viewer-file-list-empty">Loading tour...</div>
                <div v-else-if="tourError" class="diff-viewer-file-list-empty">
                  Tour unavailable
                </div>
                <CommitTourContents
                  v-else-if="tourContents.length > 0"
                  :items="tourContents"
                  :active-anchor="activeTourAnchor"
                  :expanded-anchors="expandedTourAnchors"
                  @select="scrollToTourAnchor"
                  @expand-change="setTourExpanded"
                />
                <div v-else class="diff-viewer-file-list-empty">No tour sections</div>
              </div>
            </template>
            <template v-else>
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
                      @select="selectSidebarFile"
                    />
                  </div>
                </div>
              </div>
            </template>
          </div>
        </aside>
        <div ref="mainRef" class="diff-viewer-main">
          <div v-if="diffView === 'tour'" class="diff-viewer-tour-pane">
            <div v-if="tourLoading" class="diff-viewer-loading">
              <div class="spinner"></div>
              <span>Loading tour...</span>
            </div>
            <div v-else-if="tourError" class="diff-viewer-tour-error">{{ tourError }}</div>
            <CommitTourView
              v-else-if="tourResponse"
              ref="tourViewRef"
              :tour="tourResponse"
              :commit-message="selectedTourCommitMessage"
              :expanded-anchors="expandedTourAnchors"
              :cwd="cwd"
              @expand-change="setTourExpanded"
              @active-anchor-change="handleTourActiveAnchor"
              @open-comment="openTourComment"
            />
          </div>
          <div v-show="diffView === 'files'" class="diff-viewer-files-pane" data-review="Files">
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
              <p class="diff-viewer-hint">
                Click a line to comment, or select text and click Comment.
              </p>
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
            <!-- Floating "add comment" prompt shown next to a selection in comment mode -->
            <button
              v-if="commentPrompt"
              v-tooltip.top="'Add comment on selection'"
              class="diff-viewer-comment-prompt"
              :style="{ top: `${commentPrompt.top}px`, left: `${commentPrompt.left}px` }"
              @mousedown.prevent
              @click="openCommentFromPrompt"
            >
              💬 Comment
            </button>
          </div>
        </div>
      </div>

      <!-- Mobile floating nav buttons -->
      <div v-if="isMobile && diffView === 'files'" class="diff-viewer-mobile-nav">
        <button
          v-tooltip.top="
            mode === 'comment' ? 'Comment mode (tap to switch)' : 'Edit mode (tap to switch)'
          "
          :class="`diff-viewer-mobile-nav-btn diff-viewer-mobile-mode-btn ${mode === 'comment' ? 'active' : ''}`"
          :aria-label="
            mode === 'comment' ? 'Comment mode (tap to switch)' : 'Edit mode (tap to switch)'
          "
          @click="mode = mode === 'comment' ? 'edit' : 'comment'"
        >
          {{ mode === "comment" ? "💬" : "✏️" }}
        </button>
        <button
          v-tooltip.top="'Previous file (<)'"
          class="diff-viewer-mobile-nav-btn"
          :disabled="!hasPrevFile"
          aria-label="Previous file (<)"
          @click="goToPreviousFile"
        >
          <span v-html="PREV_FILE_ICON" />
        </button>
        <button
          v-tooltip.top="'Previous change (,)'"
          class="diff-viewer-mobile-nav-btn"
          :disabled="!fileDiff"
          aria-label="Previous change (,)"
          @click="goToPreviousChange"
        >
          <span v-html="PREV_CHANGE_ICON" />
        </button>
        <button
          v-tooltip.top="'Next change (.)'"
          class="diff-viewer-mobile-nav-btn"
          :disabled="!fileDiff"
          aria-label="Next change (.)"
          @click="goToNextChange"
        >
          <span v-html="NEXT_CHANGE_ICON" />
        </button>
        <button
          v-tooltip.top="'Next file (>)'"
          class="diff-viewer-mobile-nav-btn"
          :disabled="!hasNextFile"
          aria-label="Next file (>)"
          @click="goToNextFile()"
        >
          <span v-html="NEXT_FILE_ICON" />
        </button>
      </div>

      <!-- Comment dialog -->
      <CommentDialog
        v-if="tourCommentTarget"
        :key="`tour-${tourCommentDialogOpens}`"
        v-model:text="tourCommentText"
        :where="tourCommentTarget.where"
        :quoted="tourCommentTarget.selectedText"
        @submit="handleAddTourComment"
        @cancel="tourCommentTarget = null"
      />
      <CommentDialog
        v-else-if="showCommentDialog"
        :key="commentDialogOpens"
        v-model:text="commentText"
        :where="lineCommentLabel(showCommentDialog, true)"
        :quoted="showCommentDialog.selectedText"
        @submit="handleAddComment"
        @cancel="showCommentDialog = null"
      />
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
import { api, type GitTourResponse } from "../../services/api";
import { loadMonaco } from "../../services/monaco";
import { isDarkModeActive } from "../../services/theme";
import { buildTourCommentBlock, type TourCommentTarget } from "../composables/tourComments";
import { useVimEnabled, useMonacoVim } from "../composables/monacoVim";
import {
  lineCommentLabel,
  truncateWithEllipsis,
  useMonacoComments,
} from "../composables/monacoComments";
import VimToggle from "./VimToggle.vue";
import CommentDialog from "./CommentDialog.vue";
import CommitTourView from "./CommitTourView.vue";
import CommitTourContents from "./CommitTourContents.vue";
import CommitPicker from "./CommitPicker.vue";
import RangeToggle from "./RangeToggle.vue";
import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import DiffFileTree from "./DiffFileTree.vue";
import ReviewRecordButton from "./ReviewRecordButton.vue";
import CommitTourAction from "./CommitTourAction.vue";
import { navigateToConversationSlug } from "../composables/subagentLive";
import ReviewRecordingBar from "./ReviewRecordingBar.vue";
import ReviewRecordingStatus from "./ReviewRecordingStatus.vue";
import {
  clipSelection,
  collapse,
  type ReviewContext,
  type ReviewSide,
  type ReviewTarget,
} from "./reviewCapture";
import {
  reviewRecordingSupported,
  useReviewRecording,
  type ReviewConversationStart,
} from "./useReviewRecording";
import { COMMIT_MESSAGES_DIR, treeRealPathOrder, type DiffFileTreeEntry } from "./diffFileTree";
import { buildTourContents } from "./commitTourContents";
import { defaultDiffSelection, workingChangesStatus } from "./diffViewerModel";
import type { GitDiffInfo, GitFileInfo, GitFileDiff, GitCommitMessage } from "../../types";

const props = defineProps<{
  cwd: string;
  isOpen: boolean;
  initialCommit?: string;
  // File to select once initialCommit's file list loads (e.g. from the git
  // graph diffstat). Consumed on the first load only.
  initialFile?: string;
  // Conversation that receives narrated review recordings. Without one, each
  // recording starts a conversation (startReviewConversation), shown once the
  // recording is sent (followReviewConversation); with neither, no Record.
  recordingConversationId?: string;
  startReviewConversation?: () => ReviewConversationStart;
  followReviewConversation?: () => (conversationId: string) => void;
  // Conversation that builds tours on request; none hides Build tour.
  tourConversationId?: string;
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
// When one side of the diff is empty (new/deleted file), give the non-empty
// side most of the width. Monaco clamps splitViewDefaultRatio to [0.1, 0.9].
const EMPTY_SIDE_SPLIT_RATIO = 0.1;

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
const diffView = ref<"tour" | "files">("files");
const tourResponse = ref<GitTourResponse | null>(null);
const tourViewRef = ref<{ scrollToAnchor: (anchor: string) => void } | null>(null);
const tourContentsScrollRef = ref<HTMLElement | null>(null);
const activeTourAnchor = ref<string | null>(null);
const expandedTourAnchors = ref(new Set<string>());
let tourContentsResizeObserver: ResizeObserver | null = null;
const tourLoading = ref(false);
const tourError = ref<string | null>(null);
const files = ref<GitFileInfo[]>([]);
const selectedFile = ref<string | null>(null);
let pendingInitialFile: string | undefined;
let loadDiffsRequestId = 0;
let loadFilesRequestId = 0;
let loadFileDiffRequestId = 0;
const fileDiff = ref<GitFileDiff | null>(null);
const loading = ref(false);
const error = ref<string | null>(null);
const monacoLoaded = ref(false);
const currentChangeIndex = ref(-1);
const saveStatus = ref<"idle" | "saving" | "saved" | "error">("idle");
const mode = ref<ViewMode>("comment");
const commitMessages = ref<GitCommitMessage[]>([]);
const selectedTourCommitMessage = computed(() => {
  const hash = tourResponse.value?.hash;
  return hash ? (commitMessages.value.find((message) => message.hash === hash) ?? null) : null;
});
const amendStatus = ref<"idle" | "saving" | "saved" | "error">("idle");
const showKeyboardHint = ref(false);
const isMobile = ref(window.innerWidth < 768);
const tourCommentTarget = ref<TourCommentTarget | null>(null);
const tourCommentText = ref("");
const tourCommentDialogOpens = ref(0);
// Switching between Tour and Files closes any pending tour comment dialog.
watch(diffView, (view) => {
  tourCommentTarget.value = null;
  tourCommentText.value = "";
  if (view === "tour") showKeyboardHint.value = false;
});
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

// Tours cover a single commit, not a range through the working tree.
const tourCommit = computed(() =>
  selectedDiff.value && selectedDiff.value !== "working" && selectedTo.value === "self"
    ? diffs.value.find((diff) => diff.id === selectedDiff.value)
    : undefined,
);
const tourAvailable = computed(() => !!tourCommit.value?.hasTour);

function markTour(hash: string) {
  const diff = diffs.value.find((d) => d.id === hash);
  if (diff) diff.hasTour = true;
}

const tourSelectionKey = computed(() =>
  props.isOpen && tourAvailable.value && selectedDiff.value
    ? `${props.cwd}\n${selectedDiff.value}`
    : "",
);

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
const mainRef = ref<HTMLDivElement | null>(null);
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
// Current split ratio for the active file. Monaco 0.44's updateOptions()
// resets splitViewDefaultRatio to 0.5 when omitted, so every
// diffEditor.updateOptions() call must pass this along.
let splitViewRatioVal = 0.5;
let scheduleSaveFn: (() => void) | null = null;

// Shared comment-mode UX (click-to-comment, selection prompt, dialog state).
const {
  showCommentDialog,
  commentDialogOpens,
  commentPrompt,
  commentText,
  attach: attachComments,
  handleAddComment,
  openCommentFromPrompt,
  reset: resetComments,
} = useMonacoComments({
  monaco: () => monacoMod,
  mode: () => modeVal,
  isMobile: () => isMobileVal,
  promptHost: () => mainRef.value,
  fileRef: commentFileRef,
  onSubmit: emitComment,
});

// Reference used in the quote header of a submitted comment: the file path,
// or "commit <hash> (<subject>)" for commit-message pseudo-files.
function commentFileRef(): string | null {
  if (!selectedFile.value) return null;
  if (isCommitMessageFile(selectedFile.value)) {
    const hash = commitHashFromPath(selectedFile.value);
    const msg = commitMessages.value.find((m) => m.hash === hash);
    return msg
      ? `commit ${hash.slice(0, 8)} (${truncateWithEllipsis(msg.subject, 40)})`
      : `commit ${hash.slice(0, 8)}`;
  }
  return selectedFile.value;
}

// --- Narrated review recording ---
// While recording (and until it is handed off) the viewer stays open, so the
// captured context always describes what the reviewer is looking at.
const containerRef = ref<HTMLElement | null>(null);

function commitLabel(id: string | null): string {
  if (!id) return "";
  if (id === "working") return "working changes";
  const commit = diffs.value.find((diff) => diff.id === id);
  const label = commit
    ? `${id.slice(0, 8)} ${truncateWithEllipsis(commit.message, 60)}`
    : id.slice(0, 8);
  return selectedTo.value === "working" ? `${label} → working tree` : label;
}

function reviewFileLabel(path: string): string {
  return isCommitMessageFile(path)
    ? `commit message ${commitHashFromPath(path).slice(0, 8)}`
    : path;
}

function lineSpan(first: number, last: number): string {
  return first === last ? `${first}` : `${first}–${last}`;
}

function monacoEditors(): Array<[Monaco.editor.ICodeEditor, ReviewSide]> {
  if (!diffEditor) return [];
  return [
    [diffEditor.getModifiedEditor(), "new"],
    [diffEditor.getOriginalEditor(), "old"],
  ];
}

const reviewContext: ReviewContext = {
  view() {
    const commit = commitLabel(selectedDiff.value);
    if (diffView.value === "tour") {
      return { where: `Tour · ${commit}`, mode: "tour", commit: selectedDiff.value ?? undefined };
    }
    const file = selectedFile.value ? reviewFileLabel(selectedFile.value) : undefined;
    return {
      where: ["Files", commit, file].filter(Boolean).join(" · "),
      mode: "files",
      commit: selectedDiff.value ?? undefined,
      file,
    };
  },
  pointAt(x, y, element) {
    if (diffView.value !== "files" || !selectedFile.value) return undefined;
    const file = reviewFileLabel(selectedFile.value);
    for (const [editor, side] of monacoEditors()) {
      if (!editor.getDomNode()?.contains(element)) continue;
      const line = editor.getTargetAtClientPoint(x, y)?.position?.lineNumber;
      const model = editor.getModel();
      if (!line || !model) return { where: `Files › ${file}`, file };
      return {
        where: `Files › ${file} ${side} ${line}`,
        file,
        side,
        line,
        text: collapse(model.getLineContent(line)),
      };
    }
    return undefined;
  },
  selection() {
    if (diffView.value !== "files" || !selectedFile.value) return undefined;
    const file = reviewFileLabel(selectedFile.value);
    for (const [editor, side] of monacoEditors()) {
      const selection = editor.getSelection();
      const model = editor.getModel();
      if (!selection || selection.isEmpty() || !model) continue;
      const text = model.getValueInRange(selection);
      if (!text.trim()) continue;
      const target: ReviewTarget = {
        where: `Files › ${file} ${side} ${lineSpan(selection.startLineNumber, selection.endLineNumber)}`,
        file,
        side,
        line: selection.startLineNumber,
        end_line: selection.endLineNumber,
        text: clipSelection(text),
      };
      return target;
    }
    // Selections elsewhere (sidebar, commit list) are ordinary DOM selections.
    return undefined;
  },
  onScreen() {
    if (diffView.value !== "files") return undefined;
    if (!selectedFile.value || !fileDiff.value) return [];
    const spans = monacoEditors().flatMap(([editor, side]) => {
      const ranges = editor.getVisibleRanges();
      if (!ranges.length || !editor.getModel()?.getValueLength()) return [];
      return [
        `${side} ${lineSpan(ranges[0].startLineNumber, ranges[ranges.length - 1].endLineNumber)}`,
      ];
    });
    const file = reviewFileLabel(selectedFile.value);
    return [spans.length ? `${file} (${spans.join(", ")})` : file];
  },
};

const review = useReviewRecording({
  root: () => containerRef.value,
  context: reviewContext,
  cwd: () => props.cwd,
  conversationId: () => props.recordingConversationId,
  startConversation: () => props.startReviewConversation?.(),
  followConversation: () => props.followReviewConversation?.() ?? (() => {}),
});
const reviewSupported =
  reviewRecordingSupported() && !!window.__SHELLEY_INIT__?.transcription_available;
const canRecord = computed(
  () => reviewSupported && !!(props.recordingConversationId || props.startReviewConversation),
);
const recordingLocked = computed(() => review.phase.value !== "idle");
// Where the recording in progress goes was settled when it started.
const startsConversation = computed(() =>
  recordingLocked.value ? review.startsConversation.value : !props.recordingConversationId,
);

// After recordingLocked: the immediate run may read it.
let tourRequestId = 0;
watch(
  tourSelectionKey,
  async (key) => {
    const requestId = ++tourRequestId;
    tourResponse.value = null;
    activeTourAnchor.value = null;
    expandedTourAnchors.value = new Set();
    tourError.value = null;
    tourLoading.value = false;
    tourCommentTarget.value = null;
    tourCommentText.value = "";
    if (!key || !selectedDiff.value) {
      diffView.value = "files";
      return;
    }

    // A tour landing mid-recording (selection is locked then) waits for a
    // click rather than yanking the view out from under the narration.
    if (!recordingLocked.value) diffView.value = "tour";
    tourLoading.value = true;
    try {
      const response = await api.getGitTour(props.cwd, selectedDiff.value);
      if (requestId !== tourRequestId) return;
      tourResponse.value = response;
    } catch (err) {
      if (requestId !== tourRequestId) return;
      tourError.value = `Failed to load commit tour: ${String(err)}`;
    } finally {
      if (requestId === tourRequestId) tourLoading.value = false;
    }
  },
  { immediate: true },
);
const closeTooltip = computed(() =>
  recordingLocked.value ? "Stop recording to close" : "Close (Esc)",
);

// Locked controls use aria-disabled, not disabled, so they keep pointer events
// and focus for the tooltip that says why. Tooltips here also depend on this
// template not re-rendering while one is open (PrimeVue's tooltip removes
// itself on update), which is why the live recording values go to their
// components as refs.
const dirTooltip = computed(() =>
  recordingLocked.value
    ? "Stop recording to change directory"
    : `Git directory: ${props.cwd}\nClick to change`,
);

function requestClose() {
  if (!recordingLocked.value) {
    loadDiffsRequestId++;
    loadFilesRequestId++;
    loadFileDiffRequestId++;
    emit("close");
  }
}

// The worker's conversation replaces this one underneath, so leave first;
// while a recording locks the viewer, open it alongside instead.
function openTourWorker(slug: string) {
  if (recordingLocked.value) {
    window.open(`/c/${slug}`, "_blank", "noopener");
    return;
  }
  emit("close");
  navigateToConversationSlug(slug);
}

function openDirPicker() {
  if (!recordingLocked.value) showDirPicker.value = true;
}

function emitComment(block: string) {
  review.note({ type: "comment", where: "Comment", text: block.trim() });
  emit("comment-text-change", block);
}

watch(
  () => props.isOpen,
  (open) => (open ? review.refresh() : review.abandon()),
  { immediate: true },
);

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
  () => requestClose(),
);

// Keep modeRef in sync + update editor readOnly when mode changes.
watch([mode, selectedFile, selectedDiff, selectedTo], () => {
  modeVal = mode.value;
  // Switching out of comment mode (or files) clears any pending selection prompt.
  commentPrompt.value = null;
  if (diffEditor && selectedFile.value && !isCommitMessageFile(selectedFile.value)) {
    const isWorkingView = selectedDiff.value === "working" || selectedTo.value === "working";
    const readOnly = mode.value === "comment" || !isWorkingView;
    // Only allow drag-and-drop of selected text in edit mode. In comment mode
    // (including editable commit messages) selecting text must not move it.
    const dragAndDrop = mode.value === "edit";
    diffEditor.updateOptions({ readOnly, dragAndDrop, splitViewDefaultRatio: splitViewRatioVal });
    diffEditor.getModifiedEditor().updateOptions({ readOnly, dragAndDrop });
  }
});

// Track viewport size.
function handleResize() {
  isMobile.value = window.innerWidth < 768;
}

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
watch([() => props.isOpen, isMobile, fileDiff, diffView], () => {
  if (
    props.isOpen &&
    diffView.value === "files" &&
    !isMobile.value &&
    !hasShownKeyboardHint &&
    fileDiff.value
  ) {
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
      pendingInitialFile = props.initialFile;
      loadDiffs();
    } else if (!props.isOpen) {
      loadDiffsRequestId++;
      loadFilesRequestId++;
      loadFileDiffRequestId++;
      fileDiff.value = null;
      selectedFile.value = null;
      files.value = [];
      selectedDiff.value = null;
      selectedTo.value = "working";
      diffs.value = [];
      error.value = null;
      resetComments();
      tourCommentTarget.value = null;
      tourCommentText.value = "";
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
const editorHost = document.createElement("div");
editorHost.style.width = "100%";
editorHost.style.height = "100%";

function createEditor() {
  if (!props.isOpen || !monacoLoaded.value || !editorContainerRef.value || !monacoMod) return;
  if (editorHost.parentElement !== editorContainerRef.value) {
    editorContainerRef.value.appendChild(editorHost);
  }
  if (diffEditor) {
    commentsCleanup?.();
    commentsCleanup = attachComments(diffEditor.getModifiedEditor(), editorContainerRef.value);
    return;
  }
  const monaco = monacoMod;
  const initMobile = isMobileVal;
  diffEditor = monaco.editor.createDiffEditor(editorHost, {
    theme: isDarkModeActive() ? "vs-dark" : "vs",
    readOnly: true,
    dragAndDrop: false,
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

  // Comment-mode interactions (click-to-comment, selection prompt, hover
  // indicator) via the shared composable.
  commentsCleanup = attachComments(modEditor, editorContainerRef.value);

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
let commentsCleanup: (() => void) | null = null;

function disposeEditor() {
  if (!diffEditor) return;
  commentsCleanup?.();
  commentsCleanup = null;
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
    } else if (diffEditor) {
      commentsCleanup?.();
      commentsCleanup = null;
      const model = diffEditor.getModel();
      diffEditor.setModel(null);
      model?.original.dispose();
      model?.modified.dispose();
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
    splitViewDefaultRatio: splitViewRatioVal,
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
    // Drag-and-drop of text only in edit mode (never on commit messages).
    const dragAndDrop = !isCommitMsg && modeVal === "edit";
    // For new/deleted files one side is empty; shrink it so the non-empty
    // side gets most of the width instead of wasting half the screen.
    splitViewRatioVal = 0.5;
    if (fd.oldContent === "" && fd.newContent !== "") {
      splitViewRatioVal = EMPTY_SIDE_SPLIT_RATIO;
    } else if (fd.newContent === "" && fd.oldContent !== "") {
      splitViewRatioVal = 1 - EMPTY_SIDE_SPLIT_RATIO;
    }
    diffEditor.updateOptions({
      readOnly,
      dragAndDrop,
      splitViewDefaultRatio: splitViewRatioVal,
    });
    diffEditor.getModifiedEditor().updateOptions({ readOnly, dragAndDrop });

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
  const requestId = ++loadDiffsRequestId;
  const cwd = props.cwd;
  const initialCommit = props.initialCommit;
  try {
    loading.value = true;
    error.value = null;
    const response = await api.getGitDiffs(cwd, initialCommit, false);
    if (requestId !== loadDiffsRequestId) return;
    diffs.value = response.diffs;
    gitRoot.value = response.gitRoot;

    const selection = defaultDiffSelection(response.diffs, initialCommit);
    if (selection) {
      selectedDiff.value = selection.selectedDiff;
      selectedTo.value = selection.selectedTo;
    }
  } catch (err) {
    if (requestId !== loadDiffsRequestId) return;
    const errStr = String(err);
    if (errStr.toLowerCase().includes("not a git repository")) {
      error.value = `Not a git repository: ${cwd}`;
    } else {
      error.value = `Failed to load diffs: ${errStr}`;
    }
  } finally {
    if (requestId === loadDiffsRequestId) loading.value = false;
  }
}

let commitStatsRequestId: number | null = null;
async function loadCommitStats() {
  const requestId = loadDiffsRequestId;
  if (
    commitStatsRequestId === requestId ||
    !diffs.value.some((diff) => diff.statsLoaded === false)
  ) return;
  commitStatsRequestId = requestId;
  try {
    const response = await api.getGitDiffs(props.cwd, props.initialCommit);
    if (requestId !== loadDiffsRequestId) return;
    const byId = new Map(response.diffs.map((diff) => [diff.id, diff]));
    diffs.value = diffs.value.map((diff) => {
      const full = byId.get(diff.id);
      return full ? { ...diff, filesCount: full.filesCount, additions: full.additions,
        deletions: full.deletions, statsLoaded: full.statsLoaded } : diff;
    });
  } catch (err) {
    if (requestId === loadDiffsRequestId) error.value = `Failed to load commit stats: ${err}`;
  } finally {
    if (commitStatsRequestId === requestId) commitStatsRequestId = null;
  }
}

async function loadFiles(diffId: string) {
  const requestId = ++loadFilesRequestId;
  const cwd = props.cwd;
  const toArg = diffId === "working" ? undefined : selectedTo.value;
  const initial = pendingInitialFile;
  pendingInitialFile = undefined;
  const filesPromise = api.getGitDiffFiles(diffId, cwd, toArg);
  const messagesPromise =
    diffId === "working"
      ? Promise.resolve<GitCommitMessage[]>([])
      : api.getGitCommitMessages(cwd, diffId, toArg).catch(() => []);

  loadFileDiffRequestId++;
  selectedFile.value = null;
  fileDiff.value = null;
  try {
    loading.value = true;
    error.value = null;
    const msgs = await messagesPromise;
    if (requestId !== loadFilesRequestId) return;
    commitMessages.value = msgs;
    const commitFileEntries: GitFileInfo[] = msgs.map((msg) => ({
      path: COMMIT_MSG_PREFIX + msg.hash,
      status: "added" as const,
      additions: formatCommitMessage(msg).split("\n").length,
      deletions: 0,
      isGenerated: false,
    }));
    const pickFrom = (list: GitFileInfo[]) => {
      if (initial && list.some((file) => file.path === initial)) return initial;
      return list.length > 0 ? list[0].path : null;
    };
    if (commitFileEntries.length > 0 && (!initial || isCommitMessageFile(initial))) {
      files.value = commitFileEntries;
      selectedFile.value = pickFrom(commitFileEntries);
    }

    const realFiles = (await filesPromise) || [];
    if (requestId !== loadFilesRequestId) return;
    const allFiles = [...commitFileEntries, ...realFiles];
    files.value = allFiles;
    if (!selectedFile.value) selectedFile.value = pickFrom(allFiles);
  } catch (err) {
    if (requestId !== loadFilesRequestId) return;
    error.value = `Failed to load files: ${err}`;
  } finally {
    if (requestId === loadFilesRequestId) loading.value = false;
  }
}

async function loadFileDiff(diffId: string, filePath: string) {
  const requestId = ++loadFileDiffRequestId;
  const cwd = props.cwd;
  const toArg = diffId === "working" ? undefined : selectedTo.value;
  try {
    loading.value = true;
    error.value = null;
    if (isCommitMessageFile(filePath)) {
      const hash = commitHashFromPath(filePath);
      const msg = commitMessages.value.find((m) => m.hash === hash);
      if (msg) {
        if (requestId !== loadFileDiffRequestId) return;
        fileDiff.value = { path: filePath, oldContent: "", newContent: formatCommitMessage(msg) };
      } else if (requestId === loadFileDiffRequestId) {
        error.value = "Commit message not found";
      }
      return;
    }
    const diffData = await api.getGitFileDiff(diffId, filePath, cwd, toArg);
    if (requestId !== loadFileDiffRequestId) return;
    fileDiff.value = diffData;
  } catch (err) {
    if (requestId !== loadFileDiffRequestId) return;
    error.value = `Failed to load file diff: ${err}`;
  } finally {
    if (requestId === loadFileDiffRequestId) loading.value = false;
  }
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
    // If a nested overlay (commit/dir picker, or the image annotation view a
    // tour screenshot opened) is open, let it handle Escape.
    if (
      document.querySelector(".commit-picker-popover") ||
      document.querySelector(".commit-picker-modal") ||
      document.querySelector(".image-comment-overlay")
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
    if (tourCommentTarget.value) {
      tourCommentTarget.value = null;
    } else if (showCommentDialog.value) {
      showCommentDialog.value = null;
    } else {
      requestClose();
    }
    return;
  }
  if (diffView.value === "tour") return;
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
  // Comment mode navigation (only when comment dialogs are closed).
  if (mode.value === "comment" && !showCommentDialog.value && !tourCommentTarget.value) {
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
    return {
      realPath: f.path,
      treePath: f.path.split("/"),
      status: f.status,
      additions: f.additions,
      deletions: f.deletions,
    };
  });
});

const navOrder = computed(() => treeRealPathOrder(treeEntries.value));
watch(navOrder, (v) => (navOrderRef.value = v), { immediate: true });

const tourContents = computed(() =>
  tourResponse.value
    ? buildTourContents(tourResponse.value.tour, selectedTourCommitMessage.value !== null)
    : [],
);

function scrollToTourAnchor(anchor: string) {
  tourViewRef.value?.scrollToAnchor(anchor);
}

function setTourExpanded(anchor: string, expanded: boolean) {
  if (expanded) expandedTourAnchors.value.add(anchor);
  else expandedTourAnchors.value.delete(anchor);
}

function handleTourActiveAnchor(anchor: string) {
  activeTourAnchor.value = anchor;
  nextTick(revealActiveTourContents);
}

function revealActiveTourContents() {
  const container = tourContentsScrollRef.value;
  const anchor = activeTourAnchor.value;
  if (!container || !anchor) return;
  const button = Array.from(container.querySelectorAll<HTMLElement>("[data-tour-target]")).find(
    (element) => element.dataset.tourTarget === anchor,
  );
  if (!button) return;

  const containerRect = container.getBoundingClientRect();
  const buttonRect = button.getBoundingClientRect();
  const edgePadding = 4;
  if (buttonRect.top < containerRect.top + edgePadding) {
    container.scrollTop -= containerRect.top + edgePadding - buttonRect.top;
  } else if (buttonRect.bottom > containerRect.bottom - edgePadding) {
    container.scrollTop += buttonRect.bottom - containerRect.bottom + edgePadding;
  }
}

watch(tourContentsScrollRef, (container) => {
  tourContentsResizeObserver?.disconnect();
  if (container) tourContentsResizeObserver?.observe(container);
});
watch(layout, () => nextTick(revealActiveTourContents), { flush: "post" });

// Title for the sidebar layout's header.
const currentTitleText = computed<string | null>(() => {
  if (diffView.value === "tour") {
    return tourResponse.value?.tour.title || selectedDiff.value?.slice(0, 8) || null;
  }
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
  if (diffView.value === "tour") {
    const commit = diffs.value.find((diff) => diff.id === selectedDiff.value);
    return commit ? `${commit.id}\n\n${commit.message}` : selectedDiff.value;
  }
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
    // Show the whole stack down to (and including) the merge-base.
    list.push(...commitsOnly.slice(0, mergeBaseIdx + 1));
  } else {
    // No merge-base in the window (no upstream, or a stack too deep for
    // the server's cap): show everything the server returned, which is
    // already bounded.
    list.push(...commitsOnly);
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
function selectSidebarFile(path: string) {
  selectedFile.value = path;
  diffView.value = "files";
}

function openTourComment(target: TourCommentTarget) {
  resetComments();
  tourCommentTarget.value = target;
  tourCommentText.value = "";
  tourCommentDialogOpens.value++;
}

function handleAddTourComment() {
  if (!tourCommentTarget.value || !tourCommentText.value.trim()) return;
  emitComment(buildTourCommentBlock(tourCommentTarget.value, tourCommentText.value));
  tourCommentTarget.value = null;
  tourCommentText.value = "";
}

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
  const idle = window.requestIdleCallback ?? ((cb: () => void) => window.setTimeout(cb, 2000));
  idle(() => {
    loadMonaco()
      .then((monaco) => {
        monacoMod = monaco;
        monacoLoaded.value = true;
      })
      .catch(() => {});
  });
  tourContentsResizeObserver = new ResizeObserver(revealActiveTourContents);
  if (tourContentsScrollRef.value) tourContentsResizeObserver.observe(tourContentsScrollRef.value);
  window.addEventListener("resize", handleResize);
});
onUnmounted(() => {
  tourContentsResizeObserver?.disconnect();
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
