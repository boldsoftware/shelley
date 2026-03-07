import React, { useState } from "react";
import { LLMContent } from "../types";

interface BrowserAccessibilityToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserAccessibilityTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserAccessibilityToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as {
          action?: string;
          depth?: number;
          name?: string;
          role?: string;
          selector?: string;
        })
      : {};

  const action = input.action || "";

  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  // Build compact summary showing action and query params
  const summaryParts: string[] = [action];
  if (input.name) summaryParts.push(`name="${input.name}"`);
  if (input.role) summaryParts.push(`role=${input.role}`);
  if (input.selector) summaryParts.push(input.selector);
  if (input.depth !== undefined) summaryParts.push(`depth=${input.depth}`);
  const summary = summaryParts.filter(Boolean).join(" ") || "accessibility";

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>♿</span>
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

          {input.name && (
            <div className="tool-section">
              <div className="tool-label">Name:</div>
              <pre className="tool-code">{input.name}</pre>
            </div>
          )}

          {input.role && (
            <div className="tool-section">
              <div className="tool-label">Role:</div>
              <pre className="tool-code">{input.role}</pre>
            </div>
          )}

          {input.selector && (
            <div className="tool-section">
              <div className="tool-label">Selector:</div>
              <pre className="tool-code">{input.selector}</pre>
            </div>
          )}

          {input.depth !== undefined && (
            <div className="tool-section">
              <div className="tool-label">Depth:</div>
              <pre className="tool-code">{input.depth}</pre>
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

export default BrowserAccessibilityTool;
