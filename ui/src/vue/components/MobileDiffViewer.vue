<!-- Vue port of components/DiffViewer.tsx. Monaco diff modal with a file tree
     (DiffFileTree.vue), commit selection (CommitPicker.vue),
     comments, edit/auto-save, and vim (useMonacoVim +
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
    <div ref="containerRef" class="diff-viewer-container diff-viewer-rethink" :class="{ 'diff-viewer-readonly': mode === 'comment' || !editableSelection }">
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
              @open="onComparisonOpen"
            />

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
          :aria-disabled="recordingLocked || onVm"
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
                  @open="onComparisonOpen"
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
              {{ selectedMessage ? "Commit message" : currentTitleText ?? "\u00a0" }}
              <span v-if="currentRealFile" class="diff-rethink-current-counts"><span class="added">+{{ currentRealFile.additions }}</span> <span class="deleted">−{{ currentRealFile.deletions }}</span></span>
            </div>
            <ReviewRecordingStatus
              v-if="recordingLocked"
              :phase="review.phase.value"
              :levels="review.levels"
              :current="review.current"
            />
          </div>

          <div class="diff-viewer-controls-row">
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
              :disabled="!fileDiff || binaryPanel"
                aria-label="Previous change (,)"
                @click="goToPreviousChange"
              >
                <span v-html="PREV_CHANGE_ICON" />
              </button>
              <button
                v-tooltip.top="'Next change (.)'"
                class="diff-viewer-nav-btn"
              :disabled="!fileDiff || binaryPanel"
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
                Review
              </button>
              <button
                v-tooltip.top="'Edit mode'"
                :class="`diff-viewer-mode-btn ${mode === 'edit' ? 'active' : ''}`"
                aria-label="Edit mode"
                @click="mode = 'edit'"
              >
                Edit file
              </button>
            </div>
            <span v-if="!selectedMessage && fileDiff" class="diff-rethink-change-count">{{ changeLabel }}</span>
            <VimToggle :enabled="vimEnabled" @change="setVimEnabled" />
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
              :aria-disabled="recordingLocked || onVm"
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

      <!-- Error banner -->
      <div v-if="error" class="diff-viewer-error">{{ error }}</div>
      <div v-else-if="highlightsError" class="diff-viewer-error">
        {{ highlightsError }}
      </div>

      <!-- Main content -->
      <div
        :class="`diff-viewer-content${!isMobile && layout === 'sidebar' ? ' diff-viewer-content-sidebar' : ''}`"
      >
        <aside v-if="!isMobile && layout === 'sidebar'" class="diff-viewer-sidebar">
          <div class="diff-viewer-sidebar-section diff-viewer-sidebar-comparison" data-review="Commits">
            <div class="diff-rethink-comparison-label"><span>Comparison</span><button class="diff-viewer-sidebar-toggle" aria-label="Hide sidebar" @click="layout = 'header'">‹</button></div>
            <CommitPicker :diffs="diffs" :selected-diff="selectedDiff" :selected-to="selectedTo" :is-mobile="false" @change="onCommitChange" @open="onComparisonOpen" />
          </div>
          <div
            class="diff-viewer-sidebar-section diff-viewer-sidebar-files"
            data-review="Changed files"
          >
            <div class="diff-viewer-sidebar-label">
              <span>{{
                highlights ? "Commit Messages and Files by Importance" : "Commit Messages and Files"
              }}</span>
              <span class="diff-rethink-totals"><span class="added">+{{ totalAdditions }}</span> <span class="deleted">−{{ totalDeletions }}</span></span>
            </div>
            <div class="diff-viewer-sidebar-files-scroll">
              <div class="diff-viewer-file-list" aria-label="Files">
                <div v-if="files.length === 0" class="diff-viewer-file-list-empty">No files</div>
                <div v-else class="diff-viewer-file-tree-wrap">
                  <DiffFileTree
                    :entries="treeEntries"
                    :selected-real-path="selectedFile"
                    :ordered="!!highlights"
                    @select="selectSidebarFile"
                  />
                </div>
              </div>
            </div>
          </div>
        </aside>
        <button v-if="isMobile && mobileFileListOpen && !mobileOverview" class="diff-rethink-file-shade" aria-label="Close file picker" @click="mobileFileListOpen = false" />
        <aside v-if="isMobile" v-show="mobileOverview || mobileFileListOpen" class="diff-rethink-mobile-files" :class="{ 'is-overview': mobileOverview }" :inert="recordingLocked" :role="mobileFileListOpen ? 'dialog' : undefined" :aria-label="mobileFileListOpen ? 'Choose file' : undefined">
          <h2 v-if="mobileOverview" class="diff-rethink-opening-title">Uncommitted changes</h2>
          <div class="diff-rethink-file-heading"><span>{{ mobileOverview ? 'Files' : 'Choose file' }} <small>{{ realFiles.length }}</small></span><span v-if="mobileOverview" class="diff-rethink-totals"><span class="added">+{{ totalAdditions }}</span> <span class="deleted">−{{ totalDeletions }}</span></span><button v-if="!mobileOverview" class="btn-text" @click="mobileFileListOpen = false">Close</button></div>
          <DiffFileTree :entries="groupedTreeEntries" :selected-real-path="mobileOverview ? null : selectedFile" :ordered="!!highlights" @select="selectSidebarFile" />
        </aside>
        <div ref="mainRef" class="diff-viewer-main" v-show="!isMobile || !mobileOverview">
          <nav v-if="isMobile && !mobileOverview" class="diff-rethink-filebar" aria-label="Files and commit messages">
            <button :disabled="!hasPrevFile" aria-label="Previous file (<)" @click="goToPreviousFile">‹</button>
            <button
              class="diff-rethink-file-jump"
              :class="{ 'is-dragging': fileSwipeOffset !== 0, 'is-open': mobileFileListOpen }"
              :disabled="recordingLocked"
              :aria-expanded="mobileFileListOpen"
              @click="toggleMobileFileList"
              @touchstart.capture.passive="startFileSwipe"
              @touchmove.capture.passive="moveFileSwipe"
              @touchend.capture.passive="endFileSwipe"
              @touchcancel.capture="cancelFileSwipe"
            >
              <span class="diff-rethink-file-jump-window">
                <span :key="selectedFile ?? ''" class="diff-rethink-file-jump-content" :class="fileSwipeDirection ? `diff-file-${fileSwipeDirection}-enter` : ''" :style="{ transform: `translateX(${fileSwipeOffset}px)` }" @animationend="resetFileSwipeAnimation">
                  <span class="diff-rethink-file-jump-top"><small>{{ selectedMessage ? `${selectedMessage.hash.slice(0,8)} · ${selectedMessage.subject}` : selectedFile?.slice(0, Math.max(0, selectedFile.lastIndexOf('/'))) }}</small><span class="diff-rethink-file-position">{{ mobileFileListOpen ? 'Close list' : selectedMessage ? `${realFiles.length} files` : realFilePosition ?? 'Files' }} <span class="diff-rethink-file-chevron" aria-hidden="true">▾</span></span></span>
                  <strong><span class="diff-rethink-file-name">{{ selectedMessage ? 'Commit message' : selectedFile?.split('/').pop() || 'Choose file' }}</span><span v-if="currentRealFile" class="diff-rethink-current-counts"><span class="added">+{{ currentRealFile.additions }}</span> <span class="deleted">−{{ currentRealFile.deletions }}</span></span></strong>
                </span>
              </span>
            </button>
            <button :disabled="!hasNextFile" aria-label="Next file (>)" @click="goToNextFile()">›</button>
          </nav>
          <div v-if="mode === 'edit'" class="diff-rethink-edit-banner"><div><strong>{{ selectedMessage ? 'Editing commit message' : 'Editing file' }}</strong><span>{{ editableSelection ? 'Changes autosave · not a comment' : 'This comparison is read-only' }}</span></div><button class="btn-text" @click="mode = 'comment'">Review diff</button></div>
          <div v-if="isMobile && !selectedMessage" class="diff-rethink-mobile-tools" :inert="mobileFileListOpen">
            <div class="diff-rethink-mode-switch" role="group" aria-label="Diff mode">
              <button type="button" :class="{ active: mode === 'comment' }" :aria-pressed="mode === 'comment'" @click="mode = 'comment'">Comment</button>
              <button type="button" :class="{ active: mode === 'edit' }" :aria-pressed="mode === 'edit'" @click="mode = 'edit'">Edit</button>
            </div>
            <span class="diff-rethink-change-label">{{ changeLabel }}</span>
            <div class="diff-rethink-change-nav">
              <button aria-label="Previous change (,)" :disabled="!fileDiff" @click="goToPreviousChange">↑</button>
              <button aria-label="Next change (.)" :disabled="!fileDiff" @click="goToNextChange">↓</button>
            </div>
          </div>
          <DiffCommitMessage v-if="selectedMessage && mode === 'comment' && !rawMessage" :message="selectedMessage" @raw="showRawMessage" @edit="editCommitMessage" @comment="commentOnMessage"
            @touchstart.capture.passive="startMessageSwipe"
            @touchmove.capture.passive="moveFileSwipe"
            @touchend.capture.passive="endMessageSwipe"
            @touchcancel.capture="cancelFileSwipe">
            <section v-if="isMobile && realFiles.length" class="diff-rethink-message-files" aria-label="Changed files">
              <div class="diff-rethink-file-heading"><span>Files <small>{{ realFiles.length }}</small></span><span class="diff-rethink-totals"><span class="added">+{{ totalAdditions }}</span> <span class="deleted">−{{ totalDeletions }}</span></span></div>
              <DiffFileTree :entries="groupedRealTreeEntries" :selected-real-path="null" :ordered="!!highlights" @select="selectSidebarFile" />
            </section>
          </DiffCommitMessage>
          <div v-if="selectedMessage && mode === 'comment' && rawMessage" class="diff-rethink-raw-heading"><span>Raw commit message</span><button class="btn-text" @click="rawMessage = false">Rendered</button><button v-if="selectedMessage.isHead" class="btn-text" @click="editCommitMessage">Edit message</button></div>
          <div class="diff-viewer-files-pane" data-review="Files" v-show="!selectedMessage || mode === 'edit' || rawMessage" :inert="mobileFileListOpen">
            <div v-if="loading && !fileDiff" class="diff-viewer-loading">
              <div class="spinner"></div>
              <span>Loading...</span>
            </div>
            <div v-if="!loading && !binaryPanel && !monacoLoaded && !error" class="diff-viewer-loading">
              <div class="spinner"></div>
              <span>Loading editor...</span>
            </div>
            <div v-if="!loading && monacoLoaded && !fileDiff && !error" class="diff-viewer-empty">
              <p>Select a diff and file to view changes.</p>
              <p class="diff-viewer-hint">
                Click a line to comment, or select text and click Comment.
              </p>
            </div>
            <div v-if="fileDiff?.binary" class="diff-viewer-binary-toolbar">
              <button v-if="showBinaryAsText" type="button" class="btn-secondary" :disabled="loading" @click="showBinaryAsText = false">Binary view</button>
              <button v-else type="button" class="btn-secondary" :disabled="loading" @click="showBinaryText">View as text</button>
            </div>
            <div v-if="fileDiff && binaryPanel" class="diff-viewer-binary" data-testid="binary-file-panel">
              <div class="diff-viewer-binary-sides">
                <div class="diff-viewer-binary-side" :class="{ 'diff-viewer-binary-side-empty': !fileDiff.oldExists }" :style="{ flex: binaryBeforeRatio }">
                  <div v-if="fileDiff.oldExists" class="diff-viewer-binary-side-content">
                    <div v-if="fileDiff.oldImage" class="diff-viewer-binary-image"><img :src="binaryMediaURL('before')" :alt="`Before: ${selectedFile}`" /></div>
                    <div v-else-if="fileDiff.oldAudio" class="diff-viewer-binary-audio"><audio :src="binaryMediaURL('before')" :aria-label="`Before: ${selectedFile}`" controls preload="metadata" /></div>
                    <div v-else class="diff-viewer-binary-line">Binary file</div>
                    <a v-if="!fileDiff.oldImage && !fileDiff.oldAudio" class="diff-viewer-binary-download" :href="binaryDownloadURL('before')" download>Download</a>
                  </div>
                </div>
                <div class="diff-viewer-binary-side" :class="{ 'diff-viewer-binary-side-empty': !fileDiff.newExists }" :style="{ flex: 1 - binaryBeforeRatio }">
                  <div v-if="fileDiff.newExists" class="diff-viewer-binary-side-content">
                    <div v-if="fileDiff.newImage" class="diff-viewer-binary-image"><img :src="binaryMediaURL('after')" :alt="`After: ${selectedFile}`" /></div>
                    <div v-else-if="fileDiff.newAudio" class="diff-viewer-binary-audio"><audio :src="binaryMediaURL('after')" :aria-label="`After: ${selectedFile}`" controls preload="metadata" /></div>
                    <div v-else class="diff-viewer-binary-line">Binary file</div>
                    <a v-if="!fileDiff.newImage && !fileDiff.newAudio" class="diff-viewer-binary-download" :href="binaryDownloadURL('after')" download>Download</a>
                  </div>
                </div>
              </div>
            </div>
            <div
              ref="editorContainerRef"
              class="diff-viewer-editor"
              :style="{ display: fileDiff && !binaryPanel && monacoLoaded ? 'block' : 'none', visibility: isMobile && revealPending ? 'hidden' : 'visible' }"
              @touchstart.capture.passive="startCodeSwipe"
              @touchmove.capture.passive="moveFileSwipe"
              @touchend.capture.passive="endFileSwipe"
              @touchcancel.capture="cancelFileSwipe"
            />
            <div v-if="isMobile && revealPending && fileDiff && !binaryPanel && monacoLoaded" class="diff-rethink-reveal-wait">Loading changes…</div>
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

      <!-- Comment dialog -->
      <CommentDialog
        v-if="showCommentDialog"
        :key="commentDialogOpens"
        v-model:text="commentText"
        :where="lineCommentLabel(showCommentDialog, true)"
        :quoted="
          showCommentDialog.highlight
            ? `${showCommentDialog.selectedText}\n${highlightQuote(showCommentDialog.highlight)}`
            : showCommentDialog.selectedText
        "
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
import "./mobileDiffViewer.css";
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from "vue";
import type * as Monaco from "monaco-editor";
import { api, onMachine } from "../../services/api";
import { loadMonaco } from "../../services/monaco";
import { isDarkModeActive } from "../../services/theme";
import { useVimEnabled, useMonacoVim } from "../composables/monacoVim";
import {
  highlightQuote,
  lineCommentLabel,
  truncateWithEllipsis,
  useMonacoComments,
} from "../composables/monacoComments";
import VimToggle from "./VimToggle.vue";
import CommentDialog from "./CommentDialog.vue";
import CommitPicker from "./CommitPicker.vue";

import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import DiffFileTree from "./MobileDiffFileTree.vue";
import DiffCommitMessage from "./DiffCommitMessage.vue";
import ReviewRecordButton from "./ReviewRecordButton.vue";
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
import { COMMIT_MESSAGES_DIR, realPathOrder, type DiffFileTreeEntry } from "./diffFileTree";
import { highlightsByFile, orderByHighlights, showHighlights } from "./commitHighlights";
import { defaultDiffSelection } from "./mobileDiffViewerModel";
import type {
  CommitHighlights,
  GitDiffInfo,
  GitFileInfo,
  GitFileDiff,
  GitCommitMessage,
} from "../../types";

const props = defineProps<{
  cwd: string;
  // The conversation whose machine (this host, or its VM) cwd is on.
  conversationId?: string;
  // cwd is on that conversation's VM, where the repository picker, which
  // lists this host's repositories, can't pick.
  onVm?: boolean;
  isOpen: boolean;
  initialCommit?: string;
  initialTo?: "working" | "self";
  // File to select once initialCommit's file list loads (e.g. from the git
  // graph diffstat). Consumed on the first load only.
  initialFile?: string;
  // Conversation that receives narrated review recordings. Without one, each
  // recording starts a conversation (startReviewConversation), shown once the
  // recording is sent (followReviewConversation); with neither, no Record.
  recordingConversationId?: string;
  startReviewConversation?: () => ReviewConversationStart;
  followReviewConversation?: () => (conversationId: string) => void;
}>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "comment-text-change", text: string): void;
  (e: "cwd-change", cwd: string): void;
}>();

type ViewMode = "comment" | "edit";

const COMMIT_MSG_PREFIX = "commit-message:";
const MOBILE_LINE_DECORATIONS_WIDTH = 14;
const DESKTOP_LINE_DECORATIONS_WIDTH = 10;
const MOBILE_SCROLLBAR_SIZE = 8;
const DESKTOP_VERTICAL_SCROLLBAR_SIZE = 14;
const DESKTOP_HORIZONTAL_SCROLLBAR_SIZE = 10;
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
const files = ref<GitFileInfo[]>([]);
const selectedFile = ref<string | null>(null);
defineExpose({
  getSelection: () => ({
    diff: selectedDiff.value,
    to: selectedTo.value,
    file: selectedFile.value,
  }),
});
// Highlights for the selected single commit, if it has any; they order
// `files` and annotate lines in the editor.
const highlights = ref<CommitHighlights | null>(null);
// Files still load, unranked, when highlights fail to.
const highlightsError = ref<string | null>(null);
const highlightsByPath = computed(() => highlightsByFile(highlights.value));
let pendingInitialFile: string | undefined;
let loadDiffsRequestId = 0;
let loadFilesRequestId = 0;
let loadFileDiffRequestId = 0;
const fileDiff = ref<GitFileDiff | null>(null);
const showBinaryAsText = ref(false);
const binaryPanel = computed(() => !!fileDiff.value?.binary && !showBinaryAsText.value);
const binaryBeforeRatio = computed(() => !fileDiff.value?.oldExists ? EMPTY_SIDE_SPLIT_RATIO : !fileDiff.value?.newExists ? 1 - EMPTY_SIDE_SPLIT_RATIO : 0.5);
function showBinaryText() {
  if (selectedDiff.value && selectedFile.value) void loadFileDiff(selectedDiff.value, selectedFile.value, true);
}
function binaryDownloadURL(side: "before" | "after"): string {
  if (!selectedDiff.value || !selectedFile.value) return "";
  return api.gitFileDiffURL(selectedDiff.value, selectedFile.value, props.cwd, props.conversationId,
    selectedDiff.value === "working" ? undefined : selectedTo.value) + `&download=${side}`;
}
function binaryMediaURL(side: "before" | "after"): string {
  return binaryDownloadURL(side).replace(`&download=${side}`, `&preview=${side}`);
}
const revealPending = ref(false);
const loading = ref(false);
const error = ref<string | null>(null);
const monacoLoaded = ref(false);
const currentChangeIndex = ref(-1);
const saveStatus = ref<"idle" | "saving" | "saved" | "error">("idle");
const mode = ref<ViewMode>("comment");
const commitMessages = ref<GitCommitMessage[]>([]);
const amendStatus = ref<"idle" | "saving" | "saved" | "error">("idle");
const showKeyboardHint = ref(false);
const isMobile = ref(window.innerWidth < 768);
const mobileOverview = ref(isMobile.value && !props.initialFile);
const mobileFileListOpen = ref(false);
const rawMessage = ref(false);
const fullFile = ref(true);
const wrapLines = ref(true);
const showLineNumbers = ref(false);
const ignoreWhitespace = ref(false);
const splitView = ref(true);
const editorNarrow = ref(window.innerWidth <= 900);
const changeCount = ref(0);
const realFiles = computed(() => files.value.filter(f => !isCommitMessageFile(f.path)));
const totalAdditions = computed(() => realFiles.value.reduce((n,f) => n + f.additions, 0));
const totalDeletions = computed(() => realFiles.value.reduce((n,f) => n + f.deletions, 0));
const currentRealFile = computed(() => realFiles.value.find(f => f.path === selectedFile.value));
const selectedMessage = computed(() => selectedFile.value && isCommitMessageFile(selectedFile.value) ? commitMessages.value.find(m => m.hash === commitHashFromPath(selectedFile.value!)) : undefined);
const overviewMessage = computed(() => selectedMessage.value ?? commitMessages.value[0]);
const editableSelection = computed(() => selectedMessage.value ? !!selectedMessage.value.isHead : selectedDiff.value === 'working' || selectedTo.value === 'working');
const changeLabel = computed(() => changeCount.value ? `Change ${Math.max(0,currentChangeIndex.value)+1} of ${changeCount.value}` : 'No changes');
function onComparisonOpen(){ mobileFileListOpen.value = false; loadCommitStats(); }
function showRawMessage(){ const msg = overviewMessage.value; if(msg) selectedFile.value = COMMIT_MSG_PREFIX + msg.hash; rawMessage.value = true; mode.value = 'comment'; mobileOverview.value = false; }
function editCommitMessage(){ showRawMessage(); mode.value = 'edit'; }
function commentOnMessage(text:string){
  const msg=overviewMessage.value;if(!msg)return;
  const source=formatCommitMessage(msg);
  const exact=source.indexOf(text);
  const wrapped=exact<0 && text.trim()
    ? source.match(new RegExp(text.trim().replace(/[.*+?^${}()|[\]\\]/g,"\\$&").replace(/\s+/g,"\\s+"),"u"))
    : null;
  const offset=exact>=0?exact:wrapped?.index??-1;
  if(offset<0){showRawMessage();return;}
  const startLine=source.slice(0,offset).split('\n').length;
  selectedFile.value=COMMIT_MSG_PREFIX+msg.hash;commentDialogOpens.value++;
  showCommentDialog.value={line:startLine,side:'right',selectedText:text,startLine,endLine:startLine+(wrapped?.[0]??text).split('\n').length-1};
}


const [vimEnabledRef, setVimEnabledFn] = useVimEnabled();
const vimEnabled = vimEnabledRef;
function setVimEnabled(v: boolean) {
  setVimEnabledFn(v);
}

const layout = ref<"header" | "sidebar">(
  (() => {
    try {
      const v = localStorage.getItem("diff-viewer-layout");
      return v === "header" ? "header" : "sidebar";
    } catch {
      return "sidebar";
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
  commentOnHighlight,
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
  if (!diffEditor || binaryPanel.value) return [];
  return [
    [diffEditor.getModifiedEditor(), "new"],
    [diffEditor.getOriginalEditor(), "old"],
  ];
}

const reviewContext: ReviewContext = {
  view() {
    const commit = commitLabel(selectedDiff.value);
    const file = !mobileOverview.value && selectedFile.value ? reviewFileLabel(selectedFile.value) : undefined;
    return {
      where: ["Files", commit, mobileOverview.value ? "Changed files" : file].filter(Boolean).join(" · "),
      mode: "files",
      commit: selectedDiff.value ?? undefined,
      file,
    };
  },
  pointAt(x, y, element) {
    if (!selectedFile.value) return undefined;
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
    if(mobileOverview.value || (selectedMessage.value && mode.value === "comment" && !rawMessage.value))return undefined;
    if (!selectedFile.value) return undefined;
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
    if(mobileOverview.value)return [`${realFiles.value.length} changed files`];
    if(selectedMessage.value && mode.value === "comment" && !rawMessage.value)return [`Commit ${selectedMessage.value.hash.slice(0,8)} message`];
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

function openDirPicker() {
  if (!recordingLocked.value && !props.onVm) showDirPicker.value = true;
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
  if (diffEditor && selectedFile.value) {
    const isWorkingView = selectedDiff.value === "working" || selectedTo.value === "working";
    const readOnly = !!fileDiff.value?.binary || mode.value === "comment" || (isCommitMessageFile(selectedFile.value) ? !selectedMessage.value?.isHead : !isWorkingView);
    // Only allow drag-and-drop of selected text in edit mode. In comment mode
    // (including editable commit messages) selecting text must not move it.
    const dragAndDrop = !fileDiff.value?.binary && mode.value === "edit";
    diffEditor.updateOptions({ readOnly, domReadOnly: readOnly, tabIndex: readOnly ? -1 : 0, renderLineHighlight: readOnly ? "none" : "line", dragAndDrop, splitViewDefaultRatio: splitViewRatioVal });
    diffEditor.getModifiedEditor().updateOptions({ readOnly, domReadOnly: readOnly, tabIndex: readOnly ? -1 : 0, renderLineHighlight: readOnly ? "none" : "line", dragAndDrop });
    diffEditor.getOriginalEditor().updateOptions({ readOnly: true, domReadOnly: true, tabIndex: -1, renderLineHighlight: "none" });
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
      mobileOverview.value = isMobile.value && !props.initialFile;
      mobileFileListOpen.value = false;
      rawMessage.value = false;
      pendingInitialFile = props.initialFile;
      loadDiffs();
    } else if (!props.isOpen) {
      loadDiffsRequestId++;
      loadFilesRequestId++;
      loadFileDiffRequestId++;
      fileDiff.value = null;
      revealPending.value = false;
      selectedFile.value = null;
      files.value = [];
      highlights.value = null;
      highlightsError.value = null;
      selectedDiff.value = null;
      selectedTo.value = "working";
      diffs.value = [];
      error.value = null;
      resetComments();
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
    domReadOnly: true,
    tabIndex: -1,
    renderLineHighlight: "none",
    dragAndDrop: false,
    originalEditable: false,
    automaticLayout: true,
    renderSideBySide: splitView.value && !initMobile,
    renderSideBySideInlineBreakpoint:900,
    hideUnchangedRegions:{enabled:!fullFile.value,contextLineCount:3,minimumLineCount:6,revealLineCount:10},
    ignoreTrimWhitespace:ignoreWhitespace.value,
    renderOverviewRuler:true,
    enableSplitViewResizing: true,
    renderIndicators: true,
    renderMarginRevertIcon: false,
    lineNumbers: showLineNumbers.value ? "on" : "off",
    minimap: { enabled: false },
    scrollBeyondLastLine: true,
    smoothScrolling: initMobile,
    wordWrap: wrapLines.value ? "on" : "off",
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
    overviewRulerLanes: 0,
    quickSuggestions: false,
    suggestOnTriggerCharacters: false,
    lightbulb: { enabled: false },
    codeLens: false,
    contextmenu: false,
    links: false,
    folding: !initMobile,
    padding: {bottom:16},
  });

  diffEditor.onDidUpdateDiff(() => {changeCount.value=diffEditor?.getLineChanges()?.length??0;});
  editorSizeObserver?.disconnect();
  editorSizeObserver = new ResizeObserver(() => {
    editorNarrow.value = editorHost.clientWidth <= 900;
    if (editorHost.clientHeight > 0) initialReveal?.();
  });
  editorSizeObserver.observe(editorHost);
  editorHost.addEventListener('click', expandHiddenContext);
  const modEditor = diffEditor.getModifiedEditor();
  diffEditor.getOriginalEditor().updateOptions({ domReadOnly: true, tabIndex: -1, renderLineHighlight: "none" });
  modifiedEditor.value = modEditor;

  // Comment-mode interactions (click-to-comment, selection prompt, hover
  // indicator) via the shared composable.
  commentsCleanup = attachComments(modEditor, editorContainerRef.value);

  // Single content change listener; branches on current file context.
  modEditor.onDidChangeModelContent(() => {
    if (currentFileIsHeadCommit) {
      const newMessage=modEditor.getValue(),cwd=cwdVal,conversationId=props.conversationId;
      const message = commitMessages.value.find(m => m.isHead);
      if(message){
        const [subject,...body]=newMessage.split("\n");
        message.subject=subject;message.body=body.join("\n").replace(/^\n/, "");
      }
      if (amendTimeout) clearTimeout(amendTimeout);
      amendStatus.value = "saving";
      amendTimeout = window.setTimeout(async () => {
        try {
          await api.amendGitMessage(cwd, conversationId, newMessage);
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
let editorSizeObserver:ResizeObserver|null=null;
let initialReveal: (() => void) | null = null;
function expandHiddenContext(e:MouseEvent){const target=e.target as Element,bar=target.closest('.diff-hidden-lines'),link=bar?.querySelector('a');if(link&&!link.contains(target)){e.preventDefault();(link as HTMLElement).click();}}
let commentsCleanup: (() => void) | null = null;
let highlightsCleanup: (() => void) | null = null;

function disposeEditor() {
  if (!diffEditor) return;
  initialReveal = null;
  commentsCleanup?.();
  commentsCleanup = null;
  highlightsCleanup?.();
  highlightsCleanup = null;
  const model = diffEditor.getModel();
  editorSizeObserver?.disconnect();
  editorHost.removeEventListener('click',expandHiddenContext);
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
      initialReveal = null;
      commentsCleanup?.();
      commentsCleanup = null;
      highlightsCleanup?.();
      highlightsCleanup = null;
      const model = diffEditor.getModel();
      diffEditor.setModel(null);
      model?.original.dispose();
      model?.modified.dispose();
    }
  },
  { immediate: true, flush: "post" },
);

watch([isMobile, fullFile, wrapLines, showLineNumbers, ignoreWhitespace, splitView], () => {
  isMobileVal = isMobile.value;
  applyDisplayOptions();
});
function applyDisplayOptions(){if(!diffEditor)return;diffEditor.updateOptions({renderSideBySide:splitView.value&&!isMobile.value,renderSideBySideInlineBreakpoint:900,lineNumbers:showLineNumbers.value?'on':'off',glyphMargin:!isMobile.value,lineDecorationsWidth:isMobile.value?MOBILE_LINE_DECORATIONS_WIDTH:DESKTOP_LINE_DECORATIONS_WIDTH,lineNumbersMinChars:showLineNumbers.value?3:0,wordWrap:wrapLines.value?'on':'off',smoothScrolling:isMobile.value,ignoreTrimWhitespace:ignoreWhitespace.value,hideUnchangedRegions:{enabled:!fullFile.value,contextLineCount:3,minimumLineCount:6,revealLineCount:10},renderOverviewRuler:true,overviewRulerLanes:0,folding:!isMobile.value,padding:{bottom:16},splitViewDefaultRatio:splitViewRatioVal});}

// Swap models into the existing editor when fileDiff changes.
let diffUpdateDisposable: Monaco.IDisposable | null = null;
watch(
  [monacoLoaded, fileDiff, showBinaryAsText],
  () => {
    if (!monacoLoaded.value || !fileDiff.value || binaryPanel.value || !diffEditor || !monacoMod) return;
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
    highlightsCleanup?.();
    highlightsCleanup = null;
    diffEditor.setModel({ original: originalModel, modified: modifiedModel });
    prev?.original.dispose();
    prev?.modified.dispose();

    const isWorkingView = selectedDiff.value === "working" || selectedTo.value === "working";
    const readOnly = isCommitMsg ? !isHeadCommit || modeVal === "comment" : modeVal === "comment" || !isWorkingView;
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
      domReadOnly: readOnly,
      tabIndex: readOnly ? -1 : 0,
      renderLineHighlight: readOnly ? "none" : "line",
      dragAndDrop,
      splitViewDefaultRatio: splitViewRatioVal,
    });
    diffEditor.getModifiedEditor().updateOptions({ readOnly, domReadOnly: readOnly, tabIndex: readOnly ? -1 : 0, renderLineHighlight: readOnly ? "none" : "line", dragAndDrop });
    diffEditor.getOriginalEditor().updateOptions({ readOnly: true, domReadOnly: true, tabIndex: -1, renderLineHighlight: "none" });

    const fileHighlights = isCommitMsg ? [] : (highlightsByPath.value.get(fd.path) ?? []);
    if (fileHighlights.length > 0) {
      const editor = diffEditor.getModifiedEditor();
      highlightsCleanup = showHighlights(monaco, editor, fileHighlights, (highlight) =>
        commentOnHighlight(
          highlight.line,
          editor.getModel()?.getLineContent(highlight.line) ?? "",
          highlight.comment,
        ),
      );
    }
    // Open at the file's first highlight, else its first change.
    const revealLine = fileHighlights[0]?.line ?? null;

    let hasScrolledToFirstChange = false;
    const scrollToFirstChange = () => {
      requestAnimationFrame(() => {
        if (hasScrolledToFirstChange || !diffEditor ||
            fileDiff.value !== fd || selectedFile.value !== fd.path ||
            diffEditor.getModel()?.modified !== modifiedModel ||
            editorHost.clientHeight === 0) return;
        const changes = diffEditor.getLineChanges();
        // Wait for the diff so its alignment doesn't shift the revealed line.
        if (changes && revealLine !== null) {
          hasScrolledToFirstChange = true;
          revealHighlight(revealLine);
        } else if (changes && changes.length > 0) {
          hasScrolledToFirstChange = true;
          const firstChange = changes[0];
          const targetLine = firstChange.modifiedStartLineNumber || 1;
          const editor = diffEditor.getModifiedEditor();
          editor.revealLineInCenter(targetLine, monaco.editor.ScrollType.Immediate);
          editor.setPosition({ lineNumber: targetLine, column: 1 });
          currentChangeIndex.value = 0;
        } else if (changes) {
          hasScrolledToFirstChange = true;
          diffEditor.getModifiedEditor().setScrollTop(0);
        }
        if (hasScrolledToFirstChange) revealPending.value = false;
      });
    };
    diffUpdateDisposable?.dispose();
    diffUpdateDisposable = diffEditor.onDidUpdateDiff(scrollToFirstChange);
    initialReveal = scrollToFirstChange;
    scrollToFirstChange();
  },
  { flush: "post" },
);

function revealHighlight(line: number) {
  if (!diffEditor || !monacoMod) return;
  const editor = diffEditor.getModifiedEditor();
  editor.revealLineInCenter(line, monacoMod.editor.ScrollType.Immediate);
  editor.setPosition({ lineNumber: line, column: 1 });
  // Change navigation continues from the highlight.
  const changes = diffEditor.getLineChanges() ?? [];
  let index = -1;
  for (let i = 0; i < changes.length; i++) {
    if ((changes[i].modifiedStartLineNumber || 1) <= line) index = i;
  }
  currentChangeIndex.value = index;
}

// --- Data loaders ---
async function loadDiffs() {
  const requestId = ++loadDiffsRequestId;
  const cwd = props.cwd;
  const initialCommit = props.initialCommit;
  try {
    loading.value = true;
    error.value = null;
    const response = await api.getGitDiffs(cwd, props.conversationId, initialCommit === "working" ? undefined : initialCommit, false);
    if (requestId !== loadDiffsRequestId) return;
    diffs.value = response.diffs;
    gitRoot.value = response.gitRoot;

    const selection = defaultDiffSelection(response.diffs, initialCommit);
    if (selection) {
      selectedDiff.value = selection.selectedDiff;
      selectedTo.value = props.initialTo ?? selection.selectedTo;
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
  if (commitStatsRequestId === requestId || !diffs.value.some((diff) => diff.statsLoaded === false))
    return;
  commitStatsRequestId = requestId;
  try {
    const response = await api.getGitDiffs(props.cwd, props.conversationId, props.initialCommit === "working" ? undefined : props.initialCommit);
    if (requestId !== loadDiffsRequestId) return;
    const byId = new Map(response.diffs.map((diff) => [diff.id, diff]));
    diffs.value = diffs.value.map((diff) => {
      const full = byId.get(diff.id);
      return full
        ? {
            ...diff,
            filesCount: full.filesCount,
            additions: full.additions,
            deletions: full.deletions,
            statsLoaded: full.statsLoaded,
          }
        : diff;
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
  const filesPromise = api.getGitDiffFiles(diffId, cwd, props.conversationId, toArg);
  const messagesPromise =
    diffId === "working"
      ? Promise.resolve<GitCommitMessage[]>([])
      : api.getGitCommitMessages(cwd, props.conversationId, diffId, toArg).catch(() => []);
  // Highlights describe one commit, so they apply only to its own diff.
  const hasHighlights =
    toArg === "self" && !!diffs.value.find((diff) => diff.id === diffId)?.hasHighlights;
  const highlightsPromise = hasHighlights
    ? api.getGitHighlights(cwd, props.conversationId, diffId).catch((err) => {
        if (requestId === loadFilesRequestId) highlightsError.value = String(err);
        return null;
      })
    : Promise.resolve(null);

  loadFileDiffRequestId++;
  selectedFile.value = null;
  fileDiff.value = null;
  highlights.value = null;
  highlightsError.value = null;
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
      if (isMobile.value) mobileOverview.value = false;
    }

    const [realFiles, loadedHighlights] = await Promise.all([filesPromise, highlightsPromise]);
    if (requestId !== loadFilesRequestId) return;
    highlights.value = loadedHighlights;
    const allFiles = [
      ...commitFileEntries,
      ...(loadedHighlights
        ? orderByHighlights(realFiles || [], loadedHighlights.files)
        : realFiles || []),
    ];
    files.value = allFiles;
    if (!selectedFile.value) selectedFile.value = pickFrom(allFiles);
  } catch (err) {
    if (requestId !== loadFilesRequestId) return;
    error.value = `Failed to load files: ${err}`;
  } finally {
    if (requestId === loadFilesRequestId) loading.value = false;
  }
}

async function loadFileDiff(diffId: string, filePath: string, asText = false) {
  const requestId = ++loadFileDiffRequestId;
  const cwd = props.cwd;
  const toArg = diffId === "working" ? undefined : selectedTo.value;
  revealPending.value = isMobile.value && !isCommitMessageFile(filePath);
  try {
    showBinaryAsText.value = false;
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
    const diffData = await api.getGitFileDiff(diffId, filePath, cwd, props.conversationId, toArg, asText);
    if (requestId !== loadFileDiffRequestId) return;
    showBinaryAsText.value = asText;
    if (diffData.binary && !asText) revealPending.value = false;
    fileDiff.value = diffData;
  } catch (err) {
    if (requestId !== loadFileDiffRequestId) return;
    revealPending.value = false;
    error.value = `Failed to load file diff: ${err}`;
  } finally {
    if (requestId === loadFileDiffRequestId) loading.value = false;
  }
}

// --- Navigation ---
const fileSwipeOffset = ref(0);
const fileSwipeDirection = ref<"next" | "prev" | null>(null);
let ignoreFileJumpClickUntil = 0;
let fileSwipe: { id: number; x: number; y: number; time: number; path: string } | null = null;

function cancelFileSwipe() {
  fileSwipe = null;
  fileSwipeOffset.value = 0;
}

function toggleMobileFileList() {
  if (performance.now() < ignoreFileJumpClickUntil) return;
  mobileFileListOpen.value = !mobileFileListOpen.value;
}

function startFileSwipe(e: TouchEvent) {
  cancelFileSwipe();
  if (!isMobile.value || mobileFileListOpen.value ||
      recordingLocked.value || showCommentDialog.value || !selectedFile.value ||
      e.touches.length !== 1) return;
  const touch = e.touches[0];
  fileSwipe = {
    id: touch.identifier, x: touch.clientX, y: touch.clientY, time: e.timeStamp,
    path: selectedFile.value,
  };
}

function startCodeSwipe(e: TouchEvent) {
  if (mode.value !== "comment" || selectedMessage.value || !fileDiff.value || !diffEditor ||
      (e.target as Element).closest("button, a, [role='button'], .scrollbar")) return;
  const editor = diffEditor.getModifiedEditor();
  if (!editor.getSelection()?.isEmpty() ||
      editor.getScrollWidth() > editor.getLayoutInfo().contentWidth + 1) return;
  startFileSwipe(e);
}

function startMessageSwipe(e: TouchEvent) {
  const target = e.target as Element;
  if (target.closest("button, a, [role='button']") || window.getSelection()?.toString()) return;
  const scroller = target.closest(".diff-tree-scroll, pre");
  if (scroller && scroller.scrollWidth > scroller.clientWidth + 1) return;
  startFileSwipe(e);
}

function moveFileSwipe(e: TouchEvent) {
  if (!fileSwipe || e.touches.length !== 1 || e.touches[0].identifier !== fileSwipe.id) {
    cancelFileSwipe();
    return;
  }
  const dx = e.touches[0].clientX - fileSwipe.x;
  const dy = e.touches[0].clientY - fileSwipe.y;
  fileSwipeOffset.value = Math.abs(dx) > Math.abs(dy) * 1.3
    ? Math.max(-72, Math.min(72, dx))
    : 0;
}

function endFileSwipe(e: TouchEvent) {
  const start = fileSwipe;
  cancelFileSwipe();
  if (!start || e.touches.length || e.changedTouches.length !== 1 ||
      e.changedTouches[0].identifier !== start.id ||
      !isMobile.value || recordingLocked.value || mobileFileListOpen.value ||
      selectedFile.value !== start.path) return;
  const touch = e.changedTouches[0];
  const dx = touch.clientX - start.x;
  const dy = touch.clientY - start.y;
  if (Math.abs(dx) > 30 && Math.abs(dx) > Math.abs(dy) * 1.3)
    ignoreFileJumpClickUntil = performance.now() + 500;
  if (Math.abs(dx) < 72 || Math.abs(dx) < Math.abs(dy) * 1.5 ||
      e.timeStamp - start.time > 800) return;

  const order = navOrderRef.value;
  const index = order.indexOf(selectedFile.value ?? "");
  const next = index + (dx < 0 ? 1 : -1);
  if (index < 0 || next < 0 || next >= order.length) return;
  fileSwipeDirection.value = dx < 0 ? "next" : "prev";
  selectNavItem(order[next]);
}

function endMessageSwipe(e: TouchEvent) {
  if (window.getSelection()?.toString()) {
    cancelFileSwipe();
    return;
  }
  endFileSwipe(e);
}

function resetFileSwipeAnimation() {
  fileSwipeDirection.value = null;
}

function selectNavItem(path: string) {
  mobileOverview.value = false;
  mobileFileListOpen.value = false;
  if (isMobile.value && isCommitMessageFile(path)) {
    mode.value = "comment";
    rawMessage.value = false;
  }
  selectedFile.value = path;
  currentChangeIndex.value = -1;
}

function goToNextFile(): boolean {
  resetFileSwipeAnimation();
  const order = navOrderRef.value;
  if (order.length === 0 || !selectedFile.value) return false;
  const idx = order.indexOf(selectedFile.value);
  if (idx >= 0 && idx < order.length - 1) {
    selectNavItem(order[idx + 1]);
    return true;
  }
  return false;
}

function goToPreviousFile(): boolean {
  resetFileSwipeAnimation();
  const order = navOrderRef.value;
  if (order.length === 0 || !selectedFile.value) return false;
  const idx = order.indexOf(selectedFile.value);
  if (idx > 0) {
    selectNavItem(order[idx - 1]);
    return true;
  }
  return false;
}

function goToNextChange() {
  if (binaryPanel.value) return;
  if(mobileOverview.value || (selectedMessage.value && mode.value === "comment" && !rawMessage.value)){goToNextFile();return;}
  if (!diffEditor) return;
  const changes = diffEditor.getLineChanges();
  if (!changes || changes.length === 0) {
    goToNextFile();
    return;
  }
  const modEditor = diffEditor.getModifiedEditor();
  const nextIdx = currentChangeIndex.value + 1;
  if (nextIdx >= changes.length) {
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
  if (binaryPanel.value) return;
  if(mobileOverview.value || (selectedMessage.value && mode.value === "comment" && !rawMessage.value)){goToPreviousFile();return;}
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
    const response = await fetch(onMachine("/api/write-file", props.conversationId), {
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
    const viewMenu=document.querySelector<HTMLDetailsElement>('.diff-view-options[open]');
    if(viewMenu){e.preventDefault();viewMenu.open=false;viewMenu.querySelector('summary')?.focus();return;}
    // If Monaco's find widget is open, let Monaco close it.
    const findWidget = editorContainerRef.value?.querySelector(".find-widget.visible");
    if (findWidget) return;
    // If a nested commit/dir picker is open, let it handle Escape.
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
    if(mobileFileListOpen.value){mobileFileListOpen.value=false;return;}
    if (showCommentDialog.value) {
      showCommentDialog.value = null;
    } else {
      requestClose();
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
      if(mobileOverview.value && overviewMessage.value)showRawMessage();
      else {mobileOverview.value=false;if(selectedMessage.value)rawMessage.value=true;}
      nextTick(()=>{if(!diffEditor)return;diffEditor.layout();const editor=diffEditor.getModifiedEditor();editor.focus();editor.trigger("keyboard","actions.find",null);});
    }
    return;
  }
  // When Monaco's find widget is open, let all non-modifier keys pass through.
  const findWidget = editorContainerRef.value?.querySelector(".find-widget.visible");
  if (findWidget) return;
  // Intercept PageUp/PageDown to scroll the diff editor.
  if (e.key === "PageUp" || e.key === "PageDown") {
    if(mobileOverview.value || (selectedMessage.value && mode.value === "comment" && !rawMessage.value))return;
    if (diffEditor) {
      e.preventDefault();
      e.stopPropagation();
      const modEditor = diffEditor.getModifiedEditor();
      modEditor.trigger("keyboard", e.key === "PageUp" ? "cursorPageUp" : "cursorPageDown", null);
    }
    return;
  }
  // Comment mode navigation (only when comment dialogs are closed).
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
    return {
      realPath: f.path,
      treePath: f.path.split("/"),
      status: f.status,
      additions: f.additions,
      deletions: f.deletions,
      highlightCount: highlightsByPath.value.get(f.path)?.length,
    };
  });
});

const groupedTreeEntries = computed(() => treeEntries.value.map((entry) => {
  if (isCommitMessageFile(entry.realPath)) return entry;
  const parts = entry.realPath.split("/");
  if (parts.length === 1) return entry;
  const name = parts.pop()!;
  return { ...entry, treePath: [parts.join("/"), name] };
}));
const groupedRealTreeEntries = computed(() => groupedTreeEntries.value.filter((entry) => !isCommitMessageFile(entry.realPath)));
const navOrder = computed(() => realPathOrder(groupedTreeEntries.value, !!highlights.value));
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
const realFilePosition = computed(() => {
  const paths = navOrder.value.filter((path) => !isCommitMessageFile(path));
  const index = paths.indexOf(selectedFile.value ?? "");
  return index < 0 ? null : `File ${index + 1} of ${paths.length}`;
});
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
  const count = highlightsByPath.value.get(file.path)?.length ?? 0;
  if (count > 0) label += ` \u2605${count}`;
  return label;
}

// --- Event handlers from child components ---
function selectSidebarFile(path: string) {
  resetFileSwipeAnimation();
  selectNavItem(path);
  rawMessage.value = false;
}

function onCommitChange(diff: string, to: "working" | "self") {
  if(isMobile.value)mobileOverview.value=diff==="working";mobileFileListOpen.value=false;rawMessage.value=false;
  selectedDiff.value = diff;
  selectedTo.value = isMobile.value && diff !== "working" ? "self" : to;
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

<style scoped>
.diff-viewer-rethink { --diff-added:#3e7955;--diff-deleted:#a45160; }
.added { color:var(--diff-added); }.deleted {color:var(--diff-deleted)}
.diff-viewer-header {background:var(--bg-base);padding:7px 12px}
.diff-viewer-header-title {font:500 12px/20px ui-monospace,SFMono-Regular,Menlo,monospace}
.diff-rethink-current-counts,.diff-rethink-totals {display:inline-flex;gap:8px;white-space:nowrap;font:11px/18px ui-monospace,SFMono-Regular,Menlo,monospace}
.diff-rethink-current-counts {margin-left:10px}.diff-rethink-totals {margin-left:auto;font-weight:400}
.diff-viewer-controls-row {gap:8px}.diff-viewer-mode-btn {font-size:11px;padding:5px 8px;min-height:30px}
.diff-rethink-change-count {font-size:11px;color:var(--text-secondary);white-space:nowrap}
.diff-viewer-sidebar {background:var(--bg-base)}.diff-viewer-sidebar-comparison {flex:none;gap:8px;padding:12px;border-bottom:1px solid var(--border-color)}
.diff-rethink-comparison-label{display:flex;align-items:center;justify-content:space-between;font-size:11px;color:var(--text-secondary)}
.diff-viewer-sidebar-comparison :deep(.commit-picker-trigger){width:100%;min-height:40px;background:var(--bg-secondary);border:1px solid var(--border-color);border-radius:4px}
.diff-viewer-sidebar-label {font-size:11px;letter-spacing:0;text-transform:none;gap:8px;align-items:center}
.diff-viewer-sidebar-label>span:first-child{font-weight:500}
.diff-viewer-main {display:flex;flex-direction:column;min-height:0;overflow:hidden}
.diff-viewer-files-pane {position:relative;flex:1;min-height:0;height:auto;display:flex;flex-direction:column}
.diff-viewer-editor {flex:1;min-height:0}.diff-commit-message {flex:1;min-height:0;overflow:auto}
.diff-rethink-reveal-wait {position:absolute;inset:0;z-index:2;display:grid;place-items:center;background:var(--bg-base);color:var(--text-secondary);font:12px/20px system-ui,sans-serif}
.diff-rethink-edit-banner {display:flex;align-items:center;justify-content:space-between;gap:12px;padding:9px 14px;border-bottom:1px solid #d9c9a5;background:#faf3df;color:#6b5127;font-size:12px;flex:none}
.diff-rethink-edit-banner strong {font-weight:600}.diff-rethink-edit-banner span {margin-left:12px;font-size:11px}
.diff-rethink-edit-banner button,.diff-rethink-raw-heading button {color:inherit;background:none;border:0;font:inherit;cursor:pointer}
.diff-rethink-raw-heading {display:flex;align-items:center;gap:12px;padding:8px 16px;font-size:11px;flex:none;border-bottom:1px solid var(--border-color)}
.diff-rethink-raw-heading>span {margin-right:auto;color:var(--text-secondary)}
.diff-viewer-readonly .diff-viewer-editor :deep(.cursors-layer .cursor) {visibility:hidden!important}
.diff-viewer-editor :deep(.line-insert) {background:color-mix(in srgb,#6fba87 19%,var(--bg-base))!important}
.diff-viewer-editor :deep(.line-delete) {background:color-mix(in srgb,#c67b86 19%,var(--bg-base))!important}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .center) {justify-content:center!important;align-items:center;gap:6px;width:100%;font:500 11px/18px system-ui,sans-serif;color:var(--text-secondary)}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .diff-hidden-lines) {background:var(--bg-secondary)!important;border-top:1px solid var(--border-color);border-bottom:1px solid var(--border-color)}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .center a) {display:inline-flex;align-items:center;justify-content:center;width:20px;border-radius:3px}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .center a:hover) {background:var(--bg-tertiary)}
.diff-rethink-file-shade {position:absolute;inset:0;background:#15283d55;border:0;z-index:40}
.diff-rethink-mobile-files {position:absolute;top:60px;left:0;right:0;z-index:41;display:flex;flex-direction:column;max-height:calc(100% - 60px);min-height:0;background:var(--bg-base);border-bottom:1px solid var(--border-color);box-shadow:0 8px 24px #14243a22;border-radius:0 0 8px 8px;padding:0 14px 10px}
.diff-rethink-mobile-files.is-overview {inset:0;max-height:none;border:0;border-radius:0;box-shadow:none;padding:0 14px 14px;overflow-y:auto;display:block}
.diff-rethink-mobile-files.is-overview .diff-commit-message {margin:0 -14px;overflow:visible}
.diff-rethink-mobile-files.is-overview :deep(.diff-tree) {height:auto;overflow:visible}
.diff-rethink-mobile-files.is-overview :deep(.diff-tree-scroll) {flex:none;height:auto;max-height:none;overflow:visible}
.diff-rethink-mobile-files:not(.is-overview) :deep(.diff-tree) {flex:1;min-height:0;overflow:hidden}
.diff-rethink-message-files {margin-top:24px;padding-top:8px;border-top:1px solid var(--border-color)}
.diff-rethink-message-files :deep(.diff-tree-search) {display:none}
.diff-rethink-message-files :deep(.diff-tree) {flex:none;height:auto;overflow:visible}
.diff-rethink-message-files :deep(.diff-tree-scroll) {flex:none;height:auto;max-height:none;overflow-x:auto;overflow-y:hidden}
.diff-rethink-file-heading {display:flex;align-items:center;gap:10px;padding:12px 0 8px;flex:none;font-size:12px}.diff-rethink-file-heading small{font-size:11px;color:var(--text-secondary);margin-left:4px}
.diff-rethink-file-heading button {background:none;border:0;font:inherit;color:var(--text-secondary);min-height:32px;cursor:pointer}
.diff-rethink-mobile-files:not(.is-overview) .diff-rethink-file-heading{border-bottom:1px solid var(--border-color);margin-bottom:6px;font-weight:600}
.diff-rethink-opening-title {font-size:17px;font-weight:500;margin:18px 0}
.diff-rethink-filebar {display:flex;align-items:center;gap:4px;padding:4px 8px;min-height:60px;border-bottom:1px solid var(--border-color);background:var(--bg-base);flex:none;z-index:42}
.diff-rethink-filebar>button {display:flex;align-items:center;justify-content:center;min-width:44px;min-height:48px;border:0;background:none;color:var(--text-primary);font:26px/20px system-ui,sans-serif;cursor:pointer}
.diff-rethink-filebar>button:disabled{opacity:.3;cursor:default}.diff-rethink-filebar .diff-rethink-file-jump{flex:1;min-width:0;justify-content:space-between;gap:8px;font-size:12px;text-align:left}
.diff-rethink-file-jump{overflow:hidden;touch-action:pan-y;border:1px solid var(--border-color)!important;border-radius:6px;background:var(--bg-secondary)!important;padding:3px 8px!important}
.diff-rethink-file-jump.is-open{background:var(--bg-tertiary)!important;border-color:var(--primary)!important}
.diff-rethink-file-jump-window{position:relative;display:block;flex:1;min-width:0;overflow:hidden}
.diff-rethink-file-jump-content{display:block;width:100%;transition:transform .18s ease}
.diff-rethink-file-jump.is-dragging .diff-rethink-file-jump-content{transition:none}
.diff-rethink-file-jump-top{display:flex;align-items:baseline;gap:6px;min-width:0}
.diff-rethink-file-jump-top small{flex:1;min-width:0}
.diff-rethink-file-position{flex:none;color:var(--text-secondary);font:10px/18px system-ui,sans-serif;white-space:nowrap}
.diff-rethink-file-chevron{display:inline-block;margin-left:2px;transition:transform .16s ease}
.diff-rethink-file-jump.is-open .diff-rethink-file-chevron{transform:rotate(180deg)}
.diff-file-next-enter{animation:diff-file-in-right .22s ease-out both}
.diff-file-prev-enter{animation:diff-file-in-left .22s ease-out both}
@keyframes diff-file-in-right{from{transform:translateX(100%)}to{transform:translateX(0)}}
@keyframes diff-file-in-left{from{transform:translateX(-100%)}to{transform:translateX(0)}}
@media(prefers-reduced-motion:reduce){.diff-file-next-enter,.diff-file-prev-enter{animation-duration:.01ms}}
.diff-rethink-file-jump>span:first-child{min-width:0}.diff-rethink-file-jump strong {display:flex;align-items:baseline;min-width:0;overflow:hidden;white-space:nowrap;font-size:13px;font-weight:500}
.diff-rethink-file-name{min-width:0;overflow:hidden;text-overflow:ellipsis}
.diff-rethink-file-jump strong .diff-rethink-current-counts{flex:none}
.diff-rethink-file-jump small {display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--text-secondary);font-size:10px;line-height:18px}
.diff-rethink-file-jump .diff-rethink-current-counts{font-size:10px;line-height:18px}
.diff-rethink-mobile-tools {display:flex;align-items:center;gap:8px;padding:4px 10px;border-bottom:1px solid var(--border-color);flex:none;font-size:11px;color:var(--text-secondary)}
.diff-rethink-mobile-tools>.btn-text{margin-left:auto;font-size:12px;white-space:nowrap;background:none;border:0;color:inherit}
.diff-rethink-change-nav{display:flex;gap:2px}.diff-rethink-change-nav button{min-height:36px;min-width:32px;border:0;background:none;font:17px/20px system-ui,sans-serif;color:inherit}.diff-rethink-change-nav button:disabled{opacity:.3}
@media(max-width:767px){
.diff-viewer-header-mobile{padding:5px 8px;gap:6px;flex-direction:row;align-items:center;flex:none}
.diff-viewer-header-main,.diff-viewer-mobile-selectors{min-width:0}.diff-viewer-mobile-selectors{width:100%}
.diff-viewer-header-mobile :deep(.commit-picker){width:100%;min-width:0}
.diff-viewer-content{min-height:0}.diff-viewer-main{height:100%;width:100%}
.diff-rethink-edit-banner {padding:8px 10px;font-size:11px}.diff-rethink-edit-banner span{display:block;margin:2px 0 0;font-size:10px}
.diff-viewer-rethink{width:100%;height:100%;border-radius:0}
}
:global(.dark) .diff-viewer-rethink {--diff-added:#88ba9a;--diff-deleted:#d39aa4}
:global(.dark) .diff-rethink-edit-banner {background:#393122;color:#e3ce9f;border-color:#6b5d3f}
</style>
