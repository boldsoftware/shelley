import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface BrowserConsoleLogsToolProps {
  toolName: string; // to distinguish between recent and clear
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserConsoleLogsTool({
  toolName,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserConsoleLogsToolProps) {
  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Determine display text based on tool name and state
  const getDisplayText = () => {
    if (isRunning) {
      return toolName === "browser_console_clear_logs"
        ? "clearing console..."
        : "fetching console logs...";
    }
    return toolName === "browser_console_clear_logs" ? "clear console" : "console logs";
  };

  const displayText = getDisplayText();
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <ToolCard
      emoji="📋"
      running={isRunning}
      complete={isComplete}
      title={displayText}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      {isComplete ? (
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
          <ToolCode error={hasError}>{output || "(no output)"}</ToolCode>
        </ToolSection>
      ) : undefined}
    </ToolCard>
  );
}

export default BrowserConsoleLogsTool;
