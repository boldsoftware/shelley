import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserEvalToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { expression: string }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserEvalTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserEvalToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract expression from toolInput
  const expression =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "expression" in toolInput &&
    typeof (toolInput as { expression?: unknown }).expression === "string"
      ? (toolInput as { expression: string }).expression
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Extract result from toolResult
  const result =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Truncate expression for display
  const truncateText = (text: string, maxLen: number = 300) => {
    if (text.length <= maxLen) return text;
    return text.substring(0, maxLen) + "...";
  };

  const displayExpression = truncateText(expression);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>⚡</span>
          <span className="tool-command">{displayExpression}</span>
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
            <div className="tool-label">Expression:</div>
            <pre className="tool-code">{expression}</pre>
          </div>

          {isComplete && (
            <div className="tool-section">
              <div className="tool-label">
                Result{hasError ? " (Error)" : ""}:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <pre className={`tool-code ${hasError ? "error" : ""}`}>
                {result || "(no result)"}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BrowserEvalTool;
