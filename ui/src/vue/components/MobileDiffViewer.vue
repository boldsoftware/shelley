<!-- Mobile diff viewer with commit selection, file navigation, and comments. -->
<template>
  <div v-if="isOpen" class="diff-viewer-overlay" :style="commentViewport ?? undefined">
    <div ref="containerRef" class="diff-viewer-container diff-viewer-rethink diff-viewer-readonly">
      <!-- Toast notifications -->
      <div
        v-if="review.sent.value && !review.error.value"
        class="diff-viewer-toast diff-viewer-toast-saved"
        role="status"
        data-testid="review-recording-sent"
      >
        ✅ Sent — transcribing in the conversation
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
              :range-hashes="rangeHashes"
              :is-mobile="isMobile"
              @open-graph="emit('open-git-graph')"
            />

          </div>
          <ReviewRecordingStatus
            v-if="recordingLocked"
            :phase="review.phase.value"
            :levels="review.levels"
            :current="review.current"
          />
        </div>
        <DiffReviewButton v-if="mobileOverview || (selectedMessage && !showCommentDialog) || mobileFileListOpen" :count="reviewNotes?.length ?? 0" :disabled="recordingLocked" @open="emit('open-review', $event)" />
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
          <svg aria-hidden="true" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
            <path d="M4 4l16 16M20 4 4 20" />
          </svg>
        </button>
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
      <div class="diff-viewer-content">
        <aside v-if="isMobile" v-show="mobileOverview || mobileFileListOpen" class="diff-rethink-mobile-files" :class="{ 'is-overview': mobileOverview }" :inert="recordingLocked" :role="mobileFileListOpen ? 'dialog' : undefined" :aria-label="mobileFileListOpen ? 'Choose file' : undefined">
          <h2 v-if="mobileOverview" class="diff-rethink-opening-title">Uncommitted changes</h2>
          <div class="diff-rethink-file-heading"><span>{{ mobileOverview ? 'Files' : 'Choose file' }} <small>{{ realFiles.length }}</small></span><span v-if="mobileOverview" class="diff-rethink-totals"><span class="added">+{{ totalAdditions }}</span> <span class="deleted">−{{ totalDeletions }}</span></span><button v-if="!mobileOverview" class="btn-text" @click="mobileFileListOpen = false">Close</button></div>
          <DiffFileTree :entries="groupedTreeEntries" :selected-real-path="mobileOverview ? null : selectedFile" :ordered="!!highlights" mobile @select="selectSidebarFile" />
        </aside>
        <div ref="mainRef" class="diff-viewer-main" v-show="!isMobile || !mobileOverview">
          <nav v-if="isMobile && !mobileOverview" class="diff-rethink-filebar" aria-label="Files and commit messages">
            <button
              class="diff-rethink-file-jump"
              :class="{ 'is-dragging': fileSwipeOffset !== 0, 'is-open': mobileFileListOpen }"
              :disabled="recordingLocked"
              :aria-expanded="mobileFileListOpen"
              :aria-label="mobileFileListOpen ? 'Close file list' : 'Open file list'"
              @click="toggleMobileFileList"
              @touchstart.capture.passive="startFileSwipe"
              @touchmove.capture.passive="moveFileSwipe"
              @touchend.capture.passive="endFileSwipe"
              @touchcancel.capture="cancelFileSwipe"
            >
              <span class="diff-rethink-file-jump-window">
                <span :key="selectedFile ?? ''" class="diff-rethink-file-jump-content" :class="fileSwipeDirection ? `diff-file-${fileSwipeDirection}-enter` : ''" :style="{ transform: `translateX(${fileSwipeOffset}px)` }" @animationend="resetFileSwipeAnimation">
                  <span class="diff-rethink-file-jump-top" :class="{ 'is-commit': !!selectedMessage }"><small v-if="!selectedMessage">{{ selectedFile?.slice(0, Math.max(0, selectedFile.lastIndexOf('/'))) }}</small><span class="diff-rethink-file-position">{{ mobileFileListOpen ? 'Close file list' : selectedMessage ? isCommitRange ? `${commitMessages.length} commits · ${realFiles.length} files` : `${realFiles.length} files` : realFilePosition ?? 'Files' }} <span class="diff-rethink-file-chevron" aria-hidden="true">▾</span></span></span>
                  <strong><span class="diff-rethink-file-name">{{ selectedMessage ? isCommitRange ? 'Commit messages' : 'Commit message' : selectedFile?.split('/').pop() || 'Choose file' }}</span><span v-if="currentRealFile" class="diff-rethink-current-counts"><span class="added">+{{ currentRealFile.additions }}</span> <span class="deleted">−{{ currentRealFile.deletions }}</span></span></strong>
                </span>
              </span>
            </button>
          </nav>
          <div v-if="isMobile && (!selectedMessage || showCommentDialog)" class="diff-rethink-change-nav" :class="{ 'is-commenting': showCommentDialog }" :inert="mobileFileListOpen">

            <textarea
              v-if="showCommentDialog"
              ref="commentInput"
              v-model="commentText"
              class="diff-viewer-comment-input diff-rethink-rail-input"
              :aria-label="commentReady ? `Comment on ${lineCommentLabel(showCommentDialog, true)}` : 'Comment draft'"

              :placeholder="commentReady ? 'Enter your comment...' : 'Select a changed text hunk to comment...'"
              @keydown.esc.stop="showCommentDialog = null"
            />
            <span class="diff-rethink-bottom-change-label">{{ changeLabel }}</span>
            <template v-if="showCommentDialog">
              <button class="diff-rethink-cancel-comment" type="button" @click="showCommentDialog = null">Cancel</button>
              <button class="diff-rethink-comment-change" type="button" :disabled="!commentReady || !commentText.trim()" @pointerdown.prevent @click="submitHunkComment">Save Comment</button>
            </template>
            <button v-else class="diff-rethink-comment-change" type="button" aria-label="Comment on current hunk" :disabled="editorBlocked || revealPending || !changeCount || recordingLocked" @click="commentOnCurrentHunk">
              Comment
            </button>
            <DiffReviewButton v-if="reviewNotes?.length && !showCommentDialog && !mobileFileListOpen" :count="reviewNotes.length" :disabled="recordingLocked" @open="emit('open-review', $event)" />
            <button v-if="showCommentDialog || (fileDiff && (hasPrevFile || (!editorBlocked && currentChangeIndex > 0)))" class="diff-rethink-prev-change" :aria-label="prevChangeGoesToFile ? (prevChangeGoesToCommit ? 'Previous commit' : 'Previous file') : showCommentDialog ? 'Previous hunk' : 'Previous hunk (,)'" :disabled="!!showCommentDialog && (loading || revealPending || (!hasPrevFile && (editorBlocked || currentChangeIndex <= 0)))" @pointerdown="showCommentDialog && $event.preventDefault()" @click="showCommentDialog ? navigateCommentHunk(-1) : goToPreviousChange()"><span v-html="PREV_CHANGE_ICON" /></button>
            <button v-if="showCommentDialog || (fileDiff && (hasNextFile || (!editorBlocked && currentChangeIndex < changeCount - 1)))" class="diff-rethink-next-change" :aria-label="nextChangeGoesToFile ? 'Next file' : showCommentDialog ? 'Next hunk' : 'Next hunk (.)'" :disabled="!!showCommentDialog && (loading || revealPending || (!hasNextFile && (editorBlocked || currentChangeIndex >= changeCount - 1)))" @pointerdown="showCommentDialog && $event.preventDefault()" @click="showCommentDialog ? navigateCommentHunk(1) : goToNextChange()"><span v-html="NEXT_CHANGE_ICON" /></button>
          </div>
          <div v-if="isMobile && selectedMessage && rawMessage" class="diff-rethink-mobile-tools">
            <button type="button" class="diff-rethink-raw-toggle" @click="toggleRawMessage">View rendered</button>
          </div>
          <div v-if="selectedMessage && isCommitRange && isMobile && !rawMessage" class="diff-rethink-range-overview"
            @touchstart.capture.passive="startMessageSwipe"
            @touchmove.capture.passive="moveFileSwipe"
            @touchend.capture.passive="endMessageSwipe"
            @touchcancel.capture="cancelFileSwipe">
            <header class="diff-rethink-range-header">
              <h2>{{ commitMessages.length }} commits</h2>
              <button type="button" class="diff-rethink-range-link" aria-label="Show range in graph" :disabled="recordingLocked" @click="emit('open-git-graph')">
                <span><code>{{ selectedDiff?.slice(0, 8) }}</code> → <code>{{ selectedTo.slice(0, 8) }}</code></span>
                <span>Graph ›</span>
              </button>
            </header>
            <div class="diff-rethink-range-commits">
              <details v-for="message in commitMessages" :key="message.hash">
                <summary><code>{{ message.hash.slice(0, 8) }}</code><span>{{ message.subject }}</span></summary>
                <DiffCommitMessage :message="message" :is-mobile="isMobile" is-range @raw="showRawMessage(message)" @comment="openPreviewComment(message, $event)" />
              </details>
            </div>
            <section v-if="realFiles.length" class="diff-rethink-message-files" aria-label="Changed files">
              <div class="diff-rethink-file-heading"><span>Files <small>{{ realFiles.length }}</small></span><span class="diff-rethink-totals"><span class="added">+{{ totalAdditions }}</span> <span class="deleted">−{{ totalDeletions }}</span></span></div>
              <DiffFileTree :entries="groupedRealTreeEntries" :selected-real-path="null" :ordered="!!highlights" mobile @select="selectSidebarFile" />
            </section>
          </div>
          <DiffCommitMessage v-else-if="selectedMessage && !rawMessage" :message="selectedMessage" :is-mobile="isMobile" @raw="showRawMessage" @comment="openPreviewComment(selectedMessage, $event)"
            @touchstart.capture.passive="startMessageSwipe"
            @touchmove.capture.passive="moveFileSwipe"
            @touchend.capture.passive="endMessageSwipe"
            @touchcancel.capture="cancelFileSwipe">
            <section v-if="isMobile && realFiles.length" class="diff-rethink-message-files" aria-label="Changed files">
              <div class="diff-rethink-file-heading"><span>Files <small>{{ realFiles.length }}</small></span><span class="diff-rethink-totals"><span class="added">+{{ totalAdditions }}</span> <span class="deleted">−{{ totalDeletions }}</span></span></div>
              <DiffFileTree :entries="groupedRealTreeEntries" :selected-real-path="null" :ordered="!!highlights" mobile @select="selectSidebarFile" />
            </section>
          </DiffCommitMessage>

          <div
            ref="filesPaneRef"
            class="diff-viewer-files-pane"
            data-review="Files"
            v-show="!selectedMessage || rawMessage"
            :inert="mobileFileListOpen"
            @touchstart.capture="startCodeTap"
            @touchmove.capture="moveCodeTap"
            @touchend.capture="endCodeTap"
            @touchcancel.capture="codeTap = null"
          >
            <div v-if="loading && !fileDiff" class="diff-viewer-loading">
              <div class="spinner"></div>
              <span>Loading...</span>
            </div>
            <div v-if="!loading && !editorBlocked && !monacoLoaded && !error" class="diff-viewer-loading">
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
              v-if="fileDiff?.tooLarge"
              class="diff-viewer-empty diff-viewer-large"
              data-testid="large-file-panel"
              role="status"
              @touchstart.capture.passive="startMessageSwipe"
              @touchmove.capture.passive="moveFileSwipe"
              @touchend.capture.passive="endMessageSwipe"
              @touchcancel.capture="cancelFileSwipe"
            >
              <h3>Too large to display</h3>
              <p>{{ fileDiff.limitReason }}</p>
              <p>This file’s changes are not shown here.</p>
              <p>
                Before: {{ formatFileSize(fileDiff.oldBytes) }} ·
                After: {{ formatFileSize(fileDiff.newBytes) }}
              </p>
              <p><a :href="binaryDownloadURL('patch')" download>Download patch</a></p>
              <button type="button" class="btn-secondary" @click="toggleMobileFileList">
                Choose another file
              </button>
            </div>
            <div v-if="fileDiff?.binary && !fileDiff.tooLarge" class="diff-viewer-binary-toolbar">
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
              :style="{ display: fileDiff && !editorBlocked && monacoLoaded ? 'block' : 'none', visibility: isMobile && revealPending ? 'hidden' : 'visible' }"
              @touchstart.capture="startCodeSwipe($event); startOverviewTouch($event)"
              @touchmove.capture="moveOverviewTouch($event); moveFileSwipe($event)"
              @touchend.capture="endOverviewTouch($event); endFileSwipe($event)"
              @touchcancel.capture="endOverviewTouch($event); cancelFileSwipe()"
            />
            <div v-if="isMobile && revealPending && fileDiff && !editorBlocked && monacoLoaded" class="diff-rethink-reveal-wait">Loading changes…</div>

            <template v-if="lineTapPrompt && !recordingLocked">
              <button
                type="button"
                class="diff-rethink-tap-comment"
                :style="{ top: `${lineTapPrompt.bubbleTop}px` }"
                :aria-label="`Comment on ${lineCommentLabel(lineTapPrompt, true)}`"
                @pointerdown.prevent
                @click="openTappedLineComment"
              >{{ lineTapPrompt.startLine === lineTapPrompt.endLine ? "＋ Comment" : `＋ Comment on ${lineTapPrompt.endLine - lineTapPrompt.startLine + 1} lines` }}</button>
            </template>

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
          <nav v-if="isMobile && selectedMessage && !showCommentDialog" class="diff-rethink-commit-nav" aria-label="Commit navigation">
            <button type="button" :disabled="!hasPrevFile || mobileFileListOpen" aria-label="Previous file" @click="goToPreviousFile()"><span v-html="PREV_CHANGE_ICON" /></button>
            <button type="button" :disabled="!hasNextFile || mobileFileListOpen" aria-label="Next file" @click="goToNextFile()"><span v-html="NEXT_CHANGE_ICON" /></button>
          </nav>
        </div>
      </div>

      <CommentDialog
        v-if="previewComment"
        :key="previewCommentOpens"
        v-model:text="previewCommentText"
        :where="`Commit ${previewComment.message.hash.slice(0, 8)}`"
        :quoted="truncateWithEllipsis(previewComment.quote, 160)"
        @submit="submitPreviewComment"
        @cancel="previewComment = null"
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
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import type * as Monaco from "monaco-editor";
import { api } from "../../services/api";
import { loadMonaco } from "../../services/monaco";
import { isDarkModeActive } from "../../services/theme";

import {

  lineCommentLabel,
  truncateWithEllipsis,
  useMonacoComments,
  type CommentDialogInfo,
} from "../composables/monacoComments";
import CommentDialog from "./CommentDialog.vue";
import { type DraftDiffComment, type NewDraftDiffComment, mountDraftDiffComments } from "./draftDiffReview";
import DiffReviewButton from "./DiffReviewButton.vue";
import CommitPicker from "./CommitPicker.vue";

import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import DiffFileTree from "./DiffFileTree.vue";
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
import { defaultDiffSelection } from "./diffViewerModel";
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
  // Machine (VM name with namespace) the cwd is on; use reviewMachine ?? 'local'.
  reviewMachine?: string;
  // Parent-owned review modal/panel is open; skip this viewer's key handler.
  reviewOpen?: boolean;
  // Draft review notes owned by the parent.
  reviewNotes?: readonly DraftDiffComment[];
  initialCommit?: string;
  initialTo?: string;
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
  (e: "open-git-graph"): void;
  (e: "save-review-comment", note: NewDraftDiffComment): void;
  (e: "open-review", event: MouseEvent): void;
  (e: "cwd-change", cwd: string): void;
}>();

const COMMIT_MSG_PREFIX = "commit-message:";
const MOBILE_LINE_DECORATIONS_WIDTH = 14;
const MOBILE_SCROLLBAR_SIZE = 8;
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

// SVG icon markup rendered in the navigation buttons.
const PREV_CHANGE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M10 2L4 8l6 6V2z" /></svg>';
const NEXT_CHANGE_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M6 2l6 6-6 6V2z" /></svg>';
const DIR_ICON =
  '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>';

// --- Reactive state (mirrors React useState) ---
const diffs = ref<GitDiffInfo[]>([]);
const showDirPicker = ref(false);
const selectedDiff = ref<string | null>(null);
const selectedTo = ref("working");
const gitRoot = ref("");
const modelVersion = ref(0);
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
const binaryPanel = computed(() => !fileDiff.value?.tooLarge && !!fileDiff.value?.binary && !showBinaryAsText.value);
const editorBlocked = computed(() => !!fileDiff.value?.tooLarge || binaryPanel.value);
function formatFileSize(bytes = 0): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toLocaleString(undefined, { maximumFractionDigits: 1 })} KiB`;
}
const binaryBeforeRatio = computed(() => !fileDiff.value?.oldExists ? EMPTY_SIDE_SPLIT_RATIO : !fileDiff.value?.newExists ? 1 - EMPTY_SIDE_SPLIT_RATIO : 0.5);
function showBinaryText() {
  if (selectedDiff.value && selectedFile.value) void loadFileDiff(selectedDiff.value, selectedFile.value, true);
}
function binaryDownloadURL(side: "before" | "after" | "patch"): string {
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
let revealLastChangeForPath: string | null = null;
const commitMessages = ref<GitCommitMessage[]>([]);
const rangeHashes = computed(() => new Set(commitMessages.value.map((message) => message.hash)));
const previewComment = ref<{ message: GitCommitMessage; quote: string } | null>(null);
const previewCommentText = ref("");
const previewCommentOpens = ref(0);
const commentViewport = ref<{ top: string; height: string; bottom: string } | null>(null);

const isMobile = ref(window.innerWidth < 768);
const mobileOverview = ref(isMobile.value && !props.initialFile);
const mobileFileListOpen = ref(false);
const rawMessage = ref(false);
const changeCount = ref(0);
const realFiles = computed(() => files.value.filter(f => !isCommitMessageFile(f.path)));
const totalAdditions = computed(() => realFiles.value.reduce((n,f) => n + f.additions, 0));
const totalDeletions = computed(() => realFiles.value.reduce((n,f) => n + f.deletions, 0));
const currentRealFile = computed(() => realFiles.value.find(f => f.path === selectedFile.value));
const selectedMessage = computed(() => selectedFile.value && isCommitMessageFile(selectedFile.value) ? commitMessages.value.find(m => m.hash === commitHashFromPath(selectedFile.value!)) : undefined);
const isCommitRange = computed(() => !!selectedDiff.value && selectedDiff.value !== "working" && selectedTo.value !== "self");
const overviewMessage = computed(() => selectedMessage.value ?? commitMessages.value[0]);
const changeLabel = computed(() => fileDiff.value?.tooLarge ? 'Diff not displayed' : binaryPanel.value ? 'Binary file' : changeCount.value ? `Change ${Math.max(0,currentChangeIndex.value)+1} of ${changeCount.value}` : 'No changes');
function showRawMessage(msg = overviewMessage.value){ if(msg) selectedFile.value = COMMIT_MSG_PREFIX + msg.hash; rawMessage.value = true; mobileOverview.value = false; }
function toggleRawMessage(){
  if(rawMessage.value){rawMessage.value=false;}
  else showRawMessage();
}
// --- Non-reactive refs (mirror React useRef) ---
let monacoMod: typeof Monaco | null = null;
const editorContainerRef = ref<HTMLDivElement | null>(null);
const mainRef = ref<HTMLDivElement | null>(null);
let diffEditor: Monaco.editor.IStandaloneDiffEditor | null = null;
let currentHunkDecorations: Monaco.editor.IEditorDecorationsCollection | null = null;
let commentTargetDecorations: Monaco.editor.IEditorDecorationsCollection | null = null;
let currentHunkZoneObserver: MutationObserver | null = null;
function markCurrentDeletedZone() {
  if (!diffEditor) return;
  const zones = diffEditor.getModifiedEditor().getDomNode()
    ?.querySelectorAll(".inline-deleted-margin-view-zone");
  if (!zones) return;
  const changes = diffEditor.getLineChanges();
  const index = Math.max(0, currentChangeIndex.value);
  const change = changes?.[index];
  const hasRemovedLines = (c: NonNullable<typeof change>) =>
    c.originalEndLineNumber > 0 && c.originalEndLineNumber >= c.originalStartLineNumber;
  const zoneIndex = change && hasRemovedLines(change)
    ? changes!.slice(0, index).filter(hasRemovedLines).length
    : -1;
  zones.forEach((zone, i) => {
    const active = i === zoneIndex;
    if (zone.classList.contains("diff-current-hunk-deleted") !== active) {
      zone.classList.toggle("diff-current-hunk-deleted", active);
    }
  });
}
function markCurrentHunk() {
  if (!diffEditor || !monacoMod) return;
  const editor = diffEditor.getModifiedEditor();
  currentHunkDecorations ??= editor.createDecorationsCollection();
  const index = currentChangeIndex.value;
  const changes = diffEditor.getLineChanges();
  const change = changes?.[index];
  const lineCount = editor.getModel()?.getLineCount() ?? 0;
  if (!change || !lineCount) {
    currentHunkDecorations.clear();
    markCurrentDeletedZone();
    return;
  }
  const start = Math.max(1, Math.min(lineCount, change.modifiedStartLineNumber));
  const end = Math.max(start, Math.min(lineCount, change.modifiedEndLineNumber));
  currentHunkDecorations.set([{
    range: new monacoMod.Range(start, 1, end, 1),
    options: { isWholeLine: true, linesDecorationsClassName: "diff-current-hunk-gutter" },
  }]);
  markCurrentDeletedZone();
}
watch(currentChangeIndex, markCurrentHunk);
const editorReady = ref(false);

let isMobileVal = isMobile.value;
// Shared comment-mode UX (click-to-comment, selection prompt, dialog state).
const {
  showCommentDialog,

  commentPrompt,
  commentText,
  attach: attachComments,
  handleAddComment,
  openCommentFromPrompt,
  commentOnHighlight,
  reset: resetComments,
} = useMonacoComments({
  monaco: () => monacoMod,
  mode: () => "comment",
  isMobile: () => isMobileVal,
  promptHost: () => mainRef.value,
  fileRef: commentFileRef,
  onSubmit: emitComment,
});

const hunkDrafts = new Map<string, string>();
const commentInput = ref<HTMLTextAreaElement | null>(null);
const commentFile = ref<string | null>(null);
const filesPaneRef = ref<HTMLDivElement | null>(null);
const lineTapPrompt = ref<(CommentDialogInfo & { anchor: number; startLine: number; endLine: number; bubbleTop: number }) | null>(null);

watch([selectedDiff, selectedTo, selectedFile, mobileFileListOpen, () => props.cwd, () => props.isOpen], () => {
  lineTapPrompt.value = null;
});

function codeCommentHunkIndex(changes: readonly Monaco.editor.ILineChange[], side: "left" | "right", line: number) {
  return changes.findIndex(change => side === "left"
    ? line >= change.originalStartLineNumber && line <= change.originalEndLineNumber
    : line >= change.modifiedStartLineNumber && line <= change.modifiedEndLineNumber);
}
const commentReady = computed(() =>
  commentFile.value === selectedFile.value && !loading.value && !revealPending.value &&
  !editorBlocked.value && !selectedMessage.value,
);
const markedCommentTarget = computed(() => showCommentDialog.value
  ? (commentReady.value ? showCommentDialog.value : null)
  : lineTapPrompt.value);

function deletedLineAtOffset(change: Monaco.editor.ILineChange, offset: number) {
  const original = diffEditor!.getOriginalEditor();
  let line = change.originalStartLineNumber;
  const top = original.getTopForLineNumber(line, true);
  // Monaco inserts view zones in the original editor for wrapped deleted rows.
  while (line < change.originalEndLineNumber &&
         original.getTopForLineNumber(line + 1, true) - top <= offset) line++;
  return line;
}

function markDeletedCommentTarget() {
  if (!diffEditor) return;
  const target = markedCommentTarget.value;
  const changes = diffEditor.getLineChanges()?.filter(change => change.originalEndLineNumber > 0) ?? [];
  diffEditor.getModifiedEditor().getDomNode()
    ?.querySelectorAll<HTMLElement>(".view-lines.line-delete[monaco-view-zone]").forEach((zone, index) => {
      const change = changes[index];
      const matches = target?.side === "left" && change &&
        (target.endLine ?? target.line) >= change.originalStartLineNumber &&
        (target.startLine ?? target.line) <= change.originalEndLineNumber;
      zone.querySelectorAll<HTMLElement>(":scope > .view-line").forEach(row => {
        const line = matches ? deletedLineAtOffset(change, row.offsetTop) : 0;
        const active = !!target && line >= (target.startLine ?? target.line) && line <= (target.endLine ?? target.line);
        if (row.classList.contains("diff-comment-target-line") !== active) {
          row.classList.toggle("diff-comment-target-line", active);
        }
      });
    });
}

function markCommentTarget() {
  if (!diffEditor || !monacoMod) return;
  const editor = diffEditor.getModifiedEditor();
  const target = markedCommentTarget.value;
  if (target?.side === "right" && editor.getModel()) {
    commentTargetDecorations ??= editor.createDecorationsCollection();
    commentTargetDecorations.set([{
      range: new monacoMod.Range(target.startLine ?? target.line, 1, target.endLine ?? target.line, 1),
      options: { isWholeLine: true, className: "diff-comment-target-line", linesDecorationsClassName: "diff-comment-target-gutter", zIndex: 30 },
    }]);
  } else {
    commentTargetDecorations?.clear();
  }
  markDeletedCommentTarget();
}
watch([markedCommentTarget, modelVersion], markCommentTarget, { flush: "post" });
const hunkDraftKey = (target: CommentDialogInfo) =>
  `${commentFile.value}:${target.side}:${target.startLine ?? target.line}:${target.endLine ?? target.line}`;
const pendingDraftKey = (path: string | null) => `${path}:pending`;

watch(showCommentDialog, target => {
  if (!target) {
    hunkDrafts.clear();
    commentFile.value = null;
  } else {
    commentFile.value = selectedFile.value;
    nextTick(() => commentInput.value?.focus({ preventScroll: true }));
  }
});

function navigateCommentHunk(direction: -1 | 1) {
  if (!diffEditor || loading.value || revealPending.value) return;
  const path = selectedFile.value;
  if (direction === 1) goToNextChange();
  else goToPreviousChange();
  if (selectedFile.value === path) commentOnCurrentHunk();
}

function submitHunkComment() {
  const target = showCommentDialog.value;
  if (!target || !commentReady.value || !commentText.value.trim()) return;
  hunkDrafts.delete(hunkDraftKey(target));
  handleAddComment();
}

function revealCommentTarget() {
  const target = showCommentDialog.value;
  if (!target || !commentReady.value || !diffEditor || !monacoMod || !isMobile.value) return;
  diffEditor.layout();
  const editor = diffEditor.getModifiedEditor();
  if (target.side === "left") {
    const deletedChanges = diffEditor.getLineChanges()?.filter(change => change.originalEndLineNumber > 0) ?? [];
    const index = deletedChanges.findIndex(change =>
      change.originalStartLineNumber <= target.line && change.originalEndLineNumber >= target.line,
    );
    const zone = editorHost.querySelectorAll<HTMLElement>(".inline-deleted-margin-view-zone")[index];
    const editorNode = editor.getDomNode();
    if (zone && editorNode) {
      editor.setScrollTop(
        editor.getScrollTop() + zone.getBoundingClientRect().top - editorNode.getBoundingClientRect().top - 8,
        monacoMod.editor.ScrollType.Immediate,
      );
    }
  } else {
    editor.revealLineNearTop(target.startLine ?? target.line, monacoMod.editor.ScrollType.Immediate);
  }
}

function syncCommentViewport() {
  const viewport = window.visualViewport;
  commentViewport.value = isMobile.value && (showCommentDialog.value || previewComment.value) && viewport
    ? { top: `${viewport.offsetTop}px`, height: `${viewport.height}px`, bottom: "auto" }
    : null;
  if (showCommentDialog.value) requestAnimationFrame(revealCommentTarget);
}

watch([showCommentDialog, previewComment], syncCommentViewport, { flush: "post" });

function commentOnCurrentHunk() {
  if (!diffEditor || !monacoMod || editorBlocked.value || revealPending.value || recordingLocked.value) return;
  const change = diffEditor.getLineChanges()?.[Math.max(0, currentChangeIndex.value)];
  if (!change) return;
  const deleted = change.modifiedEndLineNumber === 0;
  openCodeComment(
    deleted ? "left" : "right",
    deleted ? change.originalStartLineNumber : change.modifiedStartLineNumber,
    deleted ? change.originalEndLineNumber : change.modifiedEndLineNumber,
  );
}

function openCodeComment(side: "left" | "right", startLine: number, endLine: number) {
  lineTapPrompt.value = null;
  if (!diffEditor || !monacoMod) return;
  const model = (side === "left" ? diffEditor.getOriginalEditor() : diffEditor.getModifiedEditor()).getModel();
  if (!model) return;
  const previous = showCommentDialog.value;
  const pendingText = previous && commentFile.value === null ? commentText.value : "";
  if (previous && commentFile.value === selectedFile.value) {
    hunkDrafts.set(hunkDraftKey(previous), commentText.value);
  }
  commentFile.value = selectedFile.value;
  commentPrompt.value = null;
  showCommentDialog.value = {
    line: startLine,
    side,
    startLine,
    endLine,
    selectedText: model.getValueInRange(
      new monacoMod.Range(startLine, 1, endLine, model.getLineMaxColumn(endLine)),
    ),
  };
  commentText.value = pendingText || hunkDrafts.get(hunkDraftKey(showCommentDialog.value)) || "";
  hunkDrafts.delete(pendingDraftKey(selectedFile.value));
}

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
  if (selectedTo.value === "working") return `${label} → working tree`;
  if (selectedTo.value !== "self") return `${label} → ${selectedTo.value.slice(0, 8)}`;
  return label;
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
  if (!diffEditor || editorBlocked.value) return [];
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
    if(mobileOverview.value || (selectedMessage.value && !rawMessage.value))return undefined;
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
    if(selectedMessage.value && !rawMessage.value)return [`Commit ${selectedMessage.value.hash.slice(0,8)} message`];
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

function reviewMachine() {
  return props.reviewMachine ?? "local";
}

function emitComment(_block: string) {
  const target = showCommentDialog.value;
  const fd = fileDiff.value;
  const text = commentText.value.trim();
  if (!target || !fd || !selectedDiff.value || !text || fd.path !== selectedFile.value) return;
  const raw = target.side === "left" ? fd.oldContent : fd.newContent;
  const line = target.startLine ?? target.line;
  const endLine = target.endLine ?? target.line;
  const quote = target.selectedText?.trim()
    ? target.selectedText
    : raw.split("\n").slice(line - 1, endLine).join("\n");
  emit("save-review-comment", {
    cwd: props.cwd, root: gitRoot.value, machine: reviewMachine(),
    diff: selectedDiff.value, to: selectedTo.value, path: fd.path, side: target.side,
    line, endLine, quote, text, snapshot: raw,
  });
}

function openPreviewComment(message: GitCommitMessage, quote: string) {
  if (recordingLocked.value) return;
  showCommentDialog.value = null;
  previewComment.value = { message, quote };
  previewCommentText.value = "";
  previewCommentOpens.value++;
}

function submitPreviewComment() {
  const target = previewComment.value;
  if (!target || !previewCommentText.value.trim()) return;
  emit("save-review-comment", {
    cwd: props.cwd, root: gitRoot.value, machine: reviewMachine(),
    diff: selectedDiff.value ?? "", to: selectedTo.value,
    path: COMMIT_MSG_PREFIX + target.message.hash, side: "right",
    line: null, endLine: null, quote: target.quote, text: previewCommentText.value.trim(), snapshot: null,
  });
  previewComment.value = null;
  previewCommentText.value = "";
}

watch(
  () => props.isOpen,
  (open) => (open ? review.refresh() : review.abandon()),
  { immediate: true },
);

watch(selectedFile, (path, previousPath) => {
  if (showCommentDialog.value) {
    const key = commentFile.value === previousPath
      ? hunkDraftKey(showCommentDialog.value)
      : pendingDraftKey(previousPath);
    hunkDrafts.set(key, commentText.value);
    commentText.value = hunkDrafts.get(pendingDraftKey(path)) ?? "";
    commentFile.value = null;
  }
  commentPrompt.value = null;
});

watch([selectedDiff, selectedTo, () => props.cwd], () => {
  resetComments();
  hunkDrafts.clear();
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
      previewComment.value = null;
      previewCommentText.value = "";
      commitMessages.value = [];

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

function syncDeletedOverview() {
  if (!isMobile.value || !diffEditor) return;
  const modified = diffEditor.getModifiedEditor();
  const overview = editorHost.querySelector<HTMLElement>(".diffOverview");
  if (!overview) return;
  const scale = overview.clientHeight / modified.getScrollHeight();
  const zones = modified.getDomNode()?.querySelectorAll(".inline-deleted-margin-view-zone");
  const lineHeight = modified.getOption(monacoMod!.editor.EditorOption.lineHeight);
  let zoneIndex = 0;
  const ranges = (diffEditor.getLineChanges() ?? []).flatMap((change) => {
    if (change.originalEndLineNumber < 1 ||
        change.originalEndLineNumber < change.originalStartLineNumber) return [];
    const zone = zones?.[zoneIndex++];
    if (!zone) return [];
    const height = zone.querySelectorAll(":scope > .delete-sign").length * lineHeight;
    if (!height) return [];
    const lineCount = modified.getModel()!.getLineCount();
    const line = change.modifiedStartLineNumber + (change.modifiedEndLineNumber === 0 ? 1 : 0);
    const bottom = line > lineCount
      ? modified.getContentHeight() - Math.max(
          modified.getOption(monacoMod!.editor.EditorOption.padding).bottom,
          modified.getLayoutInfo().height - lineHeight,
        )
      : modified.getTopForLineNumber(Math.max(1, line));
    return [{ top: Math.floor((bottom - height) * scale), bottom: Math.ceil(bottom * scale) }];
  });
  let lane = overview.querySelector<HTMLElement>(".diff-rethink-deletion-overview");
  if (!ranges.length) {
    lane?.remove();
    if (overview.classList.contains("diff-rethink-aligned-deletions"))
      overview.classList.remove("diff-rethink-aligned-deletions");
    return;
  }
  if (!lane) {
    lane = document.createElement("div");
    lane.className = "diff-rethink-deletion-overview";
    overview.append(lane);
  }
  if (!overview.classList.contains("diff-rethink-aligned-deletions"))
    overview.classList.add("diff-rethink-aligned-deletions");
  ranges.forEach(({ top, bottom }, index) => {
    let mark = lane.children[index] as HTMLElement | undefined;
    if (!mark) {
      mark = document.createElement("div");
      mark.className = "diff-rethink-deletion-overview-mark";
      lane.append(mark);
    }
    const markTop = Math.max(0, Math.min(top, bottom - 4));
    const markHeight = Math.max(4, bottom - markTop);
    if (mark.style.top !== `${markTop}px`) mark.style.top = `${markTop}px`;
    if (mark.style.height !== `${markHeight}px`) mark.style.height = `${markHeight}px`;
  });
  while (lane.children.length > ranges.length) lane.lastElementChild?.remove();
}

function createEditor() {
  if (!props.isOpen || !monacoLoaded.value || !editorContainerRef.value || !monacoMod) return;
  if (editorHost.parentElement !== editorContainerRef.value) {
    editorContainerRef.value.appendChild(editorHost);
  }
  if (diffEditor) {
    commentsCleanup?.();
    commentsCleanup = attachComments(diffEditor.getModifiedEditor(), editorContainerRef.value);
    editorReady.value = true;
    return;
  }
  const monaco = monacoMod;
  diffEditor = monaco.editor.createDiffEditor(editorHost, {
    theme: isDarkModeActive() ? "vs-dark" : "vs",
    readOnly: true,
    domReadOnly: true,
    tabIndex: -1,
    renderLineHighlight: "none",
    dragAndDrop: false,
    originalEditable: false,
    automaticLayout: true,
    renderSideBySide: false,
    renderSideBySideInlineBreakpoint:900,
    hideUnchangedRegions:{enabled:false,contextLineCount:3,minimumLineCount:6,revealLineCount:10},
    ignoreTrimWhitespace:false,
    renderOverviewRuler:true,
    enableSplitViewResizing: true,
    renderIndicators: true,
    renderMarginRevertIcon: false,
    lineNumbers: "off",
    minimap: { enabled: false },
    scrollBeyondLastLine: true,
    smoothScrolling: true,
    wordWrap: "on",
    maxComputationTime: 1000,
    glyphMargin: false,
    lineDecorationsWidth: MOBILE_LINE_DECORATIONS_WIDTH,
    lineNumbersMinChars: 0,
    scrollbar: {
      verticalScrollbarSize: MOBILE_SCROLLBAR_SIZE,
      horizontalScrollbarSize: MOBILE_SCROLLBAR_SIZE,
    },
    overviewRulerLanes: 0,
    quickSuggestions: false,
    suggestOnTriggerCharacters: false,
    lightbulb: { enabled: false },
    codeLens: false,
    contextmenu: false,
    links: false,
    folding: false,
    padding: {bottom:16},
  });
  editorReady.value = true;

  diffEditor.onDidUpdateDiff(() => {
    changeCount.value = diffEditor?.getLineChanges()?.length ?? 0;
    syncDeletedOverview();
    markCurrentHunk();
    markCommentTarget();
  });
  editorSizeObserver?.disconnect();
  editorSizeObserver = new ResizeObserver(() => {
    if (editorHost.clientHeight <= 0) return;
    if (showCommentDialog.value) requestAnimationFrame(revealCommentTarget);
    else initialReveal?.();
  });
  editorSizeObserver.observe(editorHost);
  editorHost.addEventListener('click', expandHiddenContext);
  const modEditor = diffEditor.getModifiedEditor();
  modEditor.onDidLayoutChange(syncDeletedOverview);
  currentHunkZoneObserver = new MutationObserver(() => {
    markCurrentDeletedZone();
    markDeletedCommentTarget();
    syncDeletedOverview();
  });
  currentHunkZoneObserver.observe(editorHost, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["class"],
  });
  diffEditor.getOriginalEditor().updateOptions({ domReadOnly: true, tabIndex: -1, renderLineHighlight: "none" });

  let previousScrollTop = modEditor.getScrollTop();
  modEditor.onDidScrollChange(({ scrollTopChanged, scrollHeightChanged, scrollTop }) => {
    if (scrollTopChanged && lineTapPrompt.value && filesPaneRef.value) {
      lineTapPrompt.value.bubbleTop = Math.max(0, Math.min(filesPaneRef.value.clientHeight - 43,
        lineTapPrompt.value.bubbleTop + previousScrollTop - scrollTop));
    }
    previousScrollTop = scrollTop;
    if (scrollHeightChanged) syncDeletedOverview();
    if (!scrollTopChanged || !isMobile.value || showCommentDialog.value || revealPending.value ||
        fileDiff.value?.path !== selectedFile.value) return;
    const changes = diffEditor?.getLineChanges();
    if (!changes?.length) return;
    const focusTop = modEditor.getScrollTop() + modEditor.getLayoutInfo().height / 2;
    let index = -1;
    for (let i = 0; i < changes.length; i++) {
      if (modEditor.getTopForLineNumber(changes[i].modifiedStartLineNumber || 1) <= focusTop) index = i;
    }
    currentChangeIndex.value = Math.max(0, index);
  });

  // Comment-mode interactions (click-to-comment, selection prompt, hover
  // indicator) via the shared composable.
  commentsCleanup = attachComments(modEditor, editorContainerRef.value);
}
let editorSizeObserver:ResizeObserver|null=null;
let initialReveal: (() => void) | null = null;
function expandHiddenContext(e:MouseEvent){const target=e.target as Element,bar=target.closest('.diff-hidden-lines'),link=bar?.querySelector('a');if(link&&!link.contains(target)){e.preventDefault();(link as HTMLElement).click();}}
let draftNotesCleanup: (() => void) | null = null;
watch(
  [() => props.reviewNotes, fileDiff, modelVersion, editorReady, selectedDiff, selectedTo, gitRoot, () => props.reviewMachine],
  () => {
    draftNotesCleanup?.();
    draftNotesCleanup = null;
    const fd = fileDiff.value;
    if (!diffEditor || !editorReady.value || !fd || isCommitMessageFile(fd.path) || !selectedDiff.value) return;
    const model = diffEditor.getModel();
    if (!model) return;
    const matching = (side: "left" | "right") => (props.reviewNotes ?? []).filter((note) =>
      note.line !== null && note.root === gitRoot.value && note.machine === reviewMachine() &&
      note.diff === selectedDiff.value && note.to === selectedTo.value && note.path === fd.path &&
      note.side === side && note.snapshot === (side === "left" ? fd.oldContent : fd.newContent));
    const cleanups = [
      mountDraftDiffComments(diffEditor.getOriginalEditor(), matching("left")),
      mountDraftDiffComments(diffEditor.getModifiedEditor(), matching("right")),
    ];
    draftNotesCleanup = () => cleanups.forEach((fn) => fn());
  },
  { deep: true, flush: "post" },
);
onUnmounted(() => draftNotesCleanup?.());
let commentsCleanup: (() => void) | null = null;
let highlightsCleanup: (() => void) | null = null;

function disposeEditor() {
  if (!diffEditor) return;
  editorReady.value = false;
  initialReveal = null;
  commentsCleanup?.();
  commentsCleanup = null;
  highlightsCleanup?.();
  highlightsCleanup = null;
  const model = diffEditor.getModel();
  editorSizeObserver?.disconnect();
  currentHunkZoneObserver?.disconnect();
  currentHunkZoneObserver = null;
  editorHost.removeEventListener('click',expandHiddenContext);
  diffEditor.dispose();
  model?.original.dispose();
  model?.modified.dispose();
  diffEditor = null;
  currentHunkDecorations = null;
  commentTargetDecorations = null;

}

// Create/dispose the editor when isOpen + monacoLoaded change.
watch(
  [() => props.isOpen, monacoLoaded],
  () => {
    if (props.isOpen && monacoLoaded.value) {
      nextTick(() => createEditor());
    } else if (diffEditor) {
      editorReady.value = false;
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


// Swap models into the existing editor when fileDiff changes.
let diffUpdateDisposable: Monaco.IDisposable | null = null;
watch(
  [editorReady, fileDiff, showBinaryAsText],
  () => {
    if (!diffEditor) return;
    if (editorBlocked.value) {
      initialReveal = null;
      highlightsCleanup?.();
      highlightsCleanup = null;
      diffUpdateDisposable?.dispose();
      diffUpdateDisposable = null;
      commentPrompt.value = null;
      revealPending.value = false;
      changeCount.value = 0;
      currentChangeIndex.value = -1;
      const previous = diffEditor.getModel();
      diffEditor.setModel(null);
      previous?.original.dispose();
      previous?.modified.dispose();
      return;
    }
    if (!editorReady.value || !fileDiff.value || !monacoMod) return;
    const monaco = monacoMod;
    const fd = fileDiff.value;
    diffEditor.updateOptions({
      wordWrap:
        (fd.oldBytes ?? fd.oldContent.length) + (fd.newBytes ?? fd.newContent.length) > 512 * 1024
          ? "off"
          : "on",
    });

    const isCommitMsg = isCommitMessageFile(fd.path);

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
    modelVersion.value++;

    // For new/deleted files one side is empty; shrink it so the non-empty
    // side gets most of the width instead of wasting half the screen.
    let splitViewRatio = 0.5;
    if (fd.oldContent === "" && fd.newContent !== "") {
      splitViewRatio = EMPTY_SIDE_SPLIT_RATIO;
    } else if (fd.newContent === "" && fd.oldContent !== "") {
      splitViewRatio = 1 - EMPTY_SIDE_SPLIT_RATIO;
    }
    diffEditor.updateOptions({
      readOnly: true,
      domReadOnly: true,
      tabIndex: -1,
      renderLineHighlight: "none",
      dragAndDrop: false,
      splitViewDefaultRatio: splitViewRatio,
    });
    diffEditor.getModifiedEditor().updateOptions({ readOnly: true, domReadOnly: true, tabIndex: -1, renderLineHighlight: "none", dragAndDrop: false });
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
        if (changes && revealLastChangeForPath === fd.path) {
          hasScrolledToFirstChange = true;
          revealLastChangeForPath = null;
          if (changes.length > 0) {
            const index = changes.length - 1;
            const targetLine = changes[index].modifiedStartLineNumber || 1;
            const editor = diffEditor.getModifiedEditor();
            editor.revealLineInCenter(targetLine, monaco.editor.ScrollType.Immediate);
            editor.setPosition({ lineNumber: targetLine, column: 1 });
            currentChangeIndex.value = index;
          } else {
            diffEditor.getModifiedEditor().setScrollTop(0);
          }
        } else if (changes && revealLine !== null) {
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
        if (hasScrolledToFirstChange) {
          revealPending.value = false;
          if (showCommentDialog.value && commentFile.value !== fd.path) commentOnCurrentHunk();
        }
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

    const mobileCommit = isMobile.value && !initialCommit
      ? response.diffs.find((diff) => diff.id !== "working")?.id
      : undefined;
    const selection = defaultDiffSelection(response.diffs, initialCommit ?? mobileCommit);
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
    const commitFileEntries: GitFileInfo[] = commitMessages.value.map((msg) => ({
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
    fileDiff.value = null;
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
    if (diffData.tooLarge || (diffData.binary && !asText)) revealPending.value = false;
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
  const now = performance.now();
  if (now < ignoreFileJumpClickUntil) return;
  if (mobileFileListOpen.value) {
    mobileFileListOpen.value = false;
    ignoreFileJumpClickUntil = now + 400;
  } else {
    mobileFileListOpen.value = true;
  }
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

let codeTap: { id: number; x: number; y: number; time: number; path: string | null } | null = null;

function startCodeTap(e: TouchEvent) {
  codeTap = null;
  const element = e.target;
  if (!isMobile.value || !(element instanceof Element) ||
      element.closest("button, a, [role='button'], .diffOverview, .scrollbar")) return;
  if (e.touches.length !== 1 || selectedMessage.value) return;
  if (!showCommentDialog.value &&
      !diffEditor?.getModifiedEditor().getSelection()?.isEmpty()) return;
  const touch = e.touches[0];
  codeTap = { id: touch.identifier, x: touch.clientX, y: touch.clientY, time: e.timeStamp, path: selectedFile.value };
}

function moveCodeTap(e: TouchEvent) {
  const touch = e.touches[0];
  if (codeTap && (e.touches.length !== 1 || touch.identifier !== codeTap.id ||
      Math.hypot(touch.clientX - codeTap.x, touch.clientY - codeTap.y) > 8)) codeTap = null;
}

function endCodeTap(e: TouchEvent) {
  const tap = codeTap;
  codeTap = null;
  if (!tap || e.touches.length || e.changedTouches.length !== 1 || e.timeStamp - tap.time > 300 ||
      tap.path !== selectedFile.value || loading.value || revealPending.value || editorBlocked.value ||
      recordingLocked.value || mobileFileListOpen.value) return;
  const touch = e.changedTouches[0];
  if (touch.identifier !== tap.id || Math.hypot(touch.clientX - tap.x, touch.clientY - tap.y) > 8) return;
  if (showCommentDialog.value) {
    e.preventDefault();
    if (commentText.value.trim()) commentInput.value?.focus({ preventScroll: true });
    else showCommentDialog.value = null;
    return;
  }
  if (!diffEditor || !monacoMod) return;
  if (!(e.target as Element).closest(".view-line")) {
    lineTapPrompt.value = null;
    return;
  }
  const editor = diffEditor.getModifiedEditor();
  if (!editor.getSelection()?.isEmpty()) return;
  const deletedZone = (e.target as Element).closest<HTMLElement>(".view-lines.line-delete[monaco-view-zone]");
  if (deletedZone) {
    const zones = editor.getDomNode()?.querySelectorAll(".view-lines.line-delete[monaco-view-zone]");
    const index = zones ? Array.from(zones).indexOf(deletedZone) : -1;
    const change = diffEditor.getLineChanges()?.filter(change => change.originalEndLineNumber > 0)[index];
    if (!change || window.getSelection()?.toString()) return;
    const line = deletedLineAtOffset(change, tap.y - deletedZone.getBoundingClientRect().top);
    e.preventDefault();
    showLineTapPrompt(e.target as Element, "left", line);
    return;
  }
  const target = editor.getTargetAtClientPoint(tap.x, tap.y);
  const type = monacoMod.editor.MouseTargetType;
  if (!target || (target.type !== type.CONTENT_TEXT && target.type !== type.CONTENT_EMPTY)) return;
  const line = target.position.lineNumber;
  e.preventDefault();
  showLineTapPrompt(e.target as Element, "right", line);
}

function showLineTapPrompt(element: Element, side: "left" | "right", line: number) {
  const pane = filesPaneRef.value?.getBoundingClientRect();
  const row = element.closest(".view-line")?.getBoundingClientRect();
  if (!pane || !row) return;
  const previous = lineTapPrompt.value;
  if (previous?.side === side && previous.anchor === line && previous.startLine === previous.endLine) {
    lineTapPrompt.value = null;
    return;
  }
  const anchor = previous?.side === side ? previous.anchor : line;
  const startLine = Math.min(anchor, line);
  const endLine = Math.max(anchor, line);
  const top = Math.max(0, row.top - pane.top);
  const bottom = Math.min(pane.height, row.bottom - pane.top);
  lineTapPrompt.value = {
    side, line: startLine, anchor, startLine, endLine,
    bubbleTop: bottom + 46 <= pane.height ? bottom + 3 : Math.max(0, top - 43),
  };
}

function openTappedLineComment() {
  const target = lineTapPrompt.value;
  if (!target) return;
  const index = codeCommentHunkIndex(diffEditor?.getLineChanges() ?? [], target.side, target.line);
  if (index >= 0) currentChangeIndex.value = index;
  openCodeComment(target.side, target.startLine, target.endLine);
}

function startCodeSwipe(e: TouchEvent) {
  if (selectedMessage.value || !fileDiff.value || !diffEditor ||
      (e.target as Element).closest("button, a, [role='button'], .scrollbar, .diffOverview")) return;
  const editor = diffEditor.getModifiedEditor();
  if (!editor.getSelection()?.isEmpty() ||
      editor.getScrollWidth() > editor.getLayoutInfo().contentWidth + 1) return;
  startFileSwipe(e);
}

let overviewTouch: { id: number; element: HTMLElement; offset: number } | null = null;

function scrollOverview(touch: Touch) {
  if (!overviewTouch || !diffEditor) return;
  const editor = diffEditor.getModifiedEditor();
  const bounds = overviewTouch.element.getBoundingClientRect();
  const thumbHeight = overviewTouch.element.querySelector(".diffViewport")?.getBoundingClientRect().height ?? 0;
  const travel = bounds.height - thumbHeight;
  const maxScroll = editor.getScrollHeight() - editor.getLayoutInfo().height;
  if (travel > 0 && maxScroll > 0) {
    const position = (touch.clientY - bounds.top - overviewTouch.offset) / travel;
    editor.setScrollTop(Math.max(0, Math.min(1, position)) * maxScroll);
  }
}

function startOverviewTouch(e: TouchEvent) {
  const element = (e.target as Element).closest<HTMLElement>(".diffOverview");
  if (!isMobile.value || !diffEditor || !element || e.touches.length !== 1) return;
  const touch = e.touches[0];
  const thumb = element.querySelector(".diffViewport")?.getBoundingClientRect();
  overviewTouch = {
    id: touch.identifier,
    element,
    offset: thumb && touch.clientY >= thumb.top && touch.clientY <= thumb.bottom
      ? touch.clientY - thumb.top
      : (thumb?.height ?? 0) / 2,
  };
  e.preventDefault();
  scrollOverview(touch);
}

function moveOverviewTouch(e: TouchEvent) {
  if (!overviewTouch) return;
  const touch = Array.from(e.touches).find((item) => item.identifier === overviewTouch?.id);
  if (!touch) return;
  e.preventDefault();
  scrollOverview(touch);
}

function endOverviewTouch(e: TouchEvent) {
  if (e.changedTouches.length && e.changedTouches[0].identifier === overviewTouch?.id)
    overviewTouch = null;
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

  const order = navOrder.value;
  const index = currentFileIndex.value;
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
  revealLastChangeForPath = null;
  if (isMobile.value && isCommitMessageFile(path)) {
    rawMessage.value = false;
  }
  const changedFile = selectedFile.value !== path;
  selectedFile.value = path;
  if (changedFile) currentChangeIndex.value = -1;
}

function goToNextFile(animate = false): boolean {
  resetFileSwipeAnimation();
  const order = navOrder.value;
  if (order.length === 0 || !selectedFile.value) return false;
  const idx = currentFileIndex.value;
  if (idx >= 0 && idx < order.length - 1) {
    if (animate && isMobile.value) fileSwipeDirection.value = "next";
    selectNavItem(order[idx + 1]);
    return true;
  }
  return false;
}

function goToPreviousFile(revealLastChange = false, animate = false): boolean {
  resetFileSwipeAnimation();
  const order = navOrder.value;
  if (order.length === 0 || !selectedFile.value) return false;
  const idx = currentFileIndex.value;
  if (idx > 0) {
    const path = order[idx - 1];
    if (animate && isMobile.value) fileSwipeDirection.value = "prev";
    selectNavItem(path);
    if (revealLastChange && !isCommitMessageFile(path)) revealLastChangeForPath = path;
    return true;
  }
  return false;
}

function revealChange(change: Monaco.editor.ILineChange, index: number) {
  if (!diffEditor || !monacoMod) return;
  const editor = diffEditor.getModifiedEditor();
  const lineNumber = change.modifiedStartLineNumber || 1;
  // An animated reveal fires scroll events for intermediate hunks, which
  // can replace the selected index before the next navigation click.
  editor.revealLineInCenter(lineNumber, monacoMod.editor.ScrollType.Immediate);
  editor.setPosition({ lineNumber, column: 1 });
  currentChangeIndex.value = index;
}

function goToNextChange() {
  if (editorBlocked.value) { goToNextFile(true); return; }
  if(mobileOverview.value || (selectedMessage.value && !rawMessage.value)){goToNextFile(true);return;}
  if (!diffEditor) return;
  const changes = diffEditor.getLineChanges();
  if (!changes || changes.length === 0) {
    goToNextFile(true);
    return;
  }
  const nextIdx = currentChangeIndex.value + 1;
  if (nextIdx >= changes.length) {
    if (goToNextFile(true)) return;
    return;
  }
  revealChange(changes[nextIdx], nextIdx);
}

function goToPreviousChange() {
  if (editorBlocked.value) { goToPreviousFile(false, true); return; }
  if(mobileOverview.value || (selectedMessage.value && !rawMessage.value)){goToPreviousFile(false, true);return;}
  if (!diffEditor) return;
  const changes = diffEditor.getLineChanges();
  if (!changes || changes.length === 0) {
    goToPreviousFile(true, true);
    return;
  }
  const prevIdx = currentChangeIndex.value <= 0 ? -1 : currentChangeIndex.value - 1;
  if (prevIdx < 0) {
    if (goToPreviousFile(true, true)) return;
    revealChange(changes[0], 0);
    return;
  }
  revealChange(changes[prevIdx], prevIdx);
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
  if (props.reviewOpen) return;
  if (e.key === "Escape") {

    // If Monaco's find widget is open, let Monaco close it.
    const findWidget = editorContainerRef.value?.querySelector(".find-widget.visible");
    if (findWidget) return;

    if(mobileFileListOpen.value){mobileFileListOpen.value=false;return;}
    if (previewComment.value) { previewComment.value = null; return; }
    if (showCommentDialog.value) {
      showCommentDialog.value = null;
    } else {
      // Closing the diff can re-enable the covered graph's Escape handler
      // before this key reaches the bubble phase.
      e.stopPropagation();
      requestClose();
    }
    return;
  }
  if ((e.ctrlKey || e.metaKey) && e.key === "s") {
    e.preventDefault();
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
  if (e.target instanceof Element &&
      e.target.closest('input, textarea, [contenteditable="true"]') &&
      !editorContainerRef.value?.contains(e.target)) return;
  if (previewComment.value || showCommentDialog.value) return;
  // Intercept PageUp/PageDown to scroll the diff editor.
  if (e.key === "PageUp" || e.key === "PageDown") {
    if(mobileOverview.value || (selectedMessage.value && !rawMessage.value))return;
    if (diffEditor) {
      e.preventDefault();
      e.stopPropagation();
      const modEditor = diffEditor.getModifiedEditor();
      modEditor.trigger("keyboard", e.key === "PageUp" ? "cursorPageUp" : "cursorPageDown", null);
    }
    return;
  }
  // Comment mode navigation (only when comment dialogs are closed).
  if (!showCommentDialog.value) {
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
const rangeOverviewPath = computed(() =>
  isCommitRange.value && commitMessages.value[0]
    ? COMMIT_MSG_PREFIX + commitMessages.value[0].hash
    : null,
);
const treeEntries = computed<DiffFileTreeEntry[]>(() => {
  const visibleFiles = rangeOverviewPath.value
    ? files.value.filter((f) => !isCommitMessageFile(f.path) || f.path === rangeOverviewPath.value)
    : files.value;
  const msgByHash = new Map(commitMessages.value.map((m) => [m.hash, m]));
  return visibleFiles.map((f) => {
    if (isCommitMessageFile(f.path)) {
      const hash = commitHashFromPath(f.path);
      const msg = msgByHash.get(hash);
      const subject = msg ? msg.subject : hash.slice(0, 8);
      const leaf = rangeOverviewPath.value ? `${commitMessages.value.length} commits` : subject;
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

const currentFileIndex = computed(() =>
  navOrder.value.indexOf(isCommitRange.value && selectedMessage.value
    ? rangeOverviewPath.value ?? ""
    : selectedFile.value ?? ""),
);
const realFilePosition = computed(() => {
  const paths = navOrder.value.filter((path) => !isCommitMessageFile(path));
  const index = paths.indexOf(selectedFile.value ?? "");
  return index < 0 ? null : `File ${index + 1} of ${paths.length}`;
});
const hasNextFile = computed(
  () => currentFileIndex.value >= 0 && currentFileIndex.value < navOrder.value.length - 1,
);
const hasPrevFile = computed(() => currentFileIndex.value > 0);
const prevChangeGoesToFile = computed(() =>
  hasPrevFile.value && (editorBlocked.value || changeCount.value === 0 || currentChangeIndex.value <= 0),
);
const prevChangeGoesToCommit = computed(() =>
  prevChangeGoesToFile.value && isCommitMessageFile(navOrder.value[currentFileIndex.value - 1]),
);
const nextChangeGoesToFile = computed(() =>
  hasNextFile.value && (editorBlocked.value || changeCount.value === 0 || currentChangeIndex.value >= changeCount.value - 1),
);


// --- Event handlers from child components ---
function selectSidebarFile(path: string) {
  resetFileSwipeAnimation();
  selectNavItem(path);
  rawMessage.value = false;
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
  window.visualViewport?.addEventListener("resize", syncCommentViewport);
  window.visualViewport?.addEventListener("scroll", syncCommentViewport);
});
onUnmounted(() => {
  window.removeEventListener("resize", handleResize);
  window.visualViewport?.removeEventListener("resize", syncCommentViewport);
  window.visualViewport?.removeEventListener("scroll", syncCommentViewport);
  window.removeEventListener("keydown", handleKeyDown, true);
  themeObserver?.disconnect();
  diffUpdateDisposable?.dispose();

  disposeEditor();
});
</script>

<style scoped>
.diff-viewer-rethink { --diff-added:#3e7955;--diff-deleted:#a45160; }
.added { color:var(--diff-added); }.deleted {color:var(--diff-deleted)}
.diff-viewer-header {background:var(--bg-base);padding:7px 12px}
.diff-rethink-current-counts,.diff-rethink-totals {display:inline-flex;gap:8px;white-space:nowrap;font:11px/18px ui-monospace,SFMono-Regular,Menlo,monospace}
.diff-rethink-current-counts {margin-left:10px}.diff-rethink-totals {margin-left:auto;font-weight:400}
.diff-viewer-main {display:flex;flex-direction:column;min-height:0;overflow:hidden}
.diff-viewer-files-pane {position:relative;flex:1;min-height:0;height:auto;display:flex;flex-direction:column}
.diff-viewer-editor {flex:1;min-height:0}.diff-commit-message {flex:1;min-height:0;overflow:auto}
.diff-viewer-large {touch-action:pan-y}
.diff-rethink-reveal-wait {position:absolute;inset:0;z-index:2;display:grid;place-items:center;background:var(--bg-base);color:var(--text-secondary);font:12px/20px system-ui,sans-serif}
.diff-viewer-readonly .diff-viewer-editor :deep(.cursors-layer .cursor) {visibility:hidden!important}
.diff-viewer-editor :deep(.line-insert) {background:color-mix(in srgb,#6fba87 19%,var(--bg-base))!important}
.diff-viewer-editor :deep(.line-delete) {background:color-mix(in srgb,#c67b86 19%,var(--bg-base))!important}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .center) {justify-content:center!important;align-items:center;gap:6px;width:100%;font:500 11px/18px system-ui,sans-serif;color:var(--text-secondary)}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .diff-hidden-lines) {background:var(--bg-secondary)!important;border-top:1px solid var(--border-color);border-bottom:1px solid var(--border-color)}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .center a) {display:inline-flex;align-items:center;justify-content:center;width:20px;border-radius:3px}
.diff-viewer-editor :deep(.diff-hidden-lines-widget .center a:hover) {background:var(--bg-tertiary)}
.diff-rethink-mobile-files {position:absolute;top:60px;left:0;right:0;z-index:41;display:flex;flex-direction:column;max-height:calc(100% - 60px);min-height:0;background:var(--bg-base);border-bottom:1px solid var(--border-color);box-shadow:0 8px 24px #14243a22;border-radius:0 0 8px 8px;padding:0 14px 10px}
.diff-rethink-mobile-files.is-overview {inset:0;max-height:none;border:0;border-radius:0;box-shadow:none;padding:0 14px 14px;overflow-y:auto;display:block}

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
.diff-rethink-change-nav{display:flex;gap:2px}.diff-rethink-change-nav button{min-height:36px;min-width:32px;border:0;background:none;font:17px/20px system-ui,sans-serif;color:inherit}
@media(max-width:767px){
.diff-viewer-header-mobile{padding:5px 8px;gap:6px;flex-direction:row;align-items:center;flex:none}
.diff-viewer-header-main,.diff-viewer-mobile-selectors{min-width:0}.diff-viewer-mobile-selectors{width:100%}
.diff-viewer-header-mobile :deep(.commit-picker){width:100%;min-width:0}
.diff-viewer-content{min-height:0}.diff-viewer-main{height:100%;width:100%}
.diff-viewer-rethink{width:100%;height:100%;border-radius:0}
}
:global(.dark) .diff-viewer-rethink {--diff-added:#88ba9a;--diff-deleted:#d39aa4}
</style>
