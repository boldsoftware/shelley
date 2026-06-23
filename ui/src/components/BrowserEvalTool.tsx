import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

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
    <ToolCard
      emoji="⚡"
      running={isRunning}
      complete={isComplete}
      title={<span title={expression}>{displayExpression}</span>}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Expression:">
        <ToolCode>{expression}</ToolCode>
      </ToolSection>

      {isComplete && (
        <ToolSection
          label={
            <span className="flex items-center gap-2">
              <span>Result{hasError ? " (Error)" : ""}:</span>
              {executionTime && (
                <span className="text-muted-foreground">{executionTime}</span>
              )}
            </span>
          }
        >
          <ToolCode error={hasError}>{result || "(no result)"}</ToolCode>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default BrowserEvalTool;
