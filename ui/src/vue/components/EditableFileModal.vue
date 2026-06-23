<!-- Vue port of components/EditableFileModal.tsx. Monaco-based file editor
     modal teleported to <body>. Preserves the diff-viewer-overlay/container/
     header/content/editor/close class contract, role="presentation"/"dialog",
     aria-modal, the aria-label "Edit file" (or the given title), and the
     agents-md-* header/save-status classes. Loads Monaco via services/monaco,
     auto-saves (debounced) to /api/write-file, and wires vim via
     useMonacoVim + <VimToggle>. Emits "close" (React onClose) and
     "saved" (React onSaved, with the saved content). -->
<template>
  <Teleport v-if="isOpen" to="body">
    <div class="diff-viewer-overlay" role="presentation">
      <div
        class="diff-viewer-container"
        role="dialog"
        aria-modal="true"
        :aria-label="title || 'Edit file'"
      >
        <div class="diff-viewer-header">
          <div class="diff-viewer-header-row">
            <span class="agents-md-header-title">{{ title || "Edit file" }}</span>
            <code class="agents-md-header-path">{{ path }}</code>
            <span
              v-if="saveStatus !== 'idle'"
              :class="`agents-md-save-status agents-md-save-${saveStatus}`"
            >
              <template v-if="saveStatus === 'saving'">Saving...</template>
              <template v-else-if="saveStatus === 'saved'">Saved</template>
              <template v-else-if="saveStatus === 'error'">Error saving</template>
            </span>
            <VimToggle v-if="isDesktop" :enabled="vimEnabled" @change="setVimEnabled" />
            <button class="diff-viewer-close" title="Close (Esc)" @click="emit('close')">×</button>
          </div>
        </div>
        <div class="diff-viewer-content">
          <div v-if="loadStatus === 'error'" class="diff-viewer-loading">
            <span>Failed to load {{ path }}. Editing is disabled.</span>
          </div>
          <template v-else>
            <div v-if="!monacoLoaded || loadStatus !== 'loaded'" class="diff-viewer-loading">
              <div class="spinner"></div>
              <span>Loading editor...</span>
            </div>
            <div
              ref="containerRef"
              class="diff-viewer-editor"
              :style="{
                display:
                  monacoLoaded && content !== null && loadStatus === 'loaded' ? 'block' : 'none',
              }"
            />
            <div v-if="isDesktop && vimEnabled" ref="vimStatusRef" class="monaco-vim-status" />
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, shallowRef, watch } from "vue";
import type * as Monaco from "monaco-editor";
import { loadMonaco } from "../../services/monaco";
import { isDarkModeActive } from "../../services/theme";
import { useVimEnabled, useMonacoVim } from "../composables/monacoVim";
import VimToggle from "./VimToggle.vue";

type SaveStatus = "idle" | "saving" | "saved" | "error";
type LoadStatus = "loading" | "loaded" | "error";

const props = withDefaults(
  defineProps<{
    isOpen: boolean;
    path: string;
    title?: string;
    language?: string;
    loadUrl?: string;
  }>(),
  { language: "markdown" },
);
const emit = defineEmits<{
  (e: "close"): void;
  (e: "saved", content: string): void;
}>();

const content = ref<string | null>(null);
const loadStatus = ref<LoadStatus>("loading");
const monacoLoaded = ref(false);
const saveStatus = ref<SaveStatus>("idle");

// Monaco editor instances must NOT be deeply reactive: a plain ref() proxies
// the editor's huge internal object graph, so vim mode (which drives the editor
// hard on every keystroke) pegs the main thread and hangs the page. shallowRef
// tracks the reference swap (create/dispose) without proxying internals.
const editor = shallowRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
let monacoMod: typeof Monaco | null = null;
const containerRef = ref<HTMLDivElement | null>(null);
const vimStatusRef = ref<HTMLDivElement | null>(null);
let saveTimeout: number | null = null;
let statusTimeout: number | null = null;
const [vimEnabledRef, setVimEnabledFn] = useVimEnabled();
const vimEnabled = vimEnabledRef;
function setVimEnabled(v: boolean) {
  setVimEnabledFn(v);
}
const isDesktop = ref(window.innerWidth >= 768);

function onResize() {
  isDesktop.value = window.innerWidth >= 768;
}

// --- File load (when opened / path / loadUrl change) ---
watch(
  () => [props.isOpen, props.path, props.loadUrl] as const,
  () => {
    if (!props.isOpen) return;
    let cancelled = false;
    loadStatus.value = "loading";
    (async () => {
      try {
        const response = await fetch(
          props.loadUrl || `/api/read?path=${encodeURIComponent(props.path)}`,
        );
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const text = props.loadUrl
          ? ((await response.json()) as { content: string }).content
          : await response.text();
        if (cancelled) return;
        content.value = text ?? "";
        loadStatus.value = "loaded";
      } catch (err) {
        console.error("Failed to load editable file:", err);
        if (!cancelled) loadStatus.value = "error";
      }
    })();
    // store cancel on a ref-keyed closure via the watcher's cleanup
    currentLoadCancel = () => {
      cancelled = true;
    };
  },
  { immediate: true },
);
let currentLoadCancel: (() => void) | null = null;

// --- Load Monaco when opened ---
watch(
  () => [props.isOpen, monacoLoaded.value] as const,
  () => {
    if (props.isOpen && !monacoLoaded.value) {
      loadMonaco()
        .then((monaco) => {
          monacoMod = monaco;
          monacoLoaded.value = true;
        })
        .catch((err) => console.error("Failed to load Monaco:", err));
    }
  },
  { immediate: true },
);

async function saveContent(text: string) {
  if (!props.path) return;
  if (statusTimeout) clearTimeout(statusTimeout);
  try {
    saveStatus.value = "saving";
    const response = await fetch("/api/write-file", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: props.path, content: text }),
    });
    if (response.ok) {
      emit("saved", text);
    }
    saveStatus.value = response.ok ? "saved" : "error";
    statusTimeout = window.setTimeout(() => (saveStatus.value = "idle"), response.ok ? 2000 : 3000);
  } catch (err) {
    console.error("Failed to save:", err);
    saveStatus.value = "error";
    statusTimeout = window.setTimeout(() => (saveStatus.value = "idle"), 3000);
  }
}

function scheduleSave(text: string) {
  if (saveTimeout) clearTimeout(saveTimeout);
  saveTimeout = window.setTimeout(() => {
    saveContent(text);
    saveTimeout = null;
  }, 1000);
}

// Quit handler for vim's :q / :wq / :x and ZZ / ZQ. Flush any pending
// debounced save synchronously when the user asks to save+quit so the modal
// closes only after the latest content has been persisted.
function handleVimQuit({ save }: { save: boolean }) {
  if (save && editor.value) {
    if (saveTimeout) {
      clearTimeout(saveTimeout);
      saveTimeout = null;
    }
    saveContent(editor.value.getValue());
  }
  emit("close");
}

useMonacoVim(
  () => editor.value,
  () => vimStatusRef.value,
  () => isDesktop.value && vimEnabled.value,
  handleVimQuit,
);

// --- Create the editor once content + monaco are ready ---
watch(
  () => [monacoLoaded.value, content.value, props.language] as const,
  () => {
    if (!monacoLoaded.value || content.value === null || !containerRef.value || !monacoMod) return;
    if (editor.value) return;

    const monaco = monacoMod;
    const nextEditor = monaco.editor.create(containerRef.value, {
      value: content.value,
      language: props.language,
      theme: isDarkModeActive() ? "vs-dark" : "vs",
      minimap: { enabled: false },
      wordWrap: "on",
      lineNumbers: "on",
      scrollBeyondLastLine: false,
      automaticLayout: true,
      fontSize: 14,
      padding: { top: 8 },
    });
    editor.value = nextEditor;

    nextEditor.onDidChangeModelContent(() => scheduleSave(nextEditor.getValue()));
    nextEditor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      if (saveTimeout) {
        clearTimeout(saveTimeout);
        saveTimeout = null;
      }
      saveContent(nextEditor.getValue());
    });
  },
  { immediate: true, flush: "post" },
);

function disposeEditor() {
  if (saveTimeout) {
    clearTimeout(saveTimeout);
    saveTimeout = null;
  }
  if (editor.value) {
    editor.value.dispose();
    editor.value = null;
  }
}

// --- Theme observer ---
let themeObserver: MutationObserver | null = null;
watch(
  () => monacoLoaded.value,
  () => {
    if (!monacoMod) return;
    themeObserver?.disconnect();
    const updateTheme = () => monacoMod?.editor.setTheme(isDarkModeActive() ? "vs-dark" : "vs");
    themeObserver = new MutationObserver((mutations) => {
      for (const mutation of mutations) if (mutation.attributeName === "class") updateTheme();
    });
    themeObserver.observe(document.documentElement, { attributes: true });
  },
  { immediate: true },
);

// --- Escape handling (capture phase; vim-aware guard) ---
function handleKeyDown(e: KeyboardEvent) {
  if (e.key !== "Escape") return;
  // If vim mode is in a non-normal mode (insert/visual/...), let monaco-vim
  // handle Escape (to drop back to normal) instead of closing the modal.
  const vimFocused =
    containerRef.value?.contains(document.activeElement) ||
    vimStatusRef.value?.contains(document.activeElement);
  if (
    isDesktop.value &&
    vimEnabled.value &&
    vimFocused &&
    (vimStatusRef.value?.textContent ?? "").trim() !== ""
  ) {
    return;
  }
  emit("close");
}

// --- React to open/close: attach/detach Escape + reset on close ---
watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      window.addEventListener("keydown", handleKeyDown, true);
    } else {
      window.removeEventListener("keydown", handleKeyDown, true);
      // Flush a pending debounced save before tearing down.
      if (saveTimeout && editor.value) {
        clearTimeout(saveTimeout);
        saveTimeout = null;
        saveContent(editor.value.getValue());
      }
      if (statusTimeout) {
        clearTimeout(statusTimeout);
        statusTimeout = null;
      }
      currentLoadCancel?.();
      disposeEditor();
      content.value = null;
    }
  },
);

onMounted(() => {
  window.addEventListener("resize", onResize);
  if (props.isOpen) window.addEventListener("keydown", handleKeyDown, true);
});
onUnmounted(() => {
  window.removeEventListener("resize", onResize);
  window.removeEventListener("keydown", handleKeyDown, true);
  themeObserver?.disconnect();
  disposeEditor();
});
</script>
