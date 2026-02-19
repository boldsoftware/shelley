import React, { useState, useRef, useEffect } from "react";
import { copyToClipboard } from "../utils/clipboard";
import { linkifyText } from "../utils/linkify";
import {
  Message as MessageType,
  LLMMessage,
  LLMContent,
  Usage,
  isDistillStatusMessage,
} from "../types";
import BashTool from "./BashTool";
import PatchTool from "./PatchTool";
import ScreenshotTool from "./ScreenshotTool";
import GenericTool from "./GenericTool";

import KeywordSearchTool from "./KeywordSearchTool";
import BrowserNavigateTool from "./BrowserNavigateTool";
import BrowserEvalTool from "./BrowserEvalTool";
import ReadImageTool from "./ReadImageTool";
import BrowserConsoleLogsTool from "./BrowserConsoleLogsTool";
import ChangeDirTool from "./ChangeDirTool";
import BrowserResizeTool from "./BrowserResizeTool";
import SubagentTool from "./SubagentTool";
import OutputIframeTool from "./OutputIframeTool";
import ThinkingContent from "./ThinkingContent";
import UsageDetailModal from "./UsageDetailModal";
import MessageActionBar from "./MessageActionBar";

// Display data types from different tools
interface ToolDisplay {
  tool_use_id: string;
  tool_name?: string;
  display: unknown;
}

interface MessageProps {
  message: MessageType;
  onOpenDiffViewer?: (commit: string, cwd?: string) => void;
  onCommentTextChange?: (text: string) => void;
}

// Copy icon for the commit hash copy button
const CopyIcon = () => (
  <svg
    width="12"
    height="12"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    style={{ verticalAlign: "middle" }}
  >
    <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
  </svg>
);

const CheckIcon = () => (
  <svg
    width="12"
    height="12"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
    style={{ verticalAlign: "middle" }}
  >
    <polyline points="20 6 9 17 4 12" />
  </svg>
);

// GitInfoMessage renders a compact git state notification
function GitInfoMessage({
  message,
  onOpenDiffViewer,
}: {
  message: MessageType;
  onOpenDiffViewer?: (commit: string, cwd?: string) => void;
}) {
  const [copied, setCopied] = useState(false);

  // Parse user_data which contains structured git state info
  let commitHash: string | null = null;
  let subject: string | null = null;
  let branch: string | null = null;
  let worktree: string | null = null;

  if (message.user_data) {
    try {
      const userData =
        typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
      if (userData.commit) {
        commitHash = userData.commit;
      }
      if (userData.subject) {
        subject = userData.subject;
      }
      if (userData.branch) {
        branch = userData.branch;
      }
      if (userData.worktree) {
        worktree = userData.worktree;
      }
    } catch (err) {
      console.error("Failed to parse gitinfo user_data:", err);
    }
  }

  if (!commitHash) {
    return null;
  }

  const canShowDiff = commitHash && onOpenDiffViewer;

  const handleDiffClick = () => {
    if (commitHash && onOpenDiffViewer) {
      onOpenDiffViewer(commitHash, worktree || undefined);
    }
  };

  const handleCopyHash = (e: React.MouseEvent) => {
    e.preventDefault();
    if (commitHash) {
      copyToClipboard(commitHash).then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      });
    }
  };

  // Truncate subject if too long
  const truncatedSubject = subject && subject.length > 40 ? subject.slice(0, 37) + "..." : subject;

  return (
    <div
      className="message message-gitinfo"
      data-testid="message-gitinfo"
      style={{
        padding: "0.4rem 1rem",
        fontSize: "0.8rem",
        color: "var(--text-secondary)",
        textAlign: "center",
        fontStyle: "italic",
      }}
    >
      <span>
        {worktree && (
          <span
            style={{
              fontFamily: "monospace",
              fontSize: "0.75rem",
              marginRight: "0.5em",
            }}
          >
            {worktree}
          </span>
        )}
        {branch && (
          <span
            style={{
              fontWeight: 500,
              fontStyle: "normal",
            }}
          >
            {branch}
          </span>
        )}
        {branch ? " now at " : "now at "}
        <code
          onClick={handleCopyHash}
          title="Click to copy commit hash"
          style={{
            fontFamily: "monospace",
            fontSize: "0.75rem",
            background: "var(--bg-tertiary)",
            padding: "0.1em 0.3em",
            borderRadius: "3px",
            cursor: "pointer",
          }}
        >
          {commitHash}
        </code>
        <button
          onClick={handleCopyHash}
          title="Copy commit hash"
          style={{
            background: "none",
            border: "none",
            padding: "0.1em 0.3em",
            cursor: "pointer",
            color: copied ? "var(--success-color, #22c55e)" : "var(--text-tertiary)",
            verticalAlign: "middle",
            marginLeft: "0.2em",
          }}
        >
          {copied ? <CheckIcon /> : <CopyIcon />}
        </button>
        {truncatedSubject && (
          <span style={{ marginLeft: "0.3em" }} title={subject || undefined}>
            "{truncatedSubject}"
          </span>
        )}
        {canShowDiff && (
          <>
            {" "}
            <a
              href="#"
              onClick={(e) => {
                e.preventDefault();
                handleDiffClick();
              }}
              style={{
                color: "var(--link-color, #0066cc)",
                textDecoration: "underline",
              }}
            >
              diff
            </a>
          </>
        )}
      </span>
    </div>
  );
}

// DistillStatusMessage renders a compact status message for conversation distillation
function DistillStatusMessage({ message }: { message: MessageType }) {
  let status = "in_progress";
  let sourceSlug = "";

  if (message.user_data) {
    try {
      const userData =
        typeof message.user_data === "string" ? JSON.parse(message.user_data) : message.user_data;
      status = userData.distill_status || "in_progress";
      sourceSlug = userData.source_slug || "";
    } catch {
      // ignore parse errors
    }
  }

  const isInProgress = status === "in_progress";
  const isError = status === "error";

  return (
    <div
      className="message message-gitinfo"
      style={{
        padding: "0.5rem 1rem",
        fontSize: "0.8rem",
        color: isError ? "var(--error-text)" : "var(--text-secondary)",
        textAlign: "center",
        fontStyle: "italic",
      }}
    >
      {isInProgress && (
        <span>
          <span
            className="spinner spinner-small"
            style={{
              display: "inline-block",
              marginRight: "6px",
              verticalAlign: "middle",
            }}
          />
          Distilling conversation{sourceSlug ? ` "${sourceSlug}"` : ""}…
        </span>
      )}
      {status === "complete" && (
        <span>Distilled from{sourceSlug ? ` "${sourceSlug}"` : " prior conversation"}</span>
      )}
      {isError && <span>Distillation failed{sourceSlug ? ` for "${sourceSlug}"` : ""}</span>}
    </div>
  );
}

function Message({ message, onOpenDiffViewer, onCommentTextChange }: MessageProps) {
  // Render system messages with distill_status as status indicators
  if (message.type === "system") {
    if (isDistillStatusMessage(message)) {
      return <DistillStatusMessage message={message} />;
    }
    return null;
  }

  // Render gitinfo messages as compact status updates
  if (message.type === "gitinfo") {
    return <GitInfoMessage message={message} onOpenDiffViewer={onOpenDiffViewer} />;
  }

  // Action bar state (show on hover or tap)
  const [showActionBar, setShowActionBar] = useState(false);
  const [isHovered, setIsHovered] = useState(false);
  const [showUsageModal, setShowUsageModal] = useState(false);
  const messageRef = useRef<HTMLDivElement | null>(null);

  // Show action bar on hover or when explicitly tapped
  const actionBarVisible = showActionBar || isHovered;

  // Parse usage data if available (only for agent messages)
  let usage: Usage | null = null;
  if (message.type === "agent" && message.usage_data) {
    try {
      usage =
        typeof message.usage_data === "string"
          ? JSON.parse(message.usage_data)
          : message.usage_data;
    } catch (err) {
      console.error("Failed to parse usage data:", err);
    }
  }

  // Calculate duration if we have timing info
  let durationMs: number | null = null;
  if (usage?.start_time && usage?.end_time) {
    const start = new Date(usage.start_time).getTime();
    const end = new Date(usage.end_time).getTime();
    durationMs = end - start;
  }

  // Convert Go struct Type field (number) to string type
  // Based on llm/llm.go constants (iota continues across types in same const block):
  // MessageRoleUser = 0, MessageRoleAssistant = 1,
  // ContentTypeText = 2, ContentTypeThinking = 3, ContentTypeRedactedThinking = 4,
  // ContentTypeToolUse = 5, ContentTypeToolResult = 6
  const getContentType = (type: number): string => {
    switch (type) {
      case 0:
        return "message_role_user"; // Should not occur in Content, but handle gracefully
      case 1:
        return "message_role_assistant"; // Should not occur in Content, but handle gracefully
      case 2:
        return "text";
      case 3:
        return "thinking";
      case 4:
        return "redacted_thinking";
      case 5:
        return "tool_use";
      case 6:
        return "tool_result";
      default:
        return "unknown";
    }
  };

  // Get text content from message for copying (includes tool results and thinking)
  const getMessageText = (): string => {
    if (!llmMessage?.Content) return "";

    const textParts: string[] = [];
    llmMessage.Content.forEach((content) => {
      const contentType = getContentType(content.Type);
      if (contentType === "text" && content.Text) {
        textParts.push(content.Text);
      } else if (contentType === "thinking") {
        // Include thinking content
        const thinkingText = content.Thinking || content.Text;
        if (thinkingText) {
          textParts.push(`[Thinking]\n${thinkingText}`);
        }
      } else if (contentType === "tool_result" && content.ToolResult) {
        // Extract text from tool result content
        content.ToolResult.forEach((result) => {
          if (result.Text) {
            textParts.push(result.Text);
          }
        });
      }
    });
    return textParts.join("\n");
  };

  // Handle tap on message to toggle action bar (for mobile)
  const handleMessageClick = (e: React.MouseEvent) => {
    // Don't toggle if clicking on a link, button, or interactive element
    const target = e.target as HTMLElement;
    if (
      target.closest("a") ||
      target.closest("button") ||
      target.closest("[data-action-bar]") ||
      target.closest(".bash-tool-header") ||
      target.closest(".patch-tool-header") ||
      target.closest(".generic-tool-header") ||
      target.closest(".think-tool-header") ||
      target.closest(".keyword-search-tool-header") ||
      target.closest(".change-dir-tool-header") ||
      target.closest(".browser-tool-header") ||
      target.closest(".screenshot-tool-header")
    ) {
      return;
    }
    setShowActionBar((prev) => !prev);
  };

  // Handle mouse enter/leave for hover
  const handleMouseEnter = () => setIsHovered(true);
  const handleMouseLeave = () => setIsHovered(false);

  // Close action bar when clicking outside
  useEffect(() => {
    if (!showActionBar) return;

    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!messageRef.current?.contains(target)) {
        setShowActionBar(false);
      }
    };

    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [showActionBar]);

  // Handle copy action
  const handleCopy = () => {
    const text = getMessageText();
    if (text) {
      copyToClipboard(text).catch((err) => {
        console.error("Failed to copy text:", err);
      });
    }
    setShowActionBar(false);
  };

  // Handle usage detail action
  const handleShowUsage = () => {
    setShowUsageModal(true);
    setShowActionBar(false);
  };

  let displayData: ToolDisplay[] | null = null;
  if (message.display_data) {
    try {
      displayData =
        typeof message.display_data === "string"
          ? JSON.parse(message.display_data)
          : message.display_data;
    } catch (err) {
      console.error("Failed to parse display data:", err);
    }
  }

  // Parse LLM data if available
  let llmMessage: LLMMessage | null = null;
  if (message.llm_data) {
    try {
      llmMessage =
        typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    } catch (err) {
      console.error("Failed to parse LLM data:", err);
    }
  }

  const isUser = message.type === "user" && !hasToolResult(llmMessage);
  const isTool = message.type === "tool" || hasToolContent(llmMessage);
  const isError = message.type === "error";

  // Determine which actions to show in action bar
  const messageText = getMessageText();
  const hasCopyAction = !!messageText;
  const hasUsageAction = message.type === "agent" && !!usage;

  // Build a map of tool use IDs to their inputs for linking tool_result back to tool_use
  const toolUseMap: Record<string, { name: string; input: unknown }> = {};
  if (llmMessage && llmMessage.Content) {
    llmMessage.Content.forEach((content) => {
      if (content.Type === 5 && content.ID && content.ToolName) {
        // tool_use
        toolUseMap[content.ID] = {
          name: content.ToolName,
          input: content.ToolInput,
        };
      }
    });
  }

  const renderContent = (content: LLMContent) => {
    const contentType = getContentType(content.Type);

    switch (contentType) {
      case "message_role_user":
      case "message_role_assistant":
        // These shouldn't occur in Content objects, but display as text if they do
        return (
          <div
            style={{
              background: "#fff7ed",
              border: "1px solid #fed7aa",
              borderRadius: "0.25rem",
              padding: "0.5rem",
              fontSize: "0.875rem",
            }}
          >
            <div style={{ color: "#9a3412", fontFamily: "monospace" }}>
              [Unexpected message role content: {contentType}]
            </div>
            <div style={{ marginTop: "0.25rem" }}>{content.Text || JSON.stringify(content)}</div>
          </div>
        );
      case "text":
        return (
          <div className="whitespace-pre-wrap break-words">{linkifyText(content.Text || "")}</div>
        );
      case "tool_use":
        // IMPORTANT: When adding a new tool component here, also add it to:
        // 1. The tool_result case below
        // 2. TOOL_COMPONENTS map in ChatInterface.tsx
        // See AGENTS.md in this directory.

        // Use specialized component for bash tool
        if (content.ToolName === "bash") {
          return <BashTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for patch tool
        if (content.ToolName === "patch") {
          return (
            <PatchTool
              toolInput={content.ToolInput}
              isRunning={true}
              onCommentTextChange={onCommentTextChange}
            />
          );
        }
        // Use specialized component for screenshot tool
        if (content.ToolName === "screenshot" || content.ToolName === "browser_take_screenshot") {
          return <ScreenshotTool toolInput={content.ToolInput} isRunning={true} />;
        }

        // Use specialized component for change_dir tool
        if (content.ToolName === "change_dir") {
          return <ChangeDirTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for keyword search tool
        if (content.ToolName === "keyword_search") {
          return <KeywordSearchTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for browser navigate tool
        if (content.ToolName === "browser_navigate") {
          return <BrowserNavigateTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for browser eval tool
        if (content.ToolName === "browser_eval") {
          return <BrowserEvalTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for read image tool
        if (content.ToolName === "read_image") {
          return <ReadImageTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for browser resize tool
        if (content.ToolName === "browser_resize") {
          return <BrowserResizeTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for subagent tool
        if (content.ToolName === "subagent") {
          return <SubagentTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for output iframe tool
        if (content.ToolName === "output_iframe") {
          return <OutputIframeTool toolInput={content.ToolInput} isRunning={true} />;
        }
        // Use specialized component for browser console logs tools
        if (
          content.ToolName === "browser_recent_console_logs" ||
          content.ToolName === "browser_clear_console_logs"
        ) {
          return (
            <BrowserConsoleLogsTool
              toolName={content.ToolName}
              toolInput={content.ToolInput}
              isRunning={true}
            />
          );
        }
        // Default rendering for other tools using GenericTool
        return (
          <GenericTool
            toolName={content.ToolName || "Unknown Tool"}
            toolInput={content.ToolInput}
            isRunning={true}
          />
        );
      case "tool_result": {
        const hasError = content.ToolError;
        const toolUseId = content.ToolUseID;
        const startTime = content.ToolUseStartTime;
        const endTime = content.ToolUseEndTime;

        // Calculate execution time if available
        let executionTime = "";
        if (startTime && endTime) {
          const start = new Date(startTime).getTime();
          const end = new Date(endTime).getTime();
          const diffMs = end - start;
          if (diffMs < 1000) {
            executionTime = `${diffMs}ms`;
          } else {
            executionTime = `${(diffMs / 1000).toFixed(1)}s`;
          }
        }

        // Get a short summary of the tool result for mobile-friendly display
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

        // unused for now
        void getToolResultSummary;

        // Get tool information from the toolUseMap or fallback to content
        const toolInfo = toolUseId && toolUseMap && toolUseMap[toolUseId];
        const toolName =
          (toolInfo && typeof toolInfo === "object" && toolInfo.name) ||
          content.ToolName ||
          "Unknown Tool";
        const toolInput = toolInfo && typeof toolInfo === "object" ? toolInfo.input : undefined;

        // Use specialized component for bash tool
        if (toolName === "bash") {
          return (
            <BashTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for patch tool
        if (toolName === "patch") {
          return (
            <PatchTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
              display={content.Display}
              onCommentTextChange={onCommentTextChange}
            />
          );
        }

        // Use specialized component for screenshot tool
        if (toolName === "screenshot" || toolName === "browser_take_screenshot") {
          return (
            <ScreenshotTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for change_dir tool
        if (toolName === "change_dir") {
          return (
            <ChangeDirTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for keyword search tool
        if (toolName === "keyword_search") {
          return (
            <KeywordSearchTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for browser navigate tool
        if (toolName === "browser_navigate") {
          return (
            <BrowserNavigateTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for browser eval tool
        if (toolName === "browser_eval") {
          return (
            <BrowserEvalTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for read image tool
        if (toolName === "read_image") {
          return (
            <ReadImageTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for browser resize tool
        if (toolName === "browser_resize") {
          return (
            <BrowserResizeTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Use specialized component for subagent tool
        if (toolName === "subagent") {
          return (
            <SubagentTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
              displayData={content.Display as { slug?: string; conversation_id?: string }}
            />
          );
        }

        // Use specialized component for output iframe tool
        if (toolName === "output_iframe") {
          return (
            <OutputIframeTool
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
              display={content.Display}
            />
          );
        }

        // Use specialized component for browser console logs tools
        if (
          toolName === "browser_recent_console_logs" ||
          toolName === "browser_clear_console_logs"
        ) {
          return (
            <BrowserConsoleLogsTool
              toolName={toolName}
              toolInput={toolInput}
              isRunning={false}
              toolResult={content.ToolResult}
              hasError={hasError}
              executionTime={executionTime}
            />
          );
        }

        // Default rendering for other tools using GenericTool
        return (
          <GenericTool
            toolName={toolName}
            toolInput={toolInput}
            isRunning={false}
            toolResult={content.ToolResult}
            hasError={hasError}
            executionTime={executionTime}
          />
        );
      }
      case "redacted_thinking":
        return <div className="text-tertiary italic text-sm">[Thinking content hidden]</div>;
      case "thinking": {
        const thinkingText = content.Thinking || content.Text || "";
        if (!thinkingText) return null;
        return <ThinkingContent thinking={thinkingText} />;
      }
      default: {
        // For unknown content types, show the type and try to display useful content
        const displayText = content.Text || content.Data || "";
        const hasMediaType = content.MediaType;
        const hasOtherData = Object.keys(content).some(
          (key) => key !== "Type" && key !== "ID" && content[key as keyof typeof content],
        );

        return (
          <div
            style={{
              background: "var(--bg-tertiary)",
              border: "1px solid var(--border)",
              borderRadius: "0.25rem",
              padding: "0.75rem",
            }}
          >
            <div
              className="text-xs text-secondary"
              style={{ marginBottom: "0.5rem", fontFamily: "monospace" }}
            >
              Unknown content type: {contentType} (value: {content.Type})
            </div>

            {/* Show media content if available */}
            {hasMediaType && (
              <div style={{ marginBottom: "0.5rem" }}>
                <div className="text-xs text-secondary" style={{ marginBottom: "0.25rem" }}>
                  Media Type: {content.MediaType}
                </div>
                {content.MediaType?.startsWith("image/") && content.Data && (
                  <img
                    src={`data:${content.MediaType};base64,${content.Data}`}
                    alt="Tool output image"
                    className="rounded border"
                    style={{ maxWidth: "100%", height: "auto", maxHeight: "300px" }}
                  />
                )}
              </div>
            )}

            {/* Show text content if available */}
            {displayText && (
              <div className="text-sm whitespace-pre-wrap break-words">{displayText}</div>
            )}

            {/* Show raw JSON for debugging if no text content */}
            {!displayText && hasOtherData && (
              <details className="text-xs">
                <summary className="text-secondary" style={{ cursor: "pointer" }}>
                  Show raw content
                </summary>
                <pre
                  style={{
                    marginTop: "0.5rem",
                    padding: "0.5rem",
                    background: "var(--bg-base)",
                    borderRadius: "0.25rem",
                    fontSize: "0.75rem",
                    overflow: "auto",
                  }}
                >
                  {JSON.stringify(content, null, 2)}
                </pre>
              </details>
            )}
          </div>
        );
      }
    }
  };

  // Render display data for tool-specific rendering
  const renderDisplayData = (toolDisplay: ToolDisplay, toolName?: string) => {
    const display = toolDisplay.display;

    // Skip rendering screenshot displays here - they are handled by tool_result rendering
    if (
      display &&
      typeof display === "object" &&
      "type" in display &&
      display.type === "screenshot"
    ) {
      return null;
    }

    // Infer tool type from display content if tool name not provided
    const inferredToolName =
      toolName ||
      (typeof display === "string" && display.includes("---") && display.includes("+++")
        ? "patch"
        : undefined);

    // Render patch tool displays using PatchTool component
    if (inferredToolName === "patch" && typeof display === "string") {
      // Create a mock toolResult with the diff in Text field
      const mockToolResult: LLMContent[] = [
        {
          ID: toolDisplay.tool_use_id,
          Type: 6, // tool_result
          Text: display,
        },
      ];

      return (
        <PatchTool
          toolInput={{}}
          isRunning={false}
          toolResult={mockToolResult}
          hasError={false}
          onCommentTextChange={onCommentTextChange}
        />
      );
    }

    // For other types of display data, use GenericTool component
    const mockToolResult: LLMContent[] = [
      {
        ID: toolDisplay.tool_use_id,
        Type: 6, // tool_result
        Text: JSON.stringify(display, null, 2),
      },
    ];

    return (
      <GenericTool
        toolName={inferredToolName || toolName || "Tool output"}
        toolInput={{}}
        isRunning={false}
        toolResult={mockToolResult}
        hasError={false}
      />
    );
  };

  const getMessageClasses = () => {
    if (isUser) {
      return "message message-user";
    }
    if (isError) {
      return "message message-error";
    }
    if (isTool) {
      return "message message-tool";
    }
    return "message message-agent";
  };

  // Special rendering for error messages
  if (isError) {
    let errorText = "An error occurred";
    if (llmMessage && llmMessage.Content && llmMessage.Content.length > 0) {
      const textContent = llmMessage.Content.find((c) => c.Type === 2);
      if (textContent && textContent.Text) {
        errorText = textContent.Text;
      }
    }
    return (
      <>
        <div
          ref={messageRef}
          className={getMessageClasses()}
          onClick={handleMessageClick}
          onMouseEnter={handleMouseEnter}
          onMouseLeave={handleMouseLeave}
          style={{ position: "relative" }}
          data-testid="message"
          role="alert"
          aria-label="Error message"
        >
          {actionBarVisible && (hasCopyAction || hasUsageAction) && (
            <MessageActionBar
              onCopy={hasCopyAction ? handleCopy : undefined}
              onShowUsage={hasUsageAction ? handleShowUsage : undefined}
            />
          )}
          <div className="message-content" data-testid="message-content">
            <div className="whitespace-pre-wrap break-words">{errorText}</div>
          </div>
        </div>
        {showUsageModal && usage && (
          <UsageDetailModal
            usage={usage}
            durationMs={durationMs}
            onClose={() => setShowUsageModal(false)}
          />
        )}
      </>
    );
  }

  // If we have display_data, use that for rendering (more compact, tool-specific)
  if (displayData && displayData.length > 0) {
    return (
      <>
        <div
          ref={messageRef}
          className={getMessageClasses()}
          onClick={handleMessageClick}
          onMouseEnter={handleMouseEnter}
          onMouseLeave={handleMouseLeave}
          style={{ position: "relative" }}
          data-testid="message"
          role="article"
        >
          {actionBarVisible && (hasCopyAction || hasUsageAction) && (
            <MessageActionBar
              onCopy={hasCopyAction ? handleCopy : undefined}
              onShowUsage={hasUsageAction ? handleShowUsage : undefined}
            />
          )}
          <div className="message-content" data-testid="message-content">
            {displayData.map((toolDisplay, index) => (
              <div key={index}>{renderDisplayData(toolDisplay, toolDisplay.tool_name)}</div>
            ))}
          </div>
        </div>
        {showUsageModal && usage && (
          <UsageDetailModal
            usage={usage}
            durationMs={durationMs}
            onClose={() => setShowUsageModal(false)}
          />
        )}
      </>
    );
  }

  // Don't render messages with no meaningful content
  if (!llmMessage || !llmMessage.Content || llmMessage.Content.length === 0) {
    return null;
  }

  // Filter out redacted thinking, empty content, tool_use, and tool_result
  // Keep thinking content (3) for display
  const meaningfulContent =
    llmMessage?.Content?.filter((c) => {
      const contentType = c.Type;
      // Filter out redacted thinking (4), tool_use (5), tool_result (6), and empty text content
      // Keep thinking (3) if it has content
      if (contentType === 3) {
        return !!(c.Thinking || c.Text);
      }
      return (
        contentType !== 4 &&
        contentType !== 5 &&
        contentType !== 6 &&
        (c.Text?.trim() || contentType !== 2)
      ); // 4 = redacted_thinking, 5 = tool_use, 6 = tool_result, 2 = text
    }) || [];

  // Don't filter out messages that contain operation status like "[Operation cancelled]"
  const hasOperationStatus = llmMessage?.Content?.some(
    (c) => c.Type === 2 && c.Text?.includes("[Operation"),
  );

  if (meaningfulContent.length === 0 && !hasOperationStatus) {
    return null;
  }

  // If we have operation status but no meaningful content, render the status
  const contentToRender =
    meaningfulContent.length > 0
      ? meaningfulContent
      : llmMessage?.Content?.filter((c) => c.Type === 2 && c.Text?.includes("[Operation")) || [];

  return (
    <>
      <div
        ref={messageRef}
        className={getMessageClasses()}
        onClick={handleMessageClick}
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
        style={{ position: "relative" }}
        data-testid="message"
        role="article"
      >
        {actionBarVisible && (hasCopyAction || hasUsageAction) && (
          <MessageActionBar
            onCopy={hasCopyAction ? handleCopy : undefined}
            onShowUsage={hasUsageAction ? handleShowUsage : undefined}
          />
        )}
        {/* Message content */}
        <div className="message-content" data-testid="message-content">
          {contentToRender.map((content, index) => (
            <div key={index}>{renderContent(content)}</div>
          ))}
        </div>
      </div>
      {showUsageModal && usage && (
        <UsageDetailModal
          usage={usage}
          durationMs={durationMs}
          onClose={() => setShowUsageModal(false)}
        />
      )}
    </>
  );
}

// Helper functions
function hasToolResult(llmMessage: LLMMessage | null): boolean {
  if (!llmMessage) return false;
  return llmMessage.Content?.some((c) => c.Type === 6) ?? false; // 6 = tool_result
}

function hasToolContent(llmMessage: LLMMessage | null): boolean {
  if (!llmMessage) return false;
  return llmMessage.Content?.some((c) => c.Type === 5 || c.Type === 6) ?? false; // 5 = tool_use, 6 = tool_result
}

export default Message;
