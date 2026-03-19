import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserNavigateToolProps {
  toolInput?: unknown; // { url: string }
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserNavigateTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserNavigateToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract URL from toolInput
  const url =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "url" in toolInput &&
    typeof toolInput.url === "string"
      ? toolInput.url
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Truncate URL for display
  const truncateUrl = (urlStr: string, maxLen: number = 300) => {
    if (urlStr.length <= maxLen) return urlStr;
    return urlStr.substring(0, maxLen) + "...";
  };

  const displayUrl = truncateUrl(url);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>🌐</span>
          <span className="tool-command" title={url}>
            {displayUrl}
          </span>
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
          <div className="tool-section">
            <div className="tool-label">URL:</div>
            <div className="tool-code">
              <a href={url} target="_blank" rel="noopener noreferrer">
                {url}
              </a>
            </div>
          </div>

          {isComplete && output && (
            <div className="tool-section">
              <div className="tool-label">
                Output{hasError ? " (Error)" : ""}:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <pre className={`tool-code ${hasError ? "error" : ""}`}>{output}</pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BrowserNavigateTool;
