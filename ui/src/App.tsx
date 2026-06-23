import React, { useState, useEffect, useCallback, useMemo, useRef } from "react";
import { WorkerPoolContextProvider } from "@pierre/diffs/react";
import type { SupportedLanguages } from "@pierre/diffs";
import ChatInterface from "./components/ChatInterface";
import type { EphemeralTerminal } from "./components/TerminalPanel";
import ConversationDrawer from "./components/ConversationDrawer";
import CommandPalette from "./components/CommandPalette";
import ModelsModal from "./components/ModelsModal";
import NotificationsModal from "./components/NotificationsModal";
import FeatureFlagsModal from "./components/FeatureFlagsModal";
import { focusMessageInputIfUnfocused } from "./utils/focusMessageInput";
import { Conversation, ConversationWithState, ConversationListPatchEvent } from "./types";
import { api } from "./services/api";
import { messageStore } from "./services/messageStore";
import { applyConversationListPatch } from "./services/conversationListStream";
import { connectGlobalStream, type StreamStatus } from "./services/globalStream";
import { handleNotificationEvent } from "./services/notifications";
import { useI18n } from "./i18n";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";

// Worker pool configuration for @pierre/diffs syntax highlighting
// Workers run tokenization off the main thread for better performance with large diffs
const diffsPoolOptions = {
  workerFactory: () => new Worker("/diffs-worker.js"),
};

// Languages to preload in the highlighter (matches PatchTool.tsx langMap)
const diffsHighlighterOptions = {
  langs: [
    "typescript",
    "tsx",
    "javascript",
    "jsx",
    "python",
    "ruby",
    "go",
    "rust",
    "java",
    "c",
    "cpp",
    "csharp",
    "php",
    "swift",
    "kotlin",
    "scala",
    "bash",
    "sql",
    "html",
    "css",
    "scss",
    "json",
    "xml",
    "yaml",
    "toml",
    "markdown",
  ] as SupportedLanguages[],
};

// Check if a slug is a generated ID (format: cXXXX where X is alphanumeric)
function isGeneratedId(slug: string | null): boolean {
  if (!slug) return true;
  return /^c[a-z0-9]+$/i.test(slug);
}

// Get slug from the current URL path (expects /c/<slug> format)
function getSlugFromPath(): string | null {
  const path = window.location.pathname;
  // Check for /c/<slug> format
  if (path.startsWith("/c/")) {
    const slug = path.slice(3); // Remove "/c/" prefix
    if (slug) {
      return slug;
    }
  }
  return null;
}

function isNewPath(): boolean {
  return window.location.pathname === "/new";
}

// Capture the initial slug from URL BEFORE React renders, so it won't be affected
// by the useEffect that updates the URL based on current conversation.
const initialSlugFromUrl = getSlugFromPath();
const initialIsNew = isNewPath();

// Update the URL to reflect the current conversation slug. Drafts (no real
// slug yet) use their conversation_id so reload restores the draft.
function updateUrlWithSlug(conversation: Conversation | undefined) {
  const currentSlug = getSlugFromPath();
  let newSlug: string | null = null;
  if (conversation?.slug && !isGeneratedId(conversation.slug)) {
    newSlug = conversation.slug;
  } else if (conversation?.is_draft) {
    newSlug = conversation.conversation_id;
  }

  if (currentSlug !== newSlug) {
    if (newSlug) {
      window.history.replaceState({}, "", `/c/${newSlug}`);
    } else {
      window.history.replaceState({}, "", "/");
    }
  }
}

function updatePageTitle(conversation: Conversation | undefined) {
  const hostname = window.__SHELLEY_INIT__?.hostname;
  const parts: string[] = [];

  if (conversation?.slug && !isGeneratedId(conversation.slug)) {
    parts.push(conversation.slug);
  }
  if (hostname) {
    parts.push(hostname);
  }
  parts.push("Shelley Agent");

  document.title = parts.join(" - ");
}

function App() {
  const { t } = useI18n();
  const [conversations, setConversations] = useState<ConversationWithState[]>([]);
  const [currentConversationId, setCurrentConversationId] = useState<string | null>(null);
  // Track viewed conversation separately (needed for subagents which aren't in main list)
  const [viewedConversation, setViewedConversation] = useState<Conversation | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerCollapsed, setDrawerCollapsed] = useState(false);
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false);
  const [diffViewerTrigger, setDiffViewerTrigger] = useState(0);
  const [gitGraphTrigger, setGitGraphTrigger] = useState(0);
  // Bumped to spawn a fresh interactive-shell terminal in ChatInterface.
  const [terminalTrigger, setTerminalTrigger] = useState(0);
  const [modelsModalOpen, setModelsModalOpen] = useState(false);
  const [notificationsModalOpen, setNotificationsModalOpen] = useState(false);
  const [featureFlagsModalOpen, setFeatureFlagsModalOpen] = useState(false);
  const [modelsRefreshTrigger, setModelsRefreshTrigger] = useState(0);
  // Bumped whenever the user picks a cwd via a quick action (e.g. command
  // palette). ChatInterface re-reads localStorage when this changes so the
  // selected cwd updates even if we're already on /new.
  const [cwdSyncTrigger, setCwdSyncTrigger] = useState(0);
  const [navigateUserMessageTrigger, setNavigateUserMessageTrigger] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Global ephemeral terminals - persist across conversation switches and
  // (via dtach sessions on the server) page reloads. We hydrate from the
  // server's terminal list on mount.
  const [ephemeralTerminals, setEphemeralTerminals] = useState<EphemeralTerminal[]>([]);
  const [streamStatus, setStreamStatus] = useState<StreamStatus>("connected");
  // Bumped each time the global stream reconnects, so ChatInterface can
  // reload the focused conversation's history (it may have missed events
  // while the stream was down).
  const [reconnectNonce, setReconnectNonce] = useState(0);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/terminals")
      .then((r) => (r.ok ? r.json() : []))
      .then((rows: Array<{ id: string; command: string; cwd: string; created_at: string }>) => {
        if (cancelled || !Array.isArray(rows) || rows.length === 0) return;
        setEphemeralTerminals((prev) => {
          const have = new Set(prev.map((t) => t.termId).filter(Boolean));
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
      .catch((err) => {
        console.warn("failed to fetch persistent terminals:", err);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleTerminalAttached = useCallback((id: string, termId: string) => {
    setEphemeralTerminals((prev) => prev.map((t) => (t.id === id ? { ...t, termId } : t)));
  }, []);

  const handleTerminalClose = useCallback((id: string) => {
    setEphemeralTerminals((prev) => {
      const t = prev.find((x) => x.id === id);
      if (t && t.termId) {
        // Best-effort: tell the server to kill the persistent session.
        fetch(`/api/terminals/${encodeURIComponent(t.termId)}`, { method: "DELETE" }).catch((err) =>
          console.warn("failed to delete terminal:", err),
        );
      }
      return prev.filter((x) => x.id !== id);
    });
  }, []);
  const [showActiveTrigger, setShowActiveTrigger] = useState(0);
  const initialSlugResolved = useRef(false);
  const conversationListHashRef = useRef<string | null>(null);
  const conversationsRef = useRef<ConversationWithState[]>([]);

  // Resolve initial slug from URL - uses the captured initialSlugFromUrl
  // Returns the conversation if found, null otherwise
  const resolveInitialSlug = useCallback(
    async (convs: Conversation[]): Promise<Conversation | null> => {
      if (initialSlugResolved.current) return null;
      initialSlugResolved.current = true;

      const urlSlug = initialSlugFromUrl;
      if (!urlSlug) return null;

      // First check if we already have this conversation in our list
      // (match by slug OR conversation_id — drafts use their id in the URL).
      const existingConv = convs.find((c) => c.slug === urlSlug || c.conversation_id === urlSlug);
      if (existingConv) return existingConv;

      // Otherwise, try to fetch by slug (may be a subagent)
      try {
        const conv = await api.getConversationBySlug(urlSlug);
        if (conv) return conv;
      } catch (err) {
        console.error("Failed to resolve slug:", err);
      }

      // Slug not found, clear the URL
      window.history.replaceState({}, "", "/");
      return null;
    },
    [],
  );

  // Load conversations on mount
  useEffect(() => {
    loadConversations();
  }, []);

  // The patch stream emits both top-level conversations and their subagents in
  // a single list so subagent state can be diffed inline. Anything that's
  // about the user-facing “conversation list” (navigation, default
  // selection) should ignore subagents.
  const topLevelConversations = useMemo(
    () => conversations.filter((c) => !c.parent_conversation_id),
    [conversations],
  );

  const navigateToNextConversation = useCallback(() => {
    if (topLevelConversations.length === 0) return;
    const currentIndex = topLevelConversations.findIndex(
      (c) => c.conversation_id === currentConversationId,
    );
    // Next = further down the list (older)
    const nextIndex =
      currentIndex < 0 ? 0 : Math.min(currentIndex + 1, topLevelConversations.length - 1);
    const next = topLevelConversations[nextIndex];
    setCurrentConversationId(next.conversation_id);
    setViewedConversation(next);
  }, [topLevelConversations, currentConversationId]);

  const navigateToPreviousConversation = useCallback(() => {
    if (topLevelConversations.length === 0) return;
    const currentIndex = topLevelConversations.findIndex(
      (c) => c.conversation_id === currentConversationId,
    );
    // Previous = further up the list (newer)
    const prevIndex = currentIndex < 0 ? 0 : Math.max(currentIndex - 1, 0);
    const prev = topLevelConversations[prevIndex];
    setCurrentConversationId(prev.conversation_id);
    setViewedConversation(prev);
  }, [topLevelConversations, currentConversationId]);

  const navigateToNextUserMessage = useCallback(() => {
    setNavigateUserMessageTrigger((prev) => Math.abs(prev) + 1);
  }, []);

  const navigateToPreviousUserMessage = useCallback(() => {
    setNavigateUserMessageTrigger((prev) => -(Math.abs(prev) + 1));
  }, []);

  // Global keyboard shortcuts (including Ctrl+M chord sequences)
  useEffect(() => {
    const isMac = navigator.platform.toUpperCase().includes("MAC");
    let chordPending = false;
    let chordTimer: number | null = null;

    const clearChord = () => {
      chordPending = false;
      if (chordTimer !== null) {
        clearTimeout(chordTimer);
        chordTimer = null;
      }
    };

    const handleKeyDown = (e: KeyboardEvent) => {
      // Handle second key of Ctrl+M chord (before the Mac Ctrl passthrough)
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
        // Any other key cancels the chord
        return;
      }

      // Ctrl+M on all platforms: start chord sequence
      // (intentionally before the Mac Ctrl passthrough — we use Ctrl, not Cmd,
      // to avoid overriding Cmd+M which is system minimize on macOS)
      if (e.ctrlKey && !e.metaKey && !e.altKey && (e.key === "m" || e.key === "M")) {
        e.preventDefault();
        chordPending = true;
        // Auto-cancel chord after 1.5 seconds
        chordTimer = window.setTimeout(clearChord, 1500);
        return;
      }

      // On macOS: Ctrl+K is readline (kill to end of line), let it pass through
      if (isMac && e.ctrlKey && !e.metaKey) return;
      // On macOS use Cmd+K, on other platforms use Ctrl+K
      const modifierPressed = isMac ? e.metaKey : e.ctrlKey;

      if (modifierPressed && e.key === "k") {
        e.preventDefault();
        setCommandPaletteOpen((prev) => !prev);
        return;
      }

      // Alt+ArrowDown: next conversation
      if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && e.key === "ArrowDown") {
        e.preventDefault();
        navigateToNextConversation();
        return;
      }

      // Alt+ArrowUp: previous conversation
      if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && e.key === "ArrowUp") {
        e.preventDefault();
        navigateToPreviousConversation();
        return;
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      clearChord();
    };
  }, [
    navigateToNextConversation,
    navigateToPreviousConversation,
    navigateToNextUserMessage,
    navigateToPreviousUserMessage,
  ]);

  // Handle popstate events (browser back/forward and SubagentTool navigation)
  useEffect(() => {
    const handlePopState = async () => {
      if (isNewPath()) {
        setCurrentConversationId(null);
        setViewedConversation(null);
        return;
      }
      const slug = getSlugFromPath();
      if (!slug) {
        return;
      }

      // Try to find in existing conversations first
      // (match by slug OR conversation_id — drafts use their id in the URL).
      const existingConv = conversations.find((c) => c.slug === slug || c.conversation_id === slug);
      if (existingConv) {
        setCurrentConversationId(existingConv.conversation_id);
        setViewedConversation(existingConv);
        return;
      }

      // Otherwise fetch by slug (may be a subagent)
      try {
        const conv = await api.getConversationBySlug(slug);
        if (conv) {
          setCurrentConversationId(conv.conversation_id);
          setViewedConversation(conv);
        }
      } catch (err) {
        console.error("Failed to navigate to conversation:", err);
      }
    };

    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, [conversations]);

  useEffect(() => {
    conversationsRef.current = conversations;
  }, [conversations]);

  const syncConversations = useCallback(
    (updater: (prev: ConversationWithState[]) => ConversationWithState[]) => {
      setConversations((prev) => {
        const next = updater(prev);
        conversationsRef.current = next;
        return next;
      });
    },
    [],
  );

  const globalStreamRef = useRef<{ forceReconnect: () => void } | null>(null);

  // Recover from an unapplicable patch: drop the stale hash so the next
  // /api/stream2 connection sends a fresh reset, then force a reconnect.
  const recoverConversationListStream = useCallback(() => {
    conversationListHashRef.current = null;
    globalStreamRef.current?.forceReconnect();
  }, []);

  const handleConversationListPatch = useCallback(
    (event: ConversationListPatchEvent) => {
      const currentHash = conversationListHashRef.current;
      if (!event.reset && event.old_hash !== currentHash) {
        // A patch arrived that doesn't anchor to our current state. This is
        // not necessarily a bug (e.g. cross-event ordering during reconnect),
        // but applying it would corrupt local state. Drop the hash and force
        // a reconnect; the server replies with a fresh reset.
        console.warn("conversation list patch hash mismatch; recovering via reconnect", {
          eventOldHash: event.old_hash,
          currentHash,
        });
        recoverConversationListStream();
        return;
      }
      // Apply the patch OUTSIDE the React state updater so any error throws
      // synchronously here (where our try/catch can see it) rather than
      // during React's commit phase (where it crashes the renderer).
      const prev = conversationsRef.current;
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
      // Propagate server-reported max_sequence_id for all conversations in
      // the updated list so ChatInterface can skip unnecessary backfills.
      //
      // Also mirror the persistent agent_working flag into the transient
      // store. The conversation_list_patch stream is the SINGLE
      // authoritative source of truth for agent_working: server-side
      // recomputeMu serializes patch emission so patches arrive in a
      // strict old_hash→new_hash chain, and the embedded ConversationWithState
      // is read from the conversations row inside the same recompute. The
      // alternative path — per-conversation `conversation_state` events
      // — rides a separate streamPub fan-out and can race with these
      // list patches at the client, so globalStream no longer trusts it
      // for working state.
      //
      // For this to be safe, every code path that flips agent_working in
      // the DB must do so BEFORE committing a sibling user-visible Tx that
      // would otherwise fire a list-patch carrying the stale prior value.
      // See ConversationManager.AcceptUserMessage (which now calls
      // SetAgentWorking(true) before recordMessage) for the matching
      // server-side ordering.
      for (const conv of next) {
        messageStore.setMaxSequenceIdKnown(conv.conversation_id, conv.max_sequence_id);
        messageStore.setAgentWorking(conv.conversation_id, conv.agent_working);
      }
      syncConversations(() => next);
      conversationListHashRef.current = event.new_hash;
    },
    [syncConversations, recoverConversationListStream],
  );

  // Open the single long-lived /api/stream2 connection. The server delivers
  // events for ALL active conversations on this connection; per-conversation
  // events are routed by `conversation_id` into messageStore by the global
  // stream handler. Backfill of a focused conversation's history happens via
  // REST in ChatInterface.
  useEffect(() => {
    const stream = connectGlobalStream({
      getHash: () => conversationListHashRef.current,
      onListPatch: handleConversationListPatch,
      onNotificationEvent: handleNotificationEvent,
      onStatusChange: setStreamStatus,
      onReconnect: () => {
        // Stream came back after a disconnect. messageStore.markAllStale()
        // already cleared hasFullHistory; bump the nonce so ChatInterface
        // re-fetches the focused conversation's history immediately.
        setReconnectNonce((n) => n + 1);
      },
    });
    globalStreamRef.current = stream;
    return () => {
      globalStreamRef.current = null;
      stream.close();
    };
  }, [handleConversationListPatch]);

  // Update page title and URL when conversation changes
  useEffect(() => {
    // Use viewedConversation if it matches (handles subagents), otherwise look up from list
    const currentConv =
      viewedConversation?.conversation_id === currentConversationId
        ? viewedConversation
        : conversations.find((conv) => conv.conversation_id === currentConversationId);
    if (currentConv) {
      updatePageTitle(currentConv);
      updateUrlWithSlug(currentConv);
    }
  }, [currentConversationId, viewedConversation, conversations]);

  const loadConversations = async () => {
    try {
      setLoading(true);
      setError(null);
      const snapshot = await api.getConversationsSnapshot();
      // Seed max_sequence_id_known from the freshly-loaded list so ChatInterface
      // can skip the REST backfill if the cache is already up-to-date.
      for (const conv of snapshot.conversations) {
        messageStore.setMaxSequenceIdKnown(conv.conversation_id, conv.max_sequence_id);
      }
      // Prune IDB cache for archived/forgotten conversations (not in the
      // server's list anymore, and not touched locally for over a week).
      // Fire-and-forget; failure here is non-fatal.
      const activeIds = snapshot.conversations.map((c) => c.conversation_id);
      void messageStore.pruneStale(activeIds, 7 * 24 * 60 * 60 * 1000);
      const streamHash = conversationListHashRef.current;
      if (!streamHash) {
        syncConversations(() => snapshot.conversations);
        conversationListHashRef.current = snapshot.hash;
      }
      const currentList = streamHash ? conversationsRef.current : snapshot.conversations;
      const topLevel = currentList.filter((c) => !c.parent_conversation_id);

      // Try to resolve conversation from URL slug first (slug may match a
      // subagent, so search the full list).
      const slugConv = await resolveInitialSlug(currentList);
      if (slugConv) {
        setCurrentConversationId(slugConv.conversation_id);
        setViewedConversation(slugConv);
      } else if (!initialIsNew && topLevel.length > 0) {
        // No slug in URL and not on /new — select the most recent
        // top-level conversation.
        setCurrentConversationId(topLevel[0].conversation_id);
        setViewedConversation(topLevel[0]);
      }
    } catch (err) {
      console.error("Failed to load conversations:", err);
      setError("Failed to load conversations. Please refresh the page.");
    } finally {
      setLoading(false);
    }
  };

  const startNewConversation = () => {
    // Save the current conversation's cwd to localStorage so the new conversation picks it up
    if (currentConversation?.cwd) {
      localStorage.setItem("shelley_selected_cwd", currentConversation.cwd);
    }
    // Clear the current conversation - a new one will be created when the user sends their first message
    setCurrentConversationId(null);
    setViewedConversation(null);
    // Navigate to /new so a reload keeps the user in the new-conversation view.
    window.history.replaceState({}, "", "/new");
    setDrawerOpen(false);
  };

  const startNewConversationWithCwd = (cwd: string) => {
    localStorage.setItem("shelley_selected_cwd", cwd);
    setCurrentConversationId(null);
    setViewedConversation(null);
    window.history.replaceState({}, "", "/new");
    setDrawerOpen(false);
    // Force ChatInterface to re-read the cwd from localStorage even if it's
    // already mounted in the new-conversation view.
    setCwdSyncTrigger((n) => n + 1);
  };

  // Retarget the working directory of the conversation currently being
  // composed, in place. Unlike startNewConversationWithCwd this never clears
  // the current conversation, so a half-written draft survives the change.
  // - No conversation yet (pure /new): just update the sticky cwd and nudge
  //   ChatInterface to re-read it.
  // - Draft conversation: also persist the new cwd to the draft row so a
  //   reload keeps it.
  const setConversationCwd = (cwd: string) => {
    localStorage.setItem("shelley_selected_cwd", cwd);
    const conv =
      conversations.find((c) => c.conversation_id === currentConversationId) ||
      (viewedConversation?.conversation_id === currentConversationId ? viewedConversation : null);
    if (conv?.is_draft) {
      api.updateDraftCwd(conv.conversation_id, cwd).catch((err) => {
        // 404 once the draft has been promoted (its cwd is then immutable);
        // that's expected, so log at a low level rather than as an error.
        console.debug("Could not persist draft cwd (likely already promoted):", err);
      });
    }
    // Force ChatInterface to re-read the cwd from localStorage in place.
    setCwdSyncTrigger((n) => n + 1);
  };

  const selectConversation = (conversation: Conversation) => {
    setCurrentConversationId(conversation.conversation_id);
    setViewedConversation(conversation);
    setDrawerOpen(false);
  };

  const toggleDrawerCollapsed = () => {
    setDrawerCollapsed((prev) => !prev);
  };

  const updateConversation = useCallback(
    (updatedConversation: Conversation) => {
      // The top-level conversation list is owned by the patch stream; keep the
      // currently viewed metadata fresh without changing that list out-of-band.
      if (updatedConversation.conversation_id === currentConversationId) {
        setViewedConversation(updatedConversation);
      }
    },
    [currentConversationId],
  );

  const handleConversationArchived = (
    conversationId: string,
    nextConversation?: Conversation | null,
  ) => {
    void messageStore.delete(conversationId);
    // If the archived conversation was current, switch immediately; the patch
    // stream will remove it from the list. The drawer tells us which
    // conversation to select next (the one immediately below the archived one,
    // else the one immediately above); fall back to the first remaining
    // top-level conversation if it didn't.
    if (currentConversationId === conversationId) {
      if (nextConversation && nextConversation.conversation_id !== conversationId) {
        setCurrentConversationId(nextConversation.conversation_id);
        setViewedConversation(nextConversation);
        return;
      }
      const remaining = conversationsRef.current.filter(
        (conv) => conv.conversation_id !== conversationId && !conv.parent_conversation_id,
      );
      setCurrentConversationId(remaining.length > 0 ? remaining[0].conversation_id : null);
      setViewedConversation(remaining.length > 0 ? remaining[0] : null);
    }
  };

  const handleConversationUnarchived = (conversation: Conversation) => {
    // The conversation list patch stream will add it back to the active list.
    // Update viewedConversation so archived state reflects immediately
    if (conversation.conversation_id === currentConversationId) {
      setViewedConversation(conversation);
    }
    // Switch drawer back to active conversations view
    setShowActiveTrigger((prev) => prev + 1);
  };

  const handleConversationRenamed = (conversation: Conversation) => {
    if (conversation.conversation_id === currentConversationId) {
      setViewedConversation(conversation);
    }
  };

  if (loading && conversations.length === 0) {
    return (
      <div className="flex h-screen items-center justify-center bg-background text-foreground">
        <div className="flex flex-col items-center gap-3">
          <Loader2Icon className="size-6 animate-spin text-muted-foreground" />
          <p className="text-muted-foreground">{t("loading")}</p>
        </div>
      </div>
    );
  }

  if (error && conversations.length === 0) {
    return (
      <div className="flex h-screen items-center justify-center bg-background p-4 text-foreground">
        <div className="flex flex-col items-center gap-4 text-center">
          <p className="text-destructive">{error}</p>
          <Button onClick={loadConversations}>{t("retry")}</Button>
        </div>
      </div>
    );
  }

  const currentConversation =
    conversations.find((conv) => conv.conversation_id === currentConversationId) ||
    (viewedConversation?.conversation_id === currentConversationId
      ? { ...viewedConversation, working: false, subagent_count: 0, max_sequence_id: 0 }
      : undefined);

  // Get the CWD from the current conversation, or fall back to the most recent conversation
  const mostRecentCwd =
    currentConversation?.cwd ||
    (topLevelConversations.length > 0 ? topLevelConversations[0].cwd : null);

  const handleFirstMessage = async (
    message: string,
    model: string,
    cwd?: string,
    conversationType?: "normal" | "orchestrator",
    subagentBackend?: "shelley" | "claude-cli" | "codex-cli",
    toolOverrides?: Record<string, "on" | "off">,
    thinkingLevel?: "off" | "minimal" | "low" | "medium" | "high" | "xhigh",
  ) => {
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

      // Optimistically seed the transient working flag for the new
      // conversation BEFORE focus-switching to it. ChatInterface's focus
      // effect runs resetTransient() synchronously when conversationId
      // changes, and resetTransient preserves whatever was already in
      // the transient store. Without this seed, if the authoritative
      // conversation_list_patch carrying agent_working=true hasn't been
      // delivered yet by the time React commits, the focus effect would
      // initialize agentWorking from an empty transient (i.e. false) and
      // the thinking indicator would stay dark until the next patch
      // arrives — visible flicker on slow first responses.
      messageStore.setAgentWorking(newConversationId, true);

      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to send first message:", err);
      setError(err instanceof Error ? err.message : "Failed to send message");
      throw err;
    }
  };

  const handleDistillNewGeneration = async (
    sourceConversationId: string,
    model: string,
    cwd?: string,
    method?: "default" | "compact",
    instructions?: string,
  ) => {
    try {
      await api.distillNewGeneration(sourceConversationId, model, cwd, method, instructions);
      // Don't bypass the patch stream here. Setting `conversations` directly
      // without updating `conversationListHashRef` would desync future patch
      // events; the stream will deliver the new generation as a regular patch
      // moments after the API call returns.
      setCurrentConversationId(sourceConversationId);
    } catch (err) {
      console.error("Failed to distill into new generation:", err);
      setError("Failed to distill into new generation");
      throw err;
    }
  };

  return (
    <WorkerPoolContextProvider
      poolOptions={diffsPoolOptions}
      highlighterOptions={diffsHighlighterOptions}
    >
      <div className="flex h-screen flex-col bg-background text-foreground">
        {window.__SHELLEY_INIT__?.banner && (
          <div
            className="shrink-0 truncate border-b border-border bg-secondary px-3 py-1 text-center text-xs text-secondary-foreground"
            title={window.__SHELLEY_INIT__.banner}
          >
            {window.__SHELLEY_INIT__.banner}
          </div>
        )}
        <div className="relative flex min-h-0 flex-1 overflow-hidden">
          <ConversationDrawer
          isOpen={drawerOpen}
          isCollapsed={drawerCollapsed}
          onClose={() => setDrawerOpen(false)}
          onToggleCollapse={toggleDrawerCollapsed}
          conversations={conversations}
          currentConversationId={currentConversationId}
          viewedConversation={viewedConversation}
          onSelectConversation={selectConversation}
          onNewConversation={startNewConversation}
          onConversationArchived={handleConversationArchived}
          onConversationUnarchived={handleConversationUnarchived}
          onConversationRenamed={handleConversationRenamed}
          showActiveTrigger={showActiveTrigger}
        />

        {/* Main content: Chat interface */}
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <ChatInterface
            conversationId={currentConversationId}
            streamStatus={streamStatus}
            reconnectNonce={reconnectNonce}
            onOpenDrawer={() => setDrawerOpen(true)}
            onNewConversation={startNewConversation}
            onSelectConversation={selectConversation}
            onArchiveConversation={async (conversationId: string) => {
              await api.archiveConversation(conversationId);
              handleConversationArchived(conversationId);
            }}
            currentConversation={currentConversation}
            onConversationUpdate={updateConversation}
            onFirstMessage={handleFirstMessage}
            onDraftCreated={(id: string) => {
              // Lazy-created draft conversation — promote the URL/state to
              // point at it. The patch stream surfaces the row in the
              // drawer; we just track it as the current conversation.
              setCurrentConversationId(id);
            }}
            onDistillNewGeneration={handleDistillNewGeneration}
            mostRecentCwd={mostRecentCwd}
            isDrawerCollapsed={drawerCollapsed}
            onToggleDrawerCollapse={toggleDrawerCollapsed}
            openDiffViewerTrigger={diffViewerTrigger}
            openGitGraphTrigger={gitGraphTrigger}
            openTerminalTrigger={terminalTrigger}
            modelsRefreshTrigger={modelsRefreshTrigger}
            cwdSyncTrigger={cwdSyncTrigger}
            onOpenModelsModal={() => setModelsModalOpen(true)}
            ephemeralTerminals={ephemeralTerminals}
            setEphemeralTerminals={setEphemeralTerminals}
            onTerminalAttached={handleTerminalAttached}
            onTerminalClose={handleTerminalClose}
            navigateUserMessageTrigger={navigateUserMessageTrigger}
            onConversationUnarchived={handleConversationUnarchived}
          />
        </div>

        {/* Command Palette */}
        <CommandPalette
          isOpen={commandPaletteOpen}
          onClose={() => {
            setCommandPaletteOpen(false);
            focusMessageInputIfUnfocused();
          }}
          conversations={topLevelConversations}
          currentConversation={currentConversation || null}
          onNewConversation={() => {
            startNewConversation();
            setCommandPaletteOpen(false);
          }}
          onNewConversationWithCwd={(cwd: string) => {
            startNewConversationWithCwd(cwd);
            setCommandPaletteOpen(false);
          }}
          onSetConversationCwd={(cwd: string) => {
            setConversationCwd(cwd);
            setCommandPaletteOpen(false);
          }}
          onSelectConversation={(conversation) => {
            selectConversation(conversation);
            setCommandPaletteOpen(false);
          }}
          onArchiveConversation={async (conversationId: string) => {
            try {
              await api.archiveConversation(conversationId);
              handleConversationArchived(conversationId);
            } catch (err) {
              console.error("Failed to archive conversation:", err);
            }
          }}
          onOpenDiffViewer={() => {
            setDiffViewerTrigger((prev) => prev + 1);
            setCommandPaletteOpen(false);
          }}
          onOpenGitGraph={() => {
            setGitGraphTrigger((prev) => prev + 1);
            setCommandPaletteOpen(false);
          }}
          onOpenTerminal={() => {
            setTerminalTrigger((prev) => prev + 1);
            setCommandPaletteOpen(false);
          }}
          onOpenModelsModal={() => {
            setModelsModalOpen(true);
            setCommandPaletteOpen(false);
          }}
          onOpenNotificationsModal={() => {
            setNotificationsModalOpen(true);
            setCommandPaletteOpen(false);
          }}
          onOpenFeatureFlagsModal={() => {
            setFeatureFlagsModalOpen(true);
            setCommandPaletteOpen(false);
          }}
          onNextConversation={navigateToNextConversation}
          onPreviousConversation={navigateToPreviousConversation}
          onNextUserMessage={navigateToNextUserMessage}
          onPreviousUserMessage={navigateToPreviousUserMessage}
          hasCwd={
            !!(
              currentConversation?.cwd ||
              mostRecentCwd ||
              localStorage.getItem("shelley_selected_cwd") ||
              window.__SHELLEY_INIT__?.default_cwd
            )
          }
        />

        <ModelsModal
          isOpen={modelsModalOpen}
          onClose={() => {
            setModelsModalOpen(false);
            focusMessageInputIfUnfocused();
          }}
          onModelsChanged={() => setModelsRefreshTrigger((prev) => prev + 1)}
        />

        <NotificationsModal
          isOpen={notificationsModalOpen}
          onClose={() => {
            setNotificationsModalOpen(false);
            focusMessageInputIfUnfocused();
          }}
        />

        <FeatureFlagsModal
          isOpen={featureFlagsModalOpen}
          onClose={() => {
            setFeatureFlagsModalOpen(false);
            focusMessageInputIfUnfocused();
          }}
        />

        {/* Backdrop for mobile drawer */}
          {drawerOpen && (
            <div
              className="fixed inset-0 z-40 bg-black/50 md:hidden"
              onClick={() => setDrawerOpen(false)}
            />
          )}
        </div>
      </div>
    </WorkerPoolContextProvider>
  );
}

export default App;
