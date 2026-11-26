<template>
  <Modal :is-open="isOpen" title="Live settings" class-name="modal-wide" @close="emit('close')">
    <p class="live-settings-hint">
      One set of preferences for all calls in this Shelley, independent of profiles.
    </p>
    <div v-if="error" class="settings-modal-error" role="alert">{{ error }}</div>
    <div v-if="draft && options" class="live-settings-body">
      <section>
        <label class="advanced-settings-header" for="live-settings-sound">Voice</label>
        <select id="live-settings-sound" v-model="draft.voice" class="live-settings-sound">
          <option v-for="voice in options.voices" :key="voice" :value="voice">
            {{ voice.charAt(0).toUpperCase() + voice.slice(1) }}
          </option>
        </select>
        <p class="live-settings-hint">
          The sound of the voice. Its persona comes from the profile's voice prompt.
        </p>
        <div class="live-settings-call-control">
          <div class="live-settings-slider-heading">
            <label for="live-settings-pace">Speaking pace</label>
            <output for="live-settings-pace">{{ paceLabel }}</output>
          </div>
          <input
            id="live-settings-pace"
            v-model.number="draft.speaking_pace"
            class="live-settings-slider"
            type="range"
            min="-2"
            max="2"
            step="1"
            :aria-valuetext="paceLabel"
          />
          <div class="live-settings-slider-ticks" aria-hidden="true">
            <span v-for="label in paceLabels" :key="label">{{ label }}</span>
          </div>
          <p class="live-settings-hint">Speaking guidance, not playback speed.</p>
        </div>
        <div class="live-settings-call-control">
          <label class="live-settings-slider-heading" for="live-settings-progress">
            Spoken progress updates
            <ToggleSwitch v-model="draft.progress_updates" input-id="live-settings-progress" />
          </label>
        </div>
        <p class="live-settings-hint">All changes apply when the next call starts.</p>
      </section>
    </div>
    <p v-else-if="!error" class="live-settings-hint">Loading Live settings…</p>
    <template #footer>
      <Button severity="secondary" label="Cancel" @click="emit('close')" />
      <Button label="Save" :loading="saving" :disabled="!draft || !options" @click="save" />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import ToggleSwitch from "primevue/toggleswitch";
import Modal from "./Modal.vue";
import { liveSettingsApi } from "../../services/api";
import type { LiveSettings } from "../../types";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void }>();
const draft = ref<LiveSettings | null>(null);
const options = ref<Awaited<ReturnType<typeof liveSettingsApi.options>> | null>(null);
const error = ref("");
const saving = ref(false);
const paceLabels = ["Very slow", "Slow", "Natural", "Fast", "Very fast"];
const paceLabel = computed(() => (draft.value ? paceLabels[draft.value.speaking_pace + 2] : ""));

watch(
  () => props.isOpen,
  async (open) => {
    if (!open) return;
    draft.value = null;
    options.value = null;
    error.value = "";
    try {
      const [saved, choices] = await Promise.all([
        liveSettingsApi.get(),
        liveSettingsApi.options(),
      ]);
      draft.value = saved;
      options.value = choices;
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err);
    }
  },
  { immediate: true },
);

async function save() {
  if (!draft.value) return;
  saving.value = true;
  error.value = "";
  try {
    await liveSettingsApi.save({ ...draft.value });
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}
</script>

<style scoped>
.live-settings-hint {
  margin: 0.5rem 0;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}
.live-settings-sound {
  display: block;
  width: 100%;
  margin-top: 0.5rem;
  padding: 0.5rem;
  border: 1px solid var(--border);
  border-radius: 0.25rem;
  background: var(--bg-base);
  color: var(--text-primary);
  font: inherit;
}
.live-settings-call-control {
  margin-top: 1.5rem;
}
.live-settings-slider-heading,
.live-settings-slider-ticks {
  display: flex;
  justify-content: space-between;
  gap: 0.75rem;
}
.live-settings-slider-heading {
  align-items: center;
  font-size: var(--font-size-13);
}
.live-settings-slider-heading output,
.live-settings-slider-ticks {
  color: var(--text-secondary);
  font-size: var(--font-size-11);
}
.live-settings-slider {
  display: block;
  width: 100%;
  margin: 0.5rem 0;
  accent-color: var(--link-color);
}
</style>
