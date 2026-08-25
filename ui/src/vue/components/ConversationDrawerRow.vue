<!-- Row child of ConversationDrawer.vue. Renders one conversation-item (plus
     its expanded subagents). Split out of the parent only to avoid duplicating
     ~200 lines of markup; it has multiple root nodes so it renders inline with
     no wrapper element, preserving the .conversation-group > .conversation-item
     DOM contract the grouping e2e test relies on. All state/handlers come from
     the DrawerCtx inject. Mirrors renderConversationItem in the React source. -->
<template>
  <div
    :class="`conversation-item ${isActive ? 'active' : ''}${isNew ? ' conversation-item-enter' : ''}`"
    :data-conversation-id="conversation.conversation_id"
    style="cursor: pointer"
    @click="onRowClick"
    @auxclick="ctx.handleAuxClick($event, conversation)"
  >
    <div class="drawer-conversation-item-flex-container">
      <div class="drawer-conversation-header-row">
        <div class="drawer-conversation-item-flex-container">
          <input
            v-if="ctx.editingId.value === conversation.conversation_id"
            ref="renameInput"
            type="text"
            :value="ctx.editingSlug.value"
            class="conversation-title drawer-rename-input"
            @input="ctx.editingSlug.value = ($event.target as HTMLInputElement).value"
            @blur="ctx.handleRename(conversation.conversation_id)"
            @keydown="ctx.handleRenameKeyDown($event, conversation.conversation_id)"
            @click.stop
          />
          <div v-else-if="isDraft" class="conversation-title conversation-title-draft">
            {{ ctx.draftLabels.value[conversation.conversation_id] || "draft" }}
          </div>
          <div v-else class="conversation-title">
            <em v-if="!conversation.slug">untitled</em>
            <template v-else>{{ conversation.slug }}</template>
          </div>
        </div>
        <span
          v-if="convState.working"
          class="working-indicator drawer-working-indicator"
          :title="ctx.t('agentIsWorking')"
        />
        <div
          v-if="!isDraft && !itemArchived"
          class="conversation-actions drawer-actions-row drawer-actions-top"
        >
          <Button
            class="btn-icon-sm drawer-actions-trigger"
            text
            severity="secondary"
            size="small"
            v-tooltip.top="{ value: ctx.t('actions'), showDelay: 400, hideDelay: 150 }"
            :aria-label="ctx.t('actions')"
            aria-haspopup="menu"
            :aria-expanded="actionsMenuOpen"
            @click.stop="toggleActionsMenu"
          >
            <OverflowDotsIcon class="drawer-actions-trigger-icon" />
          </Button>
          <Menu
            ref="actionsMenuRef"
            :model="actionsMenuItems"
            popup
            :aria-label="ctx.t('actions')"
            :pt="{
              root: { class: 'drawer-actions-menu', 'data-testid': 'conversation-actions-menu' },
            }"
            @show="actionsMenuOpen = true"
            @hide="actionsMenuOpen = false"
          />
        </div>
      </div>

      <!-- Tags / tag editor -->
      <div
        v-if="tagsEditing || conversationTags.length > 0"
        :ref="setTagEditorRefMaybe"
        :class="`conversation-tags${tagsEditing ? ' conversation-tags-editing' : ''}`"
        @click="tagsEditing ? $event.stopPropagation() : undefined"
      >
        <template v-for="tag in conversationTags" :key="tag">
          <span v-if="tagsEditing" class="conversation-tag conversation-tag-removable">
            <span class="conversation-tag-hash">#</span>
            <span class="conversation-tag-text">{{ tag }}</span>
            <button
              type="button"
              class="conversation-tag-remove"
              :aria-label="`${ctx.t('removeTag')} ${tag}`"
              v-tooltip.top="ctx.t('removeTag')"
              @click="ctx.handleRemoveTag(conversation, tag)"
            >
              ×
            </button>
          </span>
          <!-- Not editing: clicking a chip toggles it in the drawer's tag
               filter. While the editor is open the chips mean "remove". -->
          <button
            v-else
            type="button"
            :class="`conversation-tag conversation-tag-filterable${tagFiltered(tag) ? ' conversation-tag-filter-on' : ''}`"
            :title="`#${tag}`"
            :aria-pressed="tagFiltered(tag)"
            data-testid="conversation-tag-chip"
            :data-tag="tag"
            @click="onTagChipClick($event, tag)"
          >
            <span class="conversation-tag-hash">#</span>{{ tag }}
          </button>
        </template>
        <form v-if="tagsEditing" class="conversation-tag-inline-form" @submit.prevent="onTagSubmit">
          <span class="conversation-tag-hash">#</span>
          <input
            ref="tagInput"
            type="text"
            :value="ctx.tagInput.value"
            :placeholder="ctx.t('addTagPlaceholder')"
            class="conversation-tag-inline-input"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            role="combobox"
            aria-autocomplete="list"
            :aria-expanded="tagMenuOpen"
            @input="onTagInput"
            @keydown="onTagInputKeyDown"
            @focus="onTagInputFocus"
            @blur="onTagInputBlur"
          />
          <!-- Suggestion dropdown, teleported out of the drawer's clipping
               overflow and pinned under the input. Mirrors the search box's
               `tag:` menu: substring matches, arrow/Enter selection, counts. -->
          <Teleport to="body">
            <div
              v-if="tagMenuOpen"
              class="tag-filter-menu tag-editor-menu"
              :style="tagMenuStyle"
              data-testid="tag-editor-menu"
              role="listbox"
              @mousedown.prevent
            >
              <div class="tag-filter-options scrollable">
                <button
                  v-for="(offer, i) in tagOffers"
                  :key="offer.tag"
                  type="button"
                  role="option"
                  :aria-selected="i === tagHighlightIndex"
                  :class="`tag-filter-option${i === tagHighlightIndex ? ' highlighted' : ''}`"
                  data-testid="tag-editor-option"
                  :data-tag="offer.tag"
                  @mousemove="tagHighlightIndex = i"
                  @click="chooseTagOffer(offer.tag)"
                >
                  <span class="tag-filter-option-name">
                    <span class="conversation-tag-hash">#</span>{{ offer.tag }}
                  </span>
                  <span class="tag-filter-option-count">{{ offer.count }}</span>
                </button>
              </div>
            </div>
          </Teleport>
        </form>
      </div>

      <!-- Preview / snippet -->
      <div
        v-if="convState.search_snippet"
        class="conversation-preview conversation-snippet"
        :title="stripSnippetMarks(convState.search_snippet)"
      >
        <template v-for="(seg, i) in renderSnippetSegments(convState.search_snippet)" :key="i">
          <mark v-if="seg.mark" class="conversation-snippet-mark">{{ seg.text }}</mark>
          <template v-else>{{ seg.text }}</template>
        </template>
      </div>
      <div
        v-else-if="isDraft"
        class="conversation-preview"
        :title="conversation.draft?.trim() || undefined"
      >
        {{ conversation.draft?.trim() || "\u00a0" }}
      </div>
      <div v-else class="conversation-preview" :title="convState.preview || undefined">
        {{ convState.preview || "\u00a0" }}
      </div>

      <div class="conversation-meta">
        <span class="conversation-date">{{ ctx.formatDate(conversation.updated_at) }}</span>
        <span
          v-if="conversation.cwd && ctx.groupBy.value !== 'cwd'"
          class="conversation-cwd"
          :title="conversation.cwd"
        >
          {{ ctx.formatCwdForDisplay(conversation.cwd) }}
        </span>
        <!-- Terminal count. Only shown when the conversation has more than
             one terminal pinned to it: a single terminal is the ordinary
             case and not worth a badge. -->
        <span
          v-if="!isDraft && !itemArchived && terminalCount > 1"
          class="conversation-terminal-count"
          v-tooltip.top="`${terminalCount} ${ctx.t('terminalsPinnedHere')}`"
          :aria-label="`${terminalCount} ${ctx.t('terminalsPinnedHere')}`"
        >
          <svg
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            class="conversation-terminal-count-icon"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M8 9l3 3-3 3m5 0h3M4 5h16a1 1 0 011 1v12a1 1 0 01-1 1H4a1 1 0 01-1-1V6a1 1 0 011-1z"
            />
          </svg>
          {{ terminalCount }}
        </span>
        <button
          v-if="!isDraft && !itemArchived && hasSubagents"
          class="subagent-count-badge"
          v-tooltip.top="subagentBadgeTooltip"
          :aria-label="isExpanded ? ctx.t('collapseSubagents') : ctx.t('expandSubagents')"
          @click="ctx.toggleSubagents($event, conversation.conversation_id)"
        >
          <span
            v-if="runningSubagentCount > 0"
            class="working-indicator"
            data-testid="subagent-badge-running"
            aria-hidden="true"
          />
          <span class="drawer-subagent-count-badge-text">{{ subagentBadgeText }}</span>
          <svg
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            :class="`drawer-subagent-chevron ${isExpanded ? 'drawer-subagent-chevron-expanded' : 'drawer-subagent-chevron-collapsed'}`"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M9 5l7 7-7 7"
            />
          </svg>
        </button>
        <div v-if="isDraft" class="conversation-actions drawer-actions-row">
          <DeleteButton :conversation-id="conversation.conversation_id" />
        </div>
      </div>

      <div
        v-if="convState.git_commit"
        :class="`conversation-git drawer-git-info ${isActive ? 'drawer-git-info-active' : ''}`"
      >
        <span
          v-tooltip.top="`Click to copy ${convState.git_commit}`"
          :class="`drawer-git-hash ${ctx.copiedConvId.value === conversation.conversation_id ? 'drawer-git-hash-copied' : ''}`"
          @click="
            ctx.handleCopyGitHash($event, convState.git_commit!, conversation.conversation_id)
          "
        >
          {{
            ctx.copiedConvId.value === conversation.conversation_id
              ? "copied!".padEnd(convState.git_commit!.length, "\u00a0")
              : convState.git_commit
          }}
        </span>
        <span
          v-if="convState.git_subject"
          :title="convState.git_subject"
          class="drawer-git-subject"
        >
          {{ convState.git_subject }}
        </span>
      </div>
    </div>

    <div v-if="itemArchived" class="conversation-actions drawer-actions-row-offset">
      <Button
        class="btn-icon-sm"
        text
        severity="secondary"
        size="small"
        v-tooltip.top="ctx.t('restore')"
        :aria-label="ctx.t('restore')"
        @click="ctx.handleUnarchive($event, conversation.conversation_id)"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="drawer-icon-size">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
          />
        </svg>
      </Button>
      <DeleteButton :conversation-id="conversation.conversation_id" />
    </div>
  </div>

  <!-- Subagents -->
  <div
    v-if="!itemArchived && isExpanded && conversationSubagents.length > 0"
    class="subagent-list drawer-subagent-list"
  >
    <div
      v-for="sub in conversationSubagents"
      :key="sub.conversation_id"
      :class="`conversation-item subagent-item drawer-subagent-item-style ${sub.conversation_id === ctx.currentConversationId.value ? 'active' : ''}${ctx.seenIds.value !== null && !ctx.seenIds.value.has(sub.conversation_id) ? ' conversation-item-enter' : ''}`"
      @click="onSubClick($event, sub)"
      @auxclick="ctx.handleAuxClick($event, sub)"
    >
      <div class="drawer-conversation-item-flex-container">
        <div class="drawer-conversation-header-row">
          <div class="drawer-conversation-item-flex-container">
            <div class="conversation-title">
              <em v-if="!sub.slug">untitled</em>
              <template v-else>{{ sub.slug }}</template>
            </div>
          </div>
          <span v-if="sub.working" class="working-indicator" :title="ctx.t('subagentIsWorking')" />
        </div>
        <div class="conversation-preview" :title="sub.preview || undefined">
          {{ sub.preview || "\u00a0" }}
        </div>
        <div class="conversation-meta">
          <span class="conversation-date drawer-subagent-date">{{
            ctx.formatDate(sub.updated_at)
          }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  computed,
  defineComponent,
  h,
  inject,
  nextTick,
  onBeforeUnmount,
  ref,
  watch,
  type VNode,
} from "vue";
import Button from "primevue/button";
import Menu from "primevue/menu";
import type { MenuItem } from "primevue/menuitem";
import OverflowDotsIcon from "./OverflowDotsIcon.vue";
import type { Conversation, ConversationWithState } from "../../types";
import { isImeComposing } from "../../utils/imeComposing";
import {
  DrawerCtxKey,
  parseTags,
  stripSnippetMarks,
  renderSnippetSegments,
} from "./conversationDrawerShared";
import { isTagSelected } from "../../utils/tagFilter";
import { perfCount } from "../../utils/perf";

const props = defineProps<{
  conversation: Conversation | ConversationWithState;
}>();

const ctx = inject(DrawerCtxKey)!;

const renameInput = ref<HTMLInputElement | null>(null);
const tagInput = ref<HTMLInputElement | null>(null);
const actionsMenuRef = ref<InstanceType<typeof Menu> | null>(null);
const actionsMenuOpen = ref(false);
const actionsMenuItems = computed<MenuItem[]>(() => [
  {
    label: ctx.t("archive"),
    icon: "pi pi-inbox",
    command: ({ originalEvent }) =>
      void ctx.handleArchive(originalEvent as MouseEvent, props.conversation.conversation_id),
  },
  {
    label: ctx.t("rename"),
    icon: "pi pi-pencil",
    command: ({ originalEvent }) =>
      ctx.handleStartRename(originalEvent as MouseEvent, props.conversation),
  },
  {
    label: ctx.t("editTags"),
    icon: "pi pi-tag",
    command: ({ originalEvent }) =>
      ctx.handleOpenTagEditor(originalEvent as MouseEvent, props.conversation.conversation_id),
  },
]);

const convState = computed(() => props.conversation as ConversationWithState);
const isDraft = computed(() => !!props.conversation.is_draft);
const isActive = computed(
  () => props.conversation.conversation_id === ctx.currentConversationId.value,
);
const conversationSubagents = computed<ConversationWithState[]>(() =>
  isDraft.value ? [] : ctx.subagentsByParent.value[props.conversation.conversation_id] || [],
);
const subagentCount = computed(() =>
  isDraft.value ? 0 : conversationSubagents.value.length || convState.value.subagent_count || 0,
);
const hasSubagents = computed(() => subagentCount.value > 0);
// Live terminals pinned to this conversation. Badged only when > 1.
const terminalCount = computed(
  () => ctx.terminalCounts.value[props.conversation.conversation_id] ?? 0,
);
// How many of this conversation's subagents are currently working. Drives
// the working ring + "running/total" split on the count badge.
const runningSubagentCount = computed(
  () => conversationSubagents.value.filter((s) => s.working).length,
);
const subagentBadgeText = computed(() =>
  runningSubagentCount.value > 0
    ? `${runningSubagentCount.value}/${subagentCount.value}`
    : `${subagentCount.value}`,
);
const subagentBadgeTooltip = computed(() => {
  const base = isExpanded.value ? ctx.t("hideSubagents") : ctx.t("showSubagents");
  return runningSubagentCount.value > 0
    ? `${base} (${runningSubagentCount.value} ${ctx.t("running")})`
    : base;
});
const isExpanded = computed(() =>
  ctx.expandedSubagents.value.has(props.conversation.conversation_id),
);
const itemArchived = computed(() => props.conversation.archived);
const isNew = computed(
  () =>
    !isDraft.value &&
    ctx.seenIds.value !== null &&
    !ctx.seenIds.value.has(props.conversation.conversation_id),
);
const conversationTags = computed(() => {
  perfCount("drawerRow.tags");
  return isDraft.value ? [] : parseTags(props.conversation);
});
const tagsEditing = computed(
  () => !isDraft.value && ctx.tagEditorId.value === props.conversation.conversation_id,
);
function tagFiltered(tag: string): boolean {
  return isTagSelected(ctx.selectedTags.value, tag);
}
function onTagChipClick(e: MouseEvent, tag: string) {
  // Don't let the click also select the conversation.
  e.stopPropagation();
  ctx.toggleTagFilter(tag);
}

function onRowClick(e: MouseEvent) {
  if (ctx.handleModifiedClick(e, props.conversation)) return;
  ctx.selectConversation(props.conversation);
}
function onSubClick(e: MouseEvent, sub: Conversation) {
  if (ctx.handleModifiedClick(e, sub)) return;
  ctx.selectConversation(sub);
}

function toggleActionsMenu(e: MouseEvent) {
  actionsMenuRef.value?.toggle(e);
}

// --- Tag editor dropdown ---------------------------------------------------
// The row's "Edit tags" input opens a suggestion dropdown, mirroring the
// search box's `tag:` menu: existing tags matching what's typed anywhere in
// their text (not just as a prefix), ranked best-first, each with its usage
// count. Arrow keys move the highlight; Enter commits the highlighted match
// (the best one, by default), or the typed text when it matches nothing.
const tagInputFocused = ref(false);
// Escape closes the menu without closing the editor; latched until the typed
// text changes so it does not immediately reopen on the next keystroke.
const tagMenuDismissed = ref(false);
const tagHighlightIndex = ref(0);

const tagOffers = computed(() =>
  tagsEditing.value ? ctx.matchTagOffers(props.conversation, ctx.tagInput.value) : [],
);
const tagMenuOpen = computed(
  () =>
    tagsEditing.value &&
    tagInputFocused.value &&
    !tagMenuDismissed.value &&
    tagOffers.value.length > 0,
);

// The menu is teleported to <body> (the drawer's overflow would clip it), so
// it is positioned in viewport coordinates under the input. Recomputed on
// open and whenever the drawer scrolls or the window resizes.
const menuRect = ref({ left: 0, top: 0, width: 0 });
const tagMenuStyle = computed(() => ({
  left: `${menuRect.value.left}px`,
  top: `${menuRect.value.top}px`,
  width: `${menuRect.value.width}px`,
}));
function updateMenuRect() {
  const el = tagInput.value;
  if (!el) return;
  const r = el.getBoundingClientRect();
  menuRect.value = { left: r.left, top: r.bottom + 4, width: Math.max(r.width, 160) };
}
watch(tagMenuOpen, (open) => {
  if (!open) {
    window.removeEventListener("scroll", updateMenuRect, true);
    window.removeEventListener("resize", updateMenuRect);
    return;
  }
  void nextTick(updateMenuRect);
  window.addEventListener("scroll", updateMenuRect, true);
  window.addEventListener("resize", updateMenuRect);
});
onBeforeUnmount(() => {
  window.removeEventListener("scroll", updateMenuRect, true);
  window.removeEventListener("resize", updateMenuRect);
});
// Keep the highlight in range as the offered set narrows.
watch(tagOffers, () => {
  if (tagHighlightIndex.value >= tagOffers.value.length) tagHighlightIndex.value = 0;
});

function onTagInput(e: Event) {
  ctx.tagInput.value = (e.target as HTMLInputElement).value;
  tagMenuDismissed.value = false;
  tagHighlightIndex.value = 0;
}
function onTagInputFocus() {
  tagInputFocused.value = true;
}
// A click on an option keeps focus (its @mousedown.prevent), so blurring means
// the field really lost focus: close the menu.
function onTagInputBlur() {
  tagInputFocused.value = false;
}
async function commitTag(tag: string) {
  await ctx.handleAddTag(props.conversation, tag);
  tagMenuDismissed.value = false;
  tagHighlightIndex.value = 0;
  await nextTick();
  tagInput.value?.focus();
}
function chooseTagOffer(tag: string) {
  void commitTag(tag);
}
// The form's submit path: Enter with the menu open commits the highlighted
// offer; otherwise it commits exactly what was typed (a brand-new tag).
async function onTagSubmit() {
  const best = tagMenuOpen.value ? tagOffers.value[tagHighlightIndex.value] : undefined;
  await commitTag(best ? best.tag : ctx.tagInput.value);
}
function onTagInputKeyDown(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  if (tagMenuOpen.value) {
    const n = tagOffers.value.length;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      tagHighlightIndex.value = (tagHighlightIndex.value + 1) % n;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      tagHighlightIndex.value = (tagHighlightIndex.value - 1 + n) % n;
      return;
    }
  }
  if (e.key === "Escape") {
    e.preventDefault();
    // Peel one layer: first the open menu, then the editor.
    if (tagMenuOpen.value) {
      tagMenuDismissed.value = true;
      return;
    }
    ctx.tagEditorId.value = null;
    ctx.tagInput.value = "";
  }
}

// Forward the active rename/tag-editor DOM refs up to the parent so its
// focus/select/outside-click logic can reach them (mirrors the React refs,
// which are bound only on the active row).
function setTagEditorRef(el: Element | null) {
  ctx.tagEditorRef.value = (el as HTMLElement) ?? null;
}
// Bound unconditionally; only writes the shared ref while this row is the
// active tag editor (and clears it back to null otherwise via the v-if).
const setTagEditorRefMaybe = (el: unknown) => {
  if (tagsEditing.value) setTagEditorRef((el as Element) ?? null);
};
watch(renameInput, (el) => {
  if (el) ctx.renameInputRef.value = el;
});
watch(tagInput, (el) => {
  if (el) ctx.tagInputRef.value = el;
});

// Inline delete button (mirrors renderDeleteButton). Defined as a render
// component so the confirm/trash markup isn't duplicated in template.
const DeleteButton = defineComponent({
  props: { conversationId: { type: String, required: true } },
  setup(p) {
    const checkIcon = () =>
      h(
        "svg",
        { fill: "none", stroke: "currentColor", viewBox: "0 0 24 24", class: "drawer-icon-size" },
        [
          h("path", {
            "stroke-linecap": "round",
            "stroke-linejoin": "round",
            "stroke-width": 2,
            d: "M5 13l4 4L19 7",
          }),
        ],
      );
    const xIcon = () =>
      h(
        "svg",
        { fill: "none", stroke: "currentColor", viewBox: "0 0 24 24", class: "drawer-icon-size" },
        [
          h("path", {
            "stroke-linecap": "round",
            "stroke-linejoin": "round",
            "stroke-width": 2,
            d: "M6 18L18 6M6 6l12 12",
          }),
        ],
      );
    const trashIcon = () =>
      h(
        "svg",
        { fill: "none", stroke: "currentColor", viewBox: "0 0 24 24", class: "drawer-icon-size" },
        [
          h("path", {
            "stroke-linecap": "round",
            "stroke-linejoin": "round",
            "stroke-width": 2,
            d: "M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16",
          }),
        ],
      );
    return () => {
      if (ctx.pendingDeleteId.value === p.conversationId) {
        return h(
          "div",
          {
            class: "drawer-delete-confirm",
            ref: (el: unknown) => (ctx.pendingDeleteRef.value = (el as HTMLElement) ?? null),
            onClick: (e: MouseEvent) => e.stopPropagation(),
            title: ctx.t("confirmDelete"),
          },
          [
            h("span", { class: "drawer-delete-confirm-label" }, ctx.t("confirmDeleteShort")),
            h(
              "button",
              {
                type: "button",
                class: "btn-icon-sm btn-danger drawer-delete-confirm-yes",
                title: ctx.t("delete_"),
                "aria-label": ctx.t("delete_"),
                onClick: (e: MouseEvent) => ctx.handleConfirmDelete(e, p.conversationId),
              },
              [checkIcon()],
            ),
            h(
              "button",
              {
                type: "button",
                class: "btn-icon-sm",
                title: ctx.t("cancel"),
                "aria-label": ctx.t("cancel"),
                onClick: (e: MouseEvent) => ctx.handleCancelDelete(e),
              },
              [xIcon()],
            ),
          ] as VNode[],
        );
      }
      return h(
        "button",
        {
          class: "btn-icon-sm btn-danger",
          title: ctx.t("deletePermanently"),
          "aria-label": ctx.t("delete_"),
          onClick: (e: MouseEvent) => ctx.handleDeleteClick(e, p.conversationId),
        },
        [trashIcon()],
      );
    };
  },
});
</script>
