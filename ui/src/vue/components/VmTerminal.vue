<!-- A shell on a VM of the prod tab: Shelley runs ssh to it through the
     exe.dev integration that lists it (GET /api/exe-vms/<name>/terminal),
     until this unmounts. Mounting it again starts a new session. -->
<template>
  <div
    class="vm-terminal"
    :style="{ backgroundColor: isDark ? '#1a1b26' : '#f8f9fa' }"
    data-testid="vm-terminal"
  >
    <TerminalInstance
      :term="term"
      :is-visible="visible"
      :is-dark="isDark"
      :renderer="terminalRenderer"
      :socket-path="`/api/exe-vms/${encodeURIComponent(name)}/terminal`"
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

const props = defineProps<{ name: string; visible: boolean }>();

const isDark = useDarkMode();
const { terminalRenderer } = useTerminalRendererPreference();
// The command is the terminal's banner; the server decides what runs.
const term: EphemeralTerminal = {
  id: `vm-${props.name}`,
  command: `ssh ${props.name}`,
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
