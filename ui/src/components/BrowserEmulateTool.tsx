import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface BrowserEmulateToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserEmulateTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserEmulateToolProps) {
  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as {
          action?: string;
          device?: string;
          width?: number;
          height?: number;
          mobile?: boolean;
          touch?: boolean;
          device_scale_factor?: number;
          enabled?: boolean;
          media?: string;
        })
      : {};

  const action = input.action || "";
  const device = input.device || "";

  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  // Build compact summary
  const summaryParts: string[] = [action];
  if (device) summaryParts.push(device);
  if (input.width && input.height) summaryParts.push(`${input.width}×${input.height}`);
  if (input.media) summaryParts.push(input.media);
  if (input.enabled !== undefined) summaryParts.push(input.enabled ? "on" : "off");
  const summary = summaryParts.filter(Boolean).join(" ") || "emulate";

  return (
    <ToolCard
      emoji="📱"
      running={isRunning}
      complete={isComplete}
      title={summary}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Action:">
        <ToolCode>{action || "(none)"}</ToolCode>
      </ToolSection>

      {device && (
        <ToolSection label="Device:">
          <ToolCode>{device}</ToolCode>
        </ToolSection>
      )}

      {input.width !== undefined && input.height !== undefined && (
        <ToolSection label="Dimensions:">
          <ToolCode>
            {input.width} × {input.height}
          </ToolCode>
        </ToolSection>
      )}

      {input.media && (
        <ToolSection label="Media:">
          <ToolCode>{input.media}</ToolCode>
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

export default BrowserEmulateTool;
