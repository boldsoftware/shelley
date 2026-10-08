<!-- A profile's system prompt: Shelley's built-in one, or a custom Go
     text/template. The template is highlighted as it's typed, checked against
     the server (as a save would check it), and what's wrong is marked where
     it is. The variables a template can use insert at the cursor. -->
<template>
  <div class="prompt-editor">
    <div class="tool-override-choices" role="radiogroup" aria-label="System prompt">
      <button
        v-for="choice in [
          { custom: false, label: 'Built-in' },
          { custom: true, label: 'Custom' },
        ]"
        :key="choice.label"
        type="button"
        role="radio"
        :aria-checked="custom === choice.custom"
        :class="`tool-override-choice${custom === choice.custom ? ' active' : ''}`"
        @click="setCustom(choice.custom)"
      >
        {{ choice.label }}
      </button>
    </div>
    <p class="profiles-hint">
      <template v-if="custom">
        A Go
        <a href="https://pkg.go.dev/text/template" target="_blank" rel="noopener">template</a>,
        rendered when a conversation starts with this profile or switches to it.
      </template>
      <template v-else>
        Shelley's own prompt, which improves with Shelley. Choose Custom to start from a copy.
      </template>
    </p>
    <template v-if="custom">
      <div class="prompt-editor-scroll">
        <div class="prompt-editor-stack">
          <pre
            ref="preRef"
            class="prompt-editor-layer"
            aria-hidden="true"
            v-html="highlighted"
          ></pre>
          <textarea
            ref="textareaRef"
            v-model="text"
            class="prompt-editor-layer prompt-editor-input"
            spellcheck="false"
            rows="1"
            aria-label="System prompt template"
            :aria-invalid="!!problem"
            :aria-describedby="problem ? problemId : undefined"
          />
        </div>
      </div>
      <div :id="problemId" role="alert">
        <button v-if="problem" type="button" class="prompt-editor-problem" @click="showProblem">
          {{ problem.line ? `Line ${problem.line}: ` : "" }}{{ problem.message }}
        </button>
      </div>
      <details v-if="info" class="profiles-vars">
        <summary>Variables</summary>
        <dl>
          <template v-for="v in info.variables" :key="v.name">
            <dt>
              <button type="button" class="prompt-editor-var" @click="insert(action(v.name))">
                {{ action(v.name) }}
              </button>
            </dt>
            <dd>{{ v.description }}</dd>
          </template>
        </dl>
      </details>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from "vue";
import { profilesApi } from "../../services/api";
import { highlightTemplate, problemRange } from "./templateHighlight";
import type { SystemPromptInfo, TemplateProblem } from "../../types";

/** "" means the built-in prompt. */
const prompt = defineModel<string>({ required: true });
const emit = defineEmits<{
  /** What's wrong with the template, as of the last check; null if nothing. */
  (e: "problem", problem: TemplateProblem | null): void;
}>();

// Loaded once: it changes only with Shelley.
let infoLoad: Promise<SystemPromptInfo> | null = null;
const info = ref<SystemPromptInfo | null>(null);
const custom = ref(prompt.value !== "");
// The custom text, kept while Built-in is chosen so choosing Custom again
// brings it back.
const text = ref(prompt.value);
// The last check, of the text it checked. Its problem stays shown while
// newer text waits for its check, but stops counting against saving.
const checked = ref<{ text: string; problem: TemplateProblem | null }>({ text: "", problem: null });
const problem = computed(() => checked.value.problem);
const textareaRef = ref<HTMLTextAreaElement | null>(null);
const preRef = ref<HTMLPreElement | null>(null);
const problemId = useId();

const highlighted = computed(() => highlightTemplate(text.value, problem.value));

async function loadInfo(): Promise<SystemPromptInfo> {
  infoLoad ??= profilesApi.systemPrompt();
  return (info.value = await infoLoad);
}
loadInfo().catch(() => (infoLoad = null));

watch([custom, text], () => {
  prompt.value = custom.value ? text.value : "";
});

async function setCustom(on: boolean) {
  custom.value = on;
  if (!on || text.value) return;
  const { template } = await loadInfo();
  // Unless something was typed meanwhile.
  if (custom.value && !text.value) text.value = template;
}

// Check a pause after typing stops; a newer check supersedes an older one.
let timer: ReturnType<typeof setTimeout> | undefined;
let inflight: AbortController | null = null;
watch(
  prompt,
  (value) => {
    clearTimeout(timer);
    inflight?.abort();
    if (!value) {
      checked.value = { text: value, problem: null };
      return;
    }
    timer = setTimeout(async () => {
      const controller = (inflight = new AbortController());
      try {
        checked.value = {
          text: value,
          problem: await profilesApi.checkSystemPrompt(value, controller.signal),
        };
      } catch {
        // Saving checks it again, and says why it can't.
        if (!controller.signal.aborted) checked.value = { text: value, problem: null };
      }
    }, 300);
  },
  { immediate: true },
);
watch(
  () => (checked.value.text === prompt.value ? checked.value.problem : null),
  (p) => emit("problem", p),
);
onBeforeUnmount(() => {
  clearTimeout(timer);
  inflight?.abort();
});

function showProblem() {
  const el = textareaRef.value;
  if (!el || !problem.value) return;
  const [a, b] = problemRange(text.value, problem.value);
  el.focus({ preventScroll: true });
  el.setSelectionRange(a, b);
  preRef.value?.querySelector(".tpl-error")?.scrollIntoView({ block: "center" });
}

// How a template refers to a variable: {{.Name}}.
function action(name: string): string {
  return `{{${name}}}`;
}

async function insert(snippet: string) {
  const el = textareaRef.value;
  if (!el) return;
  const at = el.selectionStart;
  text.value = text.value.slice(0, at) + snippet + text.value.slice(el.selectionEnd);
  await nextTick();
  el.focus();
  el.setSelectionRange(at + snippet.length, at + snippet.length);
}
</script>
