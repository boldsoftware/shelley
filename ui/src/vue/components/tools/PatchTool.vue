<!-- Vue port of components/PatchTool.tsx. Uses @pierre/diffs SSR module
     (preloadPatchDiff / preloadDiffHTML) to render syntax-highlighted diffs
     as HTML strings, avoiding the React-only component bindings.

     Approach: The @pierre/diffs/ssr module provides preloadPatchDiff (for
     unified diff strings) and preloadDiffHTML (for oldFile/newFile snapshots)
     which generate complete HTML+CSS strings for the diff. We render these
     via v-html. No WorkerPoolContext needed — the SSR codepath uses the
     DiffHunksRenderer directly (synchronous shiki highlighting via WASM).

     Error boundary: Instead of a React class component, we use a try/catch
     around the async render and fall back to a raw <pre> on failure.

     Preserves: .patch-tool, .patch-tool-details, .patch-tool-header,
     .patch-tool-toggle, .patch-tool-emoji, data-testid tool-call-completed,
     and all other classes from the React original. -->
<template>
  <div class="patch-tool" :data-testid="isComplete ? 'tool-call-completed' : 'tool-call-running'">
    <div class="patch-tool-header" @click="isExpanded = !isExpanded">
      <div class="patch-tool-summary">
        <span class="patch-tool-emoji" :class="{ running: isRunning }">🖋️</span>
        <span class="patch-tool-filename" :title="filename">{{ filename }}</span>
        <span v-if="isComplete && hasError" class="patch-tool-error">✗</span>
        <span v-if="isComplete && !hasError" class="patch-tool-success">✓</span>
      </div>
      <div class="patch-tool-header-controls">
        <button
          v-if="showDiffToggle"
          class="patch-tool-diff-mode-toggle"
          :title="sideBySide ? 'Switch to inline diff' : 'Switch to side-by-side diff'"
          @click.stop="toggleSideBySide"
        >
          <svg
            width="14"
            height="14"
            viewBox="0 0 14 14"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
          >
            <template v-if="sideBySide">
              <!-- Side-by-side icon (two columns) -->
              <rect
                x="1"
                y="2"
                width="5"
                height="10"
                rx="1"
                stroke="currentColor"
                stroke-width="1.5"
                fill="none"
              />
              <rect
                x="8"
                y="2"
                width="5"
                height="10"
                rx="1"
                stroke="currentColor"
                stroke-width="1.5"
                fill="none"
              />
            </template>
            <template v-else>
              <!-- Inline icon (single column with horizontal lines) -->
              <rect
                x="2"
                y="2"
                width="10"
                height="10"
                rx="1"
                stroke="currentColor"
                stroke-width="1.5"
                fill="none"
              />
              <line x1="4" y1="5" x2="10" y2="5" stroke="currentColor" stroke-width="1.5" />
              <line x1="4" y1="9" x2="10" y2="9" stroke="currentColor" stroke-width="1.5" />
            </template>
          </svg>
        </button>
        <button
          class="patch-tool-toggle"
          :aria-label="isExpanded ? 'Collapse' : 'Expand'"
          :aria-expanded="isExpanded"
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            class="tool-chevron"
            :class="{ 'tool-chevron-expanded': isExpanded }"
          >
            <path
              d="M4.5 3L7.5 6L4.5 9"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </button>
      </div>
    </div>

    <div v-if="isExpanded" class="patch-tool-details">
      <div v-if="isComplete && !hasError && hasDiff" class="patch-tool-section">
        <div class="patch-tool-diffs-container">
          <div v-if="diffHtml" v-html="diffHtml" />
          <pre v-else-if="diffError && rawDiff" class="patch-tool-raw-diff">{{ rawDiff }}</pre>
        </div>
      </div>

      <div v-if="isComplete && hasError" class="patch-tool-section">
        <pre class="patch-tool-error-message">{{ errorMessage || "Patch failed" }}</pre>
      </div>

      <div v-if="isRunning" class="patch-tool-section">
        <div class="patch-tool-label">Applying patch...</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, onMounted, onUnmounted } from "vue";
import type { LLMContent } from "../../../types";
import type { FileContents, SupportedLanguages, ThemeTypes, ThemesType } from "@pierre/diffs";
import { preloadPatchDiff, preloadDiffHTML } from "@pierre/diffs/ssr";
import { isDarkModeActive } from "../../../services/theme";

// LocalStorage key for side-by-side preference
const STORAGE_KEY_SIDE_BY_SIDE = "shelley-diff-side-by-side";

function getSideBySidePreference(): boolean {
  try {
    const stored = localStorage.getItem(STORAGE_KEY_SIDE_BY_SIDE);
    if (stored !== null) {
      return stored === "true";
    }
    return window.innerWidth >= 768;
  } catch {
    return window.innerWidth >= 768;
  }
}

function setSideBySidePreference(value: boolean): void {
  try {
    localStorage.setItem(STORAGE_KEY_SIDE_BY_SIDE, value ? "true" : "false");
  } catch {
    // Ignore storage errors
  }
}

interface PatchDisplayData {
  path: string;
  diff?: string;
  oldContent?: string;
  newContent?: string;
}

const DIFF_THEMES: ThemesType = { dark: "github-dark", light: "github-light" };

// Map file extension to language for syntax highlighting
function getLanguageFromPath(path: string): SupportedLanguages {
  const ext = path.split(".").pop()?.toLowerCase() || "";
  const langMap: Record<string, SupportedLanguages> = {
    ts: "typescript",
    tsx: "tsx",
    js: "javascript",
    jsx: "jsx",
    py: "python",
    rb: "ruby",
    go: "go",
    rs: "rust",
    java: "java",
    c: "c",
    cpp: "cpp",
    h: "c",
    hpp: "cpp",
    cs: "csharp",
    php: "php",
    swift: "swift",
    kt: "kotlin",
    scala: "scala",
    sh: "bash",
    bash: "bash",
    zsh: "bash",
    fish: "fish",
    ps1: "powershell",
    sql: "sql",
    html: "html",
    htm: "html",
    css: "css",
    scss: "scss",
    sass: "sass",
    less: "less",
    json: "json",
    xml: "xml",
    yaml: "yaml",
    yml: "yaml",
    toml: "toml",
    ini: "ini",
    md: "markdown",
    markdown: "markdown",
    txt: "text",
    dockerfile: "dockerfile",
    makefile: "makefile",
    cmake: "cmake",
    lua: "lua",
    perl: "perl",
    r: "r",
    vue: "vue",
    svelte: "svelte",
    astro: "astro",
  };
  return langMap[ext] || "text";
}

const props = defineProps<{
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
  onCommentTextChange?: (text: string) => void;
}>();

// State
const isExpanded = ref(!props.hasError);
const isMobile = ref(window.innerWidth < 768);
const sideBySide = ref(!isMobile.value && getSideBySidePreference());
const diffHtml = ref<string | null>(null);
const diffError = ref(false);

// Reactive theme tracking (mirrors the React useThemeType hook)
const themeType = ref<ThemeTypes>(isDarkModeActive() ? "dark" : "light");

let themeObserver: MutationObserver | null = null;
onMounted(() => {
  themeObserver = new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      if (mutation.attributeName === "class") {
        themeType.value = isDarkModeActive() ? "dark" : "light";
      }
    }
  });
  themeObserver.observe(document.documentElement, { attributes: true });
});
onUnmounted(() => {
  themeObserver?.disconnect();
});

// Viewport resize handler
function handleResize() {
  const mobile = window.innerWidth < 768;
  isMobile.value = mobile;
  if (mobile) {
    sideBySide.value = false;
  }
}

onMounted(() => {
  window.addEventListener("resize", handleResize);
});
onUnmounted(() => {
  window.removeEventListener("resize", handleResize);
});

function toggleSideBySide() {
  const newValue = !sideBySide.value;
  sideBySide.value = newValue;
  setSideBySidePreference(newValue);
}

// Computed properties
const path = computed(() => {
  const ti = props.toolInput;
  if (
    typeof ti === "object" &&
    ti !== null &&
    "path" in ti &&
    typeof (ti as { path: unknown }).path === "string"
  ) {
    return (ti as { path: string }).path;
  }
  return typeof ti === "string" ? ti : "";
});

const displayData = computed<PatchDisplayData | null>(() => {
  const d = props.display;
  if (d && typeof d === "object" && "path" in d) {
    return d as PatchDisplayData;
  }
  return null;
});

const errorMessage = computed(() =>
  props.toolResult && props.toolResult.length > 0 && props.toolResult[0].Text
    ? props.toolResult[0].Text
    : "",
);

const isComplete = computed(() => !props.isRunning && props.toolResult !== undefined);

const hasDiff = computed(
  () =>
    displayData.value != null &&
    (displayData.value.diff ||
      (displayData.value.oldContent != null && displayData.value.newContent != null)),
);

const filename = computed(() => displayData.value?.path || path.value || "patch");

const showDiffToggle = computed(
  () => !isMobile.value && isExpanded.value && isComplete.value && !props.hasError && hasDiff.value,
);

// Raw diff string for fallback display
const rawDiff = computed(() => {
  if (!displayData.value) return "";
  return displayData.value.diff ?? "";
});

// Render diff HTML using @pierre/diffs SSR
async function renderDiff(): Promise<string | null> {
  const dd = displayData.value;
  if (!dd) return null;

  const diffStyle = sideBySide.value ? ("split" as const) : ("unified" as const);
  const options = {
    diffStyle,
    theme: DIFF_THEMES,
    themeType: themeType.value,
    disableFileHeader: true,
  };

  try {
    // Legacy payloads with full file snapshots
    if (dd.oldContent != null && dd.newContent != null) {
      const lang = getLanguageFromPath(dd.path);
      const oldFile: FileContents = { name: dd.path, contents: dd.oldContent, lang };
      const newFile: FileContents = { name: dd.path, contents: dd.newContent, lang };
      return await preloadDiffHTML({ oldFile, newFile, options });
    }

    // New payloads with unified diff string
    if (dd.diff) {
      const result = await preloadPatchDiff({ patch: dd.diff, options });
      return result.prerenderedHTML;
    }
  } catch (e) {
    console.warn("PatchTool diff render error:", e);
    throw e;
  }

  return null;
}

// Watch all inputs that affect diff rendering and re-render
watch(
  [displayData, sideBySide, themeType, isExpanded, isComplete],
  async () => {
    if (!isExpanded.value || !isComplete.value || props.hasError || !hasDiff.value) {
      return;
    }
    try {
      diffError.value = false;
      diffHtml.value = await renderDiff();
    } catch {
      diffError.value = true;
      diffHtml.value = null;
    }
  },
  { immediate: true },
);
</script>
