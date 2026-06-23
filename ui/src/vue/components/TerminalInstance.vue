<!-- Vue port of the TerminalInstanceWithRegistry inner component of
     components/TerminalPanel.tsx. Owns a single xterm.js instance + its
     dtach-backed websocket. The Terminal is created imperatively in
     onMounted on the container template ref and disposed in onUnmounted
     (mirrors the React effect + cleanup). The xterm instance is surfaced to
     the parent via the "register"/"unregister" emits (the React
     onRegister/onUnregister callbacks). -->
<template>
  <div
    ref="containerRef"
    :data-terminal-id="term.id"
    :style="{
      width: '100%',
      height: '100%',
      display: isVisible ? 'block' : 'none',
      backgroundColor: isDark ? '#1a1b26' : '#f8f9fa',
    }"
  />
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import type { EphemeralTerminal } from "./terminalTypes";
import { getTerminalTheme, base64ToUint8Array, type TermStatus } from "./terminalHelpers";

const props = defineProps<{
  term: EphemeralTerminal;
  isVisible: boolean;
  isDark: boolean;
  conversationId?: string | null;
  model?: string | null;
}>();

const emit = defineEmits<{
  // status-change: id, status, exitCode (React onStatusChange)
  (e: "status-change", id: string, status: TermStatus, exitCode: number | null): void;
  // register/unregister: id, xterm instance (React onRegister/onUnregister)
  (e: "register", id: string, xterm: Terminal): void;
  (e: "unregister", id: string): void;
  // attached: id, termId (React onAttached)
  (e: "attached", id: string, termId: string): void;
}>();

const containerRef = ref<HTMLDivElement | null>(null);
let xtermInst: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let ws: WebSocket | null = null;
let ro: ResizeObserver | null = null;
let handlePointerDown: ((e: PointerEvent) => void) | null = null;

onMounted(() => {
  if (!containerRef.value) return;

  const xterm = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
    theme: getTerminalTheme(props.isDark),
    scrollback: 10000,
    // Kitty keyboard protocol — clients opt in via `CSI = u` so this is safe to leave on.
    vtExtensions: { kittyKeyboard: true },
  } as ConstructorParameters<typeof Terminal>[0]);
  xtermInst = xterm;

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

  fitAddon = new FitAddon();
  xterm.loadAddon(fitAddon);
  xterm.loadAddon(new WebLinksAddon());

  xterm.open(containerRef.value);
  fitAddon.fit();
  emit("register", props.term.id, xterm);

  // Mobile soft-keyboard fix: on touch devices the xterm helper textarea
  // can't be focused by tapping (it has pointer-events: none so the
  // viewport remains scrollable). Listen for pointerdown inside the
  // terminal area and focus xterm programmatically — this happens inside
  // a user gesture, which is what iOS/Android require to open the keyboard.
  handlePointerDown = (e: PointerEvent) => {
    // Only handle touch — pen/stylus shouldn't auto-summon the OSK, and
    // mouse already focuses xterm through its own handlers.
    if (e.pointerType !== "touch") return;
    xterm.focus();
  };
  containerRef.value.addEventListener("pointerdown", handlePointerDown);

  // Show the command as a banner so users can see and copy/paste what they
  // ran. Written client-side on every attach (the xterm buffer is fresh on
  // each mount, so there's no duplication).
  xterm.write(`\x1b[2m$ ${props.term.command}\x1b[0m\r\n`);

  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  // If we already have a persistent session id, reattach to it. Otherwise
  // spawn a new one by sending cmd+cwd.
  const params = new URLSearchParams();
  if (props.term.termId) {
    params.set("term_id", props.term.termId);
  }
  params.set("cmd", props.term.command);
  params.set("cwd", props.term.cwd);
  if (props.conversationId) params.set("conversation_id", props.conversationId);
  if (props.model) params.set("model", props.model);
  const wsUrl = `${protocol}//${window.location.host}/api/exec-ws?${params.toString()}`;
  ws = new WebSocket(wsUrl);
  const socket = ws;

  socket.onopen = () => {
    socket.send(JSON.stringify({ type: "init", cols: xterm.cols, rows: xterm.rows }));
    emit("status-change", props.term.id, "running", null);
  };

  socket.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      if (msg.type === "output" && msg.data) {
        xterm.write(base64ToUint8Array(msg.data));
      } else if (msg.type === "attached" && msg.term_id) {
        emit("attached", props.term.id, msg.term_id);
      } else if (msg.type === "exit") {
        const code = parseInt(msg.data, 10) || 0;
        const color = code === 0 ? "32" : "31";
        xterm.write(
          `\r\n\x1b[2;${color}m${props.term.command} completed with exit code ${code}\x1b[0m\r\n`,
        );
        emit("status-change", props.term.id, "exited", code);
      } else if (msg.type === "error") {
        xterm.write(`\r\n\x1b[31mError: ${msg.data}\x1b[0m\r\n`);
        emit("status-change", props.term.id, "error", null);
      }
    } catch (err) {
      console.error("Failed to parse terminal message:", err);
    }
  };

  socket.onerror = (event) => console.error("WebSocket error:", event);
  socket.onclose = () => {
    emit("status-change", props.term.id, "exited", null);
  };

  xterm.onData((data) => {
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "input", data }));
    }
  });

  ro = new ResizeObserver(() => {
    if (!fitAddon) return;
    fitAddon.fit();
    if (socket.readyState === WebSocket.OPEN && xtermInst) {
      socket.send(
        JSON.stringify({
          type: "resize",
          cols: xtermInst.cols,
          rows: xtermInst.rows,
        }),
      );
    }
  });
  ro.observe(containerRef.value);
});

onUnmounted(() => {
  ro?.disconnect();
  if (handlePointerDown && containerRef.value) {
    containerRef.value.removeEventListener("pointerdown", handlePointerDown);
  }
  ws?.close();
  xtermInst?.dispose();
  emit("unregister", props.term.id);
});

// Update theme (React effect on [isDark]).
watch(
  () => props.isDark,
  (dark) => {
    if (xtermInst) {
      xtermInst.options.theme = getTerminalTheme(dark);
    }
  },
);

// Refit when visibility changes (React effect on [isVisible]).
watch(
  () => props.isVisible,
  (visible) => {
    if (visible && fitAddon) {
      setTimeout(() => fitAddon?.fit(), 20);
    }
  },
);
</script>
