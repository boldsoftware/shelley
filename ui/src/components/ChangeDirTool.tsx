import React, { useState } from "react";
import { LLMContent } from "../types";

interface ChangeDirToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { path: string }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function ChangeDirTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: ChangeDirToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract path from toolInput
  const path =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "path" in toolInput &&
    typeof (toolInput as { path: unknown }).path === "string"
      ? (toolInput as { path: string }).path
      : "";

  // Get result text
  const resultText =
    toolResult
      ?.map((r) => r.Text)
      .filter(Boolean)
      .join("") || "";

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>📂</span>
          <span className="tool-command">cd {path || "..."}</span>
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
            <div className="tool-label">
              Path:
              {executionTime && <span className="tool-time">{executionTime}</span>}
            </div>
            <div className={`tool-code ${hasError ? "error" : ""}`}>{path || "(no path)"}</div>
          </div>
          {isComplete && (
            <div className="tool-section">
              <div className="tool-label">Result:</div>
              <div className={`tool-code ${hasError ? "error" : ""}`}>
                {resultText || "(no output)"}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default ChangeDirTool;
