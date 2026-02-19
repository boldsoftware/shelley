import React, { useEffect, useRef, useState, useCallback } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { copyToClipboard } from "../utils/clipboard";
import { isDarkModeActive } from "../services/theme";
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
}

interface TerminalPanelProps {
  terminals: EphemeralTerminal[];
  onClose: (id: string) => void;
  onInsertIntoInput?: (text: string) => void;
  autoFocusId?: string | null;
  onAutoFocusConsumed?: () => void;
  onActiveTerminalExited?: () => void;
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

// SVG icons
const CopyIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
  </svg>
);

const CopyAllIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
    <line x1="12" y1="17" x2="18" y2="17" />
  </svg>
);

const InsertIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <path d="M12 3v12" />
    <path d="m8 11 4 4 4-4" />
    <path d="M4 21h16" />
  </svg>
);

const InsertAllIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <path d="M12 3v12" />
    <path d="m8 11 4 4 4-4" />
    <path d="M4 21h16" />
    <line x1="4" y1="18" x2="20" y2="18" />
  </svg>
);

const CheckIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <polyline points="20 6 9 17 4 12" />
  </svg>
);

const CloseIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <line x1="18" y1="6" x2="6" y2="18" />
    <line x1="6" y1="6" x2="18" y2="18" />
  </svg>
);

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
    <button
      onClick={onClick}
      title={title}
      className={`terminal-panel-action-btn${feedback ? " terminal-panel-action-btn-feedback" : ""}`}
    >
      {children}
    </button>
  );
}

export default function TerminalPanel({
  terminals,
  onClose,
  onInsertIntoInput,
  autoFocusId,
  onAutoFocusConsumed,
  onActiveTerminalExited,
}: TerminalPanelProps) {
  const [activeTabId, setActiveTabId] = useState<string | null>(null);
  const [height, setHeight] = useState(300);
  const [heightLocked, setHeightLocked] = useState(false);
  const isFirstTerminalRef = useRef(true);
  const [copyFeedback, setCopyFeedback] = useState<string | null>(null);
  const [statusMap, setStatusMap] = useState<
    Map<string, { status: TermStatus; exitCode: number | null; contentLines: number }>
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
    (id: string, status: TermStatus, exitCode: number | null, contentLines: number) => {
      setStatusMap((prev) => {
        const next = new Map(prev);
        const existing = next.get(id);
        // Don't overwrite exit status with ws.onclose
        if (
          existing &&
          existing.status === "exited" &&
          status === "exited" &&
          contentLines === -1
        ) {
          return prev;
        }
        const lines = contentLines === -1 ? existing?.contentLines || 0 : contentLines;
        next.set(id, {
          status,
          exitCode: exitCode ?? existing?.exitCode ?? null,
          contentLines: lines,
        });
        return next;
      });
    },
    [],
  );

  // Auto-size only for the very first terminal. After that, keep whatever height we have.
  useEffect(() => {
    if (heightLocked || !activeTabId) return;
    if (!isFirstTerminalRef.current) return;
    const info = statusMap.get(activeTabId);
    if (!info) return;

    const cellHeight = 17; // approximate
    const minHeight = 60;
    const maxHeight = 500;
    const tabBarHeight = 38;

    if (info.status === "exited" || info.status === "error") {
      const needed = Math.min(
        maxHeight,
        Math.max(minHeight, info.contentLines * cellHeight + tabBarHeight + 16),
      );
      setHeight(needed);
      setHeightLocked(true);
      isFirstTerminalRef.current = false;
    } else if (info.status === "running") {
      // While the first command is still running, grow if needed
      const needed = Math.min(
        maxHeight,
        Math.max(minHeight, info.contentLines * cellHeight + tabBarHeight + 16),
      );
      setHeight((prev) => Math.max(prev, needed));
    }
  }, [statusMap, activeTabId, heightLocked]);

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
        setHeightLocked(true);
        isFirstTerminalRef.current = false;
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
    copyToClipboard(getBufferText("screen"));
    showFeedback("copyScreen");
  }, [getBufferText, showFeedback]);

  const copyAll = useCallback(() => {
    copyToClipboard(getBufferText("all"));
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

  if (terminals.length === 0) return null;

  // Truncate command for tab label
  const tabLabel = (cmd: string) => {
    // Show first word or first 30 chars
    const firstWord = cmd.split(/\s+/)[0];
    if (firstWord.length > 30) return firstWord.substring(0, 27) + "...";
    return firstWord;
  };

  return (
    <div className="terminal-panel" style={{ height: `${height}px`, flexShrink: 0 }}>
      {/* Resize handle at top */}
      <div className="terminal-panel-resize-handle" onMouseDown={handleResizeMouseDown}>
        <div className="terminal-panel-resize-grip" />
      </div>

      {/* Tab bar + actions */}
      <div className="terminal-panel-header">
        <div className="terminal-panel-tabs">
          {terminals.map((t) => {
            const info = statusMap.get(t.id);
            const isActive = t.id === activeTabId;
            return (
              <div
                key={t.id}
                className={`terminal-panel-tab${isActive ? " terminal-panel-tab-active" : ""}`}
                onClick={() => setActiveTabId(t.id)}
                title={t.command}
              >
                {info?.status === "running" && (
                  <span className="terminal-panel-tab-indicator terminal-panel-tab-running">●</span>
                )}
                {info?.status === "exited" && info.exitCode === 0 && (
                  <span className="terminal-panel-tab-indicator terminal-panel-tab-success">✓</span>
                )}
                {info?.status === "exited" && info.exitCode !== 0 && (
                  <span className="terminal-panel-tab-indicator terminal-panel-tab-error">✗</span>
                )}
                {info?.status === "error" && (
                  <span className="terminal-panel-tab-indicator terminal-panel-tab-error">✗</span>
                )}
                <span className="terminal-panel-tab-label">{tabLabel(t.command)}</span>
                <button
                  className="terminal-panel-tab-close"
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

        {/* Action buttons */}
        <div className="terminal-panel-actions">
          <ActionButton
            onClick={copyScreen}
            title="Copy visible screen"
            feedback={copyFeedback === "copyScreen"}
          >
            {copyFeedback === "copyScreen" ? <CheckIcon /> : <CopyIcon />}
          </ActionButton>
          <ActionButton
            onClick={copyAll}
            title="Copy all output"
            feedback={copyFeedback === "copyAll"}
          >
            {copyFeedback === "copyAll" ? <CheckIcon /> : <CopyAllIcon />}
          </ActionButton>
          {onInsertIntoInput && (
            <>
              <ActionButton
                onClick={insertScreen}
                title="Insert visible screen into input"
                feedback={copyFeedback === "insertScreen"}
              >
                {copyFeedback === "insertScreen" ? <CheckIcon /> : <InsertIcon />}
              </ActionButton>
              <ActionButton
                onClick={insertAll}
                title="Insert all output into input"
                feedback={copyFeedback === "insertAll"}
              >
                {copyFeedback === "insertAll" ? <CheckIcon /> : <InsertAllIcon />}
              </ActionButton>
            </>
          )}
          <div className="terminal-panel-actions-divider" />
          <ActionButton onClick={handleCloseActive} title="Close active terminal">
            <CloseIcon />
          </ActionButton>
        </div>
      </div>

      {/* Terminal content area */}
      <div className="terminal-panel-content">
        {terminals.map((t) => (
          <TerminalInstanceWithRegistry
            key={t.id}
            term={t}
            isVisible={t.id === activeTabId}
            isDark={isDark}
            onStatusChange={handleStatusChange}
            onRegister={registerXterm}
            onUnregister={unregisterXterm}
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
}: {
  term: EphemeralTerminal;
  isVisible: boolean;
  isDark: boolean;
  onStatusChange: (
    id: string,
    status: TermStatus,
    exitCode: number | null,
    contentLines: number,
  ) => void;
  onRegister: (id: string, xterm: Terminal) => void;
  onUnregister: (id: string) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const xterm = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
      theme: getTerminalTheme(isDark),
      scrollback: 10000,
    });
    xtermRef.current = xterm;

    const fitAddon = new FitAddon();
    fitAddonRef.current = fitAddon;
    xterm.loadAddon(fitAddon);
    xterm.loadAddon(new WebLinksAddon());

    xterm.open(containerRef.current);
    fitAddon.fit();
    onRegister(term.id, xterm);

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const wsUrl = `${protocol}//${window.location.host}/api/exec-ws?cmd=${encodeURIComponent(term.command)}&cwd=${encodeURIComponent(term.cwd)}`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: "init", cols: xterm.cols, rows: xterm.rows }));
      onStatusChange(term.id, "running", null, 0);
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === "output" && msg.data) {
          xterm.write(base64ToUint8Array(msg.data));
          const buf = xterm.buffer.active;
          let lines = 0;
          for (let i = buf.length - 1; i >= 0; i--) {
            const line = buf.getLine(i);
            if (line && line.translateToString(true).trim()) {
              lines = i + 1;
              break;
            }
          }
          onStatusChange(term.id, "running", null, lines);
        } else if (msg.type === "exit") {
          const code = parseInt(msg.data, 10) || 0;
          const buf = xterm.buffer.active;
          let lines = 0;
          for (let i = buf.length - 1; i >= 0; i--) {
            const line = buf.getLine(i);
            if (line && line.translateToString(true).trim()) {
              lines = i + 1;
              break;
            }
          }
          onStatusChange(term.id, "exited", code, lines);
        } else if (msg.type === "error") {
          xterm.write(`\r\n\x1b[31mError: ${msg.data}\x1b[0m\r\n`);
          onStatusChange(term.id, "error", null, 0);
        }
      } catch (err) {
        console.error("Failed to parse terminal message:", err);
      }
    };

    ws.onerror = (event) => console.error("WebSocket error:", event);
    ws.onclose = () => {
      onStatusChange(term.id, "exited", null, -1);
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

    return () => {
      ro.disconnect();
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
      style={{
        width: "100%",
        height: "100%",
        display: isVisible ? "block" : "none",
        backgroundColor: isDark ? "#1a1b26" : "#f8f9fa",
      }}
    />
  );
}
