<!-- Vue port of src/App.tsx. Owns global state: conversation list,
     current/viewed conversation, drawer, command palette + modals, ephemeral
     terminals, the global SSE stream, URL/slug sync, page title, and global
     keyboard shortcuts. Renders ConversationDrawer, ChatInterface,
     CommandPalette and the modals.

     Worker pool: the React App wrapped everything in @pierre/diffs
     WorkerPoolContextProvider. PatchTool.vue uses the @pierre/diffs SSR
     (synchronous) API and needs no worker pool, so no provider is rendered
     here. -->
<template>
  <!-- Loading gate -->
  <div v-if="loading && conversations.length === 0" class="loading-container">
    <div class="loading-content">
      <div class="spinner" style="margin: 0 auto 1rem" />
      <p class="text-secondary">{{ t("loading") }}</p>
    </div>
  </div>

  <div v-else-if="error && conversations.length === 0" class="error-container">
    <div class="error-content">
      <p class="error-message" style="margin-bottom: 1rem">{{ error }}</p>
      <Button :label="t('retry')" @click="loadConversations" />
    </div>
  </div>

  <template v-else>
    <div v-if="error" class="error-banner" role="alert">
      <div class="error-banner-content">
        <span class="error-message">{{ error }}</span>
        <Button label="Dismiss" text @click="error = null" />
      </div>
    </div>
    <div v-if="banner" class="top-banner" :title="banner">{{ banner }}</div>
    <div class="app-container">
      <ConversationDrawer
        :is-open="drawerOpen"
        :is-collapsed="drawerCollapsed"
        :conversations="conversations"
        :ephemeral-terminals="ephemeralTerminals"
        :current-conversation-id="currentConversationId"
        :viewed-conversation="viewedConversation"
        :show-active-trigger="showActiveTrigger"
        :message-sent-trigger="messageSentTrigger"
        @open="drawerOpen = true"
        @close="drawerOpen = false"
        @toggle-collapse="toggleDrawerCollapsed"
        @select-conversation="selectConversation"
        @new-conversation="startNewConversation"
        @archived="handleConversationArchived"
        @unarchived="handleConversationUnarchived"
        @renamed="handleConversationRenamed"
      />

      <div class="main-content">
        <ChatInterface
          ref="chatInterfaceRef"
          :conversation-id="currentConversationId"
          :stream-status="streamStatus"
          :reconnect-nonce="reconnectNonce"
          :disk-space-status="diskSpaceStatus"
          :on-disk-space-status="applyDiskSpaceStatus"
          :on-open-drawer="() => (drawerOpen = true)"
          :on-new-conversation="startNewConversation"
          :on-select-conversation="selectConversation"
          :on-archive-conversation="archiveFromChat"
          :current-conversation="currentConversation"
          :conversations="conversations"
          :on-conversation-update="updateConversation"
          :on-message-sent="handleMessageSent"
          :on-first-message="handleFirstMessage"
          :on-draft-created="onDraftCreated"
          :on-distill-new-generation="handleDistillNewGeneration"
          :most-recent-cwd="mostRecentCwd"
          :is-drawer-collapsed="drawerCollapsed"
          :on-toggle-drawer-collapse="toggleDrawerCollapsed"
          :open-diff-viewer-trigger="diffViewerTrigger"
          :open-git-graph-trigger="gitGraphTrigger"
          :open-terminal-trigger="terminalTrigger"
          :models-refresh-trigger="modelsRefreshTrigger"
          :cwd-sync-trigger="cwdSyncTrigger"
          :on-open-models-modal="() => (modelsModalOpen = true)"
          :on-open-mcp-servers-modal="() => (mcpServersModalOpen = true)"
          :on-open-file-finder="openFileFinder"
          :on-open-command-palette="() => (commandPaletteOpen = true)"
          :ephemeral-terminals="ephemeralTerminals"
          :set-ephemeral-terminals="setEphemeralTerminals"
          :on-terminal-attached="handleTerminalAttached"
          :on-terminal-scope-change="handleTerminalScopeChange"
          :on-terminal-close="handleTerminalClose"
          :navigate-user-message-trigger="navigateUserMessageTrigger"
          :scroll-to-bottom-trigger="conversationSelectionTrigger"
          :on-conversation-unarchived="handleConversationUnarchived"
          :external-comment-text="editorCommentText"
        />
      </div>

      <CommandPalette
        :is-open="commandPaletteOpen"
        :conversations="topLevelConversations"
        :current-conversation="currentConversation || null"
        :has-cwd="commandPaletteHasCwd"
        :can-record-audio="chatInterfaceRef?.canRecordAudio ?? false"
        :can-record-screen="chatInterfaceRef?.canRecordScreen ?? false"
        @record="beginRecording"
        @close="onCommandPaletteClose"
        @error="error = $event"
        @new-conversation="
          () => {
            startNewConversation();
            commandPaletteOpen = false;
          }
        "
        @new-conversation-with-cwd="
          (cwd) => {
            startNewConversationWithCwd(cwd);
            commandPaletteOpen = false;
          }
        "
        @set-conversation-cwd="
          (cwd) => {
            setConversationCwd(cwd);
            commandPaletteOpen = false;
          }
        "
        @select-conversation="
          (c) => {
            selectConversation(c);
            commandPaletteOpen = false;
          }
        "
        @archive-conversation="archiveFromPalette"
        @open-diff-viewer="
          () => {
            diffViewerTrigger++;
            commandPaletteOpen = false;
          }
        "
        @open-git-graph="
          () => {
            gitGraphTrigger++;
            commandPaletteOpen = false;
          }
        "
        @open-terminal="
          () => {
            terminalTrigger++;
            commandPaletteOpen = false;
          }
        "
        @open-file-finder="openFileFinder"
        @open-models-modal="
          () => {
            modelsModalOpen = true;
            commandPaletteOpen = false;
          }
        "
        @open-mcp-servers-modal="
          () => {
            mcpServersModalOpen = true;
            commandPaletteOpen = false;
          }
        "
        @open-integrations-modal="
          () => {
            integrationsModalOpen = true;
            commandPaletteOpen = false;
          }
        "
        @open-notifications-modal="
          () => {
            notificationsModalOpen = true;
            commandPaletteOpen = false;
          }
        "
        @open-feature-flags-modal="
          () => {
            featureFlagsModalOpen = true;
            commandPaletteOpen = false;
          }
        "
        @open-favicon-emoji-picker="
          () => {
            faviconEmojiPickerOpen = true;
            commandPaletteOpen = false;
          }
        "
        @next-conversation="navigateToNextConversation"
        @previous-conversation="navigateToPreviousConversation"
        @next-user-message="navigateToNextUserMessage"
        @previous-user-message="navigateToPreviousUserMessage"
      />

      <ModelsModal
        :is-open="modelsModalOpen"
        @close="
          () => {
            modelsModalOpen = false;
            focusMessageInputIfUnfocused();
          }
        "
        @models-changed="modelsRefreshTrigger++"
      />

      <McpServersModal
        :is-open="mcpServersModalOpen"
        :login-result="mcpLoginResult"
        @close="
          () => {
            mcpServersModalOpen = false;
            mcpLoginResult = null;
            focusMessageInputIfUnfocused();
          }
        "
      />

      <IntegrationsModal
        :is-open="integrationsModalOpen"
        @open-models-modal="
          () => {
            integrationsModalOpen = false;
            modelsModalOpen = true;
          }
        "
        @close="
          () => {
            integrationsModalOpen = false;
            focusMessageInputIfUnfocused();
          }
        "
      />

      <NotificationsModal
        :is-open="notificationsModalOpen"
        @close="
          () => {
            notificationsModalOpen = false;
            focusMessageInputIfUnfocused();
          }
        "
      />

      <FeatureFlagsModal
        :is-open="featureFlagsModalOpen"
        @close="
          () => {
            featureFlagsModalOpen = false;
            focusMessageInputIfUnfocused();
          }
        "
      />

      <FaviconEmojiPicker
        :is-open="faviconEmojiPickerOpen"
        @close="
          () => {
            faviconEmojiPickerOpen = false;
            focusMessageInputIfUnfocused();
          }
        "
      />

      <FileFinderModal
        :is-open="fileFinderOpen"
        :initial-dir="finderDir"
        @close="fileFinderOpen = false"
        @select="openFileInEditor"
      />

      <EditableFileModal
        v-if="editorFilePath"
        :key="editorFilePath"
        :is-open="!!editorFilePath"
        :path="editorFilePath"
        :title="`Edit ${tildifyPath(editorFilePath)}`"
        :load-url="`/api/read-file?path=${encodeURIComponent(editorFilePath)}`"
        commentable
        @close="editorFilePath = null"
        @comment="onEditorComment"
      />

      <div v-if="drawerOpen" class="backdrop hide-on-desktop" @click="drawerOpen = false" />
    </div>

    <!-- Recomputation-counter overlay (performance-hud flag). -->
    <PerfHud v-if="perfHudEnabled" />
  </template>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, provide, ref, watch, shallowRef } from "vue";
import ChatInterface from "./components/ChatInterface.vue";
import type { RecordingMode } from "./components/recordingDestination";
import { isImeComposing } from "../utils/imeComposing";
import { comboMatches, MENU_COMBOS } from "../utils/menuShortcuts";
import ConversationDrawer from "./components/ConversationDrawer.vue";
import CommandPalette from "./components/CommandPalette.vue";
import ModelsModal from "./components/ModelsModal.vue";
import McpServersModal from "./components/McpServersModal.vue";
import IntegrationsModal from "./components/IntegrationsModal.vue";
import NotificationsModal from "./components/NotificationsModal.vue";
import FeatureFlagsModal from "./components/FeatureFlagsModal.vue";
import FaviconEmojiPicker from "./components/FaviconEmojiPicker.vue";
import FileFinderModal from "./components/FileFinderModal.vue";
import EditableFileModal from "./components/EditableFileModal.vue";
import Button from "primevue/button";
import type { EphemeralTerminal } from "./components/terminalTypes";
import { focusMessageInputIfUnfocused } from "../utils/focusMessageInput";
import { tildifyPath } from "../utils/tildify";
import { resolveAbsPath } from "../utils/absPath";
import {
  type Conversation,
  type ConversationWithState,
  type ConversationListPatchEvent,
  type DiskSpaceStatus,
  type ChatRequest,
} from "../types";
import { api, ApiError } from "../services/api";
import { btwStore } from "../services/btwStore";
import { messageStore } from "../services/messageStore";
import {
  reduceConversationListPatch,
  type ConversationListState,
} from "../services/conversationListStream";
import { connectGlobalStream, type StreamStatus } from "../services/globalStream";
import { handleNotificationEvent } from "../services/notifications";
import { loadCachedDraft } from "../services/draftCache";
import { initialDrawerCollapsed, saveDrawerCollapsedPreference } from "../utils/drawerStartup";
import { perfCount } from "../utils/perf";
import { useI18n } from "./composables/i18n";
import { ConversationsListKey, CurrentConversationIdKey } from "./composables/subagentLive";
import { provideOpenFileEditor } from "./composables/fileEditor";
import { useFeatureFlag } from "./composables/featureFlags";
import { useMobileDrawerSwipe } from "./composables/mobileDrawerSwipe";
import PerfHud from "./components/PerfHud.vue";

const perfHudEnabled = useFeatureFlag("performance-hud");

const { t } = useI18n();

// ---- URL/slug helpers ----
function isGeneratedId(slug: string | null): boolean {
  if (!slug) return true;
  return /^c[a-z0-9]+$/i.test(slug);
}
function getSlugFromPath(): string | null {
  const path = window.location.pathname;
  if (path.startsWith("/c/")) {
    const slug = path.slice(3);
    if (slug) return slug;
  }
  return null;
}
function isNewPath(): boolean {
  return window.location.pathname === "/new";
}

// A brand-new-conversation draft composed offline never reaches the server
// (createDraft fails), so it survives only in localStorage under the "new"
// slot. On reopen we land on "/" and would otherwise auto-select the most
// recent conversation, orphaning that text. Detect a pending non-empty cached
// draft so startup can keep the user in the new-conversation view instead.
function hasPendingNewDraft(): boolean {
  return !!loadCachedDraft(null)?.value.trim();
}

// Captured BEFORE render so URL-updating effects don't clobber it.
const initialSlugFromUrl = getSlugFromPath();
const initialIsNew = isNewPath() || (!getSlugFromPath() && hasPendingNewDraft());

function updateUrlWithSlug(conversation: Conversation | undefined) {
  const currentSlug = getSlugFromPath();
  let newSlug: string | null = null;
  if (conversation?.slug && !isGeneratedId(conversation.slug)) {
    newSlug = conversation.slug;
  } else if (conversation?.is_draft) {
    newSlug = conversation.conversation_id;
  }
  if (currentSlug !== newSlug) {
    if (newSlug) window.history.replaceState({}, "", `/c/${newSlug}`);
    else window.history.replaceState({}, "", "/");
  }
}

function updatePageTitle(conversation: Conversation | undefined) {
  const hostname = window.__SHELLEY_INIT__?.hostname;
  const parts: string[] = [];
  if (conversation?.slug && !isGeneratedId(conversation.slug)) parts.push(conversation.slug);
  if (hostname) parts.push(hostname);
  parts.push("Shelley Agent");
  document.title = parts.join(" - ");
}

const banner = window.__SHELLEY_INIT__?.banner;

// ---- state ----
// Patches replace the list (and only the rows they touch), so deep reactivity is unnecessary.
const conversations = shallowRef<ConversationWithState[]>([]);
const currentConversationId = ref<string | null>(null);
// Subagent tool widgets (SubagentTool.vue) join their slug against the live
// conversation list to show what the subagent is doing right now.
provide(ConversationsListKey, conversations);
provide(CurrentConversationIdKey, currentConversationId);
const viewedConversation = ref<Conversation | null>(null);
const drawerOpen = ref(false);
useMobileDrawerSwipe(drawerOpen);
watch(drawerOpen, (open) => {
  if (!open || !window.matchMedia("(max-width: 767px)").matches) return;
  document.querySelector<HTMLTextAreaElement>('[data-testid="message-input"]')?.blur();
});
// The drawer starts expanded unless the user collapsed it last time.
const drawerCollapsed = ref(initialDrawerCollapsed(localStorage));
const commandPaletteOpen = ref(false);
const diffViewerTrigger = ref(0);
const gitGraphTrigger = ref(0);
const terminalTrigger = ref(0);
const modelsModalOpen = ref(false);
// The MCP login callback redirects to /?mcp_login=NAME, with &mcp_error=MSG if
// it failed (or only ?mcp_error=MSG): show that in the MCP Servers dialog.
const mcpLoginResult = ref(takeMcpLoginResult());
const mcpServersModalOpen = ref(mcpLoginResult.value !== null);
function takeMcpLoginResult() {
  const url = new URL(window.location.href);
  const name = url.searchParams.get("mcp_login");
  const error = url.searchParams.get("mcp_error");
  if (name === null && error === null) return null;
  url.searchParams.delete("mcp_login");
  url.searchParams.delete("mcp_error");
  window.history.replaceState(window.history.state, "", url);
  return { name, error };
}
const integrationsModalOpen = ref(false);
const notificationsModalOpen = ref(false);
const featureFlagsModalOpen = ref(false);
const faviconEmojiPickerOpen = ref(false);
// Fuzzy file finder (Cmd/Ctrl+P) + the generic editor it opens.
const fileFinderOpen = ref(false);
const editorFilePath = ref<string | null>(null);
// Comment submitted from the file editor's comment mode, to be injected into
// the chat message input. Fresh object per submit so the watcher always fires.
const editorCommentText = ref<{ text: string } | null>(null);
const modelsRefreshTrigger = ref(0);
const cwdSyncTrigger = ref(0);
const navigateUserMessageTrigger = ref(0);
const conversationSelectionTrigger = ref(0);
const loading = ref(true);
const error = ref<string | null>(null);
const ephemeralTerminals = ref<EphemeralTerminal[]>([]);
const streamStatus = ref<StreamStatus>("connected");
// Server-wide low-disk notice; the server sends a snapshot on every (re)connect.
// Revisions only order statuses from one server process: a transition that
// could not be persisted (e.g. the disk was full) is lost on restart, so a
// snapshot is accepted even when its revision is older.
const diskSpaceStatus = ref<DiskSpaceStatus | null>(null);
function applyDiskSpaceStatus(status: DiskSpaceStatus, snapshot = false) {
  if (!snapshot && diskSpaceStatus.value && status.revision < diskSpaceStatus.value.revision) {
    return;
  }
  diskSpaceStatus.value = status;
}
const reconnectNonce = ref(0);
const showActiveTrigger = ref(0);
const messageSentTrigger = ref(0);

// ---- non-reactive refs ----
let initialSlugResolved = false;
let conversationListHash: string | null = null;
let globalStreamHandle: { forceReconnect: () => void; close: () => void } | null = null;

// setEphemeralTerminals supports both array and updater-function forms (parity
// with React's setState) so ChatInterface can call it like the React prop.
function setEphemeralTerminals(
  next: EphemeralTerminal[] | ((prev: EphemeralTerminal[]) => EphemeralTerminal[]),
) {
  ephemeralTerminals.value = typeof next === "function" ? next(ephemeralTerminals.value) : next;
}

// termId is null for a terminal the server ran without a persistent session.
function handleTerminalAttached(id: string, termId: string | null, closeOnExit: boolean) {
  setEphemeralTerminals((prev) =>
    prev.map((tm) =>
      tm.id !== id
        ? tm
        : termId === null
          ? { ...tm, unpersisted: true }
          : { ...tm, termId, closeOnExit },
    ),
  );
}

// Applied only after the server has persisted the new scope.
function handleTerminalScopeChange(id: string, conversationId: string | null) {
  setEphemeralTerminals((prev) =>
    prev.map((tm) => (tm.id === id ? { ...tm, conversationId } : tm)),
  );
}

function handleTerminalClose(id: string) {
  setEphemeralTerminals((prev) => {
    const tm = prev.find((x) => x.id === id);
    if (tm && tm.termId) {
      fetch(`/api/terminals/${encodeURIComponent(tm.termId)}`, { method: "DELETE" }).catch((err) =>
        console.warn("failed to delete terminal:", err),
      );
    }
    return prev.filter((x) => x.id !== id);
  });
}

// ---- derived ----
const topLevelConversations = computed(() =>
  conversations.value.filter((c) => !c.parent_conversation_id),
);

const currentConversation = computed<ConversationWithState | undefined>(() => {
  const found = conversations.value.find(
    (conv) => conv.conversation_id === currentConversationId.value,
  );
  if (found) return found;
  if (viewedConversation.value?.conversation_id === currentConversationId.value) {
    return {
      ...viewedConversation.value,
      working: false,
      subagent_count: 0,
      running_background_jobs: 0,
      max_sequence_id: 0,
    } as ConversationWithState;
  }
  return undefined;
});

const mostRecentCwd = computed(() => {
  const conversation = currentConversation.value?.cwd
    ? currentConversation.value
    : topLevelConversations.value[0];
  return conversation?.git_worktree_root || conversation?.cwd;
});

const commandPaletteHasCwd = computed(
  () =>
    !!(
      currentConversation.value?.cwd ||
      mostRecentCwd.value ||
      localStorage.getItem("shelley_selected_cwd") ||
      window.__SHELLEY_INIT__?.default_cwd
    ),
);

// Directory the fuzzy file finder searches: the current conversation's cwd,
// else the last-used/most-recent cwd, else the server default, else $HOME.
const finderDir = computed(
  () =>
    currentConversation.value?.cwd ||
    mostRecentCwd.value ||
    localStorage.getItem("shelley_selected_cwd") ||
    window.__SHELLEY_INIT__?.default_cwd ||
    "",
);

// ---- navigation ----
function navigateToNextConversation() {
  const list = topLevelConversations.value;
  if (list.length === 0) return;
  const currentIndex = list.findIndex((c) => c.conversation_id === currentConversationId.value);
  const nextIndex = currentIndex < 0 ? 0 : Math.min(currentIndex + 1, list.length - 1);
  const next = list[nextIndex];
  currentConversationId.value = next.conversation_id;
  viewedConversation.value = next;
}
function navigateToPreviousConversation() {
  const list = topLevelConversations.value;
  if (list.length === 0) return;
  const currentIndex = list.findIndex((c) => c.conversation_id === currentConversationId.value);
  const prevIndex = currentIndex < 0 ? 0 : Math.max(currentIndex - 1, 0);
  const prev = list[prevIndex];
  currentConversationId.value = prev.conversation_id;
  viewedConversation.value = prev;
}
function navigateToNextUserMessage() {
  navigateUserMessageTrigger.value = Math.abs(navigateUserMessageTrigger.value) + 1;
}
function navigateToPreviousUserMessage() {
  navigateUserMessageTrigger.value = -(Math.abs(navigateUserMessageTrigger.value) + 1);
}

// ---- conversation list patch handling ----
// The conversation list and the hash it was produced under are a single
// coupled value: the patch stream is a strict old_hash->new_hash chain, so a
// patch may only be applied to the exact list its old_hash describes. The
// reactive `conversations` ref is the template's source of truth; we mirror it
// (plus the hash) into listState and only ever advance both together via
// commitListState. This matches the React app's coupled {list, hash} state so
// neither client can apply a patch built against new state onto a stale list.
function listStateNow(): ConversationListState {
  return { list: conversations.value, hash: conversationListHash };
}

function commitListState(next: ConversationListState) {
  conversations.value = next.list;
  conversationListHash = next.hash;
}

function recoverConversationListStream() {
  conversationListHash = null;
  globalStreamHandle?.forceReconnect();
}

function handleConversationListPatch(event: ConversationListPatchEvent) {
  perfCount("app.listPatch");
  const prev = conversations.value;
  const result = reduceConversationListPatch(listStateNow(), event);
  if (!result.ok) {
    if (result.reason === "hash-mismatch") {
      console.warn("conversation list patch hash mismatch; recovering via reconnect", {
        eventOldHash: result.eventOldHash,
        currentHash: conversationListHash,
      });
    } else {
      console.error(
        "failed to apply conversation list patch; recovering via reconnect",
        result.error,
        { patch: event.patch, prevLen: prev.length },
      );
    }
    recoverConversationListStream();
    return;
  }
  for (const removedId of result.removedIds) {
    const removed = prev.find((conversation) => conversation.conversation_id === removedId);
    void messageStore.delete(removedId);
    btwStore.clear(removedId, removed?.parent_conversation_id ?? undefined);
  }
  messageStore.seedMaxSequenceIdsKnown(result.state.list);
  for (const conv of result.state.list) {
    // Seed from `working` — the list's authoritative working flag, which the
    // drawer indicator also renders — so the status bar and the conversation
    // list (the source of truth) never disagree.
    messageStore.setAgentWorking(conv.conversation_id, conv.working);
  }
  commitListState(result.state);
}

// ---- initial slug resolution ----
async function resolveInitialSlug(convs: Conversation[]): Promise<Conversation | null> {
  if (initialSlugResolved) return null;
  initialSlugResolved = true;
  const urlSlug = initialSlugFromUrl;
  if (!urlSlug) return null;
  const existingConv = convs.find((c) => c.slug === urlSlug || c.conversation_id === urlSlug);
  if (existingConv) return existingConv;
  try {
    const conv = await api.getConversationBySlug(urlSlug);
    if (conv) return conv;
  } catch (err) {
    console.error("Failed to resolve slug:", err);
  }
  window.history.replaceState({}, "", "/");
  return null;
}

async function loadConversations() {
  try {
    loading.value = true;
    error.value = null;
    const snapshot = await api.getConversationsSnapshot();
    messageStore.seedMaxSequenceIdsKnown(snapshot.conversations);
    const activeIds = snapshot.conversations.map((c) => c.conversation_id);
    void messageStore.pruneStale(activeIds, 7 * 24 * 60 * 60 * 1000);
    const streamHash = conversationListHash;
    if (!streamHash) {
      commitListState({ list: snapshot.conversations, hash: snapshot.hash });
    }
    const currentList = streamHash ? conversations.value : snapshot.conversations;
    const topLevel = currentList.filter((c) => !c.parent_conversation_id);

    const slugConv = await resolveInitialSlug(currentList);
    if (slugConv) {
      currentConversationId.value = slugConv.conversation_id;
      viewedConversation.value = slugConv;
    } else if (!initialIsNew && topLevel.length > 0) {
      currentConversationId.value = topLevel[0].conversation_id;
      viewedConversation.value = topLevel[0];
    }
  } catch (err) {
    console.error("Failed to load conversations:", err);
    // The server's reason (e.g. a full disk) beats a generic line.
    error.value = err instanceof ApiError ? err.message : t("failedToLoadConversations");
  } finally {
    loading.value = false;
  }
}

// ---- conversation actions ----
async function startNewConversation(): Promise<boolean> {
  error.value = null;
  const conversation = currentConversation.value;
  let cwd = conversation?.git_worktree_root || conversation?.cwd;
  // Conversations opened outside the list (e.g. archived ones) have no git metadata.
  if (
    cwd &&
    !conversations.value.some((c) => c.conversation_id === conversation?.conversation_id)
  ) {
    try {
      const directory = await api.listDirectory(cwd);
      if (currentConversationId.value !== conversation?.conversation_id) return false;
      if (directory.error) throw new Error(directory.error);
      cwd = directory.git_worktree_root || cwd;
    } catch (err) {
      if (currentConversationId.value !== conversation?.conversation_id) return false;
      error.value = err instanceof Error ? err.message : "Failed to resolve working directory";
      return false;
    }
  }
  if (cwd) {
    localStorage.setItem("shelley_selected_cwd", cwd);
  }
  currentConversationId.value = null;
  viewedConversation.value = null;
  window.history.replaceState({}, "", "/new");
  drawerOpen.value = false;
  return true;
}

function startNewConversationWithCwd(cwd: string) {
  localStorage.setItem("shelley_selected_cwd", cwd);
  currentConversationId.value = null;
  viewedConversation.value = null;
  window.history.replaceState({}, "", "/new");
  drawerOpen.value = false;
  cwdSyncTrigger.value++;
}

function setConversationCwd(cwd: string) {
  localStorage.setItem("shelley_selected_cwd", cwd);
  const conv =
    conversations.value.find((c) => c.conversation_id === currentConversationId.value) ||
    (viewedConversation.value?.conversation_id === currentConversationId.value
      ? viewedConversation.value
      : null);
  if (conv?.is_draft) {
    api.updateDraft(conv.conversation_id, { cwd }).catch((err) => {
      console.debug("Could not persist draft cwd (likely already promoted):", err);
    });
  }
  cwdSyncTrigger.value++;
}

function selectConversation(conversation: Conversation) {
  conversationSelectionTrigger.value++;
  currentConversationId.value = conversation.conversation_id;
  viewedConversation.value = conversation;
  drawerOpen.value = false;
}

function toggleDrawerCollapsed() {
  drawerCollapsed.value = !drawerCollapsed.value;
  saveDrawerCollapsedPreference(drawerCollapsed.value, localStorage);
}

function updateConversation(updatedConversation: Conversation) {
  if (updatedConversation.conversation_id === currentConversationId.value) {
    viewedConversation.value = updatedConversation;
  }
}

function handleMessageSent(conversationId: string) {
  if (conversationId === currentConversationId.value) messageSentTrigger.value++;
}

function handleConversationArchived(
  conversationId: string,
  nextConversation?: Conversation | null,
) {
  void messageStore.delete(conversationId);
  btwStore.clear(conversationId);
  if (currentConversationId.value === conversationId) {
    if (nextConversation && nextConversation.conversation_id !== conversationId) {
      currentConversationId.value = nextConversation.conversation_id;
      viewedConversation.value = nextConversation;
      return;
    }
    const remaining = conversations.value.filter(
      (conv) => conv.conversation_id !== conversationId && !conv.parent_conversation_id,
    );
    currentConversationId.value = remaining.length > 0 ? remaining[0].conversation_id : null;
    viewedConversation.value = remaining.length > 0 ? remaining[0] : null;
  }
}

function handleConversationUnarchived(conversation: Conversation) {
  if (conversation.conversation_id === currentConversationId.value) {
    viewedConversation.value = conversation;
  }
  showActiveTrigger.value++;
}

function handleConversationRenamed(conversation: Conversation) {
  if (conversation.conversation_id === currentConversationId.value) {
    viewedConversation.value = conversation;
  }
}

async function archiveFromChat(conversationId: string) {
  await api.archiveConversation(conversationId);
  handleConversationArchived(conversationId);
}

async function archiveFromPalette(conversationId: string) {
  try {
    await api.archiveConversation(conversationId);
    handleConversationArchived(conversationId);
  } catch (err) {
    console.error("Failed to archive conversation:", err);
  }
}

function onDraftCreated(conversation: Conversation) {
  // The conversation-list patch can arrive after createDraft resolves. Keep
  // the returned draft as the current fallback before changing ids so
  // ChatInterface sees is_draft immediately and skips message loading without
  // fabricating an empty complete message cache.
  viewedConversation.value = conversation;
  currentConversationId.value = conversation.conversation_id;
}

function onCommandPaletteClose() {
  commandPaletteOpen.value = false;
  focusMessageInputIfUnfocused();
}

// Open the fuzzy file finder (also reachable via the command palette).
function openFileFinder() {
  commandPaletteOpen.value = false;
  fileFinderOpen.value = true;
}

// Finder selected a file: close it and open the generic editor on that path.
function openFileInEditor(absPath: string) {
  fileFinderOpen.value = false;
  editorFilePath.value = absPath;
}

// Open any file in the editor modal, from anywhere in the tree (patch tool
// cards, etc.). A successful patch records an absolute path, but a failed one
// carries only the path the agent passed, which may be relative; resolve those
// against the same directory the file finder searches (the conversation's cwd,
// else the last-used one). /api/read-file requires a clean absolute path.
provideOpenFileEditor((path: string) => {
  const abs = resolveAbsPath(path, finderDir.value);
  if (abs) openFileInEditor(abs);
});

// A comment submitted from the file editor's comment mode: hand it to
// ChatInterface for injection into the message input.
function onEditorComment(text: string) {
  editorCommentText.value = { text };
}

async function handleFirstMessage(
  message: string,
  model: string,
  cwd?: string,
  conversationOptions?: ChatRequest["conversation_options"],
) {
  try {
    const response = await api.sendMessageWithNewConversation({
      message,
      model,
      cwd,
      conversation_options: conversationOptions,
    });
    const newConversationId = response.conversation_id;
    messageStore.setAgentWorking(newConversationId, true);
    currentConversationId.value = newConversationId;
    handleMessageSent(newConversationId);
  } catch (err) {
    console.error("Failed to send first message:", err);
    error.value = err instanceof Error ? err.message : "Failed to send message";
    throw err;
  }
}

async function handleDistillNewGeneration(
  sourceConversationId: string,
  model: string,
  cwd?: string,
  method?: "default" | "compact",
  instructions?: string,
) {
  try {
    // Compaction happens in place; don't touch currentConversationId here or
    // a user who navigated away mid-request gets pulled back.
    await api.distillNewGeneration(sourceConversationId, model, cwd, method, instructions);
  } catch (err) {
    console.error("Failed to compact into new generation:", err);
    error.value = "Failed to compact conversation";
    throw err;
  }
}

const chatInterfaceRef = ref<InstanceType<typeof ChatInterface> | null>(null);
function beginRecording(mode: RecordingMode) {
  // The palette can open over another modal; don't hide a live recorder behind it.
  if (document.querySelector('[role="dialog"], [role="alertdialog"]')) return;
  commandPaletteOpen.value = false;
  void chatInterfaceRef.value?.beginRecording(mode);
}

// ---- global keyboard shortcuts (incl. Ctrl+M chord) ----
let chordPending = false;
let chordTimer: number | null = null;
function clearChord() {
  chordPending = false;
  if (chordTimer !== null) {
    clearTimeout(chordTimer);
    chordTimer = null;
  }
}
const isMac = navigator.platform.toUpperCase().includes("MAC");

function handleKeyDown(e: KeyboardEvent) {
  const recordingMode = comboMatches(e, MENU_COMBOS.recordAudio)
    ? "microphone"
    : comboMatches(e, MENU_COMBOS.recordScreen)
      ? "screen"
      : null;
  if (recordingMode) {
    if (e.defaultPrevented || isImeComposing(e)) return;
    e.preventDefault();
    clearChord();
    if (!e.repeat) beginRecording(recordingMode);
    return;
  }

  if (chordPending) {
    clearChord();
    if (e.key === "n" || e.key === "N") {
      e.preventDefault();
      navigateToNextUserMessage();
      return;
    }
    if (e.key === "p" || e.key === "P") {
      e.preventDefault();
      navigateToPreviousUserMessage();
      return;
    }
    return;
  }

  if (e.ctrlKey && !e.metaKey && !e.altKey && (e.key === "m" || e.key === "M")) {
    e.preventDefault();
    chordPending = true;
    chordTimer = window.setTimeout(clearChord, 1500);
    return;
  }

  if (isMac && e.ctrlKey && !e.metaKey) return;
  const modifierPressed = isMac ? e.metaKey : e.ctrlKey;

  if (modifierPressed && e.key === "k") {
    e.preventDefault();
    commandPaletteOpen.value = !commandPaletteOpen.value;
    return;
  }

  // Cmd/Ctrl+P opens the fuzzy file finder (overrides the browser's print
  // shortcut, which all major browsers let us preventDefault).
  if (comboMatches(e, MENU_COMBOS.editFile)) {
    e.preventDefault();
    fileFinderOpen.value = true;
    return;
  }

  if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && e.key === "ArrowDown") {
    e.preventDefault();
    navigateToNextConversation();
    return;
  }

  if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && e.key === "ArrowUp") {
    e.preventDefault();
    navigateToPreviousConversation();
    return;
  }
}

// ---- popstate (back/forward + SubagentTool navigation) ----
async function handlePopState() {
  if (isNewPath()) {
    currentConversationId.value = null;
    viewedConversation.value = null;
    return;
  }
  const slug = getSlugFromPath();
  if (!slug) return;
  const existingConv = conversations.value.find(
    (c) => c.slug === slug || c.conversation_id === slug,
  );
  if (existingConv) {
    currentConversationId.value = existingConv.conversation_id;
    viewedConversation.value = existingConv;
    return;
  }
  try {
    const conv = await api.getConversationBySlug(slug);
    if (conv) {
      currentConversationId.value = conv.conversation_id;
      viewedConversation.value = conv;
    }
  } catch (err) {
    console.error("Failed to navigate to conversation:", err);
  }
}

// ---- page title + URL sync ----
watch(
  [currentConversationId, viewedConversation, conversations],
  () => {
    const currentConv =
      viewedConversation.value?.conversation_id === currentConversationId.value
        ? viewedConversation.value
        : conversations.value.find((conv) => conv.conversation_id === currentConversationId.value);
    if (currentConv) {
      updatePageTitle(currentConv);
      updateUrlWithSlug(currentConv);
    }
  },
  { deep: false },
);

// ---- lifecycle ----
onMounted(() => {
  // Hydrate persistent terminals from the server.
  let cancelled = false;
  fetch("/api/terminals")
    .then((r) => (r.ok ? r.json() : []))
    .then(
      (
        rows: Array<{
          id: string;
          command: string;
          close_on_exit: boolean;
          cwd: string;
          conversation_id: string | null;
          created_at: string;
        }>,
      ) => {
        if (cancelled || !Array.isArray(rows) || rows.length === 0) return;
        setEphemeralTerminals((prev) => {
          const have = new Set(prev.map((tm) => tm.termId).filter(Boolean));
          const restored: EphemeralTerminal[] = rows
            .filter((r) => !have.has(r.id))
            .map((r) => ({
              id: r.id,
              termId: r.id,
              command: r.command,
              closeOnExit: r.close_on_exit,
              cwd: r.cwd,
              conversationId: r.conversation_id ?? null,
              createdAt: new Date(r.created_at || Date.now()),
            }));
          return [...restored, ...prev];
        });
      },
    )
    .catch((err) => console.warn("failed to fetch persistent terminals:", err));
  terminalsHydrationCancel = () => {
    cancelled = true;
  };

  loadConversations();

  globalStreamHandle = connectGlobalStream({
    getHash: () => conversationListHash,
    onListPatch: handleConversationListPatch,
    onNotificationEvent: handleNotificationEvent,
    onModelsChanged: () => modelsRefreshTrigger.value++,
    onStatusChange: (status) => (streamStatus.value = status),
    onDiskSpaceStatus: applyDiskSpaceStatus,
    onReconnect: () => {
      reconnectNonce.value++;
    },
  });

  document.addEventListener("keydown", handleKeyDown);
  window.addEventListener("popstate", handlePopState);
});

let terminalsHydrationCancel: (() => void) | null = null;

onUnmounted(() => {
  terminalsHydrationCancel?.();
  globalStreamHandle?.close();
  globalStreamHandle = null;
  document.removeEventListener("keydown", handleKeyDown);
  window.removeEventListener("popstate", handlePopState);
  clearChord();
});
</script>
