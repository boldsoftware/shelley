import React, { useState, useEffect, useCallback, useRef } from "react";
import { WorkerPoolContextProvider } from "@pierre/diffs/react";
import type { SupportedLanguages } from "@pierre/diffs";
import ChatInterface from "./components/ChatInterface";
import type { EphemeralTerminal } from "./components/TerminalPanel";
import ConversationDrawer from "./components/ConversationDrawer";
import CommandPalette from "./components/CommandPalette";
import ModelsModal from "./components/ModelsModal";
import NotificationsModal from "./components/NotificationsModal";
import { Conversation, ConversationWithState, ConversationListUpdate } from "./types";
import { api } from "./services/api";
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

// Capture the initial slug from URL BEFORE React renders, so it won't be affected
// by the useEffect that updates the URL based on current conversation.
const initialSlugFromUrl = getSlugFromPath();

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
  const [modelsModalOpen, setModelsModalOpen] = useState(false);
  const [notificationsModalOpen, setNotificationsModalOpen] = useState(false);
  const [modelsRefreshTrigger, setModelsRefreshTrigger] = useState(0);
  const [navigateUserMessageTrigger, setNavigateUserMessageTrigger] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Global ephemeral terminals - persist across conversation switches
  const [ephemeralTerminals, setEphemeralTerminals] = useState<EphemeralTerminal[]>([]);
  const [subagentUpdate, setSubagentUpdate] = useState<Conversation | null>(null);
  const [showActiveTrigger, setShowActiveTrigger] = useState(0);
  const [subagentStateUpdate, setSubagentStateUpdate] = useState<{
    conversation_id: string;
    working: boolean;
  } | null>(null);
  const initialSlugResolved = useRef(false);

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

  const navigateToNextConversation = useCallback(() => {
    if (conversations.length === 0) return;
    const currentIndex = conversations.findIndex(
      (c) => c.conversation_id === currentConversationId,
    );
    // Next = further down the list (older)
    const nextIndex = currentIndex < 0 ? 0 : Math.min(currentIndex + 1, conversations.length - 1);
    const next = conversations[nextIndex];
    setCurrentConversationId(next.conversation_id);
    setViewedConversation(next);
  }, [conversations, currentConversationId]);

  const navigateToPreviousConversation = useCallback(() => {
    if (conversations.length === 0) return;
    const currentIndex = conversations.findIndex(
      (c) => c.conversation_id === currentConversationId,
    );
    // Previous = further up the list (newer)
    const prevIndex = currentIndex < 0 ? 0 : Math.max(currentIndex - 1, 0);
    const prev = conversations[prevIndex];
    setCurrentConversationId(prev.conversation_id);
    setViewedConversation(prev);
  }, [conversations, currentConversationId]);

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
      const slug = getSlugFromPath();
      if (!slug) return;

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

  // Handle conversation list updates from the message stream
  const handleConversationListUpdate = useCallback((update: ConversationListUpdate) => {
    if (update.type === "update" && update.conversation) {
      // Handle subagent conversations separately
      if (update.conversation.parent_conversation_id) {
        setSubagentUpdate(update.conversation);
        return;
      }

      // If the conversation is archived, remove it from the active list
      if (update.conversation.archived) {
        setConversations((prev) =>
          prev.filter((c) => c.conversation_id !== update.conversation!.conversation_id),
        );
        return;
      }

      setConversations((prev) => {
        // Check if this conversation already exists
        const existingIndex = prev.findIndex(
          (c) => c.conversation_id === update.conversation!.conversation_id,
        );

        const gitFields = {
          git_repo_root: update.git_repo_root,
          git_worktree_root: update.git_worktree_root,
        };
        if (existingIndex >= 0) {
          // Update existing conversation in place, preserving working state and git info
          // (working state is updated separately via conversation_state)
          const updated = [...prev];
          updated[existingIndex] = {
            ...update.conversation!,
            ...gitFields,
            working: prev[existingIndex].working,
            git_commit: prev[existingIndex].git_commit,
            git_subject: prev[existingIndex].git_subject,
            subagent_count: prev[existingIndex].subagent_count,
          };
          return updated;
        } else {
          // Add new conversation at the top (not working by default)
          return [
            { ...update.conversation!, ...gitFields, working: false, subagent_count: 0 },
            ...prev,
          ];
        }
      });
    } else if (update.type === "delete" && update.conversation_id) {
      setConversations((prev) => prev.filter((c) => c.conversation_id !== update.conversation_id));
    }
  }, []);

  // Handle conversation state updates (working state changes)
  const handleConversationStateUpdate = useCallback(
    (state: { conversation_id: string; working: boolean }) => {
      // Check if this is a top-level conversation
      setConversations((prev) => {
        const found = prev.find((conv) => conv.conversation_id === state.conversation_id);
        if (found) {
          return prev.map((conv) =>
            conv.conversation_id === state.conversation_id
              ? { ...conv, working: state.working }
              : conv,
          );
        }
        // Not a top-level conversation, might be a subagent
        // Pass the state update to the drawer
        setSubagentStateUpdate(state);
        return prev;
      });
    },
    [],
  );

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
      const convs = await api.getConversations();
      setConversations(convs);

      // Try to resolve conversation from URL slug first
      const slugConv = await resolveInitialSlug(convs);
      if (slugConv) {
        setCurrentConversationId(slugConv.conversation_id);
        setViewedConversation(slugConv);
      } else if (!currentConversationId && convs.length > 0) {
        // If we have conversations and no current one selected, select the first
        setCurrentConversationId(convs[0].conversation_id);
        setViewedConversation(convs[0]);
      }
      // If no conversations exist, leave currentConversationId as null
      // The UI will show the welcome screen and create conversation on first message
    } catch (err) {
      console.error("Failed to load conversations:", err);
      setError("Failed to load conversations. Please refresh the page.");
    } finally {
      setLoading(false);
    }
  };

  const refreshConversations = async () => {
    try {
      const convs = await api.getConversations();
      setConversations(convs);
    } catch (err) {
      console.error("Failed to refresh conversations:", err);
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
    // Skip subagent conversations for the main list
    if (updatedConversation.parent_conversation_id) {
      return;
    }
    setConversations((prev) =>
      prev.map((conv) =>
        conv.conversation_id === updatedConversation.conversation_id
          ? {
              ...updatedConversation,
              working: conv.working,
              git_commit: conv.git_commit,
              git_subject: conv.git_subject,
              subagent_count: conv.subagent_count,
            }
          : conv,
      ),
    );
  };

  const handleConversationArchived = (conversationId: string) => {
    setConversations((prev) => prev.filter((conv) => conv.conversation_id !== conversationId));
    // If the archived conversation was current, switch to another or clear
    if (currentConversationId === conversationId) {
      const remaining = conversations.filter((conv) => conv.conversation_id !== conversationId);
      setCurrentConversationId(remaining.length > 0 ? remaining[0].conversation_id : null);
    }
  };

  const handleConversationUnarchived = (conversation: Conversation) => {
    // Add back to active list if not already present (SSE may also deliver this).
    // We need this handler in case no SSE connection is active (e.g., when all
    // conversations are archived). If SSE also delivers the update,
    // handleConversationListUpdate will find it already present and update in-place.
    setConversations((prev) =>
      prev.some((c) => c.conversation_id === conversation.conversation_id)
        ? prev
        : [{ ...conversation, working: false, subagent_count: 0 }, ...prev],
    );
    // Update viewedConversation so archived state reflects immediately
    if (conversation.conversation_id === currentConversationId) {
      setViewedConversation(conversation);
    }
    // Switch drawer back to active conversations view
    setShowActiveTrigger((prev) => prev + 1);
  };

  const handleConversationRenamed = (conversation: Conversation) => {
    // Update the conversation in the list with the new slug, preserving working/git state
    setConversations((prev) =>
      prev.map((c) =>
        c.conversation_id === conversation.conversation_id
          ? {
              ...conversation,
              working: c.working,
              git_commit: c.git_commit,
              git_subject: c.git_subject,
              subagent_count: c.subagent_count,
            }
          : c,
      ),
    );
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
    currentConversation?.cwd || (conversations.length > 0 ? conversations[0].cwd : null);

  const handleFirstMessage = async (message: string, model: string, cwd?: string) => {
    try {
      const response = await api.sendMessageWithNewConversation({ message, model, cwd });
      const newConversationId = response.conversation_id;

      // Fetch the new conversation details
      const updatedConvs = await api.getConversations();
      setConversations(updatedConvs);
      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to send first message:", err);
      setError("Failed to send message");
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

      // Fetch the new conversation details and switch to the new conversation
      const updatedConvs = await api.getConversations();
      setConversations(updatedConvs);
      setCurrentConversationId(newConversationId);
    } catch (err) {
      console.error("Failed to distill conversation:", err);
      setError("Failed to distill conversation");
      throw err;
    }
  };

  return (
    <WorkerPoolContextProvider
      poolOptions={diffsPoolOptions}
      highlighterOptions={diffsHighlighterOptions}
    >
      <div className="app-container">
        {/* Conversations drawer */}
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
          subagentUpdate={subagentUpdate}
          subagentStateUpdate={subagentStateUpdate}
          showActiveTrigger={showActiveTrigger}
        />

        {/* Main chat interface */}
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
            onConversationListUpdate={handleConversationListUpdate}
            onConversationStateUpdate={handleConversationStateUpdate}
            onFirstMessage={handleFirstMessage}
            onDistillConversation={handleDistillConversation}
            mostRecentCwd={mostRecentCwd}
            isDrawerCollapsed={drawerCollapsed}
            onToggleDrawerCollapse={toggleDrawerCollapsed}
            openDiffViewerTrigger={diffViewerTrigger}
            modelsRefreshTrigger={modelsRefreshTrigger}
            onOpenModelsModal={() => setModelsModalOpen(true)}
            onReconnect={refreshConversations}
            ephemeralTerminals={ephemeralTerminals}
            setEphemeralTerminals={setEphemeralTerminals}
            navigateUserMessageTrigger={navigateUserMessageTrigger}
            onConversationUnarchived={handleConversationUnarchived}
          />
        </div>

        {/* Command Palette */}
        <CommandPalette
          isOpen={commandPaletteOpen}
          onClose={() => setCommandPaletteOpen(false)}
          conversations={conversations}
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
