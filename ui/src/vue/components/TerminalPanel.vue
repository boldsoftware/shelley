<!-- Vue port of components/TerminalPanel.tsx. A bottom dock of ephemeral
     terminals backed by server-side dtach sessions (so they survive
     conversation switches + reloads). Preserves the .terminal-panel* class
     contract, the action-button titles, and the tab status indicators.

     The EphemeralTerminal type is re-exported here (from terminalTypes.ts) so
     other code can `import type { EphemeralTerminal } from
     "./components/TerminalPanel.vue"` exactly as it imported from the React
     module. The actual xterm.js + websocket lifecycle lives in the
     TerminalInstance.vue child (one per terminal).

     React callback props are mapped to emits:
       onClose                -> emit("close", id)
       onInsertIntoInput      -> emit("insert-into-input", text)
       onAutoFocusConsumed    -> emit("auto-focus-consumed")
       onActiveTerminalExited -> emit("active-terminal-exited")
       onAttached             -> emit("attached", id, termId)
     The presence of an onInsertIntoInput handler in React (which gates the
     insert buttons) is mirrored by the required `canInsertIntoInput` prop. -->
<template>
  <div
    v-if="terminals.length > 0"
    :class="`terminal-panel${minimized ? ' terminal-panel-minimized' : ''}`"
    :style="minimized ? undefined : { height: `${height}px`, flexShrink: 0 }"
  >
    <!-- Resize handle at top — hidden when minimized -->
    <div v-if="!minimized" class="terminal-panel-resize-handle" @mousedown="handleResizeMouseDown">
      <div class="terminal-panel-resize-grip" />
    </div>

    <!-- Tab bar + actions -->
    <div class="terminal-panel-header">
      <!-- Minimize/maximize toggle -->
      <button
        class="terminal-panel-action-btn"
        :title="minimized ? 'Expand terminals' : 'Minimize terminals'"
        @click="toggleMinimized"
      >
        <ChevronUpIcon v-if="minimized" />
        <ChevronDownIcon v-else />
      </button>

      <div class="terminal-panel-tabs">
        <div
          v-for="t in terminals"
          :key="t.id"
          :class="`terminal-panel-tab${t.id === activeTabId ? ' terminal-panel-tab-active' : ''}`"
          :title="t.command"
          @click="onTabClick(t.id)"
        >
          <span
            v-if="statusMap.get(t.id)?.status === 'running'"
            class="terminal-panel-tab-indicator terminal-panel-tab-running"
            >●</span
          >
          <span
            v-if="statusMap.get(t.id)?.status === 'exited' && statusMap.get(t.id)?.exitCode === 0"
            class="terminal-panel-tab-indicator terminal-panel-tab-success"
            >✓</span
          >
          <span
            v-if="statusMap.get(t.id)?.status === 'exited' && statusMap.get(t.id)?.exitCode !== 0"
            class="terminal-panel-tab-indicator terminal-panel-tab-error"
            >✗</span
          >
          <span
            v-if="statusMap.get(t.id)?.status === 'error'"
            class="terminal-panel-tab-indicator terminal-panel-tab-error"
            >✗</span
          >
          <span class="terminal-panel-tab-label">{{ tabLabel(t.command) }}</span>
          <button
            class="terminal-panel-tab-close"
            title="Close terminal"
            @click.stop="emit('close', t.id)"
          >
            ×
          </button>
        </div>
      </div>

      <!-- Action buttons — hidden when minimized -->
      <div v-if="!minimized" class="terminal-panel-actions">
        <button
          :class="`terminal-panel-action-btn${copyFeedback === 'copyScreen' ? ' terminal-panel-action-btn-feedback' : ''}`"
          title="Copy visible screen"
          @click="copyScreen"
        >
          <CheckIcon v-if="copyFeedback === 'copyScreen'" />
          <CopyIcon v-else />
        </button>
        <button
          :class="`terminal-panel-action-btn${copyFeedback === 'copyAll' ? ' terminal-panel-action-btn-feedback' : ''}`"
          title="Copy all output"
          @click="copyAll"
        >
          <CheckIcon v-if="copyFeedback === 'copyAll'" />
          <CopyAllIcon v-else />
        </button>
        <template v-if="canInsertIntoInput">
          <button
            :class="`terminal-panel-action-btn${copyFeedback === 'insertScreen' ? ' terminal-panel-action-btn-feedback' : ''}`"
            title="Insert visible screen into input"
            @click="insertScreen"
          >
            <CheckIcon v-if="copyFeedback === 'insertScreen'" />
            <InsertIcon v-else />
          </button>
          <button
            :class="`terminal-panel-action-btn${copyFeedback === 'insertAll' ? ' terminal-panel-action-btn-feedback' : ''}`"
            title="Insert all output into input"
            @click="insertAll"
          >
            <CheckIcon v-if="copyFeedback === 'insertAll'" />
            <InsertAllIcon v-else />
          </button>
        </template>
        <div class="terminal-panel-actions-divider" />
        <button
          class="terminal-panel-action-btn"
          title="Close active terminal"
          @click="handleCloseActive"
        >
          <CloseIcon />
        </button>
      </div>
    </div>

    <!-- Terminal content area — hidden (not unmounted) when minimized -->
    <div class="terminal-panel-content" :style="minimized ? { display: 'none' } : undefined">
      <TerminalInstance
        v-for="t in terminals"
        :key="t.id"
        :term="t"
        :is-visible="t.id === activeTabId"
        :is-dark="isDark"
        :conversation-id="conversationId ?? null"
        :model="model ?? null"
        @status-change="handleStatusChange"
        @register="registerXterm"
        @unregister="unregisterXterm"
        @attached="(id, termId) => emit('attached', id, termId)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import type { Terminal } from "@xterm/xterm";
import { isDarkModeActive } from "../../services/theme";
import TerminalInstance from "./TerminalInstance.vue";
import type { TermStatus } from "./terminalHelpers";
import type { EphemeralTerminal } from "./terminalTypes";
import CopyIcon from "./terminalIcons/CopyIcon.vue";
import CopyAllIcon from "./terminalIcons/CopyAllIcon.vue";
import InsertIcon from "./terminalIcons/InsertIcon.vue";
import InsertAllIcon from "./terminalIcons/InsertAllIcon.vue";
import CheckIcon from "./terminalIcons/CheckIcon.vue";
import CloseIcon from "./terminalIcons/CloseIcon.vue";
import ChevronUpIcon from "./terminalIcons/ChevronUpIcon.vue";
import ChevronDownIcon from "./terminalIcons/ChevronDownIcon.vue";

// Re-export EphemeralTerminal so importers can keep importing it from this
// module (the canonical definition lives in terminalTypes.ts).
export type { EphemeralTerminal } from "./terminalTypes";

const props = defineProps<{
  terminals: EphemeralTerminal[];
  autoFocusId?: string | null;
  // Mirrors the presence of React's onInsertIntoInput callback, which gates
  // the insert buttons. When false the insert actions are not rendered.
  canInsertIntoInput?: boolean;
  // Context surfaced to spawned sessions via SHELLEY_* env vars. Only used on
  // initial spawn; reattaches use the env baked in when the session was
  // created.
  conversationId?: string | null;
  model?: string | null;
}>();

const emit = defineEmits<{
  (e: "close", id: string): void;
  (e: "insert-into-input", text: string): void;
  (e: "auto-focus-consumed"): void;
  (e: "active-terminal-exited"): void;
  (e: "attached", id: string, termId: string): void;
}>();

const activeTabId = ref<string | null>(null);
const height = ref(300);
const minimized = ref(false);
const copyFeedback = ref<string | null>(null);
const statusMap = ref<Map<string, { status: TermStatus; exitCode: number | null }>>(new Map());
const isResizingRef = { current: false };
const startYRef = { current: 0 };
const startHeightRef = { current: 0 };

// Detect dark mode
const isDark = ref(isDarkModeActive());
let observer: MutationObserver | null = null;
onMounted(() => {
  observer = new MutationObserver(() => {
    isDark.value = isDarkModeActive();
  });
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  });
});
onUnmounted(() => observer?.disconnect());

// Auto-select newest tab when a new terminal is added (React effect on
// [terminals.length]).
watch(
  () => props.terminals.length,
  (len) => {
    if (len > 0) {
      const lastTerminal = props.terminals[props.terminals.length - 1];
      activeTabId.value = lastTerminal.id;
      minimized.value = false; // expand when a new terminal arrives
    } else {
      activeTabId.value = null;
    }
  },
);

// If active tab got closed, switch to the last remaining (React effect on
// [terminals, activeTabId]).
watch(
  () => [props.terminals, activeTabId.value] as const,
  () => {
    if (activeTabId.value && !props.terminals.find((t) => t.id === activeTabId.value)) {
      if (props.terminals.length > 0) {
        activeTabId.value = props.terminals[props.terminals.length - 1].id;
      } else {
        activeTabId.value = null;
      }
    }
  },
);

function handleStatusChange(id: string, status: TermStatus, exitCode: number | null) {
  const prev = statusMap.value;
  const next = new Map(prev);
  const existing = next.get(id);
  // Don't overwrite exit status with ws.onclose
  if (existing && existing.status === "exited" && status === "exited") {
    return;
  }
  next.set(id, {
    status,
    exitCode: exitCode ?? existing?.exitCode ?? null,
  });
  statusMap.value = next;
}

// Resize drag
function handleResizeMouseDown(e: MouseEvent) {
  e.preventDefault();
  isResizingRef.current = true;
  startYRef.current = e.clientY;
  startHeightRef.current = height.value;

  const handleMouseMove = (ev: MouseEvent) => {
    if (!isResizingRef.current) return;
    // Dragging up increases height
    const delta = startYRef.current - ev.clientY;
    height.value = Math.max(80, Math.min(800, startHeightRef.current + delta));
  };

  const handleMouseUp = () => {
    isResizingRef.current = false;
    document.removeEventListener("mousemove", handleMouseMove);
    document.removeEventListener("mouseup", handleMouseUp);
  };

  document.addEventListener("mousemove", handleMouseMove);
  document.addEventListener("mouseup", handleMouseUp);
}

function showFeedback(type: string) {
  copyFeedback.value = type;
  setTimeout(() => (copyFeedback.value = null), 1500);
}

// Registry of xterm instances by terminal id.
const xtermRegistry = new Map<string, Terminal>();
function registerXterm(id: string, xterm: Terminal) {
  xtermRegistry.set(id, xterm);
}
function unregisterXterm(id: string) {
  xtermRegistry.delete(id);
}

// Auto-focus terminal when autoFocusId is set (React effect on
// [autoFocusId, onAutoFocusConsumed]).
watch(
  () => props.autoFocusId,
  (autoFocusId) => {
    if (!autoFocusId) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    let attempt = 0;
    const tryFocus = () => {
      if (cancelled) return;
      const xterm = xtermRegistry.get(autoFocusId);
      if (xterm) {
        activeTabId.value = autoFocusId;
        minimized.value = false; // expand when focusing a terminal
        // Double-rAF to ensure we're past any keyup/form events that might steal focus
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            xterm.focus();
          });
        });
        emit("auto-focus-consumed");
        return;
      }
      if (++attempt < 10) {
        timer = setTimeout(tryFocus, 50);
      }
    };
    // Small initial delay to let the form submit / keyup events settle
    timer = setTimeout(tryFocus, 50);
    // Cleanup when autoFocusId changes again.
    const stop = watch(
      () => props.autoFocusId,
      () => {
        cancelled = true;
        clearTimeout(timer);
        stop();
      },
    );
  },
);

// Restore focus to message input when the active terminal exits (React effect
// on [activeTabId, statusMap, onActiveTerminalExited]).
const prevActiveStatusRef = {
  current: { tabId: null as string | null, status: undefined as TermStatus | undefined },
};
watch(
  () => [activeTabId.value, statusMap.value] as const,
  () => {
    if (!activeTabId.value) return;
    const info = statusMap.value.get(activeTabId.value);
    const prev = prevActiveStatusRef.current;
    // Only trigger on status transition within the same tab
    const wasRunning = prev.tabId === activeTabId.value && prev.status === "running";
    prevActiveStatusRef.current = { tabId: activeTabId.value, status: info?.status };
    if (wasRunning && (info?.status === "exited" || info?.status === "error")) {
      emit("active-terminal-exited");
    }
  },
);

function getBufferText(mode: "screen" | "all"): string {
  if (!activeTabId.value) return "";
  const xterm = xtermRegistry.get(activeTabId.value);
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
}

function copyScreen() {
  navigator.clipboard.writeText(getBufferText("screen"));
  showFeedback("copyScreen");
}
function copyAll() {
  navigator.clipboard.writeText(getBufferText("all"));
  showFeedback("copyAll");
}
function insertScreen() {
  if (props.canInsertIntoInput) {
    emit("insert-into-input", getBufferText("screen"));
    showFeedback("insertScreen");
  }
}
function insertAll() {
  if (props.canInsertIntoInput) {
    emit("insert-into-input", getBufferText("all"));
    showFeedback("insertAll");
  }
}

function handleCloseActive() {
  if (activeTabId.value) emit("close", activeTabId.value);
}

function toggleMinimized() {
  minimized.value = !minimized.value;
}

function onTabClick(id: string) {
  activeTabId.value = id;
  if (minimized.value) minimized.value = false;
}

// Refit terminals when un-minimizing by nudging the container to trigger
// ResizeObserver (React effect on [minimized, activeTabId]).
const wasMinimizedRef = { current: minimized.value };
watch(
  () => [minimized.value, activeTabId.value] as const,
  () => {
    const wasMinimized = wasMinimizedRef.current;
    wasMinimizedRef.current = minimized.value;
    if (wasMinimized && !minimized.value && activeTabId.value) {
      const timer = setTimeout(() => {
        const el = document.querySelector(`[data-terminal-id="${activeTabId.value}"]`);
        if (el) {
          (el as HTMLElement).style.height = "99.9%";
          requestAnimationFrame(() => {
            (el as HTMLElement).style.height = "100%";
          });
        }
      }, 30);
      // No explicit cleanup needed; the timer is short-lived.
      void timer;
    }
  },
);

// Truncate command for tab label
function tabLabel(cmd: string): string {
  // Show first word or first 30 chars
  const firstWord = cmd.split(/\s+/)[0];
  if (firstWord.length > 30) return firstWord.substring(0, 27) + "...";
  return firstWord;
}
</script>
