<!-- Vue port of components/ChatInterface.tsx. The main chat shell: message
     list (via Message.vue), streaming/tool-progress, composer, context-usage
     bar, terminal/diff/git panels, model/thinking pickers, distill, TOC,
     scroll behavior. Preserves the e2e DOM/ARIA/CSS contract. -->
<template>
  <div ref="chatRootRef" class="full-height flex flex-col">
    <!-- Header -->
    <div class="header">
      <div class="header-left">
        <Button
          class="btn-icon hide-on-desktop"
          text
          severity="secondary"
          :aria-label="t('openConversations')"
          @click="props.onOpenDrawer()"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M4 6h16M4 12h16M4 18h16"
            />
          </svg>
        </Button>

        <Button
          v-if="isDrawerCollapsed && onToggleDrawerCollapse"
          class="btn-icon show-on-desktop-only"
          text
          severity="secondary"
          :aria-label="t('expandSidebar')"
          v-tooltip.top="t('expandSidebar')"
          @click="onToggleDrawerCollapse && onToggleDrawerCollapse()"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M13 5l7 7-7 7M5 5l7 7-7 7"
            />
          </svg>
        </Button>

        <h1 class="app-bar-title header-title" :title="currentConversation?.slug || 'Shelley'">
          {{ displayTitle }}
        </h1>
      </div>

      <div class="header-actions">
        <button class="btn-new" :aria-label="t('newConversation')" @click="onNewConversationClick">
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-icon-1rem">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M12 4v16m8-8H4"
            />
          </svg>
        </button>

        <!-- Overflow menu (PrimeVue Popover + Select) -->
        <ChatOverflowMenu
          :has-cwd="hasCwd"
          :links="links"
          :can-archive="
            !!(conversationId && onArchiveConversation && !currentConversation?.archived)
          "
          :can-export="!!(conversationId && messages.length > 0)"
          :has-update="hasUpdate"
          @open-command-palette="props.onOpenCommandPalette?.()"
          @open-diffs="showDiffViewer = true"
          @open-git-graph="showGitGraph = true"
          @open-terminal="openInAppTerminal"
          @open-external-link="openExternalLink"
          @archive="archiveFromMenu"
          @export="openExport"
          @edit-agents-md="showAgentsMdEditor = true"
          @edit-file="props.onOpenFileFinder?.()"
          @check-version="openVersionModal"
        />
      </div>
    </div>

    <!-- Messages area -->
    <div class="messages-area-wrapper" :aria-busy="loading">
      <div ref="messagesContainerRef" class="messages-container scrollable">
        <div v-if="!loading || renderingConversation" ref="messagesListRef" class="messages-list">
          <!-- empty state -->
          <div v-if="messages.length === 0" class="empty-state">
            <div class="empty-state-content">
              <p class="text-base chat-welcome-text">
                <template v-for="(part, i) in welcomeParts" :key="i">
                  <strong v-if="part === '{hostname}'">{{ hostname }}</strong>
                  <a
                    v-else-if="part === '{openSourceLink}'"
                    href="https://github.com/boldsoftware/shelley/"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="chat-welcome-link"
                    >{{ t("welcomeOpenSource") }}</a
                  >
                  <a
                    v-else-if="part === '{customizeLink}'"
                    href="https://github.com/boldsoftware/shelley/"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="chat-welcome-link"
                    >{{ t("welcomeCustomize") }}</a
                  >
                  <a
                    v-else-if="part === '{docsLink}'"
                    href="https://exe.dev/docs/proxy"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="chat-welcome-link"
                    >docs</a
                  >
                  <a
                    v-else-if="part === '{proxyLink}'"
                    :href="proxyURL"
                    target="_blank"
                    rel="noopener noreferrer"
                    class="chat-welcome-link"
                    >{{ proxyURL }}</a
                  >
                  <template v-else>{{ part }}</template>
                </template>
              </p>
              <PvMessage v-if="models.length === 0" severity="warn" class="no-models-message">
                <p class="no-models-title">{{ t(modelSetupHint.title) }}</p>
                <p v-if="modelSetupHint.note">{{ t(modelSetupHint.note) }}</p>
                <!-- Render each remedy as the literal command it runs, so the
                     user can see what a click does (and copy it to a terminal
                     instead if they prefer). -->
                <ul v-if="modelSetupHint.actions.length" class="no-models-commands">
                  <li v-for="action in modelSetupHint.actions" :key="action.command">
                    <a :href="suggestURL(action.command)" target="_blank" rel="noopener noreferrer"
                      ><code>{{ sshCommandLine(action.command) }}</code></a
                    >
                  </li>
                </ul>
                <p v-if="modelSetupHint.footer">{{ t(modelSetupHint.footer) }}</p>
              </PvMessage>
              <p v-else class="text-sm chat-secondary-text">{{ t("sendMessageToStart") }}</p>
            </div>
          </div>
          <!-- generations -->
          <template v-for="block in renderModel" :key="`gen-${block.generation}`">
            <div v-if="block.divider" class="generation-divider">
              <span
                >New generation started — older messages are retained here but no longer sent to the
                LLM.</span
              >
            </div>
            <div :class="block.sectionClass">
              <ModelBar
                :key="block.modelBar.key"
                :model="block.modelBar.model"
                :models-used="block.modelBar.modelsUsed"
                :models="models"
                :thinking-level="conversationThinkingLevel"
              />
              <SystemPromptView
                v-for="sp in block.systemPrompts"
                :key="sp.key"
                :message="sp.message"
              />
              <ChunkHost
                v-for="chunk in block.chunks"
                :key="chunk.key"
                :chunk="chunk"
                :conversation-id="conversationId"
                :on-open-diff-viewer="handleOpenDiffViewer"
                :on-comment-text-change="setDiffCommentText"
                :on-fork="forkHandler"
              />
            </div>
          </template>
          <!-- streaming preview -->
          <div
            v-if="showStreamingPreview || showStreamingThinking"
            class="message message-agent streaming-message"
          >
            <div class="message-content" data-testid="message-content">
              <ThinkingContent v-if="showStreamingThinking" :thinking="streamingThinking" />
              <div
                v-if="showStreamingPreview && markdownMode === 'off'"
                class="whitespace-pre-wrap break-words"
              >
                {{ streamingText }}<span class="streaming-cursor">▊</span>
              </div>
              <div v-else-if="showStreamingPreview" class="streaming-markdown">
                <MarkdownContent :text="streamingText" />
                <span class="streaming-cursor">▊</span>
              </div>
            </div>
          </div>
          <!-- ghost pending (queued) messages at the bottom -->
          <QueuedGhostMessage
            v-for="qm in queuedGhosts"
            :key="`queued-${qm.id}`"
            :queued="qm"
            :on-cancel="conversationId ? cancelQueuedMessage : undefined"
          />
          <div v-if="queuedGhosts.length > 1 && conversationId" class="queued-cancel-all-row">
            <button
              class="queued-message-badge-cancel"
              data-testid="cancel-all-queued"
              v-tooltip.top="'Cancel all queued messages'"
              @click="cancelQueuedMessages"
            >
              Cancel all queued
            </button>
          </div>
          <div ref="bottomSentinelRef" class="messages-bottom-sentinel" aria-hidden="true" />
        </div>
      </div>

      <div v-if="loading" class="conversation-loading-overlay">
        <div v-if="showLoadingProgressUI" class="conversation-loading">
          <div class="spinner" />
          <div class="conversation-loading-title" role="status" aria-live="polite">
            {{ loadingTitle }}
          </div>
          <div class="conversation-loading-subtitle">{{ loadingSubtitle }}</div>
          <div class="conversation-loading-bar">
            <div :class="loadingBarFillClass" :style="loadingBarFillStyle" />
          </div>
        </div>
        <div v-else class="flex items-center justify-center full-height">
          <div class="spinner" />
        </div>
      </div>

      <!-- Floating nav cluster -->
      <div v-if="conversationId && messages.length > 0" class="chat-nav-cluster">
        <ConversationTOC
          :messages="visibleMessages"
          :container-ref="messagesContainerRef"
          :near-bottom="!showScrollToBottom"
          :conversation-slug="currentConversation?.slug"
          @scroll-bottom="scrollToBottom"
          @scroll-away="markUserScrolledUp"
        />
        <button
          v-if="showScrollToBottom"
          class="scroll-to-bottom-button"
          aria-label="Scroll to bottom"
          v-tooltip.top="scrollToBottomTooltip"
          @click="scrollToBottom"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-scroll-icon">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M19 14l-7 7m0 0l-7-7m7 7V3"
            />
          </svg>
        </button>
      </div>
    </div>

    <!-- Terminal Panel -->
    <TerminalPanel
      :terminals="ephemeralTerminals"
      :conversation-id="conversationId"
      :model="selectedModel"
      :auto-focus-id="terminalAutoFocusId"
      :can-insert-into-input="true"
      @attached="(id, termId) => onTerminalAttached?.(id, termId)"
      @scope-change="(id, cid) => onTerminalScopeChange?.(id, cid)"
      @scope-error="(message) => (error = message)"
      @close="onTerminalCloseHandler"
      @insert-into-input="handleInsertFromTerminal"
      @auto-focus-consumed="terminalAutoFocusId = null"
      @active-terminal-exited="focusMessageInputIfUnfocused"
    />

    <!-- Status bar -->
    <div :class="statusBarClass">
      <div class="status-bar-content">
        <ChatStatusContent v-if="showStatusContent" v-bind="statusContentProps" />
      </div>
    </div>

    <!-- Message input -->
    <!-- No :key here, matching React: MessageInput must NOT remount on the
         first-message conversationId flip, or its post-await setMessage("")
         would run on a destroyed instance and the fresh instance would
         re-seed from a stale draft seed. Text sync across conversation
         switches is handled by MessageInput's draftSeed watch. -->
    <MessageInput
      v-if="!currentConversation?.archived"
      :on-send="sendMessage"
      :on-queue="queueMessage"
      :on-compact="
        conversationId && onDistillNewGeneration ? handleDistillCompactNewGeneration : undefined
      "
      :show-queue-option="!!conversationId"
      :can-queue="canQueue"
      :auto-queue="autoQueue"
      :disabled="sending || loading"
      :auto-focus="true"
      :injected-text="messageInputInjectedText"
      :draft-seed="draftSeed"
      :initial-rows="messageInputInitialRows"
      :conversation-id="conversationId"
      :lazy-draft-id="lazyDraftId"
      :model-options="readyModels"
      :current-model-id="selectedModel"
      @clear-injected-text="
        diffCommentText = '';
        terminalInjectedText = null;
      "
      @draft-change="handleDraftChange"
      @draft-send-started="handleDraftSendStarted"
      @draft-cleared="handleDraftCleared"
    >
      <template v-if="statusSlotInline" #status>
        <ChatStatusContent v-bind="statusContentProps" />
      </template>
    </MessageInput>

    <!-- Directory Picker Modal -->
    <DirectoryPickerModal
      :is-open="showDirectoryPicker"
      :initial-path="currentConversation?.cwd || selectedCwd"
      @close="showDirectoryPicker = false"
      @select="applyPickedCwd"
    />

    <MessageSelectionToolbar :on-comment="handleMessageComment" />

    <!-- Git Graph Viewer -->
    <GitGraphViewer
      :cwd="(diffViewerCwd || currentConversation?.cwd || selectedCwd) as string"
      :is-open="showGitGraph"
      :covered="showDiffViewer"
      :can-open-diff="true"
      @close="
        showGitGraph = false;
        focusMessageInputIfUnfocused();
      "
      @open-diff="
        (commit, cwd) => {
          diffViewerInitialCommit = commit;
          diffViewerCwd = cwd;
          showDiffViewer = true;
        }
      "
    />

    <!-- Image annotation view. Opened by clicking any image in the
         conversation (see composables/imageComment.ts); its comments land in
         the message input like the diff viewer's. -->
    <ImageCommentModal
      v-if="imageCommentTarget"
      :key="imageCommentTarget.src"
      :target="imageCommentTarget"
      @submit="(text) => (diffCommentText = text)"
      @close="closeImageComment"
    />

    <!-- Diff Viewer -->
    <DiffViewer
      :cwd="(diffViewerCwd || currentConversation?.cwd || selectedCwd) as string"
      :is-open="showDiffViewer"
      :initial-commit="diffViewerInitialCommit"
      @close="onDiffViewerClose"
      @comment-text-change="(text) => (diffCommentText = text)"
      @cwd-change="(cwd) => (diffViewerCwd = cwd)"
    />

    <!-- AGENTS.md Editor Modal -->
    <AgentsMdEditorModal :is-open="showAgentsMdEditor" @close="showAgentsMdEditor = false" />

    <!-- Version Checker Modal -->
    <VersionChecker
      :is-open="showVersionModal"
      :version-info="versionInfo"
      :is-loading="versionLoading"
      @close="closeVersionModal"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, provide, reactive, ref, watch } from "vue";
import Button from "primevue/button";
import PvMessage from "primevue/message";
import {
  type Message,
  type Conversation,
  type ChatRequest,
  type ToolProgress,
  type Usage,
  type LLMContent,
  isDistillStatusMessage,
  distillStatus,
  parseQueuedMessages,
  queuedMessageText,
} from "../../types";
import { api } from "../../services/api";
import { messageStore } from "../../services/messageStore";
import { cacheDiag } from "../../services/cacheDiag";
import {
  loadCachedDraft,
  saveCachedDraft,
  clearCachedDraft,
  reconcileComposerDraft,
} from "../../services/draftCache";
import { setFaviconStatus } from "../../services/favicon";
import {
  modelSetupHintKeys,
  canSendWithModel,
  needsModel,
  sshCommandLine,
  suggestURL,
} from "../../utils/modelSetupHint";
import { useMarkdownMode } from "../composables/markdownMode";
import { useConversationView } from "../composables/conversationView";
import { useI18n } from "../composables/i18n";
import { useDraftAutosave } from "../composables/draftAutosave";
import { useFeatureFlag } from "../composables/featureFlags";
import { useVersionChecker } from "../composables/versionChecker";
import { provideToolProgress } from "../composables/toolProgress";
import { closeImageComment, useImageCommentTarget } from "../composables/imageComment";
import { isImeComposing } from "../../utils/imeComposing";
import { focusMessageInputIfUnfocused } from "../../utils/focusMessageInput";
import { buildMessageQuote } from "../../utils/messageQuote";
import { hasMultipleUsers } from "../../utils/messageAuthors";
import { tildifyPath } from "../../utils/tildify";
import { handleModifiedNavClick } from "../utils/openInNewTab";
import { isAutoExpandTool } from "../../utils/toolMeta";
import { formatDay } from "../../utils/messageTime";
import {
  clearConversationViewCache,
  isHumanUserMessage,
  isVisibleConversationMessage,
} from "../../utils/conversationView";
import { SLASH_COMMANDS } from "../../utils/slashCommands";
import {
  perfCount,
  perfRecordConversationLoad,
  perfWrap,
  type ConversationLoadSource,
} from "../../utils/perf";
import {
  aggregateOtherUsage,
  type OtherUsageEntry,
  type OtherUsageRow,
  type UsageEntry,
} from "../../utils/tokenCostGraph";
import { coalesceMessages, type CoalescedItem } from "./coalesce";
import type { RenderNode, RenderChunk, GenerationBlock } from "./renderNode";
import type { EphemeralTerminal } from "./terminalTypes";
import {
  THINKING_LEVELS,
  normalizeThinkingLevelForModel,
  THINKING_LEVEL_KEY,
  storedThinkingLevel,
  type ThinkingLevel,
} from "./thinkingLevel";
import { SELECTED_MODEL_KEY, pickReadyModel, storedSelectedModel } from "./selectedModel";

import MessageInput from "./MessageInput.vue";
import ConversationTOC from "./ConversationTOC.vue";
import ModelBar from "./ModelBar.vue";
import SystemPromptView from "./SystemPromptView.vue";
import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import MessageSelectionToolbar from "./MessageSelectionToolbar.vue";
import DiffViewer from "./DiffViewer.vue";
import ImageCommentModal from "./ImageCommentModal.vue";
import GitGraphViewer from "./GitGraphViewer.vue";
import AgentsMdEditorModal from "./AgentsMdEditorModal.vue";
import TerminalPanel from "./TerminalPanel.vue";
import VersionChecker from "./VersionChecker.vue";
import ChatOverflowMenu from "./ChatOverflowMenu.vue";
import { matchChatInterfaceAction } from "../../utils/menuShortcuts";
import ChunkHost from "./ChunkHost.vue";
import { chunkMountKey } from "./chunkMount";
import QueuedGhostMessage from "./QueuedGhostMessage.vue";
import ChatStatusContent from "./ChatStatusContent.vue";
import MarkdownContent from "./MarkdownContent.vue";
import ThinkingContent from "./tools/ThinkingContent.vue";

// Props mirror ChatInterfaceProps in the React source. Callbacks that
// ChatInterface awaits or simply invokes are passed as function props
// (matching MessageInput.vue's onSend pattern) so the await semantics survive.
const props = withDefaults(
  defineProps<{
    conversationId: string | null;
    streamStatus?: "connected" | "reconnecting" | "disconnected";
    reconnectNonce?: number;
    onOpenDrawer: () => void;
    onNewConversation: () => void;
    onSelectConversation?: (conversation: Conversation) => void;
    onArchiveConversation?: (conversationId: string) => Promise<void>;
    currentConversation?: Conversation;
    onConversationUpdate?: (conversation: Conversation) => void;
    onFirstMessage?: (
      message: string,
      model: string,
      cwd?: string,
      toolOverrides?: Record<string, "on" | "off">,
      thinkingLevel?: Exclude<ThinkingLevel, "default">,
    ) => Promise<void>;
    onDistillNewGeneration?: (
      sourceConversationId: string,
      model: string,
      cwd?: string,
      method?: "default" | "compact",
      instructions?: string,
    ) => Promise<void>;
    mostRecentCwd?: string | null;
    isDrawerCollapsed?: boolean;
    onToggleDrawerCollapse?: () => void;
    openDiffViewerTrigger?: number;
    openGitGraphTrigger?: number;
    openTerminalTrigger?: number;
    modelsRefreshTrigger?: number;
    cwdSyncTrigger?: number;
    onOpenModelsModal?: () => void;
    onOpenFileFinder?: () => void;
    onOpenCommandPalette?: () => void;
    ephemeralTerminals: EphemeralTerminal[];
    setEphemeralTerminals: (
      next: EphemeralTerminal[] | ((prev: EphemeralTerminal[]) => EphemeralTerminal[]),
    ) => void;
    onTerminalAttached?: (id: string, termId: string) => void;
    onTerminalScopeChange?: (id: string, conversationId: string | null) => void;
    onTerminalClose?: (id: string) => void;
    navigateUserMessageTrigger?: number;
    /** Incremented when app navigation explicitly selects a conversation. */
    scrollToBottomTrigger?: number;
    onConversationUnarchived?: (conversation: Conversation) => void;
    onDraftCreated?: (conversation: Conversation) => void;
    /** Comment block from the standalone file editor (App-level modal) to
     *  inject into the message input. Fresh object per submit. */
    externalCommentText?: { text: string } | null;
  }>(),
  {
    streamStatus: "connected",
    reconnectNonce: 0,
  },
);

const { t } = useI18n();
const { markdownMode } = useMarkdownMode();
const { conversationViewMode } = useConversationView();
// A view-mode switch refilters coalescedItems, which renumbers every chunk
// and can restructure them wholesale; reset the tail-first floor and
// re-prime for the new model. Synchronous on purpose: renderModel is a
// pull-based computed, so primeTailFirstMount reads the NEW model here, and
// the floor is in place before the next DOM flush — toggling back to "all"
// in a huge conversation must not synchronously mount 20k rows. (Functions
// hoisted; safe to reference at setup time.)
watch(conversationViewMode, () => {
  resetTailFirst();
  primeTailFirstMount();
});
const toolPillsEnabled = useFeatureFlag("tool-pills");
const {
  hasUpdate,
  versionInfo,
  showModal: showVersionModal,
  isLoading: versionLoading,
  openModal: openVersionModal,
  closeModal: closeVersionModal,
} = useVersionChecker();

// ---- core state ----
const messages = ref<Message[]>([]);

// The id of the bottom-most message in the conversation. Provided to
// descendant Message components (through the recursive MessageRenderNode) so
// an error message can show its Retry button only when it is last: once a
// retry (or any new turn) appends a message, the error is no longer at the
// bottom and retrying it would be a server-side no-op.
//
// Slug markers don't count. They render as nothing, carry only the usage of the
// LLM call that named the conversation, and land at an arbitrary point (that
// call races the first turn), so treating one as "last" would hide the Retry
// button on a genuinely retryable error. The server's GetLatestMessage skips
// them for the same reason.
const lastMessageId = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    if (messages.value[i].type !== "slug") return messages.value[i].message_id;
  }
  return null;
});
provide("lastMessageId", lastMessageId);

// When more than one distinct human user (by exe.dev email) has participated in
// a conversation, descendant Message components show each user message's author
// email. Empty-string emails are ignored (unauthenticated/direct access), so a
// mix of empty and a single real email still counts as one participant and
// elides the label. Provided to Message.vue through MessageRenderNode.
const showUserEmails = computed(() => {
  perfCount("chat.showUserEmails");
  return hasMultipleUsers(messages.value);
});
provide("showUserEmails", showUserEmails);
const loading = ref(true);
const renderingConversation = ref(false);
const showLoadingProgressUI = ref(false);
const loadingProgress = ref<{
  phase: "cache" | "downloading" | "parsing" | "rendering";
  bytesDownloaded: number;
  bytesTotal?: number;
  messages?: number;
  source?: ConversationLoadSource;
} | null>(null);
const sending = ref(false);
const error = ref<string | null>(null);
const models = ref<
  Array<{
    id: string;
    display_name?: string;
    source?: string;
    ready: boolean;
    max_context_tokens?: number;
    supports_reasoning?: boolean;
    reasoning_levels?: Exclude<ThinkingLevel, "default">[];
  }>
>(window.__SHELLEY_INIT__?.models || []);

// Ready model ids, surfaced to MessageInput for /model argument autocomplete.
const readyModelIds = computed(() => models.value.filter((m) => m.ready).map((m) => m.id));
// Ready models with reasoning capabilities, for the same autocomplete to offer
// only levels the target model accepts.
const readyModels = computed(() => models.value.filter((m) => m.ready));

// Copy for the empty-model-list state. The server tells us WHY the list is
// empty (missing exe.dev reflection/llm integration, or not on exe.dev at
// all) so the advice names the right fix.
const modelSetupHint = computed(() =>
  modelSetupHintKeys(
    window.__SHELLEY_INIT__?.model_setup_hint,
    window.__SHELLEY_INIT__?.is_exe_dev,
  ),
);

// noModelErrorMessage is the terse inline error when a send is blocked for
// want of a model. The remedies live in the warning panel (and its suggest
// links), so repeating them in the status bar would just be noise; inside an
// existing conversation the panel is hidden, so add the one-line note.
function noModelErrorMessage(): string {
  const hint = modelSetupHint.value;
  if (messages.value.length === 0 || !hint.note) return t(hint.title);
  return `${t(hint.title)}. ${t(hint.note)}`;
}

const thinkingLevel = ref<ThinkingLevel>(storedThinkingLevel());
function setThinkingLevel(level: ThinkingLevel) {
  thinkingLevel.value = level;
  localStorage.setItem(THINKING_LEVEL_KEY, level);
}

function thinkingLevelForModel(modelId: string, level: ThinkingLevel): ThinkingLevel {
  const model = models.value.find((candidate) => candidate.id === modelId);
  return normalizeThinkingLevelForModel(level, model);
}

// selectedModel is "" when the server serves no models. Deliberately no
// hardcoded fallback id: inventing one (this used to default to
// "claude-sonnet-4.6") made the composer look usable and turned a clear
// "no models configured" state into a misleading "Unsupported model:
// claude-sonnet-4.6" error from the server. Empty disables sending instead.
const selectedModel = ref<string>(
  pickReadyModel(window.__SHELLEY_INIT__?.models || [], storedSelectedModel()),
);
// applyModel updates the picker's local state only (ref + localStorage).
// Used both by user picks and by server echoes; never talks to the server.
function applyModel(model: string) {
  selectedModel.value = model;
  localStorage.setItem(SELECTED_MODEL_KEY, model);
}
// In-flight picker PUT tracking. While a PUT for a draft is outstanding,
// the conversation-model watch ignores echoes FOR THAT DRAFT: they are
// either our own PUT reflecting back or a stale row racing a newer pick,
// and applying them would visibly revert the picker the user just moved.
// Echoes for other conversations (a genuine switch) still apply. Once the
// last PUT settles, echoes flow again and converge on the server's value.
let modelPutsInFlight = 0;
let modelPutDraftId: string | null = null;
// putDraftModel best-effort syncs a picked model onto the server-side
// draft row. A 404 means the draft was promoted concurrently (the model
// then travels with the promoting chat POST); other failures fall back to
// the same promote-time sync.
function putDraftModel(draftId: string, model: string) {
  modelPutsInFlight++;
  modelPutDraftId = draftId;
  api
    .updateDraft(draftId, { model })
    .then((conv) => {
      // The PUT bumped the row's updated_at — the arbiter the draft-text
      // cache reconciles against. Re-base like saveDraft does, or a
      // reload inside the autosave debounce would judge the locally
      // cached keystrokes stale and resurrect the server's older text.
      // Only advance: this response may land after a later text
      // autosave's, and regressing the stamp would re-open the window.
      if (draftConvId === draftId && conv.updated_at > draftSyncedAt) {
        draftSyncedAt = conv.updated_at;
      }
      const cur = loadCachedDraft(draftId);
      if (cur && conv.updated_at > cur.basedOn) {
        saveCachedDraft(draftId, cur.value, conv.updated_at);
      }
    })
    .catch(() => {})
    .finally(() => {
      modelPutsInFlight--;
      if (modelPutsInFlight === 0) modelPutDraftId = null;
    });
}
// Changing the model or reasoning level of a conversation that is already under
// way. Both are server state at this point: they are baked into the agent loop
// at build time, and conversation_options are locked once a conversation is
// promoted (see the send path's `promoting` guard) — so a purely local change
// would silently do nothing. /model already does the whole job for both:
// validates the argument, rebuilds the loop, records a modelchange marker in the
// log, and broadcasts the updated conversation, which the currentConversation
// watch applies. So route through it rather than duplicating any of that, and
// don't apply locally first: a rejected switch would visibly snap back.
async function sendModelCommand(arg: string) {
  const id = props.conversationId;
  if (!id) return;
  try {
    await api.sendMessage(id, { message: `/model ${arg}`, model: selectedModel.value });
  } catch (err) {
    console.error("Failed to run /model:", err);
    error.value = err instanceof Error ? err.message : "Failed to change model settings";
  }
}

function switchConversationModel(model: string) {
  if (model === selectedModel.value) return;
  const rounded = thinkingLevelForModel(model, thinkingLevel.value);
  const arg =
    rounded !== "default" && rounded !== thinkingLevel.value ? `${model} ${rounded}` : model;
  return sendModelCommand(arg);
}

// Reasoning pills in the status readout's picker. Same policy as the model
// above: don't touch local state, let the server's echo drive the pill, so a
// rejected level doesn't leave the UI (and the stored default) claiming a
// setting the conversation doesn't have.
//
// The "auto" sentinel is the exception. It means "defer to the model's own
// default", which has no /model spelling ("default" there selects the default
// MODEL), so it can only be applied locally. It's only offered when the model's
// concrete default is unknown, in which case there's no level to send anyway.
function switchConversationThinkingLevel(level: ThinkingLevel) {
  // The pills are radios and re-emit on a click on the current one; without this
  // guard that rebuilds the agent loop and appends a marker for a no-op.
  if (level === thinkingLevel.value) return;
  if (level === "default") {
    setThinkingLevel(level);
    return;
  }
  void sendModelCommand(level);
}

// Model pick from the composer's picker (new/draft conversations), where the
// model is still purely client state until the first send.
//
// setSelectedModel is the USER-pick path. Server-driven updates (conversation
// switch, /model echo) go through applyModel instead — that split, not a
// value-equality guard, is what keeps echoes from looping back into PUTs: an
// equality check against the (stale until the echo lands) conversation row
// would drop a legitimate re-pick of the original model made while a previous
// pick's PUT was still in flight.
function setSelectedModel(model: string) {
  const rounded = thinkingLevelForModel(model, thinkingLevel.value);
  if (rounded !== thinkingLevel.value) setThinkingLevel(rounded);
  applyModel(model);
  // Keep the server-side draft row in sync with the picker. Without this,
  // the draft keeps the model it was created with until the promoting chat
  // POST overrides it — so a client that promotes without an explicit
  // `model` (push reply, crashed client's retry) or another device
  // reopening the draft sees the stale model.
  const draftId =
    props.currentConversation?.is_draft && props.conversationId
      ? props.conversationId
      : lazyDraftId.value;
  if (draftId) putDraftModel(draftId, model);
}

const selectedCwd = ref<string>("");
const cwdInitialized = ref(false);
function setSelectedCwd(cwd: string) {
  selectedCwd.value = cwd;
  localStorage.setItem("shelley_selected_cwd", cwd);
}

const cwdError = ref<string | null>(null);
const showDirectoryPicker = ref(false);

// Directory pick from the picker modal. How it applies depends on whether the
// conversation exists yet — the same split as the model picker: a draft's cwd is
// client state until the first send, while an existing conversation's is server
// state (the agent's tools run there), so it has to change through the server so
// the live toolset moves with it and the agent is told where it now is.
async function applyPickedCwd(path: string) {
  cwdError.value = null;
  const id = props.conversationId;
  if (!id || props.currentConversation?.is_draft) {
    setSelectedCwd(path);
    // Drafts keep their cwd in the row as well as locally, so a reload or
    // another device sees the pick (and so re-opening this picker starts from
    // it — it reads currentConversation?.cwd first). 404 once promoted, which
    // is a no-op: the cwd travels with the send by then. Mirrors
    // setConversationCwd in App.vue, the command-palette path to the same
    // change.
    if (id) {
      api.updateDraft(id, { cwd: path }).catch((err) => {
        console.debug("Could not persist draft cwd (likely already promoted):", err);
      });
    }
    return;
  }
  try {
    await api.setConversationCwd(id, path);
    // Deliberately no local write: the server broadcasts the updated
    // conversation and the currentConversation watch applies it, so a rejected
    // change can't leave the readout claiming a directory we are not in.
  } catch (err) {
    console.error("Failed to change directory:", err);
    error.value = err instanceof Error ? err.message : "Failed to change directory";
  }
}
const isMobile = ref(window.innerWidth < 768);
const showDiffViewer = ref(false);
const showGitGraph = ref(false);
const showAgentsMdEditor = ref(false);
const diffViewerInitialCommit = ref<string | undefined>(undefined);
const diffViewerCwd = ref<string | undefined>(undefined);
const diffCommentText = ref("");
// The image being annotated, if any (module state so any image in the message
// tree can open the view without prop drilling).
const imageCommentTarget = useImageCommentTarget();
const agentWorking = ref(false);
const cancelling = ref(false);
const contextWindowSize = ref(0);
const toolProgress = ref<Record<string, ToolProgress>>({});
// Distributed via provide/inject so per-second tool-progress events reach
// only the running tool's component instead of re-rendering every message
// via a changed prop identity (see composables/toolProgress.ts).
provideToolProgress(toolProgress);
const streamingText = ref("");
const streamingThinking = ref("");
const showAdvancedSettings = ref(false);
const advancedSettingsRef = ref<HTMLDivElement | null>(null);
const availableTools = ref<Array<{ name: string; summary: string; default_on: boolean }>>([]);

const showScrollToBottom = ref(false);
// Keyboard shortcut for jumping to the newest message, surfaced in the
// scroll-to-bottom button's tooltip on desktop (mobile has no keyboard).
const isMac = navigator.platform.toUpperCase().includes("MAC");
const scrollToBottomShortcut = isMac ? "\u2318\u2193" : "Ctrl+\u2193";
const scrollToBottomTooltip = computed(() =>
  isMobile.value ? "Scroll to bottom" : `Scroll to bottom (${scrollToBottomShortcut})`,
);
const lastKnownMessageCount = ref<number | null>(null);
const terminalInjectedText = ref<string | null>(null);
const terminalAutoFocusId = ref<string | null>(null);

// ---- refs to DOM ----
const chatRootRef = ref<HTMLDivElement | null>(null);
const messagesContainerRef = ref<HTMLDivElement | null>(null);
const messagesListRef = ref<HTMLDivElement | null>(null);
const bottomSentinelRef = ref<HTMLDivElement | null>(null);

// ---- non-reactive refs (mutable closures) ----
let userScrolled = false;
let highlightTimeout: number | null = null;
let loadingFlag = false;
// undefined = none, null = bottom, number = saved position
let pendingScroll: number | null | undefined = undefined;
let loadingProgressDelay: number | null = null;
let currentConversationId: string | null = props.conversationId;
let conversationWatchInitialized = false;
let lastScrollToBottomTrigger = props.scrollToBottomTrigger ?? 0;
let conversationLoadEpoch = 0;
let catchingUp = false;
// Layout-free "is the viewport at/near the bottom" signal, maintained by the
// bottom sentinel's IntersectionObserver. Persisted (instead of a raw scrollTop)
// so a reload restores to the true bottom even when content-visibility:auto
// chunks report inflated contain-intrinsic-size estimates that make scrollHeight
// unreliable. New conversations start pinned to the bottom.
let atBottom = true;
// Scroll bookkeeping shared by handleScroll and the ResizeObserver, declared
// here (not next to that logic further down) because the immediate
// conversationId watch resets them during setup; a `let` still in its TDZ at
// that point throws and leaves the composer stuck disabled. See the
// ResizeObserver setup for what they mean.
let lastListHeight = 0;
let clampBudget = 0;
let lastContainerHeight = 0;
// The IntersectionObserver's raw view of the bottom sentinel. Unlike atBottom
// (which handleScroll also flips on inferred scroll-ups) this only changes
// when the sentinel actually enters/leaves the viewport, so the container
// ResizeObserver can use it to recognize clamps that left us at the bottom.
let sentinelAtBottom = true;
// When handleScroll last inferred a user scroll-up from a scrollTop drop, and
// by how much. A container-growth clamp normally reaches the ResizeObserver
// before its scroll event, but a forced reflow (anything reading layout right
// after the DOM change) flushes the clamp early so the scroll event lands
// first; the ResizeObserver uses these to retroactively undo that misread.
let inferredScrollUpAt = -Infinity;
let inferredScrollUpDelta = 0;
// Last upward wheel / touch gesture; a scroll-up near a real gesture must
// never be undone as a clamp misread.
let lastScrollGestureAt = -Infinity;
// Last scroll event on the messages container, whatever its cause. Used by
// the bottom sentinel's IntersectionObserver to tell "the user scrolled away"
// from "the list grew above the viewport": growth (a tail-first sweep mount
// laying out near the viewport, an image decoding) moves the sentinel out of
// the near-bottom zone WITHOUT any scroll event, and must not disarm follow.
let lastScrollEventAt = -Infinity;
let hiddenAt: number | null = null;
let lastGeneration: { id: string | null; gen: number } | null = null;

// ---- tail-first chunk mounting ----
//
// Mounting a 20k-message transcript takes seconds of main-thread time even
// though content-visibility:auto keeps off-screen chunks unlaid-out — the
// cost is creating the Vue subtrees and DOM nodes themselves. So on load,
// only the last INITIAL_TAIL_CHUNKS chunks mount (>= tailFloor); everything
// above renders as a fixed-height PendingChunk placeholder, and a background
// sweep mounts the rest TOP-DOWN (sweptTop watermark descends the history
// oldest-first) during idle time.
//
// Top-down matters: the placeholder height matches .messages-chunk's
// contain-intrinsic-size estimate, and a chunk mounted far above the
// viewport books at that same estimate until first laid out — so each sweep
// swap is height-neutral and needs no scroll compensation. Mounting
// floor-adjacent chunks first would put them inside the browser's
// content-visibility proximity margin, where they lay out (and deflate)
// immediately under the reader. With the 10-chunk tail the floor boundary
// sits tens of thousands of pixels above the scrollport, so even the final
// sweep steps stay estimate-booked. Deflation only happens when the user
// scrolls up to content — the pre-existing clamp machinery's job.
//
// Placeholders scrolled near the viewport reveal themselves early (see
// PendingChunk), and TOC/fragment jumps reveal their target's chunk through
// revealChunkTarget, so nothing the user can reach stays a placeholder.
// Reveals are tracked by chunk KEY (stable under mid-history renumbering);
// the floor is positional but self-corrects: floorKey is re-located on every
// render-model rebuild and the floor/watermark shift with it.
//
// Known best-effort windows (the sweep drains a 21k-message history in
// ~3s Chromium / ~6s Safari, so these are brief): find-in-page and
// select-all miss unmounted history; printing mid-sweep prints placeholder
// bands (beforeprint triggers reveals, but the mount is async).
const INITIAL_TAIL_CHUNKS = 10; // ~500 rows at RENDER_CHUNK_SIZE=50
const SWEEP_CHUNKS_PER_STEP = 4;
const tailFloor = ref(0);
const sweptTop = ref(0);
const revealedChunks = reactive(new Set<string>());
let tailSweepToken = 0;
let tailPrimedFor: string | null = null;
let tailFloorKey: string | null = null;
let pendingIdle: (() => void) | null = null;

// Test knob: Playwright can't practically seed 500+ rows, so it shrinks the
// tail/chunks and/or freezes the sweep via localStorage (same tab-scoped
// pattern as ff:* overrides). Values are clamped to sane minimums so a
// malformed override can't hang chunking (chunkSize 0) or hide the tail.
function tailFirstTestOverrides(): { tailChunks?: number; sweep?: boolean; chunkSize?: number } {
  try {
    const raw = localStorage.getItem("shelley.tailFirstTest");
    if (!raw) return {};
    const parsed = JSON.parse(raw) as { tailChunks?: unknown; sweep?: unknown; chunkSize?: unknown };
    const posInt = (v: unknown): number | undefined =>
      typeof v === "number" && Number.isInteger(v) && v >= 1 ? v : undefined;
    return {
      tailChunks: posInt(parsed.tailChunks),
      sweep: typeof parsed.sweep === "boolean" ? parsed.sweep : undefined,
      chunkSize: posInt(parsed.chunkSize),
    };
  } catch {
    return {};
  }
}

// Chunk keys in globalIndex order. O(total chunks) but only evaluated when
// the floor machinery needs it (priming, reveals, and the floor-relocation
// watcher below).
const chunkKeysByIndex = computed<string[]>(() => {
  const keys: string[] = [];
  for (const block of renderModel.value) {
    for (const chunk of block.chunks) keys.push(chunk.key);
  }
  return keys;
});

function cancelPendingIdle(): void {
  pendingIdle?.();
  pendingIdle = null;
}

function resetTailFirst(): void {
  tailSweepToken++;
  cancelPendingIdle();
  tailFloor.value = 0;
  sweptTop.value = 0;
  revealedChunks.clear();
  tailPrimedFor = null;
  tailFloorKey = null;
}

/** Install the mount floor for the current render model and start the sweep.
 * Shared by primeTailFirstMount and the floor-relocation watcher's
 * lost-anchor fallback. Keeps revealedChunks: keys are stable, so regions
 * the user already visited stay mounted across a re-establish. */
function establishTailFloor(): void {
  const overrides = tailFirstTestOverrides();
  const tailChunks = overrides.tailChunks ?? INITIAL_TAIL_CHUNKS;
  const keys = chunkKeysByIndex.value;
  const floor = Math.max(0, keys.length - tailChunks);
  if (floor <= 0) return;
  tailPrimedFor = currentConversationId;
  tailFloor.value = floor;
  tailFloorKey = keys[floor];
  sweptTop.value = 0;
  if (overrides.sweep !== false) startTailSweep();
}

/** Set the mount floor for the just-loaded conversation. Idempotent per
 * conversation — but only once a floor was actually established: the
 * reveal→incremental-tail→finish path calls this twice, and when the first
 * call saw a small partial cache (floor 0) the second call must still be
 * able to install a floor for the full network snapshot. */
function primeTailFirstMount(): void {
  if (tailPrimedFor === currentConversationId) return;
  establishTailFloor();
}

// The floor and watermark are positional; chunk keys are not. A watcher
// registered below renderModel's definition (TDZ) re-locates the floor
// anchor on every render-model rebuild — see relocateTailFloor.
function relocateTailFloor(): void {
  if (tailFloor.value <= 0 || tailPrimedFor !== currentConversationId) return;
  const keys = chunkKeysByIndex.value;
  const idx = tailFloorKey ? keys.indexOf(tailFloorKey) : -1;
  if (idx === -1) {
    tailSweepToken++;
    cancelPendingIdle();
    establishTailFloor();
    if (tailFloor.value <= 0) resetTailFirst();
    return;
  }
  const delta = idx - tailFloor.value;
  if (delta !== 0) {
    tailFloor.value = idx;
    // Shifting the watermark with the floor errs toward mounting extra
    // chunks near it (benign) rather than unmounting swept history.
    sweptTop.value = Math.max(0, Math.min(sweptTop.value + delta, idx));
  }
}

type IdleDeadlineLike = { timeRemaining(): number };
/** Schedule cb for idle time; returns a cancel function. The setTimeout
 * fallback (Safari has no requestIdleCallback) uses a frame-ish delay: with
 * ~150 steps for a 21k-message history, 50ms/step stretched the sweep to
 * 12s of find-in-page-missing-history, while 16ms keeps it ~3s and the
 * nextTick between steps still yields to patches and input. */
function scheduleIdle(cb: (deadline?: IdleDeadlineLike) => void): () => void {
  if (typeof requestIdleCallback === "function") {
    const id = requestIdleCallback(cb, { timeout: 500 });
    return () => cancelIdleCallback(id);
  }
  const id = window.setTimeout(() => cb(), 16);
  return () => clearTimeout(id);
}

function startTailSweep(): void {
  const token = ++tailSweepToken;
  cancelPendingIdle();
  const step = (deadline?: IdleDeadlineLike) => {
    pendingIdle = null;
    if (token !== tailSweepToken) return;
    if (tailFloor.value <= 0 || sweptTop.value >= tailFloor.value) return;
    // Pause while the user is actively scrolling: a mount landing mid-fling
    // competes with scroll frames, and if the fling is heading into the
    // swept region the near-viewport reveal path covers it anyway.
    if (performance.now() - lastScrollGestureAt < 250) {
      pendingIdle = scheduleIdle(step);
      return;
    }
    // Under deadline pressure (timeout-fired callback, busy frame) mount one
    // chunk instead of a full batch so the sweep never contributes a long
    // task to a streaming or scrolling frame.
    const n = deadline && deadline.timeRemaining() < 8 ? 1 : SWEEP_CHUNKS_PER_STEP;
    sweptTop.value = Math.min(tailFloor.value, sweptTop.value + n);
    // Let Vue patch the newly mounted chunks before booking the next step, so
    // one idle callback never batches multiple steps into a single long task.
    void nextTick(() => {
      if (token !== tailSweepToken) return;
      pendingIdle = scheduleIdle(step);
    });
  };
  pendingIdle = scheduleIdle(step);
}

function revealChunk(globalIndex: number): void {
  if (globalIndex < 0) return;
  const keys = chunkKeysByIndex.value;
  // Look-behind: the near-viewport observer only fires on scrollport entry
  // (clipped by the scroll container), so pre-reveal a couple of chunks
  // above to keep upward scrolling ahead of the mount cost.
  for (let i = Math.max(0, globalIndex - 2); i <= globalIndex; i++) {
    const key = keys[i];
    if (key) revealedChunks.add(key);
  }
}

// message_id / tool_use_id → chunk globalIndex, for TOC and #fragment jumps
// into unmounted history. Built lazily (only on a jump) and cached per
// render-model identity. During streaming the model identity changes every
// delta, so consecutive jumps may each pay the O(nodes) walk — acceptable
// for an explicitly user-initiated action.
interface ChunkTargetIndex {
  byMessage: Map<string, number>;
  byTool: Map<string, number>;
  // Normalized 8-char fragment prefix → index (the TOC's #m-…/#t-… scheme),
  // precomputed so fragment resolution (which retries on a timer) is O(1).
  byMessageFrag: Map<string, number>;
  byToolFrag: Map<string, number>;
}
const chunkTargetIndexCache = new WeakMap<GenerationBlock[], ChunkTargetIndex>();

function fragPrefix(id: string): string {
  return id.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8);
}

function collectChunkTargets(node: RenderNode, index: number, into: ChunkTargetIndex): void {
  switch (node.kind) {
    case "message":
      if (node.item.message) {
        into.byMessage.set(node.item.message.message_id, index);
        into.byMessageFrag.set(fragPrefix(node.item.message.message_id), index);
      }
      break;
    case "tool-pills":
      for (const item of node.items) {
        if (item.toolUseId) {
          into.byTool.set(item.toolUseId, index);
          into.byToolFrag.set(fragPrefix(item.toolUseId), index);
        }
      }
      break;
    case "tool-call":
      if (node.item.toolUseId) {
        into.byTool.set(node.item.toolUseId, index);
        into.byToolFrag.set(fragPrefix(node.item.toolUseId), index);
      }
      break;
    case "carried-band":
      for (const child of node.children) collectChunkTargets(child, index, into);
      break;
  }
}

function chunkTargetIndex(): ChunkTargetIndex {
  const model = renderModel.value;
  let idx = chunkTargetIndexCache.get(model);
  if (!idx) {
    idx = {
      byMessage: new Map(),
      byTool: new Map(),
      byMessageFrag: new Map(),
      byToolFrag: new Map(),
    };
    for (const block of model) {
      for (const chunk of block.chunks) {
        for (const node of chunk.nodes) collectChunkTargets(node, chunk.globalIndex, idx);
      }
    }
    chunkTargetIndexCache.set(model, idx);
  }
  return idx;
}

/** Ensure the chunk containing the target is mounted. Returns true when the
 * target was found in not-fully-mounted history; callers should re-query the
 * DOM after the next tick. Fragments use the TOC's normalized 8-char prefix
 * scheme. */
function revealChunkTarget(target: {
  messageId?: string;
  toolUseId?: string;
  fragment?: string;
}): boolean {
  if (tailFloor.value <= 0 || sweptTop.value >= tailFloor.value) return false;
  const idx = chunkTargetIndex();
  let found: number | undefined;
  if (target.messageId !== undefined) found = idx.byMessage.get(target.messageId);
  else if (target.toolUseId !== undefined) found = idx.byTool.get(target.toolUseId);
  else if (target.fragment) {
    if (target.fragment.startsWith("m-")) found = idx.byMessageFrag.get(target.fragment.slice(2));
    else if (target.fragment.startsWith("t-")) found = idx.byToolFrag.get(target.fragment.slice(2));
  }
  if (found === undefined) return false;
  // A jump usually precedes reading around the target: reveal one chunk of
  // look-ahead below it too (revealChunk covers the look-behind above).
  revealChunk(found);
  const keys = chunkKeysByIndex.value;
  const below = keys[found + 1];
  if (below) revealedChunks.add(below);
  return true;
}

provide(chunkMountKey, {
  floor: tailFloor,
  sweptTop,
  revealed: revealedChunks,
  reveal: revealChunk,
  revealTarget: revealChunkTarget,
});

const links = window.__SHELLEY_INIT__?.links || [];
const hostname = window.__SHELLEY_INIT__?.hostname || "localhost";

// ---- tool overrides (persisted) ----
const TOOL_OVERRIDES_KEY = "shelley.toolOverrides";
const toolOverrides = ref<Record<string, "on" | "off">>(
  (() => {
    try {
      const raw = localStorage.getItem(TOOL_OVERRIDES_KEY);
      if (!raw) return {};
      const parsed = JSON.parse(raw);
      if (parsed && typeof parsed === "object") {
        const clean: Record<string, "on" | "off"> = {};
        for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
          if (v === "on" || v === "off") clean[k] = v;
        }
        return clean;
      }
    } catch {
      /* ignore */
    }
    return {};
  })(),
);
function setToolOverride(name: string, value: "default" | "on" | "off") {
  const next = { ...toolOverrides.value };
  if (value === "default") delete next[name];
  else next[name] = value;
  toolOverrides.value = next;
  try {
    if (Object.keys(next).length === 0) localStorage.removeItem(TOOL_OVERRIDES_KEY);
    else localStorage.setItem(TOOL_OVERRIDES_KEY, JSON.stringify(next));
  } catch {
    /* ignore */
  }
}
function resetToolOverrides() {
  toolOverrides.value = {};
  try {
    localStorage.removeItem(TOOL_OVERRIDES_KEY);
  } catch {
    /* ignore */
  }
}
const toolOverrideCount = computed(() => Object.keys(toolOverrides.value).length);

const toolOverrideList = computed(() => availableTools.value);

// ---- per-conversation localStorage helpers ----
function msgCountKey(): string | null {
  return props.conversationId ? `shelley_msg_count_${props.conversationId}` : null;
}
function saveMsgCount(count: number) {
  const key = msgCountKey();
  if (!key) return;
  try {
    localStorage.setItem(key, String(count));
  } catch {
    /* ignore */
  }
}
function loadMsgCount(): number | null {
  const key = msgCountKey();
  if (!key) return null;
  try {
    const v = localStorage.getItem(key);
    if (v == null) return null;
    const n = Number(v);
    return Number.isFinite(n) ? n : null;
  } catch {
    return null;
  }
}
function scrollKey(): string | null {
  return props.conversationId ? `shelley_scroll_${props.conversationId}` : null;
}
function saveScroll(scrollTop: number) {
  const key = scrollKey();
  if (!key) return;
  // When we're at the bottom, persist a sentinel rather than the numeric
  // offset. content-visibility:auto chunks report estimated heights for
  // off-screen content, so a saved offset can no longer sit at the bottom
  // after a reload (scrollHeight is inflated) — which silently disarmed
  // auto-follow. Restoring the sentinel re-pins to the real bottom instead.
  localStorage.setItem(key, atBottom ? "bottom" : String(scrollTop));
}
function loadScroll(): number | null {
  const key = scrollKey();
  if (!key) return null;
  const v = localStorage.getItem(key);
  // null (no value) and the "bottom" sentinel both mean "restore to bottom".
  if (v == null || v === "bottom") return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}

// ---- derived ----
// Distilling = an in_progress distill status message exists with no later
// terminal (complete/error) one. Status messages are immutable, so a finished
// distillation appends a second terminal message rather than mutating the
// in_progress one.
const isDistilling = computed(() => {
  let inProgress = false;
  for (const m of messages.value) {
    const status = distillStatus(m);
    if (status === "in_progress") {
      inProgress = true;
    } else if (status === "complete" || status === "error") {
      inProgress = false;
    }
  }
  return inProgress;
});

const selectedModelInfo = computed(() => models.value.find((m) => m.id === selectedModel.value));
const maxContextTokens = computed(() => selectedModelInfo.value?.max_context_tokens || 200000);

// Content type constants mirror llm/llm.go.
const LLM_TYPE_TEXT = 2;
const LLM_TYPE_TOOL_USE = 5;

// Short excerpt of an agent message for the token cost graph hover readout:
// first text block, or the first tool call when the message is tools-only.
// Cached by message_id: llm_data can be large and messages with usage data
// are complete, so their snippet never changes.
const snippetCache = new Map<string, string>();
function messageSnippet(m: Message): string {
  const cached = snippetCache.get(m.message_id);
  if (cached !== undefined) return cached;
  let snippet = "";
  if (m.llm_data) {
    try {
      const llm = typeof m.llm_data === "string" ? JSON.parse(m.llm_data) : m.llm_data;
      const content: LLMContent[] = llm?.Content || [];
      for (const c of content) {
        if (c.Type === LLM_TYPE_TEXT && c.Text?.trim()) {
          snippet = c.Text.trim().slice(0, 100);
          break;
        }
      }
      if (!snippet) {
        for (const c of content) {
          if (c.Type === LLM_TYPE_TOOL_USE && c.ToolName) {
            snippet = `→ ${c.ToolName}`;
            break;
          }
        }
      }
    } catch {
      /* ignore malformed llm_data */
    }
  }
  snippetCache.set(m.message_id, snippet);
  return snippet;
}

// Parsed usage_data / other_usage_data, cached by message_id like
// snippetCache/humanUserCache above. The usage walk below runs on every stream
// update — `messages` is replaced wholesale — so re-parsing would be
// O(conversation) JSON.parse per streamed token. All four caches are dropped
// on conversation switch (see the conversationId watch) so they stay bounded
// by one conversation's message count rather than the session's.
//
// Only messages that ALREADY carry the field are cached: a row is written once,
// complete, with its usage (there is no UPDATE ... SET usage_data), so a cached
// parse can't go stale — but caching "field absent" would be a bet on that
// invariant rather than a consequence of it, and would silently ignore usage
// that arrived later for the same message_id. Absent is a cheap early return
// anyway; malformed-but-present is cached so bad JSON is parsed at most once.
const usageParseCache = new Map<string, Usage | null>();
function parseUsage(m: Message): Usage | null {
  if (!m.usage_data) return null;
  const cached = usageParseCache.get(m.message_id);
  if (cached !== undefined) return cached;
  let u: Usage | null = null;
  try {
    u = typeof m.usage_data === "string" ? JSON.parse(m.usage_data) : m.usage_data;
  } catch {
    /* ignore malformed usage */
  }
  usageParseCache.set(m.message_id, u);
  return u;
}

// Shared cache-miss result. Readonly so a caller can't mutate every message's
// "no other usage" answer at once; the cached parses are handed out the same
// way, since callers only ever read them.
const NO_OTHER_USAGE: readonly OtherUsageEntry[] = Object.freeze([]);
const otherUsageParseCache = new Map<string, readonly OtherUsageEntry[]>();
function parseOtherUsage(m: Message): readonly OtherUsageEntry[] {
  if (!m.other_usage_data) return NO_OTHER_USAGE;
  const cached = otherUsageParseCache.get(m.message_id);
  if (cached !== undefined) return cached;
  let entries: readonly OtherUsageEntry[] = NO_OTHER_USAGE;
  try {
    const parsed = JSON.parse(m.other_usage_data);
    if (Array.isArray(parsed)) entries = parsed;
  } catch {
    /* ignore malformed other usage */
  }
  otherUsageParseCache.set(m.message_id, entries);
  return entries;
}

// The usage walk below is only consumed by the context usage popup's cost
// graph, which isn't mounted until the popup is first opened. Until then this
// stays false and the computed returns empty, so a conversation whose cost the
// user never asks about pays nothing on the streaming path. ContextUsageBar
// flips it via onUsageNeeded — on hover/focus as a head start, and on the
// popover's show event as the guarantee — and it stays flipped for the rest of
// the conversation: the popup can be reopened, and a stale graph would be worse
// than the walk. This is what the token-cost-graph feature flag used to gate;
// it is reset per conversation with the memo caches below.
const usageWanted = ref(false);

// Per-LLM-call usage entries (in order) for the token cost graph in the
// context usage popup. Includes every generation: the graph shows cumulative
// conversation cost, not just the live context window. All-zero records
// (e.g. error placeholders) are skipped.
//
// The same single walk also collects "other" (indirect) LLM usage —
// compaction summarization, LLM-backed tools, slug generation, … — from any
// message (any type) carrying other_usage_data, aggregated into per-
// (purpose, model, url) rows. Inclusion semantics are identical to
// usage_data: forked copies carry both fields and both are counted.
const usageData = computed<{ entries: UsageEntry[]; otherRows: OtherUsageRow[] }>(() => {
  if (!usageWanted.value) return { entries: [], otherRows: [] };
  perfCount("chat.usageEntries");
  const out: UsageEntry[] = [];
  const otherEntries: OtherUsageEntry[] = [];
  // A turn starts at the first call, after a human user message, or after an
  // agent message that declared end_of_turn. Tool results also arrive as
  // "user" messages; those don't start turns.
  let nextStartsTurn = true;
  // Timestamp of the message that triggered the pending turn; anchors the
  // first call's duration (created_at only marks call completion).
  let turnStartTs = 0;
  for (const m of messages.value) {
    otherEntries.push(...parseOtherUsage(m));
    if (isHumanUserMessage(m)) {
      nextStartsTurn = true;
      turnStartTs = Date.parse(m.created_at) || 0;
      continue;
    }
    if (m.type !== "agent") continue;
    // end_of_turn doesn't depend on usage; honor it even for agent messages
    // without (or with malformed) usage data. Read it up front, but apply it
    // after this call so the call itself stays in its own turn.
    const endsTurn = !!m.end_of_turn;
    const u = parseUsage(m);
    if (
      u &&
      (u.input_tokens || 0) +
        (u.cache_creation_input_tokens || 0) +
        (u.cache_read_input_tokens || 0) +
        (u.output_tokens || 0) >
        0
    ) {
      out.push({
        ...u,
        snippet: messageSnippet(m),
        generation: m.generation,
        timestamp: Date.parse(m.created_at) || 0,
        startsTurn: nextStartsTurn,
        turnStartTimestamp: nextStartsTurn && turnStartTs ? turnStartTs : undefined,
      });
      nextStartsTurn = false;
    }
    if (endsTurn) {
      nextStartsTurn = true;
      // No anchor until a human message triggers the next turn; anchoring to
      // this agent message would count idle time as active.
      turnStartTs = 0;
    }
  }
  return { entries: out, otherRows: aggregateOtherUsage(otherEntries) };
});
const usageEntries = computed<UsageEntry[]>(() => usageData.value.entries);
const otherUsageRows = computed<OtherUsageRow[]>(() => usageData.value.otherRows);

watch(
  selectedModelInfo,
  (model) => {
    if (!model) return;
    const rounded = thinkingLevelForModel(model.id, thinkingLevel.value);
    if (rounded !== thinkingLevel.value) setThinkingLevel(rounded);
  },
  { immediate: true },
);

const conversationThinkingLevel = computed<string | null>(() => {
  const raw = props.currentConversation?.conversation_options;
  if (!raw) return null;
  try {
    const opts = JSON.parse(raw);
    return opts?.thinking_level || null;
  } catch {
    return null;
  }
});

const displayTitle = computed(() => {
  const title = props.currentConversation?.slug || "Shelley";
  if (props.currentConversation?.archived) return `${title} (archived)`;
  return title;
});

const hasCwd = computed(() => !!(props.currentConversation?.cwd || selectedCwd.value));
const proxyURL = computed(() => `https://${hostname}/`);
// On exe.dev hosts the welcome message advertises the proxy features; off
// exe.dev those don't apply, so show a shorter variant without proxy details.
const isExeDev = window.__SHELLEY_INIT__?.is_exe_dev ?? false;
const welcomeParts = computed(() =>
  t(isExeDev ? "welcomeMessage" : "welcomeMessageLocal").split(
    /(\{hostname\}|\{openSourceLink\}|\{customizeLink\}|\{docsLink\}|\{proxyLink\})/,
  ),
);

const coalescedItems = computed(() => {
  const items = perfWrap("chat.coalesceMessages", () => coalesceMessages(messages.value))();
  if (conversationViewMode.value === "all") return items;
  return items.filter(
    (item) =>
      item.type === "message" &&
      !!item.message &&
      isVisibleConversationMessage(item.message, conversationViewMode.value),
  );
});
const visibleMessages = computed(() =>
  messages.value.filter((message) =>
    isVisibleConversationMessage(message, conversationViewMode.value),
  ),
);

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatMessageCount(count: number): string {
  return messageCountFormatter.format(count);
}

function loadSourceLabel(source: ConversationLoadSource | undefined): string {
  switch (source) {
    case "memory":
      return "Memory cache";
    case "indexeddb":
      return "IndexedDB cache";
    case "incremental":
      return "Cache + server tail";
    case "network":
      return "Network";
    default:
      return "Message cache";
  }
}

const loadingTitle = computed(() => {
  const progress = loadingProgress.value;
  switch (progress?.phase) {
    case "cache":
      return "Checking message cache…";
    case "parsing":
      return "Preparing conversation…";
    case "rendering":
      return progress.messages !== undefined
        ? `Rendering ${formatMessageCount(progress.messages)} messages…`
        : "Rendering conversation…";
    default:
      return "Loading conversation…";
  }
});

const loadingSubtitle = computed(() => {
  const progress = loadingProgress.value;
  const known = progress?.messages ?? lastKnownMessageCount.value;
  const knownText =
    known !== null && known !== undefined ? `${formatMessageCount(known)} messages` : "";
  if (!progress || progress.phase === "cache") {
    return knownText ? `${knownText} last time · checking IndexedDB` : "Checking IndexedDB";
  }
  if (progress.phase === "rendering") {
    const pieces = [loadSourceLabel(progress.source)];
    if (knownText) pieces.push(knownText);
    if (progress.bytesDownloaded > 0) pieces.push(formatBytes(progress.bytesDownloaded));
    return pieces.join(" · ");
  }
  const bytes =
    progress.bytesTotal && progress.bytesTotal > 0
      ? `${formatBytes(progress.bytesDownloaded)} of ${formatBytes(progress.bytesTotal)}`
      : `${formatBytes(progress.bytesDownloaded)} downloaded`;
  return knownText ? `${bytes} · ~${knownText} last time` : bytes;
});

// ---- Render model (porting renderMessages into structured data) ----
const renderModel = computed<GenerationBlock[]>(perfWrap("chat.renderModel", buildRenderModel));
// A mid-history restructure (regenerated turn, compaction) renumbers chunks;
// re-anchor the tail-first floor to its key. See relocateTailFloor.
watch(renderModel, relocateTailFloor);
function buildRenderModel(): GenerationBlock[] {
  const msgs = messages.value;
  if (msgs.length === 0) return [];

  const currentGeneration = props.currentConversation?.current_generation || 1;
  const systemMessagesByGeneration = new Map<number, Message[]>();
  const modelsByGeneration = new Map<number, string>();
  // All distinct models a generation actually ran, in first-seen order, so the
  // ModelBar can show "Mixed" (with the list on hover) once /model switched the
  // model partway through a generation. The first entry is the starting model.
  const modelsUsedByGeneration = new Map<number, string[]>();
  const itemsByGeneration = new Map<number, CoalescedItem[]>();
  const generationSet = new Set<number>();

  msgs.forEach((message) => {
    generationSet.add(message.generation);
    if (
      conversationViewMode.value === "all" &&
      message.type === "system" &&
      !isDistillStatusMessage(message)
    ) {
      const existing = systemMessagesByGeneration.get(message.generation) || [];
      existing.push(message);
      systemMessagesByGeneration.set(message.generation, existing);
    }
    if (message.usage_data) {
      try {
        const usage =
          typeof message.usage_data === "string"
            ? JSON.parse(message.usage_data)
            : message.usage_data;
        if (usage?.model) {
          if (!modelsByGeneration.has(message.generation)) {
            modelsByGeneration.set(message.generation, usage.model);
          }
          const used = modelsUsedByGeneration.get(message.generation) || [];
          if (!used.includes(usage.model)) {
            used.push(usage.model);
            modelsUsedByGeneration.set(message.generation, used);
          }
        }
      } catch {
        /* ignore */
      }
    }
  });

  coalescedItems.value.forEach((item) => {
    generationSet.add(item.generation);
    const existing = itemsByGeneration.get(item.generation) || [];
    existing.push(item);
    itemsByGeneration.set(item.generation, existing);
  });

  generationSet.add(currentGeneration);
  const generations = Array.from(generationSet).sort((a, b) => a - b);

  const tsState: { lastMin: number | null; lastDay: string | null; now: Date } = {
    lastMin: null,
    lastDay: null,
    now: new Date(),
  };

  const itemTime = (item: CoalescedItem): string | null => {
    if (item.type === "tool") return item.toolStartTime || null;
    return item.message?.created_at || null;
  };

  const TOKEN_MARKER_STEP = 10_000;
  const tokenState = { lastBucket: 0 };

  const contextSizeOf = (item: CoalescedItem): number | null => {
    if (item.type !== "message" || item.message?.type !== "agent") return null;
    const raw = item.message?.usage_data;
    if (!raw) return null;
    try {
      const usage = typeof raw === "string" ? JSON.parse(raw) : raw;
      const ctx =
        (usage?.input_tokens ?? 0) +
        (usage?.cache_creation_input_tokens ?? 0) +
        (usage?.cache_read_input_tokens ?? 0) +
        (usage?.output_tokens ?? 0);
      return ctx > 0 ? ctx : null;
    } catch {
      return null;
    }
  };

  const maybeTokenMarker = (item: CoalescedItem, keyPrefix: string): RenderNode | null => {
    const ctx = contextSizeOf(item);
    if (ctx === null) return null;
    const bucket = Math.floor(ctx / TOKEN_MARKER_STEP);
    if (bucket <= tokenState.lastBucket) return null;
    tokenState.lastBucket = bucket;
    const label = `${Math.round(ctx / 1000)}k tokens`;
    return { kind: "token-marker", key: `tok-${keyPrefix}`, label, ctx };
  };

  const maybeTimestamp = (iso: string | null, keyPrefix: string): RenderNode[] => {
    if (!iso) return [];
    const d = new Date(iso);
    if (isNaN(d.getTime())) return [];
    const minBucket = Math.floor(d.getTime() / 60_000);
    const dayKey = d.toDateString();
    if (tsState.lastMin === minBucket && tsState.lastDay === dayKey) return [];
    const showDay = tsState.lastDay !== dayKey;
    tsState.lastMin = minBucket;
    tsState.lastDay = dayKey;
    const out: RenderNode[] = [];
    if (showDay) {
      out.push({
        kind: "day-separator",
        key: `ts-day-${keyPrefix}`,
        label: formatDay(d, tsState.now),
      });
    }
    out.push({ kind: "timestamp", key: `ts-${keyPrefix}`, createdAt: iso });
    return out;
  };

  const blocks: GenerationBlock[] = [];

  generations.forEach((generation, generationIndex) => {
    const items = itemsByGeneration.get(generation) || [];
    tokenState.lastBucket = 0;

    const sectionNodes: RenderNode[] = [];
    let pillBuf: CoalescedItem[] = [];
    let pillSink: RenderNode[] = sectionNodes;

    const flushPills = (keySuffix: string | number) => {
      if (pillBuf.length === 0) return;
      const buf = pillBuf;
      pillBuf = [];
      pillSink.push({
        kind: "tool-pills",
        key: `tool-pills-${generation}-${buf[0].toolUseId || keySuffix}`,
        items: buf,
      });
    };

    const renderItemInto = (sink: RenderNode[], item: CoalescedItem, index: number) => {
      const isPillable =
        toolPillsEnabled.value &&
        item.type === "tool" &&
        !isAutoExpandTool(item.toolName, item.toolInput, item.display);
      if (!isPillable || pillBuf.length === 0) {
        const tsNodes = maybeTimestamp(
          itemTime(item),
          item.message?.message_id || item.toolUseId || `g${generation}-i${index}`,
        );
        if (tsNodes.length > 0) {
          flushPills(index);
          tsNodes.forEach((n) => sink.push(n));
        }
      }
      if (item.type === "message" && item.message) {
        flushPills(index);
        sink.push({ kind: "message", key: item.message.message_id, item });
        const tokNode = maybeTokenMarker(
          item,
          item.message.message_id || `g${generation}-i${index}`,
        );
        if (tokNode) sink.push(tokNode);
      } else if (item.type === "tool") {
        if (isPillable) {
          pillBuf.push(item);
        } else {
          flushPills(index);
          sink.push({
            kind: "tool-call",
            key: item.toolUseId || `tool-${generation}-${item.toolName || "unknown"}-${index}`,
            item,
          });
        }
      }
    };

    let i = 0;
    while (i < items.length) {
      if (items[i].carried) {
        const start = i;
        const band: RenderNode[] = [];
        flushPills(`pre-carried-${start}`);
        pillSink = band;
        const tsSnapshot = { ...tsState };
        let count = 0;
        while (i < items.length && items[i].carried) {
          renderItemInto(band, items[i], i);
          if (items[i].type === "message") count++;
          i++;
        }
        flushPills(`carried-${start}`);
        pillSink = sectionNodes;
        tsState.lastMin = tsSnapshot.lastMin;
        tsState.lastDay = tsSnapshot.lastDay;
        sectionNodes.push({
          kind: "carried-band",
          key: `carried-band-${generation}-${start}`,
          count,
          children: band,
        });
        continue;
      }
      renderItemInto(sectionNodes, items[i], i);
      i++;
    }
    flushPills("end");

    blocks.push({
      generation,
      divider:
        generationIndex > 0
          ? { from: generations[generationIndex - 1], to: generation }
          : undefined,
      sectionClass: `generation-section${generation < currentGeneration ? " generation-section-previous" : ""}`,
      modelBar: {
        key: `model-bar-${generation}`,
        model: modelsByGeneration.get(generation) || props.currentConversation?.model,
        modelsUsed: modelsUsedByGeneration.get(generation) || [],
      },
      systemPrompts: (systemMessagesByGeneration.get(generation) || []).map((m) => ({
        key: `system-prompt-${m.message_id}`,
        message: m,
      })),
      chunks: chunkRenderNodes(sectionNodes),
    });
  });

  // The trailing chunks — where following, streaming, and most reading happen —
  // render live (no content-visibility). A chunk booked at its
  // contain-intrinsic-size estimate can be thousands of pixels off until real
  // layout corrects it (WebKit clamps scrollTop when that happens, see the
  // .messages-chunk comment in styles.css); live chunks have real heights from
  // birth, so the follow path never does math on estimates. Older chunks flip
  // back to content-visibility:auto as they leave the window, keeping their
  // real laid-out size via contain-intrinsic-size:auto's last-remembered-size.
  let remaining = LIVE_TAIL_CHUNKS;
  for (let b = blocks.length - 1; b >= 0 && remaining > 0; b--) {
    const chunks = blocks[b].chunks;
    for (let c = chunks.length - 1; c >= 0 && remaining > 0; c--) {
      chunks[c].live = true;
      remaining--;
    }
  }

  // Conversation-wide chunk numbering for tail-first mounting (chunkMount.ts).
  let globalIndex = 0;
  for (const block of blocks) {
    for (const chunk of block.chunks) chunk.globalIndex = globalIndex++;
  }

  return blocks;
}

// Wrap consecutive render nodes into fixed-size chunks. Each chunk gets
// content-visibility:auto (see .messages-chunk in styles.css) so WebKit can
// skip layout/paint for off-screen chunks without paying per-frame containment
// bookkeeping for one giant always-visible box (which cost 150-200ms per
// composite while typing) or for thousands of per-row boxes (which made every
// frame re-check thousands of viewport-relevancy candidates).
//
// Chunk keys reuse the first node's key: appending messages only ever touches
// the last chunk, so earlier chunk elements (and their laid-out sizes,
// remembered via contain-intrinsic-size:auto) stay stable.
const RENDER_CHUNK_SIZE = 50;
// How many trailing chunks render live (see buildRenderModel), i.e. the last
// LIVE_TAIL_CHUNKS * RENDER_CHUNK_SIZE rows. One would suffice for the follow
// math (only the newborn tail chunk is ever booked at its estimate); a few
// more keep recent history estimate-free for near-bottom scrolling at
// negligible cost (measured on real Safari 26.4, 17k-message conversation:
// see the commit message).
const LIVE_TAIL_CHUNKS = 5;
// Read once per component: the override is set via addInitScript before the
// app boots, and re-reading localStorage on every model rebuild is waste.
const renderChunkSize = tailFirstTestOverrides().chunkSize ?? RENDER_CHUNK_SIZE;
function chunkRenderNodes(nodes: RenderNode[]): RenderChunk[] {
  const chunks: RenderChunk[] = [];
  for (let i = 0; i < nodes.length; i += renderChunkSize) {
    const slice = nodes.slice(i, i + renderChunkSize);
    // globalIndex is assigned conversation-wide in buildRenderModel once all
    // blocks exist.
    chunks.push({ key: `chunk-${slice[0].key}`, nodes: slice, globalIndex: 0 });
  }
  return chunks;
}

const showStreamingPreview = computed(
  () => conversationViewMode.value === "all" && !!streamingText.value && agentWorking.value,
);

const showStreamingThinking = computed(
  () => conversationViewMode.value === "all" && !!streamingThinking.value && agentWorking.value,
);

// ---- scroll ----
const MAX_SCROLL_OFFSET = 0x7fffffff;
function observedBottomScrollTop(listHeight: number, containerHeight: number): number {
  return Math.max(0, listHeight - containerHeight);
}
const BOTTOM_PIN_SCROLL_RELEASE_DELTA = 128;
// The bottom sentinel's IntersectionObserver rootMargin, which the observer
// below is built from. An upward scroll larger than this cannot be one of the
// sub-margin layout clamps that handleScroll must ignore: it necessarily takes
// the sentinel out of the near-bottom zone.
const BOTTOM_SENTINEL_MARGIN_PX = 100;
// How long a clamp bookkeeping entry stays valid. A layout clamp and its
// scroll event land within a rendering update or two of each other; anything
// older is stale and must not affect genuine gestures.
const CLAMP_MISREAD_UNDO_WINDOW_MS = 250;
// Conversation-list selection means "show me the latest". Large transcripts
// keep hydrating syntax highlighting and diff widgets after the first bottom
// paint, so keep following explicit selection until a real user or navigation
// gesture moves away.
let followExplicitSelectionToBottom = false;
let suppressExplicitSelectionClamp = false;
let scrollPointerActive = false;
let bottomPinFrame: number | null = null;
let bottomPinActive = false;

function stopBottomPin() {
  bottomPinActive = false;
  if (bottomPinFrame !== null) cancelAnimationFrame(bottomPinFrame);
  bottomPinFrame = null;
}

function markUserScrolledUp() {
  stopBottomPin();
  followExplicitSelectionToBottom = false;
  suppressExplicitSelectionClamp = false;
  userScrolled = true;
  atBottom = false;
  showScrollToBottom.value = true;
}

function releaseBottomPinForUser() {
  if (!bottomPinActive && !followExplicitSelectionToBottom) return;
  markUserScrolledUp();
}

function handleBottomPinWheel(e: WheelEvent) {
  if (e.deltaY < 0) {
    lastScrollGestureAt = performance.now();
    releaseBottomPinForUser();
  }
}

function handleBottomPinTouch() {
  lastScrollGestureAt = performance.now();
  scrollPointerActive = true;
  stopBottomPin();
}

function handleScrollPointerDown() {
  lastScrollGestureAt = performance.now();
  scrollPointerActive = true;
  stopBottomPin();
}

function handleScrollPointerUp() {
  scrollPointerActive = false;
}

function scrollToBottom() {
  const container = messagesContainerRef.value;
  if (!container) return;
  stopBottomPin();
  userScrolled = false;
  showScrollToBottom.value = false;
  if (followExplicitSelectionToBottom) {
    atBottom = true;
    saveScroll(container.scrollTop);
  }
  let framesRemaining = 120;
  bottomPinActive = true;
  const step = () => {
    const el = messagesContainerRef.value;
    if (!el || userScrolled || framesRemaining-- <= 0) {
      stopBottomPin();
      return;
    }
    const bottomScrollTop =
      lastListHeight > 0 && lastContainerHeight > 0
        ? observedBottomScrollTop(lastListHeight, lastContainerHeight)
        : null;
    el.scrollTop = bottomScrollTop ?? MAX_SCROLL_OFFSET;
    if (bottomScrollTop !== null) lastObservedScrollTop = bottomScrollTop;
    if (!bottomPinActive) return;
    bottomPinFrame = requestAnimationFrame(step);
  };
  step();
}

function requestCurrentConversationBottom() {
  followExplicitSelectionToBottom = true;
  suppressExplicitSelectionClamp = pendingScroll !== undefined;
  pendingScroll = null;
  userScrolled = false;
  atBottom = true;
  showScrollToBottom.value = false;
  nextTick(() => {
    // A same-row selection may interrupt an in-flight numeric restoration.
    // Consume that override here even when no message/load watcher will run.
    if (pendingScroll === null) pendingScroll = undefined;
    scrollToBottom();
  });
}

function syncFromStore(focusedId: string) {
  const rec = messageStore.peek(focusedId);
  if (focusedId !== currentConversationId) return;
  if (!rec) return;
  perfCount("chat.syncFromStore");
  messages.value = rec.messages;
  if (rec.messages.length > 0 || rec.hasFullHistory) {
    lastKnownMessageCount.value = rec.messages.length;
    saveMsgCount(rec.messages.length);
  }
  contextWindowSize.value = rec.contextWindowSize;
  if (props.onConversationUpdate && rec.conversation) {
    props.onConversationUpdate(rec.conversation);
  }
}

function syncTransientFromStore(focusedId: string) {
  const tr = messageStore.getTransient(focusedId);
  if (focusedId !== currentConversationId) return;
  perfCount("chat.syncTransient");
  toolProgress.value = tr.toolProgress;
  streamingText.value = tr.streamingText;
  streamingThinking.value = tr.streamingThinking;
  agentWorking.value = tr.agentWorking;
}

const LARGE_LOAD_STATUS_MESSAGES = 100;
const LOAD_DETAIL_DELAY_MS = 300;
const messageCountFormatter = new Intl.NumberFormat();

interface ConversationLoadTiming {
  startedAt: number;
  hydrateMs: number;
  fetchMs: number;
  renderMs: number;
}

function clearConversationLoading(): void {
  loadingFlag = false;
  loading.value = false;
  renderingConversation.value = false;
  if (loadingProgressDelay) {
    clearTimeout(loadingProgressDelay);
    loadingProgressDelay = null;
  }
  showLoadingProgressUI.value = false;
  loadingProgress.value = null;
}

function beginConversationLoading(focusedId: string): void {
  if (!loading.value) return;
  loadingFlag = true;
  renderingConversation.value = false;
  const knownCount = loadMsgCount();
  lastKnownMessageCount.value = knownCount;
  loadingProgress.value = {
    phase: "cache",
    bytesDownloaded: 0,
    messages: knownCount ?? undefined,
  };
  showLoadingProgressUI.value = (knownCount ?? 0) >= LARGE_LOAD_STATUS_MESSAGES;
  if (!showLoadingProgressUI.value) {
    if (loadingProgressDelay) clearTimeout(loadingProgressDelay);
    loadingProgressDelay = window.setTimeout(() => {
      if (focusedId === currentConversationId && loading.value) {
        showLoadingProgressUI.value = true;
      }
    }, LOAD_DETAIL_DELAY_MS);
  }
}

/** Keep the already-painted status overlay through Vue's DOM patch and one
 * browser paint. The timeout only bounds browsers/background tabs that stop
 * delivering animation frames. */
function waitForConversationPaint(): Promise<void> {
  if (document.visibilityState === "hidden") return Promise.resolve();
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timer);
      resolve();
    };
    const timer = window.setTimeout(finish, 250);
    requestAnimationFrame(() => requestAnimationFrame(finish));
  });
}

type CachedConversationRecord = NonNullable<ReturnType<typeof messageStore.peek>>;

function applyConversationRecord(cached: CachedConversationRecord): void {
  messages.value = cached.messages;
  lastKnownMessageCount.value = cached.messages.length;
  saveMsgCount(cached.messages.length);
  contextWindowSize.value = cached.contextWindowSize;
  if (props.onConversationUpdate && cached.conversation) {
    props.onConversationUpdate(cached.conversation);
  }
}

/** Paint usable cached history before a tail/full refresh. The refresh remains
 * part of the same measured load, but can no longer strand the cache behind an
 * overlay if the network stalls. */
async function revealCachedConversation(
  focusedId: string,
  loadEpoch: number,
  source: ConversationLoadSource,
  timing: ConversationLoadTiming,
  cached: CachedConversationRecord,
): Promise<void> {
  if (!loading.value) return;
  if (focusedId !== currentConversationId || loadEpoch !== conversationLoadEpoch) return;

  applyConversationRecord(cached);
  primeTailFirstMount();
  renderingConversation.value = true;
  if (cached.messages.length >= LARGE_LOAD_STATUS_MESSAGES) {
    showLoadingProgressUI.value = true;
  }
  loadingProgress.value = {
    phase: "rendering",
    bytesDownloaded: 0,
    messages: cached.messages.length,
    source,
  };

  const renderStarted = performance.now();
  await nextTick();
  await waitForConversationPaint();
  if (focusedId !== currentConversationId || loadEpoch !== conversationLoadEpoch) return;
  timing.renderMs += performance.now() - renderStarted;
  clearConversationLoading();
}

async function finishConversationLoad(
  focusedId: string,
  loadEpoch: number,
  source: ConversationLoadSource,
  timing: ConversationLoadTiming,
  cached: CachedConversationRecord,
  bytes: number,
): Promise<void> {
  if (focusedId !== currentConversationId || loadEpoch !== conversationLoadEpoch) return;

  applyConversationRecord(cached);
  primeTailFirstMount();

  const renderStarted = performance.now();
  if (loading.value) {
    renderingConversation.value = true;
    if (cached.messages.length >= LARGE_LOAD_STATUS_MESSAGES) {
      showLoadingProgressUI.value = true;
    }
    loadingProgress.value = {
      phase: "rendering",
      bytesDownloaded: bytes,
      messages: cached.messages.length,
      source,
    };
  }

  await nextTick();
  await waitForConversationPaint();
  if (focusedId !== currentConversationId || loadEpoch !== conversationLoadEpoch) return;

  const renderMs = timing.renderMs + (performance.now() - renderStarted);
  const totalMs = performance.now() - timing.startedAt;
  clearConversationLoading();
  perfRecordConversationLoad({
    conversationId: focusedId,
    source,
    messages: cached.messages.length,
    bytes,
    hydrateMs: timing.hydrateMs,
    fetchMs: timing.fetchMs,
    renderMs,
    totalMs,
  });
}

async function loadMessages(focusedId: string) {
  const loadEpoch = ++conversationLoadEpoch;
  const isCurrent = () =>
    focusedId === currentConversationId && loadEpoch === conversationLoadEpoch;
  const timing: ConversationLoadTiming = {
    startedAt: performance.now(),
    hydrateMs: 0,
    fetchMs: 0,
    renderMs: 0,
  };
  beginConversationLoading(focusedId);

  // Drafts never have server-side messages; skip the network load entirely so
  // a stalled fetch can't strand the loading spinner. The switch watcher
  // already renders the empty composer for drafts, but guard here too in case
  // loadMessages is reached via another path. Match the draft flag to this id
  // so a stale currentConversation can't suppress a real load.
  if (
    props.currentConversation?.is_draft &&
    props.currentConversation.conversation_id === focusedId
  ) {
    clearConversationLoading();
    return;
  }

  const wasHydrated = messageStore.isHydrated(focusedId);
  const hadHotMessages = (messageStore.peek(focusedId)?.messages.length ?? 0) > 0;
  // Hot-memory loads have no asynchronous cache read to give the status a
  // chance to paint before Vue mounts the large message tree. Yield one paint
  // explicitly; otherwise status + thousands of rows land in the same flush.
  if (wasHydrated && loading.value) {
    await nextTick();
    await waitForConversationPaint();
    if (!isCurrent()) return;
  }
  if (!wasHydrated) {
    const hydrateStarted = performance.now();
    await messageStore.hydrate(focusedId);
    timing.hydrateMs = performance.now() - hydrateStarted;
  }
  if (!isCurrent()) return;

  let cached = messageStore.peek(focusedId);
  if (cached) {
    messages.value = cached.messages;
    if (cached.messages.length > 0 || cached.hasFullHistory) {
      lastKnownMessageCount.value = cached.messages.length;
      saveMsgCount(cached.messages.length);
    }
    contextWindowSize.value = cached.contextWindowSize;
    if (props.onConversationUpdate && cached.conversation) {
      props.onConversationUpdate(cached.conversation);
    }
  }

  const cacheIsComplete =
    !!cached &&
    cached.hasFullHistory &&
    (cached.maxSequenceIdKnown <= 0 || cached.maxSequenceId >= cached.maxSequenceIdKnown);

  if (cacheIsComplete && !cached!.needsRefresh) {
    cacheDiag("hit", "load.served_from_cache", {
      conversation_id: focusedId,
      messages: cached!.messages.length,
    });
    await finishConversationLoad(
      focusedId,
      loadEpoch,
      wasHydrated || hadHotMessages ? "memory" : "indexeddb",
      timing,
      cached!,
      0,
    );
    return;
  }

  if (cached && cached.messages.length > 0) {
    await revealCachedConversation(
      focusedId,
      loadEpoch,
      wasHydrated || hadHotMessages ? "memory" : "indexeddb",
      timing,
      cached,
    );
    if (!isCurrent()) return;
  }

  // Incremental path: the cache holds a complete contiguous history, we just
  // don't know whether the server has grown past it (stream reconnect, or the
  // list's known-max is ahead of us). Ask only for the tail — a few hundred
  // bytes instead of re-downloading the whole conversation.
  if (cached && cached.hasFullHistory && cached.messages.length > 0) {
    const fromSeq = cached.maxSequenceId;
    const fetchStarted = performance.now();
    let fetchComplete = false;
    try {
      const tail = await api.getConversationSince(focusedId, fromSeq);
      timing.fetchMs += performance.now() - fetchStarted;
      fetchComplete = true;
      if (!isCurrent()) return;
      messageStore.applyIncrementalTail(focusedId, tail, fromSeq);
      cached = messageStore.peek(focusedId);
      if (!cached) throw new Error("conversation cache vanished after incremental refresh");
      if (props.onConversationUpdate && tail.conversation) {
        props.onConversationUpdate(tail.conversation);
      }
      await finishConversationLoad(focusedId, loadEpoch, "incremental", timing, cached, 0);
      return;
    } catch (err) {
      if (!fetchComplete) timing.fetchMs += performance.now() - fetchStarted;
      cacheDiag(
        "fail",
        "refresh.incremental_failed",
        { conversation_id: focusedId, error: String(err) },
        focusedId,
      );
      if (!isCurrent()) return;
    }
  }

  cacheDiag("info", "load.full_rest", {
    conversation_id: focusedId,
    reason: !cached
      ? "cold"
      : !cached.hasFullHistory
        ? "partial-history"
        : cached.needsRefresh
          ? "reconnect"
          : "server-ahead",
    cached_messages: cached?.messages.length ?? 0,
    cached_max: cached?.maxSequenceId ?? -1,
    known_max: cached?.maxSequenceIdKnown ?? 0,
  });

  try {
    loadingFlag = loading.value;
    error.value = null;
    let downloadedBytes = 0;
    if (loading.value) {
      loadingProgress.value = {
        phase: "downloading",
        bytesDownloaded: 0,
        messages: lastKnownMessageCount.value ?? undefined,
        source: "network",
      };
    }

    const fetchStarted = performance.now();
    const response = await api.getConversationWithProgress(focusedId, (progress) => {
      downloadedBytes = progress.bytesDownloaded;
      if (!isCurrent() || !loading.value) return;
      loadingProgress.value = {
        ...progress,
        messages: lastKnownMessageCount.value ?? undefined,
        source: "network",
      };
    });
    timing.fetchMs += performance.now() - fetchStarted;
    if (!isCurrent()) return;

    // applyFullHistory is non-regressing: a REST snapshot can be STALE relative
    // to the live /api/stream2 feed, so render from the store after its merge.
    messageStore.applyFullHistory(focusedId, response);
    cached = messageStore.peek(focusedId);
    if (!cached) throw new Error("conversation cache missing after full load");
    if (response.context_window_size !== undefined) {
      contextWindowSize.value = response.context_window_size;
    }
    if (props.onConversationUpdate && response.conversation) {
      props.onConversationUpdate(response.conversation);
    }
    await finishConversationLoad(focusedId, loadEpoch, "network", timing, cached, downloadedBytes);
  } catch (err) {
    if (!isCurrent()) return;
    console.error("Failed to load messages:", err);
    error.value = "Failed to load messages";
    clearConversationLoading();
  }
}

// ---- sending / actions ----
const pendingQueuedMessages: {
  conversationId: string;
  text: string;
  controller: AbortController;
}[] = [];

async function queueMessage(message: string) {
  if (!message.trim() || !props.conversationId) return;
  // Same guard as sendMessage: a queued turn runs the LLM later, so an
  // unavailable model just defers the confusing "Unsupported model" error.
  // Throws (not returns) so MessageInput's catch restores the composer text.
  if (!canSendWithModel(selectedModel.value, readyModelIds.value)) {
    const err = new Error(noModelErrorMessage());
    error.value = err.message;
    throw err;
  }
  const pending = {
    conversationId: props.conversationId,
    text: message,
    controller: new AbortController(),
  };
  pendingQueuedMessages.push(pending);
  try {
    await api.sendMessage(
      props.conversationId,
      {
        message: message.trim(),
        model: selectedModel.value,
        queue: true,
      },
      pending.controller.signal,
    );
  } catch (err) {
    if (pending.controller.signal.aborted) return;
    console.error("Failed to queue message:", err);
    throw err;
  } finally {
    const index = pendingQueuedMessages.indexOf(pending);
    if (index !== -1) pendingQueuedMessages.splice(index, 1);
  }
}

async function cancelQueuedMessages() {
  if (!props.conversationId) return;
  try {
    await api.cancelQueuedMessages(props.conversationId);
  } catch (err) {
    console.error("Failed to cancel queued messages:", err);
  }
}

async function cancelQueuedMessage(queuedId: string) {
  if (!props.conversationId) return;
  try {
    await api.cancelQueuedMessage(props.conversationId, queuedId);
  } catch (err) {
    console.error("Failed to cancel queued message:", err);
  }
}

// Ghost pending messages derived from the open conversation's queued_messages
// JSON array (not messages rows). Rendered at the bottom of the conversation.
const queuedGhosts = computed(() => {
  perfCount("chat.queuedGhosts");
  return parseQueuedMessages(props.currentConversation?.queued_messages);
});

// Build the conversation_options bundle from the current composer selection
// (tool overrides, thinking level). "default" omits the
// thinking override so the model's configured/provider default applies. Used
// when promoting an autosaved draft on
// first send — the draft is created (via POST /draft autosave) without
// options, so the selection only reaches the server on the promoting chat
// request.
function buildConversationOptions(): ChatRequest["conversation_options"] | undefined {
  const hasOverrides = Object.keys(toolOverrides.value).length > 0;
  const explicitThinking = thinkingLevel.value === "default" ? undefined : thinkingLevel.value;
  const hasThinking = explicitThinking !== undefined;
  if (!hasOverrides && !hasThinking) return undefined;
  return {
    ...(hasOverrides ? { tool_overrides: { ...toolOverrides.value } } : {}),
    ...(explicitThinking ? { thinking_level: explicitThinking } : {}),
  };
}

async function sendFirstMessage(prompt: string) {
  if (!props.onFirstMessage) return;
  if (!canSendWithModel(selectedModel.value, readyModelIds.value)) {
    throw new Error(noModelErrorMessage());
  }
  if (selectedCwd.value) {
    const validation = await api.validateCwd(selectedCwd.value);
    if (!validation.valid) {
      throw new Error(`Invalid working directory: ${validation.error}`);
    }
  }
  await props.onFirstMessage(
    prompt,
    selectedModel.value,
    selectedCwd.value || undefined,
    Object.keys(toolOverrides.value).length > 0 ? { ...toolOverrides.value } : undefined,
    thinkingLevel.value === "default" ? undefined : thinkingLevel.value,
  );
}

async function forkConversation(messageId?: string) {
  if (!props.conversationId) return;
  try {
    const forked = await api.forkConversation(props.conversationId, { messageId });
    props.onSelectConversation?.(forked);
  } catch (err) {
    console.error("Failed to fork conversation:", err);
    error.value = err instanceof Error ? err.message : "Failed to fork conversation";
  }
}
const forkHandler = (messageId: string) => {
  void forkConversation(messageId);
};

async function sendMessage(message: string) {
  if (!message.trim() || sending.value) return;
  const trimmedMessage = message.trim();

  // Guard every send path on actually having a model. Shelley used to fall
  // back to a hardcoded "claude-sonnet-4.6" here, which the server then
  // rejected with a confusing "Unsupported model" naming an id the user never
  // picked. Fail locally with setup advice instead. Slash commands that don't
  // hit the LLM (/fork, /diff, /archive, ...) are handled below and stay
  // usable; the checks live on the paths that need a model.
  //
  // THROW rather than return: MessageInput clears the textarea optimistically
  // and only restores it in its catch ("Keep the message on error so user can
  // retry"). Returning would look like success and silently discard what the
  // user typed — along with its cached draft — exactly when they can't send.
  if (!canSendWithModel(selectedModel.value, readyModelIds.value) && needsModel(trimmedMessage)) {
    const err = new Error(noModelErrorMessage());
    error.value = err.message;
    throw err;
  }

  if (trimmedMessage === SLASH_COMMANDS.FORK.command) {
    await forkConversation();
    return;
  }
  // /clear starts a fresh generation in the same conversation: it drops the
  // prior context and re-hydrates a vanilla system prompt (like compaction,
  // but without the summary). No-op when there is no conversation yet.
  if (trimmedMessage === SLASH_COMMANDS.CLEAR.command) {
    if (!props.conversationId) return;
    try {
      error.value = null;
      await handleStartNewGeneration();
    } catch (err) {
      console.error("Failed to run /clear:", err);
      error.value = err instanceof Error ? err.message : "Failed to clear conversation";
    }
    return;
  }
  // /model is handled server-side synchronously (it switches the model and
  // returns immediately without starting a turn), so it must NOT flip the
  // agent-working state — otherwise "Agent working..." would stick on. Send it
  // like a normal message but skip the working indicator.
  if (
    (trimmedMessage === "/model" || trimmedMessage.startsWith("/model ")) &&
    props.conversationId
  ) {
    try {
      sending.value = true;
      error.value = null;
      await api.sendMessage(props.conversationId, {
        message: trimmedMessage,
        model: selectedModel.value,
      });
    } catch (err) {
      console.error("Failed to run /model:", err);
      error.value = err instanceof Error ? err.message : "Unknown error";
    } finally {
      sending.value = false;
    }
    return;
  }
  if (trimmedMessage === SLASH_COMMANDS.DIFF.command) {
    showDiffViewer.value = true;
    return;
  }
  if (trimmedMessage === SLASH_COMMANDS.ARCHIVE.command) {
    await archiveFromMenu();
    return;
  }
  if (
    trimmedMessage === SLASH_COMMANDS.RENAME.command ||
    trimmedMessage.startsWith(`${SLASH_COMMANDS.RENAME.command} `)
  ) {
    const requestedSlug = trimmedMessage.slice(SLASH_COMMANDS.RENAME.command.length).trim();
    if (!props.conversationId) {
      const err = new Error("Start a conversation before renaming it.");
      error.value = err.message;
      throw err;
    }
    if (!requestedSlug) {
      const err = new Error("Usage: /rename <new slug>");
      error.value = err.message;
      throw err;
    }
    try {
      sending.value = true;
      error.value = null;
      const conversation = await api.renameConversation(props.conversationId, requestedSlug);
      props.onConversationUpdate?.(conversation);
    } catch (err) {
      console.error("Failed to run /rename:", err);
      error.value = err instanceof Error ? err.message : "Failed to rename conversation";
      throw err;
    } finally {
      sending.value = false;
    }
    return;
  }
  // /compact and its legacy alias /distill both run compaction.
  for (const cmd of [SLASH_COMMANDS.COMPACT.command, SLASH_COMMANDS.DISTILL.command]) {
    if (trimmedMessage === cmd || trimmedMessage.startsWith(`${cmd} `)) {
      const instructions = trimmedMessage.slice(cmd.length).trim();
      await handleDistillCompactNewGeneration(instructions || undefined);
      return;
    }
  }
  if (
    trimmedMessage === SLASH_COMMANDS.NEW.command ||
    trimmedMessage.startsWith(`${SLASH_COMMANDS.NEW.command} `)
  ) {
    const prompt = trimmedMessage.slice(SLASH_COMMANDS.NEW.command.length).trim();
    props.onNewConversation();
    if (!prompt || !props.onFirstMessage) return;
    try {
      sending.value = true;
      error.value = null;
      agentWorking.value = true;
      streamingText.value = "";
      streamingThinking.value = "";
      await sendFirstMessage(prompt);
    } catch (err) {
      console.error("Failed to send /new message:", err);
      error.value = err instanceof Error ? err.message : "Unknown error";
      agentWorking.value = false;
    } finally {
      sending.value = false;
    }
    return;
  }

  if (trimmedMessage.startsWith("!")) {
    const shellCommand = trimmedMessage.slice(1).trim();
    if (shellCommand) {
      const terminal: EphemeralTerminal = {
        id: `term-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
        command: shellCommand,
        cwd:
          props.currentConversation?.cwd ||
          selectedCwd.value ||
          window.__SHELLEY_INIT__?.default_cwd ||
          "/",
        createdAt: new Date(),
        // Owned by the conversation it was launched from. On /new there is no
        // conversation yet, so it starts global.
        conversationId: props.conversationId ?? null,
      };
      props.setEphemeralTerminals((prev) => [...prev, terminal]);
      const firstWord = shellCommand.split(/\s+/)[0];
      const baseName = firstWord.split("/").pop() || firstWord;
      const interactiveShells = ["bash", "sh", "zsh", "fish", "nu", "nushell"];
      if (interactiveShells.includes(baseName)) {
        terminalAutoFocusId.value = terminal.id;
      }
      setTimeout(() => scrollToBottom(), 100);
    }
    return;
  }

  try {
    sending.value = true;
    error.value = null;
    agentWorking.value = true;
    streamingText.value = "";
    streamingThinking.value = "";

    if (!props.conversationId && inflightCreate) {
      try {
        await inflightCreate;
      } catch {
        /* fall through */
      }
    }
    const isDraftConv = !!props.currentConversation?.is_draft;
    const effectiveId = props.conversationId || draftConvId;
    if (!effectiveId && props.onFirstMessage) {
      await sendFirstMessage(message.trim());
    } else if (effectiveId) {
      // When this send promotes an autosaved draft, carry the composer's
      // conversation_options (thinking level, tool overrides).
      // The draft was created without them, and PromoteDraft only preserves
      // what's stored — so without this the selection is lost and reasoning
      // is silently disabled for adaptive models. Follow-up messages on an
      // already-promoted conversation must NOT resend options (they're locked).
      const promoting = isDraftConv || (!props.conversationId && !!draftConvId);
      await api.sendMessage(effectiveId, {
        message: message.trim(),
        model: selectedModel.value,
        cwd:
          (isDraftConv || !props.conversationId) && selectedCwd.value
            ? selectedCwd.value
            : undefined,
        conversation_options: promoting ? buildConversationOptions() : undefined,
      });
    }
  } catch (err) {
    console.error("Failed to send message:", err);
    error.value = err instanceof Error ? err.message : "Unknown error";
    agentWorking.value = false;
    throw err;
  } finally {
    sending.value = false;
  }
}

async function handleCancel() {
  if (!props.conversationId || cancelling.value) return;
  const queued = queuedGhosts.value;
  const pending = pendingQueuedMessages.filter(
    ({ conversationId }) => conversationId === props.conversationId,
  );
  const pendingText = pending.map(({ text }) => text).join("\n");
  const queuedText = [
    ...queued.map(queuedMessageText),
    ...pending.map(({ text }) => text),
  ].join("\n");
  pending.forEach(({ controller }) => controller.abort());
  try {
    cancelling.value = true;
    await api.cancelConversation(props.conversationId);
    if (!draftText && queuedText) seedComposer(queuedText);
    agentWorking.value = false;
  } catch (err) {
    console.error("Failed to cancel conversation:", err);
    error.value = "Failed to cancel. Please try again.";
    if (!draftText && pendingText) seedComposer(pendingText);
  } finally {
    cancelling.value = false;
  }
}

async function handleDistillCompactNewGeneration(instructions?: string) {
  if (!props.conversationId || !props.onDistillNewGeneration) return;
  await props.onDistillNewGeneration(
    props.conversationId,
    selectedModel.value,
    props.currentConversation?.cwd || selectedCwd.value || undefined,
    "compact",
    instructions,
  );
}

async function handleStartNewGeneration() {
  if (!props.conversationId) return;
  const conversation = await api.startNewGeneration(props.conversationId);
  props.onConversationUpdate?.(conversation);
}

async function handleUnarchive() {
  if (!props.conversationId) return;
  try {
    const conversation = await api.unarchiveConversation(props.conversationId);
    props.onConversationUnarchived?.(conversation);
  } catch (err) {
    console.error("Failed to unarchive conversation:", err);
  }
}

function handleOpenDiffViewer(commit: string, cwd?: string) {
  diffViewerInitialCommit.value = commit;
  diffViewerCwd.value = cwd;
  showDiffViewer.value = true;
}

function handleMessageComment(messageId: string, snippet: string) {
  diffCommentText.value = buildMessageQuote(messageId, snippet);
}

function handleInsertFromTerminal(text: string) {
  terminalInjectedText.value = text;
}

// Overflow-menu action handlers. Closing the menu is owned by ChatOverflowMenu
// (the PrimeVue Popover hides itself on click); these just perform the action.
function openExternalLink(url: string) {
  window.open(url, "_blank");
}
// Open an in-app interactive shell terminal, mirroring the command palette's
// "Open Terminal" action (and the openTerminalTrigger watch below). Used by the
// overflow menu's Terminal item and its keyboard shortcut.
function openInAppTerminal() {
  const cwd =
    props.currentConversation?.cwd ||
    selectedCwd.value ||
    window.__SHELLEY_INIT__?.default_cwd ||
    "/";
  const terminal: EphemeralTerminal = {
    id: `term-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
    command: 'exec "${SHELL:-bash}" -i',
    cwd,
    createdAt: new Date(),
    conversationId: props.conversationId ?? null,
  };
  props.setEphemeralTerminals((prev) => [...prev, terminal]);
  terminalAutoFocusId.value = terminal.id;
  setTimeout(() => scrollToBottom(), 100);
}
// Focus an already-open terminal if there is one, otherwise open a new one.
// Used by the Ctrl+` shortcut: a repeat press should bring you back to the
// existing shell rather than spawning another. Setting terminalAutoFocusId lets
// TerminalPanel un-minimize, activate that tab, and focus its xterm.
function focusOrOpenTerminal() {
  const existing = props.ephemeralTerminals;
  if (existing.length > 0) {
    // Reset to null first so re-focusing the terminal that's already in
    // autoFocusId still fires TerminalPanel's watcher (it watches the value,
    // not a trigger). nextTick re-assigns the id to run the focus effect.
    terminalAutoFocusId.value = null;
    const id = existing[existing.length - 1].id;
    nextTick(() => {
      terminalAutoFocusId.value = id;
    });
    return;
  }
  openInAppTerminal();
}
function openExport() {
  window.open(`/export/${props.conversationId}`, "_blank", "noopener");
}
async function archiveFromMenu() {
  if (!props.conversationId || !props.onArchiveConversation) return;
  try {
    await props.onArchiveConversation(props.conversationId);
  } catch (err) {
    console.error("Failed to archive conversation:", err);
  }
}

// Keyboard shortcuts for the overflow-menu actions this component owns. Each
// case invokes the same handler as the corresponding menu click (Terminal is
// the one deliberate exception: the shortcut re-focuses an existing terminal
// rather than always opening a new one), and is gated by the same availability
// the menu uses (see the ChatOverflowMenu props bound in the template) so a
// shortcut never fires for a hidden item. The palette (Cmd/Ctrl+K) and file
// finder (Cmd/Ctrl+Shift+P) are handled in App.vue, which owns those modals.
// See utils/menuShortcuts.ts for the combos.
function handleMenuShortcut(e: KeyboardEvent) {
  // Don't hijack keystrokes while typing in a field.
  const target = e.target as HTMLElement | null;
  if (
    target &&
    (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)
  ) {
    return;
  }
  const action = matchChatInterfaceAction(e);
  if (!action) return;
  switch (action) {
    case "diffs":
      if (!hasCwd.value) return;
      showDiffViewer.value = true;
      break;
    case "gitGraph":
      if (!hasCwd.value) return;
      showGitGraph.value = true;
      break;
    case "terminal":
      focusOrOpenTerminal();
      break;
    case "archive":
      if (
        !props.conversationId ||
        !props.onArchiveConversation ||
        props.currentConversation?.archived
      )
        return;
      void archiveFromMenu();
      break;
    case "export":
      if (!props.conversationId || messages.value.length === 0) return;
      openExport();
      break;
    case "editAgentsMd":
      showAgentsMdEditor.value = true;
      break;
    case "checkVersion":
      openVersionModal();
      break;
  }
  e.preventDefault();
}

function onNewConversationClick(e: MouseEvent) {
  if (handleModifiedNavClick(e, "/new")) return;
  props.onNewConversation();
}

// ---- draft autosave ----
// The composer's live text. Deliberately NOT a ref: every keystroke flows
// through handleDraftChange, and making it reactive would re-render
// ChatInterface (and re-run every directive's `updated` hook, including
// v-tooltip's PrimeVue style reload) per keystroke — which in a huge
// conversation makes typing crawl in Safari. MessageInput owns the live text;
// ChatInterface only pushes into the composer via draftSeed (below) when
// reconciliation decides the text must change.
let draftText = "";
// Programmatic seed for the composer. Wrapped in an object so re-seeding with
// an identical string still triggers MessageInput's watch.
const draftSeed = ref<{ value: string } | null>(null);
function seedComposer(value: string) {
  draftText = value;
  draftSeed.value = { value };
}
const lazyDraftId = ref<string | null>(null);
let draftConvId: string | null = props.conversationId;
let inflightCreate: Promise<string> | null = null;
// The server `updated_at` of the draft row we last successfully synced to.
// Keystrokes stamp the localStorage mirror with this so a reload can tell
// whether the cached text is ahead of what the server acknowledged. "" before
// any server row exists (new-conversation view). See draftCache.
let draftSyncedAt = "";

async function saveDraft(value: string) {
  const id = draftConvId;
  if (id) {
    if (props.currentConversation?.is_draft) {
      const conv = await api.updateDraft(id, { draft: value });
      // The server advanced updated_at to acknowledge this text. Re-base the
      // live cache entry onto it so keystrokes typed while this PUT was
      // outstanding (stamped with the older time) stay ahead of the server.
      // Only advance — a concurrent model PUT (putDraftModel) may have
      // already re-based onto a newer stamp, and regressing would re-open
      // the stale-cache window.
      if (draftConvId === id && conv.updated_at > draftSyncedAt) {
        draftSyncedAt = conv.updated_at;
      }
      const cur = loadCachedDraft(id);
      if (cur && conv.updated_at > cur.basedOn) {
        saveCachedDraft(id, cur.value, conv.updated_at);
      }
    }
    return;
  }
  if (!value.trim()) return;
  if (inflightCreate) {
    await inflightCreate;
    return;
  }
  const p = api
    .createDraft({
      draft: value,
      model: selectedModel.value,
      cwd: selectedCwd.value || undefined,
    })
    .then((conv) => {
      draftConvId = conv.conversation_id;
      draftSyncedAt = conv.updated_at;
      // A model picked while this createDraft was in flight had no draft id
      // to PUT onto and would otherwise be dropped (and the row echo would
      // revert the picker). Reconcile: the picker is authoritative.
      if (conv.model && conv.model !== selectedModel.value) {
        putDraftModel(conv.conversation_id, selectedModel.value);
      }
      // Migrate the `null` new-view cache to the real id so a reload of
      // /c/<id> finds the keystrokes (same session; see lazyDraftId). Re-base
      // onto the new row's updated_at so the migrated text stays ahead.
      const cached = loadCachedDraft(null);
      if (cached) {
        saveCachedDraft(conv.conversation_id, cached.value, conv.updated_at);
        clearCachedDraft(null);
      }
      // App receives the complete draft row before switching conversation ids,
      // so its currentConversation fallback can identify this as a draft and
      // skip message loading without pretending the empty cache is history.
      lazyDraftId.value = conv.conversation_id;
      props.onDraftCreated?.(conv);
      return conv.conversation_id;
    });
  inflightCreate = p;
  try {
    await p;
  } finally {
    if (inflightCreate === p) inflightCreate = null;
  }
}

const draftAutosave = useDraftAutosave(saveDraft);
function handleDraftChange(value: string) {
  perfCount("chat.draftChange");
  draftText = value;
  // Mirror to localStorage SYNCHRONOUSLY before the debounced server autosave:
  // if the tab reloads (or the network silently dropped) before the PUT lands,
  // the keystroke survives, stamped with the last server updated_at we synced
  // to; on next load that stamp is >= the (frozen, on failure) server
  // updated_at, so the cached text wins.
  //
  // Every session's composer is mirrored: the new-conversation view, real
  // drafts, and the next-message composer of an already-sent conversation
  // (client-side only, no server draft). draftSyncedAt is the last server
  // updated_at for draft/new sessions and "" for non-draft ones (nothing to
  // reconcile against; the cache is authoritative).
  saveCachedDraft(draftConvId, value, draftSyncedAt);
  draftAutosave.schedule(value);
}
function handleDraftSendStarted() {
  draftAutosave.cancel();
}
function handleDraftCleared() {
  draftText = "";
  lastSeededValue = "";
  draftAutosave.cancel();
  // Draft is gone (sent or deleted): drop the local mirror so a later visit
  // doesn't resurrect it. Clear both the live id and the `null` new-view slot.
  clearCachedDraft(draftConvId);
  clearCachedDraft(null);
  draftSyncedAt = "";
}

const messageInputInjectedText = computed(
  () => terminalInjectedText.value || diffCommentText.value || undefined,
);
const messageInputInitialRows = computed(() =>
  props.conversationId && !props.currentConversation?.is_draft ? 1 : 3,
);
const canQueue = computed(() => agentWorking.value && !!props.conversationId);
const autoQueue = computed(() => isDistilling.value && !!props.conversationId);

// Status content visibility on mobile (mirrors the renderStatusContent gate)
const showStatusContent = computed(
  () =>
    !isMobile.value ||
    !props.conversationId ||
    props.currentConversation?.is_draft ||
    props.currentConversation?.archived,
);
const statusSlotInline = computed(
  () => !!props.conversationId && !props.currentConversation?.is_draft && isMobile.value,
);

const statusBarClass = computed(
  () =>
    `status-bar${props.currentConversation?.archived ? " status-bar-archived" : ""}${
      !props.conversationId || props.currentConversation?.is_draft ? " status-bar-new" : ""
    }`,
);

// compact callback for the context bar (only when handler available)
const contextBarDistill = computed(() =>
  props.onDistillNewGeneration ? () => handleDistillCompactNewGeneration() : undefined,
);

function setDiffCommentText(text: string) {
  diffCommentText.value = text;
}

// Comments submitted from the App-level file editor modal flow in via prop.
watch(
  () => props.externalCommentText,
  (v) => {
    if (v?.text) diffCommentText.value = v.text;
  },
);

function onTerminalCloseHandler(id: string) {
  if (props.onTerminalClose) {
    props.onTerminalClose(id);
  } else {
    props.setEphemeralTerminals((prev) => prev.filter((tm) => tm.id !== id));
  }
}

function onDiffViewerClose() {
  showDiffViewer.value = false;
  diffViewerInitialCommit.value = undefined;
  diffViewerCwd.value = undefined;
  if (!showGitGraph.value) focusMessageInputIfUnfocused();
}

// Loading bar fill class/style mirror the React conditional.
const loadingBarFillClass = computed(() => {
  const phase = loadingProgress.value?.phase;
  if (phase === "parsing" || phase === "rendering") {
    return "conversation-loading-bar-fill parsing";
  }
  const lp = loadingProgress.value;
  if (phase === "cache" || !lp?.bytesTotal || lp.bytesTotal <= 0) {
    return "conversation-loading-bar-fill indeterminate";
  }
  return "conversation-loading-bar-fill";
});
const loadingBarFillStyle = computed<Record<string, string> | undefined>(() => {
  const lp = loadingProgress.value;
  if (!lp || lp.phase !== "downloading") return undefined;
  if (lp.bytesTotal && lp.bytesTotal > 0) {
    return { width: `${Math.min(100, (lp.bytesDownloaded / lp.bytesTotal) * 100)}%` };
  }
  return undefined;
});

// Props bundle for ChatStatusContent (rendered in the status bar OR the
// mobile message-input slot — mutually exclusive locations).
const statusContentProps = computed(() => {
  perfCount("chat.statusContentProps");
  return {
    currentConversation: props.currentConversation,
    conversationId: props.conversationId,
    streamStatus: props.streamStatus,
    error: error.value,
    agentWorking: agentWorking.value,
    cancelling: cancelling.value,
    selectedCwd: selectedCwd.value,
    contextWindowSize: contextWindowSize.value,
    maxContextTokens: maxContextTokens.value,
    usageEntries: usageEntries.value,
    otherUsageRows: otherUsageRows.value,
    hostname,
    models: models.value,
    selectedModel: selectedModel.value,
    sending: sending.value,
    refreshingModels: refreshingModels.value,
    thinkingLevel: thinkingLevel.value,
    toolOverrides: toolOverrides.value,
    toolOverrideList: toolOverrideList.value,
    toolOverrideCount: toolOverrideCount.value,
    cwdError: cwdError.value,
    onUnarchive: handleUnarchive,
    onClearError: () => (error.value = null),
    onCancel: handleCancel,
    onDistillNewGeneration: contextBarDistill.value,
    onStartNewGeneration: handleStartNewGeneration,
    onSelectModel: setSelectedModel,
    // The status readout's inline picker only renders for a conversation that
    // already exists, where the model and reasoning level are server state (see
    // sendModelCommand); the composer's picker only renders before the first
    // send, where they are not. Separate handlers, not shared ones.
    onSwitchConversationModel: switchConversationModel,
    onSwitchConversationThinkingLevel: switchConversationThinkingLevel,
    onManageModels: () => props.onOpenModelsModal?.(),
    onRefreshModels: handleRefreshModels,
    onThinkingChange: setThinkingLevel,
    onSetToolOverride: setToolOverride,
    onResetToolOverrides: resetToolOverrides,
    onOpenDirectoryPicker: () => (showDirectoryPicker.value = true),
    onUsageNeeded: () => (usageWanted.value = true),
  };
});

// ============ effects / watchers ============

// Sync selected model from the conversation: both when switching to an existing
// one AND when its model changes underneath us (e.g. a mid-conversation /model
// switch, which the server broadcasts on the conversation stream). Without the
// latter, the status/details would keep showing the old model after /model.
// Server-driven: applyModel, not setSelectedModel — echoing a row back into a
// PUT would loop, and while our own picker PUTs are in flight the row is
// stale, so applying it would revert the pick (see modelPutsInFlight).
watch(
  () => [props.currentConversation?.conversation_id, props.currentConversation?.model] as const,
  () => {
    if (!props.currentConversation?.model) return;
    if (modelPutsInFlight > 0 && props.currentConversation.conversation_id === modelPutDraftId) {
      return;
    }
    applyModel(props.currentConversation.model);
  },
);

// Sync the reasoning level from the conversation, the counterpart of the model
// watch above. /model can change the level mid-conversation (from the status
// readout's picker or a typed command), and the conversation's stored options
// are then the truth — without this the pills would keep showing the level the
// composer last chose locally, i.e. the switch the user just made wouldn't
// appear. Only follows a conversation that actually recorded a level: a null
// means "never set", which must not clobber the local default.
watch(
  () => [props.currentConversation?.conversation_id, conversationThinkingLevel.value] as const,
  ([, level]) => {
    if (!level || level === thinkingLevel.value) return;
    if (!THINKING_LEVELS.some((l) => l.value === level)) return;
    setThinkingLevel(level as ThinkingLevel);
  },
  { immediate: true },
);

// Reset cwdInitialized when switching to new conversation.
watch(
  () => props.conversationId,
  (id) => {
    if (id === null) {
      cwdInitialized.value = false;
      showAdvancedSettings.value = false;
    }
  },
);

// Re-read cwd from localStorage when a quick action bumps the sync trigger.
watch(
  () => props.cwdSyncTrigger,
  (trigger) => {
    if (!trigger) return;
    const stored = localStorage.getItem("shelley_selected_cwd");
    if (stored) {
      selectedCwd.value = stored;
      cwdInitialized.value = true;
    }
  },
);

// Initialize CWD: localStorage > mostRecentCwd > server default.
watch(
  [() => props.mostRecentCwd, cwdInitialized],
  () => {
    if (cwdInitialized.value) return;
    const storedCwd = localStorage.getItem("shelley_selected_cwd");
    if (storedCwd) {
      selectedCwd.value = storedCwd;
      cwdInitialized.value = true;
      return;
    }
    if (props.mostRecentCwd) {
      selectedCwd.value = props.mostRecentCwd;
      cwdInitialized.value = true;
      return;
    }
    const defaultCwd = window.__SHELLEY_INIT__?.default_cwd || "";
    if (defaultCwd) {
      selectedCwd.value = defaultCwd;
      cwdInitialized.value = true;
    }
  },
  { immediate: true },
);

// User-triggered model catalog refresh (re-runs LLM integration discovery
// server-side, like Shelley startup does).
const refreshingModels = ref(false);
async function handleRefreshModels() {
  if (refreshingModels.value) return;
  refreshingModels.value = true;
  try {
    const newModels = await api.refreshModels();
    models.value = newModels;
    if (window.__SHELLEY_INIT__) window.__SHELLEY_INIT__.models = newModels;
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to refresh models";
  } finally {
    refreshingModels.value = false;
  }
}

// Refresh models list when triggered or when starting a new conversation.
watch(
  [() => props.modelsRefreshTrigger, () => props.conversationId],
  () => {
    if (props.modelsRefreshTrigger === undefined) return;
    if (props.modelsRefreshTrigger === 0 && props.conversationId !== null) return;
    api
      .getModels()
      .then((newModels) => {
        models.value = newModels;
        if (window.__SHELLEY_INIT__) window.__SHELLEY_INIT__.models = newModels;
      })
      .catch((err) => console.error("Failed to refresh models:", err));
  },
  { immediate: true },
);

// Keep the picker honest about availability. A model id can go stale two
// ways: it was persisted in localStorage while the integrations were healthy,
// or the catalog shrank under us (integration detached, refresh returned
// fewer models). Displaying a stale id invites the user to send it, and the
// server then rejects it with a confusing "Unsupported model" naming a model
// they never chose — the same class of bug as the old hardcoded fallback.
// Clear the selection so the picker reads "No model available" and the send
// guard blocks locally with setup advice.
watch(
  readyModelIds,
  (ready) => {
    if (!selectedModel.value) return;
    if (ready.includes(selectedModel.value)) return;
    // Prefer the server's default (or any ready model) over showing nothing,
    // so a mere catalog reshuffle doesn't strand the composer.
    applyModel(pickReadyModel(models.value));
  },
  { immediate: true },
);

// Fetch tool registry once.
onMounted(() => {
  api
    .getTools()
    .then((r) => (availableTools.value = r.tools))
    .catch(() => {});
});

// Close advanced settings popover on outside click.
function onAdvancedSettingsOutside(e: MouseEvent) {
  if (advancedSettingsRef.value && !advancedSettingsRef.value.contains(e.target as Node)) {
    showAdvancedSettings.value = false;
  }
}
watch(showAdvancedSettings, (open) => {
  document.removeEventListener("mousedown", onAdvancedSettingsOutside);
  if (open) document.addEventListener("mousedown", onAdvancedSettingsOutside);
});

// Generation bump -> reset context window state.
watch(
  [
    () => props.currentConversation?.current_generation,
    () => props.currentConversation?.conversation_id,
  ],
  () => {
    const gen = props.currentConversation?.current_generation;
    const id = props.currentConversation?.conversation_id ?? null;
    if (gen === undefined || id === null) {
      lastGeneration = null;
      return;
    }
    const prev = lastGeneration;
    lastGeneration = { id, gen };
    if (prev && prev.id === id && gen > prev.gen) {
      contextWindowSize.value = 0;
      if (props.conversationId) messageStore.setContextWindowSize(props.conversationId, 0);
    }
  },
  { immediate: true },
);

// Mobile media query.
const mobileMq = window.matchMedia("(max-width: 767px)");
const onMobileChange = (e: MediaQueryListEvent) => (isMobile.value = e.matches);
mobileMq.addEventListener("change", onMobileChange);

// Favicon working indicator.
watch(agentWorking, (working) => {
  if (working) setFaviconStatus("working");
});

// ---- conversation switch: hydrate + subscribe ----
let unsubStore: (() => void) | null = null;
let unsubTransient: (() => void) | null = null;

function teardownSubscriptions() {
  unsubStore?.();
  unsubTransient?.();
  unsubStore = null;
  unsubTransient = null;
}

watch(
  [() => props.conversationId, () => props.scrollToBottomTrigger ?? 0],
  ([id, scrollToBottomTrigger]) => {
    const conversationChanged = !conversationWatchInitialized || id !== currentConversationId;
    const explicitlySelected =
      conversationWatchInitialized && scrollToBottomTrigger !== lastScrollToBottomTrigger;
    conversationWatchInitialized = true;
    lastScrollToBottomTrigger = scrollToBottomTrigger;

    // Selecting the already-open row is still an explicit request for its
    // latest content. Avoid tearing down subscriptions or reloading it.
    if (!conversationChanged) {
      if (explicitlySelected && id) requestCurrentConversationBottom();
      return;
    }

    currentConversationId = id;
    followExplicitSelectionToBottom = explicitlySelected;
    suppressExplicitSelectionClamp = explicitlySelected;
    pendingScroll = id ? (explicitlySelected ? null : loadScroll()) : undefined;
    teardownSubscriptions();
    // An annotation view belongs to the image it was opened from; switching
    // conversations leaves it stranded.
    closeImageComment();
    clearConversationLoading();
    // Reset scroll bookkeeping so state from the previous conversation can't
    // leak across the switch. lastListHeight/clampBudget are especially
    // important: the observer re-attach (watch on the recreated .messages-list)
    // fires an initial ResizeObserver callback, and a stale lastListHeight from
    // a taller previous conversation would inject a spurious clampBudget that
    // could swallow the user's first genuine scroll-up. atBottom defaults to
    // true because a freshly loaded conversation renders pinned to the bottom.
    lastListHeight = 0;
    clampBudget = 0;
    lastContainerHeight = 0;
    sentinelAtBottom = true;
    inferredScrollUpAt = -Infinity;
    inferredScrollUpDelta = 0;
    lastScrollGestureAt = -Infinity;
    lastScrollEventAt = -Infinity;
    atBottom = true;
    // Per-message memo caches are keyed by message_id, which is globally
    // unique, so stale entries are never *wrong* — they'd just accumulate for
    // every conversation visited in the session. Drop them on the switch.
    snippetCache.clear();
    clearConversationViewCache();
    usageParseCache.clear();
    otherUsageParseCache.clear();
    usageWanted.value = false;
    // Tail-first mounting is per conversation: cancel the old sweep and mount
    // everything until the new load primes a fresh floor.
    resetTailFirst();
    if (!id) {
      messages.value = [];
      contextWindowSize.value = 0;
      toolProgress.value = {};
      streamingText.value = "";
      streamingThinking.value = "";
      agentWorking.value = false;
      if (loadingProgressDelay) {
        clearTimeout(loadingProgressDelay);
        loadingProgressDelay = null;
      }
      showLoadingProgressUI.value = false;
      loadingProgress.value = null;
      loadingFlag = false;
      loading.value = false;
      return;
    }
    const focusedId = id;
    messageStore.resetTransient(focusedId);
    const initialTransient = messageStore.getTransient(focusedId);
    agentWorking.value = initialTransient.agentWorking;
    toolProgress.value = {};
    streamingText.value = "";
    streamingThinking.value = "";

    unsubStore = messageStore.subscribe(focusedId, () => syncFromStore(focusedId));
    unsubTransient = messageStore.subscribeTransient(focusedId, () =>
      syncTransientFromStore(focusedId),
    );

    // A draft conversation has no server-side messages by definition: it only
    // carries composer text. Never spin or hit the network for it — that path
    // could strand the spinner forever if the fetch stalls or a switch race
    // trips loadMessages' isCurrent() early-return before `loading` is cleared.
    // Show its (empty) message list + composer immediately.
    if (props.currentConversation?.is_draft) {
      messages.value = messageStore.peek(focusedId)?.messages ?? [];
      loadingFlag = false;
      loading.value = false;
      if (loadingProgressDelay) {
        clearTimeout(loadingProgressDelay);
        loadingProgressDelay = null;
      }
      showLoadingProgressUI.value = false;
      loadingProgress.value = null;
      return;
    }

    // Decide the loading state SYNCHRONOUSLY before kicking off the async
    // load. Otherwise `loading` stays false (its value from the previous
    // conversation) while loadMessages awaits messageStore.hydrate(), so the
    // template renders the "Send a message to start the conversation"
    // empty-state over a conversation that clearly has history — a multi-second
    // flash on cold loads. If we already have messages in memory we can show
    // them immediately (no spinner); otherwise show the spinner until
    // loadMessages resolves, so the empty-state only appears for genuinely
    // empty conversations.
    const inMemory = messageStore.peek(focusedId);
    if (
      inMemory &&
      inMemory.messages.length > 0 &&
      inMemory.messages.length < LARGE_LOAD_STATUS_MESSAGES
    ) {
      loading.value = false;
    } else {
      loading.value = true;
    }
    void loadMessages(focusedId);
  },
  { immediate: true },
);

// A promoted draft keeps the same conversation id, so the switch watcher above
// does not run again. Fetch the complete history once its row changes from
// draft to real conversation; this captures the persisted system prompt as
// well as the streamed user message.
watch(
  () => [props.conversationId, props.currentConversation?.is_draft] as const,
  ([id, isDraft], [previousID, wasDraft]) => {
    if (id && id === previousID && wasDraft === true && isDraft === false) {
      void loadMessages(id);
    }
  },
);

// draftConvId mirror.
watch(
  () => props.conversationId,
  (id) => {
    draftConvId = id;
  },
);

// Genuine navigation ends a lazy-draft session.
watch([() => props.conversationId, lazyDraftId], () => {
  if (lazyDraftId.value && props.conversationId !== lazyDraftId.value) lazyDraftId.value = null;
});

// The session (conversation id) we last seeded the composer for. Guards the
// non-draft branch from re-seeding on echoes (e.g. updated_at bumps from new
// messages), which would wipe in-progress local edits. "" sentinel != any real
// id and != the null new-view session, so the first run always seeds.
let lastSeededSession: string | null | undefined = undefined;
// The exact value we last programmatically wrote into the composer. Lets the
// reconcile watch tell an untouched seeded composer (safe to re-seed on a
// server echo) from one the user has since edited (must not clobber).
let lastSeededValue = "";

// Initialize the composer from the conversation row when switching
// conversations. Drafts and the new-conversation view reconcile the server
// copy with the localStorage mirror via updated_at; non-draft conversations
// have no server-side next-message draft, so their localStorage mirror is
// authoritative (client-side only).
//
// reconcileComposerDraft() is the pure, unit-tested core; it returns null when
// the composer must be left untouched (same session, would clobber live
// keystrokes) — the guard that fixes the Safari "cursor jumps to end / text
// rewritten as I type" bug on slow networks (out-of-order autosave echoes).
watch(
  [
    () => props.conversationId,
    () => props.currentConversation?.is_draft,
    () => props.currentConversation?.draft,
    () => props.currentConversation?.updated_at,
    lazyDraftId,
  ],
  () => {
    perfCount("chat.draftReconcileWatch");
    const result = reconcileComposerDraft({
      conversationId: props.conversationId ?? null,
      lazyDraftId: lazyDraftId.value,
      isDraft: !!props.currentConversation?.is_draft,
      serverDraft: props.currentConversation?.draft || "",
      serverUpdatedAt: props.currentConversation?.updated_at || "",
      cached: loadCachedDraft(props.conversationId ?? null),
      composerValue: draftText,
      lastSeededSession,
      lastSeededValue,
    });
    if (result === null) return;
    draftSyncedAt = result.draftSyncedAt;
    seedComposer(result.value);
    lastSeededValue = result.value;
    lastSeededSession = result.seededSession;
  },
  { immediate: true },
);

// Reconnect nonce -> re-fetch focused conversation.
watch(
  () => props.reconnectNonce,
  (nonce) => {
    if (nonce === 0) return;
    if (!props.conversationId) return;
    void loadMessages(props.conversationId);
  },
);

// Trigger: open diff viewer.
watch(
  () => props.openDiffViewerTrigger,
  (trigger) => {
    if (trigger && trigger > 0) showDiffViewer.value = true;
  },
);
// Trigger: open git graph.
watch(
  () => props.openGitGraphTrigger,
  (trigger) => {
    if (trigger && trigger > 0) showGitGraph.value = true;
  },
);
// Trigger: open terminal.
watch(
  () => props.openTerminalTrigger,
  (trigger) => {
    if (!trigger || trigger <= 0) return;
    openInAppTerminal();
  },
);

// Navigate to next/previous user message when trigger changes.
watch(
  () => props.navigateUserMessageTrigger,
  (trigger) => {
    if (!trigger || !messagesContainerRef.value) return;
    const container = messagesContainerRef.value;
    const userMessageEls = container.querySelectorAll(".message-user");
    if (userMessageEls.length === 0) return;
    const direction = trigger > 0 ? 1 : -1;
    const containerRect = container.getBoundingClientRect();
    const viewportTop = containerRect.top;
    let closestIdx = -1;
    let closestDist = Infinity;
    userMessageEls.forEach((el, i) => {
      const rect = el.getBoundingClientRect();
      const dist = Math.abs(rect.top - viewportTop);
      if (dist < closestDist) {
        closestDist = dist;
        closestIdx = i;
      }
    });
    let targetIdx = closestIdx + direction;
    if (direction === 1 && closestIdx >= 0) {
      const rect = userMessageEls[closestIdx].getBoundingClientRect();
      if (rect.top > viewportTop + 50) targetIdx = closestIdx;
    }
    targetIdx = Math.max(0, Math.min(targetIdx, userMessageEls.length - 1));
    const targetEl = userMessageEls[targetIdx] as HTMLElement;
    markUserScrolledUp();
    targetEl.scrollIntoView({ behavior: "smooth", block: "start" });
    if (highlightTimeout) {
      clearTimeout(highlightTimeout);
      highlightTimeout = null;
    }
    targetEl.classList.remove("message-highlight");
    void targetEl.offsetWidth;
    targetEl.classList.add("message-highlight");
    const removeHighlight = () => {
      targetEl.classList.remove("message-highlight");
      if (highlightTimeout) {
        clearTimeout(highlightTimeout);
        highlightTimeout = null;
      }
    };
    targetEl.addEventListener("animationend", removeHighlight, { once: true });
    highlightTimeout = window.setTimeout(removeHighlight, 2000);
  },
);

// Auto-scroll after DOM updates (mirrors the useLayoutEffect).
watch(
  [messages, loading, streamingText, streamingThinking],
  () => {
    if (loading.value) return;
    nextTick(() => {
      const wasCatchingUp = catchingUp;
      catchingUp = false;
      const pending = pendingScroll;
      if (pending !== undefined) {
        pendingScroll = undefined;
        if (pending != null) {
          const container = messagesContainerRef.value;
          if (container) {
            container.scrollTop = pending;
            // Only treat a restored position as "user scrolled away" when it's
            // not already near the bottom. Restoring a saved position that sits
            // at the bottom must keep auto-scroll armed and the button hidden,
            // otherwise following conversations silently stops (React parity).
            const nearBottom = container.scrollHeight - pending - container.clientHeight < 100;
            userScrolled = !nearBottom;
            atBottom = nearBottom;
            showScrollToBottom.value = !nearBottom;
          }
        } else {
          // Restoring to the bottom (saved sentinel or a brand-new conversation).
          // Set atBottom eagerly rather than waiting for the IntersectionObserver
          // to fire, so a save triggered during the switch window (e.g.
          // beforeunload/visibilitychange) can't persist a stale non-bottom
          // offset for a conversation that is actually pinned to the bottom.
          atBottom = true;
          scrollToBottom();
        }
        return;
      }
      if (!userScrolled && !wasCatchingUp) scrollToBottom();
    });
  },
  { flush: "post" },
);

// ---- scroll listeners + ResizeObserver ----
let scrollSaveTimer: number | null = null;
let ro: ResizeObserver | null = null;
let bottomObserver: IntersectionObserver | null = null;
let lastObservedScrollTop = 0;
// Last observed heights of the message list and container, read for free from
// the ResizeObserver entries' contentRect (no forced layout). When the list
// shrinks — or the container grows (composer resizing, panels opening) — the
// browser clamps scrollTop down, which is indistinguishable from a user
// scroll-up if you only watch scrollTop. content-visibility:auto makes this
// routine: off-screen chunks swap their estimated height for the real one as
// they lay out, so scrollHeight (and the max scrollTop) keeps changing.
// Misreading those clamps as scroll-ups wrongly disarmed auto-follow and left
// the scroll-to-bottom button stranded (GitHub #245). The ResizeObserver fires
// before the clamp's scroll event, so it hands handleScroll a pixel budget to
// discount; when a forced reflow flushes the clamp first instead, the
// ResizeObserver retroactively undoes the misread (see inferredScrollUpAt).
// (lastListHeight/clampBudget are declared with atBottom near the top of
// setup: the immediate conversationId watch resets them, and a `let` in TDZ
// there would throw during setup and strand the composer disabled.)

function handleScroll() {
  const container = messagesContainerRef.value;
  if (!container) return;
  perfCount("chat.handleScroll");
  lastScrollEventAt = performance.now();
  let upwardDelta = lastObservedScrollTop - container.scrollTop;
  // Discount any scrollTop drop the ResizeObserver already attributed to a
  // list shrink (a layout clamp, not a gesture).
  if (upwardDelta > 0 && clampBudget > 0) {
    const absorbed = Math.min(upwardDelta, clampBudget);
    upwardDelta -= absorbed;
    clampBudget -= absorbed;
  }
  const switchingConversation = pendingScroll !== undefined;
  const guardedLayoutShift =
    upwardDelta > 0 && suppressExplicitSelectionClamp && !scrollPointerActive;
  if (switchingConversation || guardedLayoutShift) {
    // Replacing one transcript with another clamps the shared scroll container
    // before the pending destination is applied. Likewise, lazy renderers can
    // change height just after an explicit open. Neither is a user scroll.
    clampBudget = 0;
    lastObservedScrollTop = container.scrollTop;
    if (guardedLayoutShift) scrollToBottom();
    return;
  }
  if (bottomPinActive && upwardDelta >= BOTTOM_PIN_SCROLL_RELEASE_DELTA) {
    stopBottomPin();
  }
  // An upward delta this large, after clamp accounting, is unambiguously a
  // gesture: clampBudget has already absorbed the pixels the ResizeObserver
  // attributed to a list shrink or container growth, so what remains is not
  // explained by layout. (Clamps themselves can far exceed this — a 1200px list
  // shrink is ordinary — which is why the discounting above has to come first.)
  // Acting on it immediately matters because the observer is async — if the list
  // grows in the same task, the ResizeObserver's follow-the-bottom branch runs
  // while sentinelAtBottom is still stale-true and yanks the reader back down
  // (measured: scrollTop 0 -> 1607). The wheel/touch handlers only cover this
  // while the bottom pin is active, so they are not a substitute.
  const definitelyGesture = scrollPointerActive || upwardDelta > BOTTOM_SENTINEL_MARGIN_PX;
  if (!bottomPinActive && upwardDelta > 0 && (!sentinelAtBottom || definitelyGesture)) {
    // Below the gesture threshold, only act when the bottom sentinel has
    // actually left the near-bottom zone. While it still intersects we are
    // following the conversation, and the
    // IntersectionObserver reports only *changes*, so it will not fire again:
    // showing the button here would strand it visible with the container
    // sitting at the bottom, and disarming auto-follow here would silently
    // stop streaming from following. Sub-margin drops are routine —
    // content-visibility:auto chunks swapping estimated for real heights clamp
    // scrollTop by a few pixels. sentinelAtBottom comes from the observer, so
    // testing it costs no forced layout (reading scrollHeight here would lay
    // out every off-screen chunk and stall the main thread).
    //
    // A genuine gesture that outruns the observer is still handled: the wheel
    // and touchstart handlers release the pin synchronously, and the observer
    // shows the button a frame later when the sentinel leaves the margin.
    //
    // Record the inference so the container ResizeObserver can undo it if a
    // growth report arrives that explains this drop as a layout clamp.
    const now = performance.now();
    inferredScrollUpDelta =
      now - inferredScrollUpAt < CLAMP_MISREAD_UNDO_WINDOW_MS
        ? inferredScrollUpDelta + upwardDelta
        : upwardDelta;
    inferredScrollUpAt = now;
    markUserScrolledUp();
  }
  // A layout clamp emits its scroll event synchronously right after the resize
  // that caused it, so any unconsumed budget now is stale; drop it so it can't
  // silently absorb a later genuine scroll-up.
  clampBudget = 0;
  lastObservedScrollTop = container.scrollTop;
  if (scrollSaveTimer) clearTimeout(scrollSaveTimer);
  scrollSaveTimer = window.setTimeout(() => {
    if (!loadingFlag) saveScroll(container.scrollTop);
  }, 100);
}

function setupScrollObservers() {
  const container = messagesContainerRef.value;
  if (!container) return;
  lastObservedScrollTop = container.scrollTop;
  container.addEventListener("scroll", handleScroll);
  container.addEventListener("wheel", handleBottomPinWheel, { passive: true });
  container.addEventListener("touchstart", handleBottomPinTouch, { passive: true });
  container.addEventListener("touchend", handleScrollPointerUp, { passive: true });
  container.addEventListener("touchcancel", handleScrollPointerUp, { passive: true });
  container.addEventListener("pointerdown", handleScrollPointerDown, { passive: true });
  window.addEventListener("pointerup", handleScrollPointerUp, { passive: true });
  window.addEventListener("pointercancel", handleScrollPointerUp, { passive: true });
  bottomObserver = new IntersectionObserver(
    ([entry]) => {
      const nearBottom = entry?.isIntersecting ?? false;
      sentinelAtBottom = nearBottom;
      atBottom = nearBottom;
      showScrollToBottom.value = !nearBottom;
      if (nearBottom) {
        userScrolled = false;
        suppressExplicitSelectionClamp = false;
        stopBottomPin();
        if (!loadingFlag && followExplicitSelectionToBottom) {
          saveScroll(container.scrollTop);
        }
      } else if (!bottomPinActive) {
        if (!userScrolled && followExplicitSelectionToBottom) {
          // An explicitly selected conversation may grow after its first
          // bottom paint as lazy renderers hydrate. Keep the selection at its
          // promised destination unless the user has tried to scroll away.
          scrollToBottom();
        } else if (!userScrolled && performance.now() - lastScrollEventAt > 200) {
          // The sentinel left the near-bottom zone with NO scroll event: the
          // viewport didn't move, the list grew under it (a tail-first sweep
          // mount laying out near the viewport, an image decode). We were
          // following, so keep following. A real gesture always produces
          // scroll events, so it can't be misread here; layout clamps DO
          // fire scroll events, but they land at/near the bottom where the
          // sentinel stays visible and this branch isn't reached.
          scrollToBottom();
        } else {
          // The sentinel left the near-bottom zone, so we are no longer
          // following the conversation. handleScroll cannot always have
          // noticed: its event can race this async observer while
          // sentinelAtBottom is still stale-true.
          userScrolled = true;
        }
      }
    },
    { root: container, rootMargin: `0px 0px ${BOTTOM_SENTINEL_MARGIN_PX}px 0px`, threshold: 0 },
  );
  ro = new ResizeObserver((entries) => {
    perfCount("chat.listResizeObserver");
    // contentRect.height is already computed for the ResizeObserver callback,
    // so reading it forces no extra layout — unlike container.scrollHeight,
    // which would lay out off-screen content-visibility chunks and stall the
    // main thread. A list shrink means the imminent scroll event is a clamp,
    // not a gesture, so record how much handleScroll should discount.
    let listHeight = lastListHeight;
    let containerHeight = lastContainerHeight;
    for (const entry of entries) {
      if (entry.target === container) containerHeight = entry.contentRect.height;
      else listHeight = entry.contentRect.height;
    }
    if (listHeight < lastListHeight) {
      // A list shrink means the imminent — or just-landed — scroll event is a
      // clamp, not a gesture. Same two orderings as container growth below,
      // handled the same way: when this shrink report arrives before the
      // clamp's scroll event, budget the pixels for handleScroll to discount;
      // when the clamp's scroll event landed first and handleScroll already
      // misread it as a scroll-up, retroactively undo the misread.
      //
      // The scroll-event-first ordering is not exotic — it is how WebKit
      // resolves content-visibility estimate deflation, and it is what made
      // Safari silently stop following streaming conversations while Chrome
      // kept scrolling. While following, the follow branch below writes a
      // scrollTop computed from list heights that include off-screen
      // .messages-chunk boxes at their contain-intrinsic-size ESTIMATES.
      // WebKit's estimates drift far from real heights (measured: a 150136px
      // target clamped to 146906 — a 3230px jump), so resolving the write
      // lays the chunks out, deflates the estimates, clamps scrollTop to the
      // real bottom, and fires the scroll event — all before this observer
      // runs. handleScroll saw an upward jump far past the unambiguous-
      // gesture threshold with clampBudget still 0 and disarmed auto-follow;
      // the viewport sat exactly at the real bottom, so the sentinel never
      // left view and the IntersectionObserver never corrected it.
      //
      // The undo's guards: the sentinel must still be at the bottom (a clamp
      // that big necessarily leaves us there; a genuine scroll-up of that
      // size does not), no real wheel/touch gesture nearby, and the misread
      // delta must be explained by this shrink.
      const listShrink = lastListHeight - listHeight;
      const now = performance.now();
      if (
        sentinelAtBottom &&
        now - inferredScrollUpAt < CLAMP_MISREAD_UNDO_WINDOW_MS &&
        now - lastScrollGestureAt > CLAMP_MISREAD_UNDO_WINDOW_MS &&
        inferredScrollUpDelta <= listShrink + 1
      ) {
        userScrolled = false;
        atBottom = true;
        showScrollToBottom.value = false;
        inferredScrollUpAt = -Infinity;
        inferredScrollUpDelta = 0;
      } else {
        clampBudget += listShrink;
      }
    }
    // Container growth clamps scrollTop down too (the viewport got taller, so
    // the max offset got smaller). When we were following the bottom, that
    // clamp is not a gesture. Its scroll event may land before or after this
    // callback depending on when layout flushed, so cover both orders:
    // budget the pixels for a scroll event still to come, or retroactively
    // undo a scroll-up handleScroll already misread.
    const containerGrowth = containerHeight - lastContainerHeight;
    if (lastContainerHeight > 0 && containerGrowth > 0 && sentinelAtBottom) {
      const now = performance.now();
      if (
        now - inferredScrollUpAt < CLAMP_MISREAD_UNDO_WINDOW_MS &&
        now - lastScrollGestureAt > CLAMP_MISREAD_UNDO_WINDOW_MS &&
        inferredScrollUpDelta <= containerGrowth + 1
      ) {
        userScrolled = false;
        atBottom = true;
        showScrollToBottom.value = false;
        inferredScrollUpAt = -Infinity;
        inferredScrollUpDelta = 0;
      } else {
        clampBudget += containerGrowth;
      }
    }
    lastContainerHeight = containerHeight;
    lastListHeight = listHeight;
    // Keep following pinned to the bottom as content streams in. User scroll-up
    // detection lives solely in handleScroll (with clamp discounting); inferring
    // it from resize events is what misfired on layout clamps.
    if (!userScrolled && !catchingUp) {
      // Avoid reading scrollTop after this write. In WebKit that read resolves
      // the clamped offset by synchronously laying out content-visibility
      // chunks. The observer already gives us both dimensions for free, and
      // container padding cancels out of scrollHeight - clientHeight.
      const bottomScrollTop = observedBottomScrollTop(listHeight, containerHeight);
      container.scrollTop = bottomScrollTop;
      if (listHeight > 0 && containerHeight > 0) lastObservedScrollTop = bottomScrollTop;
    } else {
      lastObservedScrollTop = container.scrollTop;
    }
  });
  // (Re)attach the element observers whenever the list/sentinel nodes change.
  // The v-if="loading" spinner tears down and recreates .messages-list on every
  // conversation load, so observers bound to the old nodes go stale — which is
  // what silently broke auto-scroll and the scroll-to-bottom button after a
  // conversation finished loading. A reactive watch re-observes the live nodes.
  watch(
    [messagesListRef, bottomSentinelRef],
    ([list, sentinel]) => {
      ro?.disconnect();
      bottomObserver?.disconnect();
      // Observe the container alongside the list: container resizes (composer
      // growing/shrinking, panels opening) clamp scrollTop just like list
      // shrinks do, and must not read as user scroll-ups.
      lastContainerHeight = 0;
      ro?.observe(container);
      if (list) ro?.observe(list);
      if (sentinel) bottomObserver?.observe(sentinel);
    },
    { immediate: true, flush: "post" },
  );
}

// Save scroll on page hide.
function saveScrollNow() {
  const container = messagesContainerRef.value;
  if (!container || !props.conversationId) return;
  saveScroll(container.scrollTop);
}
function onVisChangeSave() {
  if (document.visibilityState === "hidden") saveScrollNow();
}

// Catch-up suppression on resume.
function handleVisibilityChange() {
  if (document.visibilityState === "hidden") {
    hiddenAt = Date.now();
    return;
  }
  const hiddenFor = hiddenAt ? Date.now() - hiddenAt : 0;
  hiddenAt = null;
  if (hiddenFor > 5000) catchingUp = true;
}

// Cmd/Ctrl+ArrowDown scrolls to bottom.
//
// The composer is only exempt while the caret has somewhere left to go, so the
// native "move down/to end" gesture still works mid-edit. A blanket exemption
// for every text field broke the shortcut in the most common case there is:
// selecting a conversation (or finishing a send) auto-focuses the composer, so
// the shortcut silently did nothing for a reader who had scrolled up and never
// clicked back into the message list. An empty or already-at-end composer has
// no caret movement to preserve, so scrolling is unambiguous there.
function targetOwnsArrowDown(target: HTMLElement): boolean {
  // The terminal dock. xterm swallows nearly every key, but ArrowDown with Meta
  // is one it deliberately passes through (macOS reserves the chord), and it
  // surfaces here with the empty .xterm-helper-textarea as the target -- so
  // neither the textarea rule below nor the cover check would catch it, and
  // Cmd+Down at a shell prompt scrolled the conversation behind the dock. The
  // panel sits below the message list rather than over it, so hit-testing
  // cannot see it either.
  if (target.closest(".terminal-panel")) return true;
  if (target.isContentEditable) return true;
  // tagName rather than instanceof: matches the check this replaced, and keeps
  // working for elements from another document (portals, print views).
  switch (target.tagName) {
    case "TEXTAREA": {
      const el = target as HTMLTextAreaElement;
      // A collapsed caret at the very end of the draft has nowhere further to
      // go. Anything else — text below the caret, or a live selection the key
      // would collapse — is an edit gesture worth preserving.
      return el.selectionStart !== el.selectionEnd || el.selectionEnd < el.value.length;
    }
    // Arrow keys drive most native controls: list navigation in the drawer's
    // search/rename/tag inputs, value stepping in number/date/range, selection
    // in radio groups, and opening a select. Rather than enumerate the few
    // types where ArrowDown is inert (checkbox, buttons), leave them all alone
    // -- none of them is the composer, which is the case this shortcut is for.
    case "INPUT":
    case "SELECT":
      return true;
  }
  return false;
}

// Plain PageUp/PageDown belong to the visible reading surface. The composer is
// deliberately exempt even though it owns focus; modified page keys and other
// editable or independently scrollable controls keep their native behavior.
function targetOwnsPageKey(target: HTMLElement, container: HTMLElement): boolean {
  if (target.closest(".terminal-panel")) return true;
  if (target.classList.contains("message-textarea")) return false;
  if (target.isContentEditable) return true;
  if (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.tagName === "SELECT") {
    return true;
  }

  for (let el: HTMLElement | null = target; el && el !== chatRootRef.value; el = el.parentElement) {
    if (el === container) return false;
    const overflowY = getComputedStyle(el).overflowY;
    if (
      (overflowY === "auto" || overflowY === "scroll" || overflowY === "overlay") &&
      el.scrollHeight > el.clientHeight + 1
    ) {
      return true;
    }
  }
  return false;
}

// True when something is drawn over the message list: the diff viewer, git
// graph, command palette, a modal dialog. Scrolling a list the user cannot see
// is never what they meant, and those overlays have their own keyboard
// bindings. Hit-testing covers every overlay without this component having to
// know about any of them, and can't be fooled by one that renders into a
// portal outside our subtree.
//
// Three probes down the middle rather than one: an overlay covers all of them,
// while a tooltip or floating toolbar parked over a single point does not. The
// points are clamped into the viewport because elementFromPoint returns null
// for anything outside it, which would otherwise read as "covered" whenever
// the container extends past the window. Costs one layout read per keypress,
// which is not a hot path.
function messageListCovered(container: HTMLElement): boolean {
  const rect = container.getBoundingClientRect();
  const left = Math.max(rect.left, 0);
  const right = Math.min(rect.right, window.innerWidth);
  const top = Math.max(rect.top, 0);
  const bottom = Math.min(rect.bottom, window.innerHeight);
  // Entirely offscreen or zero-sized: nothing to scroll into view.
  if (right <= left || bottom <= top) return true;
  const x = (left + right) / 2;
  return [0.25, 0.5, 0.75].every((f) => {
    const hit = document.elementFromPoint(x, top + (bottom - top) * f);
    return !hit || !container.contains(hit);
  });
}

function handleScrollKeyDown(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  if (e.key === "PageUp" || e.key === "PageDown") {
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.defaultPrevented) return;
    const container = messagesContainerRef.value;
    if (!container) return;
    const target = e.target as HTMLElement | null;
    const root = chatRootRef.value;
    const direction: -1 | 1 = e.key === "PageUp" ? -1 : 1;
    if (
      target &&
      target !== document.body &&
      (!root?.contains(target) || targetOwnsPageKey(target, container))
    ) {
      return;
    }
    if (messageListCovered(container)) return;

    e.preventDefault();
    const pageDistance = Math.round(container.clientHeight * 0.85);
    if (direction > 0 && lastListHeight > 0 && lastContainerHeight > 0) {
      const bottomScrollTop = observedBottomScrollTop(lastListHeight, lastContainerHeight);
      if (container.scrollTop + pageDistance >= bottomScrollTop) {
        scrollToBottom();
        return;
      }
    }
    if (direction < 0 && container.scrollTop > 0) {
      lastScrollGestureAt = performance.now();
      markUserScrolledUp();
    }
    container.scrollBy({ top: direction * pageDistance });
    return;
  }

  if (e.key === "Home" || e.key === "ArrowUp") {
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.defaultPrevented) return;
    const container = messagesContainerRef.value;
    const target = e.target as HTMLElement | null;
    const root = chatRootRef.value;
    if (!container || !target || target === document.body) return;
    if (!root?.contains(target) || messageListCovered(container)) return;
    if (
      target.closest(".terminal-panel") ||
      target.isContentEditable ||
      target.tagName === "INPUT" ||
      target.tagName === "TEXTAREA" ||
      target.tagName === "SELECT"
    ) {
      return;
    }
    lastScrollGestureAt = performance.now();
    markUserScrolledUp();
    return;
  }

  if (e.key !== "ArrowDown") return;
  const mod = e.metaKey || e.ctrlKey;
  if (!mod || e.altKey || e.shiftKey) return;
  // Something closer to the key already claimed it -- notably the composer's
  // slash-command and /model autocomplete menus, which step their selection
  // with ArrowDown regardless of modifiers. Those handlers sit on the element
  // itself, so they have run by the time this document-level listener does.
  if (e.defaultPrevented) return;
  const target = e.target as HTMLElement | null;
  if (target && targetOwnsArrowDown(target)) return;
  const container = messagesContainerRef.value;
  if (!container || messageListCovered(container)) return;
  e.preventDefault();
  scrollToBottom();
}

// ?diff=<hash> on mount opens the diff viewer for that commit.
onMounted(() => {
  const params = new URLSearchParams(window.location.search);
  const commit = params.get("diff");
  if (commit) {
    const cwdParam = params.get("cwd") || undefined;
    diffViewerInitialCommit.value = commit;
    diffViewerCwd.value = cwdParam;
    showDiffViewer.value = true;
    params.delete("diff");
    params.delete("cwd");
    const qs = params.toString();
    window.history.replaceState(
      {},
      "",
      `${window.location.pathname}${qs ? `?${qs}` : ""}${window.location.hash}`,
    );
  }

  setupScrollObservers();
  document.addEventListener("visibilitychange", onVisChangeSave);
  window.addEventListener("beforeunload", saveScrollNow);
  document.addEventListener("visibilitychange", handleVisibilityChange);
  document.addEventListener("keydown", handleScrollKeyDown);
  document.addEventListener("keydown", handleMenuShortcut);
});

onUnmounted(() => {
  teardownSubscriptions();
  stopBottomPin();
  tailSweepToken++; // cancel any in-flight background mount sweep
  cancelPendingIdle();
  const container = messagesContainerRef.value;
  container?.removeEventListener("scroll", handleScroll);
  container?.removeEventListener("wheel", handleBottomPinWheel);
  container?.removeEventListener("touchstart", handleBottomPinTouch);
  container?.removeEventListener("touchend", handleScrollPointerUp);
  container?.removeEventListener("touchcancel", handleScrollPointerUp);
  container?.removeEventListener("pointerdown", handleScrollPointerDown);
  window.removeEventListener("pointerup", handleScrollPointerUp);
  window.removeEventListener("pointercancel", handleScrollPointerUp);
  if (scrollSaveTimer) clearTimeout(scrollSaveTimer);
  ro?.disconnect();
  bottomObserver?.disconnect();
  document.removeEventListener("visibilitychange", onVisChangeSave);
  window.removeEventListener("beforeunload", saveScrollNow);
  document.removeEventListener("visibilitychange", handleVisibilityChange);
  document.removeEventListener("keydown", handleScrollKeyDown);
  document.removeEventListener("keydown", handleMenuShortcut);
  document.removeEventListener("mousedown", onAdvancedSettingsOutside);
  mobileMq.removeEventListener("change", onMobileChange);
  if (loadingProgressDelay) clearTimeout(loadingProgressDelay);
  if (highlightTimeout) clearTimeout(highlightTimeout);
  // Module state: an image left open would reappear over the next conversation.
  closeImageComment();
});
</script>
