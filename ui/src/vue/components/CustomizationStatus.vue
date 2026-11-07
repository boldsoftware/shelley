<template>
  <!-- Compact: the status alone, on a title-only sidebar row; the task's
       header keeps the buttons and the error. -->
  <span
    v-if="compact"
    role="status"
    aria-label="Shelley build"
    class="customization-label customization-badge"
    :class="labelClass"
    :title="
      unavailable ? UNAVAILABLE_TITLE : [status.title, state.error].filter(Boolean).join(': ')
    "
  >
    <i v-if="state.status === 'installed' && !unavailable" class="pi pi-check" aria-hidden="true" />
    {{ status.label }}
  </span>
  <section v-else class="customization-status" aria-label="Shelley build">
    <div class="customization-line">
      <span
        role="status"
        class="customization-label"
        :class="labelClass"
        :title="unavailable ? UNAVAILABLE_TITLE : status.title"
      >
        <i
          v-if="state.status === 'installed' && !unavailable"
          class="pi pi-check"
          aria-hidden="true"
        />
        {{ status.label }}
      </span>
      <button
        v-if="state.status === 'installed'"
        type="button"
        class="text-button"
        :disabled="!!pendingConversation || unavailable || !state.running_under_systemd"
        :title="
          state.running_under_systemd
            ? 'Restart Shelley into this installed build; active conversations continue after reconnecting'
            : 'Automatic restart requires systemd supervision'
        "
        @click="emit('restart')"
      >
        {{ pendingConversation === state.conversation_id ? "Restarting…" : "Restart Shelley" }}
      </button>
      <template v-if="autoRestart && !unavailable">
        <span class="customization-auto" :title="autoRestart.title">{{ autoRestart.label }}</span>
        <button
          type="button"
          class="text-button"
          :disabled="!!pendingConversation"
          title="Don't restart into this build automatically; Restart Shelley still does"
          @click="emit('cancelAutoRestart')"
        >
          Cancel
        </button>
      </template>
    </div>
    <p v-if="state.error" class="error-line" role="alert">{{ state.error }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import type { Customization } from "../../services/customizations";

const props = defineProps<{
  state: Customization;
  pendingConversation: string;
  unavailable: boolean;
  compact?: boolean;
}>();
const emit = defineEmits<{ restart: []; cancelAutoRestart: [] }>();
const UNAVAILABLE_TITLE = "Build status unavailable; this is the last known";
const labelClass = computed(() => ({
  "customization-ready": props.state.status === "installed" && !props.unavailable,
  "customization-unavailable": props.unavailable,
}));
const IDLE_TITLE =
  "Shelley restarts into this build by itself once every conversation and action is done and you have left the UI alone for 10 seconds";
// The countdown runs here to the deadline the stream last sent.
const deadline = computed(() => props.state.auto_restart_at ?? null);
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | null = null;
watch(
  deadline,
  (at) => {
    if (tick !== null) clearInterval(tick);
    tick = null;
    now.value = Date.now();
    if (at !== null) tick = setInterval(() => (now.value = Date.now()), 250);
  },
  { immediate: true },
);
onUnmounted(() => {
  if (tick !== null) clearInterval(tick);
});
const autoRestart = computed(() => {
  const plan = props.state.auto_restart;
  if (props.state.status !== "installed" || (plan !== "pending" && plan !== "busy")) return null;
  if (plan === "busy" || deadline.value === null)
    return { label: "Restarts when idle", title: IDLE_TITLE + "; waiting for work to finish" };
  const seconds = Math.ceil((deadline.value - now.value) / 1000);
  return {
    label: seconds > 0 ? `Restarting in ${seconds}s` : "Restarting…",
    title: IDLE_TITLE + "; any activity in the UI postpones it",
  };
});
const status = computed(() => ({
  label: labels[props.state.status],
  title: titles[props.state.status],
}));
const labels: Record<Customization["status"], string> = {
  verifying: "Verifying…",
  failed: "Failed",
  installed: "Installed",
  restarting: "Restarting…",
};
const titles: Record<Customization["status"], string> = {
  verifying: "Checking, building and installing the Shelley build (scripts/install-custom)",
  failed: "Installing the Shelley build failed",
  installed: "Build installed; restart Shelley to run it",
  restarting: "Reconnecting; waiting for the installed build to start",
};
</script>

<style scoped>
.customization-status {
  margin: -2px 8px 2px 24px;
  font-size: var(--font-size-11);
}
.customization-line {
  display: flex;
  gap: 8px;
  align-items: center;
  min-width: 0;
}
.customization-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.customization-auto {
  color: var(--text-secondary);
  white-space: nowrap;
}
.customization-ready {
  color: var(--success-text);
  font-weight: 600;
}
.customization-unavailable {
  color: var(--text-secondary);
}
.customization-ready .pi {
  font-size: inherit;
}
.customization-line button {
  padding: 0 0.25rem;
  flex-shrink: 0;
  white-space: nowrap;
}
.customization-status .error-line {
  margin: 2px 0 0;
  overflow-wrap: anywhere;
}
</style>
