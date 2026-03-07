import React, { useState } from "react";
import { LLMContent } from "../types";

interface ScreenshotToolProps {
  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown; // Display data from the tool_result Content
}

function ScreenshotTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  display,
}: ScreenshotToolProps) {
  const [isExpanded, setIsExpanded] = useState(true); // Default to expanded

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

  // Use display data passed as prop (from tool_result Content.Display)
  const displayData = display;

  // Construct image URL
  let imageUrl: string | undefined = undefined;
  if (displayData && typeof displayData === "object" && displayData !== null) {
    const url =
      "url" in displayData && typeof displayData.url === "string" ? displayData.url : undefined;
    const path =
      "path" in displayData && typeof displayData.path === "string" ? displayData.path : undefined;
    const id =
      "id" in displayData && typeof displayData.id === "string" ? displayData.id : undefined;

    imageUrl =
      url ||
      (path
        ? `/api/read?path=${encodeURIComponent(path)}`
        : id
          ? `/api/read?path=${encodeURIComponent(id)}`
          : undefined);
  }

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div
      className="screenshot-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="screenshot-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="screenshot-tool-summary">
          <span className={`screenshot-tool-emoji ${isRunning ? "running" : ""}`}>📷</span>
          <span className="screenshot-tool-filename">{filename}</span>
          {isComplete && hasError && <span className="screenshot-tool-error">✗</span>}
          {isComplete && !hasError && <span className="screenshot-tool-success">✓</span>}
        </div>
        <button
          className="screenshot-tool-toggle"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            className={`tool-chevron${isExpanded ? " tool-chevron-expanded" : ""}`}
          >
            <path
              d="M4.5 3L7.5 6L4.5 9"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </button>
      </div>

      {isExpanded && (
        <div className="screenshot-tool-details">
          {isComplete && !hasError && imageUrl && (
            <div className="screenshot-tool-section">
              {executionTime && (
                <div className="screenshot-tool-label">
                  <span>Screenshot:</span>
                  <span className="screenshot-tool-time">{executionTime}</span>
                </div>
              )}
              <div className="screenshot-tool-image-container">
                <a href={imageUrl} target="_blank" rel="noopener noreferrer">
                  <img
                    src={imageUrl}
                    alt={`Screenshot: ${filename}`}
                    className="tool-image-responsive"
                  />
                </a>
              </div>
            </div>
          )}

          {isComplete && hasError && (
            <div className="screenshot-tool-section">
              <div className="screenshot-tool-label">
                <span>Error:</span>
                {executionTime && <span className="screenshot-tool-time">{executionTime}</span>}
              </div>
              <pre className="screenshot-tool-error-message">
                {toolResult && toolResult[0]?.Text
                  ? toolResult[0].Text
                  : "Screenshot capture failed"}
              </pre>
            </div>
          )}

          {isRunning && (
            <div className="screenshot-tool-section">
              <div className="screenshot-tool-label">Capturing screenshot...</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default ScreenshotTool;
