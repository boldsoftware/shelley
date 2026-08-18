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
      <h2 class="app-bar-title drawer-title">
        {{ showArchived ? t("archived") : t("conversations") }}
      </h2>
      <div class="drawer-header-actions">
        <!-- Search toggle button -->
        <Button
          :class="`btn-icon${searchOpen ? ' search-toggle-active' : ''}`"
          text
          severity="secondary"
          :aria-label="t('searchConversations')"
          v-tooltip.top="t('searchConversations')"
          @click="toggleSearch"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
            />
          </svg>
        </Button>
        <!-- Group by button -->
        <div v-if="!showArchived" ref="groupMenuRef" class="group-by-wrapper">
          <Button
            :class="`btn-icon${groupBy !== 'none' ? ' group-by-active' : ''}`"
            text
            severity="secondary"
            :aria-label="t('groupConversations')"
            v-tooltip.top="t('groupConversations')"
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
          </Button>
          <div v-if="groupMenuOpen" class="group-by-menu">
            <button
              v-for="value in ['none', 'cwd', 'git_repo', 'tag'] as GroupBy[]"
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
        <Button
          v-if="!showArchived"
          class="btn-icon hide-on-desktop"
          text
          severity="secondary"
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
        </Button>
        <Button
          class="btn-icon hide-on-desktop"
          text
          severity="secondary"
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
        </Button>
        <!-- Collapse button - desktop only -->
        <Button
          class="btn-icon show-on-desktop-only"
          text
          severity="secondary"
          :aria-label="t('collapseSidebar')"
          v-tooltip.top="t('collapseSidebar')"
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
        </Button>
      </div>
    </div>

    <!-- Search bar (hidden until the header search toggle is clicked). Doubles
         as the tag filter: `tag:` opens a dropdown of the tags currently on
         screen. -->
    <div v-if="searchOpen" ref="searchWrapRef" class="drawer-search">
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
        ref="searchInputRef"
        type="text"
        class="drawer-search-input"
        :placeholder="t('searchOrTagPlaceholder')"
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
        v-tooltip.top="t('clearSearch')"
        @click="searchQuery = ''"
      >
        ✕
      </button>
      <!-- Tag dropdown. Opens on a `tag:` term, listing only tags carried by
           conversations still on screen, each with the count it would leave —
           so a suggestion can never lead to an empty list. -->
      <div
        v-if="tagMenuOpen"
        class="tag-filter-menu"
        data-testid="tag-filter-panel"
        role="listbox"
        @mousedown.prevent
      >
        <div v-if="visibleOfferedTags.length > 0" class="tag-filter-options scrollable">
          <button
            v-for="(offer, i) in visibleOfferedTags"
            :key="offer.tag"
            type="button"
            role="option"
            :aria-selected="i === highlightIndex"
            :class="`tag-filter-option${i === highlightIndex ? ' highlighted' : ''}`"
            :data-testid="offer.untagged ? 'tag-filter-untagged-option' : 'tag-filter-option'"
            :data-tag="offer.tag"
            @mousemove="highlightIndex = i"
            @click="chooseTerm(offer.term)"
          >
            <span
              :class="`tag-filter-option-name${offer.untagged ? ' tag-filter-option-untagged' : ''}`"
            >
              <span v-if="!offer.untagged" class="conversation-tag-hash">#</span>{{ offer.tag }}
            </span>
            <span class="tag-filter-option-count">{{ offer.count }}</span>
          </button>
        </div>
        <div v-else class="tag-filter-menu-empty">
          {{ activeTagPrefix ? t("noMatchingTags") : t("noTagsToNarrow") }}
        </div>
      </div>
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
        v-else-if="emptiedByTagFilter"
        class="text-secondary drawer-empty-state"
        data-testid="tag-filter-empty"
      >
        <p>{{ t("noConversationsMatchTags") }}</p>
        <Button
          class="drawer-empty-state-action"
          severity="secondary"
          size="small"
          data-testid="tag-filter-empty-clear"
          @click="clearTagFilter"
        >
          {{ t("clearTagFilter") }}
        </Button>
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
              :title="groupTitle(key, group)"
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

    <!-- Footer actions -->
    <div class="drawer-footer">
      <Button
        v-if="!showArchived"
        class="drawer-footer-button"
        severity="secondary"
        @click="emit('open-experience')"
      >
        <span class="drawer-icon-size" aria-hidden="true">🧠</span>
        <span>{{ t("projectExperience") }}</span>
      </Button>
      <Button
        class="drawer-footer-button"
        severity="secondary"
        @click="showArchived = !showArchived"
      >
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
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from "vue";
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
import { isImeComposing } from "../../utils/imeComposing";
import { handleModifiedNavClick } from "../utils/openInNewTab";
import ConversationRow from "./ConversationDrawerRow.vue";
import Button from "primevue/button";
import { DrawerCtxKey, type GroupBy, parseTags } from "./conversationDrawerShared";
import type { EphemeralTerminal } from "./terminalTypes";
import {
  UNTAGGED_TERM,
  compareTagGroupKeys,
  completeTermInQuery,
  filterConversationsByQuery,
  formatTagTerm,
  matchTags,
  offeredTags,
  parseSearchQuery,
  queryHasTagFilter,
  removeTagFromQuery,
  removeUntaggedFromQuery,
  tagGroupKey,
  tagGroupLabel,
  tagMatchesQuery,
  toggleTagInQuery,
} from "../../utils/tagFilter";
import type { OfferedTag } from "../../utils/tagFilter";
import { perfCount } from "../../utils/perf";

const props = defineProps<{
  isOpen: boolean;
  isCollapsed: boolean;
  conversations: ConversationWithState[];
  // All live terminals, used to badge conversations that have more than one
  // terminal pinned to them.
  ephemeralTerminals: EphemeralTerminal[];
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
  (e: "open-experience"): void;
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
const searchOpen = ref(false);
const searchInputRef = ref<HTMLInputElement | null>(null);
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
    return stored === "cwd" || stored === "git_repo" || stored === "tag" ? stored : "none";
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
// Tag filter (see utils/tagFilter.ts). The selection is parsed out of the
// search query, which is the single source of truth.
const parsedQuery = computed(() => parseSearchQuery(searchQuery.value));
const selectedTags = computed(() => parsedQuery.value.tags);
const searchText = computed(() => parsedQuery.value.text);
const activeTagPrefix = computed(() => parsedQuery.value.activeTagPrefix);
const highlightIndex = ref(0);
const searchWrapRef = ref<HTMLElement | null>(null);
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
  const target = e.target as Node;
  if (tagEditorRef.value && tagEditorRef.value.contains(target)) return;
  // The tag-editor suggestion dropdown is teleported to <body>, so it sits
  // outside tagEditorRef; a click on one of its options must not be read as
  // "outside" and close the editor.
  if ((target as Element)?.closest?.(".tag-editor-menu")) return;
  tagEditorId.value = null;
  tagInput.value = "";
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

async function fetchSearchResults(query: string, seq: number) {
  try {
    const results = await api.searchConversationsFTS(query);
    if (seq !== searchSeq) return;
    searchResults.value = results;
  } catch (err) {
    if (seq !== searchSeq) return;
    console.error("Conversation search failed:", err);
    searchResults.value = [];
  } finally {
    if (seq === searchSeq) searching.value = false;
  }
}

// Debounced FTS search across active + archived conversations. Only the free
// text goes to the server; `tag:` terms are applied client-side.
watch(searchText, () => {
  if (searchTimeout) {
    clearTimeout(searchTimeout);
    searchTimeout = null;
  }
  const seq = ++searchSeq;
  const query = searchText.value.trim();
  if (!query) {
    searchResults.value = null;
    searching.value = false;
    return;
  }
  searching.value = true;
  searchTimeout = setTimeout(() => void fetchSearchResults(query, seq), 150);
});

async function refreshSearchResults() {
  const query = searchText.value.trim();
  if (!query) return;
  if (searchTimeout) {
    clearTimeout(searchTimeout);
    searchTimeout = null;
  }
  searching.value = true;
  await fetchSearchResults(query, ++searchSeq);
}

// Switch back to active conversations when triggered externally.
watch(
  () => props.showActiveTrigger,
  (trigger) => {
    if (trigger && trigger > 0) showArchived.value = false;
  },
);

// Bucket subagents under their parent.
const subagentsByParent = computed<Record<string, ConversationWithState[]>>(() => {
  perfCount("drawer.subagentsByParent");
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

async function refreshConversationSnapshots(updated: Conversation) {
  const refreshes: Promise<void>[] = [];
  if (searchText.value.trim()) refreshes.push(refreshSearchResults());
  if (
    showArchived.value ||
    archivedConversations.value.some((c) => c.conversation_id === updated.conversation_id)
  ) {
    refreshes.push(loadArchivedConversations());
  }
  await Promise.all(refreshes);
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
    await refreshConversationSnapshots(updated);
  } catch (err) {
    console.error("Failed to update tags:", err);
  }
}
async function handleAddTag(conversation: Conversation, tag?: string) {
  const value = (tag ?? tagInput.value).trim().replace(/^#+/, "");
  if (!value) return;
  const current = parseTags(conversation);
  if (current.includes(value)) {
    tagInput.value = "";
    return;
  }
  tagInput.value = "";
  await saveTags(conversation.conversation_id, [...current, value]);
}
// The row tag editor's dropdown: existing tags matching what's typed anywhere
// in their text, ranked best-first, minus the conversation's own tags. A
// typed leading `#` is not part of any stored tag, so it's stripped before
// matching.
function matchTagOffers(conversation: Conversation, typed: string): OfferedTag[] {
  const hashes = /^#+/.exec(typed)?.[0] ?? "";
  return matchTags(
    [...props.conversations, ...archivedConversations.value],
    typed.slice(hashes.length),
    parseTags(conversation),
  );
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
  if (editingId.value !== conversationId) return;
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
  editingId.value = null;
  try {
    const updated = await api.renameConversation(conversationId, sanitized);
    emit("renamed", updated);
    await refreshConversationSnapshots(updated);
  } catch (err) {
    console.error("Failed to rename conversation:", err);
  }
}
function handleRenameKeyDown(e: KeyboardEvent, conversationId: string) {
  if (isImeComposing(e)) return;
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
    tag: t("tags"),
  };
  return labels[value];
}
function toggleGroup(groupKey: string) {
  const next = new Set(collapsedGroups.value);
  if (next.has(groupKey)) next.delete(groupKey);
  else next.add(groupKey);
  collapsedGroups.value = next;
}

function toggleSearch() {
  if (searchOpen.value) {
    searchOpen.value = false;
    searchQuery.value = "";
  } else {
    searchOpen.value = true;
    void nextTick(() => searchInputRef.value?.focus());
  }
}

function onSearchKeyDown(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  // While the tag dropdown is up it owns the arrows and Enter; the search box
  // has no other use for them, and this makes `tag:` feel like an autocomplete.
  if (tagMenuOpen.value && visibleOfferedTags.value.length > 0) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      highlightIndex.value = (highlightIndex.value + 1) % visibleOfferedTags.value.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      highlightIndex.value =
        (highlightIndex.value - 1 + visibleOfferedTags.value.length) %
        visibleOfferedTags.value.length;
      return;
    }
    if (e.key === "Enter" || e.key === "Tab") {
      e.preventDefault();
      const pick = visibleOfferedTags.value[highlightIndex.value];
      if (pick) chooseTerm(pick.term);
      return;
    }
  }
  if (e.key === "Escape") {
    e.preventDefault();
    // Escape peels one layer at a time: dropdown, then query, then the box.
    if (tagMenuOpen.value) {
      tagMenuDismissed.value = true;
    } else if (searchQuery.value) {
      searchQuery.value = "";
    } else {
      searchOpen.value = false;
    }
  }
}

function onNewConversationClick(e: MouseEvent) {
  if (handleModifiedNavClick(e, "/new")) return;
  emit("new-conversation");
}

// --- Derived lists ---
const topLevelConversations = computed(() => {
  perfCount("drawer.topLevel");
  resetOrderRefsForResort();
  void resortKey.value;
  const sorted = sortConversationsByBucket(
    filterConversationsByQuery(
      props.conversations.filter((c) => !c.parent_conversation_id),
      parsedQuery.value,
    ),
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
  const sorted = sortConversationsByBucket(
    filterConversationsByQuery(archivedConversations.value, parsedQuery.value),
  );
  const { items, order } = applyStableOrder(sorted, archivedOrder);
  archivedOrder = order;
  return items;
});

// "Searching" means the FTS query is non-empty. A query of only `tag:` terms
// is a filter over the normal list, not a search — so the list keeps its
// grouping and ordering instead of collapsing into flat search results.
const isSearching = computed(() => searchText.value.trim().length > 0);

const displayedConversations = computed<(Conversation | ConversationWithState)[]>(() => {
  // The tag filter is one predicate upstream of all three lists; the active
  // and archived lists apply it in their own computeds above (before the
  // stable-order pass), search results apply it here.
  if (isSearching.value)
    return filterConversationsByQuery(searchResults.value ?? [], parsedQuery.value);
  return showArchived.value ? stableArchivedConversations.value : topLevelConversations.value;
});

// The list the tag dropdown describes: whichever list is on screen,
// unfiltered, so counts mean "conversations you would see here".
const tagFilterPool = computed<Conversation[]>(() => {
  if (isSearching.value) return searchResults.value ?? [];
  if (showArchived.value) return archivedConversations.value;
  return props.conversations.filter((c) => !c.parent_conversation_id);
});

// A dropdown entry. Each carries the whole token it inserts, so the untagged
// entry — `is:untagged`, not a tag — is not a special case in the keyboard
// and click paths.
interface TagOffer {
  tag: string;
  count: number;
  // The token written into the query when this entry is chosen.
  term: string;
  // Rendered without a leading `#`, since it is not a tag.
  untagged?: boolean;
}
const currentOfferedTags = computed<TagOffer[]>(() => {
  const offers: TagOffer[] = offeredTags(tagFilterPool.value, selectedTags.value).map((o) => ({
    tag: o.tag,
    count: o.count,
    term: formatTagTerm(o.tag),
  }));
  // "Untagged" only appears before any tag is chosen: nothing both carries
  // a tag and has none.
  if (selectedTags.value.length === 0 && !parsedQuery.value.untaggedOnly) {
    const count = tagFilterPool.value.filter((c) => parseTags(c).length === 0).length;
    if (count > 0) {
      offers.push({ tag: t("untagged"), count, term: UNTAGGED_TERM, untagged: true });
    }
  }
  return offers;
});
const visibleOfferedTags = computed(() =>
  currentOfferedTags.value.filter((o) => tagMatchesQuery(o.tag, activeTagPrefix.value ?? "")),
);

// The dropdown is open whenever the caret sits in a `tag:` term. Dismissing it
// (Escape, or a click outside) latches until the term changes, so it does not
// immediately reopen on the next keystroke.
const tagMenuDismissed = ref(false);
const tagMenuOpen = computed(
  () => searchOpen.value && activeTagPrefix.value !== null && !tagMenuDismissed.value,
);
watch(activeTagPrefix, () => {
  tagMenuDismissed.value = false;
  highlightIndex.value = 0;
});
function onTagMenuOutside(e: MouseEvent) {
  if (searchWrapRef.value && !searchWrapRef.value.contains(e.target as Node)) {
    tagMenuDismissed.value = true;
  }
}
watch(tagMenuOpen, (open) => {
  if (open) document.addEventListener("mousedown", onTagMenuOutside);
  else document.removeEventListener("mousedown", onTagMenuOutside);
});

// True only when the tag filter is what emptied the list, i.e. removing it
// would bring rows back — otherwise a search matching nothing would be
// blamed on the filter.
const emptiedByTagFilter = computed(
  () =>
    queryHasTagFilter(parsedQuery.value) &&
    displayedConversations.value.length === 0 &&
    tagFilterPool.value.length > 0,
);

// Keep the keyboard highlight in range as the offered set narrows.
watch(visibleOfferedTags, () => {
  if (highlightIndex.value >= visibleOfferedTags.value.length) highlightIndex.value = 0;
});

// Completing a tag from the dropdown rewrites the `tag:` term in place and
// leaves a trailing space, so the next keystroke starts a fresh term.
function chooseTerm(term: string) {
  searchQuery.value = completeTermInQuery(searchQuery.value, term);
  highlightIndex.value = 0;
  void nextTick(() => searchInputRef.value?.focus());
}

// Row chips and the filter share one path: both edit the query.
function toggleTagFilter(tag: string) {
  searchQuery.value = toggleTagInQuery(searchQuery.value, tag);
  if (!searchOpen.value) searchOpen.value = true;
}

// Drops the tag terms but keeps the free text, so clearing a filter does not
// also throw away what the user was searching for.
function clearTagFilter() {
  let next = searchQuery.value;
  for (const tag of selectedTags.value) next = removeTagFromQuery(next, tag);
  next = removeUntaggedFromQuery(next);
  searchQuery.value = next.trim() === "" ? "" : next;
}

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
    } else if (groupBy.value === "tag") {
      // The key is the whole sorted tag set, so every conversation appears
      // exactly once (a tag-per-group layout would duplicate rows).
      key = tagGroupKey(conv);
    }
    if (!key) {
      ungrouped.push(conv);
      continue;
    }
    let group = groups.get(key);
    if (!group) {
      const label = groupBy.value === "tag" ? tagGroupLabel(conv) : formatCwdForDisplay(key) || key;
      group = { label, conversations: [] };
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

  // Tag groups sort alphabetically by their tags, so a group's position is
  // predictable from its name. The recency-ordered modes route through
  // applyStableKeyOrder instead; that pass is skipped here because it pins
  // seen keys to old positions, stranding a new group at the top of an
  // alphabetical list.
  let sorted: [string, Group][];
  if (groupBy.value === "tag") {
    sorted = [...groups.entries()].sort(([a], [b]) => compareTagGroupKeys(a, b));
    groupKeysOrder = sorted.map(([k]) => k);
  } else {
    const desiredKeys = [...groups.entries()]
      .sort((a, b) => maxBucket(b[1].conversations) - maxBucket(a[1].conversations))
      .map(([k]) => k);
    const stableKeys = applyStableKeyOrder(desiredKeys, groupKeysOrder);
    groupKeysOrder = stableKeys;
    sorted = stableKeys.map((k) => [k, groups.get(k)!]);
  }

  if (ungrouped.length > 0) {
    const ungroupedSorted = sortConversationsByBucket(ungrouped);
    const { items, order } = applyStableOrder(ungroupedSorted, groupOrder["__ungrouped__"] || []);
    nextGroupOrder["__ungrouped__"] = order;
    const ungroupedLabel = groupBy.value === "tag" ? t("untagged") : t("other");
    sorted.push(["__ungrouped__", { label: ungroupedLabel, conversations: items }]);
  }

  groupOrder = nextGroupOrder;
  return sorted;
});

// The hover title for a truncated group heading: the full label for tag
// groups (their key is NUL-joined, not for eyes), the untruncated path else.
function groupTitle(key: string, group: Group): string | undefined {
  if (key === "__ungrouped__") return undefined;
  return groupBy.value === "tag" ? group.label : key;
}

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
  document.removeEventListener("mousedown", onTagMenuOutside);
  document.removeEventListener("mousedown", onPendingDeleteOutside);
  document.removeEventListener("mousedown", onTagEditorOutside);
});
onMounted(() => {});

// Share all row-relevant state + handlers with ConversationDrawerRow via inject.
provide(DrawerCtxKey, {
  t,
  currentConversationId: computed(() => props.currentConversationId),
  terminalCounts: computed(() => {
    const counts: Record<string, number> = {};
    for (const tm of props.ephemeralTerminals) {
      if (tm.conversationId !== null) {
        counts[tm.conversationId] = (counts[tm.conversationId] ?? 0) + 1;
      }
    }
    return counts;
  }),
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
  selectedTags,
  toggleTagFilter,
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
  matchTagOffers,
  handleRemoveTag,
  handleArchive,
  handleUnarchive,
  handleCopyGitHash,
  handleDeleteClick,
  handleConfirmDelete,
  handleCancelDelete,
});
</script>
