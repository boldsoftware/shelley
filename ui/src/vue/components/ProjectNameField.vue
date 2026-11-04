<!-- A project name being typed in place: Enter or leaving the field submits
     a changed name, Escape (or an empty or unchanged name) cancels. Names the
     server would refuse are explained under the field, which stays. -->
<template>
  <input
    ref="input"
    v-model="name"
    class="project-name-field"
    :aria-label="label"
    :aria-invalid="!!error"
    :placeholder="t('projectNamePlaceholder')"
    autocomplete="off"
    spellcheck="false"
    :disabled="busy"
    @keydown.enter.prevent="commit"
    @keydown.esc.stop.prevent="finish"
    @blur="commit"
  />
  <p v-if="error" class="error-line project-name-error" role="alert">{{ error }}</p>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { projectNameProblem, useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";

const props = defineProps<{
  initial: string;
  label: string;
  /** Saves the name; a rejection's message is shown. */
  submit: (name: string) => Promise<void>;
}>();
const emit = defineEmits<{ (e: "done"): void }>();
const { t } = useI18n();
const workspace = useWorkspace();
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

async function commit() {
  if (busy.value || finished) return;
  const trimmed = name.value.trim();
  if (!trimmed || trimmed === props.initial) return finish();
  const taken = (workspace.data.value?.projects ?? [])
    .map((project) => project.name)
    .filter((other) => other !== props.initial);
  error.value = projectNameProblem(trimmed, taken);
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
