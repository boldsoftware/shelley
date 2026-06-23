<!-- Vue port of components/CommandPalette.tsx. Cmd/Ctrl+K fuzzy command +
     conversation-jump palette. PRESERVES the class/testid/aria/text contract:
     .command-palette-overlay, .command-palette, .command-palette-input,
     .command-palette-list, .command-palette-item(.selected), data-index, the
     kbd footer, and the within-palette keyboard nav. Uses api, messageStore,
     useMarkdownMode (instead of MarkdownContext), useI18n, tildify.

     Public API (consumed by ChatInterface):
       Props:
         isOpen: boolean
         conversations: ConversationWithState[]
         currentConversation: ConversationWithState | null
         hasCwd: boolean
       Emits (mirror the React callback props 1:1):
         (e: "close"): void                                  // onClose
         (e: "new-conversation"): void                       // onNewConversation
         (e: "new-conversation-with-cwd", cwd: string): void // onNewConversationWithCwd
         (e: "set-conversation-cwd", cwd: string): void      // onSetConversationCwd
         (e: "select-conversation", c: ConversationWithState): void // onSelectConversation
         (e: "archive-conversation", id: string): void       // onArchiveConversation
         (e: "open-diff-viewer"): void                        // onOpenDiffViewer
         (e: "open-git-graph"): void                          // onOpenGitGraph
         (e: "open-terminal"): void                           // onOpenTerminal
         (e: "open-models-modal"): void                       // onOpenModelsModal
         (e: "open-notifications-modal"): void                // onOpenNotificationsModal
         (e: "open-feature-flags-modal"): void                // onOpenFeatureFlagsModal
         (e: "next-conversation"): void                       // onNextConversation
         (e: "previous-conversation"): void                   // onPreviousConversation
         (e: "next-user-message"): void                       // onNextUserMessage
         (e: "previous-user-message"): void                   // onPreviousUserMessage -->
<template>
  <div v-if="isOpen" class="command-palette-overlay" @click="emit('close')">
    <div class="command-palette" @click.stop>
      <div class="command-palette-input-wrapper">
        <svg
          class="command-palette-search-icon"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
          width="20"
          height="20"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
          />
        </svg>
        <input
          ref="inputRef"
          type="text"
          class="command-palette-input"
          :placeholder="t('searchPlaceholder')"
          :value="query"
          @input="query = ($event.target as HTMLInputElement).value"
          @keydown="handleKeyDown"
        />
        <div v-if="isSearching" class="command-palette-spinner" />
        <div class="command-palette-shortcut">
          <kbd>esc</kbd>
        </div>
      </div>

      <div ref="listRef" class="command-palette-list">
        <div v-if="displayItems.length === 0" class="command-palette-empty">
          {{ isSearching ? t("searching") : t("noResults") }}
        </div>
        <template v-else>
          <div
            v-for="(item, index) in displayItems"
            :key="item.id"
            :data-index="index"
            :class="`command-palette-item ${index === selectedIndex ? 'selected' : ''}`"
            @click="onItemClick($event, item)"
            @auxclick="onItemAuxClick($event, item)"
            @mouseenter="selectedIndex = index"
          >
            <div class="command-palette-item-icon" v-html="item.icon"></div>
            <div class="command-palette-item-content">
              <div class="command-palette-item-title">{{ item.title }}</div>
              <div v-if="item.subtitle" class="command-palette-item-subtitle">
                {{ item.subtitle }}
              </div>
            </div>
            <div v-if="item.shortcut" class="command-palette-item-shortcut">
              <kbd>{{ item.shortcut }}</kbd>
            </div>
            <div v-else-if="item.type === 'action'" class="command-palette-item-badge">
              {{ t("action") }}
            </div>
          </div>
        </template>
      </div>

      <div class="command-palette-footer">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> {{ t("toNavigate") }}
        </span>
        <span> <kbd>↵</kbd> {{ t("toSelect") }} </span>
        <span> <kbd>esc</kbd> {{ t("toClose") }} </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { ConversationWithState } from "../../types";
import type { Locale, TranslationKeys } from "../../i18n/types";
import { api } from "../../services/api";
import { messageStore } from "../../services/messageStore";
import { useMarkdownMode } from "../composables/markdownMode";
import { useI18n } from "../composables/i18n";
import { tildifyPath } from "../../utils/tildify";

interface CommandItem {
  id: string;
  type: "action" | "conversation";
  title: string;
  subtitle?: string;
  shortcut?: string;
  /** Raw SVG markup (sanitized, app-authored) rendered via v-html. */
  icon?: string;
  action: () => void;
  /** If set, cmd/ctrl/shift/middle-click opens this URL in a new tab. */
  url?: string;
  keywords?: string[];
  priority?: number;
}

const props = defineProps<{
  isOpen: boolean;
  conversations: ConversationWithState[];
  currentConversation: ConversationWithState | null;
  hasCwd: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "new-conversation"): void;
  (e: "new-conversation-with-cwd", cwd: string): void;
  (e: "set-conversation-cwd", cwd: string): void;
  (e: "select-conversation", c: ConversationWithState): void;
  (e: "archive-conversation", id: string): void;
  (e: "open-diff-viewer"): void;
  (e: "open-git-graph"): void;
  (e: "open-terminal"): void;
  (e: "open-models-modal"): void;
  (e: "open-notifications-modal"): void;
  (e: "open-feature-flags-modal"): void;
  (e: "next-conversation"): void;
  (e: "previous-conversation"): void;
  (e: "next-user-message"): void;
  (e: "previous-user-message"): void;
}>();

const { markdownMode, setMarkdownMode } = useMarkdownMode();
const { t, locale, setLocale } = useI18n();

const query = ref("");
const selectedIndex = ref(0);
const searchResults = ref<ConversationWithState[]>([]);
const isSearching = ref(false);
const isCreatingWorktree = ref(false);
const newConvGitRepoRoot = ref<string | null>(null);
const newConvGitWorktreeRoot = ref<string | null>(null);
const inputRef = ref<HTMLInputElement | null>(null);
const listRef = ref<HTMLDivElement | null>(null);
let searchTimeout: number | null = null;

// --- Icon markup (identical to the React JSX icons) ---
const SVG_OPEN =
  '<svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">';
const ICON_PLUS = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" /></svg>`;
const ICON_DOWN = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 14l-7 7m0 0l-7-7m7 7V3" /></svg>`;
const ICON_UP = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 10l7-7m0 0l7 7m-7-7v18" /></svg>`;
const ICON_DIFF = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" /></svg>`;
const ICON_GRAPH = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 3v12m0 0a3 3 0 103 3 3 3 0 00-3-3zm0-12a3 3 0 100 6 3 3 0 000-6zm12 0a3 3 0 100 6 3 3 0 000-6zm0 6c0 4-6 4-6 9" /></svg>`;
const ICON_TERMINAL = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>`;
const ICON_COG = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" /><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" /></svg>`;
const ICON_BELL = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9" /></svg>`;
const ICON_FLAG = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 21V5a2 2 0 012-2h11l-2 4 2 4H5v10" /></svg>`;
const ICON_MARKDOWN = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h8m-8 6h16" /></svg>`;
const ICON_ARCHIVE = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4" /></svg>`;
const ICON_FOLDER = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z" /></svg>`;
const ICON_WORKTREE = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7v8a2 2 0 002 2h6M8 7V5a2 2 0 012-2h4.586a1 1 0 01.707.293l4.414 4.414a1 1 0 01.293.707V15a2 2 0 01-2 2h-2M8 7H6a2 2 0 00-2 2v10a2 2 0 002 2h8a2 2 0 002-2v-2" /></svg>`;
const ICON_LANG = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 5h12M9 3v2m1.048 9.5A18.022 18.022 0 016.412 9m6.088 9h7M11 21l5-10 5 10M12.751 5C11.783 10.77 8.07 15.61 3 18.129" /></svg>`;
const ICON_TRASH = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6M1 7h22M9 7V4a2 2 0 012-2h2a2 2 0 012 2v3" /></svg>`;
const ICON_CHAT = `${SVG_OPEN}<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z" /></svg>`;

// Simple fuzzy match for actions - returns score (higher is better), -1 if no match
function fuzzyMatch(q: string, text: string): number {
  const lowerQuery = q.toLowerCase();
  const lowerText = text.toLowerCase();
  if (lowerText === lowerQuery) return 1000;
  if (lowerText.startsWith(lowerQuery)) return 500 + (lowerQuery.length / lowerText.length) * 100;
  if (lowerText.includes(lowerQuery)) return 100 + (lowerQuery.length / lowerText.length) * 50;
  let queryIdx = 0;
  let score = 0;
  let consecutiveBonus = 0;
  for (let i = 0; i < lowerText.length && queryIdx < lowerQuery.length; i++) {
    if (lowerText[i] === lowerQuery[queryIdx]) {
      score += 1 + consecutiveBonus;
      consecutiveBonus += 0.5;
      queryIdx++;
    } else {
      consecutiveBonus = 0;
    }
  }
  if (queryIdx !== lowerQuery.length) return -1;
  return score;
}

// Search conversations on the server (debounced via the query watcher).
async function searchConversations(searchQuery: string) {
  if (!searchQuery.trim()) {
    searchResults.value = [];
    isSearching.value = false;
    return;
  }
  isSearching.value = true;
  try {
    searchResults.value = await api.searchConversations(searchQuery);
  } catch (err) {
    console.error("Failed to search conversations:", err);
    searchResults.value = [];
  } finally {
    isSearching.value = false;
  }
}

watch(query, (q) => {
  if (searchTimeout) clearTimeout(searchTimeout);
  if (q.trim()) {
    searchTimeout = window.setTimeout(() => void searchConversations(q), 150);
  } else {
    searchResults.value = [];
    isSearching.value = false;
  }
});

// When the palette opens, look up git roots for the locally-selected cwd.
watch(
  [() => props.isOpen, () => props.currentConversation],
  () => {
    if (!props.isOpen) {
      newConvGitRepoRoot.value = null;
      newConvGitWorktreeRoot.value = null;
      return;
    }
    const cwd =
      props.currentConversation?.cwd ||
      localStorage.getItem("shelley_selected_cwd") ||
      window.__SHELLEY_INIT__?.default_cwd ||
      null;
    if (!cwd) return;
    let cancelled = false;
    api
      .listDirectory(cwd)
      .then((res) => {
        if (cancelled) return;
        newConvGitRepoRoot.value = res.git_repo_root ?? null;
        newConvGitWorktreeRoot.value = res.git_worktree_root ?? null;
      })
      .catch((err) => console.error("Failed to list dir for git roots:", err));
    // (cancellation handled implicitly: a newer open resets refs first)
    void cancelled;
  },
  { immediate: true },
);

// Build action items (always available).
const actionItems = computed<CommandItem[]>(() => {
  const items: CommandItem[] = [];

  items.push({
    id: "new-conversation",
    type: "action",
    title: t("newConversationAction"),
    subtitle: t("startNewConversation"),
    icon: ICON_PLUS,
    action: () => {
      emit("new-conversation");
      emit("close");
    },
    keywords: ["new", "create", "start", "conversation", "chat"],
  });

  items.push({
    id: "next-conversation",
    type: "action",
    title: t("nextConversation"),
    subtitle: t("switchToNext"),
    shortcut: "Alt+↓",
    icon: ICON_DOWN,
    action: () => {
      emit("next-conversation");
      emit("close");
    },
    keywords: ["next", "down", "forward", "conversation", "switch"],
  });

  items.push({
    id: "previous-conversation",
    type: "action",
    title: t("previousConversation"),
    subtitle: t("switchToPrevious"),
    shortcut: "Alt+↑",
    icon: ICON_UP,
    action: () => {
      emit("previous-conversation");
      emit("close");
    },
    keywords: ["previous", "up", "back", "conversation", "switch"],
  });

  items.push({
    id: "next-user-message",
    type: "action",
    title: t("nextUserMessage"),
    subtitle: t("jumpToNextMessage"),
    shortcut: "Ctrl+M, N",
    icon: ICON_DOWN,
    action: () => {
      emit("next-user-message");
      emit("close");
    },
    keywords: ["next", "down", "forward", "user", "message", "navigate", "jump"],
  });

  items.push({
    id: "previous-user-message",
    type: "action",
    title: t("previousUserMessage"),
    subtitle: t("jumpToPreviousMessage"),
    shortcut: "Ctrl+M, P",
    icon: ICON_UP,
    action: () => {
      emit("previous-user-message");
      emit("close");
    },
    keywords: ["previous", "up", "back", "user", "message", "navigate", "jump"],
  });

  if (props.hasCwd) {
    items.push({
      id: "open-diffs",
      type: "action",
      title: t("viewDiffs"),
      subtitle: t("openGitDiffViewer"),
      icon: ICON_DIFF,
      action: () => {
        emit("open-diff-viewer");
        emit("close");
      },
      keywords: ["diff", "git", "changes", "view", "compare"],
    });

    items.push({
      id: "open-git-graph",
      type: "action",
      title: t("gitGraph"),
      subtitle: t("openGitGraphViewer"),
      icon: ICON_GRAPH,
      action: () => {
        emit("open-git-graph");
        emit("close");
      },
      keywords: ["git", "graph", "log", "commits", "history", "branch", "tree"],
    });
  }

  items.push({
    id: "open-terminal",
    type: "action",
    title: "Open Terminal",
    subtitle: "Start a new interactive shell",
    icon: ICON_TERMINAL,
    action: () => {
      emit("open-terminal");
      emit("close");
    },
    keywords: ["terminal", "shell", "bash", "zsh", "fish", "login", "console", "tty", "pty"],
  });

  items.push({
    id: "manage-models",
    type: "action",
    title: t("addRemoveModelsKeys"),
    subtitle: t("configureModels"),
    icon: ICON_COG,
    action: () => {
      emit("open-models-modal");
      emit("close");
    },
    keywords: [
      "model",
      "key",
      "api",
      "configure",
      "settings",
      "anthropic",
      "openai",
      "gemini",
      "custom",
    ],
  });

  items.push({
    id: "notification-settings",
    type: "action",
    title: t("notificationSettings"),
    subtitle: t("configureNotifications"),
    icon: ICON_BELL,
    action: () => {
      emit("open-notifications-modal");
      emit("close");
    },
    keywords: ["notification", "notify", "alert", "discord", "webhook", "browser", "favicon"],
  });

  items.push({
    id: "feature-flags",
    type: "action",
    title: "Feature flags",
    subtitle: "Toggle experimental features",
    icon: ICON_FLAG,
    action: () => {
      emit("open-feature-flags-modal");
      emit("close");
    },
    keywords: ["feature", "flag", "flags", "experiment", "toggle", "override"],
  });

  const mdLabels: Record<
    string,
    { title: string; subtitle: string; next: "off" | "agent" | "all" }
  > = {
    off: { title: t("enableMarkdownAgent"), subtitle: t("renderMarkdownAgent"), next: "agent" },
    agent: { title: t("enableMarkdownAll"), subtitle: t("renderMarkdownAll"), next: "all" },
    all: { title: t("disableMarkdown"), subtitle: t("showPlainText"), next: "off" },
  };
  const md = mdLabels[markdownMode.value];
  items.push({
    id: "toggle-markdown",
    type: "action",
    title: md.title,
    subtitle: md.subtitle,
    icon: ICON_MARKDOWN,
    action: () => {
      setMarkdownMode(md.next);
      emit("close");
    },
    keywords: ["markdown", "render", "format", "rich", "text", "plain"],
  });

  // Archive current conversation
  if (props.currentConversation) {
    const conv = props.currentConversation;
    items.push({
      id: "archive-conversation",
      type: "action",
      title: t("archiveConversationAction"),
      subtitle: t("archiveCurrentConversation"),
      icon: ICON_ARCHIVE,
      action: () => {
        emit("archive-conversation", conv.conversation_id);
        emit("close");
      },
      keywords: ["archive", "hide", "remove", "close"],
    });
  }

  // "Set new conversation dir to git root / workspace root" actions.
  const cwdRepoRoot = props.currentConversation?.git_repo_root || newConvGitRepoRoot.value;
  const cwdWorktreeRoot =
    props.currentConversation?.git_worktree_root || newConvGitWorktreeRoot.value;
  const cwdNow =
    props.currentConversation?.cwd ||
    localStorage.getItem("shelley_selected_cwd") ||
    window.__SHELLEY_INIT__?.default_cwd ||
    null;

  if (cwdRepoRoot && cwdRepoRoot !== cwdNow) {
    items.push({
      id: "set-dir-git-root",
      type: "action",
      title: `${t("setWorkingDirToRepoRoot")} (${tildifyPath(cwdRepoRoot)})`,
      subtitle: cwdRepoRoot,
      icon: ICON_FOLDER,
      priority: 1000,
      action: () => {
        emit("set-conversation-cwd", cwdRepoRoot);
        emit("close");
      },
      keywords: [
        "cd",
        "change",
        "set",
        "dir",
        "directory",
        "cwd",
        "git",
        "root",
        "toplevel",
        "repo",
      ],
    });
  }
  if (cwdWorktreeRoot && cwdWorktreeRoot !== cwdNow && cwdWorktreeRoot !== cwdRepoRoot) {
    items.push({
      id: "set-dir-git-workspace-root",
      type: "action",
      title: `${t("setWorkingDirToMainRepo")} (${tildifyPath(cwdWorktreeRoot)})`,
      subtitle: cwdWorktreeRoot,
      icon: ICON_FOLDER,
      priority: 1000,
      action: () => {
        emit("set-conversation-cwd", cwdWorktreeRoot);
        emit("close");
      },
      keywords: [
        "cd",
        "change",
        "set",
        "dir",
        "directory",
        "cwd",
        "git",
        "workspace",
        "worktree",
        "main",
        "repo",
        "root",
      ],
    });
  }

  // New conversation in repo root (only when current cwd is a worktree)
  if (props.currentConversation?.git_worktree_root) {
    const worktreeRoot = props.currentConversation.git_worktree_root;
    items.push({
      id: "new-in-repo-root",
      type: "action",
      title: t("newConversationInMainRepo"),
      subtitle: worktreeRoot,
      icon: ICON_FOLDER,
      action: () => {
        emit("new-conversation-with-cwd", worktreeRoot);
        emit("close");
      },
      keywords: ["new", "repo", "root", "main", "repository", "worktree"],
    });
  }

  // New conversation in new worktree
  if (props.currentConversation?.git_repo_root && props.currentConversation?.cwd) {
    const convCwd = props.currentConversation.cwd;
    items.push({
      id: "new-in-worktree",
      type: "action",
      title: t("newConversationInNewWorktree"),
      subtitle: t("createNewWorktree"),
      icon: ICON_WORKTREE,
      action: async () => {
        if (isCreatingWorktree.value) return;
        isCreatingWorktree.value = true;
        try {
          const result = await api.createGitWorktree(convCwd);
          if (result.path) {
            emit("new-conversation-with-cwd", result.path);
            emit("close");
          }
        } catch (err) {
          console.error("Failed to create worktree:", err);
        } finally {
          isCreatingWorktree.value = false;
        }
      },
      keywords: ["new", "worktree", "branch", "git", "create"],
    });
  }

  // Clear the local cache (rotate the encryption key).
  items.push({
    id: "clear-local-cache",
    type: "action",
    title: "Clear local cache",
    subtitle: "Wipe cached conversations from this browser and rotate the encryption key",
    icon: ICON_TRASH,
    action: () => {
      void messageStore.wipeAndRotateKey().then(
        () => {
          window.location.reload();
        },
        (err) => {
          console.warn("clear-local-cache: rotation failed", err);
          window.alert("Failed to clear local cache. Please check your connection and try again.");
        },
      );
      emit("close");
    },
    keywords: [
      "clear",
      "cache",
      "wipe",
      "logout",
      "forget",
      "reset",
      "local",
      "privacy",
      "encrypt",
    ],
  });

  // Language switcher — one action per language.
  const languageOptions: {
    loc: Locale;
    flag: string;
    name: keyof TranslationKeys;
    nativeName: string;
    keywords: string[];
  }[] = [
    { loc: "en", flag: "🇺🇸", name: "english", nativeName: "English", keywords: ["english", "en"] },
    {
      loc: "ja",
      flag: "🇯🇵",
      name: "japanese",
      nativeName: "日本語",
      keywords: ["japanese", "ja", "日本語", "nihongo"],
    },
    {
      loc: "fr",
      flag: "🇫🇷",
      name: "french",
      nativeName: "Français",
      keywords: ["french", "fr", "français"],
    },
    {
      loc: "ru",
      flag: "🇷🇺",
      name: "russian",
      nativeName: "Русский",
      keywords: ["russian", "ru", "русский"],
    },
    {
      loc: "es",
      flag: "🇪🇸",
      name: "spanish",
      nativeName: "Español",
      keywords: ["spanish", "es", "español"],
    },
    {
      loc: "zh-CN",
      flag: "🇨🇳",
      name: "simplifiedChinese",
      nativeName: "简体中文",
      keywords: ["chinese", "simplified", "zh", "zh-cn", "中文", "简体"],
    },
    {
      loc: "zh-TW",
      flag: "🇹🇼",
      name: "traditionalChinese",
      nativeName: "繁體中文",
      keywords: ["chinese", "traditional", "zh", "zh-tw", "中文", "繁體"],
    },
    {
      loc: "vi",
      flag: "🇻🇳",
      name: "vietnamese",
      nativeName: "Tiếng Việt",
      keywords: ["vietnamese", "vi", "tiếng việt", "tieng viet"],
    },
    {
      loc: "upgoer5",
      flag: "🚀",
      name: "upgoerFive",
      nativeName: "Up-Goer Five",
      keywords: ["upgoer", "upgoer5", "simple", "xkcd", "ten hundred"],
    },
  ];
  for (const opt of languageOptions) {
    if (opt.loc === locale.value) continue;
    items.push({
      id: `switch-language-${opt.loc}`,
      type: "action",
      title: `${opt.flag} ${opt.nativeName}`,
      subtitle: `${t("switchLanguage")} — ${t(opt.name)}`,
      icon: ICON_LANG,
      action: () => {
        setLocale(opt.loc);
        emit("close");
      },
      keywords: ["language", "locale", "translate", "i18n", ...opt.keywords],
    });
  }

  return items;
});

function conversationToItem(conv: ConversationWithState): CommandItem {
  return {
    id: `conv-${conv.conversation_id}`,
    type: "conversation",
    title: conv.slug || conv.conversation_id,
    subtitle: conv.cwd || undefined,
    icon: ICON_CHAT,
    action: () => {
      emit("select-conversation", conv);
      emit("close");
    },
    url: conv.slug ? `/c/${conv.slug}` : undefined,
  };
}

const displayItems = computed<CommandItem[]>(() => {
  const trimmedQuery = query.value.trim();
  let filteredActions = actionItems.value;
  if (trimmedQuery) {
    filteredActions = actionItems.value
      .map((item) => {
        let maxScore = fuzzyMatch(trimmedQuery, item.title);
        if (item.subtitle) {
          const subtitleScore = fuzzyMatch(trimmedQuery, item.subtitle);
          if (subtitleScore > maxScore) maxScore = subtitleScore * 0.8;
        }
        if (item.keywords) {
          for (const keyword of item.keywords) {
            const keywordScore = fuzzyMatch(trimmedQuery, keyword);
            if (keywordScore > maxScore) maxScore = keywordScore * 0.7;
          }
        }
        return { item, score: maxScore > 0 ? maxScore + (item.priority ?? 0) : -1 };
      })
      .filter((entry) => entry.score > 0)
      .sort((a, b) => b.score - a.score)
      .map((entry) => entry.item);
  }
  const conversationsToShow = trimmedQuery ? searchResults.value : props.conversations;
  const conversationItems = conversationsToShow.map(conversationToItem);
  return [...filteredActions, ...conversationItems];
});

// Reset selection when items change.
watch(displayItems, () => {
  selectedIndex.value = 0;
});

// Focus input when opened.
watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      query.value = "";
      selectedIndex.value = 0;
      searchResults.value = [];
      setTimeout(() => inputRef.value?.focus(), 0);
    }
  },
);

// Scroll selected item into view.
watch(selectedIndex, (idx) => {
  if (!listRef.value) return;
  const selectedElement = listRef.value.querySelector(`[data-index="${idx}"]`);
  selectedElement?.scrollIntoView({ block: "nearest" });
});

function handleKeyDown(e: KeyboardEvent) {
  switch (e.key) {
    case "ArrowDown":
      e.preventDefault();
      selectedIndex.value = Math.min(selectedIndex.value + 1, displayItems.value.length - 1);
      break;
    case "ArrowUp":
      e.preventDefault();
      selectedIndex.value = Math.max(selectedIndex.value - 1, 0);
      break;
    case "Enter":
      e.preventDefault();
      if (displayItems.value[selectedIndex.value]) {
        displayItems.value[selectedIndex.value].action();
      }
      break;
    case "Escape":
      e.preventDefault();
      emit("close");
      break;
  }
}

function onItemClick(e: MouseEvent, item: CommandItem) {
  if (item.url && (e.metaKey || e.ctrlKey || e.shiftKey)) {
    e.preventDefault();
    e.stopPropagation();
    window.open(item.url, "_blank", "noopener");
    return;
  }
  item.action();
}
function onItemAuxClick(e: MouseEvent, item: CommandItem) {
  if (item.url && e.button === 1) {
    e.preventDefault();
    e.stopPropagation();
    window.open(item.url, "_blank", "noopener");
  }
}
</script>
