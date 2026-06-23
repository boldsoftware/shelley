import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface BrowserScreencastToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}

function getInputField(input: unknown, field: string): string | undefined {
  if (typeof input === "object" && input !== null && field in input) {
    const val = (input as Record<string, unknown>)[field];
    return typeof val === "string" ? val : undefined;
  }
  return undefined;
}

function getAction(input: unknown): string {
  return getInputField(input, "action") || "screencast";
}

function BrowserScreencastTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  display,
}: BrowserScreencastToolProps) {
  const action = getAction(toolInput);

  // Determine emoji and label based on action
  let emoji = "🎬";
  let label = "screencast";
  switch (action) {
    case "screencast_start":
      emoji = "🔴";
      label = "recording";
      break;
    case "screencast_stop":
      emoji = "🎬";
      label = "screencast";
      break;
    case "screencast_status":
      emoji = "📊";
      label = "screencast status";
      break;
  }

  // Extract output text from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Extract video URL from display data
  let videoUrl: string | undefined;
  if (display && typeof display === "object" && display !== null) {
    const d = display as Record<string, unknown>;
    if (d.type === "screencast") {
      if (typeof d.url === "string") {
        videoUrl = d.url;
      } else if (typeof d.path === "string") {
        videoUrl = `/api/read?path=${encodeURIComponent(d.path as string)}`;
      }
    }
  }

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <ToolCard
      className="screencast-tool"
      emoji={emoji}
      running={isRunning}
      complete={isComplete}
      title={label}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      {isRunning && (
        <ToolSection label="Status:">
          <div className="text-muted-foreground italic">
            {action === "screencast_start" && "Starting screencast recording..."}
            {action === "screencast_stop" && "Stopping screencast..."}
            {action === "screencast_status" && "Checking screencast status..."}
          </div>
        </ToolSection>
      )}

      {isComplete && !hasError && videoUrl && (
        <ToolSection
          label={
            executionTime ? (
              <span className="text-muted-foreground">{executionTime}</span>
            ) : undefined
          }
        >
          <div className="overflow-hidden rounded-md bg-muted">
            <video controls preload="metadata" className="block w-full">
              <source src={videoUrl} type="video/mp4" />
              Your browser does not support the video tag.
            </video>
          </div>
        </ToolSection>
      )}

      {isComplete && !hasError && !videoUrl && output && (
        <ToolSection
          label={
            executionTime ? (
              <span className="text-muted-foreground">{executionTime}</span>
            ) : undefined
          }
        >
          <ToolCode>{output}</ToolCode>
        </ToolSection>
      )}

      {isComplete && hasError && (
        <ToolSection
          label={
            executionTime ? (
              <span className="text-muted-foreground">{executionTime}</span>
            ) : undefined
          }
        >
          <ToolCode error>{output || "Screencast operation failed"}</ToolCode>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default BrowserScreencastTool;
