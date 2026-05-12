import React, { useState, useEffect, useCallback, useMemo, useRef } from "react";
import { WorkerPoolContextProvider } from "@pierre/diffs/react";
import type { SupportedLanguages } from "@pierre/diffs";
import ChatInterface from "./components/ChatInterface";
import type { EphemeralTerminal } from "./components/TerminalPanel";
import ConversationDrawer from "./components/ConversationDrawer";
import CommandPalette from "./components/CommandPalette";
import ModelsModal from "./components/ModelsModal";
import NotificationsModal from "./components/NotificationsModal";
import { Conversation, ConversationWithState, ConversationListPatchEvent } from "./types";
import { api } from "./services/api";
import { conversationCache } from "./services/conversationCache";
import {
  applyConversationListPatch,
  connectConversationListStream,
} from "./services/conversationListStream";
import { useI18n } from "./i18n";

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

// Update the URL to reflect the current conversation slug
function updateUrlWithSlug(conversation: Conversation | undefined) {
  const currentSlug = getSlugFromPath();
  const newSlug =
    conversation?.slug && !isGeneratedId(conversation.slug) ? conversation.slug : null;

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
  const [modelsModalOpen, setModelsModalOpen] = useState(false);
  const [notificationsModalOpen, setNotificationsModalOpen] = useState(false);
  const [modelsRefreshTrigger, setModelsRefreshTrigger] = useState(0);
  const [navigateUserMessageTrigger, setNavigateUserMessageTrigger] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Global ephemeral terminals - persist across conversation switches and
  // (via dtach sessions on the server) page reloads. We hydrate from the
  // server's terminal list on mount.
  const [ephemeralTerminals, setEphemeralTerminals] = useState<EphemeralTerminal[]>([]);

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
      const existingConv = convs.find((c) => c.slug === urlSlug);
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
      const existingConv = conversations.find((c) => c.slug === slug);
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

  const handleConversationListPatch = useCallback(
    (event: ConversationListPatchEvent) => {
      const currentHash = conversationListHashRef.current;
      if (!event.reset && event.old_hash !== currentHash) {
        return;
      }
      syncConversations((prev) => {
        const next = applyConversationListPatch(prev, event.patch);
        const nextIds = new Set(next.map((conv) => conv.conversation_id));
        for (const conv of prev) {
          if (!nextIds.has(conv.conversation_id)) {
            conversationCache.delete(conv.conversation_id);
          }
        }
        return next;
      });
      conversationListHashRef.current = event.new_hash;
    },
    [syncConversations],
  );

  // Open the standalone list-only stream only when no conversation is
  // selected. When one is selected, ChatInterface opens the combined stream
  // (messages + list patches) so the UI never holds more than one
  // subscription at a time.
  useEffect(() => {
    if (currentConversationId) return;
    const stream = connectConversationListStream({
      getHash: () => conversationListHashRef.current,
      onPatch: handleConversationListPatch,
      onStatusChange: (status) => {
        if (status !== "connected") {
          console.warn(`Conversation list stream ${status}`);
        }
      },
    });
    return () => stream.close();
  }, [currentConversationId, handleConversationListPatch]);

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
    // Clear URL when starting new conversation
    window.history.replaceState({}, "", "/");
    setDrawerOpen(false);
  };

  const startNewConversationWithCwd = (cwd: string) => {
    localStorage.setItem("shelley_selected_cwd", cwd);
    setCurrentConversationId(null);
    setViewedConversation(null);
    window.history.replaceState({}, "", "/");
    setDrawerOpen(false);
  };

  const selectConversation = (conversation: Conversation) => {
    setCurrentConversationId(conversation.conversation_id);
    setViewedConversation(conversation);
    setDrawerOpen(false);
  };

  const toggleDrawerCollapsed = () => {
    setDrawerCollapsed((prev) => !prev);
  };

  const updateConversation = (updatedConversation: Conversation) => {
    // The top-level conversation list is owned by the patch stream; keep the
    // currently viewed metadata fresh without changing that list out-of-band.
    if (updatedConversation.conversation_id === currentConversationId) {
      setViewedConversation(updatedConversation);
    }
  };

  const handleConversationArchived = (conversationId: string) => {
    conversationCache.delete(conversationId);
    // If the archived conversation was current, switch immediately; the patch
    // stream will remove it from the list.
    if (currentConversationId === conversationId) {
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
      <div className="loading-container">
        <div className="loading-content">
          <div className="spinner" style={{ margin: "0 auto 1rem" }}></div>
          <p className="text-secondary">{t("loading")}</p>
        </div>
      </div>
    );
  }

  if (error && conversations.length === 0) {
    return (
      <div className="error-container">
        <div className="error-content">
          <p className="error-message" style={{ marginBottom: "1rem" }}>
            {error}
          </p>
          <button onClick={loadConversations} className="btn-primary">
            {t("retry")}
          </button>
        </div>
      </div>
    );
  }

  const currentConversation =
    conversations.find((conv) => conv.conversation_id === currentConversationId) ||
    (viewedConversation?.conversation_id === currentConversationId
      ? { ...viewedConversation, working: false, subagent_count: 0 }
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
  ) => {
    try {
      const hasOverrides = toolOverrides && Object.keys(toolOverrides).length > 0;
      const convOpts =
        conversationType === "orchestrator" || hasOverrides
          ? {
              ...(conversationType === "orchestrator"
                ? { type: "orchestrator" as const, subagent_backend: subagentBackend || "shelley" }
                : {}),
              ...(hasOverrides ? { tool_overrides: toolOverrides } : {}),
            }
          : undefined;
      const response = await api.sendMessageWithNewConversation({
        message,
        model,
        cwd,
        conversation_options: convOpts,
      });
      const newConversationId = response.conversation_id;

      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to send first message:", err);
      setError(err instanceof Error ? err.message : "Failed to send message");
      throw err;
    }
  };

  const handleDistillConversation = async (
    sourceConversationId: string,
    model: string,
    cwd?: string,
  ) => {
    try {
      const response = await api.distillConversation(sourceConversationId, model, cwd);
      const newConversationId = response.conversation_id;

      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to distill conversation:", err);
      setError("Failed to distill conversation");
      throw err;
    }
  };

  const handleDistillReplaceConversation = async (
    sourceConversationId: string,
    model: string,
    cwd?: string,
  ) => {
    try {
      const response = await api.distillReplaceConversation(sourceConversationId, model, cwd);
      const newConversationId = response.conversation_id;
      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to distill-replace conversation:", err);
      setError("Failed to distill-replace conversation");
      throw err;
    }
  };

  const handleDistillNewGeneration = async (
    sourceConversationId: string,
    model: string,
    cwd?: string,
  ) => {
    try {
      await api.distillNewGeneration(sourceConversationId, model, cwd);
      const updatedConvs = await api.getConversations();
      setConversations(updatedConvs);
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
      <div className="app-container">
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
        <div className="main-content">
          <ChatInterface
            conversationId={currentConversationId}
            onOpenDrawer={() => setDrawerOpen(true)}
            onNewConversation={startNewConversation}
            onArchiveConversation={async (conversationId: string) => {
              await api.archiveConversation(conversationId);
              handleConversationArchived(conversationId);
            }}
            currentConversation={currentConversation}
            onConversationUpdate={updateConversation}
            conversationListHash={conversationListHashRef.current}
            onConversationListPatch={handleConversationListPatch}
            onFirstMessage={handleFirstMessage}
            onDistillConversation={handleDistillConversation}
            onDistillReplaceConversation={handleDistillReplaceConversation}
            onDistillNewGeneration={handleDistillNewGeneration}
            mostRecentCwd={mostRecentCwd}
            isDrawerCollapsed={drawerCollapsed}
            onToggleDrawerCollapse={toggleDrawerCollapsed}
            openDiffViewerTrigger={diffViewerTrigger}
            openGitGraphTrigger={gitGraphTrigger}
            modelsRefreshTrigger={modelsRefreshTrigger}
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
          onClose={() => setCommandPaletteOpen(false)}
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
          onOpenModelsModal={() => {
            setModelsModalOpen(true);
            setCommandPaletteOpen(false);
          }}
          onOpenNotificationsModal={() => {
            setNotificationsModalOpen(true);
            setCommandPaletteOpen(false);
          }}
          onNextConversation={navigateToNextConversation}
          onPreviousConversation={navigateToPreviousConversation}
          onNextUserMessage={navigateToNextUserMessage}
          onPreviousUserMessage={navigateToPreviousUserMessage}
          hasCwd={!!(currentConversation?.cwd || mostRecentCwd)}
        />

        <ModelsModal
          isOpen={modelsModalOpen}
          onClose={() => setModelsModalOpen(false)}
          onModelsChanged={() => setModelsRefreshTrigger((prev) => prev + 1)}
        />

        <NotificationsModal
          isOpen={notificationsModalOpen}
          onClose={() => setNotificationsModalOpen(false)}
        />

        {/* Backdrop for mobile drawer */}
        {drawerOpen && (
          <div className="backdrop hide-on-desktop" onClick={() => setDrawerOpen(false)} />
        )}
      </div>
    </WorkerPoolContextProvider>
  );
}

export default App;
