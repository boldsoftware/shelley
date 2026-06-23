import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface ReadContextFileToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { path: string }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function ReadContextFileTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: ReadContextFileToolProps) {
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
    <ToolCard
      emoji="📋"
      running={isRunning}
      complete={isComplete}
      title={
        <>
          read context: {path || "..."}
        </>
      }
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection
        label={
          <span className="flex items-center gap-2">
            <span>Path:</span>
            {executionTime && (
              <span className="text-muted-foreground">{executionTime}</span>
            )}
          </span>
        }
      >
        <ToolCode error={hasError}>{path || "(no path)"}</ToolCode>
      </ToolSection>
      {isComplete && (
        <ToolSection label="Content:">
          <ToolCode error={hasError}>{resultText || "(no output)"}</ToolCode>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default ReadContextFileTool;
