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
      <button class="btn-primary" @click="loadConversations">{{ t("retry") }}</button>
    </div>
  </div>

  <template v-else>
    <div v-if="banner" class="top-banner" :title="banner">{{ banner }}</div>
    <div class="app-container">
      <ConversationDrawer
        :is-open="drawerOpen"
        :is-collapsed="drawerCollapsed"
        :conversations="conversations"
        :current-conversation-id="currentConversationId"
        :viewed-conversation="viewedConversation"
        :show-active-trigger="showActiveTrigger"
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
          :conversation-id="currentConversationId"
          :stream-status="streamStatus"
          :reconnect-nonce="reconnectNonce"
          :on-open-drawer="() => (drawerOpen = true)"
          :on-new-conversation="startNewConversation"
          :on-select-conversation="selectConversation"
          :on-archive-conversation="archiveFromChat"
          :current-conversation="currentConversation"
          :on-conversation-update="updateConversation"
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
          :ephemeral-terminals="ephemeralTerminals"
          :set-ephemeral-terminals="setEphemeralTerminals"
          :on-terminal-attached="handleTerminalAttached"
          :on-terminal-close="handleTerminalClose"
          :navigate-user-message-trigger="navigateUserMessageTrigger"
          :on-conversation-unarchived="handleConversationUnarchived"
        />
      </div>

      <CommandPalette
        :is-open="commandPaletteOpen"
        :conversations="topLevelConversations"
        :current-conversation="currentConversation || null"
        :has-cwd="commandPaletteHasCwd"
        @close="onCommandPaletteClose"
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
        @open-models-modal="
          () => {
            modelsModalOpen = true;
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

      <div v-if="drawerOpen" class="backdrop hide-on-desktop" @click="drawerOpen = false" />
    </div>
  </template>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import ChatInterface from "./components/ChatInterface.vue";
import ConversationDrawer from "./components/ConversationDrawer.vue";
import CommandPalette from "./components/CommandPalette.vue";
import ModelsModal from "./components/ModelsModal.vue";
import NotificationsModal from "./components/NotificationsModal.vue";
import FeatureFlagsModal from "./components/FeatureFlagsModal.vue";
import type { EphemeralTerminal } from "./components/terminalTypes";
import { focusMessageInputIfUnfocused } from "../utils/focusMessageInput";
import {
  type Conversation,
  type ConversationWithState,
  type ConversationListPatchEvent,
} from "../types";
import { api } from "../services/api";
import { messageStore } from "../services/messageStore";
import { applyConversationListPatch } from "../services/conversationListStream";
import { connectGlobalStream, type StreamStatus } from "../services/globalStream";
import { handleNotificationEvent } from "../services/notifications";
import { useI18n } from "./composables/i18n";

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

// Captured BEFORE render so URL-updating effects don't clobber it.
const initialSlugFromUrl = getSlugFromPath();
const initialIsNew = isNewPath();

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
const conversations = ref<ConversationWithState[]>([]);
const currentConversationId = ref<string | null>(null);
const viewedConversation = ref<Conversation | null>(null);
const drawerOpen = ref(false);
const drawerCollapsed = ref(false);
const commandPaletteOpen = ref(false);
const diffViewerTrigger = ref(0);
const gitGraphTrigger = ref(0);
const terminalTrigger = ref(0);
const modelsModalOpen = ref(false);
const notificationsModalOpen = ref(false);
const featureFlagsModalOpen = ref(false);
const modelsRefreshTrigger = ref(0);
const cwdSyncTrigger = ref(0);
const navigateUserMessageTrigger = ref(0);
const loading = ref(true);
const error = ref<string | null>(null);
const ephemeralTerminals = ref<EphemeralTerminal[]>([]);
const streamStatus = ref<StreamStatus>("connected");
const reconnectNonce = ref(0);
const showActiveTrigger = ref(0);

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

function handleTerminalAttached(id: string, termId: string) {
  setEphemeralTerminals((prev) => prev.map((tm) => (tm.id === id ? { ...tm, termId } : tm)));
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
      max_sequence_id: 0,
    } as ConversationWithState;
  }
  return undefined;
});

const mostRecentCwd = computed(
  () =>
    currentConversation.value?.cwd ||
    (topLevelConversations.value.length > 0 ? topLevelConversations.value[0].cwd : null),
);

const commandPaletteHasCwd = computed(
  () =>
    !!(
      currentConversation.value?.cwd ||
      mostRecentCwd.value ||
      localStorage.getItem("shelley_selected_cwd") ||
      window.__SHELLEY_INIT__?.default_cwd
    ),
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
function recoverConversationListStream() {
  conversationListHash = null;
  globalStreamHandle?.forceReconnect();
}

function handleConversationListPatch(event: ConversationListPatchEvent) {
  const currentHash = conversationListHash;
  if (!event.reset && event.old_hash !== currentHash) {
    console.warn("conversation list patch hash mismatch; recovering via reconnect", {
      eventOldHash: event.old_hash,
      currentHash,
    });
    recoverConversationListStream();
    return;
  }
  const prev = conversations.value;
  let next: ConversationWithState[];
  try {
    next = applyConversationListPatch(prev, event.patch);
  } catch (err) {
    console.error("failed to apply conversation list patch; recovering via reconnect", err, {
      patch: event.patch,
      prevLen: prev.length,
    });
    recoverConversationListStream();
    return;
  }
  const nextIds = new Set(next.map((conv) => conv.conversation_id));
  for (const conv of prev) {
    if (!nextIds.has(conv.conversation_id)) {
      void messageStore.delete(conv.conversation_id);
    }
  }
  for (const conv of next) {
    messageStore.setMaxSequenceIdKnown(conv.conversation_id, conv.max_sequence_id);
    messageStore.setAgentWorking(conv.conversation_id, conv.agent_working);
  }
  conversations.value = next;
  conversationListHash = event.new_hash;
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
    for (const conv of snapshot.conversations) {
      messageStore.setMaxSequenceIdKnown(conv.conversation_id, conv.max_sequence_id);
    }
    const activeIds = snapshot.conversations.map((c) => c.conversation_id);
    void messageStore.pruneStale(activeIds, 7 * 24 * 60 * 60 * 1000);
    const streamHash = conversationListHash;
    if (!streamHash) {
      conversations.value = snapshot.conversations;
      conversationListHash = snapshot.hash;
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
    error.value = "Failed to load conversations. Please refresh the page.";
  } finally {
    loading.value = false;
  }
}

// ---- conversation actions ----
function startNewConversation() {
  if (currentConversation.value?.cwd) {
    localStorage.setItem("shelley_selected_cwd", currentConversation.value.cwd);
  }
  currentConversationId.value = null;
  viewedConversation.value = null;
  window.history.replaceState({}, "", "/new");
  drawerOpen.value = false;
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
    api.updateDraftCwd(conv.conversation_id, cwd).catch((err) => {
      console.debug("Could not persist draft cwd (likely already promoted):", err);
    });
  }
  cwdSyncTrigger.value++;
}

function selectConversation(conversation: Conversation) {
  currentConversationId.value = conversation.conversation_id;
  viewedConversation.value = conversation;
  drawerOpen.value = false;
}

function toggleDrawerCollapsed() {
  drawerCollapsed.value = !drawerCollapsed.value;
}

function updateConversation(updatedConversation: Conversation) {
  if (updatedConversation.conversation_id === currentConversationId.value) {
    viewedConversation.value = updatedConversation;
  }
}

function handleConversationArchived(
  conversationId: string,
  nextConversation?: Conversation | null,
) {
  void messageStore.delete(conversationId);
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

function onDraftCreated(id: string) {
  currentConversationId.value = id;
}

function onCommandPaletteClose() {
  commandPaletteOpen.value = false;
  focusMessageInputIfUnfocused();
}

async function handleFirstMessage(
  message: string,
  model: string,
  cwd?: string,
  conversationType?: "normal" | "orchestrator",
  subagentBackend?: "shelley" | "claude-cli" | "codex-cli",
  toolOverrides?: Record<string, "on" | "off">,
  thinkingLevel?: "off" | "minimal" | "low" | "medium" | "high" | "xhigh",
) {
  try {
    const hasOverrides = toolOverrides && Object.keys(toolOverrides).length > 0;
    const hasThinking = !!thinkingLevel;
    const convOpts =
      conversationType === "orchestrator" || hasOverrides || hasThinking
        ? {
            ...(conversationType === "orchestrator"
              ? { type: "orchestrator" as const, subagent_backend: subagentBackend || "shelley" }
              : {}),
            ...(hasOverrides ? { tool_overrides: toolOverrides } : {}),
            ...(hasThinking ? { thinking_level: thinkingLevel } : {}),
          }
        : undefined;
    const response = await api.sendMessageWithNewConversation({
      message,
      model,
      cwd,
      conversation_options: convOpts,
    });
    const newConversationId = response.conversation_id;
    messageStore.setAgentWorking(newConversationId, true);
    currentConversationId.value = newConversationId;
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
    await api.distillNewGeneration(sourceConversationId, model, cwd, method, instructions);
    currentConversationId.value = sourceConversationId;
  } catch (err) {
    console.error("Failed to distill into new generation:", err);
    error.value = "Failed to distill into new generation";
    throw err;
  }
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
    .then((rows: Array<{ id: string; command: string; cwd: string; created_at: string }>) => {
      if (cancelled || !Array.isArray(rows) || rows.length === 0) return;
      setEphemeralTerminals((prev) => {
        const have = new Set(prev.map((tm) => tm.termId).filter(Boolean));
        const restored: EphemeralTerminal[] = rows
          .filter((r) => !have.has(r.id))
          .map((r) => ({
            id: r.id,
            termId: r.id,
            command: r.command,
            cwd: r.cwd,
            createdAt: new Date(r.created_at || Date.now()),
          }));
        return [...restored, ...prev];
      });
    })
    .catch((err) => console.warn("failed to fetch persistent terminals:", err));
  terminalsHydrationCancel = () => {
    cancelled = true;
  };

  loadConversations();

  globalStreamHandle = connectGlobalStream({
    getHash: () => conversationListHash,
    onListPatch: handleConversationListPatch,
    onNotificationEvent: handleNotificationEvent,
    onStatusChange: (status) => (streamStatus.value = status),
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
