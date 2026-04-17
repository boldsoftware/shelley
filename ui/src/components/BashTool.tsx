import React, { useMemo, useState } from "react";
import { LLMContent } from "../types";
import AnsiText from "./AnsiText";

// Display data from the bash tool backend
interface BashDisplayData {
  workingDir: string;
}

interface BashToolProps {
  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;

  // Streaming output from tool progress
  streamingOutput?: string;
}

/** Max lines shown in the streaming preview before "Show more" is needed. */
const PREVIEW_LINES = 5;

function BashTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  display,
  streamingOutput,
}: BashToolProps) {
  // Details panel (command, full output) — collapsed by default, stays collapsed after completion.
  const [isExpanded, setIsExpanded] = useState(false);
  // Streaming preview — expanded to show full streaming output (beyond PREVIEW_LINES).
  const [previewExpanded, setPreviewExpanded] = useState(false);
  const previewRef = React.useRef<HTMLPreElement>(null);
  const expandedStreamRef = React.useRef<HTMLPreElement>(null);

  // Collapse details when tool completes (if we auto-expanded for streaming,
  // the user sees the preview instead, so the details panel should close).
  const prevRunning = React.useRef(isRunning);
  React.useEffect(() => {
    if (prevRunning.current && !isRunning) {
      setIsExpanded(false);
      setPreviewExpanded(false);
    }
    prevRunning.current = isRunning;
  }, [isRunning]);

  // Auto-scroll streaming output to bottom (whichever ref is active).
  React.useEffect(() => {
    const el = previewRef.current ?? expandedStreamRef.current;
    if (el && streamingOutput) {
      el.scrollTop = el.scrollHeight;
    }
  }, [streamingOutput]);

  // Extract working directory from display data
  const displayData: BashDisplayData | null =
    display &&
    typeof display === "object" &&
    "workingDir" in display &&
    typeof display.workingDir === "string"
      ? (display as BashDisplayData)
      : null;

  // Extract command from toolInput
  const command =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "command" in toolInput &&
    typeof toolInput.command === "string"
      ? toolInput.command
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Extract output from toolResult
  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  // Check if this was a cancelled operation
  const isCancelled = hasError && output.includes("Tool execution cancelled by user");

  // Truncate command for display
  const truncateCommand = (cmd: string, maxLen: number = 300) => {
    if (cmd.length <= maxLen) return cmd;
    return cmd.substring(0, maxLen) + "...";
  };

  const displayCommand = truncateCommand(command);
  const isComplete = !isRunning && toolResult !== undefined;

  // Compute streaming preview: show last N lines by default.
  const { visibleStreaming, hasMoreLines, lineCount } = useMemo(() => {
    if (!streamingOutput) return { visibleStreaming: "", hasMoreLines: false, lineCount: 0 };
    const lines = streamingOutput.split("\n");
    return {
      visibleStreaming: previewExpanded ? streamingOutput : lines.slice(-PREVIEW_LINES).join("\n"),
      hasMoreLines: lines.length > PREVIEW_LINES,
      lineCount: lines.length,
    };
  }, [streamingOutput, previewExpanded]);

  return (
    <div
      className="bash-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="bash-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="bash-tool-summary">
          <span className={`bash-tool-emoji ${isRunning ? "running" : ""}`}>🛠️</span>
          <span className="bash-tool-command" title={command}>
            {displayCommand}
          </span>
          {displayData?.workingDir && (
            <span className="bash-tool-cwd" title={displayData.workingDir}>
              in {displayData.workingDir}
            </span>
          )}
          {isComplete && isCancelled && <span className="bash-tool-cancelled">✗ cancelled</span>}
          {isComplete && hasError && !isCancelled && <span className="bash-tool-error">✗</span>}
          {isComplete && !hasError && <span className="bash-tool-success">✓</span>}
        </div>
        <button
          className="bash-tool-toggle"
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

      {/* Streaming preview — shown below header while running, outside the details panel */}
      {isRunning && streamingOutput && !isExpanded && (
        <div className="bash-tool-preview">
          <AnsiText ref={previewRef} className="bash-tool-preview-code" text={visibleStreaming} />
          {hasMoreLines && !previewExpanded && (
            <button
              className="bash-tool-preview-more"
              onClick={(e) => {
                e.stopPropagation();
                setPreviewExpanded(true);
              }}
            >
              Show all {lineCount} lines
            </button>
          )}
        </div>
      )}

      {isExpanded && (
        <div className="bash-tool-details">
          {displayData?.workingDir && (
            <div className="bash-tool-section">
              <div className="bash-tool-label">Working Directory:</div>
              <pre className="bash-tool-code bash-tool-code-cwd">{displayData.workingDir}</pre>
            </div>
          )}
          <div className="bash-tool-section">
            <div className="bash-tool-label">Command:</div>
            <pre className="bash-tool-code">{command}</pre>
          </div>

          {isRunning && streamingOutput && (
            <div className="bash-tool-section">
              <div className="bash-tool-label">Output (streaming):</div>
              <AnsiText
                ref={expandedStreamRef}
                className="bash-tool-code bash-tool-streaming"
                text={streamingOutput}
              />
            </div>
          )}

          {isComplete && (
            <div className="bash-tool-section">
              <div className="bash-tool-label">
                Output{hasError ? " (Error)" : ""}:
                {executionTime && <span className="bash-tool-time">{executionTime}</span>}
              </div>
              <AnsiText
                className={`bash-tool-code ${hasError ? "error" : ""}`}
                text={output || "(no output)"}
              />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default BashTool;
