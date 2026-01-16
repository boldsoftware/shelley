import React, { useState, useEffect } from "react";
import { Conversation, ConversationWithState } from "../types";
import { api } from "../services/api";

interface ConversationDrawerProps {
  isOpen: boolean;
  isCollapsed: boolean;
  onClose: () => void;
  onToggleCollapse: () => void;
  conversations: ConversationWithState[];
  currentConversationId: string | null;
  onSelectConversation: (id: string) => void;
  onNewConversation: () => void;
  onConversationArchived?: (id: string) => void;
  onConversationUnarchived?: (conversation: Conversation) => void;
  onConversationRenamed?: (conversation: Conversation) => void;
}

function ConversationDrawer({
  isOpen,
  isCollapsed,
  onClose,
  onToggleCollapse,
  conversations,
  currentConversationId,
  onSelectConversation,
  onNewConversation,
  onConversationArchived,
  onConversationUnarchived,
  onConversationRenamed,
}: ConversationDrawerProps) {
  const [showArchived, setShowArchived] = useState(false);
  const [archivedConversations, setArchivedConversations] = useState<Conversation[]>([]);
  const [loadingArchived, setLoadingArchived] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingSlug, setEditingSlug] = useState("");
  const renameInputRef = React.useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (showArchived && archivedConversations.length === 0) {
      loadArchivedConversations();
    }
  }, [showArchived]);

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
      return "Yesterday";
    } else if (diffDays < 7) {
      return `${diffDays} days ago`;
    } else {
      return date.toLocaleDateString();
    }
  };

  // Format cwd with ~ for home directory (display only)
  const formatCwdForDisplay = (cwd: string | null | undefined): string | null => {
    if (!cwd) return null;
    const homeDir = window.__SHELLEY_INIT__?.home_dir;
    if (homeDir && cwd === homeDir) {
      return "~";
    }
    if (homeDir && cwd.startsWith(homeDir + "/")) {
      return "~" + cwd.slice(homeDir.length);
    }
    return cwd;
  };

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
    if (!confirm("Are you sure you want to permanently delete this conversation?")) {
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
      alert("A conversation with this name already exists");
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

  const displayedConversations = showArchived ? archivedConversations : conversations;

  return (
    <>
      {/* Drawer */}
      <div className={`drawer ${isOpen ? "open" : ""} ${isCollapsed ? "collapsed" : ""}`}>
        {/* Header */}
        <div className="drawer-header">
          <h2 className="drawer-title">{showArchived ? "Archived" : "Conversations"}</h2>
          <div className="drawer-header-actions">
            {/* New conversation button - mobile only */}
            {!showArchived && (
              <button
                onClick={onNewConversation}
                className="btn-icon hide-on-desktop"
                aria-label="New conversation"
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
              aria-label="Close conversations"
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
              aria-label="Collapse sidebar"
              title="Collapse sidebar"
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

        {/* Conversations list */}
        <div className="drawer-body scrollable">
          {loadingArchived && showArchived ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>Loading...</p>
            </div>
          ) : displayedConversations.length === 0 ? (
            <div style={{ padding: "1rem", textAlign: "center" }} className="text-secondary">
              <p>{showArchived ? "No archived conversations" : "No conversations yet"}</p>
              {!showArchived && (
                <p className="text-sm" style={{ marginTop: "0.25rem" }}>
                  Start a new conversation to get started
                </p>
              )}
            </div>
          ) : (
            <div className="conversation-list">
              {displayedConversations.map((conversation) => {
                const isActive = conversation.conversation_id === currentConversationId;
                return (
                  <div
                    key={conversation.conversation_id}
                    className={`conversation-item ${isActive ? "active" : ""}`}
                    onClick={() => {
                      if (!showArchived) {
                        onSelectConversation(conversation.conversation_id);
                      }
                    }}
                    style={{ cursor: showArchived ? "default" : "pointer" }}
                  >
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
                        <div style={{ flex: 1, minWidth: 0 }}>
                          {editingId === conversation.conversation_id ? (
                            <input
                              ref={renameInputRef}
                              type="text"
                              value={editingSlug}
                              onChange={(e) => setEditingSlug(e.target.value)}
                              onBlur={() => handleRename(conversation.conversation_id)}
                              onKeyDown={(e) =>
                                handleRenameKeyDown(e, conversation.conversation_id)
                              }
                              onClick={(e) => e.stopPropagation()}
                              autoFocus
                              className="conversation-title"
                              style={{
                                width: "100%",
                                background: "transparent",
                                border: "none",
                                borderBottom: "1px solid var(--text-secondary)",
                                outline: "none",
                                padding: 0,
                                font: "inherit",
                                color: "inherit",
                              }}
                            />
                          ) : (
                            <div className="conversation-title">
                              {getConversationPreview(conversation)}
                            </div>
                          )}
                        </div>
                        {(conversation as ConversationWithState).working && (
                          <span
                            className="working-indicator"
                            title="Agent is working"
                            style={{
                              width: "8px",
                              height: "8px",
                              borderRadius: "50%",
                              backgroundColor: "var(--accent-color, #3b82f6)",
                              flexShrink: 0,
                              animation: "pulse 2s ease-in-out infinite",
                            }}
                          />
                        )}
                      </div>
                      <div className="conversation-meta">
                        <span className="conversation-date">
                          {formatDate(conversation.updated_at)}
                        </span>
                        {conversation.cwd && (
                          <span className="conversation-cwd" title={conversation.cwd}>
                            {formatCwdForDisplay(conversation.cwd)}
                          </span>
                        )}
                        {!showArchived && (
                          <div
                            className="conversation-actions"
                            style={{ display: "flex", gap: "0.25rem", marginLeft: "auto" }}
                          >
                            <button
                              onClick={(e) => handleStartRename(e, conversation)}
                              className="btn-icon-sm"
                              title="Rename"
                              aria-label="Rename conversation"
                            >
                              <svg
                                fill="none"
                                stroke="currentColor"
                                viewBox="0 0 24 24"
                                style={{ width: "1rem", height: "1rem" }}
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
                              title="Archive"
                              aria-label="Archive conversation"
                            >
                              <svg
                                fill="none"
                                stroke="currentColor"
                                viewBox="0 0 24 24"
                                style={{ width: "1rem", height: "1rem" }}
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
                    </div>
                    {showArchived && (
                      <div
                        className="conversation-actions"
                        style={{ display: "flex", gap: "0.25rem", marginLeft: "0.5rem" }}
                      >
                        <button
                          onClick={(e) => handleUnarchive(e, conversation.conversation_id)}
                          className="btn-icon-sm"
                          title="Restore"
                          aria-label="Restore conversation"
                        >
                          <svg
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                            style={{ width: "1rem", height: "1rem" }}
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
                          title="Delete permanently"
                          aria-label="Delete conversation"
                        >
                          <svg
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                            style={{ width: "1rem", height: "1rem" }}
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
                );
              })}
            </div>
          )}
        </div>

        {/* Footer with archived toggle */}
        <div className="drawer-footer">
          <button
            onClick={() => setShowArchived(!showArchived)}
            className="btn-secondary"
            style={{
              width: "100%",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              gap: "0.5rem",
            }}
          >
            <svg
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              style={{ width: "1rem", height: "1rem" }}
            >
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
            <span>{showArchived ? "Back to Conversations" : "View Archived"}</span>
          </button>
        </div>
      </div>
    </>
  );
}

export default ConversationDrawer;
