import React from "react";
import { LLMContent } from "../types";
import { ToolCard } from "./ToolCard";

interface WebSearchToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  searchResults?: LLMContent[];
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
}

function WebSearchResultItem({ result }: { result: LLMContent }) {
  const title = result.Title || "Untitled";
  const url = result.URL || "";
  const pageAge = result.PageAge || "";

  return (
    <div className="border-b border-border py-2 last:border-b-0">
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        className="font-medium text-primary hover:underline"
      >
        {title}
      </a>
      <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span className="break-all">{url}</span>
        {pageAge && <span>{pageAge}</span>}
      </div>
    </div>
  );
}

function WebSearchTool({ toolInput, isRunning, searchResults, toolResult }: WebSearchToolProps) {
  // Anthropic sends {"query": "..."}; OpenAI Responses sends {"queries": [...]}
  let queries: string[] = [];
  if (toolInput && typeof toolInput === "object") {
    const ti = toolInput as { query?: string; queries?: string[] };
    if (typeof ti.query === "string") queries = [ti.query];
    else if (Array.isArray(ti.queries)) queries = ti.queries;
  }
  const query = queries.join(" / ");

  const results = searchResults || toolResult || [];
  // OpenAI's server-side search doesn't deliver structured results to us;
  // the citations are attached to the assistant's message text instead.
  // So "complete with 0 results" is normal for OpenAI — only mark running
  // based on the isRunning flag.
  const isComplete = !isRunning;
  const resultCount = results.length;
  const showCount = resultCount > 0;

  return (
    <ToolCard
      emoji="🔍"
      running={isRunning}
      complete={isComplete}
      title={
        <>
          Web Search{query ? ": " : ""}
          {query && <span className="text-muted-foreground">{query}</span>}
        </>
      }
      status={
        isComplete && showCount ? (
          <span className="text-muted-foreground">
            {resultCount} result{resultCount !== 1 ? "s" : ""}
          </span>
        ) : null
      }
    >
      {results.length > 0 ? (
        <div className="-my-2">
          {results.map((result, index) => (
            <WebSearchResultItem key={index} result={result} />
          ))}
        </div>
      ) : undefined}
    </ToolCard>
  );
}

export default WebSearchTool;
