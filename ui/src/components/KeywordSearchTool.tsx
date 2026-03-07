import React, { useState } from "react";
import { LLMContent } from "../types";

interface KeywordSearchToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { query: string, search_terms: string[] }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function KeywordSearchTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: KeywordSearchToolProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract query and search terms from toolInput
  const query =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "query" in toolInput &&
    typeof toolInput.query === "string"
      ? toolInput.query
      : "";

  const searchTerms =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "search_terms" in toolInput &&
    Array.isArray(toolInput.search_terms)
      ? toolInput.search_terms
      : [];

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Truncate search terms for display
  const truncateSearchTerms = (terms: string[], maxLen: number = 300) => {
    const joined = terms.join(", ");
    if (joined.length <= maxLen) return joined;
    return joined.substring(0, maxLen) + "...";
  };

  const displayText = query || truncateSearchTerms(searchTerms);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>🔍</span>
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
          {query && (
            <div className="tool-section">
              <div className="tool-label">Query:</div>
              <pre className="tool-code">{query}</pre>
            </div>
          )}

          {searchTerms.length > 0 && (
            <div className="tool-section">
              <div className="tool-label">Search Terms:</div>
              <pre className="tool-code">{searchTerms.join(", ")}</pre>
            </div>
          )}

          {isComplete && (
            <div className="tool-section">
              <div className="tool-label">
                Results{hasError ? " (Error)" : ""}:
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

export default KeywordSearchTool;
