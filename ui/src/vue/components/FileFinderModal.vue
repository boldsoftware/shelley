<!-- Server-based fuzzy file finder. Lists files under a working directory and
     ranks them against the query on the server (/api/find-files), so the
     browser never needs the full file list. The search runs in two phases:
     a fast name-only request renders immediately, and a parallel git-grep
     content request folds its hits in when it lands (snippets attach to
     visible rows; content-only files append below). Selecting a file emits
     its absolute path (the parent opens EditableFileModal on it). A "change
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
        <span class="ff-dir-path ellipsis-start">{{ displayDir }}</span>
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
        Searching <code class="ff-scope-path ellipsis-start">{{ tildifyPath(scopeDir) }}</code>
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
            <span class="grp-path ellipsis-start" :title="hit.path">
              <HighlightedText :text="hit.path" :positions="hit.matched_indexes" />
            </span>
            <!-- Content match: the grep excerpt that earned this file its row
                 (or bolstered a name match), with the matched term marked. -->
            <span v-if="hit.snippet" class="ff-snippet" :title="hit.snippet">
              <span class="ff-snippet-line">{{ hit.line }}:</span>
              <HighlightedText :text="hit.snippet" :positions="hit.snippet_matched_indexes" />
            </span>
          </span>
        </button>

        <div v-if="!loading && !grepPending && matches.length === 0 && !error" class="grp-empty">
          {{ matchQuery ? "No matching files." : "No files in this directory." }}
        </div>
        <div v-if="(loading || grepPending) && matches.length === 0" class="grp-empty">
          Searching…
        </div>
      </div>

      <!-- Name matches render as soon as the fast pass returns; this footer
           admits the slower git-grep pass is still out looking. -->
      <div v-if="grepPending" class="ff-grep-pending">Searching file contents…</div>
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
import { mergeContentMatches } from "../../utils/fileSearch";
import HighlightedText from "./HighlightedText.vue";
import { tildifyPath } from "../../utils/tildify";

interface FileMatch {
  path: string;
  matched_indexes?: number[];
  // Present when the file's content matched (git grep in a repo): the 1-based
  // line number, its trimmed text, and rune offsets to highlight within it.
  line?: number;
  snippet?: string;
  snippet_matched_indexes?: number[];
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
// True while the content (git grep) request is still in flight after the
// name results have been requested; drives the "Searching file contents…"
// footer and keeps the empty state from claiming "no matches" prematurely.
const grepPending = ref(false);
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

// The list never grows past this many rows: name matches first (the server
// already caps those), then appended content hits up to the cap.
const MAX_ROWS = 100;

// Fold the content-phase (git grep) results into the list the name phase
// already rendered: attach snippets to rows that are present (including the
// pinned row), append the rest in the server's path order. Never reorders or
// resets the selection — the user may already be arrowing through the list.
function applyContentMatches(res: {
  search_dir: string;
  matches: FileMatch[];
  truncated: boolean;
}) {
  // Both phases resolve a path query against the filesystem independently, so
  // a directory appearing/vanishing between the two requests can leave them
  // rooted in different trees. Joining such content paths against the name
  // phase's search_dir would emit wrong absolute paths; drop them instead.
  if (res.search_dir !== searchDir.value) return;
  const merged = mergeContentMatches(matches.value, res.matches, MAX_ROWS);
  matches.value = merged.matches;
  if (merged.capped || res.truncated) truncated.value = true;
}

// Two-phase search: a fast name-only request and a slower git-grep content
// request fire together, sharing one AbortController so the next keystroke
// cancels whichever is still in flight. Name results apply the moment they
// arrive; content results wait for them (they attach to/append after the name
// rows) and are a silent bonus — their failure never disturbs the list.
async function runSearch() {
  if (!dir.value) return;
  abortController?.abort();
  const controller = new AbortController();
  abortController = controller;
  loading.value = true;
  error.value = null;
  const q = query.value.trim();

  const namePromise = api.findFiles(dir.value, q, controller.signal, { content: "skip" });
  // An empty query lists the directory alphabetically; there is no term to
  // grep for, so skip the content request entirely.
  const contentPromise = q
    ? api.findFiles(dir.value, q, controller.signal, { content: "only" })
    : null;
  grepPending.value = contentPromise !== null;
  // The content promise can reject before it's awaited below (abort, or an
  // early bail on name failure); pre-attach a handler so the rejection is
  // never reported as unhandled.
  contentPromise?.catch(() => {});

  let nameApplied = false;
  try {
    const res = await namePromise;
    if (!controller.signal.aborted) {
      // The server resolves an empty/relative dir (e.g. to $HOME) and echoes
      // the absolute path back; adopt it so joinPath produces valid paths.
      dir.value = res.dir;
      // Matches are relative to search_dir, which a path query moves elsewhere.
      searchDir.value = res.search_dir;
      matchQuery.value = res.match_query;
      matches.value = res.matches;
      truncated.value = res.truncated;
      activeIdx.value = 0;
      nameApplied = true;
    }
  } catch (err) {
    if (controller.signal.aborted || (err as Error).name === "AbortError") return;
    error.value = err instanceof Error ? err.message : String(err);
    // Nothing was searched, so don't leave the previous scope line claiming
    // otherwise next to the error, nor a footer promising content results
    // that won't be applied. Abort the content request too: its result is
    // useless without name results to attach to, and cancelling frees the
    // server from a grep that can run for seconds.
    controller.abort();
    searchDir.value = dir.value;
    matches.value = [];
    grepPending.value = false;
  } finally {
    if (abortController === controller) loading.value = false;
  }

  if (contentPromise) {
    try {
      const res = await contentPromise;
      // Applied only on top of successfully-applied name results: if those
      // failed or were superseded, these rows have nothing to attach to.
      if (!controller.signal.aborted && nameApplied) applyContentMatches(res);
    } catch {
      // Content search is best-effort; the name results are already up.
    }
  }
  if (abortController === controller) {
    grepPending.value = false;
    abortController = null;
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
    grepPending.value = false;
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
