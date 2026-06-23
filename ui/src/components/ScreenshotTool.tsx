import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface ScreenshotToolProps {
  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function ScreenshotTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: ScreenshotToolProps) {
  // Extract display info from toolInput
  const getPath = (input: unknown): string | undefined => {
    if (
      typeof input === "object" &&
      input !== null &&
      "path" in input &&
      typeof input.path === "string"
    ) {
      return input.path;
    }
    return undefined;
  };

  const getId = (input: unknown): string | undefined => {
    if (
      typeof input === "object" &&
      input !== null &&
      "id" in input &&
      typeof input.id === "string"
    ) {
      return input.id;
    }
    return undefined;
  };

  const getSelector = (input: unknown): string | undefined => {
    if (
      typeof input === "object" &&
      input !== null &&
      "selector" in input &&
      typeof input.selector === "string"
    ) {
      return input.selector;
    }
    return undefined;
  };

  const filename = getPath(toolInput) || getId(toolInput) || getSelector(toolInput) || "screenshot";

  // Construct image URL from the tool result's image content.
  // The server replaces inline base64 data with a URL to /api/message/{id}/image/...
  const imageContent = toolResult && toolResult.length >= 2 ? toolResult[1] : undefined;
  const imageUrl = imageContent?.DisplayImageURL;
  const imageWidth = imageContent?.DisplayWidth;
  const imageHeight = imageContent?.DisplayHeight;

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <ToolCard
      className="screenshot-tool"
      emojiClassName="screenshot-tool-emoji"
      emoji="📷"
      running={isRunning}
      complete={isComplete}
      title={<span title={filename}>{filename}</span>}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      {isComplete && !hasError && imageUrl ? (
        <ToolSection
          label={
            executionTime ? (
              <span className="flex items-center gap-2">
                <span>Screenshot:</span>
                <span className="text-muted-foreground">{executionTime}</span>
              </span>
            ) : undefined
          }
        >
          <a href={imageUrl} target="_blank" rel="noopener noreferrer">
            <img
              src={imageUrl}
              alt={`Screenshot: ${filename}`}
              className="max-w-full rounded-md border border-border"
              width={imageWidth || undefined}
              height={imageHeight || undefined}
            />
          </a>
        </ToolSection>
      ) : isComplete && hasError ? (
        <ToolSection
          label={
            <span className="flex items-center gap-2">
              <span>Error:</span>
              {executionTime && (
                <span className="text-muted-foreground">{executionTime}</span>
              )}
            </span>
          }
        >
          <ToolCode error>
            {toolResult && toolResult[0]?.Text
              ? toolResult[0].Text
              : "Screenshot capture failed"}
          </ToolCode>
        </ToolSection>
      ) : isRunning ? (
        <ToolSection>
          <div className="text-muted-foreground italic">Capturing screenshot...</div>
        </ToolSection>
      ) : undefined}
    </ToolCard>
  );
}

export default ScreenshotTool;
