import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";
import { Badge } from "@/components/ui/badge";

interface SubagentToolProps {
  // For tool_use (pending state)
  toolInput?: unknown; // { slug: string, prompt: string, timeout_seconds?: number, wait?: boolean }
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  displayData?: { slug?: string; conversation_id?: string; cli_agent?: string; status?: string };
}

function SubagentTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
  displayData,
}: SubagentToolProps) {
  // Extract fields from toolInput
  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as {
          slug?: string;
          prompt?: string;
          model?: string;
          timeout_seconds?: number;
          wait?: boolean;
        })
      : {};

  const slug = input.slug || displayData?.slug || "subagent";
  const prompt = input.prompt || "";
  const model = input.model || "";
  const wait = input.wait !== false;
  const timeout = input.timeout_seconds || 60;

  // Detect CLI agent backend from display data
  const cliAgent = displayData?.cli_agent; // "claude-cli" or "codex-cli"
  const cliAgentLabel =
    cliAgent === "claude-cli" ? "Claude CLI" : cliAgent === "codex-cli" ? "Codex CLI" : null;

  // Extract result text
  const resultText =
    toolResult
      ?.filter((r) => r.Type === 2) // ContentTypeText
      .map((r) => r.Text)
      .join("\n") || "";

  // Truncate prompt for display
  const truncateText = (text: string, maxLen: number = 60) => {
    if (!text) return "";
    const firstLine = text.split("\n")[0];
    if (firstLine.length <= maxLen) return firstLine;
    return firstLine.substring(0, maxLen) + "...";
  };

  const displayPrompt = truncateText(prompt);
  const isComplete = !isRunning && toolResult !== undefined;

  return (
    <ToolCard
      emoji="⚡"
      running={isRunning}
      complete={isComplete}
      title={
        <span className="inline-flex items-center gap-1.5" title={prompt}>
          <span>subagent</span>
          {cliAgentLabel && (
            <Badge variant="secondary" className="font-mono">
              {cliAgentLabel}
            </Badge>
          )}
          <span className="text-muted-foreground">
            Subagent '{slug}'{model ? ` (${model})` : ""}{" "}
            {isRunning ? (wait ? "running..." : "started") : ""}
            {displayPrompt && !isRunning && ` ${displayPrompt}`}
          </span>
        </span>
      }
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection
        label={
          <span className="flex flex-wrap items-center gap-1.5">
            <span>Prompt to '{slug}':</span>
            {model && <Badge variant="secondary">{model}</Badge>}
            {!wait && <Badge variant="outline">fire-and-forget</Badge>}
            {timeout !== 60 && <Badge variant="outline">timeout: {timeout}s</Badge>}
          </span>
        }
      >
        <ToolCode>{prompt || "(no prompt)"}</ToolCode>
      </ToolSection>

      {isComplete && (
        <ToolSection
          label={
            <span className="flex items-center gap-2">
              <span>Response:</span>
              {executionTime && (
                <span className="text-muted-foreground">{executionTime}</span>
              )}
            </span>
          }
        >
          <ToolCode error={hasError}>{resultText || "(no response)"}</ToolCode>
        </ToolSection>
      )}

      {displayData?.conversation_id && (
        <ToolSection label="Conversation:">
          <a
            href={`/c/${slug}`}
            onClick={(e) => {
              // Let the browser handle cmd/ctrl/shift/middle-click (open in new tab/window).
              if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
              e.preventDefault();
              // Navigate to the subagent conversation
              window.history.pushState({}, "", `/c/${slug}`);
              window.dispatchEvent(new PopStateEvent("popstate"));
            }}
            className="text-primary hover:underline"
          >
            View subagent conversation →
          </a>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default SubagentTool;
