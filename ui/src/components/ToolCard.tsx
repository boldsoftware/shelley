import * as React from "react";
import { ChevronRightIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { useToolExpandedState } from "./ToolDetailContext";

/**
 * Shared chrome for tool-call cards in the conversation, styled with the
 * shadcn/preset theme. Replaces the legacy `.tool` / `.tool-header` CSS used by
 * every tool renderer. Preserves the data-testid contract the e2e suite and
 * Message rendering rely on: the root carries `tool-call-completed` when
 * `complete`, else `tool-call-running`. Expand/collapse is seeded from
 * ToolDetailContext via useToolExpandedState() (expanded inside a detail modal,
 * collapsed inline), matching prior behavior.
 */
interface ToolCardProps {
  emoji?: React.ReactNode;
  running?: boolean;
  complete: boolean;
  title: React.ReactNode;
  status?: React.ReactNode;
  children?: React.ReactNode;
  className?: string;
  /** Extra class on the emoji span — lets specific tools expose a legacy
   *  per-tool hook (e.g. patch-tool-emoji, screenshot-tool-emoji). */
  emojiClassName?: string;
  /** Extra class on the expand/collapse toggle button (e.g. patch-tool-toggle). */
  toggleClassName?: string;
}

export function ToolCard({
  emoji,
  running,
  complete,
  title,
  status,
  children,
  className,
  emojiClassName,
  toggleClassName,
}: ToolCardProps) {
  const [expanded, setExpanded] = useToolExpandedState();
  const expandable = children != null && children !== false;

  return (
    <div
      data-testid={complete ? "tool-call-completed" : "tool-call-running"}
      className={cn(
        "tool my-1 overflow-hidden rounded-lg border border-border bg-card text-card-foreground",
        className
      )}
    >
      <div
        className={cn(
          "flex items-center gap-2 px-3 py-1.5",
          expandable && "cursor-pointer select-none hover:bg-muted/50"
        )}
        onClick={expandable ? () => setExpanded((v) => !v) : undefined}
      >
        {emoji != null && (
          <span
            className={cn(
              "tool-emoji shrink-0 text-base leading-none",
              emojiClassName,
              running && "animate-pulse"
            )}
          >
            {emoji}
          </span>
        )}
        <span className="min-w-0 flex-1 truncate font-mono text-[13px]">
          {title}
        </span>
        {status != null && <span className="shrink-0 text-xs">{status}</span>}
        {expandable && (
          <button
            type="button"
            aria-label={expanded ? "Collapse" : "Expand"}
            aria-expanded={expanded}
            className={cn("shrink-0 text-muted-foreground", toggleClassName)}
            onClick={(e) => {
              e.stopPropagation();
              setExpanded((v) => !v);
            }}
          >
            <ChevronRightIcon
              className={cn("size-3.5 transition-transform", expanded && "rotate-90")}
            />
          </button>
        )}
      </div>
      {expandable && expanded && (
        <div className="border-t border-border px-3 py-2 text-sm">{children}</div>
      )}
    </div>
  );
}

/** A labelled section inside a tool card's expanded body. */
export function ToolSection({
  label,
  children,
  className,
}: {
  label?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("mb-2 last:mb-0", className)}>
      {label != null && (
        <div className="mb-1 text-xs font-medium text-muted-foreground">
          {label}
        </div>
      )}
      {children}
    </div>
  );
}

/** Monospace, scrollable code/output block for tool detail bodies. */
export function ToolCode({
  children,
  error,
  className,
}: {
  children: React.ReactNode;
  error?: boolean;
  className?: string;
}) {
  return (
    <pre
      className={cn(
        "tool-code max-h-96 overflow-auto rounded-md bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-words",
        error && "text-destructive",
        className
      )}
    >
      {children}
    </pre>
  );
}

/** Inline success/error glyph for a tool card's status slot. */
export function ToolStatusMark({ error }: { error?: boolean }) {
  return error ? (
    <span className="font-medium text-destructive" aria-label="failed">
      ✗
    </span>
  ) : (
    <span
      className="font-medium text-emerald-600 dark:text-emerald-500"
      aria-label="succeeded"
    >
      ✓
    </span>
  );
}
