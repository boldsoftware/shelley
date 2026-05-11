import React, { useState, useEffect, useRef, useCallback } from "react";
import { ConversationWithState, Model } from "../types";
import { useMarkdown } from "../contexts/MarkdownContext";
import { ThemeMode, getStoredTheme, setStoredTheme, applyTheme } from "../services/theme";
import { useI18n } from "../i18n";
import { useVersionChecker } from "./VersionChecker";
import MarkdownContent from "./MarkdownContent";
import ModelPicker from "./ModelPicker";
import MessageInput from "./MessageInput";
import DirectoryPickerModal from "./DirectoryPickerModal";

interface HomeFeedProps {
  conversations: ConversationWithState[];
  onSelectConversation: (conversation: ConversationWithState) => void;
  onNewConversation: () => void;
  onArchiveConversation: (conversationId: string) => Promise<void>;
  onFirstMessage: (message: string, model: string, cwd?: string) => Promise<void>;
  onReplyToConversation: (conversationId: string, message: string) => Promise<void>;
  mostRecentCwd: string | null;
  onOpenModelsModal?: () => void;
  onOpenDrawer: () => void;
  models: Model[];
  defaultModel: string;
  hostname: string;
}

function timeAgo(date: Date): string {
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  if (diffSec < 60) return "just now";
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDay = Math.floor(diffHr / 24);
  if (diffDay < 7) return `${diffDay}d ago`;
  return date.toLocaleDateString();
}

function formatCwd(cwd: string | null | undefined): string | null {
  if (!cwd) return null;
  const homeDir = window.__SHELLEY_INIT__?.home_dir;
  if (homeDir && cwd === homeDir) return "~";
  if (homeDir && cwd.startsWith(homeDir + "/")) return "~" + cwd.slice(homeDir.length);
  return cwd;
}

function HomeFeed({
  conversations,
  onSelectConversation,
  onNewConversation,
  onArchiveConversation,
  onFirstMessage,
  onReplyToConversation,
  mostRecentCwd,
  onOpenModelsModal,
  onOpenDrawer,
  models,
  defaultModel,
  hostname,
}: HomeFeedProps) {
  const { t } = useI18n();
  const [sending, setSending] = useState(false);
  const [selectedModel, setSelectedModel] = useState(defaultModel);
  const [selectedCwd, setSelectedCwd] = useState(
    () => localStorage.getItem("shelley_selected_cwd") || mostRecentCwd || "",
  );
  const [showDirectoryPicker, setShowDirectoryPicker] = useState(false);
  const [replyingTo, setReplyingTo] = useState<string | null>(null);
  const [sendingReply, setSendingReply] = useState(false);
  const [showOverflowMenu, setShowOverflowMenu] = useState(false);
  const [themeMode, setThemeMode] = useState<ThemeMode>(getStoredTheme);
  const [copiedHash, setCopiedHash] = useState<string | null>(null);
  const copyTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const { markdownMode, setMarkdownMode } = useMarkdown();
  const { hasUpdate, openModal: openVersionModal, VersionModal } = useVersionChecker();
  const overflowMenuRef = useRef<HTMLDivElement>(null);

  // Clean up copy timeout on unmount
  useEffect(() => {
    return () => {
      if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
    };
  }, []);

  // Close overflow menu on outside click
  useEffect(() => {
    if (!showOverflowMenu) return;
    const handleClick = (e: MouseEvent) => {
      if (overflowMenuRef.current && !overflowMenuRef.current.contains(e.target as Node)) {
        setShowOverflowMenu(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [showOverflowMenu]);

  // Update selectedCwd when mostRecentCwd changes (only if no cwd set yet)
  const selectedCwdRef = useRef(selectedCwd);
  selectedCwdRef.current = selectedCwd;
  useEffect(() => {
    if (mostRecentCwd && !selectedCwdRef.current) {
      setSelectedCwd(mostRecentCwd);
    }
  }, [mostRecentCwd]);

  const handleSendNew = useCallback(
    async (message: string) => {
      if (!message.trim() || sending) return;
      setSending(true);
      try {
        await onFirstMessage(message.trim(), selectedModel, selectedCwd || undefined);
      } finally {
        setSending(false);
      }
    },
    [sending, selectedModel, selectedCwd, onFirstMessage],
  );

  const handleReply = useCallback(
    async (conversationId: string, message: string) => {
      setSendingReply(true);
      try {
        await onReplyToConversation(conversationId, message.trim());
        setReplyingTo(null);
      } finally {
        setSendingReply(false);
      }
    },
    [onReplyToConversation],
  );

  const handleArchive = useCallback(
    async (e: React.MouseEvent, conversationId: string) => {
      e.stopPropagation();
      try {
        await onArchiveConversation(conversationId);
      } catch (err) {
        console.error("Failed to archive conversation:", err);
      }
    },
    [onArchiveConversation],
  );

  const handleCopyHash = useCallback((e: React.MouseEvent, hash: string) => {
    e.stopPropagation();
    navigator.clipboard
      .writeText(hash)
      .then(() => {
        setCopiedHash(hash);
        if (copyTimeoutRef.current) clearTimeout(copyTimeoutRef.current);
        copyTimeoutRef.current = setTimeout(() => setCopiedHash(null), 1500);
      })
      .catch(() => {});
  }, []);

  const links = window.__SHELLEY_INIT__?.links || [];

  return (
    <div className="full-height flex flex-col">
      {/* Header — matches ChatInterface's top bar */}
      <div className="header">
        <div className="header-left">
          <button
            onClick={onOpenDrawer}
            className="btn-icon hide-on-desktop"
            aria-label="Open conversations"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M4 6h16M4 12h16M4 18h16"
              />
            </svg>
          </button>
          <h1 className="header-title">Shelley on {hostname}</h1>
        </div>
        <div className="header-actions">
          <button onClick={onNewConversation} className="btn-new" aria-label="New conversation">
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
                d="M12 4v16m8-8H4"
              />
            </svg>
          </button>

          {/* Overflow menu — same as ChatInterface */}
          <div ref={overflowMenuRef} style={{ position: "relative" }}>
            <button
              onClick={() => setShowOverflowMenu(!showOverflowMenu)}
              className="btn-icon"
              aria-label={t("moreOptions")}
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M12 5v.01M12 12v.01M12 19v.01M12 6a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2z"
                />
              </svg>
              {hasUpdate && <span className="version-update-dot" />}
            </button>

            {showOverflowMenu && (
              <div className="overflow-menu">
                {links.map((link, index) => (
                  <button
                    key={index}
                    onClick={() => {
                      setShowOverflowMenu(false);
                      window.open(link.url, "_blank");
                    }}
                    className="overflow-menu-item"
                  >
                    <svg
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                      style={{ width: "1.25rem", height: "1.25rem", marginRight: "0.75rem" }}
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d={
                          link.icon_svg ||
                          "M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
                        }
                      />
                    </svg>
                    {link.title}
                  </button>
                ))}

                {/* Version check */}
                {links.length > 0 && <div className="overflow-menu-divider" />}
                <button
                  onClick={() => {
                    setShowOverflowMenu(false);
                    openVersionModal();
                  }}
                  className="overflow-menu-item"
                >
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    style={{ width: "1.25rem", height: "1.25rem", marginRight: "0.75rem" }}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                    />
                  </svg>
                  {t("checkForNewVersion")}
                  {hasUpdate && <span className="version-menu-dot" />}
                </button>

                {/* Theme toggle */}
                <div className="overflow-menu-divider" />
                <div className="theme-toggle-row">
                  <button
                    onClick={() => {
                      setThemeMode("system");
                      setStoredTheme("system");
                      applyTheme("system");
                    }}
                    className={`theme-toggle-btn${themeMode === "system" ? " theme-toggle-btn-selected" : ""}`}
                    title={t("system")}
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"
                      />
                    </svg>
                  </button>
                  <button
                    onClick={() => {
                      setThemeMode("light");
                      setStoredTheme("light");
                      applyTheme("light");
                    }}
                    className={`theme-toggle-btn${themeMode === "light" ? " theme-toggle-btn-selected" : ""}`}
                    title={t("light")}
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"
                      />
                    </svg>
                  </button>
                  <button
                    onClick={() => {
                      setThemeMode("dark");
                      setStoredTheme("dark");
                      applyTheme("dark");
                    }}
                    className={`theme-toggle-btn${themeMode === "dark" ? " theme-toggle-btn-selected" : ""}`}
                    title={t("dark")}
                  >
                    <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"
                      />
                    </svg>
                  </button>
                </div>

                {/* Markdown toggle */}
                <div className="overflow-menu-divider" />
                <div className="md-toggle-row">
                  <div className="md-toggle-label">{t("markdown")}</div>
                  <div className="md-toggle-buttons">
                    <button
                      onClick={() => setMarkdownMode("off")}
                      className={`md-toggle-btn${markdownMode === "off" ? " md-toggle-btn-selected" : ""}`}
                    >
                      {t("off")}
                    </button>
                    <button
                      onClick={() => setMarkdownMode("agent")}
                      className={`md-toggle-btn${markdownMode === "agent" ? " md-toggle-btn-selected" : ""}`}
                    >
                      {t("agent")}
                    </button>
                    <button
                      onClick={() => setMarkdownMode("all")}
                      className={`md-toggle-btn${markdownMode === "all" ? " md-toggle-btn-selected" : ""}`}
                    >
                      {t("all")}
                    </button>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Scrollable content */}
      <div className="hf-scroll">
        {/* New conversation input */}
        <div className="hf-new">
          <MessageInput
            onSend={handleSendNew}
            disabled={sending}
            autoFocus={true}
            persistKey="home-feed-new"
            initialRows={2}
            statusSlot={
              <div className="hf-new-fields">
                <div className="status-field status-field-model">
                  <span className="status-field-label">{t("modelLabel")}</span>
                  <ModelPicker
                    models={models}
                    selectedModel={selectedModel}
                    onSelectModel={setSelectedModel}
                    onManageModels={() => onOpenModelsModal?.()}
                    disabled={sending}
                  />
                </div>
                <div className="status-field status-field-cwd">
                  <span className="status-field-label">{t("dirLabel")}</span>
                  <button
                    className="status-chip"
                    onClick={() => setShowDirectoryPicker(true)}
                    disabled={sending}
                  >
                    {formatCwd(selectedCwd) || "(no cwd)"}
                  </button>
                </div>
              </div>
            }
          />
        </div>

        {/* Conversation list */}
        <div className="hf-list">
          {conversations.length === 0 ? (
            <div className="hf-empty">
              <p>No conversations yet. Start one above!</p>
            </div>
          ) : (
            conversations.map((conv, idx) => {
              const previewText = conv.preview ?? "";
              const slug = conv.slug || conv.conversation_id.slice(0, 8);
              const updatedAt = new Date(conv.updated_at);
              const isReplying = replyingTo === conv.conversation_id;
              const cwd = formatCwd(conv.cwd);

              return (
                <React.Fragment key={conv.conversation_id}>
                  {idx > 0 && <div className="hf-divider" />}
                  <div className={`hf-row${conv.working ? " hf-row-working" : ""}`}>
                    {/* Left: conversation info (matches drawer styling) */}
                    <div
                      className="hf-row-left"
                      onClick={() => setReplyingTo(isReplying ? null : conv.conversation_id)}
                    >
                      <div className="hf-row-title">
                        {conv.working && <span className="hf-working-dot" />}
                        <span className="hf-row-slug">{slug}</span>
                      </div>
                      <div className="hf-row-meta">
                        <span className="hf-row-time">{timeAgo(updatedAt)}</span>
                        {cwd && (
                          <span className="hf-row-cwd" title={conv.cwd || ""}>
                            {cwd}
                          </span>
                        )}
                      </div>
                      {conv.git_commit && (
                        <div className="hf-row-git">
                          <span
                            className={`hf-row-git-hash hf-row-git-hash-clickable${copiedHash === conv.git_commit ? " copied" : ""}`}
                            title={copiedHash === conv.git_commit ? "Copied!" : "Click to copy"}
                            onClick={(e) => handleCopyHash(e, conv.git_commit!)}
                          >
                            {copiedHash === conv.git_commit ? "✓" : conv.git_commit}
                          </span>
                          {conv.git_subject && (
                            <span className="hf-row-git-subject" title={conv.git_subject}>
                              {conv.git_subject}
                            </span>
                          )}
                        </div>
                      )}
                    </div>

                    {/* Center: last message preview (full, no truncation) */}
                    <div
                      className="hf-row-preview"
                      onClick={() => setReplyingTo(isReplying ? null : conv.conversation_id)}
                    >
                      {previewText ? (
                        markdownMode !== "off" ? (
                          <div className="hf-prose">
                            <MarkdownContent text={previewText} />
                          </div>
                        ) : (
                          <p className="hf-preview-text">{previewText}</p>
                        )
                      ) : (
                        <p className="hf-preview-text hf-preview-empty">
                          <em>No agent response yet</em>
                        </p>
                      )}
                    </div>

                    {/* Right: action buttons — always visible */}
                    <div className="hf-row-actions">
                      <button
                        className="hf-action-btn"
                        onClick={() => onSelectConversation(conv)}
                        title="Open"
                      >
                        <svg
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                          style={{ width: "15px", height: "15px" }}
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
                          />
                        </svg>
                      </button>
                      <button
                        className="hf-action-btn"
                        onClick={() => setReplyingTo(isReplying ? null : conv.conversation_id)}
                        title="Reply"
                      >
                        <svg
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                          style={{ width: "15px", height: "15px" }}
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M3 10h10a8 8 0 018 8v2M3 10l6 6m-6-6l6-6"
                          />
                        </svg>
                      </button>
                      <button
                        className="hf-action-btn"
                        onClick={(e) => handleArchive(e, conv.conversation_id)}
                        title="Archive"
                      >
                        <svg
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                          style={{ width: "15px", height: "15px" }}
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

                    {/* Inline reply area — spans full row width */}
                    {isReplying && (
                      <div className="hf-reply-area">
                        <MessageInput
                          onSend={(msg) => handleReply(conv.conversation_id, msg)}
                          disabled={sendingReply}
                          autoFocus={true}
                          persistKey={`home-reply-${conv.conversation_id}`}
                          initialRows={1}
                        />
                        <button className="hf-reply-cancel" onClick={() => setReplyingTo(null)}>
                          Cancel
                        </button>
                      </div>
                    )}
                  </div>
                </React.Fragment>
              );
            })
          )}
        </div>
      </div>

      <DirectoryPickerModal
        isOpen={showDirectoryPicker}
        onClose={() => setShowDirectoryPicker(false)}
        onSelect={(path) => {
          setSelectedCwd(path);
          localStorage.setItem("shelley_selected_cwd", path);
        }}
        initialPath={selectedCwd}
      />

      {VersionModal}
    </div>
  );
}

export default HomeFeed;
