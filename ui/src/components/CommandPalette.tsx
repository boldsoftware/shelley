import React, { useState, useEffect, useRef, useMemo, useCallback } from "react";
import { ConversationWithState } from "../types";
import { api } from "../services/api";
import { useMarkdown } from "../contexts/MarkdownContext";
import { useI18n, type Locale } from "../i18n";

interface CommandItem {
  id: string;
  type: "action" | "conversation";
  title: string;
  subtitle?: string;
  shortcut?: string;
  icon?: React.ReactNode;
  action: () => void;
  keywords?: string[]; // Additional keywords for search
}

interface CommandPaletteProps {
  isOpen: boolean;
  onClose: () => void;
  conversations: ConversationWithState[];
  currentConversation: ConversationWithState | null;
  onNewConversation: () => void;
  onNewConversationWithCwd: (cwd: string) => void;
  onSelectConversation: (conversation: ConversationWithState) => void;
  onArchiveConversation: (conversationId: string) => void;
  onOpenDiffViewer: () => void;
  onOpenModelsModal: () => void;
  onOpenNotificationsModal: () => void;
  onNextConversation: () => void;
  onPreviousConversation: () => void;
  onNextUserMessage: () => void;
  onPreviousUserMessage: () => void;
  hasCwd: boolean;
}

// Simple fuzzy match for actions - returns score (higher is better), -1 if no match
function fuzzyMatch(query: string, text: string): number {
  const lowerQuery = query.toLowerCase();
  const lowerText = text.toLowerCase();

  // Exact match gets highest score
  if (lowerText === lowerQuery) return 1000;

  // Starts with gets high score
  if (lowerText.startsWith(lowerQuery)) return 500 + (lowerQuery.length / lowerText.length) * 100;

  // Contains gets medium score
  if (lowerText.includes(lowerQuery)) return 100 + (lowerQuery.length / lowerText.length) * 50;

  // Fuzzy match - all query chars must appear in order
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

  // All query chars must be found
  if (queryIdx !== lowerQuery.length) return -1;

  return score;
}

function CommandPalette({
  isOpen,
  onClose,
  conversations,
  currentConversation,
  onNewConversation,
  onNewConversationWithCwd,
  onSelectConversation,
  onArchiveConversation,
  onOpenDiffViewer,
  onOpenModelsModal,
  onOpenNotificationsModal,
  onNextConversation,
  onPreviousConversation,
  onNextUserMessage,
  onPreviousUserMessage,
  hasCwd,
}: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [searchResults, setSearchResults] = useState<ConversationWithState[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const [isCreatingWorktree, setIsCreatingWorktree] = useState(false);
  const { markdownMode, setMarkdownMode } = useMarkdown();
  const { t, locale, setLocale } = useI18n();
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<number | null>(null);

  // Search conversations on the server
  const searchConversations = useCallback(async (searchQuery: string) => {
    if (!searchQuery.trim()) {
      setSearchResults([]);
      setIsSearching(false);
      return;
    }

    setIsSearching(true);
    try {
      const results = await api.searchConversations(searchQuery);
      setSearchResults(results);
    } catch (err) {
      console.error("Failed to search conversations:", err);
      setSearchResults([]);
    } finally {
      setIsSearching(false);
    }
  }, []);

  // Debounced search when query changes
  useEffect(() => {
    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
    }

    if (query.trim()) {
      searchTimeoutRef.current = window.setTimeout(() => {
        searchConversations(query);
      }, 150); // 150ms debounce
    } else {
      setSearchResults([]);
      setIsSearching(false);
    }

    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
      }
    };
  }, [query, searchConversations]);

  // Build action items (these are always available)
  const actionItems: CommandItem[] = useMemo(() => {
    const items: CommandItem[] = [];

    items.push({
      id: "new-conversation",
      type: "action",
      title: t("newConversationAction"),
      subtitle: t("startNewConversation"),
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 4v16m8-8H4" />
        </svg>
      ),
      action: () => {
        onNewConversation();
        onClose();
      },
      keywords: ["new", "create", "start", "conversation", "chat"],
    });

    items.push({
      id: "next-conversation",
      type: "action",
      title: t("nextConversation"),
      subtitle: t("switchToNext"),
      shortcut: "Alt+↓",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M19 14l-7 7m0 0l-7-7m7 7V3"
          />
        </svg>
      ),
      action: () => {
        onNextConversation();
        onClose();
      },
      keywords: ["next", "down", "forward", "conversation", "switch"],
    });

    items.push({
      id: "previous-conversation",
      type: "action",
      title: t("previousConversation"),
      subtitle: t("switchToPrevious"),
      shortcut: "Alt+↑",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M5 10l7-7m0 0l7 7m-7-7v18"
          />
        </svg>
      ),
      action: () => {
        onPreviousConversation();
        onClose();
      },
      keywords: ["previous", "up", "back", "conversation", "switch"],
    });

    items.push({
      id: "next-user-message",
      type: "action",
      title: t("nextUserMessage"),
      subtitle: t("jumpToNextMessage"),
      shortcut: "Ctrl+M, N",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M19 14l-7 7m0 0l-7-7m7 7V3"
          />
        </svg>
      ),
      action: () => {
        onNextUserMessage();
        onClose();
      },
      keywords: ["next", "down", "forward", "user", "message", "navigate", "jump"],
    });

    items.push({
      id: "previous-user-message",
      type: "action",
      title: t("previousUserMessage"),
      subtitle: t("jumpToPreviousMessage"),
      shortcut: "Ctrl+M, P",
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M5 10l7-7m0 0l7 7m-7-7v18"
          />
        </svg>
      ),
      action: () => {
        onPreviousUserMessage();
        onClose();
      },
      keywords: ["previous", "up", "back", "user", "message", "navigate", "jump"],
    });

    if (hasCwd) {
      items.push({
        id: "open-diffs",
        type: "action",
        title: t("viewDiffs"),
        subtitle: t("openGitDiffViewer"),
        icon: (
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
            />
          </svg>
        ),
        action: () => {
          onOpenDiffViewer();
          onClose();
        },
        keywords: ["diff", "git", "changes", "view", "compare"],
      });
    }

    items.push({
      id: "manage-models",
      type: "action",
      title: t("addRemoveModelsKeys"),
      subtitle: t("configureModels"),
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
          />
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
          />
        </svg>
      ),
      action: () => {
        onOpenModelsModal();
        onClose();
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
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
          />
        </svg>
      ),
      action: () => {
        onOpenNotificationsModal();
        onClose();
      },
      keywords: ["notification", "notify", "alert", "discord", "webhook", "browser", "favicon"],
    });

    const mdLabels: Record<
      string,
      { title: string; subtitle: string; next: "off" | "agent" | "all" }
    > = {
      off: {
        title: t("enableMarkdownAgent"),
        subtitle: t("renderMarkdownAgent"),
        next: "agent",
      },
      agent: {
        title: t("enableMarkdownAll"),
        subtitle: t("renderMarkdownAll"),
        next: "all",
      },
      all: { title: t("disableMarkdown"), subtitle: t("showPlainText"), next: "off" },
    };
    const md = mdLabels[markdownMode];
    items.push({
      id: "toggle-markdown",
      type: "action",
      title: md.title,
      subtitle: md.subtitle,
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M4 6h16M4 12h8m-8 6h16"
          />
        </svg>
      ),
      action: () => {
        setMarkdownMode(md.next);
        onClose();
      },
      keywords: ["markdown", "render", "format", "rich", "text", "plain"],
    });

    // Archive current conversation
    if (currentConversation) {
      items.push({
        id: "archive-conversation",
        type: "action",
        title: t("archiveConversationAction"),
        subtitle: t("archiveCurrentConversation"),
        icon: (
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
            />
          </svg>
        ),
        action: () => {
          onArchiveConversation(currentConversation.conversation_id);
          onClose();
        },
        keywords: ["archive", "hide", "remove", "close"],
      });
    }

    // New conversation in repo root (only when current cwd is a worktree)
    if (currentConversation?.git_worktree_root) {
      items.push({
        id: "new-in-repo-root",
        type: "action",
        title: t("newConversationInMainRepo"),
        subtitle: currentConversation.git_worktree_root,
        icon: (
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
            />
          </svg>
        ),
        action: () => {
          onNewConversationWithCwd(currentConversation.git_worktree_root!);
          onClose();
        },
        keywords: ["new", "repo", "root", "main", "repository", "worktree"],
      });
    }

    // New conversation in new worktree
    if (currentConversation?.git_repo_root && currentConversation?.cwd) {
      items.push({
        id: "new-in-worktree",
        type: "action",
        title: t("newConversationInNewWorktree"),
        subtitle: t("createNewWorktree"),
        icon: (
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M8 7v8a2 2 0 002 2h6M8 7V5a2 2 0 012-2h4.586a1 1 0 01.707.293l4.414 4.414a1 1 0 01.293.707V15a2 2 0 01-2 2h-2M8 7H6a2 2 0 00-2 2v10a2 2 0 002 2h8a2 2 0 002-2v-2"
            />
          </svg>
        ),
        action: async () => {
          if (isCreatingWorktree) return;
          setIsCreatingWorktree(true);
          try {
            const result = await api.createGitWorktree(currentConversation.cwd!);
            if (result.path) {
              onNewConversationWithCwd(result.path);
              onClose();
            }
          } catch (err) {
            console.error("Failed to create worktree:", err);
          } finally {
            setIsCreatingWorktree(false);
          }
        },
        keywords: ["new", "worktree", "branch", "git", "create"],
      });
    }

    // Language switcher — one action per language
    const languageOptions: {
      loc: Locale;
      flag: string;
      name: "english" | "japanese" | "french" | "russian" | "spanish" | "simplifiedChinese" | "traditionalChinese" | "upgoerFive";
      nativeName: string;
      keywords: string[];
    }[] = [
      {
        loc: "en",
        flag: "\ud83c\uddfa\ud83c\uddf8",
        name: "english",
        nativeName: "English",
        keywords: ["english", "en"],
      },
      {
        loc: "ja",
        flag: "\ud83c\uddef\ud83c\uddf5",
        name: "japanese",
        nativeName: "\u65e5\u672c\u8a9e",
        keywords: ["japanese", "ja", "\u65e5\u672c\u8a9e", "nihongo"],
      },
      {
        loc: "fr",
        flag: "\ud83c\uddeb\ud83c\uddf7",
        name: "french",
        nativeName: "Fran\u00e7ais",
        keywords: ["french", "fr", "fran\u00e7ais"],
      },
      {
        loc: "ru",
        flag: "\ud83c\uddf7\ud83c\uddfa",
        name: "russian",
        nativeName: "\u0420\u0443\u0441\u0441\u043a\u0438\u0439",
        keywords: ["russian", "ru", "\u0440\u0443\u0441\u0441\u043a\u0438\u0439"],
      },
      {
        loc: "es",
        flag: "\ud83c\uddea\ud83c\uddf8",
        name: "spanish",
        nativeName: "Espa\u00f1ol",
        keywords: ["spanish", "es", "espa\u00f1ol"],
      },
      {
        loc: "zh-CN",
        flag: "\ud83c\udde8\ud83c\uddf3",
        name: "simplifiedChinese",
        nativeName: "\u7b80\u4f53\u4e2d\u6587",
        keywords: ["chinese", "simplified", "zh", "zh-cn", "\u4e2d\u6587", "\u7b80\u4f53"],
      },
      {
        loc: "zh-TW",
        flag: "\ud83c\uddf9\ud83c\uddfc",
        name: "traditionalChinese",
        nativeName: "\u7e41\u9ad4\u4e2d\u6587",
        keywords: ["chinese", "traditional", "zh", "zh-tw", "\u4e2d\u6587", "\u7e41\u9ad4"],
      },
      {
        loc: "upgoer5",
        flag: "\ud83d\ude80",
        name: "upgoerFive",
        nativeName: "Up-Goer Five",
        keywords: ["upgoer", "upgoer5", "simple", "xkcd", "ten hundred"],
      },
    ];
    const langIcon = (
      <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={2}
          d="M3 5h12M9 3v2m1.048 9.5A18.022 18.022 0 016.412 9m6.088 9h7M11 21l5-10 5 10M12.751 5C11.783 10.77 8.07 15.61 3 18.129"
        />
      </svg>
    );
    for (const opt of languageOptions) {
      if (opt.loc === locale) continue;
      items.push({
        id: `switch-language-${opt.loc}`,
        type: "action",
        title: `${opt.flag} ${opt.nativeName}`,
        subtitle: `${t("switchLanguage")} — ${t(opt.name)}`,
        icon: langIcon,
        action: () => {
          setLocale(opt.loc);
          onClose();
        },
        keywords: ["language", "locale", "translate", "i18n", ...opt.keywords],
      });
    }

    return items;
  }, [
    locale,
    setLocale,
    t,
    onNewConversation,
    onNextConversation,
    onPreviousConversation,
    onNextUserMessage,
    onPreviousUserMessage,
    onOpenDiffViewer,
    onOpenModelsModal,
    onOpenNotificationsModal,
    onArchiveConversation,
    onNewConversationWithCwd,
    onClose,
    hasCwd,
    currentConversation,
    isCreatingWorktree,
    markdownMode,
    setMarkdownMode,
  ]);

  // Convert conversations to command items
  const conversationToItem = useCallback(
    (conv: ConversationWithState): CommandItem => ({
      id: `conv-${conv.conversation_id}`,
      type: "conversation",
      title: conv.slug || conv.conversation_id,
      subtitle: conv.cwd || undefined,
      icon: (
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z"
          />
        </svg>
      ),
      action: () => {
        onSelectConversation(conv);
        onClose();
      },
    }),
    [onSelectConversation, onClose],
  );

  // Compute the final list of items to display
  const displayItems = useMemo(() => {
    const trimmedQuery = query.trim();

    // Filter actions based on query (client-side fuzzy match)
    let filteredActions = actionItems;
    if (trimmedQuery) {
      filteredActions = actionItems.filter((item) => {
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
        return maxScore > 0;
      });
    }

    // Use search results if we have a query, otherwise use initial conversations
    const conversationsToShow = trimmedQuery ? searchResults : conversations;
    const conversationItems = conversationsToShow.map(conversationToItem);

    return [...filteredActions, ...conversationItems];
  }, [query, actionItems, searchResults, conversations, conversationToItem]);

  // Reset selection when items change
  useEffect(() => {
    setSelectedIndex(0);
  }, [displayItems]);

  // Focus input when opened
  useEffect(() => {
    if (isOpen) {
      setQuery("");
      setSelectedIndex(0);
      setSearchResults([]);
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isOpen]);

  // Scroll selected item into view
  useEffect(() => {
    if (!listRef.current) return;
    const selectedElement = listRef.current.querySelector(`[data-index="${selectedIndex}"]`);
    selectedElement?.scrollIntoView({ block: "nearest" });
  }, [selectedIndex]);

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setSelectedIndex((prev) => Math.min(prev + 1, displayItems.length - 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        setSelectedIndex((prev) => Math.max(prev - 1, 0));
        break;
      case "Enter":
        e.preventDefault();
        if (displayItems[selectedIndex]) {
          displayItems[selectedIndex].action();
        }
        break;
      case "Escape":
        e.preventDefault();
        onClose();
        break;
    }
  };

  if (!isOpen) return null;

  return (
    <div className="command-palette-overlay" onClick={onClose}>
      <div className="command-palette" onClick={(e) => e.stopPropagation()}>
        <div className="command-palette-input-wrapper">
          <svg
            className="command-palette-search-icon"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            width="20"
            height="20"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
            />
          </svg>
          <input
            ref={inputRef}
            type="text"
            className="command-palette-input"
            placeholder={t("searchPlaceholder")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
          />
          {isSearching && <div className="command-palette-spinner" />}
          <div className="command-palette-shortcut">
            <kbd>esc</kbd>
          </div>
        </div>

        <div className="command-palette-list" ref={listRef}>
          {displayItems.length === 0 ? (
            <div className="command-palette-empty">
              {isSearching ? t("searching") : t("noResults")}
            </div>
          ) : (
            displayItems.map((item, index) => (
              <div
                key={item.id}
                data-index={index}
                className={`command-palette-item ${index === selectedIndex ? "selected" : ""}`}
                onClick={() => item.action()}
                onMouseEnter={() => setSelectedIndex(index)}
              >
                <div className="command-palette-item-icon">{item.icon}</div>
                <div className="command-palette-item-content">
                  <div className="command-palette-item-title">{item.title}</div>
                  {item.subtitle && (
                    <div className="command-palette-item-subtitle">{item.subtitle}</div>
                  )}
                </div>
                {item.shortcut && (
                  <div className="command-palette-item-shortcut">
                    <kbd>{item.shortcut}</kbd>
                  </div>
                )}
                {item.type === "action" && !item.shortcut && (
                  <div className="command-palette-item-badge">{t("action")}</div>
                )}
              </div>
            ))
          )}
        </div>

        <div className="command-palette-footer">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t("toNavigate")}
          </span>
          <span>
            <kbd>↵</kbd> {t("toSelect")}
          </span>
          <span>
            <kbd>esc</kbd> {t("toClose")}
          </span>
        </div>
      </div>
    </div>
  );
}

export default CommandPalette;
