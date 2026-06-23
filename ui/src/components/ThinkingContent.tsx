import React, { useState } from "react";
import { ChevronRightIcon } from "lucide-react";
import { cn } from "@/lib/utils";

interface ThinkingContentProps {
  thinking: string;
}

function ThinkingContent({ thinking }: ThinkingContentProps) {
  const [isExpanded, setIsExpanded] = useState(true);

  // Truncate thinking for display - get first 80 chars
  const truncateThinking = (text: string, maxLen: number = 80) => {
    if (!text) return "";
    const firstLine = text.split("\n")[0];
    if (firstLine.length <= maxLen) return firstLine;
    return firstLine.substring(0, maxLen) + "...";
  };

  const preview = truncateThinking(thinking);

  return (
    <div
      className="thinking-content my-1 rounded-lg border border-border bg-muted/40 text-sm"
      data-testid="thinking-content"
    >
      <div
        onClick={() => setIsExpanded(!isExpanded)}
        className="flex cursor-pointer items-start gap-2 px-3 py-2 select-none"
      >
        <span className="shrink-0 leading-none" aria-hidden>
          💭
        </span>
        <div
          className={cn(
            "min-w-0 flex-1 text-muted-foreground italic",
            isExpanded ? "whitespace-pre-wrap" : "truncate"
          )}
        >
          {isExpanded ? thinking : preview}
        </div>
        <button
          className="shrink-0 text-muted-foreground"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
        >
          <ChevronRightIcon
            className={cn("size-3.5 transition-transform", isExpanded && "rotate-90")}
          />
        </button>
      </div>
    </div>
  );
}

export default ThinkingContent;
