import React, { useState } from "react";
import { CopyIcon, FlameIcon } from "lucide-react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";
import { Button } from "@/components/ui/button";

interface BrowserProfileToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function BrowserProfileTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: BrowserProfileToolProps) {
  const [copied, setCopied] = useState(false);

  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as { action?: string; categories?: string })
      : {};

  const action = input.action || "";

  const output =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  // Detect file paths in output (for cpu_stop, trace_stop results)
  const filePathMatch = output.match(/([^\s]+\.json)/i);
  const savedFilePath = filePathMatch ? filePathMatch[1] : null;

  const summary = action || "profile";

  const handleCopyPath = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (savedFilePath) {
      navigator.clipboard.writeText(savedFilePath).then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      });
    }
  };

  return (
    <ToolCard
      emoji="📊"
      running={isRunning}
      complete={isComplete}
      title={summary}
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Action:">
        <ToolCode>{action || "(none)"}</ToolCode>
      </ToolSection>

      {input.categories && (
        <ToolSection label="Categories:">
          <ToolCode>{input.categories}</ToolCode>
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

      {isComplete && savedFilePath && !hasError && (
        <ToolSection label="Profile/Trace file:">
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1 font-mono text-xs">
              {savedFilePath}
            </code>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleCopyPath}
            >
              <CopyIcon />
              {copied ? "Copied" : "Copy path"}
            </Button>
            {(action === "cpu_stop" || action === "trace_stop") && (
              <Button asChild variant="outline" size="sm">
                <a
                  href={`https://www.speedscope.app/#profileURL=${encodeURIComponent(window.location.origin + "/api/read?path=" + encodeURIComponent(savedFilePath))}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  onClick={(e) => e.stopPropagation()}
                >
                  <FlameIcon />
                  Open in Speedscope
                </a>
              </Button>
            )}
          </div>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default BrowserProfileTool;
