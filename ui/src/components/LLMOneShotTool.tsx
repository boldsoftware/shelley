import React from "react";
import { LLMContent } from "../types";
import { ToolCard, ToolSection, ToolCode, ToolStatusMark } from "./ToolCard";

interface LLMOneShotToolProps {
  toolInput?: unknown; // { prompt_file: string, output_file?: string, model?: string, system_prompt?: string }
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function LLMOneShotTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  executionTime,
}: LLMOneShotToolProps) {
  const input =
    typeof toolInput === "object" && toolInput !== null
      ? (toolInput as {
          prompt_file?: string;
          output_file?: string;
          model?: string;
          system_prompt?: string;
        })
      : {};

  const promptFile = input.prompt_file || "";
  const model = input.model || "";

  const resultText =
    toolResult
      ?.filter((r) => r.Type === 2)
      .map((r) => r.Text)
      .join("\n") || "";

  const isComplete = !isRunning && toolResult !== undefined;

  const summaryParts: string[] = [];
  if (promptFile) summaryParts.push(promptFile);
  if (model) summaryParts.push(`model: ${model}`);
  const summary = summaryParts.join(" · ") || "llm_one_shot";

  return (
    <ToolCard
      emoji="🤖"
      running={isRunning}
      complete={isComplete}
      title={
        <>
          llm_one_shot{" "}
          <span className="text-muted-foreground">{summary}</span>
        </>
      }
      status={isComplete ? <ToolStatusMark error={hasError} /> : null}
    >
      <ToolSection label="Prompt file:">
        <ToolCode>{promptFile || "(none)"}</ToolCode>
      </ToolSection>

      {model && (
        <ToolSection label="Model:">
          <ToolCode>{model}</ToolCode>
        </ToolSection>
      )}

      {input.system_prompt && (
        <ToolSection label="System prompt:">
          <ToolCode>{input.system_prompt}</ToolCode>
        </ToolSection>
      )}

      {input.output_file && (
        <ToolSection label="Output file:">
          <ToolCode>{input.output_file}</ToolCode>
        </ToolSection>
      )}

      {isComplete && (
        <ToolSection
          label={
            <span className="flex items-center gap-2">
              <span>Result{hasError ? " (Error)" : ""}:</span>
              {executionTime && (
                <span className="text-muted-foreground">{executionTime}</span>
              )}
            </span>
          }
        >
          <ToolCode error={hasError}>{resultText || "(no output)"}</ToolCode>
        </ToolSection>
      )}
    </ToolCard>
  );
}

export default LLMOneShotTool;
