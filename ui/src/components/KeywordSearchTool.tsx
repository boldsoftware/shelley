import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

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

  const fullText = query || searchTerms.join(", ");
  const displayText = query || truncateSearchTerms(searchTerms);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <ToolCard
      emoji="🔍"
      running={isRunning}
      complete={isComplete}
      title={<span title={fullText}>{displayText}</span>}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      {query && (
        <ToolSection label="Query:">
          <ToolCode>{query}</ToolCode>
        </ToolSection>
      )}

      {searchTerms.length > 0 && (
        <ToolSection label="Search Terms:">
          <ToolCode>{searchTerms.join(", ")}</ToolCode>
        </ToolSection>
      )}

      {isComplete && (
        <ToolSection
          label={
            <span className="flex items-center gap-2">
              <span>Results{hasError ? " (Error)" : ""}:</span>
              {executionTime && (
                <span className="text-muted-foreground">{executionTime}</span>
              )}
            </span>
          }
        >
          <ToolCode error={hasError}>{output || "(no output)"}</ToolCode>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default KeywordSearchTool;
