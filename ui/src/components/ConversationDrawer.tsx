import React, { useState, useEffect, useMemo, useCallback } from "react";
import { Conversation, ConversationWithState } from "../types";
import { api } from "../services/api";
import { useI18n } from "../i18n";
import { sortConversationsByBucket, maxBucket } from "../utils/conversationSort";
import { tildifyPath } from "../utils/tildify";

type GroupBy = "none" | "cwd" | "git_repo";

// SNIPPET_MARK_START / END match db.SnippetMarkStart / SnippetMarkEnd on the
// server. The server wraps every matched FTS term in these sentinel bytes;
// we split on them here and render the highlighted runs with <mark>.
const SNIPPET_MARK_START = "\x02";
const SNIPPET_MARK_END = "\x03";

function stripSnippetMarks(snippet: string): string {
  return snippet.split(SNIPPET_MARK_START).join("").split(SNIPPET_MARK_END).join("");
}

function renderSnippet(snippet: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let i = 0;
  let key = 0;
  while (i < snippet.length) {
    const start = snippet.indexOf(SNIPPET_MARK_START, i);
    if (start === -1) {
      out.push(snippet.slice(i));
      break;
    }
    if (start > i) out.push(snippet.slice(i, start));
    const end = snippet.indexOf(SNIPPET_MARK_END, start + 1);
    if (end === -1) {
      // Malformed; surface remainder as plain text so we don't drop content.
      out.push(snippet.slice(start + 1));
      break;
    }
    out.push(
      <mark key={key++} className="conversation-snippet-mark">
        {snippet.slice(start + 1, end)}
      </mark>,
    );
    i = end + 1;
  }
  return out;
}

interface ConversationDrawerProps {
  isOpen: boolean;
  isCollapsed: boolean;
  onClose: () => void;
  onToggleCollapse: () => void;
  conversations: ConversationWithState[];
  currentConversationId: string | null;
  viewedConversation?: Conversation | null; // The currently viewed conversation (may be a subagent)
  onSelectConversation: (conversation: Conversation) => void;
  onNewConversation: () => void;
  onConversationArchived?: (id: string) => void;
  onConversationUnarchived?: (conversation: Conversation) => void;
  onConversationRenamed?: (conversation: Conversation) => void;
  showActiveTrigger?: number; // Increment to switch back to active conversations view
}

function ConversationDrawer({
  isOpen,
  isCollapsed,
  onClose,
  onToggleCollapse,
  conversations,
  currentConversationId,
  viewedConversation,
  onSelectConversation,
  onNewConversation,
  onConversationArchived,
  onConversationUnarchived,
  onConversationRenamed,
  showActiveTrigger,
}: ConversationDrawerProps) {
  const { t } = useI18n();

  // Build the URL for a conversation, or null if it has no slug to route to.
  const conversationUrl = (conversation: Conversation): string | null => {
    if (!conversation.slug) return null;
    return `/c/${conversation.slug}`;
  };

  // For left-clicks with a modifier key (cmd/ctrl/shift/meta), open the conversation
  // in a new tab/window instead of switching in place. Returns true if handled.
  const handleModifiedClick = (e: React.MouseEvent, conversation: Conversation): boolean => {
    if (!(e.metaKey || e.ctrlKey || e.shiftKey)) return false;
    const url = conversationUrl(conversation);
    if (!url) return false;
    e.preventDefault();
    e.stopPropagation();
    window.open(url, "_blank", "noopener");
    return true;
  };

  // Middle-click (auxiliary button 1) opens in a new background tab.
  const handleAuxClick = (e: React.MouseEvent, conversation: Conversation) => {
    if (e.button !== 1) return;
    const url = conversationUrl(conversation);
    if (!url) return;
    e.preventDefault();
    e.stopPropagation();
    window.open(url, "_blank", "noopener");
  };
  const [showArchived, setShowArchived] = useState(false);
  const [archivedConversations, setArchivedConversations] = useState<Conversation[]>([]);
  const [loadingArchived, setLoadingArchived] = useState(false);
  // Free-text search across active AND archived conversations. Backed by
  // SQLite FTS5 on the server; matches slug or message content.
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState<ConversationWithState[] | null>(null);
  const [searching, setSearching] = useState(false);
  const searchTimeoutRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  // Monotonic counter so out-of-order fetch responses can't overwrite newer
  // results when the user is typing fast.
  const searchSeqRef = React.useRef(0);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingSlug, setEditingSlug] = useState("");
  const [expandedSubagents, setExpandedSubagents] = useState<Set<string>>(new Set());
  const [groupBy, setGroupBy] = useState<GroupBy>(() => {
    const stored = localStorage.getItem("shelley-group-by");
    return stored === "cwd" || stored === "git_repo" ? stored : "none";
  });
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set());
  const [groupMenuOpen, setGroupMenuOpen] = useState(false);
  const [copiedConvId, setCopiedConvId] = useState<string | null>(null);
  const groupMenuRef = React.useRef<HTMLDivElement>(null);
  const renameInputRef = React.useRef<HTMLInputElement>(null);
  const copyTimeoutRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  // Close group menu on outside click
  useEffect(() => {
    if (!groupMenuOpen) return;
    const handleClick = (e: MouseEvent) => {
      if (groupMenuRef.current && !groupMenuRef.current.contains(e.target as Node)) {
        setGroupMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [groupMenuOpen]);

  useEffect(() => {
    if (showArchived && archivedConversations.length === 0) {
      loadArchivedConversations();
    }
  }, [showArchived]);

  // Debounced FTS search across active + archived conversations.
  useEffect(() => {
    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
      searchTimeoutRef.current = null;
    }
    // Bump on every input change so any in-flight fetch from a prior query
    // (including ones whose debounce already fired) can't write stale
    // results into the UI.
    const seq = ++searchSeqRef.current;
    const q = searchQuery.trim();
    if (!q) {
      setSearchResults(null);
      setSearching(false);
      return;
    }
    setSearching(true);
    searchTimeoutRef.current = setTimeout(async () => {
      try {
        const results = await api.searchConversationsFTS(q);
        if (seq !== searchSeqRef.current) return; // superseded
        setSearchResults(results);
      } catch (err) {
        if (seq !== searchSeqRef.current) return;
        console.error("Conversation search failed:", err);
        setSearchResults([]);
      } finally {
        if (seq === searchSeqRef.current) setSearching(false);
      }
    }, 150);
    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
        searchTimeoutRef.current = null;
      }
    };
  }, [searchQuery]);

  // Switch back to active conversations when triggered externally (e.g., after unarchive)
  useEffect(() => {
    if (showActiveTrigger && showActiveTrigger > 0) {
      setShowArchived(false);
    }
  }, [showActiveTrigger]);

  // The conversations prop now contains both top-level conversations and
  // subagents (the patch stream emits one diff for the whole tree). Bucket
  // subagents under their parent so the drawer can render them inline.
  const subagentsByParent = useMemo(() => {
    const out: Record<string, ConversationWithState[]> = {};
    for (const conv of conversations) {
      if (conv.parent_conversation_id) {
        (out[conv.parent_conversation_id] ||= []).push(conv);
      }
    }
    for (const key of Object.keys(out)) {
      out[key] = sortConversationsByBucket(out[key]);
    }
    return out;
  }, [conversations]);

  // Auto-expand the parent when viewing one of its subagents.
  useEffect(() => {
    const parentId = viewedConversation?.parent_conversation_id;
    if (!showArchived && parentId) {
      setExpandedSubagents((prev) => (prev.has(parentId) ? prev : new Set([...prev, parentId])));
    }
  }, [viewedConversation, showArchived]);

  const toggleSubagents = (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    setExpandedSubagents((prev) => {
      const next = new Set(prev);
      if (next.has(conversationId)) {
        next.delete(conversationId);
      } else {
        next.add(conversationId);
      }
      return next;
    });
  };

  const loadArchivedConversations = async () => {
    setLoadingArchived(true);
    try {
      const archived = await api.getArchivedConversations();
      setArchivedConversations(archived);
    } catch (err) {
      console.error("Failed to load archived conversations:", err);
    } finally {
      setLoadingArchived(false);
    }
  };

  const formatDate = (timestamp: string) => {
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
  };

  // Format cwd with ~ for home directory (display only)
  const formatCwdForDisplay = tildifyPath;

  const getConversationPreview = (conversation: Conversation) => {
    if (conversation.slug) {
      return conversation.slug;
    }
    // Show full conversation ID
    return conversation.conversation_id;
  };

  const handleArchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    try {
      await api.archiveConversation(conversationId);
      onConversationArchived?.(conversationId);
      // Refresh archived list if viewing
      if (showArchived) {
        loadArchivedConversations();
      }
    } catch (err) {
      console.error("Failed to archive conversation:", err);
    }
  };

  const handleUnarchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    try {
      const conversation = await api.unarchiveConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
      onConversationUnarchived?.(conversation);
    } catch (err) {
      console.error("Failed to unarchive conversation:", err);
    }
  };

  const handleDelete = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    if (!confirm(t("confirmDelete"))) {
      return;
    }
    try {
      await api.deleteConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
    } catch (err) {
      console.error("Failed to delete conversation:", err);
    }
  };

  // Sanitize slug: lowercase, alphanumeric and hyphens only, max 60 chars
  const sanitizeSlug = (input: string): string => {
    return input
      .toLowerCase()
      .replace(/[\s_]+/g, "-")
      .replace(/[^a-z0-9-]+/g, "")
      .replace(/-+/g, "-")
      .replace(/^-|-$/g, "")
      .slice(0, 60)
      .replace(/-$/g, "");
  };

  const handleStartRename = (e: React.MouseEvent, conversation: Conversation) => {
    e.stopPropagation();
    setEditingId(conversation.conversation_id);
    setEditingSlug(conversation.slug || "");
    // Select all text after render
    setTimeout(() => renameInputRef.current?.select(), 0);
  };

  const handleRename = async (conversationId: string) => {
    const sanitized = sanitizeSlug(editingSlug);
    if (!sanitized) {
      setEditingId(null);
      return;
    }

    // Check for uniqueness against current conversations
    const isDuplicate = [...conversations, ...archivedConversations].some(
      (c) => c.slug === sanitized && c.conversation_id !== conversationId,
    );
    if (isDuplicate) {
      alert(t("duplicateName"));
      return;
    }

    try {
      const updated = await api.renameConversation(conversationId, sanitized);
      onConversationRenamed?.(updated);
      setEditingId(null);
    } catch (err) {
      console.error("Failed to rename conversation:", err);
    }
  };

  const handleRenameKeyDown = (e: React.KeyboardEvent, conversationId: string) => {
    // Don't submit while IME is composing (e.g., converting Japanese hiragana to kanji)
    if (e.nativeEvent.isComposing) {
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      handleRename(conversationId);
    } else if (e.key === "Escape") {
      setEditingId(null);
    }
  };

  useEffect(() => {
    return () => {
      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
    };
  }, []);

  const handleCopyGitHash = useCallback((e: React.MouseEvent, hash: string, convId: string) => {
    e.stopPropagation();
    navigator.clipboard
      .writeText(hash)
      .then(() => {
        setCopiedConvId(convId);
        if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
        copyTimeoutRef.current = setTimeout(() => setCopiedConvId(null), 1500);
      })
      .catch(() => {});
  }, []);

  const handleGroupByChange = (value: GroupBy) => {
    setGroupBy(value);
    localStorage.setItem("shelley-group-by", value);
    setCollapsedGroups(new Set());
  };

  const toggleGroup = (groupKey: string) => {
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(groupKey)) {
        next.delete(groupKey);
      } else {
        next.add(groupKey);
      }
      return next;
    });
  };

  const topLevelConversations = useMemo(
    () => sortConversationsByBucket(conversations.filter((c) => !c.parent_conversation_id)),
    [conversations],
  );
  // When a search query is active, the FTS results replace the normal list
  // entirely (they already include both active and archived hits, ordered
  // active-first by updated_at).
  const isSearching = searchQuery.trim().length > 0;
  const displayedConversations = isSearching
    ? (searchResults ?? [])
    : showArchived
      ? sortConversationsByBucket(archivedConversations)
      : topLevelConversations;

  // Compute grouped conversations
  const groupedConversations = useMemo(() => {
    if (groupBy === "none" || showArchived || isSearching) return null;

    const groups = new Map<string, { label: string; conversations: ConversationWithState[] }>();
    const ungrouped: ConversationWithState[] = [];

    for (const conv of topLevelConversations) {
      let key: string | null = null;

      if (groupBy === "cwd") {
        key = conv.cwd || null;
      } else if (groupBy === "git_repo") {
        // Prefer git_worktree_root (main repo) so worktrees group with their parent
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

    // Sort items within each group by 5-minute bucket of updated_at, then
    // by conversation_id, so groups are stable while conversations refresh.
    for (const [, group] of groups) {
      group.conversations = sortConversationsByBucket(group.conversations);
    }

    // Sort groups by the bucketed timestamp of their newest conversation.
    const sorted = [...groups.entries()].sort(
      (a, b) => maxBucket(b[1].conversations) - maxBucket(a[1].conversations),
    );

    if (ungrouped.length > 0) {
      sorted.push([
        "__ungrouped__",
        { label: t("other"), conversations: sortConversationsByBucket(ungrouped) },
      ]);
    }

    return sorted;
  }, [topLevelConversations, groupBy, showArchived, t]);

  const renderConversationItem = (conversation: Conversation | ConversationWithState) => {
    const convState = conversation as ConversationWithState;
    const isActive = conversation.conversation_id === currentConversationId;
    const conversationSubagents = subagentsByParent[conversation.conversation_id] || [];
    const subagentCount = conversationSubagents.length || convState.subagent_count || 0;
    const hasSubagents = subagentCount > 0;
    const isExpanded = expandedSubagents.has(conversation.conversation_id);
    // Use the per-row archived flag so search results (which mix active and
    // archived hits) render the correct action buttons.
    const itemArchived = conversation.archived;
    return (
      <React.Fragment key={conversation.conversation_id}>
        <div
          className={`conversation-item ${isActive ? "active" : ""}`}
          onClick={(e) => {
            if (handleModifiedClick(e, conversation)) return;
            onSelectConversation(conversation);
          }}
          onAuxClick={(e) => handleAuxClick(e, conversation)}
          style={{ cursor: "pointer" }}
        >
          <div className="drawer-conversation-item-flex-container">
            <div className="drawer-conversation-header-row">
              <div className="drawer-conversation-item-flex-container">
                {editingId === conversation.conversation_id ? (
                  <input
                    ref={renameInputRef}
                    type="text"
                    value={editingSlug}
                    onChange={(e) => setEditingSlug(e.target.value)}
                    onBlur={() => handleRename(conversation.conversation_id)}
                    onKeyDown={(e) => handleRenameKeyDown(e, conversation.conversation_id)}
                    onClick={(e) => e.stopPropagation()}
                    autoFocus
                    className="conversation-title drawer-rename-input"
                  />
                ) : (
                  <div className="conversation-title">{getConversationPreview(conversation)}</div>
                )}
              </div>
              {(conversation as ConversationWithState).working && (
                <span
                  className="working-indicator drawer-working-indicator"
                  title={t("agentIsWorking")}
                />
              )}
            </div>
            {convState.search_snippet ? (
              <div
                className="conversation-preview conversation-snippet"
                title={stripSnippetMarks(convState.search_snippet)}
              >
                {renderSnippet(convState.search_snippet)}
              </div>
            ) : (
              <div className="conversation-preview" title={convState.preview || undefined}>
                {convState.preview || "\u00a0"}
              </div>
            )}
            <div className="conversation-meta">
              <span className="conversation-date">{formatDate(conversation.updated_at)}</span>
              {conversation.cwd && groupBy !== "cwd" && (
                <span className="conversation-cwd" title={conversation.cwd}>
                  {formatCwdForDisplay(conversation.cwd)}
                </span>
              )}
              {!itemArchived && hasSubagents && (
                <button
                  onClick={(e) => toggleSubagents(e, conversation.conversation_id)}
                  className="subagent-count-badge"
                  title={isExpanded ? t("hideSubagents") : t("showSubagents")}
                  aria-label={isExpanded ? t("collapseSubagents") : t("expandSubagents")}
                >
                  <span className="drawer-subagent-count-badge-text">{subagentCount}</span>
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className={`drawer-subagent-chevron ${
                      isExpanded
                        ? "drawer-subagent-chevron-expanded"
                        : "drawer-subagent-chevron-collapsed"
                    }`}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M9 5l7 7-7 7"
                    />
                  </svg>
                </button>
              )}
              {!itemArchived && (
                <div className="conversation-actions drawer-actions-row">
                  <button
                    onClick={(e) => handleStartRename(e, conversation)}
                    className="btn-icon-sm"
                    title={t("rename")}
                    aria-label={t("rename")}
                  >
                    <svg
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                      className="drawer-icon-size"
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                      />
                    </svg>
                  </button>
                  <button
                    onClick={(e) => handleArchive(e, conversation.conversation_id)}
                    className="btn-icon-sm"
                    title={t("archive")}
                    aria-label={t("archive")}
                  >
                    <svg
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                      className="drawer-icon-size"
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
                      />
                    </svg>
                  </button>
                </div>
              )}
            </div>
            {convState.git_commit && (
              <div
                className={`conversation-git drawer-git-info ${
                  isActive ? "drawer-git-info-active" : ""
                }`}
              >
                <span
                  onClick={(e) =>
                    handleCopyGitHash(e, convState.git_commit!, conversation.conversation_id)
                  }
                  title={`Click to copy ${convState.git_commit}`}
                  className={`drawer-git-hash ${
                    copiedConvId === conversation.conversation_id ? "drawer-git-hash-copied" : ""
                  }`}
                >
                  {copiedConvId === conversation.conversation_id
                    ? "copied!".padEnd(convState.git_commit!.length, "\u00a0")
                    : convState.git_commit}
                </span>
                {convState.git_subject && (
                  <span title={convState.git_subject} className="drawer-git-subject">
                    {convState.git_subject}
                  </span>
                )}
              </div>
            )}
          </div>
          {itemArchived && (
            <div className="conversation-actions drawer-actions-row-offset">
              <button
                onClick={(e) => handleUnarchive(e, conversation.conversation_id)}
                className="btn-icon-sm"
                title={t("restore")}
                aria-label={t("restore")}
              >
                <svg
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                  className="drawer-icon-size"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                  />
                </svg>
              </button>
              <button
                onClick={(e) => handleDelete(e, conversation.conversation_id)}
                className="btn-icon-sm btn-danger"
                title={t("deletePermanently")}
                aria-label={t("delete_")}
              >
                <svg
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                  className="drawer-icon-size"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                  />
                </svg>
              </button>
            </div>
          )}
        </div>
        {/* Render subagents if expanded */}
        {!itemArchived && isExpanded && conversationSubagents.length > 0 && (
          <div className="subagent-list drawer-subagent-list">
            {conversationSubagents.map((sub) => {
              const isSubActive = sub.conversation_id === currentConversationId;
              return (
                <div
                  key={sub.conversation_id}
                  className={`conversation-item subagent-item drawer-subagent-item-style ${isSubActive ? "active" : ""}`}
                  onClick={(e) => {
                    if (handleModifiedClick(e, sub)) return;
                    onSelectConversation(sub);
                  }}
                  onAuxClick={(e) => handleAuxClick(e, sub)}
                >
                  <div className="drawer-conversation-item-flex-container">
                    <div className="drawer-conversation-header-row">
                      <div className="drawer-conversation-item-flex-container">
                        <div className="conversation-title">{sub.slug || sub.conversation_id}</div>
                      </div>
                      {sub.working && (
                        <span
                          className="working-indicator drawer-subagent-working-indicator"
                          title={t("subagentIsWorking")}
                        />
                      )}
                    </div>
                    <div className="conversation-preview" title={sub.preview || undefined}>
                      {sub.preview || "\u00a0"}
                    </div>
                    <div className="conversation-meta">
                      <span className="conversation-date drawer-subagent-date">
                        {formatDate(sub.updated_at)}
                      </span>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </React.Fragment>
    );
  };

  return (
    <>
      {/* Drawer */}
      <div className={`drawer ${isOpen ? "open" : ""} ${isCollapsed ? "collapsed" : ""}`}>
        {/* Header */}
        <div className="drawer-header">
          <h2 className="drawer-title">{showArchived ? t("archived") : t("conversations")}</h2>
          <div className="drawer-header-actions">
            {/* Group by button */}
            {!showArchived && (
              <div className="group-by-wrapper" ref={groupMenuRef}>
                <button
                  onClick={() => setGroupMenuOpen((v) => !v)}
                  className={`btn-icon${groupBy !== "none" ? " group-by-active" : ""}`}
                  aria-label={t("groupConversations")}
                  title={t("groupConversations")}
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10"
                    />
                  </svg>
                </button>
                {groupMenuOpen && (
                  <div className="group-by-menu">
                    {(["none", "cwd", "git_repo"] as GroupBy[]).map((value) => {
                      const labels: Record<GroupBy, string> = {
                        none: t("noGrouping"),
                        cwd: t("directory"),
                        git_repo: t("gitRepo"),
                      };
                      return (
                        <button
                          key={value}
                          className={`group-by-menu-item${groupBy === value ? " active" : ""}`}
                          onClick={() => {
                            handleGroupByChange(value);
                            setGroupMenuOpen(false);
                          }}
                        >
                          {labels[value]}
                        </button>
                      );
                    })}
                  </div>
                )}
              </div>
            )}
            {/* New conversation button - mobile only */}
            {!showArchived && (
              <button
                onClick={onNewConversation}
                className="btn-icon hide-on-desktop"
                aria-label={t("newConversation")}
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M12 4v16m8-8H4"
                  />
                </svg>
              </button>
            )}
            <button
              onClick={onClose}
              className="btn-icon hide-on-desktop"
              aria-label={t("closeConversations")}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M6 18L18 6M6 6l12 12"
                />
              </svg>
            </button>
            {/* Collapse button - desktop only */}
            <button
              onClick={onToggleCollapse}
              className="btn-icon show-on-desktop-only"
              aria-label={t("collapseSidebar")}
              title={t("collapseSidebar")}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M11 19l-7-7 7-7m8 14l-7-7 7-7"
                />
              </svg>
            </button>
          </div>
        </div>

        {/* Search bar — FTS over slug + message content, includes archived */}
        <div className="drawer-search">
          <svg
            className="drawer-search-icon"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            width="16"
            height="16"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
            />
          </svg>
          <input
            type="text"
            className="drawer-search-input"
            placeholder={t("searchConversations")}
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && searchQuery) {
                e.preventDefault();
                setSearchQuery("");
              }
            }}
            aria-label={t("searchConversations")}
          />
          {searchQuery && (
            <button
              type="button"
              className="drawer-search-clear"
              onClick={() => setSearchQuery("")}
              aria-label={t("clearSearch")}
              title={t("clearSearch")}
            >
              ✕
            </button>
          )}
        </div>

        {/* Conversations list */}
        <div className="drawer-body scrollable">
          {isSearching && searching && searchResults === null ? (
            <div className="text-secondary drawer-empty-state">
              <p>{t("searching")}</p>
            </div>
          ) : loadingArchived && showArchived && !isSearching ? (
            <div className="text-secondary drawer-empty-state">
              <p>{t("loading")}</p>
            </div>
          ) : displayedConversations.length === 0 ? (
            <div className="text-secondary drawer-empty-state">
              <p>
                {isSearching
                  ? t("noSearchResults")
                  : showArchived
                    ? t("noArchivedConversations")
                    : t("noConversationsYet")}
              </p>
              {!showArchived && !isSearching && (
                <p className="text-sm drawer-empty-state-hint">{t("startNewToGetStarted")}</p>
              )}
            </div>
          ) : groupedConversations ? (
            <div className="conversation-list">
              {groupedConversations.map(([key, group]) => {
                const isCollapsed = collapsedGroups.has(key);
                return (
                  <div key={key} className="conversation-group">
                    <button className="conversation-group-header" onClick={() => toggleGroup(key)}>
                      <svg
                        fill="none"
                        stroke="currentColor"
                        viewBox="0 0 24 24"
                        className="conversation-group-chevron"
                        style={{
                          transform: isCollapsed ? "rotate(-90deg)" : "rotate(0deg)",
                        }} // Dynamic: transform depends on isCollapsed state
                      >
                        <path
                          strokeLinecap="round"
                          strokeLinejoin="round"
                          strokeWidth={2}
                          d="M19 9l-7 7-7-7"
                        />
                      </svg>
                      <span
                        className="conversation-group-label"
                        title={key === "__ungrouped__" ? undefined : key}
                      >
                        {group.label}
                      </span>
                      <span className="conversation-group-count">{group.conversations.length}</span>
                    </button>
                    {!isCollapsed && group.conversations.map(renderConversationItem)}
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="conversation-list">
              {displayedConversations.map(renderConversationItem)}
            </div>
          )}
        </div>

        {/* Footer with archived toggle */}
        <div className="drawer-footer">
          <button
            onClick={() => setShowArchived(!showArchived)}
            className="btn-secondary drawer-footer-button"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="drawer-icon-size">
              {showArchived ? (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M11 15l-3-3m0 0l3-3m-3 3h8M3 12a9 9 0 1118 0 9 9 0 01-18 0z"
                />
              ) : (
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
                />
              )}
            </svg>
            <span>{showArchived ? t("backToConversations") : t("viewArchived")}</span>
          </button>
        </div>
      </div>
    </>
  );
}

export default ConversationDrawer;
