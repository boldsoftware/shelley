import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

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
    <ToolCard
      emoji="⚙️"
      running={isRunning}
      complete={isComplete}
      title={toolName}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      {toolInput !== undefined && (
        <ToolSection label="Input:">
          <ToolCode>{formatData(toolInput)}</ToolCode>
        </ToolSection>
      )}

      {isRunning && (
        <ToolSection label="Status:">
          <div className="text-muted-foreground italic">running...</div>
        </ToolSection>
      )}

      {isComplete && (
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
      )}
    </ToolCard>
  );
}

export default GenericTool;
