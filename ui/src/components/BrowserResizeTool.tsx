import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserResizeToolProps {
  toolInput?: unknown; // { width: number, height: number }
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserResizeTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserResizeToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract dimensions from toolInput
  const width =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "width" in toolInput &&
    typeof (toolInput as { width: unknown }).width === "number"
      ? (toolInput as { width: number }).width
      : 0;

  const height =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "height" in toolInput &&
    typeof (toolInput as { height: unknown }).height === "number"
      ? (toolInput as { height: number }).height
      : 0;

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;
  const displaySize = width > 0 && height > 0 ? `${width}×${height}` : "...";

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>📐</span>
          <span className="tool-command">resize {displaySize}</span>
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
            <div className="tool-label">Dimensions:</div>
            <div className="tool-code">
              {width} × {height} pixels
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

export default BrowserResizeTool;
