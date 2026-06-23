import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

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
    <ToolCard
      emoji="📡"
      running={isRunning}
      complete={isComplete}
      title={summary}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Action:">
        <ToolCode>{action || "(none)"}</ToolCode>
      </ToolSection>

      {input.filter && (
        <ToolSection label="Filter:">
          <ToolCode>{input.filter}</ToolCode>
        </ToolSection>
      )}

      {input.limit !== undefined && (
        <ToolSection label="Limit:">
          <ToolCode>{input.limit}</ToolCode>
        </ToolSection>
      )}

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

export default BrowserNetworkTool;
