import React, { useState } from "react";
import { LLMContent } from "../types";

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
  const [isExpanded, setIsExpanded] = useState(false);

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
    <div className="tool" data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}>
      <div className="tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="tool-summary">
          <span className={`tool-emoji ${isRunning ? "running" : ""}`}>🤖</span>
          <span className="tool-name">llm_one_shot</span>
          {isComplete && hasError && <span className="tool-error">✗</span>}
          {isComplete && !hasError && <span className="tool-success">✓</span>}
          <span className="tool-command">{summary}</span>
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
            <div className="tool-label">Prompt file:</div>
            <pre className="tool-code">{promptFile || "(none)"}</pre>
          </div>

          {model && (
            <div className="tool-section">
              <div className="tool-label">Model:</div>
              <pre className="tool-code">{model}</pre>
            </div>
          )}

          {input.system_prompt && (
            <div className="tool-section">
              <div className="tool-label">System prompt:</div>
              <pre className="tool-code">{input.system_prompt}</pre>
            </div>
          )}

          {input.output_file && (
            <div className="tool-section">
              <div className="tool-label">Output file:</div>
              <pre className="tool-code">{input.output_file}</pre>
            </div>
          )}

          {isComplete && (
            <div className="tool-section">
              <div className="tool-label">
                Result{hasError ? " (Error)" : ""}:
                {executionTime && <span className="tool-time">{executionTime}</span>}
              </div>
              <pre className={`tool-code ${hasError ? "error" : ""}`}>
                {resultText || "(no output)"}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default LLMOneShotTool;
