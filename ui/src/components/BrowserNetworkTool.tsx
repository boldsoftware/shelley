import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserNetworkToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserNetworkTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserNetworkToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as { action?: string; filter?: string; limit?: number })
      : {};

  const action = input.action || "";

  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  const summaryParts: string[] = [action];
  if (input.filter) summaryParts.push(`filter: ${input.filter}`);
  const summary = summaryParts.filter(Boolean).join(" ") || "network";

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>📡</span>
          <span className="tool-command">{summary}</span>
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
            <div className="tool-label">Action:</div>
            <pre className="tool-code">{action || "(none)"}</pre>
          </div>

          {input.filter && (
            <div className="tool-section">
              <div className="tool-label">Filter:</div>
              <pre className="tool-code">{input.filter}</pre>
            </div>
          )}

          {input.limit !== undefined && (
            <div className="tool-section">
              <div className="tool-label">Limit:</div>
              <pre className="tool-code">{input.limit}</pre>
            </div>
          )}

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

export default BrowserNetworkTool;
