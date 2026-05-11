import React, { useEffect, useMemo, useRef, useState } from "react";
import { Message, LLMMessage, LLMContent } from "../types";

interface TOCEntry {
  id: string; // unique stable id (used as fragment slug)
  kind: "top" | "user" | "eot" | "gen" | "bottom";
  label: string;
  messageId?: string; // for messages, target [data-message-id]
  generation?: number;
}

interface ConversationTOCProps {
  messages: Message[];
  containerRef: React.RefObject<HTMLElement | null>;
  conversationSlug?: string | null;
}

// Extract a short human-readable label from a message's text content.
function extractMessageLabel(message: Message, maxLen = 70): string {
  if (!message.llm_data) return "";
  let llm: LLMMessage | null = null;
  try {
    llm =
      typeof message.llm_data === "string"
        ? (JSON.parse(message.llm_data) as LLMMessage)
        : (message.llm_data as LLMMessage);
  } catch {
    return "";
  }
  if (!llm?.Content) return "";
  const parts: string[] = [];
  for (const c of llm.Content as LLMContent[]) {
    if (c.Type === 2 && c.Text) parts.push(c.Text); // text
  }
  let s = parts.join(" ").replace(/\s+/g, " ").trim();
  // Drop common markdown noise prefixes (headers, list markers, code fences)
  s = s.replace(/^[#>*\-`\s]+/, "").trim();
  if (s.length > maxLen) s = s.slice(0, maxLen - 1) + "…";
  return s;
}

// Slug-like fragment id derived from message_id. Short and stable.
function fragmentForMessage(messageId: string): string {
  // message_id is uuid-ish; use first 8 chars
  const short = messageId.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8);
  return `m-${short}`;
}

function buildEntries(messages: Message[]): TOCEntry[] {
  const entries: TOCEntry[] = [];
  entries.push({ id: "top", kind: "top", label: "Top of conversation" });

  let prevGen: number | null = null;
  for (const m of messages) {
    if (m.generation !== prevGen) {
      if (prevGen !== null) {
        entries.push({
          id: `gen-${m.generation}`,
          kind: "gen",
          label: `New generation (${m.generation})`,
          generation: m.generation,
        });
      }
      prevGen = m.generation;
    }
    if (m.type === "user") {
      // Skip pure tool-result user messages
      let onlyToolResults = false;
      if (m.llm_data) {
        try {
          const llm =
            typeof m.llm_data === "string" ? JSON.parse(m.llm_data) : (m.llm_data as LLMMessage);
          const content = (llm?.Content || []) as LLMContent[];
          onlyToolResults =
            content.length > 0 && content.every((c) => c.Type === 6 || c.Type === 4);
        } catch {
          // ignore
        }
      }
      if (onlyToolResults) continue;
      const text = extractMessageLabel(m);
      if (!text) continue;
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "user",
        label: text,
        messageId: m.message_id,
      });
    } else if (m.type === "agent" && m.end_of_turn) {
      const text = extractMessageLabel(m);
      if (!text) continue;
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "eot",
        label: text,
        messageId: m.message_id,
      });
    }
  }

  entries.push({ id: "bottom", kind: "bottom", label: "End of conversation" });
  return entries;
}

function findMessageElement(container: HTMLElement, messageId: string): HTMLElement | null {
  return container.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(messageId)}"]`);
}

function findMessageElementByFragment(
  container: HTMLElement,
  fragment: string,
): HTMLElement | null {
  if (!fragment.startsWith("m-")) return null;
  const short = fragment.slice(2);
  // Find first message whose normalized id starts with `short`
  const all = container.querySelectorAll<HTMLElement>("[data-message-id]");
  for (const el of all) {
    const mid = el.getAttribute("data-message-id") || "";
    const norm = mid.replace(/[^a-zA-Z0-9]/g, "");
    if (norm.startsWith(short)) return el;
  }
  return null;
}

function highlight(el: HTMLElement) {
  el.classList.remove("message-highlight");
  void el.offsetWidth;
  el.classList.add("message-highlight");
  window.setTimeout(() => el.classList.remove("message-highlight"), 2200);
}

export function scrollToFragment(
  container: HTMLElement,
  fragment: string,
  options: { highlight?: boolean } = {},
): boolean {
  const el = findMessageElementByFragment(container, fragment);
  if (!el) return false;
  el.scrollIntoView({ behavior: "smooth", block: "start" });
  if (options.highlight !== false) highlight(el);
  return true;
}

const ConversationTOC: React.FC<ConversationTOCProps> = ({ messages, containerRef }) => {
  const [open, setOpen] = useState(false);
  const [activeId, setActiveId] = useState<string | null>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const [popoverPos, setPopoverPos] = useState<{
    bottom: number;
    right: number;
  } | null>(null);

  const entries = useMemo(() => buildEntries(messages), [messages]);

  // Determine which entry is currently "active" based on scroll position.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const update = () => {
      let active: string | null = null;
      const containerRect = container.getBoundingClientRect();
      const cutoff = containerRect.top + 80;
      for (const entry of entries) {
        if (entry.kind === "top") {
          if (container.scrollTop <= 40) active = entry.id;
          continue;
        }
        if (entry.kind === "bottom") continue;
        if (entry.kind === "gen") continue;
        if (!entry.messageId) continue;
        const el = findMessageElement(container, entry.messageId);
        if (!el) continue;
        if (el.getBoundingClientRect().top <= cutoff) active = entry.id;
      }
      // If near bottom, mark bottom active
      const nearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 80;
      if (nearBottom) active = "bottom";
      setActiveId(active);
    };
    update();
    container.addEventListener("scroll", update, { passive: true });
    return () => container.removeEventListener("scroll", update);
  }, [entries, containerRef]);

  // Position the popover above the trigger button, anchored to the viewport.
  useEffect(() => {
    if (!open) return;
    const updatePos = () => {
      const btn = buttonRef.current;
      if (!btn) return;
      const rect = btn.getBoundingClientRect();
      setPopoverPos({
        bottom: window.innerHeight - rect.top + 8,
        right: window.innerWidth - rect.right,
      });
    };
    updatePos();
    window.addEventListener("resize", updatePos);
    window.addEventListener("scroll", updatePos, true);
    return () => {
      window.removeEventListener("resize", updatePos);
      window.removeEventListener("scroll", updatePos, true);
    };
  }, [open]);

  // Close on outside click / Escape
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (popoverRef.current?.contains(t)) return;
      if (buttonRef.current?.contains(t)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // Resolve URL fragment on mount and on messages/hash change.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const fragment = window.location.hash.slice(1);
    if (!fragment) return;
    // Retry briefly while messages may still be laying out
    let tries = 0;
    const tryScroll = () => {
      if (scrollToFragment(container, fragment)) return;
      if (++tries < 10) window.setTimeout(tryScroll, 100);
    };
    tryScroll();
  }, [messages.length, containerRef]);

  useEffect(() => {
    const onHashChange = () => {
      const container = containerRef.current;
      if (!container) return;
      const fragment = window.location.hash.slice(1);
      if (fragment) scrollToFragment(container, fragment);
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, [containerRef]);

  const handleGoto = (entry: TOCEntry) => {
    const container = containerRef.current;
    if (!container) return;
    setOpen(false);
    if (entry.kind === "top") {
      container.scrollTo({ top: 0, behavior: "smooth" });
      history.replaceState(null, "", window.location.pathname + window.location.search);
      return;
    }
    if (entry.kind === "bottom") {
      // Pin to bottom across a few frames, since lazy layout can grow
      // scrollHeight after the first scroll lands.
      let lastHeight = -1;
      let stable = 0;
      let frames = 0;
      const step = () => {
        const el = containerRef.current;
        if (!el) return;
        el.scrollTop = el.scrollHeight;
        if (el.scrollHeight === lastHeight) {
          if (++stable >= 3) return;
        } else {
          stable = 0;
          lastHeight = el.scrollHeight;
        }
        if (++frames < 60) requestAnimationFrame(step);
      };
      requestAnimationFrame(step);
      history.replaceState(null, "", window.location.pathname + window.location.search);
      return;
    }
    if (entry.kind === "gen") {
      // Find the first message of that generation
      const target = messages.find((m) => m.generation === entry.generation);
      if (target) {
        const el = findMessageElement(container, target.message_id);
        if (el) {
          el.scrollIntoView({ behavior: "smooth", block: "start" });
          highlight(el);
        }
      }
      return;
    }
    if (!entry.messageId) return;
    const el = findMessageElement(container, entry.messageId);
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "start" });
    highlight(el);
    const url = `${window.location.pathname}${window.location.search}#${entry.id}`;
    history.replaceState(null, "", url);
  };

  // Counts for status text
  const counts = useMemo(() => {
    let user = 0,
      eot = 0,
      gen = 0;
    for (const e of entries) {
      if (e.kind === "user") user++;
      else if (e.kind === "eot") eot++;
      else if (e.kind === "gen") gen++;
    }
    return { user, eot, gen };
  }, [entries]);

  return (
    <>
      <button
        ref={buttonRef}
        className={`toc-button${open ? " toc-button-open" : ""}`}
        onClick={() => setOpen((o) => !o)}
        aria-label="Conversation table of contents"
        aria-haspopup="true"
        aria-expanded={open}
        title="Table of contents"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" className="toc-button-icon">
          <line x1="8" y1="6" x2="20" y2="6" strokeWidth={2} strokeLinecap="round" />
          <line x1="8" y1="12" x2="20" y2="12" strokeWidth={2} strokeLinecap="round" />
          <line x1="8" y1="18" x2="20" y2="18" strokeWidth={2} strokeLinecap="round" />
          <circle cx="4" cy="6" r="1.4" fill="currentColor" />
          <circle cx="4" cy="12" r="1.4" fill="currentColor" />
          <circle cx="4" cy="18" r="1.4" fill="currentColor" />
        </svg>
      </button>

      {open && (
        <div
          ref={popoverRef}
          className="toc-popover"
          role="dialog"
          aria-label="Table of contents"
          style={
            popoverPos
              ? { bottom: `${popoverPos.bottom}px`, right: `${popoverPos.right}px` }
              : undefined
          }
        >
          <div className="toc-popover-header">
            <span className="toc-popover-title">Jump to…</span>
            <span className="toc-popover-counts">
              {counts.user} user · {counts.eot} replies
              {counts.gen ? ` · ${counts.gen + 1} gens` : ""}
            </span>
          </div>
          <div className="toc-popover-list">
            {entries.map((entry) => (
              <button
                key={entry.id}
                className={`toc-entry toc-entry-${entry.kind}${activeId === entry.id ? " toc-entry-active" : ""}`}
                onClick={() => handleGoto(entry)}
              >
                <span className="toc-entry-icon" aria-hidden="true">
                  {entry.kind === "top" && "↑"}
                  {entry.kind === "bottom" && "↓"}
                  {entry.kind === "user" && "•"}
                  {entry.kind === "eot" && "✓"}
                  {entry.kind === "gen" && "✦"}
                </span>
                <span className="toc-entry-label">{entry.label}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </>
  );
};

export default ConversationTOC;
