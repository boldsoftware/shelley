<!-- Global speaker voice picker for GPT-Live calls. -->
<template>
  <Modal :is-open="isOpen" title="Live voice" class-name="language-picker-modal" @close="close">
    <p class="live-voice-hint">Speaker voice for live calls. Changes apply to your next call.</p>
    <input
      ref="filterInputRef"
      v-model="filter"
      type="search"
      class="live-voice-filter"
      placeholder="Filter voices..."
      aria-label="Filter voices"
    />
    <div v-if="error" class="live-voice-error" role="alert">{{ error }}</div>
    <div v-if="loading && voices.length === 0" class="live-voice-status">Loading…</div>
    <div
      v-else
      ref="optionsRef"
      class="language-picker-options live-voice-options"
      role="group"
      aria-label="Live voice"
    >
      <button
        v-for="voice in filteredVoices"
        :key="voice"
        type="button"
        class="language-picker-option"
        :aria-pressed="liveVoice === voice"
        :disabled="saving"
        @click="select(voice)"
      >
        <span>{{ voice }}</span>
        <span v-if="voice === DEFAULT_LIVE_VOICE" class="live-voice-default">(default)</span>
        <i
          v-if="liveVoice === voice"
          class="pi pi-check language-picker-check"
          aria-hidden="true"
        />
      </button>
      <div v-if="filteredVoices.length === 0" class="live-voice-status">No matching voices.</div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import Modal from "./Modal.vue";
import { DEFAULT_LIVE_VOICE, refreshLiveVoice, setLiveVoice, useLiveVoice } from "../composables/liveVoice";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void }>();

const { liveVoice } = useLiveVoice();
const voices = ref<string[]>([]);
const filter = ref("");
const loading = ref(false);
const saving = ref(false);
const error = ref("");
const filterInputRef = ref<HTMLInputElement | null>(null);
const optionsRef = ref<HTMLDivElement | null>(null);

let session = 0;

const filteredVoices = computed(() => {
  const q = filter.value.trim().toLowerCase();
  if (!q) return voices.value;
  return voices.value.filter((v) => v.includes(q));
});

function close() {
  emit("close");
}

async function select(voice: string) {
  if (saving.value) return;
  const seq = session;
  saving.value = true;
  error.value = "";
  try {
    await setLiveVoice(voice);
    if (seq === session) close();
  } catch (err) {
    if (seq === session) error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (seq === session) saving.value = false;
  }
}

watch(
  () => props.isOpen,
  async (open) => {
    const seq = ++session;
    if (!open) return;
    filter.value = "";
    error.value = "";
    saving.value = false;
    loading.value = true;
    await nextTick();
    filterInputRef.value?.focus();
    try {
      const cfg = await refreshLiveVoice();
      if (seq !== session) return;
      voices.value = cfg.voices;
      await nextTick();
      optionsRef.value
        ?.querySelector<HTMLButtonElement>('[aria-pressed="true"]')
        ?.scrollIntoView({ block: "nearest" });
    } catch (err) {
      if (seq === session) error.value = err instanceof Error ? err.message : String(err);
    } finally {
      if (seq === session) loading.value = false;
    }
  },
  { immediate: true },
);
</script>
