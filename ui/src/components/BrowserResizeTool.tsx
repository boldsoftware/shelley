import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface BrowserResizeToolProps {
  toolInput?: unknown; // { width: number, height: number }
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserResizeTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserResizeToolProps) {
  // Extract dimensions from toolInput
  const width =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "width" in toolInput &&
    typeof (toolInput as { width: unknown }).width === "number"
      ? (toolInput as { width: number }).width
      : 0;

  const height =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "height" in toolInput &&
    typeof (toolInput as { height: unknown }).height === "number"
      ? (toolInput as { height: number }).height
      : 0;

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;
  const displaySize = width > 0 && height > 0 ? `${width}×${height}` : "...";

  return (
    <ToolCard
      emoji="📐"
      running={isRunning}
      complete={isComplete}
      title={`resize ${displaySize}`}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Dimensions:">
        <ToolCode>
          {width} × {height} pixels
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

export default BrowserResizeTool;
