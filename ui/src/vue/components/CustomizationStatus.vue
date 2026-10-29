<template>
  <section class="customization-status" aria-label="Shelley build">
    <div class="customization-line">
      <span
        role="status"
        class="customization-label"
        :class="{ 'customization-ready': state.status === 'installed' && !unavailable }"
        :title="unavailable ? 'Build status unavailable' : status.title"
      >
        <i
          v-if="state.status === 'installed' && !unavailable"
          class="pi pi-check"
          aria-hidden="true"
        />
        {{ unavailable ? "Unavailable" : status.label }}
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
      <button
        v-if="state.status === 'failed' || state.status === 'stale'"
        type="button"
        class="text-button"
        :disabled="!!pendingConversation || unavailable"
        @click="emit('verify')"
      >
        Verify again
      </button>
    </div>
    <p v-if="state.error" class="error-line" role="alert">{{ state.error }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Customization } from "../../services/customizations";

const props = defineProps<{
  state: Customization;
  pendingConversation: string;
  unavailable: boolean;
}>();
const emit = defineEmits<{ restart: []; verify: []; cancelAutoRestart: [] }>();
const IDLE_TITLE =
  "Shelley restarts into this build by itself once every conversation, action and build is done and you have left the UI alone for 30 seconds";
const autoRestart = computed(() => {
  const { auto_restart: plan, auto_restart_in_ms: ms } = props.state;
  if (props.state.status !== "installed" || (plan !== "pending" && plan !== "busy")) return null;
  if (plan === "busy" || ms === undefined)
    return { label: "Restarts when idle", title: IDLE_TITLE + "; waiting for work to finish" };
  const seconds = Math.ceil(ms / 1000);
  return {
    label: seconds > 0 ? `Restarting in ${seconds}s` : "Restarting…",
    title: IDLE_TITLE + "; any activity in the UI postpones it",
  };
});
// A verified build that failed to install stays ready, and installing is
// retried after the next turn.
const status = computed(() =>
  props.state.status === "ready" && props.state.error
    ? { label: "Not installed", title: "Verified, but installing failed; retrying after the next turn" }
    : { label: labels[props.state.status], title: titles[props.state.status] },
);
const labels: Record<Customization["status"], string> = {
  waiting: "Waiting…",
  verifying: "Verifying…",
  ready: "Installing…",
  stale: "Out of date",
  failed: "Failed",
  installed: "Installed",
  restarting: "Restarting…",
  running: "Running",
};
const titles: Record<Customization["status"], string> = {
  waiting: "Waiting to verify the Shelley build",
  verifying: "Verifying the Shelley build",
  ready: "Verified; installing the build",
  stale: "This build changed since it was verified; verifying again",
  failed: "Verification failed",
  installed: "Verified build installed; restart Shelley to run it",
  restarting: "Reconnecting; waiting for the verified build to start",
  running: "Verified build installed and running",
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
