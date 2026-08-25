<!-- Server-based fuzzy file finder. Lists files under a working directory and
     ranks them against the query on the server (/api/find-files), so the
     browser never needs the full file list. Selecting a file emits its
     absolute path (the parent opens EditableFileModal on it). A "change
     directory" affordance re-roots the search via DirectoryPickerModal, and a
     query that announces itself as a path (a leading /, ~, ./ or ../) re-roots
     it for that query alone — the server reports the directory it actually
     searched as `search_dir`, which results are relative to.

     Reuses the grp-* class contract from GitRepoPicker for the list chrome;
     ff-* classes cover the directory header row. -->
<template>
  <Modal
    :is-open="isOpen"
    title="Find file to edit"
    class-name="grp-modal ff-modal"
    @close="emit('close')"
  >
    <template #title-right>
      <button
        class="ff-dir-btn"
        type="button"
        :title="`Working directory: ${dir}\nClick to change`"
        @click="showDirPicker = true"
      >
        <svg
          class="grp-icon"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
          aria-hidden="true"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
          />
        </svg>
        <span class="ff-dir-path">{{ displayDir }}</span>
      </button>
    </template>

    <div class="grp-body">
      <input
        ref="inputRef"
        class="grp-filter"
        type="text"
        v-model="query"
        :placeholder="loading ? 'Searching…' : 'Filter files, or type a path…'"
        spellcheck="false"
        aria-label="Filter files"
        @keydown="handleKey"
      />

      <!-- Shown only when the query re-rooted the search, so it's clear the
           paths below aren't relative to the directory in the header chip. -->
      <div v-if="scopeDir" class="ff-scope">
        Searching <code class="ff-scope-path">{{ tildifyPath(scopeDir) }}</code>
      </div>

      <div v-if="error" class="grp-error">{{ error }}</div>

      <div class="grp-list" ref="listRef">
        <button
          v-for="(hit, idx) in matches"
          :key="hit.path"
          :data-idx="idx"
          type="button"
          :class="`grp-row${idx === activeIdx ? ' grp-row-active' : ''}`"
          @mouseenter="activeIdx = idx"
          @click="pick(hit.path)"
        >
          <svg
            class="grp-icon"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
            aria-hidden="true"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
            />
          </svg>
          <span class="grp-main">
            <span class="grp-path" :title="hit.path">
              <template
                v-for="(seg, si) in highlightSegments(hit.path, hit.matched_indexes)"
                :key="si"
              >
                <mark v-if="seg.hit" class="grp-hit">{{ seg.text }}</mark>
                <template v-else>{{ seg.text }}</template>
              </template>
            </span>
          </span>
        </button>

        <div v-if="!loading && matches.length === 0 && !error" class="grp-empty">
          {{ matchQuery ? "No matching files." : "No files in this directory." }}
        </div>
        <div v-if="loading && matches.length === 0" class="grp-empty">Searching…</div>
      </div>

      <div v-if="truncated" class="grp-truncated">Showing top results — keep typing to narrow.</div>
    </div>

    <DirectoryPickerModal
      :is-open="showDirPicker"
      :initial-path="dir"
      @close="showDirPicker = false"
      @select="onDirSelected"
    />
  </Modal>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import Modal from "./Modal.vue";
import DirectoryPickerModal from "./DirectoryPickerModal.vue";
import { api } from "../../services/api";
import { isImeComposing } from "../../utils/imeComposing";
import { tildifyPath } from "../../utils/tildify";

interface FileMatch {
  path: string;
  matched_indexes?: number[];
}

const props = defineProps<{
  isOpen: boolean;
  initialDir: string;
}>();
const emit = defineEmits<{ (e: "close"): void; (e: "select", absPath: string): void }>();

const dir = ref(props.initialDir);
// Directory the last response's matches are relative to. Equals dir unless the
// query named a path, in which case the server re-rooted the search there.
const searchDir = ref(props.initialDir);
// The part of the query the server actually matched: empty when it listed a
// whole directory, which is a different kind of "nothing found" to report.
const matchQuery = ref("");
const query = ref("");
const matches = ref<FileMatch[]>([]);
const loading = ref(false);
const error = ref<string | null>(null);
const truncated = ref(false);
const activeIdx = ref(0);
const showDirPicker = ref(false);
const inputRef = ref<HTMLInputElement | null>(null);
const listRef = ref<HTMLDivElement | null>(null);

let searchTimeout: number | null = null;
let abortController: AbortController | null = null;

const displayDir = computed(() => tildifyPath(dir.value));
// Non-null only while a path query has moved the search off the working
// directory; that's the case the user needs told about.
const scopeDir = computed(() => (searchDir.value === dir.value ? null : searchDir.value));

// Split a path into highlighted/plain segments using the server-provided
// rune offsets. Contiguous matched indexes are coalesced into one <mark>.
function highlightSegments(text: string, positions?: number[]): { text: string; hit: boolean }[] {
  if (!positions || positions.length === 0) return [{ text, hit: false }];
  const sorted = [...positions].sort((a, b) => a - b);
  const out: { text: string; hit: boolean }[] = [];
  let cursor = 0;
  let i = 0;
  while (i < sorted.length) {
    let j = i;
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j] + 1) j++;
    const start = sorted[i];
    const end = sorted[j] + 1;
    if (start > cursor) out.push({ text: text.slice(cursor, start), hit: false });
    out.push({ text: text.slice(start, end), hit: true });
    cursor = end;
    i = j + 1;
  }
  if (cursor < text.length) out.push({ text: text.slice(cursor), hit: false });
  return out;
}

async function runSearch() {
  if (!dir.value) return;
  abortController?.abort();
  const controller = new AbortController();
  abortController = controller;
  loading.value = true;
  error.value = null;
  try {
    const res = await api.findFiles(dir.value, query.value.trim(), controller.signal);
    if (controller.signal.aborted) return;
    // The server resolves an empty/relative dir (e.g. to $HOME) and echoes
    // the absolute path back; adopt it so joinPath produces valid paths.
    dir.value = res.dir;
    // Matches are relative to search_dir, which a path query moves elsewhere.
    searchDir.value = res.search_dir;
    matchQuery.value = res.match_query;
    matches.value = res.matches;
    truncated.value = res.truncated;
    activeIdx.value = 0;
  } catch (err) {
    if (controller.signal.aborted || (err as Error).name === "AbortError") return;
    error.value = err instanceof Error ? err.message : String(err);
    // Nothing was searched, so don't leave the previous scope line claiming
    // otherwise next to the error.
    searchDir.value = dir.value;
    matches.value = [];
  } finally {
    if (abortController === controller) {
      loading.value = false;
      abortController = null;
    }
  }
}

function scheduleSearch() {
  if (searchTimeout) clearTimeout(searchTimeout);
  searchTimeout = window.setTimeout(() => {
    void runSearch();
    searchTimeout = null;
  }, 120);
}

watch(query, scheduleSearch);

// When opened: reset to the requested directory and focus the filter.
watch(
  () => props.isOpen,
  (open) => {
    if (!open) {
      abortController?.abort();
      if (searchTimeout) {
        clearTimeout(searchTimeout);
        searchTimeout = null;
      }
      return;
    }
    dir.value = props.initialDir;
    searchDir.value = props.initialDir;
    query.value = "";
    matchQuery.value = "";
    matches.value = [];
    error.value = null;
    activeIdx.value = 0;
    void runSearch();
    nextTick(() => inputRef.value?.focus());
  },
  { immediate: true },
);

function joinPath(base: string, rel: string): string {
  return base.replace(/\/+$/, "") + "/" + rel;
}

function pick(relPath: string) {
  emit("select", joinPath(searchDir.value, relPath));
  emit("close");
}

function onDirSelected(path: string) {
  showDirPicker.value = false;
  if (path && path !== dir.value) {
    dir.value = path;
    searchDir.value = path;
    query.value = "";
    matchQuery.value = "";
    matches.value = [];
    error.value = null;
    void runSearch();
  }
  nextTick(() => inputRef.value?.focus());
}

function handleKey(e: KeyboardEvent) {
  if (isImeComposing(e)) return;
  if (e.key === "ArrowDown") {
    e.preventDefault();
    activeIdx.value = Math.min(matches.value.length - 1, activeIdx.value + 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    activeIdx.value = Math.max(0, activeIdx.value - 1);
  } else if (e.key === "Enter") {
    e.preventDefault();
    const hit = matches.value[activeIdx.value];
    if (hit) pick(hit.path);
  }
}

// Keep the active row visible during keyboard navigation.
watch(activeIdx, () => {
  if (!listRef.value) return;
  const row = listRef.value.querySelector<HTMLElement>(`[data-idx="${activeIdx.value}"]`);
  if (row) row.scrollIntoView({ block: "nearest" });
});
</script>
