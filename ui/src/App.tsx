import React, { useState, useEffect, useCallback, useRef } from "react";
import ChatInterface from "./components/ChatInterface";
import ConversationDrawer from "./components/ConversationDrawer";
import CommandPalette from "./components/CommandPalette";
import { Conversation, ConversationWithState, ConversationListUpdate } from "./types";
import { api } from "./services/api";

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
  const [conversations, setConversations] = useState<ConversationWithState[]>([]);
  const [currentConversationId, setCurrentConversationId] = useState<string | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerCollapsed, setDrawerCollapsed] = useState(false);
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false);
  const [diffViewerTrigger, setDiffViewerTrigger] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [subagentUpdate, setSubagentUpdate] = useState<Conversation | null>(null);
  const [subagentStateUpdate, setSubagentStateUpdate] = useState<{ conversation_id: string; working: boolean } | null>(null);
  const initialSlugResolved = useRef(false);

  // Resolve initial slug from URL - uses the captured initialSlugFromUrl
  const resolveInitialSlug = useCallback(async (convs: Conversation[]) => {
    if (initialSlugResolved.current) return null;
    initialSlugResolved.current = true;

    // Use the slug captured at module load time, not the current URL
    // (which may have been changed by updateUrlWithSlug before this runs)
    const urlSlug = initialSlugFromUrl;
    if (!urlSlug) return null;

    // First check if we already have this conversation in our list
    const existingConv = convs.find((c) => c.slug === urlSlug);
    if (existingConv) {
      return existingConv.conversation_id;
    }

    // Otherwise, try to fetch by slug
    try {
      const conv = await api.getConversationBySlug(urlSlug);
      if (conv) {
        return conv.conversation_id;
      }
    } catch (err) {
      console.error("Failed to resolve slug:", err);
    }

    // Slug not found, clear the URL
    window.history.replaceState({}, "", "/");
    return null;
  }, []);

  // Load conversations on mount
  useEffect(() => {
    loadConversations();
  }, []);

  // Global keyboard shortcut for command palette (Cmd+K / Ctrl+K)
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setCommandPaletteOpen((prev) => !prev);
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, []);

  // Handle conversation list updates from the message stream
  const handleConversationListUpdate = useCallback((update: ConversationListUpdate) => {
    if (update.type === "update" && update.conversation) {
      // Handle subagent conversations separately
      if (update.conversation.parent_conversation_id) {
        setSubagentUpdate(update.conversation);
        return;
      }
      setConversations((prev) => {
        // Check if this conversation already exists
        const existingIndex = prev.findIndex(
          (c) => c.conversation_id === update.conversation!.conversation_id,
        );

        if (existingIndex >= 0) {
          // Update existing conversation in place, preserving working state
          // (working state is updated separately via conversation_state)
          const updated = [...prev];
          updated[existingIndex] = {
            ...update.conversation!,
            working: prev[existingIndex].working,
          };
          return updated;
        } else {
          // Add new conversation at the top (not working by default)
          return [{ ...update.conversation!, working: false }, ...prev];
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
    const currentConv = conversations.find(
      (conv) => conv.conversation_id === currentConversationId,
    );
    updatePageTitle(currentConv);
    updateUrlWithSlug(currentConv);
  }, [currentConversationId, conversations]);

  const loadConversations = async () => {
    try {
      setLoading(true);
      setError(null);
      const convs = await api.getConversations();
      setConversations(convs);

      // Try to resolve conversation from URL slug first
      const slugConvId = await resolveInitialSlug(convs);
      if (slugConvId) {
        setCurrentConversationId(slugConvId);
      } else if (!currentConversationId && convs.length > 0) {
        // If we have conversations and no current one selected, select the first
        setCurrentConversationId(convs[0].conversation_id);
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

  const startNewConversation = () => {
    // Just clear the current conversation - a new one will be created when the user sends their first message
    setCurrentConversationId(null);
    setDrawerOpen(false);
  };

  const selectConversation = (conversationId: string) => {
    setCurrentConversationId(conversationId);
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
          ? { ...updatedConversation, working: conv.working }
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
    // Add the unarchived conversation back to the list (not working by default)
    setConversations((prev) => [{ ...conversation, working: false }, ...prev]);
  };

  const handleConversationRenamed = (conversation: Conversation) => {
    // Update the conversation in the list with the new slug, preserving working state
    setConversations((prev) =>
      prev.map((c) =>
        c.conversation_id === conversation.conversation_id
          ? { ...conversation, working: c.working }
          : c,
      ),
    );
  };

  if (loading && conversations.length === 0) {
    return (
      <div className="loading-container">
        <div className="loading-content">
          <div className="spinner" style={{ margin: "0 auto 1rem" }}></div>
          <p className="text-secondary">Loading...</p>
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
            Retry
          </button>
        </div>
      </div>
    );
  }

  const currentConversation = conversations.find(
    (conv) => conv.conversation_id === currentConversationId,
  );

  // Get the CWD from the most recent conversation (first in list, sorted by updated_at desc)
  const mostRecentCwd = conversations.length > 0 ? conversations[0].cwd : null;

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

  return (
    <div className="app-container">
      {/* Conversations drawer */}
      <ConversationDrawer
        isOpen={drawerOpen}
        isCollapsed={drawerCollapsed}
        onClose={() => setDrawerOpen(false)}
        onToggleCollapse={toggleDrawerCollapsed}
        conversations={conversations}
        currentConversationId={currentConversationId}
        onSelectConversation={selectConversation}
        onNewConversation={startNewConversation}
        onConversationArchived={handleConversationArchived}
        onConversationUnarchived={handleConversationUnarchived}
        onConversationRenamed={handleConversationRenamed}
        subagentUpdate={subagentUpdate}
        subagentStateUpdate={subagentStateUpdate}
      />

      {/* Main chat interface */}
      <div className="main-content">
        <ChatInterface
          conversationId={currentConversationId}
          onOpenDrawer={() => setDrawerOpen(true)}
          onNewConversation={startNewConversation}
          currentConversation={currentConversation}
          onConversationUpdate={updateConversation}
          onConversationListUpdate={handleConversationListUpdate}
          onConversationStateUpdate={handleConversationStateUpdate}
          onFirstMessage={handleFirstMessage}
          mostRecentCwd={mostRecentCwd}
          isDrawerCollapsed={drawerCollapsed}
          onToggleDrawerCollapse={toggleDrawerCollapsed}
          openDiffViewerTrigger={diffViewerTrigger}
        />
      </div>

      {/* Command Palette */}
      <CommandPalette
        isOpen={commandPaletteOpen}
        onClose={() => setCommandPaletteOpen(false)}
        conversations={conversations}
        onNewConversation={() => {
          startNewConversation();
          setCommandPaletteOpen(false);
        }}
        onSelectConversation={(id) => {
          selectConversation(id);
          setCommandPaletteOpen(false);
        }}
        onOpenDiffViewer={() => {
          setDiffViewerTrigger((prev) => prev + 1);
          setCommandPaletteOpen(false);
        }}
        hasCwd={!!(currentConversation?.cwd || mostRecentCwd)}
      />

      {/* Backdrop for mobile drawer */}
      {drawerOpen && (
        <div className="backdrop hide-on-desktop" onClick={() => setDrawerOpen(false)} />
      )}
    </div>
  );
}

export default App;
