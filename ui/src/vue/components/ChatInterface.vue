<!-- Vue port of components/ChatInterface.tsx. The main chat shell: message
     list (via Message.vue), streaming/tool-progress, composer, context-usage
     bar, terminal/diff/git panels, model/thinking pickers, distill, TOC,
     scroll behavior. Preserves the e2e DOM/ARIA/CSS contract. -->
<template>
  <div class="full-height flex flex-col">
    <!-- Header -->
    <div class="header">
      <div class="header-left">
        <button
          class="btn-icon hide-on-desktop"
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
        </button>

        <button
          v-if="isDrawerCollapsed && onToggleDrawerCollapse"
          class="btn-icon show-on-desktop-only"
          :aria-label="t('expandSidebar')"
          :title="t('expandSidebar')"
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
        </button>

        <h1 class="header-title" :title="currentConversation?.slug || 'Shelley'">
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

        <!-- Overflow menu -->
        <div ref="overflowMenuRef" class="chat-overflow-menu-wrapper">
          <button
            class="btn-icon"
            :aria-label="t('moreOptions')"
            @click="showOverflowMenu = !showOverflowMenu"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M12 5v.01M12 12v.01M12 19v.01M12 6a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2z"
              />
            </svg>
            <span v-if="hasUpdate" class="version-update-dot" />
          </button>

          <div v-if="showOverflowMenu" class="overflow-menu">
            <button
              v-if="hasCwd"
              class="overflow-menu-item"
              @click="
                showOverflowMenu = false;
                showDiffViewer = true;
              "
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
                />
              </svg>
              {{ t("diffs") }}
            </button>
            <button
              v-if="hasCwd"
              class="overflow-menu-item"
              @click="
                showOverflowMenu = false;
                showGitGraph = true;
              "
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                <circle cx="6" cy="6" r="2" :stroke-width="2" />
                <circle cx="6" cy="18" r="2" :stroke-width="2" />
                <circle cx="18" cy="12" r="2" :stroke-width="2" />
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M6 8v8M8 6h2a4 4 0 014 4v0M8 18h2a4 4 0 004-4v0"
                />
              </svg>
              {{ t("gitGraph") }}
            </button>
            <button v-if="terminalURL" class="overflow-menu-item" @click="openTerminalUrl">
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"
                />
              </svg>
              {{ t("terminal") }}
            </button>
            <button
              v-for="(link, index) in links"
              :key="index"
              class="overflow-menu-item"
              @click="openExternalLink(link.url)"
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  :d="
                    link.icon_svg ||
                    'M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14'
                  "
                />
              </svg>
              {{ link.title }}
            </button>

            <template
              v-if="conversationId && onArchiveConversation && !currentConversation?.archived"
            >
              <div class="overflow-menu-divider" />
              <button class="overflow-menu-item" @click="archiveFromMenu">
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M5 8h14M8 8V6a4 4 0 118 0v2m-9 0v10a2 2 0 002 2h6a2 2 0 002-2V8"
                  />
                </svg>
                {{ t("archiveConversation") }}
              </button>
            </template>

            <template v-if="conversationId && messages.length > 0">
              <div class="overflow-menu-divider" />
              <button class="overflow-menu-item" @click="openExport">
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"
                  />
                </svg>
                {{ t("exportConversation") }}
              </button>
            </template>

            <div class="overflow-menu-divider" />
            <button
              class="overflow-menu-item"
              @click="
                showOverflowMenu = false;
                showAgentsMdEditor = true;
              "
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                />
              </svg>
              {{ t("editUserAgentsMd") }}
            </button>

            <div class="overflow-menu-divider" />
            <button
              class="overflow-menu-item"
              @click="
                showOverflowMenu = false;
                openVersionModal();
              "
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                />
              </svg>
              {{ t("checkForNewVersion") }}
              <span v-if="hasUpdate" class="version-menu-dot" />
            </button>

            <div class="overflow-menu-divider" />
            <div class="theme-toggle-row">
              <button
                :class="`theme-toggle-btn${themeMode === 'system' ? ' theme-toggle-btn-selected' : ''}`"
                :title="t('system')"
                @click="setThemeAndApply('system')"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"
                  />
                </svg>
              </button>
              <button
                :class="`theme-toggle-btn${themeMode === 'light' ? ' theme-toggle-btn-selected' : ''}`"
                :title="t('light')"
                @click="setThemeAndApply('light')"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"
                  />
                </svg>
              </button>
              <button
                :class="`theme-toggle-btn${themeMode === 'dark' ? ' theme-toggle-btn-selected' : ''}`"
                :title="t('dark')"
                @click="setThemeAndApply('dark')"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"
                  />
                </svg>
              </button>
            </div>

            <template v-if="notificationSupported">
              <div class="overflow-menu-divider" />
              <div class="theme-toggle-row">
                <button
                  :class="`theme-toggle-btn${browserNotifsEnabled ? ' theme-toggle-btn-selected' : ''}`"
                  :title="
                    browserNotifState === 'denied'
                      ? t('blockedByBrowser')
                      : t('enableNotifications')
                  "
                  :disabled="browserNotifState === 'denied'"
                  @click="enableBrowserNotifs"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
                    />
                  </svg>
                </button>
                <button
                  :class="`theme-toggle-btn${!browserNotifsEnabled ? ' theme-toggle-btn-selected' : ''}`"
                  :title="t('disableNotifications')"
                  @click="disableBrowserNotifs"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M5.586 15H4l1.405-1.405A2.032 2.032 0 006 12.158V9a6.002 6.002 0 014-5.659V3a2 2 0 114 0v.341c.588.17 1.14.432 1.636.772M15 17h-6v1a3 3 0 006 0v-1zM18 9a3 3 0 00-3-3M3 3l18 18"
                    />
                  </svg>
                </button>
              </div>
            </template>

            <div class="overflow-menu-divider" />
            <div class="md-toggle-row">
              <div class="md-toggle-label">{{ t("markdown") }}</div>
              <div class="md-toggle-buttons">
                <button
                  :class="`md-toggle-btn${markdownMode === 'off' ? ' md-toggle-btn-selected' : ''}`"
                  :title="t('showPlainText')"
                  @click="setMarkdownMode('off')"
                >
                  {{ t("off") }}
                </button>
                <button
                  :class="`md-toggle-btn${markdownMode === 'agent' ? ' md-toggle-btn-selected' : ''}`"
                  :title="t('renderMarkdownAgent')"
                  @click="setMarkdownMode('agent')"
                >
                  {{ t("agent") }}
                </button>
                <button
                  :class="`md-toggle-btn${markdownMode === 'all' ? ' md-toggle-btn-selected' : ''}`"
                  :title="t('renderMarkdownAll')"
                  @click="setMarkdownMode('all')"
                >
                  {{ t("all") }}
                </button>
              </div>
            </div>

            <div class="overflow-menu-divider" />
            <div class="language-selector-row">
              <div class="md-toggle-label">
                {{ t("language") }}
                <a
                  :href="reportBugHref"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="report-bug-link"
                  @click.stop
                >
                  [{{ t("reportBug") }}]
                </a>
              </div>
              <LanguageDropdown />
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Messages area -->
    <div class="messages-area-wrapper">
      <div ref="messagesContainerRef" class="messages-container scrollable">
        <template v-if="loading">
          <div v-if="showLoadingProgressUI" class="conversation-loading full-height">
            <div class="spinner" />
            <div class="conversation-loading-title">
              {{
                loadingProgress?.phase === "parsing"
                  ? "Rendering conversation\u2026"
                  : "Loading conversation\u2026"
              }}
            </div>
            <div class="conversation-loading-subtitle">
              <template v-if="loadingProgress">
                <template v-if="loadingProgress.bytesTotal && loadingProgress.bytesTotal > 0">
                  {{ formatBytes(loadingProgress.bytesDownloaded) }} of
                  {{ formatBytes(loadingProgress.bytesTotal) }}
                </template>
                <template v-else
                  >{{ formatBytes(loadingProgress.bytesDownloaded) }} downloaded</template
                >
              </template>
              <template v-else>Starting…</template>
              {{
                lastKnownMessageCount !== null
                  ? ` \u2022 ~${lastKnownMessageCount} messages last time`
                  : ""
              }}
            </div>
            <div class="conversation-loading-bar">
              <div :class="loadingBarFillClass" :style="loadingBarFillStyle" />
            </div>
          </div>
          <div v-else class="flex items-center justify-center full-height">
            <div class="spinner" />
          </div>
        </template>
        <div v-else class="messages-list">
          <!-- empty state -->
          <div v-if="messages.length === 0" class="empty-state">
            <div class="empty-state-content">
              <p class="text-base chat-welcome-text">
                <template v-for="(part, i) in welcomeParts" :key="i">
                  <strong v-if="part === '{hostname}'">{{ hostname }}</strong>
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
              <div v-if="models.length === 0" class="add-model-hint">
                <p class="text-sm chat-secondary-text">{{ t("noModelsConfiguredHint") }}</p>
              </div>
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
                :models="models"
                :thinking-level="conversationThinkingLevel"
              />
              <SystemPromptView
                v-for="sp in block.systemPrompts"
                :key="sp.key"
                :message="sp.message"
              />
              <MessageRenderNode
                v-for="node in block.nodes"
                :key="node.key"
                :node="node"
                :tool-progress="toolProgress"
                :conversation-id="conversationId"
                :on-open-diff-viewer="handleOpenDiffViewer"
                :on-comment-text-change="setDiffCommentText"
                :on-cancel-queued="cancelQueuedMessages"
                :on-fork="forkHandler"
              />
            </div>
          </template>
          <!-- streaming preview -->
          <div v-if="showStreamingPreview" class="message message-agent streaming-message">
            <div class="message-content" data-testid="message-content">
              <div v-if="markdownMode === 'off'" class="whitespace-pre-wrap break-words">
                {{ streamingText }}<span class="streaming-cursor">▊</span>
              </div>
              <div v-else class="streaming-markdown">
                <MarkdownContent :text="streamingText" />
                <span class="streaming-cursor">▊</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Floating nav cluster -->
      <div v-if="conversationId && messages.length > 0" class="chat-nav-cluster">
        <ConversationTOC
          :messages="messages"
          :container-ref="messagesContainerRef"
          :conversation-slug="currentConversation?.slug"
        />
        <button
          v-if="showScrollToBottom"
          class="scroll-to-bottom-button"
          aria-label="Scroll to bottom"
          title="Scroll to bottom"
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
         re-seed from a stale draftValue. Text sync across conversation
         switches is handled by MessageInput's draftValue watch. -->
    <MessageInput
      v-if="!currentConversation?.archived"
      :on-send="sendMessage"
      :on-queue="queueMessage"
      :show-queue-option="!!conversationId"
      :can-queue="canQueue"
      :auto-queue="autoQueue"
      :disabled="sending || loading"
      :auto-focus="true"
      :injected-text="messageInputInjectedText"
      :draft-value="draftValue"
      :initial-rows="messageInputInitialRows"
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
      :initial-path="selectedCwd"
      @close="showDirectoryPicker = false"
      @select="
        (path) => {
          setSelectedCwd(path);
          cwdError = null;
        }
      "
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
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import {
  type Message,
  type Conversation,
  type ToolProgress,
  isDistillStatusMessage,
} from "../../types";
import { api } from "../../services/api";
import { messageStore } from "../../services/messageStore";
import { type ThemeMode, getStoredTheme, setStoredTheme, applyTheme } from "../../services/theme";
import { setFaviconStatus } from "../../services/favicon";
import {
  isChannelEnabled,
  setChannelEnabled,
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
} from "../../services/notifications";
import { useMarkdownMode } from "../composables/markdownMode";
import { useI18n } from "../composables/i18n";
import { useDraftAutosave } from "../composables/draftAutosave";
import { useFeatureFlag } from "../composables/featureFlags";
import { useVersionChecker } from "../composables/versionChecker";
import { focusMessageInputIfUnfocused } from "../../utils/focusMessageInput";
import { buildMessageQuote } from "../../utils/messageQuote";
import { tildifyPath } from "../../utils/tildify";
import { handleModifiedNavClick } from "../utils/openInNewTab";
import { isAutoExpandTool } from "../../utils/toolMeta";
import { formatDay } from "../../utils/messageTime";
import { coalesceMessages, type CoalescedItem } from "./coalesce";
import type { RenderNode, GenerationBlock } from "./renderNode";
import type { EphemeralTerminal } from "./terminalTypes";
import { DEFAULT_THINKING_LEVEL, type ThinkingLevel } from "./thinkingLevel";

import MessageInput from "./MessageInput.vue";
import ConversationTOC from "./ConversationTOC.vue";
import ModelBar from "./ModelBar.vue";
import SystemPromptView from "./SystemPromptView.vue";
import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import MessageSelectionToolbar from "./MessageSelectionToolbar.vue";
import DiffViewer from "./DiffViewer.vue";
import GitGraphViewer from "./GitGraphViewer.vue";
import AgentsMdEditorModal from "./AgentsMdEditorModal.vue";
import TerminalPanel from "./TerminalPanel.vue";
import VersionChecker from "./VersionChecker.vue";
import LanguageDropdown from "./LanguageDropdown.vue";
import MessageRenderNode from "./MessageRenderNode.vue";
import ChatStatusContent from "./ChatStatusContent.vue";
import MarkdownContent from "./MarkdownContent.vue";

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
      conversationType?: "normal" | "orchestrator",
      subagentBackend?: "shelley" | "claude-cli" | "codex-cli",
      toolOverrides?: Record<string, "on" | "off">,
      thinkingLevel?: ThinkingLevel,
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
    ephemeralTerminals: EphemeralTerminal[];
    setEphemeralTerminals: (
      next: EphemeralTerminal[] | ((prev: EphemeralTerminal[]) => EphemeralTerminal[]),
    ) => void;
    onTerminalAttached?: (id: string, termId: string) => void;
    onTerminalClose?: (id: string) => void;
    navigateUserMessageTrigger?: number;
    onConversationUnarchived?: (conversation: Conversation) => void;
    onDraftCreated?: (conversationId: string) => void;
  }>(),
  {
    streamStatus: "connected",
    reconnectNonce: 0,
  },
);

const { t, locale, setLocale } = useI18n();
const { markdownMode, setMarkdownMode } = useMarkdownMode();
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
const loading = ref(true);
const showLoadingProgressUI = ref(false);
const loadingProgress = ref<{
  phase: "downloading" | "parsing";
  bytesDownloaded: number;
  bytesTotal?: number;
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
  }>
>(window.__SHELLEY_INIT__?.models || []);

const THINKING_LEVEL_KEY = "shelley.thinkingLevel";
const thinkingLevel = ref<ThinkingLevel>(
  (() => {
    try {
      const stored = localStorage.getItem(THINKING_LEVEL_KEY);
      const valid: ThinkingLevel[] = ["off", "minimal", "low", "medium", "high", "xhigh"];
      if (stored !== null && valid.includes(stored as ThinkingLevel)) {
        return stored as ThinkingLevel;
      }
    } catch {
      /* ignore */
    }
    return DEFAULT_THINKING_LEVEL;
  })(),
);
function setThinkingLevel(level: ThinkingLevel) {
  thinkingLevel.value = level;
  try {
    localStorage.setItem(THINKING_LEVEL_KEY, level);
  } catch {
    /* ignore */
  }
}

const selectedModel = ref<string>(
  (() => {
    const storedModel = localStorage.getItem("shelley_selected_model");
    const initModels = window.__SHELLEY_INIT__?.models || [];
    if (storedModel) {
      const modelInfo = initModels.find((m) => m.id === storedModel);
      if (modelInfo?.ready) return storedModel;
    }
    const defaultModel = window.__SHELLEY_INIT__?.default_model;
    if (defaultModel) return defaultModel;
    const firstReady = initModels.find((m) => m.ready);
    return firstReady?.id || "claude-sonnet-4.6";
  })(),
);
function setSelectedModel(model: string) {
  selectedModel.value = model;
  localStorage.setItem("shelley_selected_model", model);
}

const selectedCwd = ref<string>("");
const cwdInitialized = ref(false);
function setSelectedCwd(cwd: string) {
  selectedCwd.value = cwd;
  localStorage.setItem("shelley_selected_cwd", cwd);
}

const cwdError = ref<string | null>(null);
const showDirectoryPicker = ref(false);
const showOverflowMenu = ref(false);
const themeMode = ref<ThemeMode>(getStoredTheme());
const isMobile = ref(window.innerWidth < 768);
const browserNotifsEnabled = ref(isChannelEnabled("browser"));
const showDiffViewer = ref(false);
const showGitGraph = ref(false);
const showAgentsMdEditor = ref(false);
const diffViewerInitialCommit = ref<string | undefined>(undefined);
const diffViewerCwd = ref<string | undefined>(undefined);
const diffCommentText = ref("");
const agentWorking = ref(false);
const cancelling = ref(false);
const contextWindowSize = ref(0);
const toolProgress = ref<Record<string, ToolProgress>>({});
const streamingText = ref("");
const subagentBackend = ref<"shelley" | "claude-cli" | "codex-cli">("shelley");
const showAdvancedSettings = ref(false);
const advancedSettingsRef = ref<HTMLDivElement | null>(null);
const cliAgents = window.__SHELLEY_INIT__?.cli_agents || [];
const availableTools = ref<Array<{ name: string; summary: string; default_on: boolean }>>([]);

const showScrollToBottom = ref(false);
const lastKnownMessageCount = ref<number | null>(null);
const terminalInjectedText = ref<string | null>(null);
const terminalAutoFocusId = ref<string | null>(null);

// ---- refs to DOM ----
const messagesContainerRef = ref<HTMLDivElement | null>(null);
const overflowMenuRef = ref<HTMLDivElement | null>(null);

// ---- non-reactive refs (mutable closures) ----
let userScrolled = false;
let highlightTimeout: number | null = null;
let loadingFlag = false;
// undefined = none, null = bottom, number = saved position
let pendingScroll: number | null | undefined = undefined;
let loadingProgressDelay: number | null = null;
let currentConversationId: string | null = props.conversationId;
let catchingUp = false;
let hiddenAt: number | null = null;
let lastGeneration: { id: string | null; gen: number } | null = null;

const terminalURL = window.__SHELLEY_INIT__?.terminal_url || null;
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

const orchestratorPseudoTool = {
  name: "orchestrator",
  summary: "Shelley orchestrator mode (delegates to subagents).",
  default_on: false,
};
const toolOverrideList = computed(() => [orchestratorPseudoTool, ...availableTools.value]);

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
  if (key) localStorage.setItem(key, String(scrollTop));
}
function loadScroll(): number | null {
  const key = scrollKey();
  if (!key) return null;
  const v = localStorage.getItem(key);
  return v != null ? Number(v) : null;
}

// ---- derived ----
const isDistilling = computed(() =>
  messages.value.some((m) => {
    if (m.type !== "system" || !m.user_data) return false;
    try {
      const userData = typeof m.user_data === "string" ? JSON.parse(m.user_data) : m.user_data;
      return userData.distill_status === "in_progress";
    } catch {
      return false;
    }
  }),
);

const selectedModelDisplayName = computed(() => {
  const modelObj = models.value.find((m) => m.id === selectedModel.value);
  return modelObj?.display_name || selectedModel.value;
});

const maxContextTokens = computed(
  () => models.value.find((m) => m.id === selectedModel.value)?.max_context_tokens || 200000,
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
const welcomeParts = computed(() =>
  t("welcomeMessage").split(/(\{hostname\}|\{docsLink\}|\{proxyLink\})/),
);

const coalescedItems = computed(() => coalesceMessages(messages.value));

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// ---- Render model (porting renderMessages into structured data) ----
const renderModel = computed<GenerationBlock[]>(() => {
  const msgs = messages.value;
  if (msgs.length === 0) return [];

  const currentGeneration = props.currentConversation?.current_generation || 1;
  const systemMessagesByGeneration = new Map<number, Message[]>();
  const modelsByGeneration = new Map<number, string>();
  const itemsByGeneration = new Map<number, CoalescedItem[]>();
  const generationSet = new Set<number>();

  msgs.forEach((message) => {
    generationSet.add(message.generation);
    if (message.type === "system" && !isDistillStatusMessage(message)) {
      const existing = systemMessagesByGeneration.get(message.generation) || [];
      existing.push(message);
      systemMessagesByGeneration.set(message.generation, existing);
    }
    if (!modelsByGeneration.has(message.generation) && message.usage_data) {
      try {
        const usage =
          typeof message.usage_data === "string"
            ? JSON.parse(message.usage_data)
            : message.usage_data;
        if (usage?.model) modelsByGeneration.set(message.generation, usage.model);
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
        toolPillsEnabled.value && item.type === "tool" && !isAutoExpandTool(item.toolName);
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
      },
      systemPrompts: (systemMessagesByGeneration.get(generation) || []).map((m) => ({
        key: `system-prompt-${m.message_id}`,
        message: m,
      })),
      nodes: sectionNodes,
    });
  });

  return blocks;
});

const showStreamingPreview = computed(() => !!streamingText.value && agentWorking.value);

// ---- scroll ----
function scrollToBottom() {
  const container = messagesContainerRef.value;
  if (!container) return;
  userScrolled = false;
  showScrollToBottom.value = false;
  let lastHeight = -1;
  let stableCount = 0;
  let frames = 0;
  const step = () => {
    const el = messagesContainerRef.value;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    if (el.scrollHeight === lastHeight) {
      if (++stableCount >= 3) return;
    } else {
      stableCount = 0;
      lastHeight = el.scrollHeight;
    }
    if (++frames < 60) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

function syncFromStore(focusedId: string) {
  const rec = messageStore.peek(focusedId);
  if (focusedId !== currentConversationId) return;
  if (!rec) return;
  messages.value = rec.messages;
  lastKnownMessageCount.value = rec.messages.length;
  saveMsgCount(rec.messages.length);
  contextWindowSize.value = rec.contextWindowSize;
  if (props.onConversationUpdate && rec.conversation) {
    props.onConversationUpdate(rec.conversation);
  }
}

function syncTransientFromStore(focusedId: string) {
  const tr = messageStore.getTransient(focusedId);
  if (focusedId !== currentConversationId) return;
  toolProgress.value = tr.toolProgress;
  streamingText.value = tr.streamingText;
  agentWorking.value = tr.agentWorking;
}

async function loadMessages(focusedId: string) {
  const isCurrent = () => focusedId === currentConversationId;

  if (!messageStore.isHydrated(focusedId)) {
    await messageStore.hydrate(focusedId);
  }
  if (!isCurrent()) return;

  let cached = messageStore.peek(focusedId);
  if (cached) {
    pendingScroll = loadScroll();
    messages.value = cached.messages;
    lastKnownMessageCount.value = cached.messages.length;
    saveMsgCount(cached.messages.length);
    contextWindowSize.value = cached.contextWindowSize;
    if (props.onConversationUpdate && cached.conversation) {
      props.onConversationUpdate(cached.conversation);
    }
    loadingFlag = false;
    loading.value = false;
    showLoadingProgressUI.value = false;
    loadingProgress.value = null;
  }

  if (
    cached &&
    cached.hasFullHistory &&
    (cached.maxSequenceIdKnown <= 0 || cached.maxSequenceId >= cached.maxSequenceIdKnown)
  ) {
    return;
  }

  try {
    loadingFlag = true;
    if (!cached) loading.value = true;
    error.value = null;
    showLoadingProgressUI.value = false;
    if (loadingProgressDelay) clearTimeout(loadingProgressDelay);
    loadingProgressDelay = window.setTimeout(() => {
      showLoadingProgressUI.value = true;
    }, 500);
    if (!cached) lastKnownMessageCount.value = loadMsgCount();
    loadingProgress.value = { phase: "downloading", bytesDownloaded: 0 };

    let response = await api.getConversationWithProgress(focusedId, (progress) => {
      loadingProgress.value = progress;
    });
    if (!isCurrent()) return;

    // Guard against a REST/stream ordering race. The live /api/stream2 feed
    // may have already pushed messages newer than this REST snapshot into the
    // store — e.g. the agent reply to a just-created conversation lands
    // between when we issue the GET and when its response resolves.
    // applyFullHistory replaces the cached messages wholesale, which would
    // silently drop those newer streamed messages (the agent reply vanishes
    // until the next reload). Merge any newer streamed messages into the
    // response so a stale snapshot never regresses live state.
    const live = messageStore.peek(focusedId);
    const respMsgs = response.messages ?? [];
    const respMax = respMsgs.reduce((m, x) => Math.max(m, x.sequence_id), 0);
    if (live && live.messages.length > 0) {
      const newer = live.messages.filter((m) => m.sequence_id > respMax);
      if (newer.length > 0) {
        response = { ...response, messages: [...respMsgs, ...newer] };
      }
    }

    messageStore.applyFullHistory(focusedId, response);
    cached = messageStore.peek(focusedId);

    pendingScroll = loadScroll();
    const loadedMessages = response.messages ?? [];
    messages.value = loadedMessages;
    lastKnownMessageCount.value = loadedMessages.length;
    saveMsgCount(loadedMessages.length);
    loadingFlag = false;
    loading.value = false;
    if (loadingProgressDelay) {
      clearTimeout(loadingProgressDelay);
      loadingProgressDelay = null;
    }
    showLoadingProgressUI.value = false;
    loadingProgress.value = null;
    contextWindowSize.value = response.context_window_size ?? 0;
    if (props.onConversationUpdate && response.conversation) {
      props.onConversationUpdate(response.conversation);
    }
  } catch (err) {
    if (!isCurrent()) return;
    console.error("Failed to load messages:", err);
    error.value = "Failed to load messages";
    loadingFlag = false;
    loading.value = false;
    if (loadingProgressDelay) {
      clearTimeout(loadingProgressDelay);
      loadingProgressDelay = null;
    }
    showLoadingProgressUI.value = false;
    loadingProgress.value = null;
  }
}

// ---- sending / actions ----
async function queueMessage(message: string) {
  if (!message.trim() || !props.conversationId) return;
  try {
    await api.sendMessage(props.conversationId, {
      message: message.trim(),
      model: selectedModel.value,
      queue: true,
    });
  } catch (err) {
    console.error("Failed to queue message:", err);
    throw err;
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

async function sendFirstMessage(prompt: string) {
  if (!props.onFirstMessage) return;
  if (selectedCwd.value) {
    const validation = await api.validateCwd(selectedCwd.value);
    if (!validation.valid) {
      throw new Error(`Invalid working directory: ${validation.error}`);
    }
  }
  const orchestratorOn = toolOverrides.value["orchestrator"] === "on";
  const realOverrides: Record<string, "on" | "off"> = {};
  for (const [k, v] of Object.entries(toolOverrides.value)) {
    if (k === "orchestrator") continue;
    realOverrides[k] = v;
  }
  await props.onFirstMessage(
    prompt,
    selectedModel.value,
    selectedCwd.value || undefined,
    orchestratorOn ? "orchestrator" : undefined,
    orchestratorOn ? subagentBackend.value : undefined,
    Object.keys(realOverrides).length > 0 ? realOverrides : undefined,
    thinkingLevel.value,
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

  if (trimmedMessage === "/fork") {
    await forkConversation();
    return;
  }
  if (trimmedMessage === "/diff") {
    showDiffViewer.value = true;
    return;
  }
  if (trimmedMessage === "/compact" || trimmedMessage.startsWith("/compact ")) {
    const instructions = trimmedMessage.slice("/compact".length).trim();
    await handleDistillCompactNewGeneration(instructions || undefined);
    return;
  }
  if (trimmedMessage === "/new" || trimmedMessage.startsWith("/new ")) {
    const prompt = trimmedMessage.slice("/new".length).trim();
    props.onNewConversation();
    if (!prompt || !props.onFirstMessage) return;
    try {
      sending.value = true;
      error.value = null;
      agentWorking.value = true;
      streamingText.value = "";
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
      await api.sendMessage(effectiveId, {
        message: message.trim(),
        model: selectedModel.value,
        cwd:
          (isDraftConv || !props.conversationId) && selectedCwd.value
            ? selectedCwd.value
            : undefined,
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
  try {
    cancelling.value = true;
    await api.cancelConversation(props.conversationId);
    agentWorking.value = false;
  } catch (err) {
    console.error("Failed to cancel conversation:", err);
    error.value = "Failed to cancel. Please try again.";
  } finally {
    cancelling.value = false;
  }
}

async function handleDistillNewGeneration(instructions?: string) {
  if (!props.conversationId || !props.onDistillNewGeneration) return;
  await props.onDistillNewGeneration(
    props.conversationId,
    selectedModel.value,
    props.currentConversation?.cwd || selectedCwd.value || undefined,
    "default",
    instructions,
  );
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

function setThemeAndApply(mode: ThemeMode) {
  themeMode.value = mode;
  setStoredTheme(mode);
  applyTheme(mode);
}

async function enableBrowserNotifs() {
  if (browserNotifsEnabled.value) return;
  const granted = await requestBrowserNotificationPermission();
  if (granted) browserNotifsEnabled.value = true;
}
function disableBrowserNotifs() {
  if (!browserNotifsEnabled.value) return;
  setChannelEnabled("browser", false);
  browserNotifsEnabled.value = false;
}

function openExternalLink(url: string) {
  showOverflowMenu.value = false;
  window.open(url, "_blank");
}
function openTerminalUrl() {
  showOverflowMenu.value = false;
  const cwd = props.currentConversation?.cwd || selectedCwd.value || "";
  if (!terminalURL) return;
  const url = terminalURL.replace("WORKING_DIR", encodeURIComponent(cwd));
  window.open(url, "_blank");
}
function openExport() {
  showOverflowMenu.value = false;
  window.open(`/export/${props.conversationId}`, "_blank", "noopener");
}
async function archiveFromMenu() {
  showOverflowMenu.value = false;
  if (!props.conversationId || !props.onArchiveConversation) return;
  try {
    await props.onArchiveConversation(props.conversationId);
  } catch (err) {
    console.error("Failed to archive conversation:", err);
  }
}

function onNewConversationClick(e: MouseEvent) {
  if (handleModifiedNavClick(e, "/new")) return;
  props.onNewConversation();
}

const reportBugHref = `https://github.com/boldsoftware/shelley/issues/new?labels=translation&title=${encodeURIComponent(
  "Translation issue: ",
)}&body=${encodeURIComponent(
  "**Language:** \n**Where in the UI:** \n**Current text:** \n**Suggested text:** \n",
)}`;

// ---- draft autosave ----
const draftValue = ref("");
const lazyDraftId = ref<string | null>(null);
let draftConvId: string | null = props.conversationId;
let inflightCreate: Promise<string> | null = null;

async function saveDraft(value: string) {
  const id = draftConvId;
  if (id) {
    if (props.currentConversation?.is_draft) {
      await api.updateDraft(id, value);
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
      // Seed the message store with an empty full-history record for the
      // brand-new draft *before* conversationId flips to it. Otherwise the
      // conversation-switch watcher runs loadMessages on a cache miss, which
      // sets loading=true and disables the textarea. Disabling the focused
      // textarea blurs it (dismissing the soft keyboard mid-typing on iOS);
      // with a cache hit, loadMessages short-circuits and never toggles
      // loading. Mirrors the React implementation.
      messageStore.applyFullHistory(conv.conversation_id, {
        conversation_id: conv.conversation_id,
        messages: [],
        conversation: conv,
        context_window_size: 0,
        max_sequence_id: 0,
      });
      lazyDraftId.value = conv.conversation_id;
      props.onDraftCreated?.(conv.conversation_id);
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
  draftValue.value = value;
  draftAutosave.schedule(value);
}
function handleDraftSendStarted() {
  draftAutosave.cancel();
}
function handleDraftCleared() {
  draftValue.value = "";
  draftAutosave.cancel();
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

// distill callback for the context bar (only when handler available)
const contextBarDistill = computed(() =>
  props.onDistillNewGeneration ? () => handleDistillNewGeneration() : undefined,
);

function setDiffCommentText(text: string) {
  diffCommentText.value = text;
}

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

const notificationSupported = typeof Notification !== "undefined";
const browserNotifState = computed(() => getBrowserNotificationState());

// Loading bar fill class/style mirror the React conditional.
const loadingBarFillClass = computed(() => {
  const lp = loadingProgress.value;
  if (lp?.phase === "parsing") return "conversation-loading-bar-fill parsing";
  if (!lp?.bytesTotal || lp.bytesTotal <= 0) return "conversation-loading-bar-fill indeterminate";
  return "conversation-loading-bar-fill";
});
const loadingBarFillStyle = computed<Record<string, string> | undefined>(() => {
  const lp = loadingProgress.value;
  if (lp?.phase === "parsing") return undefined;
  if (lp?.bytesTotal && lp.bytesTotal > 0) {
    return { width: `${Math.min(100, (lp.bytesDownloaded / lp.bytesTotal) * 100)}%` };
  }
  return undefined;
});

// Props bundle for ChatStatusContent (rendered in the status bar OR the
// mobile message-input slot — mutually exclusive locations).
const statusContentProps = computed(() => ({
  currentConversation: props.currentConversation,
  conversationId: props.conversationId,
  streamStatus: props.streamStatus,
  error: error.value,
  agentWorking: agentWorking.value,
  cancelling: cancelling.value,
  selectedCwd: selectedCwd.value,
  contextWindowSize: contextWindowSize.value,
  maxContextTokens: maxContextTokens.value,
  selectedModelDisplayName: selectedModelDisplayName.value,
  hostname,
  models: models.value,
  selectedModel: selectedModel.value,
  sending: sending.value,
  thinkingLevel: thinkingLevel.value,
  toolOverrides: toolOverrides.value,
  toolOverrideList: toolOverrideList.value,
  toolOverrideCount: toolOverrideCount.value,
  subagentBackend: subagentBackend.value,
  cliAgents,
  cwdError: cwdError.value,
  onUnarchive: handleUnarchive,
  onClearError: () => (error.value = null),
  onCancel: handleCancel,
  onDistillNewGeneration: contextBarDistill.value,
  onStartNewGeneration: handleStartNewGeneration,
  onSelectModel: setSelectedModel,
  onManageModels: () => props.onOpenModelsModal?.(),
  onThinkingChange: setThinkingLevel,
  onSetToolOverride: setToolOverride,
  onResetToolOverrides: resetToolOverrides,
  onSubagentBackend: (backend: "shelley" | "claude-cli" | "codex-cli") =>
    (subagentBackend.value = backend),
  onOpenDirectoryPicker: () => (showDirectoryPicker.value = true),
}));

// ============ effects / watchers ============

// Sync selected model from conversation when switching to an existing one.
watch(
  () => props.currentConversation?.conversation_id,
  () => {
    if (props.currentConversation?.model) setSelectedModel(props.currentConversation.model);
  },
);

// Reset cwdInitialized + subagent backend when switching to new conversation.
watch(
  () => props.conversationId,
  (id) => {
    if (id === null) {
      cwdInitialized.value = false;
      subagentBackend.value = "shelley";
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
  () => props.conversationId,
  (id) => {
    currentConversationId = id;
    teardownSubscriptions();
    if (!id) {
      messages.value = [];
      contextWindowSize.value = 0;
      toolProgress.value = {};
      streamingText.value = "";
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

    unsubStore = messageStore.subscribe(focusedId, () => syncFromStore(focusedId));
    unsubTransient = messageStore.subscribeTransient(focusedId, () =>
      syncTransientFromStore(focusedId),
    );
    void loadMessages(focusedId);
  },
  { immediate: true },
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

// Initialize draftValue from the conversation row when switching into a draft.
watch(
  [
    () => props.conversationId,
    () => props.currentConversation?.is_draft,
    () => props.currentConversation?.draft,
    lazyDraftId,
  ],
  () => {
    if (props.conversationId === lazyDraftId.value && lazyDraftId.value !== null) return;
    if (props.currentConversation?.is_draft) {
      draftValue.value = props.currentConversation.draft || "";
    } else if (!props.conversationId) {
      draftValue.value = "";
    }
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
let terminalCwd = "/";
watch(
  () => props.openTerminalTrigger,
  (trigger) => {
    terminalCwd =
      props.currentConversation?.cwd ||
      selectedCwd.value ||
      window.__SHELLEY_INIT__?.default_cwd ||
      "/";
    if (!trigger || trigger <= 0) return;
    const terminal: EphemeralTerminal = {
      id: `term-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
      command: 'exec "${SHELL:-bash}" -i',
      cwd: terminalCwd,
      createdAt: new Date(),
    };
    props.setEphemeralTerminals((prev) => [...prev, terminal]);
    terminalAutoFocusId.value = terminal.id;
    setTimeout(() => scrollToBottom(), 100);
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

// Close overflow menu on outside click.
function onOverflowOutside(event: MouseEvent) {
  if (overflowMenuRef.value && !overflowMenuRef.value.contains(event.target as Node)) {
    showOverflowMenu.value = false;
  }
}
watch(showOverflowMenu, (open) => {
  document.removeEventListener("mousedown", onOverflowOutside);
  if (open) document.addEventListener("mousedown", onOverflowOutside);
});

// Auto-scroll after DOM updates (mirrors the useLayoutEffect).
watch(
  [messages, loading],
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
            const isNearBottom = container.scrollHeight - pending - container.clientHeight < 100;
            userScrolled = !isNearBottom;
            showScrollToBottom.value = !isNearBottom;
          }
        } else {
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
let mo: MutationObserver | null = null;

function handleScroll() {
  const container = messagesContainerRef.value;
  if (!container) return;
  const { scrollTop, scrollHeight, clientHeight } = container;
  const isNearBottom = scrollHeight - scrollTop - clientHeight < 100;
  showScrollToBottom.value = !isNearBottom;
  userScrolled = !isNearBottom;
  if (scrollSaveTimer) clearTimeout(scrollSaveTimer);
  scrollSaveTimer = window.setTimeout(() => {
    if (!loadingFlag) saveScroll(container.scrollTop);
  }, 100);
}

function setupScrollObservers() {
  const container = messagesContainerRef.value;
  if (!container) return;
  container.addEventListener("scroll", handleScroll);
  let lastScrollHeight = container.scrollHeight;
  ro = new ResizeObserver(() => {
    if (container.scrollHeight === lastScrollHeight) return;
    lastScrollHeight = container.scrollHeight;
    if (!userScrolled && !catchingUp) container.scrollTop = container.scrollHeight;
  });
  const attachRO = () => {
    const list = container.querySelector(".messages-list");
    if (list) {
      ro!.observe(list);
      return true;
    }
    return false;
  };
  if (!attachRO()) {
    mo = new MutationObserver((_, self) => {
      if (attachRO()) {
        self.disconnect();
        mo = null;
      }
    });
    mo.observe(container, { childList: true, subtree: true });
  }
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
function handleScrollKeyDown(e: KeyboardEvent) {
  if (e.key !== "ArrowDown") return;
  const mod = e.metaKey || e.ctrlKey;
  if (!mod || e.altKey || e.shiftKey) return;
  const target = e.target as HTMLElement | null;
  if (target) {
    const tag = target.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || target.isContentEditable) return;
  }
  if (!messagesContainerRef.value) return;
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
});

onUnmounted(() => {
  teardownSubscriptions();
  const container = messagesContainerRef.value;
  container?.removeEventListener("scroll", handleScroll);
  if (scrollSaveTimer) clearTimeout(scrollSaveTimer);
  mo?.disconnect();
  ro?.disconnect();
  document.removeEventListener("visibilitychange", onVisChangeSave);
  window.removeEventListener("beforeunload", saveScrollNow);
  document.removeEventListener("visibilitychange", handleVisibilityChange);
  document.removeEventListener("keydown", handleScrollKeyDown);
  document.removeEventListener("mousedown", onOverflowOutside);
  document.removeEventListener("mousedown", onAdvancedSettingsOutside);
  mobileMq.removeEventListener("change", onMobileChange);
  if (loadingProgressDelay) clearTimeout(loadingProgressDelay);
  if (highlightTimeout) clearTimeout(highlightTimeout);
});
</script>
