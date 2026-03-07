import React, { useState } from "react";
import { LLMContent } from "../types";

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
  const [isExpanded, setIsExpanded] = useState(false);

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
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>📱</span>
          <span className="tool-command">{summary}</span>
          {isComplete && hasError && <span className="tool-error">✗</span>}
          {isComplete && !hasError && <span className="tool-success">✓</span>}
        </div>
        <button
          className="tool-toggle"
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
        <div className="tool-details">
          <div className="tool-section">
            <div className="tool-label">Action:</div>
            <pre className="tool-code">{action || "(none)"}</pre>
          </div>

          {device && (
            <div className="tool-section">
              <div className="tool-label">Device:</div>
              <pre className="tool-code">{device}</pre>
            </div>
          )}

          {input.width !== undefined && input.height !== undefined && (
            <div className="tool-section">
              <div className="tool-label">Dimensions:</div>
              <pre className="tool-code">
                {input.width} × {input.height}
              </pre>
            </div>
          )}

          {input.media && (
            <div className="tool-section">
              <div className="tool-label">Media:</div>
              <pre className="tool-code">{input.media}</pre>
            </div>
          )}

          {isComplete && output && (
            <div className="tool-section">
              <div className="tool-label">
                Output{hasError ? " (Error)" : ""}:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <pre className={`tool-code ${hasError ? "error" : ""}`}>{output}</pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BrowserEmulateTool;
