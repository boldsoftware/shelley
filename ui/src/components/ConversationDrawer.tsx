import React, { useState, useEffect, useMemo, useCallback, useRef } from "react";
import { Conversation, ConversationWithState } from "../types";
import { api } from "../services/api";
import { useI18n } from "../i18n";
import {
  sortConversationsByBucket,
  maxBucket,
  applyStableOrder,
  applyStableKeyOrder,
  neighborAfterRemoval,
} from "../utils/conversationSort";
import { tildifyPath } from "../utils/tildify";
import { handleModifiedNavClick } from "../utils/openInNewTab";
import { cn } from "@/lib/utils";

type GroupBy = "none" | "cwd" | "git_repo";

// Small icon-button used for the per-row inline actions (rename, tag, archive,
// delete, restore). Muted and dimmed at rest, revealed on row hover via the
// `group-hover` utilities on the parent conversation row, and fully opaque +
// legible when the row is active (selected).
const ROW_ICON_BTN = cn(
  "flex items-center justify-center rounded p-1 text-muted-foreground opacity-40 transition-[background-color,opacity]",
  "hover:bg-accent hover:opacity-100",
  "group-hover/item:opacity-70",
  "group-[.is-active]/item:text-primary-foreground/80 group-[.is-active]/item:opacity-70",
  "group-[.is-active]/item:hover:bg-primary-foreground/20 group-[.is-active]/item:hover:opacity-100",
);

// Danger variant of the row icon button (delete).
const ROW_ICON_BTN_DANGER = cn(ROW_ICON_BTN, "hover:bg-destructive hover:text-white");

// Standard 1rem stroke icon used throughout the drawer rows.
const ROW_ICON = "size-4";

// Parses the JSON-encoded tags field on a Conversation. Tolerates the empty
// string and malformed JSON (treated as no tags) so we never crash the
// drawer on a stale or partial conversation object.
function parseTags(conversation: Conversation): string[] {
  if (!conversation.tags) return [];
  try {
    const parsed = JSON.parse(conversation.tags);
    return Array.isArray(parsed) ? parsed.filter((t) => typeof t === "string") : [];
  } catch {
    return [];
  }
}

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
      <mark
        key={key++}
        className="rounded-[2px] bg-amber-200 px-px font-semibold text-black not-italic dark:bg-amber-700 dark:text-white"
      >
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
  onConversationArchived?: (id: string, nextConversation?: Conversation | null) => void;
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
  // ID of the conversation whose tag editor popover is open. Only one row's
  // popover may be open at a time; clicking the tag button on another row
  // (or anywhere outside) closes it.
  const [tagEditorId, setTagEditorId] = useState<string | null>(null);
  const [tagInput, setTagInput] = useState("");
  const tagEditorRef = React.useRef<HTMLDivElement>(null);
  const tagInputRef = React.useRef<HTMLInputElement>(null);
  const [expandedSubagents, setExpandedSubagents] = useState<Set<string>>(new Set());
  const [groupBy, setGroupBy] = useState<GroupBy>(() => {
    const stored = localStorage.getItem("shelley-group-by");
    return stored === "cwd" || stored === "git_repo" ? stored : "none";
  });
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set());
  const [groupMenuOpen, setGroupMenuOpen] = useState(false);
  // Bumping resortKey resets all the stable-order refs so the drawer
  // re-sorts strictly by updated_at bucket again. The user triggers this
  // from the grouping menu when they want to refresh the order.
  const [resortKey, setResortKey] = useState(0);
  // Refs holding the current display order so updates to existing items
  // don't shuffle the list. Brand-new items prepend at the top.
  const topOrderRef = React.useRef<string[]>([]);
  const archivedOrderRef = React.useRef<string[]>([]);
  const subagentOrderRef = React.useRef<Record<string, string[]>>({});
  const groupOrderRef = React.useRef<Record<string, string[]>>({});
  const groupKeysOrderRef = React.useRef<string[]>([]);
  // Flat top-to-bottom order of the currently visible active conversations
  // (accounting for grouping), used to pick the next selection when one is
  // archived. Kept in a ref so the archive handler reads the latest order.
  const flatVisualOrderRef = React.useRef<Conversation[]>([]);
  // Tracks the resortKey the order refs were last computed against. When
  // it changes, the next useMemo for each list passes an empty prev-order
  // so the user-visible sort is refreshed in the same render cycle.
  const lastResortKeyRef = React.useRef(0);
  if (lastResortKeyRef.current !== resortKey) {
    topOrderRef.current = [];
    archivedOrderRef.current = [];
    subagentOrderRef.current = {};
    groupOrderRef.current = {};
    groupKeysOrderRef.current = [];
    lastResortKeyRef.current = resortKey;
  }
  // Track conversation ids we've seen in the current view so we can animate
  // newly-added rows. Updated in an effect to avoid render-time side effects.
  // Initialized lazily after first commit so the drawer doesn't animate the
  // entire list when it first mounts.
  const [seenIds, setSeenIds] = useState<Set<string> | null>(null);
  const [copiedConvId, setCopiedConvId] = useState<string | null>(null);
  // ID of the conversation/draft whose trash icon was clicked; we render an
  // inline confirm/cancel pair in place of the trash icon instead of using a
  // window.confirm() alert.
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);
  const pendingDeleteRef = useRef<HTMLDivElement>(null);
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
    const nextOrder: Record<string, string[]> = {};
    for (const key of Object.keys(out)) {
      const sorted = sortConversationsByBucket(out[key]);
      const { items, order } = applyStableOrder(sorted, subagentOrderRef.current[key] || []);
      out[key] = items;
      nextOrder[key] = order;
    }
    subagentOrderRef.current = nextOrder;
    return out;
  }, [conversations, resortKey]);

  // Track which conversation ids are currently in the list so newly-added
  // rows can animate in. We snapshot the current set after each render. On
  // the first run, we record the existing ids without setting them as
  // "seen" until *next* render, so initial mount doesn't animate the whole
  // list — only ids that arrive after mount are treated as new.
  useEffect(() => {
    const ids = new Set<string>();
    for (const c of conversations) ids.add(c.conversation_id);
    for (const c of archivedConversations) ids.add(c.conversation_id);
    setSeenIds((prev) => {
      if (prev && prev.size === ids.size) {
        let same = true;
        for (const id of ids) {
          if (!prev.has(id)) {
            same = false;
            break;
          }
        }
        if (same) return prev;
      }
      return ids;
    });
  }, [conversations, archivedConversations]);

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

  // Render the conversation title. If there's no slug yet (the title hasn't
  // come in), show an italic "untitled" placeholder. Used for both top-level
  // and subagent items.
  const renderConversationTitle = (conversation: Conversation) => {
    if (conversation.slug) return conversation.slug;
    return <em>untitled</em>;
  };

  const handleArchive = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    // Determine which conversation should be selected after this one is
    // archived: the one immediately below it in the visible list, or, if it's
    // the last item, the one immediately above it. Uses the flat visual order
    // (which accounts for grouping) so selection matches what the user sees.
    const nextConversation = neighborAfterRemoval(flatVisualOrderRef.current, conversationId);
    try {
      await api.archiveConversation(conversationId);
      onConversationArchived?.(conversationId, nextConversation);
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

  const handleDeleteClick = (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    setPendingDeleteId(conversationId);
  };

  const handleConfirmDelete = async (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    setPendingDeleteId(null);
    try {
      await api.deleteConversation(conversationId);
      setArchivedConversations((prev) => prev.filter((c) => c.conversation_id !== conversationId));
    } catch (err) {
      console.error("Failed to delete conversation:", err);
    }
  };

  const handleCancelDelete = (e: React.MouseEvent) => {
    e.stopPropagation();
    setPendingDeleteId(null);
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

  // Dismiss the inline delete confirmation on any outside click.
  useEffect(() => {
    if (!pendingDeleteId) return;
    const onMouseDown = (e: MouseEvent) => {
      if (pendingDeleteRef.current && !pendingDeleteRef.current.contains(e.target as Node)) {
        setPendingDeleteId(null);
      }
    };
    document.addEventListener("mousedown", onMouseDown);
    return () => document.removeEventListener("mousedown", onMouseDown);
  }, [pendingDeleteId]);

  // Close the tag editor when the user clicks outside it. We attach the
  // listener only while a popover is open to avoid global mousedown overhead.
  useEffect(() => {
    if (!tagEditorId) return;
    const onMouseDown = (e: MouseEvent) => {
      if (tagEditorRef.current && !tagEditorRef.current.contains(e.target as Node)) {
        setTagEditorId(null);
        setTagInput("");
      }
    };
    document.addEventListener("mousedown", onMouseDown);
    return () => document.removeEventListener("mousedown", onMouseDown);
  }, [tagEditorId]);

  const handleOpenTagEditor = (e: React.MouseEvent, conversationId: string) => {
    e.stopPropagation();
    setTagEditorId((prev) => (prev === conversationId ? null : conversationId));
    setTagInput("");
    setTimeout(() => tagInputRef.current?.focus(), 0);
  };

  // Persist a new tags array for a conversation, trimming/deduping locally so
  // the UI reflects the same normalization the server applies.
  const saveTags = async (conversationId: string, tags: string[]) => {
    const normalized: string[] = [];
    const seen = new Set<string>();
    for (const t of tags) {
      const trimmed = t.trim();
      if (!trimmed || seen.has(trimmed)) continue;
      seen.add(trimmed);
      normalized.push(trimmed);
    }
    try {
      const updated = await api.updateConversationTags(conversationId, normalized);
      onConversationRenamed?.(updated);
    } catch (err) {
      console.error("Failed to update tags:", err);
    }
  };

  const handleAddTag = async (conversation: Conversation) => {
    // Tags render with a leading '#'. Accept '#wip' typed verbatim by
    // stripping any leading hashes so we don't end up storing '##wip'.
    const value = tagInput.trim().replace(/^#+/, "");
    if (!value) return;
    const current = parseTags(conversation);
    if (current.includes(value)) {
      setTagInput("");
      return;
    }
    setTagInput("");
    await saveTags(conversation.conversation_id, [...current, value]);
  };

  const handleRemoveTag = async (conversation: Conversation, tag: string) => {
    const current = parseTags(conversation);
    await saveTags(
      conversation.conversation_id,
      current.filter((t) => t !== tag),
    );
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

  const topLevelConversations = useMemo(() => {
    const sorted = sortConversationsByBucket(
      conversations.filter((c) => !c.parent_conversation_id),
    );
    const { items, order } = applyStableOrder(sorted, topOrderRef.current);
    topOrderRef.current = order;
    return items;
  }, [conversations, resortKey]);

  // Number drafts so multiple drafts get unique labels: oldest visible
  // draft is unnumbered ("draft"), subsequent ones are "draft 2", etc.
  // Labels are pinned in a ref so deleting an older draft doesn't reshuffle
  // the numbers on the rest.
  const draftLabelsRef = useRef<Record<string, number>>({});
  const draftLabels = useMemo(() => {
    const drafts = conversations.filter((c) => c.is_draft);
    const pinned = draftLabelsRef.current;
    const used = new Set<number>();
    for (const d of drafts) {
      const n = pinned[d.conversation_id];
      if (n !== undefined) used.add(n);
    }
    // Assign new drafts the lowest unused slot, in creation order.
    const unpinned = drafts
      .filter((d) => pinned[d.conversation_id] === undefined)
      .sort((a, b) => (a.created_at < b.created_at ? -1 : 1));
    let next = 1;
    for (const d of unpinned) {
      while (used.has(next)) next++;
      pinned[d.conversation_id] = next;
      used.add(next);
    }
    // Drop labels for drafts that no longer exist (deleted or promoted).
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
  }, [conversations]);
  const stableArchivedConversations = useMemo(() => {
    const sorted = sortConversationsByBucket(archivedConversations);
    const { items, order } = applyStableOrder(sorted, archivedOrderRef.current);
    archivedOrderRef.current = order;
    return items;
  }, [archivedConversations, resortKey]);
  // When a search query is active, the FTS results replace the normal list
  // entirely (they already include both active and archived hits, ordered
  // active-first by updated_at). Search results are NOT held by the stable
  // order — they're ranked by the server and should appear ranked.
  const isSearching = searchQuery.trim().length > 0;

  const displayedConversations = isSearching
    ? (searchResults ?? [])
    : showArchived
      ? stableArchivedConversations
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

    // Within each group, apply stable order so individual conversations don't
    // shuffle as they update; new ones still surface at the top of the group.
    const nextGroupOrder: Record<string, string[]> = {};
    for (const [key, group] of groups) {
      const sorted = sortConversationsByBucket(group.conversations);
      const { items, order } = applyStableOrder(sorted, groupOrderRef.current[key] || []);
      group.conversations = items;
      nextGroupOrder[key] = order;
    }

    // Initial sort: groups newest-first by bucketed timestamp.
    const desiredKeys = [...groups.entries()]
      .sort((a, b) => maxBucket(b[1].conversations) - maxBucket(a[1].conversations))
      .map(([k]) => k);
    // Hold the group ordering stable across updates the same way we do for
    // individual conversations: existing groups keep their position; new
    // groups appear at the top.
    const stableKeys = applyStableKeyOrder(desiredKeys, groupKeysOrderRef.current);
    groupKeysOrderRef.current = stableKeys;
    const sorted: [string, { label: string; conversations: ConversationWithState[] }][] =
      stableKeys.map((k) => [k, groups.get(k)!]);

    if (ungrouped.length > 0) {
      const ungroupedSorted = sortConversationsByBucket(ungrouped);
      const { items, order } = applyStableOrder(
        ungroupedSorted,
        groupOrderRef.current["__ungrouped__"] || [],
      );
      nextGroupOrder["__ungrouped__"] = order;
      sorted.push(["__ungrouped__", { label: t("other"), conversations: items }]);
    }

    groupOrderRef.current = nextGroupOrder;
    return sorted;
  }, [topLevelConversations, groupBy, showArchived, t, resortKey]);

  // Maintain the flat visual order of the currently displayed conversations
  // for archive-based next-selection. When grouped, flatten groups in their
  // displayed order; otherwise use the displayed list (which honors search and
  // archived views), so the chosen neighbor matches what the user sees.
  flatVisualOrderRef.current = groupedConversations
    ? groupedConversations.flatMap(([, group]) => group.conversations)
    : displayedConversations;

  const renderDeleteButton = (conversationId: string) => {
    if (pendingDeleteId === conversationId) {
      return (
        <div
          className="inline-flex items-center gap-1 rounded bg-muted px-1 py-0.5 group-[.is-active]/item:bg-primary-foreground/15"
          ref={pendingDeleteRef}
          onClick={(e) => e.stopPropagation()}
          title={t("confirmDelete")}
        >
          <span className="whitespace-nowrap text-xs text-muted-foreground group-[.is-active]/item:text-primary-foreground/90">
            {t("confirmDeleteShort")}
          </span>
          <button
            type="button"
            onClick={(e) => handleConfirmDelete(e, conversationId)}
            className="flex items-center justify-center rounded p-1 text-destructive opacity-100 transition-colors hover:bg-destructive hover:text-white"
            title={t("delete_")}
            aria-label={t("delete_")}
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M5 13l4 4L19 7"
              />
            </svg>
          </button>
          <button
            type="button"
            onClick={handleCancelDelete}
            className="flex items-center justify-center rounded p-1 text-muted-foreground opacity-100 transition-colors hover:bg-accent hover:text-foreground"
            title={t("cancel")}
            aria-label={t("cancel")}
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>
      );
    }
    return (
      <button
        onClick={(e) => handleDeleteClick(e, conversationId)}
        className={ROW_ICON_BTN_DANGER}
        title={t("deletePermanently")}
        aria-label={t("delete_")}
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
          />
        </svg>
      </button>
    );
  };

  const renderConversationItem = (conversation: Conversation | ConversationWithState) => {
    const convState = conversation as ConversationWithState;
    // Drafts are real conversations with is_draft=true and no messages.
    // They render with an italic 'draft' title in place of the slug, hide
    // rename/archive (neither makes sense pre-promotion), expose a delete
    // button, and use a preview drawn from the draft body.
    const isDraft = !!conversation.is_draft;
    const isActive = conversation.conversation_id === currentConversationId;
    const conversationSubagents = isDraft
      ? []
      : subagentsByParent[conversation.conversation_id] || [];
    const subagentCount = isDraft
      ? 0
      : conversationSubagents.length || convState.subagent_count || 0;
    const hasSubagents = subagentCount > 0;
    const isExpanded = expandedSubagents.has(conversation.conversation_id);
    // Use the per-row archived flag so search results (which mix active and
    // archived hits) render the correct action buttons.
    const itemArchived = conversation.archived;
    // Treat the first render (seenIds === null) as "everything already
    // seen" so we don't animate the entire list on initial mount. The draft
    // row also never animates — it appears as soon as you hit the new view.
    const isNew = !isDraft && seenIds !== null && !seenIds.has(conversation.conversation_id);
    const conversationTags = isDraft ? [] : parseTags(conversation);
    return (
      <React.Fragment key={conversation.conversation_id}>
        <div
          className={cn(
            "conversation-item group/item mb-1 flex w-full cursor-pointer flex-row items-center gap-1 rounded-lg p-3 text-left transition-colors",
            isActive
              ? "is-active active bg-primary text-primary-foreground"
              : "hover:bg-accent hover:text-accent-foreground",
            isNew && "motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-top-1",
          )}
          onClick={(e) => {
            if (handleModifiedClick(e, conversation)) return;
            onSelectConversation(conversation);
          }}
          onAuxClick={(e) => handleAuxClick(e, conversation)}
        >
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <div className="min-w-0 flex-1">
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
                    className="w-full border-0 border-b border-current/50 bg-transparent p-0 text-sm font-medium outline-none"
                  />
                ) : isDraft ? (
                  <div className="text-sm font-medium break-all italic">
                    {draftLabels[conversation.conversation_id] || "draft"}
                  </div>
                ) : (
                  <div className="conversation-title text-sm font-medium break-all">
                    {renderConversationTitle(conversation)}
                  </div>
                )}
              </div>
              {(conversation as ConversationWithState).working && (
                <span
                  className="size-2 shrink-0 rounded-full border-[1.5px] border-current bg-transparent opacity-75"
                  title={t("agentIsWorking")}
                />
              )}
            </div>
            {(() => {
              const editing = !isDraft && tagEditorId === conversation.conversation_id;
              if (!editing && conversationTags.length === 0) return null;
              return (
                <div
                  className={cn(
                    "mt-1 flex flex-wrap items-center gap-1.5",
                    editing && "rounded bg-muted/40 p-1 group-[.is-active]/item:bg-primary-foreground/10",
                  )}
                  onClick={editing ? (e) => e.stopPropagation() : undefined}
                  ref={editing ? tagEditorRef : undefined}
                >
                  {conversationTags.map((tag) =>
                    editing ? (
                      <span
                        key={tag}
                        className="group/tag inline-flex max-w-48 items-center overflow-hidden text-xs leading-tight text-ellipsis whitespace-nowrap text-primary group-[.is-active]/item:text-primary-foreground/85"
                      >
                        <span className="mr-px opacity-60">#</span>
                        {tag}
                        <button
                          type="button"
                          className="ml-0.5 cursor-pointer px-0.5 leading-none opacity-0 transition-opacity group-hover/tag:opacity-70 hover:!opacity-100 focus:opacity-70"
                          aria-label={`${t("removeTag")} ${tag}`}
                          title={t("removeTag")}
                          onClick={() => handleRemoveTag(conversation, tag)}
                        >
                          ×
                        </button>
                      </span>
                    ) : (
                      <span
                        key={tag}
                        className="inline-flex max-w-48 items-center overflow-hidden text-xs leading-tight text-ellipsis whitespace-nowrap text-primary group-[.is-active]/item:text-primary-foreground/85"
                        title={`#${tag}`}
                      >
                        <span className="mr-px opacity-60">#</span>
                        {tag}
                      </span>
                    ),
                  )}
                  {editing && (
                    <form
                      className="inline-flex min-w-16 flex-1"
                      onSubmit={(e) => {
                        e.preventDefault();
                        handleAddTag(conversation);
                      }}
                    >
                      <span className="mr-px opacity-60">#</span>
                      <input
                        ref={tagInputRef}
                        type="text"
                        value={tagInput}
                        onChange={(e) => setTagInput(e.target.value)}
                        placeholder={t("addTagPlaceholder")}
                        className="min-w-16 flex-1 border-0 bg-transparent px-0.5 text-xs leading-snug text-inherit outline-none placeholder:opacity-50"
                        onKeyDown={(e) => {
                          if (e.key === "Escape") {
                            e.preventDefault();
                            setTagEditorId(null);
                            setTagInput("");
                          }
                        }}
                      />
                    </form>
                  )}
                </div>
              );
            })()}
            {convState.search_snippet ? (
              <div
                className="mt-0.5 line-clamp-2 text-xs break-words text-muted-foreground italic group-[.is-active]/item:text-primary-foreground/80"
                title={stripSnippetMarks(convState.search_snippet)}
              >
                {renderSnippet(convState.search_snippet)}
              </div>
            ) : isDraft ? (
              <div
                className="mt-0.5 truncate text-xs text-muted-foreground group-[.is-active]/item:text-primary-foreground/80"
                title={conversation.draft?.trim() || undefined}
              >
                {conversation.draft?.trim() || " "}
              </div>
            ) : (
              <div
                className="mt-0.5 truncate text-xs text-muted-foreground group-[.is-active]/item:text-primary-foreground/80"
                title={convState.preview || undefined}
              >
                {convState.preview || " "}
              </div>
            )}
            <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground group-[.is-active]/item:text-primary-foreground/80">
              <span className="shrink-0">{formatDate(conversation.updated_at)}</span>
              {conversation.cwd && groupBy !== "cwd" && (
                <span
                  className="min-w-0 truncate text-left font-mono text-xs opacity-80"
                  title={conversation.cwd}
                >
                  {formatCwdForDisplay(conversation.cwd)}
                </span>
              )}
              {!isDraft && !itemArchived && hasSubagents && (
                <button
                  onClick={(e) => toggleSubagents(e, conversation.conversation_id)}
                  className="ml-auto inline-flex shrink-0 items-center gap-0.5 rounded bg-muted px-1 leading-snug text-muted-foreground transition-colors hover:bg-accent hover:text-foreground group-[.is-active]/item:bg-primary-foreground/15 group-[.is-active]/item:text-primary-foreground/80 group-[.is-active]/item:hover:bg-primary-foreground/25 group-[.is-active]/item:hover:text-primary-foreground"
                  title={isExpanded ? t("hideSubagents") : t("showSubagents")}
                  aria-label={isExpanded ? t("collapseSubagents") : t("expandSubagents")}
                >
                  <span className="font-medium">{subagentCount}</span>
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className={cn(
                      "size-2.5 transition-transform",
                      isExpanded ? "rotate-90" : "rotate-0",
                    )}
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
              {isDraft && (
                <div className="ml-auto flex gap-1">
                  {renderDeleteButton(conversation.conversation_id)}
                </div>
              )}
              {!isDraft && !itemArchived && (
                <div className="ml-auto flex gap-1">
                  <button
                    onClick={(e) => handleStartRename(e, conversation)}
                    className={ROW_ICON_BTN}
                    title={t("rename")}
                    aria-label={t("rename")}
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                      />
                    </svg>
                  </button>
                  <button
                    onClick={(e) => handleOpenTagEditor(e, conversation.conversation_id)}
                    className={ROW_ICON_BTN}
                    title={t("editTags")}
                    aria-label={t("editTags")}
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M7 7h.01M7 3h5a1.99 1.99 0 011.414.586l7 7a2 2 0 010 2.828l-7 7a2 2 0 01-2.828 0l-7-7A1.99 1.99 0 013 12V7a4 4 0 014-4z"
                      />
                    </svg>
                  </button>
                  <button
                    onClick={(e) => handleArchive(e, conversation.conversation_id)}
                    className={ROW_ICON_BTN}
                    title={t("archive")}
                    aria-label={t("archive")}
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
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
                className={cn(
                  "flex items-center gap-[0.3em] text-xs text-muted-foreground",
                  isActive && "text-primary-foreground/70",
                )}
              >
                <span
                  onClick={(e) =>
                    handleCopyGitHash(e, convState.git_commit!, conversation.conversation_id)
                  }
                  title={`Click to copy ${convState.git_commit}`}
                  className={cn(
                    "cursor-pointer font-mono",
                    copiedConvId === conversation.conversation_id &&
                      "text-emerald-600 dark:text-emerald-500",
                  )}
                >
                  {copiedConvId === conversation.conversation_id
                    ? "copied!".padEnd(convState.git_commit!.length, " ")
                    : convState.git_commit}
                </span>
                {convState.git_subject && (
                  <span title={convState.git_subject} className="min-w-0 truncate">
                    {convState.git_subject}
                  </span>
                )}
              </div>
            )}
          </div>
          {itemArchived && (
            <div className="ml-2 flex gap-1">
              <button
                onClick={(e) => handleUnarchive(e, conversation.conversation_id)}
                className={ROW_ICON_BTN}
                title={t("restore")}
                aria-label={t("restore")}
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                  />
                </svg>
              </button>
              {renderDeleteButton(conversation.conversation_id)}
            </div>
          )}
        </div>
        {/* Render subagents if expanded */}
        {!itemArchived && isExpanded && conversationSubagents.length > 0 && (
          <div className="ml-6">
            {conversationSubagents.map((sub) => {
              const isSubActive = sub.conversation_id === currentConversationId;
              return (
                <div
                  key={sub.conversation_id}
                  className={cn(
                    "conversation-item group/item mb-1 flex w-full cursor-pointer flex-row items-center gap-1 rounded-lg border-l-2 border-border py-3 pr-3 pl-2 text-left text-sm transition-colors",
                    isSubActive
                      ? "is-active bg-primary text-primary-foreground"
                      : "hover:bg-accent hover:text-accent-foreground",
                    seenIds !== null &&
                      !seenIds.has(sub.conversation_id) &&
                      "motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-top-1",
                  )}
                  onClick={(e) => {
                    if (handleModifiedClick(e, sub)) return;
                    onSelectConversation(sub);
                  }}
                  onAuxClick={(e) => handleAuxClick(e, sub)}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <div className="min-w-0 flex-1">
                        <div className="conversation-title text-sm font-medium break-all">
                          {renderConversationTitle(sub)}
                        </div>
                      </div>
                      {sub.working && (
                        <span
                          className="size-[7px] shrink-0 rounded-full border-[1.5px] border-current bg-transparent opacity-75"
                          title={t("subagentIsWorking")}
                        />
                      )}
                    </div>
                    <div
                      className="mt-0.5 truncate text-xs text-muted-foreground group-[.is-active]/item:text-primary-foreground/80"
                      title={sub.preview || undefined}
                    >
                      {sub.preview || " "}
                    </div>
                    <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground group-[.is-active]/item:text-primary-foreground/80">
                      <span className="shrink-0 text-sm">{formatDate(sub.updated_at)}</span>
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
      {/* Drawer: fixed slide-in overlay on mobile, static persistent panel on
          desktop (md+). Collapsing only applies on desktop. */}
      <div
        className={cn(
          "drawer fixed inset-y-0 left-0 z-50 flex h-dvh w-80 max-w-[calc(100vw-3rem)] flex-col border-r border-border bg-background text-foreground transition-transform duration-300 ease-in-out",
          "[padding-top:env(safe-area-inset-top)] [padding-left:env(safe-area-inset-left)] [padding-bottom:env(safe-area-inset-bottom)]",
          isOpen ? "open translate-x-0" : "-translate-x-full",
          "md:static md:h-full md:!translate-x-0",
          isCollapsed && "md:hidden",
        )}
      >
        {/* Header */}
        <div className="flex items-center justify-between p-4">
          <h2 className="text-xl font-semibold">
            {showArchived ? t("archived") : t("conversations")}
          </h2>
          <div className="flex items-center gap-1">
            {/* Group by button */}
            {!showArchived && (
              <div className="relative" ref={groupMenuRef}>
                <button
                  onClick={() => setGroupMenuOpen((v) => !v)}
                  className={cn(
                    "flex items-center justify-center rounded-md p-2 transition-colors hover:bg-accent",
                    groupBy !== "none" && "text-primary",
                  )}
                  aria-label={t("groupConversations")}
                  title={t("groupConversations")}
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="size-5">
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10"
                    />
                  </svg>
                </button>
                {groupMenuOpen && (
                  <div className="absolute top-full right-0 z-50 mt-1 min-w-32 rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-lg">
                    {(["none", "cwd", "git_repo"] as GroupBy[]).map((value) => {
                      const labels: Record<GroupBy, string> = {
                        none: t("noGrouping"),
                        cwd: t("directory"),
                        git_repo: t("gitRepo"),
                      };
                      return (
                        <button
                          key={value}
                          className={cn(
                            "flex w-full items-center gap-1.5 rounded-sm px-2.5 py-1.5 text-left text-sm transition-colors hover:bg-accent",
                            groupBy === value && "font-semibold text-primary",
                          )}
                          onClick={() => {
                            handleGroupByChange(value);
                            setGroupMenuOpen(false);
                          }}
                        >
                          {labels[value]}
                        </button>
                      );
                    })}
                    <div className="mx-1 my-1 h-px bg-border" />
                    <button
                      className="flex w-full items-center gap-1.5 rounded-sm px-2.5 py-1.5 text-left text-sm transition-colors hover:bg-accent"
                      onClick={() => {
                        setResortKey((n) => n + 1);
                        setGroupMenuOpen(false);
                      }}
                      title={t("resortNow")}
                    >
                      <svg
                        fill="none"
                        stroke="currentColor"
                        viewBox="0 0 24 24"
                        className="size-3.5 shrink-0"
                        aria-hidden="true"
                      >
                        <path
                          strokeLinecap="round"
                          strokeLinejoin="round"
                          strokeWidth={2}
                          d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                        />
                      </svg>
                      {t("resortNow")}
                    </button>
                  </div>
                )}
              </div>
            )}
            {/* New conversation button - mobile only */}
            {!showArchived && (
              <button
                onClick={(e) => {
                  if (handleModifiedNavClick(e, "/new")) return;
                  onNewConversation();
                }}
                className="flex items-center justify-center rounded-md p-2 transition-colors hover:bg-accent md:hidden"
                aria-label={t("newConversation")}
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="size-5">
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
              className="flex items-center justify-center rounded-md p-2 transition-colors hover:bg-accent md:hidden"
              aria-label={t("closeConversations")}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="size-5">
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
              className="hidden items-center justify-center rounded-md p-2 transition-colors hover:bg-accent md:flex"
              aria-label={t("collapseSidebar")}
              title={t("collapseSidebar")}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="size-5">
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
        <div className="relative flex items-center px-4 pb-2">
          <svg
            className="pointer-events-none absolute left-[1.625rem] text-muted-foreground"
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
            className="flex-1 rounded-md border border-border bg-background py-1.5 pr-7 pl-[2.1rem] text-sm text-foreground outline-none focus:border-ring focus:ring-1 focus:ring-ring"
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
              className="absolute right-[1.4rem] px-1 py-0.5 text-sm leading-none text-muted-foreground hover:text-foreground"
              onClick={() => setSearchQuery("")}
              aria-label={t("clearSearch")}
              title={t("clearSearch")}
            >
              ✕
            </button>
          )}
        </div>

        {/* Conversations list */}
        <div className="min-h-0 flex-1 overflow-y-auto">
          {isSearching && searching && searchResults === null ? (
            <div className="p-4 text-center text-muted-foreground">
              <p>{t("searching")}</p>
            </div>
          ) : loadingArchived && showArchived && !isSearching ? (
            <div className="p-4 text-center text-muted-foreground">
              <p>{t("loading")}</p>
            </div>
          ) : displayedConversations.length === 0 ? (
            <div className="p-4 text-center text-muted-foreground">
              <p>
                {isSearching
                  ? t("noSearchResults")
                  : showArchived
                    ? t("noArchivedConversations")
                    : t("noConversationsYet")}
              </p>
              {!showArchived && !isSearching && (
                <p className="mt-1 text-sm">{t("startNewToGetStarted")}</p>
              )}
            </div>
          ) : groupedConversations ? (
            <div className="p-2">
              {groupedConversations.map(([key, group]) => {
                const isCollapsed = collapsedGroups.has(key);
                return (
                  <div key={key} className="conversation-group mb-1 [&+&]:mt-1.5">
                    <button
                      className="flex w-full items-center gap-1.5 rounded-md bg-muted px-3 py-1.5 text-left text-xs font-semibold tracking-wide text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                      onClick={() => toggleGroup(key)}
                    >
                      <svg
                        fill="none"
                        stroke="currentColor"
                        viewBox="0 0 24 24"
                        className="size-3 shrink-0 transition-transform"
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
                        className="conversation-group-label min-w-0 truncate"
                        title={key === "__ungrouped__" ? undefined : key}
                      >
                        {group.label}
                      </span>
                      <span className="shrink-0 font-normal text-muted-foreground/70">
                        {group.conversations.length}
                      </span>
                    </button>
                    {!isCollapsed && group.conversations.map(renderConversationItem)}
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="p-2">{displayedConversations.map(renderConversationItem)}</div>
          )}
        </div>

        {/* Footer with archived toggle */}
        <div className="p-4">
          <button
            onClick={() => setShowArchived(!showArchived)}
            className="flex w-full items-center justify-center gap-2 rounded-md border border-border bg-secondary px-4 py-2 font-medium text-secondary-foreground transition-colors hover:bg-accent"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className={ROW_ICON}>
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
