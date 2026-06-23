import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

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
    <ToolCard
      emoji="🌐"
      running={isRunning}
      complete={isComplete}
      title={<span title={url}>{displayUrl}</span>}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="URL:">
        <ToolCode>
          <a
            href={url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary hover:underline"
          >
            {url}
          </a>
        </ToolCode>
      </ToolSection>

      {isComplete && output && (
        <ToolSection
          label={
            <span className="flex items-center gap-2">
              <span>Output{hasError ? " (Error)" : ""}:</span>
              {executionTime && (
                <span className="text-muted-foreground">{executionTime}</span>
              )}
            </span>
          }
        >
          <ToolCode error={hasError}>{output}</ToolCode>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default BrowserNavigateTool;
