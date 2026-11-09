<!-- A session of a prod tab VM's web terminal: Shelley opens it through
     the exe.dev integration that lists the VM
     (GET /api/exe-vms/<name>/terminal?session=<session>) and attaches to
     the session, creating it if need be, until this unmounts; hidden, it
     stays attached. -->
<template>
  <div
    class="vm-terminal"
    :style="{ backgroundColor: isDark ? '#1a1b26' : '#f8f9fa', display: visible ? undefined : 'none' }"
    data-testid="vm-terminal"
  >
    <TerminalInstance
      :term="term"
      :is-visible="visible"
      :is-dark="isDark"
      :renderer="terminalRenderer"
      :socket-path="`/api/exe-vms/${encodeURIComponent(name)}/terminal?session=${encodeURIComponent(session)}`"
      @register="register"
    />
  </div>
</template>

<script setup lang="ts">
import { watch } from "vue";
import TerminalInstance from "./TerminalInstance.vue";
import type { TerminalHandle } from "./terminalBackend";
import type { EphemeralTerminal } from "./terminalTypes";
import { useDarkMode } from "../composables/darkMode";
import { useTerminalRendererPreference } from "../composables/terminalRendererPreference";

const props = defineProps<{ name: string; session: string; visible: boolean }>();

const isDark = useDarkMode();
const { terminalRenderer } = useTerminalRendererPreference();
// The command is the terminal's banner; the server decides what runs.
const term: EphemeralTerminal = {
  id: `vm-${props.name}-${props.session}`,
  command: `${props.name}.xterm (${props.session})`,
  closeOnExit: false,
  cwd: "",
  createdAt: new Date(),
  conversationId: null,
};

let handle: TerminalHandle | null = null;
function register(_id: string, h: TerminalHandle) {
  handle = h;
  if (props.visible) h.focus();
}
watch(
  () => props.visible,
  (visible) => {
    if (visible) handle?.focus();
  },
  { flush: "post" },
);
</script>

<style scoped>
.vm-terminal {
  flex: 1;
  min-height: 0;
  position: relative;
  overflow: hidden;
}
.vm-terminal > div {
  position: absolute;
  inset: 4px;
}
</style>
