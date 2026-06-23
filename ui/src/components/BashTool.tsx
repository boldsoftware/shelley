import React, { useMemo, useState } from "react";
import { LLMContent } from "../types";
import { useInToolDetail } from "./ToolDetailContext";
import { ToolCard, ToolSection, ToolStatusMark } from "./ToolCard";
import { cn } from "@/lib/utils";
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

/** Shared monospace block styling for code/output, matching ToolCode. */
const CODE_CLASS =
  "max-h-96 overflow-auto rounded-md bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-words";

function BashTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  display,
  streamingOutput,
}: BashToolProps) {
  // Streaming preview — expanded to show full streaming output (beyond PREVIEW_LINES).
  const [previewExpanded, setPreviewExpanded] = useState(false);
  const previewRef = React.useRef<HTMLPreElement>(null);

  // The live preview duplicates the detail body's output panel, which is open
  // by default inside the tool detail modal — suppress the preview there.
  const inToolDetail = useInToolDetail();
  const prevRunning = React.useRef(isRunning);
  React.useEffect(() => {
    if (prevRunning.current && !isRunning) {
      setPreviewExpanded(false);
    }
    prevRunning.current = isRunning;
  }, [isRunning]);

  // Auto-scroll streaming preview to bottom.
  React.useEffect(() => {
    const el = previewRef.current;
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

  // Show the live preview below the card while running (outside the collapsible
  // body so it stays visible whether or not the card is expanded), except in the
  // detail modal where the body's streaming output is already shown.
  const showPreview = isRunning && !!streamingOutput && !inToolDetail;

  return (
    <div>
      <ToolCard
        className="bash-tool"
        emoji="🛠️"
        running={isRunning}
        complete={isComplete}
        title={
          <span className="bash-tool-header flex items-center gap-2">
            <span className="bash-tool-command min-w-0 truncate" title={command}>
              {displayCommand}
            </span>
            {displayData?.workingDir && (
              <span className="shrink-0 text-muted-foreground" title={displayData.workingDir}>
                in {displayData.workingDir}
              </span>
            )}
          </span>
        }
        status={
          isComplete ? (
            isCancelled ? (
              <span className="font-medium text-destructive" aria-label="cancelled">
                ✗ cancelled
              </span>
            ) : (
              <ToolStatusMark error={hasError} />
            )
          ) : null
        }
      >
        <div className="bash-tool-details">
          {displayData?.workingDir && (
            <ToolSection label="Working Directory:">
              <pre className={cn(CODE_CLASS, "text-muted-foreground")}>{displayData.workingDir}</pre>
            </ToolSection>
          )}

          <ToolSection label="Command:">
            <pre className={cn("bash-tool-code", CODE_CLASS)}>{command}</pre>
          </ToolSection>

          {isRunning && streamingOutput && (
            <ToolSection label="Output (streaming):">
              <AnsiText className={CODE_CLASS} text={streamingOutput} />
            </ToolSection>
          )}

          {isComplete && (
            <ToolSection
              label={
                <span className="flex items-center gap-2">
                  <span>Output{hasError ? " (Error)" : ""}:</span>
                  {executionTime && <span className="text-muted-foreground">{executionTime}</span>}
                </span>
              }
            >
              <AnsiText
                className={cn("bash-tool-code", CODE_CLASS, hasError && "text-destructive")}
                text={output || "(no output)"}
              />
            </ToolSection>
          )}
        </div>
      </ToolCard>

      {/* Live streaming preview — stays visible below the card while running. */}
      {showPreview && (
        <div className="mt-1 px-3">
          <AnsiText
            ref={previewRef}
            className={cn(
              CODE_CLASS,
              "max-h-40 border border-primary/60 text-muted-foreground"
            )}
            text={visibleStreaming}
          />
          {hasMoreLines && !previewExpanded && (
            <button
              type="button"
              className="mt-1 font-mono text-xs text-primary hover:underline"
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
    </div>
  );
}

export default BashTool;
