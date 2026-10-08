<!-- The Tools modal: ToolSettings for the settings in effect. Edits a copy;
     Apply hands it back, so a conversation under way changes once, with one
     marker, however many rows were clicked. -->
<template>
  <Modal :is-open="isOpen" title="Tools" class-name="modal-wide" @close="emit('close')">
    <ToolSettings v-model:overrides="overrides" v-model:nudge="nudge" :tools="tools" />
    <template #footer>
      <span class="settings-modal-error" role="alert">{{
        error || (locked ? "Applying waits for the agent to finish." : "")
      }}</span>
      <Button severity="secondary" label="Cancel" @click="emit('close')" />
      <Button :label="applyLabel" :loading="busy" :disabled="!changed || locked" @click="apply" />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ToolSettings from "./ToolSettings.vue";
import { sameOverrides } from "../composables/profiles";
import { DEFAULT_COMPACT_NUDGE_TOKENS } from "./autoCompaction";

type Overrides = Record<string, "on" | "off">;

const props = defineProps<{
  isOpen: boolean;
  tools: { name: string; summary: string; default_on: boolean }[];
  toolOverrides: Overrides;
  /** 0 means the default. */
  compactNudgeTokens: number;
  applyLabel: string;
  /** Whether applying has to wait, for the agent to finish. */
  locked: boolean;
  /** Applies the edits; a rejection's message is shown and the modal stays. */
  onApply: (toolOverrides: Overrides, compactNudgeTokens: number) => Promise<void> | void;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const overrides = ref<Overrides>({});
const nudge = ref(0);
const busy = ref(false);
const error = ref("");

// The default nudge, spelled out, is the default.
const normalized = (n: number) => (n === DEFAULT_COMPACT_NUDGE_TOKENS ? 0 : n);

watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    overrides.value = { ...props.toolOverrides };
    nudge.value = normalized(props.compactNudgeTokens);
    error.value = "";
  },
  { immediate: true },
);

const changed = computed(
  () =>
    !sameOverrides(props.toolOverrides, overrides.value) ||
    nudge.value !== normalized(props.compactNudgeTokens),
);

async function apply() {
  busy.value = true;
  error.value = "";
  try {
    await props.onApply(overrides.value, nudge.value);
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}
</script>
