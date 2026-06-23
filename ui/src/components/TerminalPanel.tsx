import React, { useEffect, useRef, useState, useCallback } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  ClipboardCopyIcon,
  ClipboardListIcon,
  CornerDownLeftIcon,
  TextCursorInputIcon,
  XIcon,
} from "lucide-react";
import { isDarkModeActive } from "../services/theme";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import "@xterm/xterm/css/xterm.css";

function base64ToUint8Array(base64String: string): Uint8Array {
  // @ts-expect-error Uint8Array.fromBase64 is a newer API
  if (Uint8Array.fromBase64) {
    // @ts-expect-error Uint8Array.fromBase64 is a newer API
    return Uint8Array.fromBase64(base64String);
  }
  const binaryString = atob(base64String);
  return Uint8Array.from(binaryString, (char) => char.charCodeAt(0));
}

export interface EphemeralTerminal {
  id: string;
  command: string;
  cwd: string;
  createdAt: Date;
  // termId is the server-side dtach session id. Set once the websocket reports
  // "attached". When reconnecting to a known session, set this up front so the
  // websocket re-attaches rather than spawning a new session.
  termId?: string;
}

interface TerminalPanelProps {
  terminals: EphemeralTerminal[];
  onClose: (id: string) => void;
  onInsertIntoInput?: (text: string) => void;
  autoFocusId?: string | null;
  onAutoFocusConsumed?: () => void;
  onActiveTerminalExited?: () => void;
  // onAttached fires when the server tells us which persistent session id this
  // terminal landed on. Callers can persist the id to survive reloads.
  onAttached?: (id: string, termId: string) => void;
  // Context surfaced to spawned sessions via SHELLEY_* env vars. Only used on
  // initial spawn; reattaches use the env baked in when the session was
  // created.
  conversationId?: string | null;
  model?: string | null;
}

// Theme colors for xterm.js
function getTerminalTheme(isDark: boolean): Record<string, string> {
  if (isDark) {
    return {
      background: "#1a1b26",
      foreground: "#c0caf5",
      cursor: "#c0caf5",
      cursorAccent: "#1a1b26",
      selectionBackground: "#364a82",
      selectionForeground: "#c0caf5",
      black: "#32344a",
      red: "#f7768e",
      green: "#9ece6a",
      yellow: "#e0af68",
      blue: "#7aa2f7",
      magenta: "#ad8ee6",
      cyan: "#449dab",
      white: "#9699a8",
      brightBlack: "#444b6a",
      brightRed: "#ff7a93",
      brightGreen: "#b9f27c",
      brightYellow: "#ff9e64",
      brightBlue: "#7da6ff",
      brightMagenta: "#bb9af7",
      brightCyan: "#0db9d7",
      brightWhite: "#acb0d0",
    };
  }
  return {
    background: "#f8f9fa",
    foreground: "#383a42",
    cursor: "#526eff",
    cursorAccent: "#f8f9fa",
    selectionBackground: "#bfceff",
    selectionForeground: "#383a42",
    black: "#383a42",
    red: "#e45649",
    green: "#50a14f",
    yellow: "#c18401",
    blue: "#4078f2",
    magenta: "#a626a4",
    cyan: "#0184bc",
    white: "#a0a1a7",
    brightBlack: "#4f525e",
    brightRed: "#e06c75",
    brightGreen: "#98c379",
    brightYellow: "#e5c07b",
    brightBlue: "#61afef",
    brightMagenta: "#c678dd",
    brightCyan: "#56b6c2",
    brightWhite: "#ffffff",
  };
}

type TermStatus = "connecting" | "running" | "exited" | "error";

function ActionButton({
  onClick,
  title,
  children,
  feedback,
}: {
  onClick: () => void;
  title: string;
  children: React.ReactNode;
  feedback?: boolean;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      onClick={onClick}
      title={title}
      className={cn(
        "rounded-md text-muted-foreground",
        feedback &&
          "bg-emerald-500/15 text-emerald-600 hover:bg-emerald-500/15 hover:text-emerald-600 dark:text-emerald-500",
      )}
    >
      {children}
    </Button>
  );
}

export default function TerminalPanel({
  terminals,
  onClose,
  onInsertIntoInput,
  autoFocusId,
  onAutoFocusConsumed,
  onActiveTerminalExited,
  onAttached,
  conversationId,
  model,
}: TerminalPanelProps) {
  const [activeTabId, setActiveTabId] = useState<string | null>(null);
  const [height, setHeight] = useState(300);
  const [minimized, setMinimized] = useState(false);
  const [copyFeedback, setCopyFeedback] = useState<string | null>(null);
  const [statusMap, setStatusMap] = useState<
    Map<string, { status: TermStatus; exitCode: number | null }>
  >(new Map());
  const isResizingRef = useRef(false);
  const startYRef = useRef(0);
  const startHeightRef = useRef(0);

  // Detect dark mode
  const [isDark, setIsDark] = useState(isDarkModeActive);

  useEffect(() => {
    const observer = new MutationObserver(() => {
      setIsDark(isDarkModeActive());
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    });
    return () => observer.disconnect();
  }, []);

  // Auto-select newest tab when a new terminal is added
  useEffect(() => {
    if (terminals.length > 0) {
      const lastTerminal = terminals[terminals.length - 1];
      setActiveTabId(lastTerminal.id);
      setMinimized(false); // expand when a new terminal arrives
    } else {
      setActiveTabId(null);
    }
  }, [terminals.length]);

  // If active tab got closed, switch to the last remaining
  useEffect(() => {
    if (activeTabId && !terminals.find((t) => t.id === activeTabId)) {
      if (terminals.length > 0) {
        setActiveTabId(terminals[terminals.length - 1].id);
      } else {
        setActiveTabId(null);
      }
    }
  }, [terminals, activeTabId]);

  const handleStatusChange = useCallback(
    (id: string, status: TermStatus, exitCode: number | null) => {
      setStatusMap((prev) => {
        const next = new Map(prev);
        const existing = next.get(id);
        // Don't overwrite exit status with ws.onclose
        if (existing && existing.status === "exited" && status === "exited") {
          return prev;
        }
        next.set(id, {
          status,
          exitCode: exitCode ?? existing?.exitCode ?? null,
        });
        return next;
      });
    },
    [],
  );

  // Resize drag
  const handleResizeMouseDown = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      isResizingRef.current = true;
      startYRef.current = e.clientY;
      startHeightRef.current = height;

      const handleMouseMove = (e: MouseEvent) => {
        if (!isResizingRef.current) return;
        // Dragging up increases height
        const delta = startYRef.current - e.clientY;
        setHeight(Math.max(80, Math.min(800, startHeightRef.current + delta)));
      };

      const handleMouseUp = () => {
        isResizingRef.current = false;
        document.removeEventListener("mousemove", handleMouseMove);
        document.removeEventListener("mouseup", handleMouseUp);
      };

      document.addEventListener("mousemove", handleMouseMove);
      document.addEventListener("mouseup", handleMouseUp);
    },
    [height],
  );

  const showFeedback = useCallback((type: string) => {
    setCopyFeedback(type);
    setTimeout(() => setCopyFeedback(null), 1500);
  }, []);

  // Get the xterm instance for the active tab
  const xtermRegistryRef = useRef<Map<string, Terminal>>(new Map());

  const registerXterm = useCallback((id: string, xterm: Terminal) => {
    xtermRegistryRef.current.set(id, xterm);
  }, []);

  const unregisterXterm = useCallback((id: string) => {
    xtermRegistryRef.current.delete(id);
  }, []);

  // Auto-focus terminal when autoFocusId is set (e.g., for interactive shells)
  useEffect(() => {
    if (!autoFocusId) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    let attempt = 0;
    const tryFocus = () => {
      if (cancelled) return;
      const xterm = xtermRegistryRef.current.get(autoFocusId);
      if (xterm) {
        setActiveTabId(autoFocusId);
        setMinimized(false); // expand when focusing a terminal
        // Double-rAF to ensure we're past any keyup/form events that might steal focus
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            xterm.focus();
          });
        });
        onAutoFocusConsumed?.();
        return;
      }
      if (++attempt < 10) {
        timer = setTimeout(tryFocus, 50);
      }
    };
    // Small initial delay to let the form submit / keyup events settle
    timer = setTimeout(tryFocus, 50);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [autoFocusId, onAutoFocusConsumed]);

  // Restore focus to message input when the active terminal exits
  const prevActiveStatusRef = useRef<{ tabId: string | null; status: TermStatus | undefined }>({
    tabId: null,
    status: undefined,
  });
  useEffect(() => {
    if (!activeTabId || !onActiveTerminalExited) return;
    const info = statusMap.get(activeTabId);
    const prev = prevActiveStatusRef.current;
    // Only trigger on status transition within the same tab
    const wasRunning = prev.tabId === activeTabId && prev.status === "running";
    prevActiveStatusRef.current = { tabId: activeTabId, status: info?.status };
    if (wasRunning && (info?.status === "exited" || info?.status === "error")) {
      onActiveTerminalExited();
    }
  }, [activeTabId, statusMap, onActiveTerminalExited]);

  const getBufferText = useCallback(
    (mode: "screen" | "all"): string => {
      if (!activeTabId) return "";
      const xterm = xtermRegistryRef.current.get(activeTabId);
      if (!xterm) return "";

      const lines: string[] = [];
      const buffer = xterm.buffer.active;

      if (mode === "screen") {
        const startRow = buffer.viewportY;
        for (let i = 0; i < xterm.rows; i++) {
          const line = buffer.getLine(startRow + i);
          if (line) lines.push(line.translateToString(true));
        }
      } else {
        for (let i = 0; i < buffer.length; i++) {
          const line = buffer.getLine(i);
          if (line) lines.push(line.translateToString(true));
        }
      }
      return lines.join("\n").trimEnd();
    },
    [activeTabId],
  );

  const copyScreen = useCallback(() => {
    navigator.clipboard.writeText(getBufferText("screen"));
    showFeedback("copyScreen");
  }, [getBufferText, showFeedback]);

  const copyAll = useCallback(() => {
    navigator.clipboard.writeText(getBufferText("all"));
    showFeedback("copyAll");
  }, [getBufferText, showFeedback]);

  const insertScreen = useCallback(() => {
    if (onInsertIntoInput) {
      onInsertIntoInput(getBufferText("screen"));
      showFeedback("insertScreen");
    }
  }, [getBufferText, onInsertIntoInput, showFeedback]);

  const insertAll = useCallback(() => {
    if (onInsertIntoInput) {
      onInsertIntoInput(getBufferText("all"));
      showFeedback("insertAll");
    }
  }, [getBufferText, onInsertIntoInput, showFeedback]);

  const handleCloseActive = useCallback(() => {
    if (activeTabId) onClose(activeTabId);
  }, [activeTabId, onClose]);

  const toggleMinimized = useCallback(() => {
    setMinimized((prev) => !prev);
  }, []);

  // Refit terminals when un-minimizing by nudging the container to trigger ResizeObserver
  const wasMinimizedRef = useRef(minimized);
  useEffect(() => {
    const wasMinimized = wasMinimizedRef.current;
    wasMinimizedRef.current = minimized;
    if (wasMinimized && !minimized && activeTabId) {
      const timer = setTimeout(() => {
        const el = document.querySelector(`[data-terminal-id="${activeTabId}"]`);
        if (el) {
          (el as HTMLElement).style.height = "99.9%";
          requestAnimationFrame(() => {
            (el as HTMLElement).style.height = "100%";
          });
        }
      }, 30);
      return () => clearTimeout(timer);
    }
  }, [minimized, activeTabId]);

  if (terminals.length === 0) return null;

  // Truncate command for tab label
  const tabLabel = (cmd: string) => {
    // Show first word or first 30 chars
    const firstWord = cmd.split(/\s+/)[0];
    if (firstWord.length > 30) return firstWord.substring(0, 27) + "...";
    return firstWord;
  };

  return (
    <div
      className={cn(
        // KEEP "terminal-panel": retained island CSS targets it as an ancestor
        // for the mobile soft-keyboard fix (.terminal-panel .xterm ...).
        "terminal-panel flex flex-col overflow-hidden border-t border-border bg-secondary",
        minimized ? "shrink-0" : "max-h-[800px] min-h-[80px]",
      )}
      style={minimized ? undefined : { height: `${height}px`, flexShrink: 0 }}
    >
      {/* Resize handle at top — hidden when minimized */}
      {!minimized && (
        <div
          className="flex h-1.5 shrink-0 cursor-ns-resize items-center justify-center border-b border-border bg-secondary select-none hover:bg-muted"
          onMouseDown={handleResizeMouseDown}
        >
          <div className="h-[3px] w-10 rounded-full bg-muted-foreground/40" />
        </div>
      )}

      {/* Tab bar + actions */}
      <div
        className={cn(
          "flex h-[34px] shrink-0 items-center gap-2 bg-secondary px-2",
          !minimized && "border-b border-border",
        )}
      >
        {/* Minimize/maximize toggle */}
        <ActionButton
          onClick={toggleMinimized}
          title={minimized ? "Expand terminals" : "Minimize terminals"}
        >
          {minimized ? <ChevronUpIcon /> : <ChevronDownIcon />}
        </ActionButton>

        <div className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {terminals.map((t) => {
            const info = statusMap.get(t.id);
            const isActive = t.id === activeTabId;
            return (
              <div
                key={t.id}
                className={cn(
                  "flex cursor-pointer items-center gap-1 rounded-md border border-transparent px-2 py-1 font-mono text-xs whitespace-nowrap text-muted-foreground transition-colors select-none",
                  isActive
                    ? "border-border bg-background text-foreground"
                    : "hover:bg-muted hover:text-foreground",
                )}
                onClick={() => {
                  setActiveTabId(t.id);
                  if (minimized) setMinimized(false);
                }}
                title={t.command}
              >
                {info?.status === "running" && (
                  <span className="text-xs leading-none text-emerald-600 dark:text-emerald-500">
                    ●
                  </span>
                )}
                {info?.status === "exited" && info.exitCode === 0 && (
                  <span className="text-xs leading-none text-emerald-600 dark:text-emerald-500">
                    ✓
                  </span>
                )}
                {info?.status === "exited" && info.exitCode !== 0 && (
                  <span className="text-xs leading-none text-destructive">✗</span>
                )}
                {info?.status === "error" && (
                  <span className="text-xs leading-none text-destructive">✗</span>
                )}
                <span className="max-w-[120px] overflow-hidden text-ellipsis">
                  {tabLabel(t.command)}
                </span>
                <button
                  className="flex size-4 items-center justify-center rounded-sm p-0 text-sm leading-none text-muted-foreground hover:bg-muted hover:text-destructive"
                  onClick={(e) => {
                    e.stopPropagation();
                    onClose(t.id);
                  }}
                  title="Close terminal"
                >
                  ×
                </button>
              </div>
            );
          })}
        </div>

        {/* Action buttons — hidden when minimized */}
        {!minimized && (
          <div className="flex shrink-0 items-center gap-0.5 rounded-md border border-border bg-background p-0.5">
            <ActionButton
              onClick={copyScreen}
              title="Copy visible screen"
              feedback={copyFeedback === "copyScreen"}
            >
              {copyFeedback === "copyScreen" ? <CheckIcon /> : <ClipboardCopyIcon />}
            </ActionButton>
            <ActionButton
              onClick={copyAll}
              title="Copy all output"
              feedback={copyFeedback === "copyAll"}
            >
              {copyFeedback === "copyAll" ? <CheckIcon /> : <ClipboardListIcon />}
            </ActionButton>
            {onInsertIntoInput && (
              <>
                <ActionButton
                  onClick={insertScreen}
                  title="Insert visible screen into input"
                  feedback={copyFeedback === "insertScreen"}
                >
                  {copyFeedback === "insertScreen" ? <CheckIcon /> : <TextCursorInputIcon />}
                </ActionButton>
                <ActionButton
                  onClick={insertAll}
                  title="Insert all output into input"
                  feedback={copyFeedback === "insertAll"}
                >
                  {copyFeedback === "insertAll" ? <CheckIcon /> : <CornerDownLeftIcon />}
                </ActionButton>
              </>
            )}
            <div className="mx-0.5 h-4 w-px bg-border" />
            <ActionButton onClick={handleCloseActive} title="Close active terminal">
              <XIcon />
            </ActionButton>
          </div>
        )}
      </div>

      {/* Terminal content area — hidden (not unmounted) when minimized */}
      <div
        className="relative min-h-0 flex-1 overflow-hidden"
        style={minimized ? { display: "none" } : undefined}
      >
        {terminals.map((t) => (
          <TerminalInstanceWithRegistry
            key={t.id}
            term={t}
            isVisible={t.id === activeTabId}
            isDark={isDark}
            onStatusChange={handleStatusChange}
            onRegister={registerXterm}
            onUnregister={unregisterXterm}
            onAttached={onAttached}
            conversationId={conversationId ?? null}
            model={model ?? null}
          />
        ))}
      </div>
    </div>
  );
}

// Wrapper that also registers the xterm instance
function TerminalInstanceWithRegistry({
  term,
  isVisible,
  isDark,
  onStatusChange,
  onRegister,
  onUnregister,
  onAttached,
  conversationId,
  model,
}: {
  term: EphemeralTerminal;
  isVisible: boolean;
  isDark: boolean;
  onStatusChange: (id: string, status: TermStatus, exitCode: number | null) => void;
  onRegister: (id: string, xterm: Terminal) => void;
  onUnregister: (id: string) => void;
  onAttached?: (id: string, termId: string) => void;
  conversationId?: string | null;
  model?: string | null;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const xterm = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
      theme: getTerminalTheme(isDark),
      scrollback: 10000,
      // Kitty keyboard protocol — clients opt in via `CSI = u` so this is safe to leave on.
      vtExtensions: { kittyKeyboard: true },
    });
    xtermRef.current = xterm;

    // Ensure control key combinations (like Ctrl-B for tmux) are passed
    // through to the terminal and not intercepted by the browser.
    xterm.attachCustomKeyEventHandler((e: KeyboardEvent) => {
      // Allow Ctrl+Shift+C / Ctrl+Shift+V for copy/paste
      if (e.ctrlKey && e.shiftKey && (e.key === "C" || e.key === "V")) {
        return false; // Let browser handle it
      }
      // For all Ctrl+<key> combos (e.g. Ctrl-B for tmux prefix),
      // prevent the browser default and let xterm handle it.
      if (e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey && e.type === "keydown") {
        e.preventDefault();
        return true; // Let xterm process it
      }
      return true;
    });

    const fitAddon = new FitAddon();
    fitAddonRef.current = fitAddon;
    xterm.loadAddon(fitAddon);
    xterm.loadAddon(new WebLinksAddon());

    xterm.open(containerRef.current);
    fitAddon.fit();
    onRegister(term.id, xterm);

    // Mobile soft-keyboard fix: on touch devices the xterm helper textarea
    // can't be focused by tapping (it has pointer-events: none so the
    // viewport remains scrollable). Listen for pointerdown inside the
    // terminal area and focus xterm programmatically — this happens inside
    // a user gesture, which is what iOS/Android require to open the keyboard.
    const handlePointerDown = (e: PointerEvent) => {
      // Only handle touch — pen/stylus shouldn't auto-summon the OSK, and
      // mouse already focuses xterm through its own handlers.
      if (e.pointerType !== "touch") return;
      xterm.focus();
    };
    containerRef.current.addEventListener("pointerdown", handlePointerDown);

    // Show the command as a banner so users can see and copy/paste what they
    // ran. Written client-side on every attach (the xterm buffer is fresh on
    // each mount, so there's no duplication).
    xterm.write(`\x1b[2m$ ${term.command}\x1b[0m\r\n`);

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    // If we already have a persistent session id, reattach to it. Otherwise
    // spawn a new one by sending cmd+cwd.
    const params = new URLSearchParams();
    if (term.termId) {
      params.set("term_id", term.termId);
    }
    params.set("cmd", term.command);
    params.set("cwd", term.cwd);
    if (conversationId) params.set("conversation_id", conversationId);
    if (model) params.set("model", model);
    const wsUrl = `${protocol}//${window.location.host}/api/exec-ws?${params.toString()}`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: "init", cols: xterm.cols, rows: xterm.rows }));
      onStatusChange(term.id, "running", null);
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === "output" && msg.data) {
          xterm.write(base64ToUint8Array(msg.data));
        } else if (msg.type === "attached" && msg.term_id) {
          onAttached?.(term.id, msg.term_id);
        } else if (msg.type === "exit") {
          const code = parseInt(msg.data, 10) || 0;
          const color = code === 0 ? "32" : "31";
          xterm.write(
            `\r\n\x1b[2;${color}m${term.command} completed with exit code ${code}\x1b[0m\r\n`,
          );
          onStatusChange(term.id, "exited", code);
        } else if (msg.type === "error") {
          xterm.write(`\r\n\x1b[31mError: ${msg.data}\x1b[0m\r\n`);
          onStatusChange(term.id, "error", null);
        }
      } catch (err) {
        console.error("Failed to parse terminal message:", err);
      }
    };

    ws.onerror = (event) => console.error("WebSocket error:", event);
    ws.onclose = () => {
      onStatusChange(term.id, "exited", null);
    };

    xterm.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data }));
      }
    });

    const ro = new ResizeObserver(() => {
      if (!fitAddonRef.current) return;
      fitAddonRef.current.fit();
      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN && xtermRef.current) {
        wsRef.current.send(
          JSON.stringify({
            type: "resize",
            cols: xtermRef.current.cols,
            rows: xtermRef.current.rows,
          }),
        );
      }
    });
    ro.observe(containerRef.current);

    const container = containerRef.current;
    return () => {
      ro.disconnect();
      container?.removeEventListener("pointerdown", handlePointerDown);
      ws.close();
      xterm.dispose();
      onUnregister(term.id);
    };
  }, [term.id, term.command, term.cwd]);

  // Update theme
  useEffect(() => {
    if (xtermRef.current) {
      xtermRef.current.options.theme = getTerminalTheme(isDark);
    }
  }, [isDark]);

  // Refit when visibility changes
  useEffect(() => {
    if (isVisible && fitAddonRef.current) {
      setTimeout(() => fitAddonRef.current?.fit(), 20);
    }
  }, [isVisible]);

  return (
    <div
      ref={containerRef}
      data-terminal-id={term.id}
      className="absolute inset-1"
      style={{
        width: "100%",
        height: "100%",
        display: isVisible ? "block" : "none",
        backgroundColor: isDark ? "#1a1b26" : "#f8f9fa",
      }}
    />
  );
}
