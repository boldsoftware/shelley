import React, { useState } from "react";
import { LLMContent } from "../types";

interface GenericToolProps {
  toolName: string;

  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function GenericTool({
  toolName,
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: GenericToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Format data for display
  const formatData = (data: unknown): string => {
    if (data === undefined || data === null) return "";
    if (typeof data === "string") return data;
    try {
      return JSON.stringify(data, null, 2);
    } catch {
      return String(data);
    }
  };

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0
      ? toolResult.map((result) => result.Text || formatData(result)).join("\n")
      : "";

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>⚙️</span>
          <span className="tool-command">{toolName}</span>
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
          {toolInput !== undefined && (
            <div className="tool-section">
              <div className="tool-label">Input:</div>
              <pre className="tool-code">{formatData(toolInput)}</pre>
            </div>
          )}

          {isRunning && (
            <div className="tool-section">
              <div className="tool-label">Status:</div>
              <div className="tool-running-text">running...</div>
            </div>
          )}

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

export default GenericTool;
