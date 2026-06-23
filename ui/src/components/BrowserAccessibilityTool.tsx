import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

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
    <ToolCard
      emoji="♿"
      running={isRunning}
      complete={isComplete}
      title={summary}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Action:">
        <ToolCode>{action || "(none)"}</ToolCode>
      </ToolSection>

      {input.name && (
        <ToolSection label="Name:">
          <ToolCode>{input.name}</ToolCode>
        </ToolSection>
      )}

      {input.role && (
        <ToolSection label="Role:">
          <ToolCode>{input.role}</ToolCode>
        </ToolSection>
      )}

      {input.selector && (
        <ToolSection label="Selector:">
          <ToolCode>{input.selector}</ToolCode>
        </ToolSection>
      )}

      {input.depth !== undefined && (
        <ToolSection label="Depth:">
          <ToolCode>{input.depth}</ToolCode>
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

export default BrowserAccessibilityTool;
