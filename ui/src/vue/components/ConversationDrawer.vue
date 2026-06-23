<!-- Vue port of components/ConversationDrawer.tsx. The conversation
     list/search/group/archive/rename/tags/delete/drafts sidebar. PRESERVES
     EXACTLY the e2e + i18n contract: classes .drawer/.drawer.open,
     .conversation-item/.active, .conversation-title, .conversation-group,
     .conversation-group-label; aria-labels come from i18n t() keys
     ("Open conversations", "Group conversations", closeConversations,
     collapseSidebar, searchConversations, clearSearch, newConversation, plus
     archive/restore/delete_/rename/editTags/removeTag/cancel). Reuses
     utils/conversationSort, utils/tildify, vue/utils/openInNewTab.

     NOTE: the App-level `.backdrop` element lives in App.tsx / the parent
     (ChatInterface), not here — mirroring the React component which renders
     only the drawer.

     Public API (consumed by ChatInterface):
       Props:
         isOpen: boolean
         isCollapsed: boolean
         conversations: ConversationWithState[]
         currentConversationId: string | null
         viewedConversation?: Conversation | null
         showActiveTrigger?: number   // increment to switch back to active view
       Emits:
         (e: "close"): void                         // onClose
         (e: "toggle-collapse"): void               // onToggleCollapse
         (e: "select-conversation", c: Conversation): void   // onSelectConversation
         (e: "new-conversation"): void              // onNewConversation
         (e: "archived", id: string, next?: Conversation | null): void  // onConversationArchived
         (e: "unarchived", c: Conversation): void   // onConversationUnarchived
         (e: "renamed", c: Conversation): void      // onConversationRenamed -->
<template>
  <div :class="`drawer ${isOpen ? 'open' : ''} ${isCollapsed ? 'collapsed' : ''}`">
    <!-- Header -->
    <div class="drawer-header">
      <h2 class="drawer-title">{{ showArchived ? t("archived") : t("conversations") }}</h2>
      <div class="drawer-header-actions">
        <!-- Group by button -->
        <div v-if="!showArchived" ref="groupMenuRef" class="group-by-wrapper">
          <button
            :class="`btn-icon${groupBy !== 'none' ? ' group-by-active' : ''}`"
            :aria-label="t('groupConversations')"
            :title="t('groupConversations')"
            @click="groupMenuOpen = !groupMenuOpen"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10"
              />
            </svg>
          </button>
          <div v-if="groupMenuOpen" class="group-by-menu">
            <button
              v-for="value in ['none', 'cwd', 'git_repo'] as GroupBy[]"
              :key="value"
              :class="`group-by-menu-item${groupBy === value ? ' active' : ''}`"
              @click="
                handleGroupByChange(value);
                groupMenuOpen = false;
              "
            >
              {{ groupByLabel(value) }}
            </button>
            <div class="group-by-menu-separator" />
            <button
              class="group-by-menu-item"
              :title="t('resortNow')"
              @click="
                resortKey += 1;
                groupMenuOpen = false;
              "
            >
              <svg
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
                class="group-by-menu-icon"
                aria-hidden="true"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                />
              </svg>
              {{ t("resortNow") }}
            </button>
          </div>
        </div>
        <!-- New conversation button - mobile only -->
        <button
          v-if="!showArchived"
          class="btn-icon hide-on-desktop"
          :aria-label="t('newConversation')"
          @click="onNewConversationClick"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M12 4v16m8-8H4"
            />
          </svg>
        </button>
        <button
          class="btn-icon hide-on-desktop"
          :aria-label="t('closeConversations')"
          @click="emit('close')"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M6 18L18 6M6 6l12 12"
            />
          </svg>
        </button>
        <!-- Collapse button - desktop only -->
        <button
          class="btn-icon show-on-desktop-only"
          :aria-label="t('collapseSidebar')"
          :title="t('collapseSidebar')"
          @click="emit('toggle-collapse')"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M11 19l-7-7 7-7m8 14l-7-7 7-7"
            />
          </svg>
        </button>
      </div>
    </div>

    <!-- Search bar -->
    <div class="drawer-search">
      <svg
        class="drawer-search-icon"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
        width="16"
        height="16"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          :stroke-width="2"
          d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
        />
      </svg>
      <input
        type="text"
        class="drawer-search-input"
        :placeholder="t('searchConversations')"
        :value="searchQuery"
        :aria-label="t('searchConversations')"
        @input="searchQuery = ($event.target as HTMLInputElement).value"
        @keydown="onSearchKeyDown"
      />
      <button
        v-if="searchQuery"
        type="button"
        class="drawer-search-clear"
        :aria-label="t('clearSearch')"
        :title="t('clearSearch')"
        @click="searchQuery = ''"
      >
        ✕
      </button>
    </div>

    <!-- Conversations list -->
    <div class="drawer-body scrollable">
      <div
        v-if="isSearching && searching && searchResults === null"
        class="text-secondary drawer-empty-state"
      >
        <p>{{ t("searching") }}</p>
      </div>
      <div
        v-else-if="loadingArchived && showArchived && !isSearching"
        class="text-secondary drawer-empty-state"
      >
        <p>{{ t("loading") }}</p>
      </div>
      <div
        v-else-if="displayedConversations.length === 0"
        class="text-secondary drawer-empty-state"
      >
        <p>
          {{
            isSearching
              ? t("noSearchResults")
              : showArchived
                ? t("noArchivedConversations")
                : t("noConversationsYet")
          }}
        </p>
        <p v-if="!showArchived && !isSearching" class="text-sm drawer-empty-state-hint">
          {{ t("startNewToGetStarted") }}
        </p>
      </div>
      <div v-else-if="groupedConversations" class="conversation-list">
        <div v-for="[key, group] in groupedConversations" :key="key" class="conversation-group">
          <button class="conversation-group-header" @click="toggleGroup(key)">
            <svg
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              class="conversation-group-chevron"
              :style="{ transform: collapsedGroups.has(key) ? 'rotate(-90deg)' : 'rotate(0deg)' }"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M19 9l-7 7-7-7"
              />
            </svg>
            <span
              class="conversation-group-label"
              :title="key === '__ungrouped__' ? undefined : key"
            >
              {{ group.label }}
            </span>
            <span class="conversation-group-count">{{ group.conversations.length }}</span>
          </button>
          <template v-if="!collapsedGroups.has(key)">
            <ConversationRow
              v-for="conv in group.conversations"
              :key="conv.conversation_id"
              :conversation="conv"
            />
          </template>
        </div>
      </div>
      <div v-else class="conversation-list">
        <ConversationRow
          v-for="conv in displayedConversations"
          :key="conv.conversation_id"
          :conversation="conv"
        />
      </div>
    </div>

    <!-- Footer with archived toggle -->
    <div class="drawer-footer">
      <button class="btn-secondary drawer-footer-button" @click="showArchived = !showArchived">
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="drawer-icon-size">
          <path
            v-if="showArchived"
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M11 15l-3-3m0 0l3-3m-3 3h8M3 12a9 9 0 1118 0 9 9 0 01-18 0z"
          />
          <path
            v-else
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
          />
        </svg>
        <span>{{ showArchived ? t("backToConversations") : t("viewArchived") }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, provide, ref, watch } from "vue";
import type { Conversation, ConversationWithState } from "../../types";
import { api } from "../../services/api";
import { useI18n } from "../composables/i18n";
import {
  sortConversationsByBucket,
  maxBucket,
  applyStableOrder,
  applyStableKeyOrder,
  neighborAfterRemoval,
} from "../../utils/conversationSort";
import { tildifyPath } from "../../utils/tildify";
import { handleModifiedNavClick } from "../utils/openInNewTab";
import ConversationRow from "./ConversationDrawerRow.vue";
import { DrawerCtxKey, type GroupBy, parseTags } from "./conversationDrawerShared";

const props = defineProps<{
  isOpen: boolean;
  isCollapsed: boolean;
  conversations: ConversationWithState[];
  currentConversationId: string | null;
  viewedConversation?: Conversation | null;
  showActiveTrigger?: number;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "toggle-collapse"): void;
  (e: "select-conversation", c: Conversation): void;
  (e: "new-conversation"): void;
  (e: "archived", id: string, next?: Conversation | null): void;
  (e: "unarchived", c: Conversation): void;
  (e: "renamed", c: Conversation): void;
}>();

const { t } = useI18n();

// --- URL / modified-click helpers ---
function conversationUrl(conversation: Conversation): string | null {
  if (!conversation.slug) return null;
  return `/c/${conversation.slug}`;
}
function handleModifiedClick(e: MouseEvent, conversation: Conversation): boolean {
  if (!(e.metaKey || e.ctrlKey || e.shiftKey)) return false;
  const url = conversationUrl(conversation);
  if (!url) return false;
  e.preventDefault();
  e.stopPropagation();
  window.open(url, "_blank", "noopener");
  return true;
}
function handleAuxClick(e: MouseEvent, conversation: Conversation) {
  if (e.button !== 1) return;
  const url = conversationUrl(conversation);
  if (!url) return;
  e.preventDefault();
  e.stopPropagation();
  window.open(url, "_blank", "noopener");
}

// --- State ---
const showArchived = ref(false);
const archivedConversations = ref<Conversation[]>([]);
const loadingArchived = ref(false);
const searchQuery = ref("");
const searchResults = ref<ConversationWithState[] | null>(null);
const searching = ref(false);
let searchTimeout: ReturnType<typeof setTimeout> | null = null;
let searchSeq = 0;
const editingId = ref<string | null>(null);
const editingSlug = ref("");
const tagEditorId = ref<string | null>(null);
const tagInput = ref("");
const tagEditorRef = ref<HTMLElement | null>(null);
const tagInputRef = ref<HTMLInputElement | null>(null);
const expandedSubagents = ref<Set<string>>(new Set());
const groupBy = ref<GroupBy>(
  (() => {
    const stored = localStorage.getItem("shelley-group-by");
    return stored === "cwd" || stored === "git_repo" ? stored : "none";
  })(),
);
const collapsedGroups = ref<Set<string>>(new Set());
const groupMenuOpen = ref(false);
const resortKey = ref(0);
const seenIds = ref<Set<string> | null>(null);
const copiedConvId = ref<string | null>(null);
const pendingDeleteId = ref<string | null>(null);
const pendingDeleteRef = ref<HTMLElement | null>(null);
const groupMenuRef = ref<HTMLElement | null>(null);
const renameInputRef = ref<HTMLInputElement | null>(null);
let copyTimeout: ReturnType<typeof setTimeout> | null = null;

// Stable-order refs (mirror React useRef).
let topOrder: string[] = [];
let archivedOrder: string[] = [];
let subagentOrder: Record<string, string[]> = {};
let groupOrder: Record<string, string[]> = {};
let groupKeysOrder: string[] = [];
let flatVisualOrder: Conversation[] = [];
let lastResortKey = 0;
const draftLabelsPinned: Record<string, number> = {};

function resetOrderRefsForResort() {
  if (lastResortKey !== resortKey.value) {
    topOrder = [];
    archivedOrder = [];
    subagentOrder = {};
    groupOrder = {};
    groupKeysOrder = [];
    lastResortKey = resortKey.value;
  }
}

// --- Outside-click handlers (attached only while their popover is open) ---
function onGroupMenuOutside(e: MouseEvent) {
  if (groupMenuRef.value && !groupMenuRef.value.contains(e.target as Node)) {
    groupMenuOpen.value = false;
  }
}
watch(groupMenuOpen, (open) => {
  if (open) document.addEventListener("mousedown", onGroupMenuOutside);
  else document.removeEventListener("mousedown", onGroupMenuOutside);
});

function onPendingDeleteOutside(e: MouseEvent) {
  if (pendingDeleteRef.value && !pendingDeleteRef.value.contains(e.target as Node)) {
    pendingDeleteId.value = null;
  }
}
watch(pendingDeleteId, (id) => {
  if (id) document.addEventListener("mousedown", onPendingDeleteOutside);
  else document.removeEventListener("mousedown", onPendingDeleteOutside);
});

function onTagEditorOutside(e: MouseEvent) {
  if (tagEditorRef.value && !tagEditorRef.value.contains(e.target as Node)) {
    tagEditorId.value = null;
    tagInput.value = "";
  }
}
watch(tagEditorId, (id) => {
  if (id) document.addEventListener("mousedown", onTagEditorOutside);
  else document.removeEventListener("mousedown", onTagEditorOutside);
});

// Load archived when the archived view is first opened.
watch(showArchived, (sa) => {
  if (sa && archivedConversations.value.length === 0) {
    void loadArchivedConversations();
  }
});

// Debounced FTS search across active + archived conversations.
watch(searchQuery, () => {
  if (searchTimeout) {
    clearTimeout(searchTimeout);
    searchTimeout = null;
  }
  const seq = ++searchSeq;
  const q = searchQuery.value.trim();
  if (!q) {
    searchResults.value = null;
    searching.value = false;
    return;
  }
  searching.value = true;
  searchTimeout = setTimeout(async () => {
    try {
      const results = await api.searchConversationsFTS(q);
      if (seq !== searchSeq) return;
      searchResults.value = results;
    } catch (err) {
      if (seq !== searchSeq) return;
      console.error("Conversation search failed:", err);
      searchResults.value = [];
    } finally {
      if (seq === searchSeq) searching.value = false;
    }
  }, 150);
});

// Switch back to active conversations when triggered externally.
watch(
  () => props.showActiveTrigger,
  (trigger) => {
    if (trigger && trigger > 0) showArchived.value = false;
  },
);

// Bucket subagents under their parent.
const subagentsByParent = computed<Record<string, ConversationWithState[]>>(() => {
  resetOrderRefsForResort();
  void resortKey.value;
  const out: Record<string, ConversationWithState[]> = {};
  for (const conv of props.conversations) {
    if (conv.parent_conversation_id) {
      (out[conv.parent_conversation_id] ||= []).push(conv);
    }
  }
  const nextOrder: Record<string, string[]> = {};
  for (const key of Object.keys(out)) {
    const sorted = sortConversationsByBucket(out[key]);
    const { items, order } = applyStableOrder(sorted, subagentOrder[key] || []);
    out[key] = items;
    nextOrder[key] = order;
  }
  subagentOrder = nextOrder;
  return out;
});

// Track which ids exist so newly-added rows animate in.
watch(
  [() => props.conversations, archivedConversations],
  () => {
    const ids = new Set<string>();
    for (const c of props.conversations) ids.add(c.conversation_id);
    for (const c of archivedConversations.value) ids.add(c.conversation_id);
    const prev = seenIds.value;
    if (prev && prev.size === ids.size) {
      let same = true;
      for (const id of ids) {
        if (!prev.has(id)) {
          same = false;
          break;
        }
      }
      if (same) return;
    }
    seenIds.value = ids;
  },
  { immediate: true },
);

// Auto-expand the parent when viewing one of its subagents.
watch(
  [() => props.viewedConversation, showArchived],
  () => {
    const parentId = props.viewedConversation?.parent_conversation_id;
    if (!showArchived.value && parentId && !expandedSubagents.value.has(parentId)) {
      expandedSubagents.value = new Set([...expandedSubagents.value, parentId]);
    }
  },
  { immediate: true },
);

function toggleSubagents(e: MouseEvent, conversationId: string) {
  e.stopPropagation();
  const next = new Set(expandedSubagents.value);
  if (next.has(conversationId)) next.delete(conversationId);
  else next.add(conversationId);
  expandedSubagents.value = next;
}

async function loadArchivedConversations() {
  loadingArchived.value = true;
  try {
    archivedConversations.value = await api.getArchivedConversations();
  } catch (err) {
    console.error("Failed to load archived conversations:", err);
  } finally {
    loadingArchived.value = false;
  }
}

function formatDate(timestamp: string): string {
  const date = new Date(timestamp);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (diffDays === 0) {
    return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  } else if (diffDays === 1) {
    return t("yesterday");
  } else if (diffDays < 7) {
    return `${diffDays} ${t("daysAgo")}`;
  } else {
    return date.toLocaleDateString();
  }
}

const formatCwdForDisplay = tildifyPath;

// --- Archive / unarchive / delete ---
async function handleArchive(e: MouseEvent, conversationId: string) {
  e.stopPropagation();
  const nextConversation = neighborAfterRemoval(flatVisualOrder, conversationId);
  try {
    await api.archiveConversation(conversationId);
    emit("archived", conversationId, nextConversation);
    if (showArchived.value) void loadArchivedConversations();
  } catch (err) {
    console.error("Failed to archive conversation:", err);
  }
}
async function handleUnarchive(e: MouseEvent, conversationId: string) {
  e.stopPropagation();
  try {
    const conversation = await api.unarchiveConversation(conversationId);
    archivedConversations.value = archivedConversations.value.filter(
      (c) => c.conversation_id !== conversationId,
    );
    emit("unarchived", conversation);
  } catch (err) {
    console.error("Failed to unarchive conversation:", err);
  }
}
function handleDeleteClick(e: MouseEvent, conversationId: string) {
  e.stopPropagation();
  pendingDeleteId.value = conversationId;
}
async function handleConfirmDelete(e: MouseEvent, conversationId: string) {
  e.stopPropagation();
  pendingDeleteId.value = null;
  try {
    await api.deleteConversation(conversationId);
    archivedConversations.value = archivedConversations.value.filter(
      (c) => c.conversation_id !== conversationId,
    );
  } catch (err) {
    console.error("Failed to delete conversation:", err);
  }
}
function handleCancelDelete(e: MouseEvent) {
  e.stopPropagation();
  pendingDeleteId.value = null;
}

function sanitizeSlug(input: string): string {
  return input
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/[^a-z0-9-]+/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 60)
    .replace(/-$/g, "");
}

// --- Tags ---
function handleOpenTagEditor(e: MouseEvent, conversationId: string) {
  e.stopPropagation();
  tagEditorId.value = tagEditorId.value === conversationId ? null : conversationId;
  tagInput.value = "";
  setTimeout(() => tagInputRef.value?.focus(), 0);
}
async function saveTags(conversationId: string, tags: string[]) {
  const normalized: string[] = [];
  const seen = new Set<string>();
  for (const tag of tags) {
    const trimmed = tag.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    normalized.push(trimmed);
  }
  try {
    const updated = await api.updateConversationTags(conversationId, normalized);
    emit("renamed", updated);
  } catch (err) {
    console.error("Failed to update tags:", err);
  }
}
async function handleAddTag(conversation: Conversation) {
  const value = tagInput.value.trim().replace(/^#+/, "");
  if (!value) return;
  const current = parseTags(conversation);
  if (current.includes(value)) {
    tagInput.value = "";
    return;
  }
  tagInput.value = "";
  await saveTags(conversation.conversation_id, [...current, value]);
}
async function handleRemoveTag(conversation: Conversation, tag: string) {
  const current = parseTags(conversation);
  await saveTags(
    conversation.conversation_id,
    current.filter((tg) => tg !== tag),
  );
}

// --- Rename ---
function handleStartRename(e: MouseEvent, conversation: Conversation) {
  e.stopPropagation();
  editingId.value = conversation.conversation_id;
  editingSlug.value = conversation.slug || "";
  setTimeout(() => renameInputRef.value?.select(), 0);
}
async function handleRename(conversationId: string) {
  const sanitized = sanitizeSlug(editingSlug.value);
  if (!sanitized) {
    editingId.value = null;
    return;
  }
  const isDuplicate = [...props.conversations, ...archivedConversations.value].some(
    (c) => c.slug === sanitized && c.conversation_id !== conversationId,
  );
  if (isDuplicate) {
    alert(t("duplicateName"));
    return;
  }
  try {
    const updated = await api.renameConversation(conversationId, sanitized);
    emit("renamed", updated);
    editingId.value = null;
  } catch (err) {
    console.error("Failed to rename conversation:", err);
  }
}
function handleRenameKeyDown(e: KeyboardEvent, conversationId: string) {
  if (e.isComposing) return;
  if (e.key === "Enter") {
    e.preventDefault();
    void handleRename(conversationId);
  } else if (e.key === "Escape") {
    editingId.value = null;
  }
}

function handleCopyGitHash(e: MouseEvent, hash: string, convId: string) {
  e.stopPropagation();
  navigator.clipboard
    .writeText(hash)
    .then(() => {
      copiedConvId.value = convId;
      if (copyTimeout) clearTimeout(copyTimeout);
      copyTimeout = setTimeout(() => (copiedConvId.value = null), 1500);
    })
    .catch(() => {});
}

function handleGroupByChange(value: GroupBy) {
  groupBy.value = value;
  localStorage.setItem("shelley-group-by", value);
  collapsedGroups.value = new Set();
}
function groupByLabel(value: GroupBy): string {
  const labels: Record<GroupBy, string> = {
    none: t("noGrouping"),
    cwd: t("directory"),
    git_repo: t("gitRepo"),
  };
  return labels[value];
}
function toggleGroup(groupKey: string) {
  const next = new Set(collapsedGroups.value);
  if (next.has(groupKey)) next.delete(groupKey);
  else next.add(groupKey);
  collapsedGroups.value = next;
}

function onSearchKeyDown(e: KeyboardEvent) {
  if (e.key === "Escape" && searchQuery.value) {
    e.preventDefault();
    searchQuery.value = "";
  }
}

function onNewConversationClick(e: MouseEvent) {
  if (handleModifiedNavClick(e, "/new")) return;
  emit("new-conversation");
}

// --- Derived lists ---
const topLevelConversations = computed(() => {
  resetOrderRefsForResort();
  void resortKey.value;
  const sorted = sortConversationsByBucket(
    props.conversations.filter((c) => !c.parent_conversation_id),
  );
  const { items, order } = applyStableOrder(sorted, topOrder);
  topOrder = order;
  return items;
});

const draftLabels = computed<Record<string, string>>(() => {
  const drafts = props.conversations.filter((c) => c.is_draft);
  const pinned = draftLabelsPinned;
  const used = new Set<number>();
  for (const d of drafts) {
    const n = pinned[d.conversation_id];
    if (n !== undefined) used.add(n);
  }
  const unpinned = drafts
    .filter((d) => pinned[d.conversation_id] === undefined)
    .sort((a, b) => (a.created_at < b.created_at ? -1 : 1));
  let next = 1;
  for (const d of unpinned) {
    while (used.has(next)) next++;
    pinned[d.conversation_id] = next;
    used.add(next);
  }
  const live = new Set(drafts.map((d) => d.conversation_id));
  for (const id of Object.keys(pinned)) {
    if (!live.has(id)) delete pinned[id];
  }
  const labels: Record<string, string> = {};
  for (const d of drafts) {
    const n = pinned[d.conversation_id];
    labels[d.conversation_id] = n === 1 ? "draft" : `draft ${n}`;
  }
  return labels;
});

const stableArchivedConversations = computed(() => {
  resetOrderRefsForResort();
  void resortKey.value;
  const sorted = sortConversationsByBucket(archivedConversations.value);
  const { items, order } = applyStableOrder(sorted, archivedOrder);
  archivedOrder = order;
  return items;
});

const isSearching = computed(() => searchQuery.value.trim().length > 0);

const displayedConversations = computed<(Conversation | ConversationWithState)[]>(() => {
  if (isSearching.value) return searchResults.value ?? [];
  return showArchived.value ? stableArchivedConversations.value : topLevelConversations.value;
});

interface Group {
  label: string;
  conversations: ConversationWithState[];
}
const groupedConversations = computed<[string, Group][] | null>(() => {
  if (groupBy.value === "none" || showArchived.value || isSearching.value) return null;
  resetOrderRefsForResort();
  void resortKey.value;

  const groups = new Map<string, Group>();
  const ungrouped: ConversationWithState[] = [];
  for (const conv of topLevelConversations.value) {
    let key: string | null = null;
    if (groupBy.value === "cwd") {
      key = conv.cwd || null;
    } else if (groupBy.value === "git_repo") {
      key = conv.git_worktree_root || conv.git_repo_root || null;
    }
    if (!key) {
      ungrouped.push(conv);
      continue;
    }
    let group = groups.get(key);
    if (!group) {
      group = { label: formatCwdForDisplay(key) || key, conversations: [] };
      groups.set(key, group);
    }
    group.conversations.push(conv);
  }

  const nextGroupOrder: Record<string, string[]> = {};
  for (const [key, group] of groups) {
    const sorted = sortConversationsByBucket(group.conversations);
    const { items, order } = applyStableOrder(sorted, groupOrder[key] || []);
    group.conversations = items;
    nextGroupOrder[key] = order;
  }

  const desiredKeys = [...groups.entries()]
    .sort((a, b) => maxBucket(b[1].conversations) - maxBucket(a[1].conversations))
    .map(([k]) => k);
  const stableKeys = applyStableKeyOrder(desiredKeys, groupKeysOrder);
  groupKeysOrder = stableKeys;
  const sorted: [string, Group][] = stableKeys.map((k) => [k, groups.get(k)!]);

  if (ungrouped.length > 0) {
    const ungroupedSorted = sortConversationsByBucket(ungrouped);
    const { items, order } = applyStableOrder(ungroupedSorted, groupOrder["__ungrouped__"] || []);
    nextGroupOrder["__ungrouped__"] = order;
    sorted.push(["__ungrouped__", { label: t("other"), conversations: items }]);
  }

  groupOrder = nextGroupOrder;
  return sorted;
});

// Maintain the flat visual order for archive-based next-selection.
watch(
  [groupedConversations, displayedConversations],
  ([grouped, displayed]) => {
    flatVisualOrder = grouped ? grouped.flatMap(([, group]) => group.conversations) : displayed;
  },
  { immediate: true },
);

onUnmounted(() => {
  if (searchTimeout) clearTimeout(searchTimeout);
  if (copyTimeout) clearTimeout(copyTimeout);
  document.removeEventListener("mousedown", onGroupMenuOutside);
  document.removeEventListener("mousedown", onPendingDeleteOutside);
  document.removeEventListener("mousedown", onTagEditorOutside);
});
onMounted(() => {});

// Share all row-relevant state + handlers with ConversationDrawerRow via inject.
provide(DrawerCtxKey, {
  t,
  currentConversationId: computed(() => props.currentConversationId),
  subagentsByParent,
  expandedSubagents,
  seenIds,
  copiedConvId,
  pendingDeleteId,
  pendingDeleteRef,
  editingId,
  editingSlug,
  renameInputRef,
  tagEditorId,
  tagInput,
  tagEditorRef,
  tagInputRef,
  draftLabels,
  groupBy,
  formatDate,
  formatCwdForDisplay,
  handleModifiedClick,
  handleAuxClick,
  selectConversation: (c: Conversation) => emit("select-conversation", c),
  toggleSubagents,
  handleStartRename,
  handleRename,
  handleRenameKeyDown,
  handleOpenTagEditor,
  handleAddTag,
  handleRemoveTag,
  handleArchive,
  handleUnarchive,
  handleCopyGitHash,
  handleDeleteClick,
  handleConfirmDelete,
  handleCancelDelete,
});
</script>
