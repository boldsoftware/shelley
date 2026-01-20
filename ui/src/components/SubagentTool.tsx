import React, { useState } from "react";
import { LLMContent } from "../types";

interface SubagentToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { slug: string, prompt: string, timeout_seconds?: number, wait?: boolean }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  displayData?: { slug?: string; conversation_id?: string };
}

function SubagentTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  displayData,
}: SubagentToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract fields from toolInput
  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as { slug?: string; prompt?: string; timeout_seconds?: number; wait?: boolean })
      : {};

  const slug = input.slug || displayData?.slug || "subagent";
  const prompt = input.prompt || "";
  const wait = input.wait !== false;
  const timeout = input.timeout_seconds || 60;

  // Extract result text
  const resultText =
    toolResult
      ?.filter((r) => r.Type === 2) // ContentTypeText
      .map((r) => r.Text)
      .join("\n") || "";

  // Truncate prompt for display
  const truncateText = (text: string, maxLen: number = 60) => {
    if (!text) return "";
    const firstLine = text.split("\n")[0];
    if (firstLine.length <= maxLen) return firstLine;
    return firstLine.substring(0, maxLen) + "...";
  };

  const displayPrompt = truncateText(prompt);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>⚡</span>
          <span className="tool-name">subagent</span>
          {isComplete && hasError && <span className="tool-error">✗</span>}
          {isComplete && !hasError && <span className="tool-success">✓</span>}
          <span className="tool-command">
            Subagent '{slug}' {isRunning ? (wait ? "running..." : "started") : ""}
            {displayPrompt && !isRunning && ` ${displayPrompt}`}
          </span>
        </div>
        <button
          className="tool-toggle"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            style={{
              transform: isExpanded ? "rotate(90deg)" : "rotate(0deg)",
              transition: "transform 0.2s",
            }}
          >
            <path
              d="M4.5 3L7.5 6L4.5 9"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </button>
      </div>

      {isExpanded && (
        <div className="tool-details">
          <div className="tool-section">
            <div className="tool-label">
              Prompt to '{slug}':
              {!wait && <span className="tool-badge">fire-and-forget</span>}
              {timeout !== 60 && <span className="tool-badge">timeout: {timeout}s</span>}
            </div>
            <div className="tool-code">{prompt || "(no prompt)"}</div>
          </div>

          {isComplete && (
            <div className="tool-section">
              <div className="tool-label">
                Response:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <div className={`tool-code ${hasError ? "error" : ""}`}>
                {resultText || "(no response)"}
              </div>
            </div>
          )}

          {displayData?.conversation_id && (
            <div className="tool-section">
              <div className="tool-label">Conversation:</div>
              <div className="tool-code">
                <a
                  href={`/c/${slug}`}
                  onClick={(e) => {
                    e.preventDefault();
                    // Navigate to the subagent conversation
                    window.history.pushState({}, "", `/c/${slug}`);
                    window.dispatchEvent(new PopStateEvent("popstate"));
                  }}
                  style={{ color: "var(--link-color)", textDecoration: "underline" }}
                >
                  View subagent conversation →
                </a>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default SubagentTool;
