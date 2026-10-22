<!-- One terminal of the terminal panel: a ghostty-web or xterm.js view (see
     terminalBackend.ts) and the websocket to its server-side session. The
     view is surfaced to the parent via the "register"/"unregister" emits. -->
<template>
  <div
    ref="containerRef"
    :data-terminal-id="term.id"
    :style="{
      display: isVisible ? 'block' : 'none',
      backgroundColor: isDark ? '#1a1b26' : '#f8f9fa',
    }"
  >
    <p v-if="openError" class="terminal-instance-error">{{ openError }}</p>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import type { EphemeralTerminal } from "./terminalTypes";
import { base64ToUint8Array, type TermStatus } from "./terminalHelpers";
import {
  openTerminalView,
  type TerminalHandle,
  type TerminalRenderer,
  type TerminalView,
} from "./terminalBackend";
import { cleanTerminalTitle, TerminalTitleParser } from "./terminalTitle";

const props = defineProps<{
  term: EphemeralTerminal;
  isVisible: boolean;
  isDark: boolean;
  renderer: TerminalRenderer;
  model?: string | null;
}>();

const emit = defineEmits<{
  // status-change: id, status, exitCode (React onStatusChange)
  (e: "status-change", id: string, status: TermStatus, exitCode: number | null): void;
  // register/unregister: id, the terminal, and its fit callback
  (e: "register", id: string, handle: TerminalHandle, fit: () => void): void;
  (e: "unregister", id: string): void;
  // attached: id, termId (null without a persistent session), exit policy
  (e: "attached", id: string, termId: string | null, closeOnExit: boolean): void;
  // title-change: id, the window title the program set (null when cleared)
  (e: "title-change", id: string, title: string | null): void;
}>();

const containerRef = ref<HTMLDivElement | null>(null);
// Why the terminal couldn't be shown, when it couldn't.
const openError = ref<string | null>(null);
let view: TerminalView | null = null;
// What the view was opened with (or failed to open with), and whether it
// has since switched to dark colors.
let viewRenderer = props.renderer;
let viewDark = props.isDark;
// What the panel holds: whichever view is current, across reopens.
const handle: TerminalHandle = {
  focus: () => view?.focus(),
  lines: (which) => view?.lines(which) ?? [],
};
let registered = false;
let ws: WebSocket | null = null;
let ro: ResizeObserver | null = null;
// Bumped by every connect() and on unmount, so a slow connect that has been
// superseded throws its view away.
let generation = 0;

// A reattach replays the screen but not the window title, so the last one
// seen is kept per session for this browser tab.
const titleKey = (termId: string) => `shelley-terminal-title:${termId}`;
let sessionId = props.term.termId;
const titleParser = new TerminalTitleParser();
function setTitle(raw: string) {
  const title = cleanTerminalTitle(raw);
  emit("title-change", props.term.id, title);
  if (!sessionId) return;
  if (title === null) sessionStorage.removeItem(titleKey(sessionId));
  else sessionStorage.setItem(titleKey(sessionId), title);
}

// settled is set once the server reported a definitive outcome (exit or
// error) for this session, so a later socket close is not mistaken for one.
let settled = false;

function fitAndNotifyServer() {
  if (!view) return;
  view.fit();
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: "resize", cols: view.cols, rows: view.rows }));
  }
}

onMounted(async () => {
  if (!containerRef.value) return;
  if (sessionId) {
    const saved = sessionStorage.getItem(titleKey(sessionId));
    if (saved !== null) emit("title-change", props.term.id, saved);
  }
  ro = new ResizeObserver(fitAndNotifyServer);
  ro.observe(containerRef.value);
  // An unpersisted terminal's command ended with the websocket of the
  // instance that ran it; connecting again would run it a second time.
  if (props.term.unpersisted) {
    openError.value = "This terminal ended when its connection closed.";
    settled = true;
    emit("status-change", props.term.id, "exited", null);
    return;
  }
  await connect();
});

// Open a view and a websocket to the session: a new one the first time,
// otherwise the one this terminal already has. `size` keeps a reopened
// terminal's grid while it's hidden and can't be fitted.
async function connect(size?: { cols: number; rows: number }, focus = false) {
  const gen = ++generation;
  const { renderer, isDark: dark } = props;
  viewRenderer = renderer;
  let opened: TerminalView;
  try {
    opened = await openTerminalView(renderer, containerRef.value!, {
      isDark: dark,
      ...size,
      onData: (data) => {
        if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "input", data }));
      },
      onCellSizeChange: fitAndNotifyServer,
    });
  } catch (err) {
    if (gen !== generation) return;
    // Another renderer was chosen while this one loaded; it may work.
    if (renderer !== props.renderer) return connect(size, focus);
    console.error("terminal: failed to open", err);
    openError.value = `Couldn't open the terminal: ${err instanceof Error ? err.message : err}`;
    settled = true;
    emit("status-change", props.term.id, "error", null);
    return;
  }
  if (gen !== generation) {
    opened.dispose();
    return;
  }
  // The renderer or theme changed while opening. Nothing has been written
  // yet, so a fresh view loses nothing.
  if (renderer !== props.renderer || (dark !== props.isDark && !opened.setTheme(props.isDark))) {
    opened.dispose();
    return connect(size, focus);
  }
  const v = opened;
  view = v;
  viewDark = props.isDark;
  if (!registered) {
    registered = true;
    emit("register", props.term.id, handle, fitAndNotifyServer);
  }
  v.fit();
  if (focus) v.focus();

  // Show the command as a banner so users can see and copy/paste what they
  // ran. Written client-side on every attach (the buffer is fresh on each
  // connect, so there's no duplication).
  v.write(`\x1b[2m$ ${props.term.command}\x1b[0m\r\n`);

  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  // If we already have a persistent session id, reattach to it and nothing
  // else: sending cmd as well would let the server silently rerun the command
  // when the session has died. Otherwise spawn a new one with cmd+cwd.
  //
  // conversation_id comes from the terminal itself, not from whatever
  // conversation happens to be selected right now, so navigating while this
  // component mounts cannot record the wrong owner.
  const params = new URLSearchParams();
  if (sessionId) {
    params.set("term_id", sessionId);
  } else {
    params.set("cmd", props.term.command);
    params.set("cwd", props.term.cwd);
    params.set("close_on_exit", String(props.term.closeOnExit));
    if (props.term.conversationId) params.set("conversation_id", props.term.conversationId);
    if (props.model) params.set("model", props.model);
  }
  titleParser.reset();
  const socket = new WebSocket(
    `${protocol}//${window.location.host}/api/exec-ws?${params.toString()}`,
  );
  ws = socket;

  socket.onopen = () => {
    socket.send(JSON.stringify({ type: "init", cols: v.cols, rows: v.rows }));
    emit("status-change", props.term.id, "running", null);
  };

  socket.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      if (msg.type === "output" && msg.data) {
        const bytes = base64ToUint8Array(msg.data);
        v.write(bytes);
        for (const title of titleParser.push(bytes)) setTitle(title);
      } else if (msg.type === "attached" && msg.term_id) {
        sessionId = msg.term_id;
        emit("attached", props.term.id, msg.term_id, msg.close_on_exit);
        syncView(); // a switch before now had no session to reopen
      } else if (msg.type === "unpersisted") {
        v.write(
          "\x1b[33mThe disk is full, so this terminal ends if this page reloads or disconnects.\x1b[0m\r\n",
        );
        emit("attached", props.term.id, null, props.term.closeOnExit);
      } else if (msg.type === "exit") {
        const code = parseInt(msg.data, 10) || 0;
        const color = code === 0 ? "32" : "31";
        v.write(
          `\r\n\x1b[2;${color}m${props.term.command} completed with exit code ${code}\x1b[0m\r\n`,
        );
        settled = true;
        emit("status-change", props.term.id, "exited", code);
      } else if (msg.type === "error") {
        v.write(`\r\n\x1b[31mError: ${msg.data}\x1b[0m\r\n`);
        settled = true;
        emit("status-change", props.term.id, "error", null);
      }
    } catch (err) {
      console.error("Failed to parse terminal message:", err);
    }
  };

  socket.onerror = (event) => console.error("WebSocket error:", event);
  socket.onclose = () => {
    // An explicit exit or error already told us how this terminal finished;
    // the socket closing afterwards must not overwrite that with a bare
    // "exited" and no exit code. Nor does a socket we closed ourselves.
    if (settled || ws !== socket) return;
    emit("status-change", props.term.id, "exited", null);
  };
}

function disconnect() {
  const socket = ws;
  ws = null;
  socket?.close();
  view?.dispose();
  view = null;
}

onUnmounted(() => {
  generation = -1;
  ro?.disconnect();
  disconnect();
  emit("unregister", props.term.id);
});

// Bring the view in line with the renderer and theme. A new renderer, or one
// that can't change its palette in place, gets a new view, reattached to the
// session so the server replays the screen. That needs a live session: one
// that hasn't attached yet catches up on "attached", and one that has ended
// keeps its view. A view that failed to open tries the new renderer.
function syncView() {
  if (openError.value && viewRenderer !== props.renderer && !props.term.unpersisted) {
    openError.value = null;
    settled = false;
    void connect();
    return;
  }
  if (!view) return;
  if (viewRenderer === props.renderer) {
    if (viewDark === props.isDark) return;
    if (view.setTheme(props.isDark)) {
      viewDark = props.isDark;
      return;
    }
  }
  if (!sessionId || settled || ws?.readyState !== WebSocket.OPEN) return;
  const { cols, rows } = view;
  const focused = containerRef.value?.contains(document.activeElement) ?? false;
  disconnect();
  void connect({ cols, rows }, focused);
}
watch(() => [props.renderer, props.isDark], syncView);

// Refit when visibility changes (React effect on [isVisible]).
watch(
  () => props.isVisible,
  (visible) => {
    if (visible) setTimeout(() => view?.fit(), 20);
  },
);
</script>
