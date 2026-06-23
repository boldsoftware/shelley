import React, { useState } from "react";
import { ChevronRightIcon } from "lucide-react";
import { Message, LLMContent } from "../types";
import { cn } from "@/lib/utils";

interface JSONSchemaProperty {
  type?: string | string[];
  description?: string;
  enum?: (string | number | boolean | null)[];
  items?: JSONSchemaProperty;
  $ref?: string;
  [key: string]: unknown;
}

interface JSONSchema {
  type?: string;
  properties?: Record<string, JSONSchemaProperty>;
  required?: string[];
  [key: string]: unknown;
}

interface ToolDescription {
  name: string;
  description: string;
  parameters?: JSONSchema;
}

interface SystemPromptDisplayData {
  tools?: ToolDescription[];
}

interface SystemPromptViewProps {
  message: Message;
}

function ToolItem({ tool }: { tool: ToolDescription }) {
  const [expanded, setExpanded] = useState(false);

  const firstLine = tool.description.trim().split("\n")[0];
  const hasDetails: boolean =
    tool.description.trim().includes("\n") ||
    Boolean(tool.parameters?.properties && Object.keys(tool.parameters.properties).length > 0);

  const required = new Set(tool.parameters?.required ?? []);
  const properties = tool.parameters?.properties ?? {};

  return (
    <div className="overflow-hidden rounded-md border border-border bg-card">
      <div
        className={cn(
          "flex items-center gap-2 px-2.5 py-1.5",
          hasDetails && "cursor-pointer select-none hover:bg-muted/50"
        )}
        onClick={hasDetails ? () => setExpanded(!expanded) : undefined}
        onKeyDown={
          hasDetails
            ? (e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setExpanded(!expanded);
                }
              }
            : undefined
        }
        tabIndex={hasDetails ? 0 : undefined}
        role={hasDetails ? "button" : undefined}
        aria-expanded={hasDetails ? expanded : undefined}
      >
        {hasDetails ? (
          <ChevronRightIcon
            className={cn(
              "size-3 shrink-0 text-muted-foreground transition-transform",
              expanded && "rotate-90"
            )}
          />
        ) : (
          <span className="size-3 shrink-0" />
        )}
        <code className="shrink-0 font-mono text-[13px] text-primary">{tool.name}</code>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
          {firstLine}
        </span>
      </div>

      {expanded && hasDetails && (
        <div className="border-t border-border px-2.5 py-2 text-sm">
          {tool.description.trim().includes("\n") && (
            <p className="mb-2 whitespace-pre-wrap text-muted-foreground">
              {tool.description.trim().split("\n").slice(1).join("\n").trim()}
            </p>
          )}
          {Object.keys(properties).length > 0 && (
            <div>
              <div className="mb-1 text-xs font-medium text-muted-foreground">Parameters</div>
              <table className="w-full border-collapse text-xs">
                <tbody>
                  {Object.entries(properties).map(([paramName, prop]) => {
                    const isRequired = required.has(paramName);
                    const typeLabel = Array.isArray(prop.type)
                      ? prop.type.join(" | ")
                      : (prop.type ?? "");
                    return (
                      <tr key={paramName} className="align-top">
                        <td className="py-1 pr-3 whitespace-nowrap">
                          <code className="font-mono text-foreground">{paramName}</code>
                          {isRequired && <span className="text-destructive">*</span>}
                        </td>
                        <td className="py-1 pr-3 whitespace-nowrap">
                          <code className="font-mono text-muted-foreground">{typeLabel}</code>
                        </td>
                        <td className="py-1 text-muted-foreground">
                          {prop.description && <span>{prop.description}</span>}
                          {prop.enum && prop.enum.length > 0 && (
                            <span>
                              {" "}
                              Allowed values:{" "}
                              {prop.enum.map((v, i) => (
                                <React.Fragment key={i}>
                                  {i > 0 && ", "}
                                  <code className="font-mono text-foreground">{String(v)}</code>
                                </React.Fragment>
                              ))}
                            </span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function SystemPromptView({ message }: SystemPromptViewProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract system prompt text from llm_data
  let systemPromptText = "";
  if (message.llm_data) {
    try {
      const llmData =
        typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
      if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
        const textContent = llmData.Content.find((c: LLMContent) => c.Type === 2 && c.Text);
        if (textContent) {
          systemPromptText = textContent.Text;
        }
      }
    } catch (err) {
      console.error("Failed to parse system prompt:", err);
    }
  }

  // Extract tool descriptions from display_data
  let tools: ToolDescription[] = [];
  if (message.display_data) {
    try {
      const displayData: SystemPromptDisplayData =
        typeof message.display_data === "string"
          ? JSON.parse(message.display_data)
          : message.display_data;
      if (displayData && displayData.tools) {
        tools = displayData.tools;
      }
    } catch (err) {
      console.error("Failed to parse system prompt display data:", err);
    }
  }

  if (!systemPromptText) {
    return null;
  }

  // Count lines and approximate size
  const lineCount = systemPromptText.split("\n").length;
  const charCount = systemPromptText.length;
  const sizeKb = (charCount / 1024).toFixed(1);

  return (
    <div className="my-1 overflow-hidden rounded-lg border border-border bg-card text-card-foreground">
      <div
        className="flex cursor-pointer select-none items-center gap-2 px-3 py-1.5 hover:bg-muted/50"
        onClick={() => setIsExpanded(!isExpanded)}
      >
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <span className="shrink-0 text-base leading-none">📋</span>
          <span className="shrink-0 text-[13px] font-medium">System Prompt</span>
          <span className="min-w-0 truncate text-xs text-muted-foreground">
            {lineCount} lines, {sizeKb} KB{tools.length > 0 && ` · ${tools.length} tools`}
          </span>
        </div>
        <button
          type="button"
          className="shrink-0 text-muted-foreground"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <ChevronRightIcon
            className={cn("size-3.5 transition-transform", isExpanded && "rotate-90")}
          />
        </button>
      </div>

      {isExpanded && (
        <div className="border-t border-border px-3 py-2">
          {tools.length > 0 && (
            <div className="mb-3">
              <div className="mb-1.5 text-xs font-medium text-muted-foreground">
                🔧 Tools ({tools.length})
              </div>
              <div className="flex flex-col gap-1.5">
                {tools.map((tool) => (
                  <ToolItem key={tool.name} tool={tool} />
                ))}
              </div>
            </div>
          )}
          <pre className="max-h-96 overflow-auto rounded-md bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-words">
            {systemPromptText}
          </pre>
        </div>
      )}
    </div>
  );
}

export default SystemPromptView;
