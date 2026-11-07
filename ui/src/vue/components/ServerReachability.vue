<!-- One line while Shelley doesn't answer, as while it restarts: quiet at
     first, an error once that has lasted long. Requests that fail this way
     show nothing themselves (see serverReachability). -->
<template>
  <p
    v-if="line"
    class="server-reachability"
    :class="{ 'server-reachability-error': line.error }"
    role="status"
    data-testid="server-reachability"
    :title="line.text"
  >
    <i v-if="!line.error" class="pi pi-spin pi-spinner" aria-hidden="true" />{{ line.text }}
  </p>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { unreachableSince } from "../../services/serverReachability";
import { unreachableLine } from "../../services/unreachable";

/** restarting: a restart of Shelley is under way. */
const props = defineProps<{ restarting: boolean }>();
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | null = null;
const stop = () => {
  if (tick !== null) clearInterval(tick);
  tick = null;
};
watch(
  unreachableSince,
  (since) => {
    stop();
    now.value = Date.now();
    if (since !== null) tick = setInterval(() => (now.value = Date.now()), 10_000);
  },
  { immediate: true },
);
onUnmounted(stop);
const line = computed(() =>
  unreachableSince.value === null
    ? null
    : unreachableLine(unreachableSince.value, now.value, props.restarting),
);
</script>

<style scoped>
.server-reachability {
  margin: 0.25rem 0;
  padding: 0 0.75rem;
  overflow: hidden;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.server-reachability .pi {
  margin-right: 0.375rem;
  font-size: inherit;
}
.server-reachability-error {
  color: var(--error-text);
}
</style>
