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
         messageSentTrigger?: number  // increment after sending in the current chat
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
      <h2 v-if="showArchived" class="app-bar-title drawer-title">{{ t("archived") }}</h2>
      <div class="drawer-header-actions">
        <!-- Search toggle button -->
        <Button
          :class="`btn-icon${searchToolbarActive ? ' search-toggle-active' : ''}`"
          text
          severity="secondary"
          :aria-label="t('searchConversations')"
          v-tooltip.top="searchTooltip"
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
              v-for="value in groupByOptions"
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
        <!-- New conversation button -->
        <Button
          v-if="!showArchived"
          class="btn-icon"
          text
          severity="secondary"
          :aria-label="t('newConversation')"
          v-tooltip.top="t('newConversation')"
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

    <div
      v-if="groupBy === 'daily'"
      class="drawer-day-filter"
      data-testid="conversation-day-filter"
      role="group"
      aria-label="Daily, by last-touched date"
    >
      <button type="button" class="drawer-day-arrow" aria-label="Previous day" @click="pageDay(-1)">
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M15 18l-6-6 6-6"
          />
        </svg>
      </button>
      <button
        type="button"
        class="drawer-day-label"
        title="Choose a day"
        :aria-label="`Choose day: ${selectedDayLabel}, ${displayedConversations.length} chats`"
        aria-haspopup="dialog"
        :aria-expanded="dayCalendarOpen"
        aria-live="polite"
        @click="dayCalendarRef?.toggle($event)"
      >
        <span class="drawer-day-date">{{ selectedDayLabel }}</span>
        <span class="drawer-day-count">{{ displayedConversations.length }}</span>
      </button>
      <button
        type="button"
        class="drawer-day-arrow"
        aria-label="Next day"
        :disabled="!canPageForward"
        @click="pageDay(1)"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 18l6-6-6-6" />
        </svg>
      </button>
      <button
        v-if="!selectedDayIsToday"
        type="button"
        class="drawer-day-today"
        @click="selectDay(shiftLocalDay(new Date(), 0))"
      >
        Today
      </button>
      <button
        type="button"
        class="drawer-day-close"
        aria-label="Turn off Daily"
        v-tooltip.top="'Turn off Daily'"
        @click="handleGroupByChange('none')"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M6 18L18 6M6 6l12 12"
          />
        </svg>
      </button>
    </div>
    <Popover
      ref="dayCalendarRef"
      :pt="{ root: { class: 'drawer-day-calendar', 'aria-label': 'Choose last-touched day' } }"
      @show="dayCalendarOpen = true"
      @hide="dayCalendarOpen = false"
    >
      <DatePicker
        :model-value="selectedDay"
        :max-date="shiftLocalDay(new Date(), 0)"
        inline
        @date-select="selectDay($event)"
      >
        <template #date="{ date }">
          <span
            class="drawer-calendar-date"
            :title="`${calendarDayCount(date)} chats last touched`"
          >
            <span class="drawer-calendar-day-number">{{ date.day }}</span>
            <span class="drawer-calendar-activity">
              <span v-if="calendarDayCount(date) > 0" class="drawer-calendar-count">
                {{ calendarDayCount(date) }}
              </span>
            </span>
          </span>
        </template>
        <template #footer>
          <Button
            class="drawer-calendar-today"
            label="Today"
            text
            size="small"
            @click="selectDay(shiftLocalDay(new Date(), 0))"
          />
        </template>
      </DatePicker>
    </Popover>

    <!-- Search/filter shell. Text and committed filter pills share one
         wrapping editor; the filter actions stay directly underneath it. -->
    <div v-if="searchOpen" ref="searchWrapRef" class="drawer-search">
      <div class="drawer-search-shell">
        <div class="drawer-search-row">
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
          <ConversationQueryEditor
            ref="queryEditorRef"
            v-model="searchQuery"
            :placeholder="t('searchOrTagPlaceholder')"
            :ariaLabelText="t('searchConversations')"
            @keydown="onSearchKeyDown"
            @structured-edit-change="activeStructuredEdit = $event"
          />
          <button
            v-if="searchQuery.trim()"
            type="button"
            class="drawer-search-clear"
            :aria-label="t('clearSearch')"
            v-tooltip.top="t('clearSearch')"
            @click="clearSearch"
          >
            ✕
          </button>
        </div>
      </div>
      <div class="drawer-filter-actions">
        <span
          v-if="multipleParticipantsAvailable"
          class="drawer-filter-action-wrap"
          v-tooltip.top="addUserFilterTooltip"
        >
          <button
            type="button"
            class="drawer-filter-action"
            aria-label="Add user filter"
            :disabled="currentOfferedUsers.length === 0"
            @click="startUserFilter"
          >
            @ user
          </button>
        </span>
        <button
          type="button"
          class="drawer-filter-action"
          aria-label="Add tag filter"
          @click="startTagFilter"
        >
          # tag
        </button>
        <button
          v-if="isSearching"
          type="button"
          class="drawer-filter-action drawer-search-shelley"
          :disabled="enhanceDisabled"
          title="Open a search conversation with AI. Provider billing applies."
          @click="openEnhancement"
        >
          Ask Shelley
        </button>
        <span
          class="drawer-filter-result-count"
          :aria-label="`${displayedConversations.length} results`"
        >
          {{ displayedConversations.length }}
        </span>
      </div>
      <p v-if="enhanceError" role="alert">{{ enhanceError }}</p>
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
      <div
        v-else-if="userMenuOpen"
        class="tag-filter-menu"
        data-testid="user-filter-panel"
        role="listbox"
        @mousedown.prevent
      >
        <div v-if="visibleOfferedUsers.length > 0" class="tag-filter-options scrollable">
          <button
            v-for="(offer, i) in visibleOfferedUsers"
            :key="offer.email"
            type="button"
            role="option"
            :aria-selected="i === highlightIndex"
            :class="`tag-filter-option${i === highlightIndex ? ' highlighted' : ''}`"
            data-testid="user-filter-option"
            @mousemove="highlightIndex = i"
            @click="chooseUser(offer.term)"
          >
            <span class="tag-filter-option-name">{{ offer.email }}</span>
            <span class="tag-filter-option-count">{{ offer.count }}</span>
          </button>
        </div>
        <div v-else class="tag-filter-menu-empty">
          {{ activeUserPrefix?.trim() ? "No matching users" : "No more users to add" }}
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
      <div v-else-if="isSearching && searchError" role="alert" class="drawer-empty-state">
        <p>{{ searchError }}</p>
        <Button text :label="t('retry')" @click="refreshSearchResults" />
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
        v-else-if="emptiedByParticipantFilter"
        class="text-secondary drawer-empty-state"
        data-testid="participant-filter-empty"
      >
        <p>{{ t("noSearchResults") }}</p>
        <Button
          class="drawer-empty-state-action"
          severity="secondary"
          size="small"
          @click="clearParticipantFilter"
        >
          Show all conversations
        </Button>
      </div>
      <div
        v-else-if="selectedDay && displayedConversations.length === 0"
        class="text-secondary drawer-empty-state"
        data-testid="day-filter-empty"
      >
        <p>No matching chats for {{ selectedDayLabel }}.</p>
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
            <span class="conversation-group-label" :title="groupTitle(key, group)">
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
      <div v-else-if="cacheActiveTopLevelRows" class="conversation-list">
        <div
          v-for="conv in stableTopLevelConversations"
          v-show="displayedConversationIds.has(conv.conversation_id)"
          :key="conv.conversation_id"
          class="conversation-row-cache"
        >
          <ConversationRow :conversation="conv" />
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

    <ContextMenu
      ref="contextMenuRef"
      :model="contextMenuItems"
      :aria-label="t('actions')"
      :pt="{ root: { class: 'drawer-actions-menu', 'data-testid': 'conversation-context-menu' } }"
      @hide="onContextMenuHide"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, provide, ref, shallowRef, watch } from "vue";
import type {
  Conversation,
  ConversationWithParticipants,
  ConversationWithState,
} from "../../types";
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
import { filterConversationsByDay, shiftLocalDay } from "../../utils/conversationDayFilter";
import {
  compareParticipantGroupKeys,
  filterConversationsByParticipantQuery,
  hasMultiParticipantConversation,
  hasOtherParticipant,
  participantGroupKey,
  participantGroupLabel,
} from "../../utils/conversationParticipantFilter";
import {
  clearConversationQueryText,
  omitStructuredQueryEdit,
  type ActiveStructuredQueryEdit,
} from "../../utils/conversationQuery";
import { handleModifiedNavClick } from "../utils/openInNewTab";
import ConversationRow from "./ConversationDrawerRow.vue";
import ConversationQueryEditor from "./ConversationQueryEditor.vue";
import Button from "primevue/button";
import ContextMenu from "primevue/contextmenu";
import type { MenuItem } from "primevue/menuitem";
import DatePicker from "primevue/datepicker";
import Popover from "primevue/popover";
import { DrawerCtxKey, type GroupBy, parseTags } from "./conversationDrawerShared";
import { workingConversationIds } from "../../utils/workingConversations";
import type { EphemeralTerminal } from "./terminalTypes";
import {
  UNTAGGED_TERM,
  compareTagGroupKeys,
  completeTermInQuery,
  filterConversationsByQuery,
  formatTagTerm,
  formatUserTerm,
  matchTags,
  offeredTags,
  parseSearchQuery,
  queryHasTagFilter,
  rankExactMatchFirst,
  removeTagFromQuery,
  removeUnattributedFromQuery,
  removeUntaggedFromQuery,
  removeUserFromQuery,
  startFilterTermInQuery,
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
  messageSentTrigger?: number;
}>();

const emit = defineEmits<{
  (e: "open"): void;
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
const archivedConversations = ref<ConversationWithParticipants[]>([]);
const loadingArchived = ref(false);
const searchQuery = ref("");
const searchOpen = ref(false);
const selectedDay = ref<Date | null>(null);
const dayCalendarRef = ref<InstanceType<typeof Popover> | null>(null);
const dayCalendarOpen = ref(false);
const selectedDayIsToday = computed(
  () => selectedDay.value?.getTime() === shiftLocalDay(new Date(), 0).getTime(),
);
const canPageForward = computed(
  () => selectedDay.value !== null && selectedDay.value < shiftLocalDay(new Date(), 0),
);
const selectedDayLabel = computed(() => {
  const day = selectedDay.value;
  if (!day) return "";
  if (selectedDayIsToday.value) return "Today";
  if (day.getTime() === shiftLocalDay(new Date(), -1).getTime()) return t("yesterday");
  return day.toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    ...(day.getFullYear() !== new Date().getFullYear() ? { year: "numeric" } : {}),
  });
});

function selectDay(day: Date | null) {
  selectedDay.value = day;
  dayCalendarRef.value?.hide();
  filterMenuDismissed.value = true;
  tagMenuConversationSnapshot.value = null;
}

function pageDay(offset: -1 | 1) {
  selectDay(shiftLocalDay(selectedDay.value ?? new Date(), offset));
}

const searchTooltip = computed(() => ({
  value: t("searchConversations"),
  disabled: window.matchMedia("(hover: none), (pointer: coarse)").matches,
}));
const queryEditorRef = ref<InstanceType<typeof ConversationQueryEditor> | null>(null);
const activeStructuredEdit = ref<ActiveStructuredQueryEdit | null>(null);
const searchResults = ref<ConversationWithState[] | null>(null);
const searching = ref(false);
const searchError = ref<string | null>(null);
const enhancing = ref(false);
const enhanceError = ref<string | null>(null);
let enhanceController: AbortController | null = null;
const enhanceDisabled = computed(() => enhancing.value || searching.value || searchResults.value === null || !!searchError.value || !searchText.value.trim());
function stopEnhancement() {
  enhanceController?.abort();
  enhanceController = null;
  enhancing.value = false;
}
function clearEnhancement() { stopEnhancement(); enhanceError.value = null; }
async function openEnhancement() {
  if (enhanceDisabled.value) return;
  emit("close");
  const controller = new AbortController();
  enhanceController = controller;
  enhanceError.value = null;
  enhancing.value = true;
  try {
    const result = await api.startSearchChat(searchText.value.trim(), showArchived.value, controller.signal, displayedConversations.value);
    if (!controller.signal.aborted) emit("select-conversation", result.conversation);
  } catch (err) {
    if (!controller.signal.aborted) {
      enhanceError.value = err instanceof Error ? err.message : String(err);
      emit("open");
    }
  } finally {
    if (enhanceController === controller) { enhanceController = null; enhancing.value = false; }
  }
}
watch([searchQuery, showArchived], clearEnhancement);
let searchTimeout: ReturnType<typeof setTimeout> | null = null;
let searchSeq = 0;
let searchController: AbortController | null = null;
const SEARCH_DEBOUNCE_MS = 80;
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
    return stored === "cwd" ||
      stored === "git_repo" ||
      stored === "tag" ||
      stored === "participants" ||
      stored === "daily"
      ? stored
      : "none";
  })(),
);
if (groupBy.value === "daily") selectedDay.value = shiftLocalDay(new Date(), 0);
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
const parsedQuery = computed(() =>
  parseSearchQuery(
    activeStructuredEdit.value
      ? omitStructuredQueryEdit(searchQuery.value, activeStructuredEdit.value)
      : searchQuery.value,
  ),
);
const selectedTags = computed(() => parsedQuery.value.tags);
const selectedUsers = computed(() => parsedQuery.value.users);
const searchText = computed(() => parsedQuery.value.text);
const activeTagPrefix = computed(() =>
  activeStructuredEdit.value
    ? activeStructuredEdit.value.kind === "tag"
      ? activeStructuredEdit.value.prefix
      : null
    : parsedQuery.value.activeTagPrefix,
);
const activeUserPrefix = computed(() =>
  activeStructuredEdit.value
    ? activeStructuredEdit.value.kind === "user"
      ? activeStructuredEdit.value.prefix
      : null
    : parsedQuery.value.activeUserPrefix,
);
const currentUserEmail = window.__SHELLEY_INIT__?.user_email;
const activeParticipantConversations = computed(() =>
  props.conversations.filter((conversation) => !conversation.parent_conversation_id),
);
// Multiplayer capability is read off the active list alone: archived rows and
// search hits arrive asynchronously and would otherwise flip the participant
// UI (and the seed watcher below) on and off as the user browses. Without a
// current email, a conversation that itself has several participants is
// evidence enough to show badges and the user picker.
const multipleParticipantsAvailable = computed(
  () =>
    hasOtherParticipant(activeParticipantConversations.value, currentUserEmail) ||
    hasMultiParticipantConversation(activeParticipantConversations.value),
);
// "Group by participants" is offered only when some conversation actually has
// several participants; a list of disjoint single-user conversations has
// nothing to group by.
const groupByOptions = computed<GroupBy[]>(() =>
  hasMultiParticipantConversation(activeParticipantConversations.value)
    ? ["none", "cwd", "git_repo", "tag", "daily", "participants"]
    : ["none", "cwd", "git_repo", "tag", "daily"],
);
let participantDefaultsSeeded = false;
watch(
  multipleParticipantsAvailable,
  (available) => {
    if (!available || participantDefaultsSeeded || !currentUserEmail?.trim()) return;
    participantDefaultsSeeded = true;
    if (searchQuery.value !== "") return;
    searchQuery.value = `${formatUserTerm(currentUserEmail)} `;
  },
  { immediate: true },
);
const structuredFiltersActive = computed(
  () =>
    selectedUsers.value.length > 0 ||
    parsedQuery.value.includeUnattributed ||
    queryHasTagFilter(parsedQuery.value),
);
const searchToolbarActive = computed(
  () => searchOpen.value || structuredFiltersActive.value || searchText.value.trim() !== "",
);
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
  if (searchText.value.trim()) {
    searchResults.value = null;
    void refreshSearchResults();
  }
});

async function fetchSearchResults(query: string, seq: number) {
  searchController?.abort();
  const controller = new AbortController();
  searchController = controller;
  try {
    const results = await api.searchConversationsFTS(query, controller.signal, showArchived.value);
    if (!controller.signal.aborted && seq === searchSeq) searchResults.value = results;
  } catch (error) {
    if (controller.signal.aborted || seq !== searchSeq) return;
    searchResults.value = [];
    searchError.value = error instanceof Error ? error.message : String(error);
  } finally {
    if (searchController === controller) searchController = null;
    if (seq === searchSeq) searching.value = false;
  }
}

// Debounced FTS search within the current archive view. Only the free
// text goes to the server; `tag:` terms are applied client-side.
watch(searchText, () => {
  searchResults.value = null;
  searchError.value = null;
  searchController?.abort();
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
  searchTimeout = setTimeout(() => void fetchSearchResults(query, seq), SEARCH_DEBOUNCE_MS);
});

async function refreshSearchResults() {
  const query = searchText.value.trim();
  if (!query) return;
  if (searchTimeout) {
    clearTimeout(searchTimeout);
    searchTimeout = null;
  }
  searchResults.value = null;
  searchError.value = null;
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

watch(
  () => props.messageSentTrigger,
  () => {
    if (groupBy.value === "daily") selectDay(shiftLocalDay(new Date(), 0));
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
  // Guard against clock skew / future timestamps, which would otherwise
  // produce nonsense like "-1 days ago".
  const diffMs = Math.max(0, now.getTime() - date.getTime());
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

// --- Row actions ---
function conversationActions(conversation: Conversation): MenuItem[] {
  const id = conversation.conversation_id;
  return [
    {
      label: t("archive"),
      icon: "pi pi-inbox",
      command: ({ originalEvent }) => void handleArchive(originalEvent as MouseEvent, id),
    },
    {
      label: t("rename"),
      icon: "pi pi-pencil",
      command: ({ originalEvent }) => handleStartRename(originalEvent as MouseEvent, conversation),
    },
    {
      label: t("editTags"),
      icon: "pi pi-tag",
      command: ({ originalEvent }) => handleOpenTagEditor(originalEvent as MouseEvent, id),
    },
  ];
}
// One right-click menu serves every row; opening it on another row moves it.
const contextMenuRef = ref<InstanceType<typeof ContextMenu> | null>(null);
const contextMenuId = ref<string | null>(null);
// Kept after hiding so the items don't vanish while the menu fades out.
const contextMenuItems = shallowRef<MenuItem[]>([]);
function openContextMenu(e: MouseEvent, conversation: Conversation) {
  // PrimeVue closes it only on a click. A right-click elsewhere should too;
  // one on a row never gets here, as show() stops its propagation.
  document.addEventListener("contextmenu", hideContextMenu);
  contextMenuItems.value = conversationActions(conversation);
  contextMenuId.value = conversation.conversation_id;
  contextMenuRef.value?.show(e);
}
function hideContextMenu() {
  if (contextMenuId.value !== null) contextMenuRef.value?.hide();
}
function onContextMenuHide() {
  contextMenuId.value = null;
  document.removeEventListener("contextmenu", hideContextMenu);
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
  selectDay(value === "daily" ? shiftLocalDay(new Date(), 0) : null);
  localStorage.setItem("shelley-group-by", value);
  collapsedGroups.value = new Set();
}
function groupByLabel(value: GroupBy): string {
  const labels: Record<GroupBy, string> = {
    none: t("noGrouping"),
    daily: "Daily",
    cwd: t("directory"),
    git_repo: t("gitRepo"),
    tag: t("tags"),
    participants: t("participants"),
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
    queryEditorRef.value?.finishStructuredEdit();
    searchOpen.value = false;
  } else {
    searchOpen.value = true;
    void nextTick(() => queryEditorRef.value?.focusEnd());
  }
}

function clearSearchText() {
  const edit = activeStructuredEdit.value;
  queryEditorRef.value?.finishStructuredEdit();
  searchQuery.value = clearConversationQueryText(searchQuery.value, edit);
  void nextTick(() => queryEditorRef.value?.focusEnd());
}

function clearSearch() {
  queryEditorRef.value?.finishStructuredEdit();
  searchQuery.value = "";
  void nextTick(() => queryEditorRef.value?.focusEnd());
}

function startTagFilter() {
  filterMenuDismissed.value = false;
  searchQuery.value = startFilterTermInQuery(searchQuery.value, "tag:");
  void nextTick(() => queryEditorRef.value?.focusEnd());
}

function startUserFilter() {
  filterMenuDismissed.value = false;
  searchQuery.value = startFilterTermInQuery(searchQuery.value, "user:");
  void nextTick(() => queryEditorRef.value?.focusEnd());
}

function onSearchKeyDown(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  const offers = tagMenuOpen.value
    ? visibleOfferedTags.value
    : userMenuOpen.value
      ? visibleOfferedUsers.value
      : [];
  // While a filter dropdown is up it owns the arrows and Enter.
  if (filterMenuOpen.value && offers.length > 0) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      highlightIndex.value = (highlightIndex.value + 1) % offers.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      highlightIndex.value = (highlightIndex.value - 1 + offers.length) % offers.length;
      return;
    }
    const activeEditRaw = activeStructuredEdit.value
      ? searchQuery.value.slice(activeStructuredEdit.value.start, activeStructuredEdit.value.end)
      : "";
    const quotedTag =
      tagMenuOpen.value &&
      (/^tag:"/i.test(activeEditRaw) || /(?:^|\s)tag:"[^"]*$/i.test(searchQuery.value));
    const spaceCompletes =
      e.key === " " &&
      !quotedTag &&
      (userMenuOpen.value
        ? (activeUserPrefix.value?.trim().length ?? 0) > 0
        : (activeTagPrefix.value?.trim().length ?? 0) > 0);
    if (e.key === "Enter" || e.key === "Tab" || spaceCompletes) {
      e.preventDefault();
      const pick = offers[highlightIndex.value];
      if (pick) {
        if (userMenuOpen.value) chooseUser(pick.term);
        else chooseTerm(pick.term);
      }
      return;
    }
  }
  if (e.key === "Escape") {
    e.preventDefault();
    // Escape peels one layer at a time: dropdown, visible input, then box.
    if (filterMenuOpen.value) {
      filterMenuDismissed.value = true;
    } else if (
      searchText.value ||
      activeTagPrefix.value !== null ||
      activeUserPrefix.value !== null
    ) {
      clearSearchText();
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
const stableTopLevelConversations = computed(() => {
  perfCount("drawer.topLevel");
  resetOrderRefsForResort();
  void resortKey.value;
  const sorted = sortConversationsByBucket(
    props.conversations.filter((c) => !c.parent_conversation_id),
  );
  const { items, order } = applyStableOrder(sorted, topOrder);
  topOrder = order;
  return items;
});
const topLevelConversations = computed(() =>
  filterConversationsByQuery(
    filterConversationsByParticipantQuery(
      filterConversationsByDay(stableTopLevelConversations.value, selectedDay.value),
      selectedUsers.value,
      parsedQuery.value.includeUnattributed,
    ),
    parsedQuery.value,
  ),
);

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
  return filterConversationsByQuery(
    filterConversationsByParticipantQuery(
      filterConversationsByDay(items, selectedDay.value),
      selectedUsers.value,
      parsedQuery.value.includeUnattributed,
    ),
    parsedQuery.value,
  );
});

// "Searching" means the FTS query is non-empty. A query of only `tag:` terms
// is a filter over the normal list, not a search — so the list keeps its
// grouping and ordering instead of collapsing into flat search results.
const isSearching = computed(() => searchText.value.trim().length > 0);

const calendarDayCounts = computed(() => {
  const pool = isSearching.value
    ? (searchResults.value ?? [])
    : showArchived.value
      ? archivedConversations.value
      : props.conversations.filter((c) => !c.parent_conversation_id);
  const matches = filterConversationsByQuery(
    filterConversationsByParticipantQuery(
      pool,
      selectedUsers.value,
      parsedQuery.value.includeUnattributed,
    ),
    parsedQuery.value,
  );
  const counts = new Map<string, number>();
  for (const conversation of matches) {
    const key = new Date(conversation.updated_at).toDateString();
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return counts;
});

function calendarDayCount(date: { year: number; month: number; day: number }): number {
  return calendarDayCounts.value.get(new Date(date.year, date.month, date.day).toDateString()) ?? 0;
}

// Keep the rows behind an open tag picker stable. Removing a selected tag can
// broaden a tiny result set into hundreds of conversations; mounting those
// rows while the picker is still open makes the chip click visibly stall.
// The picker options still recompute immediately, while the conversation list
// applies the broader filter once the picker closes or a replacement is chosen.
const tagMenuConversationSnapshot = shallowRef<(Conversation | ConversationWithState)[] | null>(
  null,
);
const displayedConversations = computed<(Conversation | ConversationWithState)[]>(() => {
  if (filterMenuOpen.value && tagMenuConversationSnapshot.value) {
    return tagMenuConversationSnapshot.value;
  }
  // Search results apply the same predicate stack as active and archived
  // rows. Those lists establish stable order before filtering.
  if (isSearching.value)
    return filterConversationsByQuery(
      filterConversationsByParticipantQuery(
        filterConversationsByDay(searchResults.value ?? [], selectedDay.value),
        selectedUsers.value,
        parsedQuery.value.includeUnattributed,
      ),
      parsedQuery.value,
    );
  return showArchived.value ? stableArchivedConversations.value : topLevelConversations.value;
});
const displayedConversationIds = computed(
  () => new Set(displayedConversations.value.map((conversation) => conversation.conversation_id)),
);
const cacheActiveTopLevelRows = computed(
  () =>
    !showArchived.value &&
    !isSearching.value &&
    (groupBy.value === "none" || groupBy.value === "daily"),
);

// The list the tag dropdown describes: whichever list is on screen,
// participant-scoped but unfiltered by tags, so counts match the visible
// participant scope.
const tagFilterPool = computed<ConversationWithParticipants[]>(() => {
  if (isSearching.value)
    return filterConversationsByParticipantQuery(
      filterConversationsByDay(searchResults.value ?? [], selectedDay.value),
      selectedUsers.value,
      parsedQuery.value.includeUnattributed,
    );
  const pool = showArchived.value
    ? archivedConversations.value
    : props.conversations.filter((c) => !c.parent_conversation_id);
  return filterConversationsByParticipantQuery(
    filterConversationsByDay(pool, selectedDay.value),
    selectedUsers.value,
    parsedQuery.value.includeUnattributed,
  );
});

// The same on-screen source before participant scoping: the rows the
// participant facet actually decides on (drafts and subagents pass through
// it unconditionally). Identifies when that filter, rather than a text/tag
// miss, caused an empty state, and sizes the user picker's offers.
const participantFilterPool = computed<ConversationWithParticipants[]>(() => {
  const pool = isSearching.value
    ? (searchResults.value ?? [])
    : showArchived.value
      ? archivedConversations.value
      : props.conversations.filter((c) => !c.parent_conversation_id && !c.is_draft);
  return filterConversationsByDay(pool, selectedDay.value);
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
const visibleOfferedTags = computed(() => {
  const typed = activeTagPrefix.value ?? "";
  return rankExactMatchFirst(
    currentOfferedTags.value.filter((o) => tagMatchesQuery(o.tag, typed)),
    (o) => (o.untagged ? "" : o.tag),
    typed,
  );
});

interface UserOffer {
  email: string;
  count: number;
  term: string;
}
const currentOfferedUsers = computed<UserOffer[]>(() => {
  const selected = new Set(selectedUsers.value.map((email) => email.trim().toLowerCase()));
  const candidates = new Map<string, string>();
  const pool = filterConversationsByQuery(participantFilterPool.value, parsedQuery.value);
  for (const conversation of pool) {
    for (const participant of conversation.participants ?? []) {
      const email = participant.email.trim();
      const folded = email.toLowerCase();
      if (!folded || selected.has(folded) || candidates.has(folded)) continue;
      candidates.set(folded, email);
    }
  }
  const offers: UserOffer[] = [];
  for (const email of candidates.values()) {
    const count = filterConversationsByParticipantQuery(
      pool,
      [...selectedUsers.value, email],
      false,
    ).length;
    if (count > 0) offers.push({ email, count, term: formatUserTerm(email) });
  }
  return offers.sort((a, b) => b.count - a.count || a.email.localeCompare(b.email));
});
const visibleOfferedUsers = computed(() => {
  const typed = activeUserPrefix.value ?? "";
  const needle = typed.trim().toLowerCase();
  return rankExactMatchFirst(
    currentOfferedUsers.value.filter((offer) => offer.email.toLowerCase().includes(needle)),
    (offer) => offer.email,
    typed,
  );
});
const addUserFilterTooltip = computed(() => ({
  value: "All users added",
  disabled: currentOfferedUsers.value.length > 0,
}));

// A dropdown is open whenever the caret sits in a `tag:` or `user:` term.
// Dismissing it latches until the active term changes.
const filterMenuDismissed = ref(false);
const tagMenuOpen = computed(
  () => searchOpen.value && activeTagPrefix.value !== null && !filterMenuDismissed.value,
);
const userMenuOpen = computed(
  () => searchOpen.value && activeUserPrefix.value !== null && !filterMenuDismissed.value,
);
const filterMenuOpen = computed(() => tagMenuOpen.value || userMenuOpen.value);
watch([activeTagPrefix, activeUserPrefix], () => {
  filterMenuDismissed.value = false;
  highlightIndex.value = 0;
});
function onTagMenuOutside(e: MouseEvent) {
  if (searchWrapRef.value && !searchWrapRef.value.contains(e.target as Node)) {
    filterMenuDismissed.value = true;
  }
}
watch(filterMenuOpen, (open) => {
  if (open) {
    tagMenuConversationSnapshot.value = [...displayedConversations.value];
    document.addEventListener("mousedown", onTagMenuOutside);
  } else {
    tagMenuConversationSnapshot.value = null;
    document.removeEventListener("mousedown", onTagMenuOutside);
  }
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
const emptiedByParticipantFilter = computed(
  () =>
    (selectedUsers.value.length > 0 || parsedQuery.value.includeUnattributed) &&
    displayedConversations.value.length === 0 &&
    filterConversationsByQuery(participantFilterPool.value, parsedQuery.value).length > 0,
);

// Keep the keyboard highlight in range as the offered set narrows.
watch(visibleOfferedTags, () => {
  if (highlightIndex.value >= visibleOfferedTags.value.length) highlightIndex.value = 0;
});
watch(visibleOfferedUsers, () => {
  if (highlightIndex.value >= visibleOfferedUsers.value.length) highlightIndex.value = 0;
});

// Completing a middle edit rewrites its exact range. A trailing partial keeps
// the existing behavior of adding a space for the next term.
function chooseTerm(term: string) {
  if (queryEditorRef.value?.completeStructuredTerm(term)) {
    highlightIndex.value = 0;
    return;
  }
  searchQuery.value = completeTermInQuery(searchQuery.value, term);
  highlightIndex.value = 0;
  void nextTick(() => queryEditorRef.value?.focusEnd());
}

function chooseUser(term: string) {
  if (queryEditorRef.value?.completeStructuredTerm(term)) {
    const withoutUnattributed = removeUnattributedFromQuery(searchQuery.value);
    const removedUnattributed = withoutUnattributed !== searchQuery.value;
    searchQuery.value = withoutUnattributed;
    highlightIndex.value = 0;
    if (removedUnattributed) void nextTick(() => queryEditorRef.value?.focusEnd());
    return;
  }
  searchQuery.value = completeTermInQuery(removeUnattributedFromQuery(searchQuery.value), term);
  highlightIndex.value = 0;
  void nextTick(() => queryEditorRef.value?.focusEnd());
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

function clearParticipantFilter() {
  let next = searchQuery.value;
  for (const user of selectedUsers.value) next = removeUserFromQuery(next, user);
  next = removeUnattributedFromQuery(next);
  searchQuery.value = next.trim() === "" ? "" : next;
}

interface Group {
  label: string;
  conversations: ConversationWithState[];
}
const groupedConversations = computed<[string, Group][] | null>(() => {
  if (
    groupBy.value === "none" ||
    groupBy.value === "daily" ||
    showArchived.value ||
    isSearching.value
  )
    return null;
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
    } else if (groupBy.value === "participants") {
      key = participantGroupKey(conv);
    }
    if (!key) {
      ungrouped.push(conv);
      continue;
    }
    let group = groups.get(key);
    if (!group) {
      const label =
        groupBy.value === "tag"
          ? tagGroupLabel(conv)
          : groupBy.value === "participants"
            ? participantGroupLabel(key)
            : formatCwdForDisplay(key) || key;
      group = { label, conversations: [] };
      groups.set(key, group);
    }
    group.conversations.push(conv);
  }

  // Rows within a group keep their own stable order (new arrivals on top,
  // reset by "re-sort"), independent of where they fall in the global list.
  const nextGroupOrder: Record<string, string[]> = {};
  const stableGroupRows = (key: string, rows: ConversationWithState[]) => {
    const { items, order } = applyStableOrder(
      sortConversationsByBucket(rows),
      groupOrder[key] || [],
    );
    nextGroupOrder[key] = order;
    return items;
  };
  for (const [key, group] of groups)
    group.conversations = stableGroupRows(key, group.conversations);

  // Tag and participant groups sort alphabetically by their tuple, so a
  // group's position is predictable from its name (participant groups the
  // current user belongs to come first). The recency-ordered modes route
  // through applyStableKeyOrder instead; that pass is skipped here because it
  // pins seen keys to old positions, stranding a new group at the top of an
  // alphabetical list.
  let sorted: [string, Group][];
  if (groupBy.value === "tag") {
    sorted = [...groups.entries()].sort(([a], [b]) => compareTagGroupKeys(a, b));
    groupKeysOrder = sorted.map(([k]) => k);
  } else if (groupBy.value === "participants") {
    sorted = [...groups.entries()].sort(([a], [b]) =>
      compareParticipantGroupKeys(a, b, currentUserEmail),
    );
    groupKeysOrder = sorted.map(([k]) => k);
  } else {
    const allGroups = new Map<string, ConversationWithState[]>();
    for (const conv of stableTopLevelConversations.value) {
      const key =
        groupBy.value === "cwd"
          ? conv.cwd || null
          : conv.git_worktree_root || conv.git_repo_root || null;
      if (!key) continue;
      let group = allGroups.get(key);
      if (!group) {
        group = [];
        allGroups.set(key, group);
      }
      group.push(conv);
    }
    const desiredKeys = [...allGroups.entries()]
      .sort((a, b) => maxBucket(b[1]) - maxBucket(a[1]))
      .map(([k]) => k);
    const stableKeys = applyStableKeyOrder(desiredKeys, groupKeysOrder);
    groupKeysOrder = stableKeys;
    sorted = stableKeys.filter((k) => groups.has(k)).map((k) => [k, groups.get(k)!]);
  }

  if (ungrouped.length > 0) {
    const ungroupedLabel =
      groupBy.value === "tag"
        ? t("untagged")
        : groupBy.value === "participants"
          ? t("unattributed")
          : t("other");
    sorted.push([
      "__ungrouped__",
      { label: ungroupedLabel, conversations: stableGroupRows("__ungrouped__", ungrouped) },
    ]);
  }
  groupOrder = nextGroupOrder;
  return sorted;
});

// The hover title for a truncated group heading: the full label for tag
// groups (their key is NUL-joined, not for eyes), the untruncated path else.
function groupTitle(key: string, group: Group): string | undefined {
  if (key === "__ungrouped__") return undefined;
  return groupBy.value === "tag" || groupBy.value === "participants" ? group.label : key;
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
  stopEnhancement();
  searchController?.abort();
  if (searchTimeout) clearTimeout(searchTimeout);
  if (copyTimeout) clearTimeout(copyTimeout);
  document.removeEventListener("mousedown", onGroupMenuOutside);
  document.removeEventListener("mousedown", onTagMenuOutside);
  document.removeEventListener("mousedown", onPendingDeleteOutside);
  document.removeEventListener("mousedown", onTagEditorOutside);
  document.removeEventListener("contextmenu", hideContextMenu);
});

// Share all row-relevant state + handlers with ConversationDrawerRow via inject.
provide(DrawerCtxKey, {
  t,
  currentConversationId: computed(() => props.currentConversationId),
  searchText,
  showParticipantBadges: multipleParticipantsAvailable,
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
  workingIds: computed(() => workingConversationIds(props.conversations)),
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
  selectedUsers,
  toggleTagFilter,
  formatDate,
  formatCwdForDisplay,
  handleModifiedClick,
  handleAuxClick,
  selectConversation: (c: Conversation) => emit("select-conversation", c),
  toggleSubagents,
  handleStartRename,
  conversationActions,
  openContextMenu,
  contextMenuId,
  handleRename,
  handleRenameKeyDown,
  handleAddTag,
  matchTagOffers,
  handleRemoveTag,
  handleUnarchive,
  handleCopyGitHash,
  handleDeleteClick,
  handleConfirmDelete,
  handleCancelDelete,
});
</script>


<style scoped>
button.drawer-search-shelley { border: 1px solid var(--border); border-radius: 6px; padding: 5px 10px; background: var(--bg-secondary); color: var(--text-primary); font-weight: 500; }
button.drawer-search-shelley:hover:not(:disabled) { border-color: var(--link-color); background: var(--bg-base); }
button.drawer-search-shelley:disabled { opacity: .5; }
</style>
