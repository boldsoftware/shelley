<!-- Vue port of components/DirectoryPickerModal.tsx. FS browse + cwd pick +
     inline mkdir. Uses the shared Modal (PrimeVue Dialog) with the #footer
     slot for the New Folder / Cancel / Select row; the directory-picker-*
     content class contract and the "New folder name" sr-only label are
     preserved. Uses api.listDirectory/createDirectory. -->
<template>
  <Modal
    :is-open="isOpen"
    title="Select Directory"
    class-name="directory-picker-modal"
    @close="emit('close')"
  >
    <div class="directory-picker-body">
      <div class="directory-picker-input-container">
        <input
          ref="inputRef"
          type="text"
          v-model="inputPath"
          class="directory-picker-input"
          placeholder="/path/to/directory"
          @keydown="handleInputKeyDown"
        />
      </div>

      <div
        v-if="displayDir"
        :class="`directory-picker-current${displayDir.git_head_subject ? ' directory-picker-current-git' : ''}`"
      >
        <span class="directory-picker-current-path">
          {{ displayDir.path }}
          <span v-if="filterPrefix" class="directory-picker-filter">/{{ filterPrefix }}*</span>
        </span>
        <span
          v-if="displayDir.git_head_subject"
          class="directory-picker-current-subject"
          :title="displayDir.git_head_subject"
        >
          {{ displayDir.git_head_subject }}
        </span>
      </div>

      <div v-if="directoryShortcuts.length" class="directory-picker-shortcuts">
        <button
          v-for="shortcut in directoryShortcuts"
          :key="shortcut.path"
          class="directory-picker-shortcut"
          :title="shortcut.path"
          @click="goToDirectory(shortcut.path)"
        >
          <svg
            v-if="shortcut.isHome"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            class="directory-picker-icon"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M3 12l2-2m0 0l7-7 7 7m-14 0v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6"
            />
          </svg>
          <svg
            v-else
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            class="directory-picker-icon"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M3 10h10a8 8 0 018 8v2M3 10l6 6m-6-6l6-6"
            />
          </svg>
          <span>{{ shortcut.label }}</span>
          <span class="directory-picker-shortcut-path">{{ shortcut.path }}</span>
        </button>
      </div>

      <div v-if="error" class="directory-picker-error">{{ error }}</div>

      <div v-if="loading" class="directory-picker-loading">
        <div class="spinner spinner-small"></div>
        <span>Loading...</span>
      </div>

      <div v-if="!loading && !error" class="directory-picker-list">
        <button
          v-if="showParent"
          class="directory-picker-entry directory-picker-entry-parent"
          @click="handleParentClick"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="directory-picker-icon">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M11 17l-5-5m0 0l5-5m-5 5h12"
            />
          </svg>
          <span>..</span>
        </button>

        <button
          v-for="entry in visibleEntries"
          :key="entry.name"
          :class="`directory-picker-entry${entry.git_head_subject ? ' directory-picker-entry-git' : ''}`"
          @click="handleEntryClick(entry)"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="directory-picker-icon">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
            />
          </svg>
          <span class="directory-picker-entry-name">
            <template
              v-if="filterPrefix && entry.name.toLowerCase().startsWith(filterPrefix.toLowerCase())"
            >
              <strong>{{ entry.name.slice(0, filterPrefix.length) }}</strong
              >{{ entry.name.slice(filterPrefix.length) }}
            </template>
            <template v-else>{{ entry.name }}</template>
          </span>
          <span
            v-if="entry.git_head_subject"
            class="directory-picker-git-subject"
            :title="entry.git_head_subject"
          >
            {{ entry.git_head_subject }}
          </span>
        </button>

        <!-- Create new directory inline form -->
        <div v-if="isCreating" class="directory-picker-create-form">
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="directory-picker-icon">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M9 13h6m-3-3v6m-9 1V7a2 2 0 012-2h6l2 2h6a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"
            />
          </svg>
          <label :for="createInputId" class="sr-only">New folder name</label>
          <input
            :id="createInputId"
            ref="newDirInputRef"
            type="text"
            v-model="newDirName"
            placeholder="New folder name"
            class="directory-picker-create-input"
            :disabled="createLoading"
            @keydown="handleCreateKeyDown"
          />
          <button
            class="directory-picker-create-btn"
            :disabled="createLoading || !newDirName.trim()"
            v-tooltip.top="'Create'"
            aria-label="Create"
            @click="handleCreateDirectory"
          >
            <div v-if="createLoading" class="spinner spinner-small"></div>
            <svg v-else fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M5 13l4 4L19 7"
              />
            </svg>
          </button>
          <button
            class="directory-picker-create-btn directory-picker-cancel-btn"
            :disabled="createLoading"
            v-tooltip.top="'Cancel'"
            aria-label="Cancel"
            @click="handleCancelCreate"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>

        <div v-if="createError" class="directory-picker-create-error">{{ createError }}</div>

        <div v-if="hiddenEntryCount > 0" class="directory-picker-truncated">
          {{ hiddenEntryCount.toLocaleString() }} more — type to filter
        </div>

        <div
          v-if="filteredEntries.length === 0 && !showParent && !isCreating"
          class="directory-picker-empty"
        >
          {{ filterPrefix ? "No matching directories" : "No subdirectories" }}
        </div>
      </div>
    </div>

    <!-- Footer -->
    <template #footer>
      <button
        v-if="!isCreating && !loading && !error"
        class="btn directory-picker-new-btn"
        v-tooltip.top="'Create new folder'"
        @click="handleStartCreate"
      >
        <svg
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
          class="directory-picker-new-icon"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M9 13h6m-3-3v6m-9 1V7a2 2 0 012-2h6l2 2h6a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"
          />
        </svg>
        New Folder
      </button>
      <div class="directory-picker-footer-spacer"></div>
      <Button label="Cancel" text severity="secondary" @click="emit('close')" />
      <Button label="Select" :disabled="loading || !!error" @click="handleSelect" />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import { api } from "../../services/api";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import { isImeComposing } from "../../utils/imeComposing";

interface DirectoryEntry {
  name: string;
  is_dir: boolean;
  git_head_subject?: string;
}

interface CachedDirectory {
  path: string;
  parent: string;
  entries: DirectoryEntry[];
  git_head_subject?: string;
  git_repo_root?: string;
  git_worktree_root?: string;
}

interface DirectoryShortcut {
  label: string;
  path: string;
  isHome?: boolean;
}

const props = defineProps<{
  isOpen: boolean;
  initialPath?: string;
  foldersOnly?: boolean;
}>();
const emit = defineEmits<{ (e: "close"): void; (e: "select", path: string): void }>();

const createInputId = useId();

const inputPath = ref(
  props.initialPath
    ? props.initialPath.endsWith("/")
      ? props.initialPath
      : props.initialPath + "/"
    : "",
);
const loading = ref(false);
const error = ref<string | null>(null);
const inputRef = ref<HTMLInputElement | null>(null);

const isCreating = ref(false);
const newDirName = ref("");
const createError = ref<string | null>(null);
const createLoading = ref(false);
const newDirInputRef = ref<HTMLInputElement | null>(null);

const cache = new Map<string, CachedDirectory>();

const displayDir = ref<CachedDirectory | null>(null);
const filterPrefix = ref("");
let loadSequence = 0;

function parseInputPath(path: string): { dirPath: string; prefix: string } {
  if (!path) return { dirPath: "", prefix: "" };
  if (path.endsWith("/")) return { dirPath: path.slice(0, -1) || "/", prefix: "" };
  const lastSlash = path.lastIndexOf("/");
  if (lastSlash === -1) return { dirPath: "", prefix: path };
  if (lastSlash === 0) return { dirPath: "/", prefix: path.slice(1) };
  return { dirPath: path.slice(0, lastSlash), prefix: path.slice(lastSlash + 1) };
}

async function loadDirectory(path: string): Promise<CachedDirectory> {
  const normalizedPath = path || "/";
  const cached = cache.get(normalizedPath);
  if (cached) return cached;

  const result = await api.listDirectory(path || undefined);
  if (result.error) throw new Error(result.error);
  const dirData: CachedDirectory = {
    path: result.path,
    parent: result.parent,
    entries: result.entries || [],
    git_head_subject: result.git_head_subject,
    git_repo_root: result.git_repo_root,
    git_worktree_root: result.git_worktree_root,
  };
  cache.set(result.path, dirData);
  return dirData;
}

const homeDir = window.__SHELLEY_INIT__?.home_dir || "";
const directoryShortcuts = computed<DirectoryShortcut[]>(() => {
  const shortcuts: DirectoryShortcut[] = [];
  const addShortcut = (shortcut: DirectoryShortcut) => {
    if (!shortcuts.some(({ path }) => path === shortcut.path)) shortcuts.push(shortcut);
  };
  const dir = displayDir.value;
  const inputDir = parseInputPath(inputPath.value).dirPath;
  const browsingHome = !error.value && dir?.path === homeDir && (!inputDir || inputDir === homeDir);
  if (homeDir && !browsingHome) {
    addShortcut({ label: "Go to home directory", path: homeDir, isHome: true });
  }
  if (dir?.git_repo_root && dir.git_repo_root !== dir.path) {
    addShortcut({ label: "Go to git worktree root", path: dir.git_repo_root });
  }
  if (dir?.git_worktree_root && dir.git_worktree_root !== dir.path) {
    addShortcut({ label: "Go to git root", path: dir.git_worktree_root });
  }
  return shortcuts;
});

const filteredEntries = computed(
  () =>
    displayDir.value?.entries.filter((entry) => {
      if (props.foldersOnly && !entry.is_dir) return false;
      if (!filterPrefix.value) return true;
      return entry.name.toLowerCase().startsWith(filterPrefix.value.toLowerCase());
    }) || [],
);

// Cap how many rows we render. Directories like /tmp on a busy box can have
// tens of thousands of entries; rendering a DOM row (button + SVG) for each
// freezes the tab for seconds. Typing narrows the filter, so showing the
// first N plus a "keep typing" hint is strictly more usable than a hang.
const MAX_VISIBLE_ENTRIES = 500;
const visibleEntries = computed(() => filteredEntries.value.slice(0, MAX_VISIBLE_ENTRIES));
const hiddenEntryCount = computed(() =>
  Math.max(0, filteredEntries.value.length - MAX_VISIBLE_ENTRIES),
);

const showParent = computed(() => !!displayDir.value?.parent && displayDir.value.parent !== "");

function handleEntryClick(entry: DirectoryEntry) {
  if (entry.is_dir) {
    const basePath = displayDir.value?.path || "";
    inputPath.value = basePath === "/" ? `/${entry.name}/` : `${basePath}/${entry.name}/`;
  }
}

function goToDirectory(path: string) {
  inputPath.value = path === "/" ? "/" : `${path}/`;
}

function handleParentClick() {
  if (displayDir.value?.parent) {
    goToDirectory(displayDir.value.parent);
  }
}

function handleInputKeyDown(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  if (e.key === "Enter") {
    e.preventDefault();
    handleSelect();
  }
}

function handleSelect() {
  const { dirPath } = parseInputPath(inputPath.value);
  const selectedPath = inputPath.value.endsWith("/") ? (dirPath === "/" ? "/" : dirPath) : dirPath;
  emit("select", selectedPath || displayDir.value?.path || "");
  emit("close");
}

function handleStartCreate() {
  isCreating.value = true;
  newDirName.value = "";
  createError.value = null;
}

function handleCancelCreate() {
  isCreating.value = false;
  newDirName.value = "";
  createError.value = null;
}

async function handleCreateDirectory() {
  if (!newDirName.value.trim()) {
    createError.value = "Directory name is required";
    return;
  }
  if (newDirName.value.includes("/") || newDirName.value.includes("\\")) {
    createError.value = "Directory name cannot contain slashes";
    return;
  }
  const basePath = displayDir.value?.path || "/";
  const newPath = basePath === "/" ? `/${newDirName.value}` : `${basePath}/${newDirName.value}`;

  createLoading.value = true;
  createError.value = null;
  try {
    const result = await api.createDirectory(newPath);
    if (result.error) {
      createError.value = result.error;
      return;
    }
    cache.delete(basePath);
    isCreating.value = false;
    newDirName.value = "";
    inputPath.value = newPath + "/";
  } catch (err) {
    createError.value = err instanceof Error ? err.message : "Failed to create directory";
  } finally {
    createLoading.value = false;
  }
}

function handleCreateKeyDown(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  if (e.key === "Enter") {
    e.preventDefault();
    handleCreateDirectory();
  } else if (e.key === "Escape") {
    e.preventDefault();
    // Keep the Escape local: cancel create mode without letting the shared
    // modal Escape stack close the whole dialog.
    e.stopPropagation();
    handleCancelCreate();
  }
}

// Update display when input changes (while open).
watch(
  [() => props.isOpen, inputPath],
  ([open]) => {
    const requestSequence = ++loadSequence;
    if (!open) {
      loading.value = false;
      return;
    }
    const { dirPath, prefix } = parseInputPath(inputPath.value);
    filterPrefix.value = prefix;
    loading.value = true;
    error.value = null;
    loadDirectory(dirPath)
      .then((dir) => {
        if (requestSequence !== loadSequence) return;
        displayDir.value = dir;
      })
      .catch((err) => {
        if (requestSequence !== loadSequence) return;
        error.value = err instanceof Error ? err.message : "Failed to load directory";
      })
      .finally(() => {
        if (requestSequence === loadSequence) loading.value = false;
      });
  },
  { immediate: true },
);

// Initialize + focus when modal opens.
watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      if (!props.initialPath) {
        inputPath.value = "";
      } else {
        inputPath.value = props.initialPath.endsWith("/")
          ? props.initialPath
          : props.initialPath + "/";
      }
      cache.clear();
      nextTick(() => {
        const el = inputRef.value;
        if (!el) return;
        const isMobile =
          window.matchMedia("(max-width: 768px)").matches || "ontouchstart" in window;
        if (!isMobile) {
          el.focus();
          const len = el.value.length;
          el.setSelectionRange(len, len);
        }
      });
    }
  },
);

// Focus the new-directory input when entering create mode.
watch(isCreating, (creating) => {
  if (creating) nextTick(() => newDirInputRef.value?.focus());
});
</script>
