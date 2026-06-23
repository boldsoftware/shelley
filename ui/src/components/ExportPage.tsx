// Standalone conversation export view, served at /export/<conversation_id>.
//
// This is a real, bookmarkable route (not a blob: document), so it opens and
// refreshes like any normal page and avoids the browser "download blob:"
// quirks. It fetches the conversation, converts it to Markdown on the client
// (conversationToMarkdown), and shows a split editor: editable Markdown source
// on the left, live-rendered preview on the right. It reuses the app's bundled
// marked + DOMPurify (via MarkdownContent), so it works offline.
//
// NOTE: this page is mounted WITHOUT the app's TooltipProvider / Toaster /
// I18nProvider (see main.tsx), so it must not use shadcn Tooltip or sonner —
// hence the small self-contained inline toast below.
import React, { useEffect, useMemo, useRef, useState, useCallback } from "react";
import { Loader2Icon } from "lucide-react";
import { api } from "../services/api";
import { Conversation, Message } from "../types";
import { conversationToMarkdown } from "../utils/conversationMarkdown";
import MarkdownContent from "./MarkdownContent";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

function filenameFor(conversation: Conversation | null): string {
  const base = (conversation?.slug || "conversation")
    .replace(/[^a-z0-9-_]+/gi, "-")
    .replace(/^-+|-+$/g, "")
    .toLowerCase();
  return `${base || "conversation"}.md`;
}

function download(name: string, text: string, mime: string) {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text);
  }
  // Fallback for non-secure contexts.
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  document.execCommand("copy");
  document.body.removeChild(ta);
}

// Extract the /export/<id> conversation id from the current path.
export function exportConversationIdFromPath(): string | null {
  const m = window.location.pathname.match(/^\/export\/([^/]+)\/?$/);
  return m ? decodeURIComponent(m[1]) : null;
}

type MobilePane = "edit" | "preview";

function ExportPage({ conversationId }: { conversationId: string }) {
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [includeToolOutputs, setIncludeToolOutputs] = useState(true);
  // The editable source. We seed it from the generated markdown and let the
  // user edit; toggling the checkbox regenerates (with an edit guard).
  const [source, setSource] = useState("");
  const [edited, setEdited] = useState(false);
  const [mobilePane, setMobilePane] = useState<MobilePane>("edit");
  const [toast, setToast] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    api
      .getConversationWithProgress(conversationId)
      .then((resp) => {
        if (cancelled) return;
        setConversation(resp.conversation ?? null);
        setMessages(resp.messages ?? []);
        setLoading(false);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [conversationId]);

  // Markdown generated from the current options. Memoized so toggling the
  // checkbox is cheap and the edit-guard can compare against it.
  const generated = useMemo(
    () =>
      conversationToMarkdown(conversation ?? undefined, messages, {
        includeToolOutputs,
      }),
    [conversation, messages, includeToolOutputs],
  );

  // Seed the editor exactly once, when the conversation finishes loading.
  // (Checkbox toggles re-seed explicitly in onToggleToolOutputs; we don't want
  // `generated` changing to clobber the editor on every render.)
  const [seeded, setSeeded] = useState(false);
  useEffect(() => {
    if (!seeded && !loading && !error) {
      setSource(generated);
      setEdited(false);
      setSeeded(true);
    }
  }, [seeded, loading, error, generated]);

  useEffect(() => {
    if (conversation) {
      document.title = `${conversation.slug || "Conversation"} — Export`;
    }
  }, [conversation]);

  const toastTimer = useRef<number | undefined>(undefined);
  const showToast = useCallback((msg: string) => {
    setToast(msg);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(null), 1600);
  }, []);

  const onToggleToolOutputs = (next: boolean) => {
    // Regenerate with the new option. Guard against clobbering hand edits.
    const nextMd = conversationToMarkdown(conversation ?? undefined, messages, {
      includeToolOutputs: next,
    });
    if (edited && source !== generated) {
      if (!window.confirm("Switching tool outputs will discard your edits. Continue?")) {
        return;
      }
    }
    setIncludeToolOutputs(next);
    setSource(nextMd);
    setEdited(false);
  };

  const filename = filenameFor(conversation);

  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center bg-background text-foreground">
        <Loader2Icon className="size-6 animate-spin text-muted-foreground" aria-label="Loading" />
      </div>
    );
  }
  if (error) {
    return (
      <div className="flex h-screen items-center justify-center bg-background p-4 text-center text-destructive">
        <div>Failed to load conversation: {error}</div>
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col bg-background text-foreground">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="min-w-0 flex-1 truncate font-medium" title={conversation?.slug || "Conversation"}>
          {conversation?.slug || "Conversation"}
        </div>
        <div className="flex items-center gap-2">
          <Checkbox
            id="export-include-tools"
            checked={includeToolOutputs}
            onCheckedChange={(c) => onToggleToolOutputs(c === true)}
          />
          <Label htmlFor="export-include-tools" className="text-sm font-normal">
            Include tool outputs
          </Label>
        </div>
        <div
          className="flex gap-0.5 rounded-md border border-border p-0.5 text-sm md:hidden"
          role="tablist"
        >
          <button
            className={cn(
              "rounded-sm px-3 py-1",
              mobilePane === "edit"
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground",
            )}
            onClick={() => setMobilePane("edit")}
            role="tab"
            aria-selected={mobilePane === "edit"}
          >
            Markdown
          </button>
          <button
            className={cn(
              "rounded-sm px-3 py-1",
              mobilePane === "preview"
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground",
            )}
            onClick={() => setMobilePane("preview")}
            role="tab"
            aria-selected={mobilePane === "preview"}
          >
            Preview
          </button>
        </div>
      </header>

      <main className="flex min-h-0 flex-1">
        <section
          className={cn(
            "min-w-0 flex-1 flex-col border-r border-border md:flex",
            mobilePane === "edit" ? "flex" : "hidden",
          )}
          aria-label="Markdown source"
        >
          <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
            <span className="text-xs font-medium text-muted-foreground">Markdown</span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  copyText(source).then(
                    () => showToast("Markdown copied"),
                    () => showToast("Copy failed"),
                  )
                }
              >
                Copy
              </Button>
              <Button
                size="sm"
                onClick={() => {
                  download(filename, source, "text/markdown");
                  showToast(`Downloaded ${filename}`);
                }}
              >
                Download .md
              </Button>
            </div>
          </div>
          <textarea
            className="min-h-0 flex-1 resize-none bg-background p-3 font-mono text-sm text-foreground outline-none"
            spellCheck={false}
            value={source}
            onChange={(e) => {
              setSource(e.target.value);
              setEdited(true);
            }}
            aria-label="Editable markdown"
          />
        </section>

        <section
          className={cn(
            "min-w-0 flex-1 flex-col md:flex",
            mobilePane === "preview" ? "flex" : "hidden",
          )}
          aria-label="Rendered preview"
        >
          <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
            <span className="text-xs font-medium text-muted-foreground">Preview</span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  copyText(source).then(
                    () => showToast("Copied as text"),
                    () => showToast("Copy failed"),
                  )
                }
              >
                Copy
              </Button>
            </div>
          </div>
          <article className="markdown-content min-h-0 flex-1 overflow-auto p-4">
            <MarkdownContent text={source} />
          </article>
        </section>
      </main>

      {toast && (
        <div className="fixed bottom-4 left-1/2 z-50 -translate-x-1/2 rounded-md border border-border bg-popover px-3 py-1.5 text-sm text-popover-foreground shadow-lg">
          {toast}
        </div>
      )}
    </div>
  );
}

export default ExportPage;
