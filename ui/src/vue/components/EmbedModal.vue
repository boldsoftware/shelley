<!-- Edit with Shelley: the snippet that puts embed.js's button on the
     user's own pages, and the sites whose pages may frame Shelley
     (/api/embed-origins). On exe.dev, its proxy serves the script and
     decides which pages may frame Shelley. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="t('editWithShelley')"
    class-name="embed-modal"
    @close="emit('close')"
  >
    <p class="embed-help">{{ t("embedHelp") }}</p>
    <div class="embed-snippet">
      <code data-testid="embed-snippet">{{ snippet }}</code>
      <button
        type="button"
        :aria-label="copied ? t('copied') : t('copy')"
        :title="copied ? t('copied') : t('copy')"
        @click="copy"
      >
        <i :class="copied ? 'pi pi-check' : 'pi pi-copy'" aria-hidden="true" />
      </button>
    </div>
    <p v-if="vmHost" class="embed-help embed-sites-title">
      {{ t("embedExeDev").replace("{origin}", vmOrigin) }}
    </p>
    <template v-else>
      <p id="embed-sites-title" class="embed-help embed-sites-title">
        {{ t("embedSites") }}
      </p>
      <ul v-if="origins.length" class="embed-sites" aria-labelledby="embed-sites-title">
        <li v-for="origin in origins" :key="origin">
          <span>{{ origin }}</span>
          <Button
            size="small"
            severity="secondary"
            text
            :label="t('removeAction')"
            :aria-label="`${t('removeAction')} ${origin}`"
            :disabled="busy"
            @click="run(() => embedOriginsApi.remove(origin))"
          />
        </li>
      </ul>
      <form class="embed-add" @submit.prevent="add">
        <InputText
          v-model="draft"
          placeholder="https://app.example.com"
          aria-labelledby="embed-sites-title"
          fluid
          :dt="inputFieldDt"
          autocomplete="off"
          spellcheck="false"
        />
        <Button type="submit" :label="t('embedAdd')" :disabled="busy || !draft.trim()" />
      </form>
      <p v-if="error" class="embed-error" role="alert">{{ error }}</p>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Modal from "./Modal.vue";
import { inputFieldDt } from "./configFieldDt";
import { useI18n } from "../composables/i18n";
import { embedOriginsApi } from "../../services/api";
import { copyText } from "../../utils/clipboard";
import { embedSnippet } from "../../utils/embedSnippet";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();

const { snippet, vmHost } = embedSnippet(location, window.__SHELLEY_INIT__?.exe_dev_shelley_host);
const vmOrigin = `${location.protocol}//${vmHost}`;

// Where the clipboard is out of reach, as on plain http, a click on the
// snippet selects it.
const copied = ref(false);
let copiedTimer: ReturnType<typeof setTimeout> | undefined;
async function copy() {
  if (!(await copyText(snippet))) return;
  copied.value = true;
  clearTimeout(copiedTimer);
  copiedTimer = setTimeout(() => (copied.value = false), 2000);
}
onBeforeUnmount(() => clearTimeout(copiedTimer));

const origins = ref<string[]>([]);
const draft = ref("");
const busy = ref(false);
const error = ref("");

// run makes a change, or loads the list, and shows the list it returns,
// unless a later run has started.
let runs = 0;
async function run(change: () => Promise<string[]>): Promise<boolean> {
  const n = ++runs;
  busy.value = true;
  error.value = "";
  try {
    const list = await change();
    if (n === runs) origins.value = list;
    return true;
  } catch (err) {
    if (n === runs) error.value = err instanceof Error ? err.message : String(err);
    return false;
  } finally {
    if (n === runs) busy.value = false;
  }
}

async function add() {
  if (await run(() => embedOriginsApi.add(draft.value.trim()))) draft.value = "";
}

watch(
  () => props.isOpen,
  (open) => {
    if (open && !vmHost) void run(embedOriginsApi.list);
  },
  { immediate: true },
);
</script>
