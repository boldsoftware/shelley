<!-- A name being typed in place: Enter or leaving the field submits a
     changed name, Escape (or an empty or unchanged name) cancels. A name
     `check` refuses, or the submission's error, is explained under the
     field, which stays. -->
<template>
  <input
    ref="input"
    v-model="name"
    class="name-field"
    :aria-label="label"
    :aria-invalid="!!error"
    :placeholder="placeholder"
    autocomplete="off"
    spellcheck="false"
    :disabled="busy"
    @keydown.enter="onEnter"
    @keydown.esc.stop.prevent="finish"
    @blur="commit"
  />
  <p v-if="error" class="error-line name-field-error" role="alert">{{ error }}</p>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { errorMessage } from "../../utils/errorMessage";
import { isImeComposing } from "../../utils/imeComposing";

const props = defineProps<{
  initial: string;
  label: string;
  placeholder?: string;
  /** Why a trimmed, changed name can't be saved, or "". */
  check?: (name: string) => string;
  /** Saves the name; a rejection's message is shown. */
  submit: (name: string) => Promise<void>;
}>();
const emit = defineEmits<{ (e: "done"): void }>();
const input = ref<HTMLInputElement | null>(null);
const name = ref(props.initial);
const error = ref("");
const busy = ref(false);
// Done: the field is going away, and its blur must not submit.
let finished = false;
function finish() {
  finished = true;
  emit("done");
}

onMounted(() => {
  input.value?.focus();
  input.value?.select();
});

function onEnter(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  e.preventDefault();
  void commit();
}

async function commit() {
  if (busy.value || finished) return;
  const trimmed = name.value.trim();
  if (!trimmed || trimmed === props.initial) return finish();
  error.value = props.check?.(trimmed) ?? "";
  if (error.value) return;
  busy.value = true;
  try {
    await props.submit(trimmed);
    finish();
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}
</script>
