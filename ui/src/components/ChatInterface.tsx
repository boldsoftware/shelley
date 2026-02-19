import React, { useState, useEffect, useLayoutEffect, useRef, useCallback, useMemo } from "react";
import {
  Message,
  Conversation,
  StreamResponse,
  LLMContent,
  ConversationListUpdate,
  isDistillStatusMessage,
} from "../types";
import { api } from "../services/api";
import { ThemeMode, getStoredTheme, setStoredTheme, applyTheme } from "../services/theme";
import { setFaviconStatus } from "../services/favicon";
import {
  handleNotificationEvent,
  isChannelEnabled,
  setChannelEnabled,
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
} from "../services/notifications";
import MessageComponent from "./Message";
import MessageInput from "./MessageInput";
import DiffViewer from "./DiffViewer";
import BashTool from "./BashTool";
import PatchTool from "./PatchTool";
import ScreenshotTool from "./ScreenshotTool";

import KeywordSearchTool from "./KeywordSearchTool";
import BrowserNavigateTool from "./BrowserNavigateTool";
import BrowserEvalTool from "./BrowserEvalTool";
import ReadImageTool from "./ReadImageTool";
import BrowserConsoleLogsTool from "./BrowserConsoleLogsTool";
import ChangeDirTool from "./ChangeDirTool";
import BrowserResizeTool from "./BrowserResizeTool";
import SubagentTool from "./SubagentTool";
import OutputIframeTool from "./OutputIframeTool";
import DirectoryPickerModal from "./DirectoryPickerModal";
import { useVersionChecker } from "./VersionChecker";
import TerminalPanel, { EphemeralTerminal } from "./TerminalPanel";
import ModelPicker from "./ModelPicker";
import SystemPromptView from "./SystemPromptView";

interface ContextUsageBarProps {
  contextWindowSize: number;
  maxContextTokens: number;
  conversationId?: string | null;
  onContinueConversation?: () => void;
  onDistillConversation?: () => void;
}

function ContextUsageBar({
  contextWindowSize,
  maxContextTokens,
  conversationId,
  onContinueConversation,
  onDistillConversation,
}: ContextUsageBarProps) {
  const [showPopup, setShowPopup] = useState(false);
  const [continuing, setContinuing] = useState(false);
  const [distilling, setDistilling] = useState(false);
  const barRef = useRef<HTMLDivElement>(null);
  const hasAutoOpenedRef = useRef<string | null>(null);

  const percentage = maxContextTokens > 0 ? (contextWindowSize / maxContextTokens) * 100 : 0;
  const clampedPercentage = Math.min(percentage, 100);
  const showLongConversationWarning = contextWindowSize >= 100000;

  const getBarColor = () => {
    if (percentage >= 90) return "var(--error-text)";
    if (percentage >= 70) return "var(--warning-text, #f59e0b)";
    return "var(--blue-text)";
  };

  const formatTokens = (tokens: number) => {
    if (tokens >= 1000000) return `${(tokens / 1000000).toFixed(1)}M`;
    if (tokens >= 1000) return `${(tokens / 1000).toFixed(0)}k`;
    return tokens.toString();
  };

  const handleClick = () => {
    setShowPopup(!showPopup);
  };

  // Auto-open popup when hitting 100k tokens (once per conversation)
  useEffect(() => {
    if (
      showLongConversationWarning &&
      conversationId &&
      hasAutoOpenedRef.current !== conversationId
    ) {
      hasAutoOpenedRef.current = conversationId;
      setShowPopup(true);
    }
  }, [showLongConversationWarning, conversationId]);

  // Close popup when clicking outside
  useEffect(() => {
    if (!showPopup) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (barRef.current && !barRef.current.contains(e.target as Node)) {
        setShowPopup(false);
      }
    };
    document.addEventListener("click", handleClickOutside);
    return () => document.removeEventListener("click", handleClickOutside);
  }, [showPopup]);

  // Calculate fixed position when popup should be shown
  const [popupPosition, setPopupPosition] = useState<{ bottom: number; right: number } | null>(
    null,
  );

  useEffect(() => {
    if (showPopup && barRef.current) {
      const rect = barRef.current.getBoundingClientRect();
      setPopupPosition({
        bottom: window.innerHeight - rect.top + 4,
        right: window.innerWidth - rect.right,
      });
    } else {
      setPopupPosition(null);
    }
  }, [showPopup]);

  const handleContinue = async () => {
    if (continuing || !onContinueConversation) return;
    setContinuing(true);
    try {
      await onContinueConversation();
      setShowPopup(false);
    } finally {
      setContinuing(false);
    }
  };

  const handleDistill = async () => {
    if (distilling || !onDistillConversation) return;
    setDistilling(true);
    try {
      await onDistillConversation();
      setShowPopup(false);
    } finally {
      setDistilling(false);
    }
  };

  return (
    <div ref={barRef}>
      {showPopup && popupPosition && (
        <div
          style={{
            position: "fixed",
            bottom: popupPosition.bottom,
            right: popupPosition.right,
            padding: "6px 10px",
            backgroundColor: "var(--bg-secondary)",
            border: "1px solid var(--border-color)",
            borderRadius: "4px",
            fontSize: "12px",
            color: "var(--text-secondary)",
            whiteSpace: "nowrap",
            boxShadow: "0 2px 8px rgba(0,0,0,0.15)",
            zIndex: 100,
          }}
        >
          {formatTokens(contextWindowSize)} / {formatTokens(maxContextTokens)} (
          {percentage.toFixed(1)}%) tokens used
          {showLongConversationWarning && (
            <div style={{ marginTop: "6px", color: "var(--warning-text, #f59e0b)" }}>
              This conversation is getting long.
              <br />
              For best results, start a new conversation.
            </div>
          )}
          {onContinueConversation && conversationId && (
            <div style={{ display: "flex", gap: "6px", marginTop: "8px" }}>
              <button
                onClick={handleContinue}
                disabled={continuing || distilling}
                style={{
                  padding: "4px 8px",
                  backgroundColor: "var(--blue-text)",
                  color: "white",
                  border: "none",
                  borderRadius: "4px",
                  cursor: continuing || distilling ? "not-allowed" : "pointer",
                  fontSize: "12px",
                  opacity: continuing || distilling ? 0.7 : 1,
                }}
              >
                {continuing ? "Continuing..." : "Continue"}
              </button>
              {onDistillConversation && (
                <button
                  onClick={handleDistill}
                  disabled={continuing || distilling}
                  style={{
                    padding: "4px 8px",
                    backgroundColor: "var(--success-text, #22c55e)",
                    color: "white",
                    border: "none",
                    borderRadius: "4px",
                    cursor: continuing || distilling ? "not-allowed" : "pointer",
                    fontSize: "12px",
                    opacity: continuing || distilling ? 0.7 : 1,
                  }}
                >
                  {distilling ? "Distilling..." : "Distill & continue"}
                </button>
              )}
            </div>
          )}
        </div>
      )}
      <div className="context-usage-bar-container">
        {showLongConversationWarning && (
          <span
            className="context-warning-icon"
            title="This conversation is getting long. For best results, start a new conversation."
          >
            ⚠️
          </span>
        )}
        <div
          className="context-usage-bar"
          onClick={handleClick}
          title={`Context: ${formatTokens(contextWindowSize)} / ${formatTokens(maxContextTokens)} tokens (${percentage.toFixed(1)}%)`}
        >
          <div
            className="context-usage-fill"
            style={{
              width: `${clampedPercentage}%`,
              backgroundColor: getBarColor(),
            }}
          />
        </div>
      </div>
    </div>
  );
}

interface CoalescedToolCallProps {
  toolName: string;
  toolInput?: unknown;
  toolResult?: LLMContent[];
  toolError?: boolean;
  toolStartTime?: string | null;
  toolEndTime?: string | null;
  hasResult?: boolean;
  display?: unknown;
  onCommentTextChange?: (text: string) => void;
}

// Map tool names to their specialized components.
// IMPORTANT: When adding a new tool here, also add it to Message.tsx renderContent()
// for both tool_use and tool_result cases. See AGENTS.md in this directory.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const TOOL_COMPONENTS: Record<string, React.ComponentType<any>> = {
  bash: BashTool,
  patch: PatchTool,
  screenshot: ScreenshotTool,
  browser_take_screenshot: ScreenshotTool,

  keyword_search: KeywordSearchTool,
  browser_navigate: BrowserNavigateTool,
  browser_eval: BrowserEvalTool,
  read_image: ReadImageTool,
  browser_recent_console_logs: BrowserConsoleLogsTool,
  browser_clear_console_logs: BrowserConsoleLogsTool,
  change_dir: ChangeDirTool,
  browser_resize: BrowserResizeTool,
  subagent: SubagentTool,
  output_iframe: OutputIframeTool,
};

function CoalescedToolCall({
  toolName,
  toolInput,
  toolResult,
  toolError,
  toolStartTime,
  toolEndTime,
  hasResult,
  display,
  onCommentTextChange,
}: CoalescedToolCallProps) {
  // Calculate execution time if available
  let executionTime = "";
  if (hasResult && toolStartTime && toolEndTime) {
    const start = new Date(toolStartTime).getTime();
    const end = new Date(toolEndTime).getTime();
    const diffMs = end - start;
    if (diffMs < 1000) {
      executionTime = `${diffMs}ms`;
    } else {
      executionTime = `${(diffMs / 1000).toFixed(1)}s`;
    }
  }

  // Look up the specialized component for this tool
  const ToolComponent = TOOL_COMPONENTS[toolName];
  if (ToolComponent) {
    const props = {
      toolInput,
      isRunning: !hasResult,
      toolResult,
      hasError: toolError,
      executionTime,
      display,
      // BrowserConsoleLogsTool needs the toolName prop
      ...(toolName === "browser_recent_console_logs" || toolName === "browser_clear_console_logs"
        ? { toolName }
        : {}),
      // Patch tool can add comments
      ...(toolName === "patch" && onCommentTextChange ? { onCommentTextChange } : {}),
    };
    return <ToolComponent {...props} />;
  }

  const getToolResultSummary = (results: LLMContent[]) => {
    if (!results || results.length === 0) return "No output";

    const firstResult = results[0];
    if (firstResult.Type === 2 && firstResult.Text) {
      // text content
      const text = firstResult.Text.trim();
      if (text.length <= 50) return text;
      return text.substring(0, 47) + "...";
    }

    return `${results.length} result${results.length > 1 ? "s" : ""}`;
  };

  const renderContent = (content: LLMContent) => {
    if (content.Type === 2) {
      // text
      return <div className="whitespace-pre-wrap break-words">{content.Text || ""}</div>;
    }
    return <div className="text-secondary text-sm italic">[Content type {content.Type}]</div>;
  };

  if (!hasResult) {
    // Show "running" state
    return (
      <div className="message message-tool" data-testid="tool-call-running">
        <div className="message-content">
          <div className="tool-running">
            <div className="tool-running-header">
              <svg
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
                style={{ width: "1rem", height: "1rem", color: "var(--blue-text)" }}
              >
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
              <span className="tool-name">Tool: {toolName}</span>
              <span className="tool-status-running">(running)</span>
            </div>
            <div className="tool-input">
              {typeof toolInput === "string" ? toolInput : JSON.stringify(toolInput, null, 2)}
            </div>
          </div>
        </div>
      </div>
    );
  }

  // Show completed state with result
  const summary = toolResult ? getToolResultSummary(toolResult) : "No output";

  return (
    <div className="message message-tool" data-testid="tool-call-completed">
      <div className="message-content">
        <details className={`tool-result-details ${toolError ? "error" : ""}`}>
          <summary className="tool-result-summary">
            <div className="tool-result-meta">
              <div className="flex items-center space-x-2">
                <svg
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                  style={{ width: "1rem", height: "1rem", color: "var(--blue-text)" }}
                >
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
                <span className="text-sm font-medium text-blue">{toolName}</span>
                <span className={`tool-result-status text-xs ${toolError ? "error" : "success"}`}>
                  {toolError ? "✗" : "✓"} {summary}
                </span>
              </div>
              <div className="tool-result-time">
                {executionTime && <span>{executionTime}</span>}
              </div>
            </div>
          </summary>
          <div className="tool-result-content">
            {/* Show tool input */}
            <div className="tool-result-section">
              <div className="tool-result-label">Input:</div>
              <div className="tool-result-data">
                {toolInput ? (
                  typeof toolInput === "string" ? (
                    toolInput
                  ) : (
                    JSON.stringify(toolInput, null, 2)
                  )
                ) : (
                  <span className="text-secondary italic">No input data</span>
                )}
              </div>
            </div>

            {/* Show tool output with header */}
            <div className={`tool-result-section output ${toolError ? "error" : ""}`}>
              <div className="tool-result-label">Output{toolError ? " (Error)" : ""}:</div>
              <div className="space-y-2">
                {toolResult?.map((result, idx) => (
                  <div key={idx}>{renderContent(result)}</div>
                ))}
              </div>
            </div>
          </div>
        </details>
      </div>
    </div>
  );
}

// Animated "Agent working..." with letter-by-letter bold animation
function AnimatedWorkingStatus() {
  const text = "Agent working...";
  const [boldIndex, setBoldIndex] = useState(0);

  useEffect(() => {
    const interval = setInterval(() => {
      setBoldIndex((prev) => (prev + 1) % text.length);
    }, 100); // 100ms per letter
    return () => clearInterval(interval);
  }, []);

  return (
    <span className="status-message animated-working">
      {text.split("").map((char, idx) => (
        <span key={idx} className={idx === boldIndex ? "bold-letter" : ""}>
          {char}
        </span>
      ))}
    </span>
  );
}

interface ConversationStateUpdate {
  conversation_id: string;
  working: boolean;
  model?: string;
}

interface ChatInterfaceProps {
  conversationId: string | null;
  onOpenDrawer: () => void;
  onNewConversation: () => void;
  currentConversation?: Conversation;
  onConversationUpdate?: (conversation: Conversation) => void;
  onConversationListUpdate?: (update: ConversationListUpdate) => void;
  onConversationStateUpdate?: (state: ConversationStateUpdate) => void;
  onFirstMessage?: (message: string, model: string, cwd?: string) => Promise<void>;
  onContinueConversation?: (
    sourceConversationId: string,
    model: string,
    cwd?: string,
  ) => Promise<void>;
  onDistillConversation?: (
    sourceConversationId: string,
    model: string,
    cwd?: string,
  ) => Promise<void>;
  mostRecentCwd?: string | null;
  isDrawerCollapsed?: boolean;
  onToggleDrawerCollapse?: () => void;
  openDiffViewerTrigger?: number; // increment to trigger opening diff viewer
  modelsRefreshTrigger?: number; // increment to trigger models list refresh
  onOpenModelsModal?: () => void;
  onReconnect?: () => void;
  ephemeralTerminals: EphemeralTerminal[];
  setEphemeralTerminals: React.Dispatch<React.SetStateAction<EphemeralTerminal[]>>;
}

function ChatInterface({
  conversationId,
  onOpenDrawer,
  onNewConversation,
  currentConversation,
  onConversationUpdate,
  onConversationListUpdate,
  onConversationStateUpdate,
  onFirstMessage,
  onContinueConversation,
  onDistillConversation,
  mostRecentCwd,
  isDrawerCollapsed,
  onToggleDrawerCollapse,
  openDiffViewerTrigger,
  modelsRefreshTrigger,
  onOpenModelsModal,
  onReconnect,
  ephemeralTerminals,
  setEphemeralTerminals,
}: ChatInterfaceProps) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [models, setModels] = useState<
    Array<{
      id: string;
      display_name?: string;
      source?: string;
      ready: boolean;
      max_context_tokens?: number;
    }>
  >(window.__SHELLEY_INIT__?.models || []);
  const [selectedModel, setSelectedModelState] = useState<string>(() => {
    // First check localStorage for a sticky model preference
    const storedModel = localStorage.getItem("shelley_selected_model");
    const initModels = window.__SHELLEY_INIT__?.models || [];
    // Validate that the stored model exists and is ready
    if (storedModel) {
      const modelInfo = initModels.find((m) => m.id === storedModel);
      if (modelInfo?.ready) {
        return storedModel;
      }
    }
    // Fall back to server default or first ready model
    const defaultModel = window.__SHELLEY_INIT__?.default_model;
    if (defaultModel) {
      return defaultModel;
    }
    const firstReady = initModels.find((m) => m.ready);
    return firstReady?.id || "claude-sonnet-4.5";
  });
  // Wrapper to persist model selection to localStorage
  const setSelectedModel = (model: string) => {
    setSelectedModelState(model);
    localStorage.setItem("shelley_selected_model", model);
  };
  const [selectedCwd, setSelectedCwdState] = useState<string>("");
  const [cwdInitialized, setCwdInitialized] = useState(false);
  // Wrapper to persist cwd selection to localStorage
  const setSelectedCwd = (cwd: string) => {
    setSelectedCwdState(cwd);
    localStorage.setItem("shelley_selected_cwd", cwd);
  };

  // Reset cwdInitialized when switching to a new conversation so we re-read from localStorage
  useEffect(() => {
    if (conversationId === null) {
      setCwdInitialized(false);
    }
  }, [conversationId]);

  // Initialize CWD with priority: localStorage > mostRecentCwd > server default
  useEffect(() => {
    if (cwdInitialized) return;

    // First check localStorage for a sticky cwd preference
    const storedCwd = localStorage.getItem("shelley_selected_cwd");
    if (storedCwd) {
      setSelectedCwdState(storedCwd);
      setCwdInitialized(true);
      return;
    }

    // Use most recent conversation's CWD if available
    if (mostRecentCwd) {
      setSelectedCwdState(mostRecentCwd);
      setCwdInitialized(true);
      return;
    }

    // Fall back to server default
    const defaultCwd = window.__SHELLEY_INIT__?.default_cwd || "";
    if (defaultCwd) {
      setSelectedCwdState(defaultCwd);
      setCwdInitialized(true);
    }
  }, [mostRecentCwd, cwdInitialized]);

  // Refresh models list when triggered (e.g., after custom model changes) or when starting new conversation
  useEffect(() => {
    // Skip on initial mount with trigger=0, but always refresh when starting a new conversation
    if (modelsRefreshTrigger === undefined) return;
    if (modelsRefreshTrigger === 0 && conversationId !== null) return;
    api
      .getModels()
      .then((newModels) => {
        setModels(newModels);
        // Also update the global init data so other components see the change
        if (window.__SHELLEY_INIT__) {
          window.__SHELLEY_INIT__.models = newModels;
        }
      })
      .catch((err) => {
        console.error("Failed to refresh models:", err);
      });
  }, [modelsRefreshTrigger, conversationId]);

  const [cwdError, setCwdError] = useState<string | null>(null);
  const [showDirectoryPicker, setShowDirectoryPicker] = useState(false);
  // Settings modal removed - configuration moved to status bar for empty conversations
  const [showOverflowMenu, setShowOverflowMenu] = useState(false);
  const [themeMode, setThemeMode] = useState<ThemeMode>(getStoredTheme);
  const [browserNotifsEnabled, setBrowserNotifsEnabled] = useState(() =>
    isChannelEnabled("browser"),
  );
  const [showDiffViewer, setShowDiffViewer] = useState(false);
  const [diffViewerInitialCommit, setDiffViewerInitialCommit] = useState<string | undefined>(
    undefined,
  );
  const [diffViewerCwd, setDiffViewerCwd] = useState<string | undefined>(undefined);
  const [diffCommentText, setDiffCommentText] = useState("");
  const [agentWorking, setAgentWorking] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [contextWindowSize, setContextWindowSize] = useState(0);
  const terminalURL = window.__SHELLEY_INIT__?.terminal_url || null;
  const links = window.__SHELLEY_INIT__?.links || [];
  const hostname = window.__SHELLEY_INIT__?.hostname || "localhost";
  const { hasUpdate, openModal: openVersionModal, VersionModal } = useVersionChecker();
  const [reconnectAttempts, setReconnectAttempts] = useState(0);
  const [isDisconnected, setIsDisconnected] = useState(false);
  const [isReconnecting, setIsReconnecting] = useState(false);
  const [showScrollToBottom, setShowScrollToBottom] = useState(false);
  const [terminalInjectedText, setTerminalInjectedText] = useState<string | null>(null);
  const [terminalAutoFocusId, setTerminalAutoFocusId] = useState<string | null>(null);
  const messagesContainerRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);
  const overflowMenuRef = useRef<HTMLDivElement>(null);
  const reconnectTimeoutRef = useRef<number | null>(null);
  const periodicRetryRef = useRef<number | null>(null);
  const heartbeatTimeoutRef = useRef<number | null>(null);
  const lastSequenceIdRef = useRef<number>(-1);
  const hasConnectedRef = useRef(false);
  const userScrolledRef = useRef(false);
  const loadingRef = useRef(false);
  // Pending scroll target from loadMessages: undefined = none, null = bottom, number = saved position
  const pendingScrollRef = useRef<number | null | undefined>(undefined);

  // Load messages and set up streaming
  useEffect(() => {
    if (conversationId) {
      setAgentWorking(false);
      loadMessages();
      setupMessageStream();
    } else {
      // No conversation yet, show empty state
      setMessages([]);
      setContextWindowSize(0);
      loadingRef.current = false;
      setLoading(false);
    }

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current);
      }
      if (periodicRetryRef.current) {
        clearInterval(periodicRetryRef.current);
      }
      if (heartbeatTimeoutRef.current) {
        clearTimeout(heartbeatTimeoutRef.current);
      }
      // Reset sequence ID and connection tracking when conversation changes
      lastSequenceIdRef.current = -1;
      hasConnectedRef.current = false;
    };
  }, [conversationId]);

  // Show working indicator on favicon (UI concern, not a notification)
  useEffect(() => {
    if (agentWorking) {
      setFaviconStatus("working");
    }
  }, [agentWorking]);

  const scrollStore = useMemo(() => {
    const key = conversationId ? `shelley_scroll_${conversationId}` : null;
    return {
      save(scrollTop: number) {
        if (key) localStorage.setItem(key, String(scrollTop));
      },
      load(): number | null {
        if (!key) return null;
        const v = localStorage.getItem(key);
        return v != null ? Number(v) : null;
      },
    };
  }, [conversationId]);

  // Save scroll position to localStorage on page hide/unload
  useEffect(() => {
    const save = () => {
      const container = messagesContainerRef.current;
      if (!container || !conversationId) return;
      scrollStore.save(container.scrollTop);
    };
    const onVisChange = () => {
      if (document.visibilityState === "hidden") save();
    };
    document.addEventListener("visibilitychange", onVisChange);
    window.addEventListener("beforeunload", save);
    return () => {
      document.removeEventListener("visibilitychange", onVisChange);
      window.removeEventListener("beforeunload", save);
    };
  }, [conversationId]);

  // Check scroll position, handle scroll-to-bottom button, and re-scroll on content resize
  const scrollSaveTimerRef = useRef<number | null>(null);
  useEffect(() => {
    const container = messagesContainerRef.current;
    if (!container) return;

    const handleScroll = () => {
      const { scrollTop, scrollHeight, clientHeight } = container;
      const isNearBottom = scrollHeight - scrollTop - clientHeight < 100;
      setShowScrollToBottom(!isNearBottom);
      userScrolledRef.current = !isNearBottom;
      // Debounced save — 100ms after scroll settles
      if (scrollSaveTimerRef.current) clearTimeout(scrollSaveTimerRef.current);
      scrollSaveTimerRef.current = window.setTimeout(() => {
        if (!loadingRef.current) scrollStore.save(container.scrollTop);
      }, 100);
    };

    container.addEventListener("scroll", handleScroll);

    // Re-scroll to bottom when content expands (images loading, tool outputs rendering)
    // but only if the user hasn't scrolled away.
    let lastScrollHeight = container.scrollHeight;
    const ro = new ResizeObserver(() => {
      if (container.scrollHeight === lastScrollHeight) return;
      lastScrollHeight = container.scrollHeight;
      if (!userScrolledRef.current && !catchingUpRef.current) {
        container.scrollTop = container.scrollHeight;
      }
    });
    // .messages-list may not exist yet (loading spinner). Use MutationObserver
    // to attach ResizeObserver when it appears.
    const attachRO = () => {
      const list = container.querySelector(".messages-list");
      if (list) {
        ro.observe(list);
        return true;
      }
      return false;
    };
    let mo: MutationObserver | null = null;
    if (!attachRO()) {
      mo = new MutationObserver((_, self) => {
        if (attachRO()) { self.disconnect(); mo = null; }
      });
      mo.observe(container, { childList: true, subtree: true });
    }

    return () => {
      container.removeEventListener("scroll", handleScroll);
      if (scrollSaveTimerRef.current) clearTimeout(scrollSaveTimerRef.current);
      mo?.disconnect();
      ro.disconnect();
    };
  }, [scrollStore]);

  // Scroll after React commits the DOM, before the browser paints.
  // Handles both initial load (pending scroll from loadMessages) and streaming updates.
  useLayoutEffect(() => {
    if (loading) return;
    const pending = pendingScrollRef.current;
    if (pending !== undefined) {
      pendingScrollRef.current = undefined;
      if (pending != null) {
        const container = messagesContainerRef.current;
        if (container) {
          container.scrollTop = pending;
          const isNearBottom = container.scrollHeight - pending - container.clientHeight < 100;
          userScrolledRef.current = !isNearBottom;
          setShowScrollToBottom(!isNearBottom);
        }
      } else {
        scrollToBottom();
      }
      return;
    }
    if (!userScrolledRef.current && !catchingUpRef.current) {
      scrollToBottom();
    }
  }, [messages, loading]);

  // Close overflow menu when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (overflowMenuRef.current && !overflowMenuRef.current.contains(event.target as Node)) {
        setShowOverflowMenu(false);
      }
    };

    if (showOverflowMenu) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => {
        document.removeEventListener("mousedown", handleClickOutside);
      };
    }
  }, [showOverflowMenu]);

  // Check connection health - returns true if connection needs to be re-established
  const checkConnectionHealth = useCallback(() => {
    if (!conversationId) return false;

    const es = eventSourceRef.current;
    // No connection exists
    if (!es) return true;
    // EventSource.CLOSED = 2, EventSource.CONNECTING = 0
    // If closed or errored, we need to reconnect
    if (es.readyState === 2) return true;
    // If still connecting after coming back, that's fine
    return false;
  }, [conversationId]);

  // Track when the page was last hidden (for detecting stale connections on iOS Safari)
  const hiddenAtRef = useRef<number | null>(null);

  // Suppress auto-scroll during catch-up after returning from a backgrounded tab
  const catchingUpRef = useRef(false);

  const loadMessages = async () => {
    if (!conversationId) return;
    try {
      loadingRef.current = true;
      setLoading(true);
      setError(null);
      const response = await api.getConversation(conversationId);
      // Set pending scroll target before state updates so useLayoutEffect can handle it.
      pendingScrollRef.current = scrollStore.load();
      setMessages(response.messages ?? []);
      loadingRef.current = false;
      setLoading(false);
      // ConversationState is sent via the streaming endpoint, not on initial load
      // We don't update agentWorking here - the stream will provide the current state
      // Always update context window size when loading a conversation.
      // If omitted from response (due to omitempty when 0), default to 0.
      setContextWindowSize(response.context_window_size ?? 0);
      if (onConversationUpdate) {
        onConversationUpdate(response.conversation);
      }
    } catch (err) {
      console.error("Failed to load messages:", err);
      setError("Failed to load messages");
      loadingRef.current = false;
      setLoading(false);
    }
  };

  const setupMessageStream = useCallback(() => {
    const resetHeartbeatTimeout = () => {
      if (heartbeatTimeoutRef.current) {
        clearTimeout(heartbeatTimeoutRef.current);
      }
      // If we don't receive any message (including heartbeat) within 60 seconds, reconnect
      heartbeatTimeoutRef.current = window.setTimeout(() => {
        console.warn("No heartbeat received in 60 seconds, reconnecting...");
        if (eventSourceRef.current) {
          eventSourceRef.current.close();
          eventSourceRef.current = null;
        }
        setupMessageStream();
      }, 60000);
    };

    if (!conversationId) return;

    if (eventSourceRef.current) {
      eventSourceRef.current.close();
    }

    // Clear any existing heartbeat timeout
    if (heartbeatTimeoutRef.current) {
      clearTimeout(heartbeatTimeoutRef.current);
    }

    // Use last_sequence_id to resume from where we left off (avoids resending all messages)
    const lastSeqId = lastSequenceIdRef.current;
    const eventSource = api.createMessageStream(
      conversationId,
      lastSeqId >= 0 ? lastSeqId : undefined,
    );
    eventSourceRef.current = eventSource;

    eventSource.onmessage = (event) => {
      // Reset heartbeat timeout on every message
      resetHeartbeatTimeout();

      // Clear catch-up flag after the first message event (the catch-up batch)
      // so that subsequent messages auto-scroll normally again.
      catchingUpRef.current = false;

      try {
        const streamResponse: StreamResponse = JSON.parse(event.data);
        const incomingMessages = Array.isArray(streamResponse.messages)
          ? streamResponse.messages
          : [];

        // Track the latest sequence ID for reconnection
        if (incomingMessages.length > 0) {
          const maxSeqId = Math.max(...incomingMessages.map((m) => m.sequence_id));
          if (maxSeqId > lastSequenceIdRef.current) {
            lastSequenceIdRef.current = maxSeqId;
          }
        }

        // Merge new messages without losing existing ones.
        // If no new messages (e.g., only conversation/slug update or heartbeat), keep existing list.
        if (incomingMessages.length > 0) {
          setMessages((prev) => {
            const byId = new Map<string, Message>();
            for (const m of prev) byId.set(m.message_id, m);
            for (const m of incomingMessages) byId.set(m.message_id, m);
            // Preserve original order, then append truly new ones in the order received
            const result: Message[] = [];
            for (const m of prev) result.push(byId.get(m.message_id)!);
            for (const m of incomingMessages) {
              if (!prev.find((p) => p.message_id === m.message_id)) result.push(m);
            }
            return result;
          });
        }

        // Update conversation data if provided
        if (onConversationUpdate && streamResponse.conversation) {
          onConversationUpdate(streamResponse.conversation);
        }

        // Handle conversation list updates (for other conversations)
        if (onConversationListUpdate && streamResponse.conversation_list_update) {
          onConversationListUpdate(streamResponse.conversation_list_update);
        }

        // Handle conversation state updates (explicit from server)
        if (streamResponse.conversation_state) {
          // Update the conversations list with new working state
          if (onConversationStateUpdate) {
            onConversationStateUpdate(streamResponse.conversation_state);
          }
          // Update local state if this is for our conversation
          if (streamResponse.conversation_state.conversation_id === conversationId) {
            setAgentWorking(streamResponse.conversation_state.working);
            // Update selected model from conversation (ensures consistency across sessions)
            if (streamResponse.conversation_state.model) {
              setSelectedModel(streamResponse.conversation_state.model);
            }
          }
        }

        // Dispatch notification events to registered handlers
        if (streamResponse.notification_event) {
          handleNotificationEvent(streamResponse.notification_event);
        }

        if (typeof streamResponse.context_window_size === "number") {
          setContextWindowSize(streamResponse.context_window_size);
        }
      } catch (err) {
        console.error("Failed to parse message stream data:", err);
      }
    };

    eventSource.onerror = (event) => {
      console.warn("Message stream error (will retry):", event);
      // Close and retry after a delay
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
        eventSourceRef.current = null;
      }

      // Clear heartbeat timeout on error
      if (heartbeatTimeoutRef.current) {
        clearTimeout(heartbeatTimeoutRef.current);
        heartbeatTimeoutRef.current = null;
      }

      // Backoff delays: 1s, 2s, 5s, then show disconnected but keep retrying periodically
      const delays = [1000, 2000, 5000];

      setReconnectAttempts((prev) => {
        const attempts = prev + 1;

        if (attempts > delays.length) {
          // Show disconnected UI but start periodic retry every 30 seconds
          setIsReconnecting(false);
          setIsDisconnected(true);
          if (!periodicRetryRef.current) {
            periodicRetryRef.current = window.setInterval(() => {
              if (eventSourceRef.current === null) {
                console.log("Periodic reconnect attempt");
                setupMessageStream();
              }
            }, 30000);
          }
          return attempts;
        }

        // Show reconnecting indicator during backoff attempts
        setIsReconnecting(true);
        const delay = delays[attempts - 1];
        console.log(`Reconnecting in ${delay}ms (attempt ${attempts}/${delays.length})`);

        reconnectTimeoutRef.current = window.setTimeout(() => {
          if (eventSourceRef.current === null) {
            setupMessageStream();
          }
        }, delay);

        return attempts;
      });
    };

    eventSource.onopen = () => {
      console.log("Message stream connected");
      // Refresh conversations list on reconnect (may have missed updates while disconnected)
      if (hasConnectedRef.current) {
        onReconnect?.();
      }
      hasConnectedRef.current = true;
      // Reset reconnect attempts and clear periodic retry on successful connection
      setReconnectAttempts(0);
      setIsDisconnected(false);
      setIsReconnecting(false);
      if (periodicRetryRef.current) {
        clearInterval(periodicRetryRef.current);
        periodicRetryRef.current = null;
      }
      // Start heartbeat timeout monitoring
      resetHeartbeatTimeout();
    };
  }, [conversationId, onConversationUpdate, onConversationListUpdate, onConversationStateUpdate]);

  // Force-reconnect: close existing connection and reconnect to get missed messages
  const forceReconnect = useCallback(() => {
    if (!conversationId) return;
    if (eventSourceRef.current) {
      eventSourceRef.current.close();
      eventSourceRef.current = null;
    }
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current);
      reconnectTimeoutRef.current = null;
    }
    if (periodicRetryRef.current) {
      clearInterval(periodicRetryRef.current);
      periodicRetryRef.current = null;
    }
    setIsDisconnected(false);
    setIsReconnecting(false);
    setReconnectAttempts(0);
    setupMessageStream();
  }, [conversationId, setupMessageStream]);

  // Reconnect only if connection is dead
  const reconnect = useCallback(() => {
    if (!eventSourceRef.current || eventSourceRef.current.readyState === 2) {
      forceReconnect();
    }
  }, [forceReconnect]);

  // Reconnect when page becomes visible, focused, or online
  useEffect(() => {
    const handleVisibilityChange = () => {
      if (document.visibilityState === "hidden") {
        hiddenAtRef.current = Date.now();
        return;
      }
      // Page became visible
      const hiddenFor = hiddenAtRef.current ? Date.now() - hiddenAtRef.current : 0;
      hiddenAtRef.current = null;

      if (checkConnectionHealth()) {
        // Connection is already known-dead
        console.log("Tab visible: connection unhealthy, reconnecting");
        catchingUpRef.current = true;
        reconnect();
      } else if (hiddenFor > 5000) {
        // On iOS Safari, backgrounded tabs have their TCP connections killed
        // but EventSource.readyState may still show OPEN. Force reconnect
        // to pick up any missed messages from the server.
        console.log(`Tab visible after ${Math.round(hiddenFor / 1000)}s hidden, force reconnecting`);
        catchingUpRef.current = true;
        forceReconnect();
      }
    };

    const handleFocus = () => {
      if (checkConnectionHealth()) {
        console.log("Window focus: connection unhealthy, reconnecting");
        reconnect();
      }
    };

    const handleOnline = () => {
      if (checkConnectionHealth()) {
        console.log("Online: connection unhealthy, reconnecting");
        reconnect();
      }
    };

    document.addEventListener("visibilitychange", handleVisibilityChange);
    window.addEventListener("focus", handleFocus);
    window.addEventListener("online", handleOnline);

    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      window.removeEventListener("focus", handleFocus);
      window.removeEventListener("online", handleOnline);
    };
  }, [checkConnectionHealth, reconnect, forceReconnect]);

  const sendMessage = async (message: string) => {
    if (!message.trim() || sending) return;

    // Check if this is a shell command (starts with "!")
    const trimmedMessage = message.trim();
    if (trimmedMessage.startsWith("!")) {
      const shellCommand = trimmedMessage.slice(1).trim();
      if (shellCommand) {
        // Create an ephemeral terminal
        const terminal: EphemeralTerminal = {
          id: `term-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
          command: shellCommand,
          cwd:
            currentConversation?.cwd || selectedCwd || window.__SHELLEY_INIT__?.default_cwd || "/",
          createdAt: new Date(),
        };
        setEphemeralTerminals((prev) => [...prev, terminal]);
        // Auto-focus interactive shells
        const firstWord = shellCommand.split(/\s+/)[0];
        const baseName = firstWord.split("/").pop() || firstWord;
        const interactiveShells = ["bash", "sh", "zsh", "fish", "nu", "nushell"];
        if (interactiveShells.includes(baseName)) {
          setTerminalAutoFocusId(terminal.id);
        }
        // Scroll to bottom to show the new terminal
        setTimeout(() => scrollToBottom(), 100);
      }
      return;
    }

    try {
      setSending(true);
      setError(null);
      setAgentWorking(true);

      // If no conversation ID, this is the first message - validate cwd first
      if (!conversationId && onFirstMessage) {
        // Validate cwd if provided
        if (selectedCwd) {
          const validation = await api.validateCwd(selectedCwd);
          if (!validation.valid) {
            throw new Error(`Invalid working directory: ${validation.error}`);
          }
        }
        await onFirstMessage(message.trim(), selectedModel, selectedCwd || undefined);
      } else if (conversationId) {
        await api.sendMessage(conversationId, {
          message: message.trim(),
          model: selectedModel,
        });
      }
    } catch (err) {
      console.error("Failed to send message:", err);
      const message = err instanceof Error ? err.message : "Unknown error";
      setError(message);
      setAgentWorking(false);
      throw err; // Re-throw so MessageInput can preserve the text
    } finally {
      setSending(false);
    }
  };

  const scrollToBottom = () => {
    const container = messagesContainerRef.current;
    if (container) {
      container.scrollTop = container.scrollHeight;
    }
    userScrolledRef.current = false;
    setShowScrollToBottom(false);
  };

  // Callback for terminals to insert text into the message input
  const handleInsertFromTerminal = useCallback((text: string) => {
    setTerminalInjectedText(text);
  }, []);

  // Handle external trigger to open diff viewer
  useEffect(() => {
    if (openDiffViewerTrigger && openDiffViewerTrigger > 0) {
      setShowDiffViewer(true);
    }
  }, [openDiffViewerTrigger]);

  const handleCancel = async () => {
    if (!conversationId || cancelling) return;

    try {
      setCancelling(true);
      await api.cancelConversation(conversationId);
      setAgentWorking(false);
    } catch (err) {
      console.error("Failed to cancel conversation:", err);
      setError("Failed to cancel. Please try again.");
    } finally {
      setCancelling(false);
    }
  };

  // Handler to continue conversation in a new one
  const handleContinueConversation = async () => {
    if (!conversationId || !onContinueConversation) return;
    await onContinueConversation(
      conversationId,
      selectedModel,
      currentConversation?.cwd || selectedCwd || undefined,
    );
  };

  // Handler to distill and continue conversation
  const handleDistillConversation = async () => {
    if (!conversationId || !onDistillConversation) return;
    await onDistillConversation(
      conversationId,
      selectedModel,
      currentConversation?.cwd || selectedCwd || undefined,
    );
  };

  const getDisplayTitle = () => {
    return currentConversation?.slug || "Shelley";
  };

  // Process messages to coalesce tool calls
  const processMessages = () => {
    if (messages.length === 0) {
      return [];
    }

    interface CoalescedItem {
      type: "message" | "tool";
      message?: Message;
      toolUseId?: string;
      toolName?: string;
      toolInput?: unknown;
      toolResult?: LLMContent[];
      toolError?: boolean;
      toolStartTime?: string | null;
      toolEndTime?: string | null;
      hasResult?: boolean;
      display?: unknown;
    }

    const coalescedItems: CoalescedItem[] = [];
    const toolResultMap: Record<
      string,
      {
        result: LLMContent[];
        error: boolean;
        startTime: string | null;
        endTime: string | null;
      }
    > = {};
    // Some tool results may be delivered only as display_data (e.g., screenshots)
    const displayResultSet: Set<string> = new Set();
    const displayDataMap: Record<string, unknown> = {};

    // First pass: collect all tool results
    messages.forEach((message) => {
      // Collect tool_result data from llm_data if present
      if (message.llm_data) {
        try {
          const llmData =
            typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            llmData.Content.forEach((content: LLMContent) => {
              if (content && content.Type === 6 && content.ToolUseID) {
                // tool_result
                toolResultMap[content.ToolUseID] = {
                  result: content.ToolResult || [],
                  error: content.ToolError || false,
                  startTime: content.ToolUseStartTime || null,
                  endTime: content.ToolUseEndTime || null,
                };
              }
            });
          }
        } catch (err) {
          console.error("Failed to parse message LLM data for tool results:", err);
        }
      }

      // Also collect tool_use_ids from display_data to mark completion even if llm_data is omitted
      if (message.display_data) {
        try {
          const displays =
            typeof message.display_data === "string"
              ? JSON.parse(message.display_data)
              : message.display_data;
          if (Array.isArray(displays)) {
            for (const d of displays) {
              if (
                d &&
                typeof d === "object" &&
                "tool_use_id" in d &&
                typeof d.tool_use_id === "string"
              ) {
                displayResultSet.add(d.tool_use_id);
                // Store the display data for this tool use
                if ("display" in d) {
                  displayDataMap[d.tool_use_id] = d.display;
                }
              }
            }
          }
        } catch (err) {
          console.error("Failed to parse display_data for tool completion:", err);
        }
      }
    });

    // Second pass: process messages and extract tool uses
    messages.forEach((message) => {
      // Allow system messages with distill_status through, skip others
      if (message.type === "system") {
        if (!isDistillStatusMessage(message)) {
          return;
        }
        coalescedItems.push({ type: "message", message });
        return;
      }

      if (message.type === "error") {
        coalescedItems.push({ type: "message", message });
        return;
      }

      // Check if this is a user message with tool results (skip rendering them as messages)
      let hasToolResult = false;
      if (message.llm_data) {
        try {
          const llmData =
            typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            hasToolResult = llmData.Content.some((c: LLMContent) => c.Type === 6);
          }
        } catch (err) {
          console.error("Failed to parse message LLM data:", err);
        }
      }

      // If it's a user message without tool results, show it
      if (message.type === "user" && !hasToolResult) {
        coalescedItems.push({ type: "message", message });
        return;
      }

      // If it's a user message with tool results, skip it (we'll handle it via the toolResultMap)
      if (message.type === "user" && hasToolResult) {
        return;
      }

      if (message.llm_data) {
        try {
          const llmData =
            typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
          if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
            // Extract text content and tool uses separately
            const textContents: LLMContent[] = [];
            const toolUses: LLMContent[] = [];

            llmData.Content.forEach((content: LLMContent) => {
              if (content.Type === 2) {
                // text
                textContents.push(content);
              } else if (content.Type === 5) {
                // tool_use
                toolUses.push(content);
              }
            });

            // If we have text content, add it as a message (but only if it's not empty)
            const textString = textContents
              .map((c) => c.Text || "")
              .join("")
              .trim();
            if (textString) {
              coalescedItems.push({ type: "message", message });
            }

            // Check if this message was truncated (tool calls lost)
            const wasTruncated = llmData.ExcludedFromContext === true;

            // Add tool uses as separate items
            toolUses.forEach((toolUse) => {
              const resultData = toolUse.ID ? toolResultMap[toolUse.ID] : undefined;
              const completedViaDisplay = toolUse.ID ? displayResultSet.has(toolUse.ID) : false;
              const displayData = toolUse.ID ? displayDataMap[toolUse.ID] : undefined;
              coalescedItems.push({
                type: "tool",
                toolUseId: toolUse.ID,
                toolName: toolUse.ToolName,
                toolInput: toolUse.ToolInput,
                toolResult: resultData?.result,
                // Mark as error if truncated and no result
                toolError: resultData?.error || (wasTruncated && !resultData),
                toolStartTime: resultData?.startTime,
                toolEndTime: resultData?.endTime,
                // Mark as complete if truncated (tool was lost, not running)
                hasResult: !!resultData || completedViaDisplay || wasTruncated,
                display: displayData,
              });
            });
          }
        } catch (err) {
          console.error("Failed to parse message LLM data:", err);
          coalescedItems.push({ type: "message", message });
        }
      } else {
        coalescedItems.push({ type: "message", message });
      }
    });

    return coalescedItems;
  };

  const renderMessages = () => {
    if (messages.length === 0) {
      const proxyURL = `https://${hostname}/`;
      return (
        <div className="empty-state">
          <div className="empty-state-content">
            <p className="text-base" style={{ marginBottom: "1rem", lineHeight: "1.6" }}>
              Shelley is an agent, running on <strong>{hostname}</strong>. You can ask Shelley to do
              stuff. If you build a web site with Shelley, you can use exe.dev&apos;s proxy features
              (see{" "}
              <a
                href="https://exe.dev/docs/proxy"
                target="_blank"
                rel="noopener noreferrer"
                style={{ color: "var(--blue-text)", textDecoration: "underline" }}
              >
                docs
              </a>
              ) to visit it over the web at{" "}
              <a
                href={proxyURL}
                target="_blank"
                rel="noopener noreferrer"
                style={{ color: "var(--blue-text)", textDecoration: "underline" }}
              >
                {proxyURL}
              </a>
              .
            </p>
            {models.length === 0 ? (
              <div className="add-model-hint">
                <p className="text-sm" style={{ color: "var(--text-secondary)" }}>
                  No AI models configured. Press <kbd>Ctrl</kbd>
                  <span>+</span>
                  <kbd>K</kbd> or <kbd>⌘</kbd>
                  <span>+</span>
                  <kbd>K</kbd> to add a model.
                </p>
              </div>
            ) : (
              <p className="text-sm" style={{ color: "var(--text-secondary)" }}>
                Send a message to start the conversation.
              </p>
            )}
          </div>
        </div>
      );
    }

    const coalescedItems = processMessages();

    const rendered = coalescedItems.map((item, index) => {
      if (item.type === "message" && item.message) {
        return (
          <MessageComponent
            key={item.message.message_id}
            message={item.message}
            onOpenDiffViewer={(commit, cwd) => {
              setDiffViewerInitialCommit(commit);
              setDiffViewerCwd(cwd);
              setShowDiffViewer(true);
            }}
            onCommentTextChange={setDiffCommentText}
          />
        );
      } else if (item.type === "tool") {
        return (
          <CoalescedToolCall
            key={item.toolUseId || `tool-${index}`}
            toolName={item.toolName || "Unknown Tool"}
            toolInput={item.toolInput}
            toolResult={item.toolResult}
            toolError={item.toolError}
            toolStartTime={item.toolStartTime}
            toolEndTime={item.toolEndTime}
            hasResult={item.hasResult}
            display={item.display}
            onCommentTextChange={setDiffCommentText}
          />
        );
      }
      return null;
    });

    // Find system prompt message to render at the top (exclude distill status messages)
    const systemMessage = messages.find((m) => m.type === "system" && !isDistillStatusMessage(m));

    return [
      systemMessage && <SystemPromptView key="system-prompt" message={systemMessage} />,
      ...rendered,
    ];
  };

  return (
    <div className="full-height flex flex-col">
      {/* Header */}
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

          {/* Expand drawer button - desktop only when collapsed */}
          {isDrawerCollapsed && onToggleDrawerCollapse && (
            <button
              onClick={onToggleDrawerCollapse}
              className="btn-icon show-on-desktop-only"
              aria-label="Expand sidebar"
              title="Expand sidebar"
            >
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M13 5l7 7-7 7M5 5l7 7-7 7"
                />
              </svg>
            </button>
          )}

          <h1 className="header-title" title={currentConversation?.slug || "Shelley"}>
            {getDisplayTitle()}
          </h1>
        </div>

        <div className="header-actions">
          {/* Green + icon in circle for new conversation */}
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

          {/* Overflow menu */}
          <div ref={overflowMenuRef} style={{ position: "relative" }}>
            <button
              onClick={() => setShowOverflowMenu(!showOverflowMenu)}
              className="btn-icon"
              aria-label="More options"
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
                {/* Diffs button - show when we have a CWD */}
                {(currentConversation?.cwd || selectedCwd) && (
                  <button
                    onClick={() => {
                      setShowOverflowMenu(false);
                      setShowDiffViewer(true);
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
                        d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
                      />
                    </svg>
                    Diffs
                  </button>
                )}
                {terminalURL && (
                  <button
                    onClick={() => {
                      setShowOverflowMenu(false);
                      const cwd = currentConversation?.cwd || selectedCwd || "";
                      const url = terminalURL.replace("WORKING_DIR", encodeURIComponent(cwd));
                      window.open(url, "_blank");
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
                        d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"
                      />
                    </svg>
                    Terminal
                  </button>
                )}
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
                <div className="overflow-menu-divider" />
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
                  Check for New Version
                  {hasUpdate && <span className="version-menu-dot" />}
                </button>

                {/* Theme selector */}
                <div className="overflow-menu-divider" />
                <div className="theme-toggle-row">
                  <button
                    onClick={() => {
                      setThemeMode("system");
                      setStoredTheme("system");
                      applyTheme("system");
                    }}
                    className={`theme-toggle-btn${themeMode === "system" ? " theme-toggle-btn-selected" : ""}`}
                    title="System"
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
                    title="Light"
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
                    title="Dark"
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

                {/* Browser notifications toggle */}
                {typeof Notification !== "undefined" && (
                  <>
                    <div className="overflow-menu-divider" />
                    <div className="theme-toggle-row">
                      <button
                        onClick={async () => {
                          if (browserNotifsEnabled) return;
                          const granted = await requestBrowserNotificationPermission();
                          if (granted) {
                            setBrowserNotifsEnabled(true);
                          }
                        }}
                        className={`theme-toggle-btn${browserNotifsEnabled ? " theme-toggle-btn-selected" : ""}`}
                        title={
                          getBrowserNotificationState() === "denied"
                            ? "Blocked by browser"
                            : "Enable notifications"
                        }
                        disabled={getBrowserNotificationState() === "denied"}
                      >
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
                          />
                        </svg>
                      </button>
                      <button
                        onClick={() => {
                          if (!browserNotifsEnabled) return;
                          setChannelEnabled("browser", false);
                          setBrowserNotifsEnabled(false);
                        }}
                        className={`theme-toggle-btn${!browserNotifsEnabled ? " theme-toggle-btn-selected" : ""}`}
                        title="Disable notifications"
                      >
                        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M5.586 15H4l1.405-1.405A2.032 2.032 0 006 12.158V9a6.002 6.002 0 014-5.659V3a2 2 0 114 0v.341c.588.17 1.14.432 1.636.772M15 17h-6v1a3 3 0 006 0v-1zM18 9a3 3 0 00-3-3M3 3l18 18"
                          />
                        </svg>
                      </button>
                    </div>
                  </>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Messages area */}
      {/* Messages area with scroll-to-bottom button wrapper */}
      <div className="messages-area-wrapper">
        <div className="messages-container scrollable" ref={messagesContainerRef}>
          {loading ? (
            <div className="flex items-center justify-center full-height">
              <div className="spinner"></div>
            </div>
          ) : (
            <div className="messages-list">
              {renderMessages()}

            </div>
          )}
        </div>

        {/* Scroll to bottom button - outside scrollable area */}
        {showScrollToBottom && (
          <button
            className="scroll-to-bottom-button"
            onClick={scrollToBottom}
            aria-label="Scroll to bottom"
          >
            <svg
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              style={{ width: "1.25rem", height: "1.25rem" }}
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M19 14l-7 7m0 0l-7-7m7 7V3"
              />
            </svg>
          </button>
        )}
      </div>

      {/* Terminal Panel - between messages and status bar */}
      <TerminalPanel
        terminals={ephemeralTerminals}
        onClose={(id) => setEphemeralTerminals((prev) => prev.filter((t) => t.id !== id))}
        onInsertIntoInput={handleInsertFromTerminal}
        autoFocusId={terminalAutoFocusId}
        onAutoFocusConsumed={() => setTerminalAutoFocusId(null)}
        onActiveTerminalExited={() => {
          const input = document.querySelector<HTMLTextAreaElement>(
            '[data-testid="message-input"]',
          );
          input?.focus();
        }}
      />

      {/* Unified Status Bar */}
      <div className="status-bar">
        <div className="status-bar-content">
          {isDisconnected ? (
            // Disconnected state
            <>
              <span className="status-message status-warning">Disconnected</span>
              <button
                onClick={reconnect}
                className="status-button status-button-primary"
              >
                Retry
              </button>
            </>
          ) : isReconnecting ? (
            // Reconnecting state - show during backoff attempts
            <>
              <span className="status-message status-reconnecting">
                Reconnecting{reconnectAttempts > 0 ? ` (${reconnectAttempts}/3)` : ""}
                <span className="reconnecting-dots">...</span>
              </span>
            </>
          ) : error ? (
            // Error state
            <>
              <span className="status-message status-error">{error}</span>
              <button onClick={() => setError(null)} className="status-button status-button-text">
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M6 18L18 6M6 6l12 12"
                  />
                </svg>
              </button>
            </>
          ) : agentWorking && conversationId ? (
            // Agent working - show status with stop button and context bar
            <div className="status-bar-active" data-testid="agent-thinking">
              <div className="status-working-group">
                <AnimatedWorkingStatus />
                <button
                  onClick={handleCancel}
                  disabled={cancelling}
                  className="status-stop-button"
                  title={cancelling ? "Cancelling..." : "Stop"}
                >
                  <svg viewBox="0 0 24 24" fill="currentColor">
                    <rect x="6" y="6" width="12" height="12" rx="1" />
                  </svg>
                  <span className="status-stop-label">{cancelling ? "Cancelling..." : "Stop"}</span>
                </button>
              </div>
              <ContextUsageBar
                contextWindowSize={contextWindowSize}
                maxContextTokens={
                  models.find((m) => m.id === selectedModel)?.max_context_tokens || 200000
                }
                conversationId={conversationId}
                onContinueConversation={
                  onContinueConversation ? handleContinueConversation : undefined
                }
                onDistillConversation={
                  onDistillConversation ? handleDistillConversation : undefined
                }
              />
            </div>
          ) : // Idle state - show ready message, or configuration for empty conversation
          !conversationId ? (
            // Empty conversation - show model (left) and cwd (right)
            <div className="status-bar-new-conversation">
              {/* Model selector - far left */}
              <div
                className="status-field status-field-model"
                title="AI model to use for this conversation"
              >
                <span className="status-field-label">Model:</span>
                <ModelPicker
                  models={models}
                  selectedModel={selectedModel}
                  onSelectModel={setSelectedModel}
                  onManageModels={() => onOpenModelsModal?.()}
                  disabled={sending}
                />
              </div>

              {/* CWD indicator - far right */}
              <div
                className={`status-field status-field-cwd${cwdError ? " status-field-error" : ""}`}
                title={cwdError || "Working directory for file operations"}
              >
                <span className="status-field-label">Dir:</span>
                <button
                  className={`status-chip${cwdError ? " status-chip-error" : ""}`}
                  onClick={() => setShowDirectoryPicker(true)}
                  disabled={sending}
                >
                  {selectedCwd || "(no cwd)"}
                </button>
              </div>
            </div>
          ) : (
            // Active conversation - show Ready + context bar
            <div className="status-bar-active">
              <span className="status-message status-ready">Ready on {hostname}</span>
              <ContextUsageBar
                contextWindowSize={contextWindowSize}
                maxContextTokens={
                  models.find((m) => m.id === selectedModel)?.max_context_tokens || 200000
                }
                conversationId={conversationId}
                onContinueConversation={
                  onContinueConversation ? handleContinueConversation : undefined
                }
                onDistillConversation={
                  onDistillConversation ? handleDistillConversation : undefined
                }
              />
            </div>
          )}
        </div>
      </div>

      {/* Message input */}
      <MessageInput
        key={conversationId || "new"}
        onSend={sendMessage}
        disabled={sending || loading}
        autoFocus={true}
        injectedText={terminalInjectedText || diffCommentText}
        onClearInjectedText={() => {
          setDiffCommentText("");
          setTerminalInjectedText(null);
        }}
        persistKey={conversationId || "new-conversation"}
      />

      {/* Directory Picker Modal */}
      <DirectoryPickerModal
        isOpen={showDirectoryPicker}
        onClose={() => setShowDirectoryPicker(false)}
        onSelect={(path) => {
          setSelectedCwd(path);
          setCwdError(null);
        }}
        initialPath={selectedCwd}
      />

      {/* Diff Viewer */}
      <DiffViewer
        cwd={diffViewerCwd || currentConversation?.cwd || selectedCwd}
        isOpen={showDiffViewer}
        onClose={() => {
          setShowDiffViewer(false);
          setDiffViewerInitialCommit(undefined);
          setDiffViewerCwd(undefined);
        }}
        onCommentTextChange={setDiffCommentText}
        initialCommit={diffViewerInitialCommit}
        onCwdChange={setDiffViewerCwd}
      />

      {/* Version Checker Modal */}
      {VersionModal}
    </div>
  );
}

export default ChatInterface;
