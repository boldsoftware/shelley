import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserConsoleLogsToolProps {
  toolName: string; // to distinguish between recent and clear
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserConsoleLogsTool({
  toolName,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserConsoleLogsToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Determine display text based on tool name and state
  const getDisplayText = () => {
    if (isRunning) {
      return toolName === "browser_console_clear_logs"
        ? "clearing console..."
        : "fetching console logs...";
    }
    return toolName === "browser_console_clear_logs" ? "clear console" : "console logs";
  };

  const displayText = getDisplayText();
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>📋</span>
          <span className="tool-command">{displayText}</span>
          {isComplete && hasError && <span className="tool-error">✗</span>}
          {isComplete && !hasError && <span className="tool-success">✓</span>}
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
            className={`tool-chevron${isExpanded ? " tool-chevron-expanded" : ""}`}
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
          {isComplete && (
            <div className="tool-section">
              <div className="tool-label">
                Output{hasError ? " (Error)" : ""}:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <pre className={`tool-code ${hasError ? "error" : ""}`}>
                {output || "(no output)"}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BrowserConsoleLogsTool;
