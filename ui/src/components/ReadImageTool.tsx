import React, { useState } from "react";
import { LLMContent } from "../types";

interface ReadImageToolProps {
  toolInput?: unknown; // { path: string }
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function ReadImageTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: ReadImageToolProps) {
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

  const filename = getPath(toolInput) || getId(toolInput) || "image";

  // Build image URL from the base64 data in toolResult
  // The read_image tool returns [text description, image content with Data and MediaType]
  let imageUrl: string | undefined = undefined;
  if (toolResult && toolResult.length >= 2) {
    const imageContent = toolResult[1];
    if (imageContent?.Data && imageContent?.MediaType) {
      imageUrl = `data:${imageContent.MediaType};base64,${imageContent.Data}`;
    }
  }

  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <div
      className="screenshot-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="screenshot-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="screenshot-tool-summary">
          <span className={`screenshot-tool-emoji ${isRunning ? "running" : ""}`}>🖼️</span>
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
                  <span>Image:</span>
                  <span className="screenshot-tool-time">{executionTime}</span>
                </div>
              )}
              <div className="screenshot-tool-image-container">
                <a href={imageUrl} target="_blank" rel="noopener noreferrer">
                  <img
                    src={imageUrl}
                    alt={`Image: ${filename}`}
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
                {toolResult && toolResult[0]?.Text ? toolResult[0].Text : "Image read failed"}
              </pre>
            </div>
          )}

          {isRunning && (
            <div className="screenshot-tool-section">
              <div className="screenshot-tool-label">Reading image...</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default ReadImageTool;
