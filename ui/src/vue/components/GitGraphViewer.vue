<!-- Vue port of components/GitGraphViewer.tsx. Renders the commit DAG (GitX-style
     lane layout), a commit-detail pane (subject/meta/diffstat/copy buttons),
     keyboard navigation, scope toggle, repo picker, and a draggable detail
     divider. Preserves the .git-graph-* / .diff-viewer-* class contract, all
     aria-labels/roles, and visible text. Pure helpers + the lane layout live in
     gitGraphLayout.ts; subcomponents under gitGraph/.

     React callback props -> emits:
       onClose   -> emit("close")
       onOpenDiff-> emit("open-diff", commit, cwd) (presence gated by
                    `canOpenDiff` prop, mirroring the optional React callback) -->
<template>
  <div v-if="isOpen" class="diff-viewer-overlay" @click="emit('close')">
    <div class="diff-viewer-container git-graph-container" @click.stop>
      <div class="git-graph-toolbar">
        <div
          class="git-graph-scope"
          role="group"
          aria-label="Branch scope"
          title="Which refs to walk"
        >
          <button
            type="button"
            :class="`git-graph-scope-btn${scope === 'all' ? ' git-graph-scope-btn-active' : ''}`"
            :aria-pressed="scope === 'all'"
            title="Show commits from all branches"
            @click="setScope('all')"
          >
            All branches
          </button>
          <button
            type="button"
            :class="`git-graph-scope-btn${scope === 'current' ? ' git-graph-scope-btn-active' : ''}`"
            :aria-pressed="scope === 'current'"
            title="Show commits reachable from HEAD only"
            @click="setScope('current')"
          >
            Current branch
          </button>
        </div>
        <button
          class="git-graph-tool"
          :title="`Pick repository (current: ${cwd})`"
          aria-label="Pick repository"
          @click="showCwdPicker = true"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
            />
          </svg>
        </button>
        <button
          class="git-graph-tool"
          title="Close (Esc)"
          aria-label="Close"
          @click="emit('close')"
        >
          ×
        </button>
      </div>
      <GitRepoPicker
        :is-open="showCwdPicker"
        :current-path="cwd"
        @close="showCwdPicker = false"
        @select="(p) => (cwdOverride = p)"
      />

      <div class="git-graph-body" ref="bodyRef">
        <div class="git-graph-list">
          <div v-if="loading && !data" class="git-graph-status">Loading…</div>
          <div v-if="error" class="git-graph-status git-graph-error">{{ error }}</div>
          <div v-if="!loading && !error && commits.length === 0" class="git-graph-status">
            No commits.
          </div>
          <template v-if="commits.length > 0">
            <div
              v-for="(c, i) in commits"
              :key="c.hash"
              :ref="(el) => setRowRef(el, c.hash)"
              :class="`git-graph-row${c.hash === selected ? ' git-graph-row-selected' : ''}`"
              :style="{ height: `${ROW_H}px` }"
              @click="selectCommit(c.hash)"
              @dblclick="canOpenDiff && emit('open-diff', c.hash, cwd)"
            >
              <span class="git-graph-hash">{{ c.shortHash }}</span>
              <svg
                class="git-graph-svg"
                :width="rowWidth(i)"
                :height="ROW_H"
                :style="{ width: `${rowWidth(i)}px`, height: `${ROW_H}px` }"
              >
                <line
                  v-for="(ln, idx) in layout.rows[i].lines"
                  :key="idx"
                  :x1="colX(ln.from)"
                  :y1="ln.upper ? 0 : ROW_H"
                  :x2="colX(ln.to)"
                  :y2="ROW_H / 2"
                  :stroke="laneColor(ln.colorIndex)"
                  :stroke-width="1.8"
                  stroke-linecap="round"
                />
                <circle
                  :cx="colX(layout.rows[i].col)"
                  :cy="ROW_H / 2"
                  :r="DOT_R"
                  :fill="laneColor(layout.rows[i].colorIndex)"
                  :stroke="c.isHead ? 'var(--text-primary)' : 'none'"
                  :stroke-width="c.isHead ? 1.5 : 0"
                >
                  <title>{{ c.shortHash }}</title>
                </circle>
              </svg>
              <span class="git-graph-main">
                <span
                  v-if="
                    c.refs.length > 0 || (c.isMergeBase && !c.refs.some((r) => r.includes('/')))
                  "
                  class="git-graph-refs"
                >
                  <RefBadge v-for="r in c.refs" :key="r" :name="r" />
                  <span
                    v-if="c.isMergeBase && !c.refs.some((r) => r.includes('/'))"
                    class="git-graph-ref git-graph-ref-mergebase"
                    title="Merge-base with @{upstream}"
                  >
                    merge-base
                  </span>
                </span>
                <span class="git-graph-subject">{{ c.subject }}</span>
              </span>
              <span class="git-graph-author">{{ c.author }}</span>
              <span class="git-graph-time">{{ formatRelative(c.timestamp) }}</span>
            </div>
            <LoadMoreRow
              :limit="limit"
              :commits-loaded="commits.length"
              :loading="loading"
              @load="(n) => (limit = n)"
            />
          </template>
        </div>

        <template v-if="selectedCommit">
          <div
            v-if="sheetOpen"
            class="git-graph-sheet-backdrop"
            aria-hidden="true"
            @click="sheetOpen = false"
          />
          <!-- Draggable divider — desktop only; hidden on mobile via CSS
               because the detail pane becomes a bottom sheet there. -->
          <div
            class="git-graph-divider"
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize commit details (double-click to reset)"
            title="Drag to resize — double-click to reset"
            @mousedown="onDividerMouseDown"
            @dblclick="onDividerDoubleClick"
          >
            <div class="git-graph-divider-grip" aria-hidden="true" />
          </div>
          <div
            :class="`git-graph-detail${sheetOpen ? ' git-graph-detail-sheet-open' : ''}`"
            role="dialog"
            aria-label="Commit details"
            :style="isDesktop ? { width: `${detailWidth}px` } : undefined"
          >
            <div class="git-graph-sheet-topbar">
              <span class="git-graph-sheet-grip" aria-hidden="true" />
              <button
                type="button"
                class="git-graph-sheet-close"
                aria-label="Close commit details"
                title="Close details"
                @click="sheetOpen = false"
              >
                ×
              </button>
            </div>
            <div class="git-graph-detail-top">
              <img
                class="git-graph-gravatar"
                :src="gravatarUrl(selectedCommit.email, 72)"
                alt=""
                :width="48"
                :height="48"
                referrerpolicy="no-referrer"
                @error="onGravatarError"
              />
              <div class="git-graph-detail-subject">{{ selectedCommit.subject }}</div>
            </div>

            <div class="git-graph-detail-meta">
              <div>
                <strong>Author:</strong> {{ selectedCommit.author
                }}{{ selectedCommit.email ? ` <${selectedCommit.email}>` : "" }}
              </div>
              <div>
                <strong>Date:</strong>
                {{ new Date(selectedCommit.timestamp * 1000).toLocaleString() }}
              </div>
              <div class="git-graph-detail-sha-row">
                <strong>SHA:</strong>
                <code class="git-graph-detail-hash">{{ selectedCommit.hash }}</code>
                <span class="git-graph-copy-group">
                  <CopyButton :value="selectedCommit.hash" label="sha" title="Copy full SHA" />
                  <CopyButton
                    :value="selectedCommit.shortHash"
                    label="short"
                    title="Copy short SHA"
                  />
                  <CopyButton
                    v-if="data?.githubBase"
                    :value="`${data.githubBase}/commit/${selectedCommit.hash}`"
                    label="url"
                    title="Copy GitHub URL"
                  />
                </span>
              </div>
              <div
                v-if="
                  selectedCommit.refs.length > 0 ||
                  (selectedCommit.isMergeBase && !selectedCommit.refs.some((r) => r.includes('/')))
                "
                class="git-graph-detail-refs"
              >
                <RefBadge v-for="r in selectedCommit.refs" :key="r" :name="r" />
                <span
                  v-if="
                    selectedCommit.isMergeBase && !selectedCommit.refs.some((r) => r.includes('/'))
                  "
                  class="git-graph-ref git-graph-ref-mergebase"
                  title="Merge-base with @{upstream}"
                >
                  merge-base
                </span>
              </div>
              <div v-if="data?.gitRoot" class="git-graph-detail-root">
                <code>{{ data.gitRoot }}</code>
                <template v-if="data.currentBranch">
                  {{ " " }}on <strong>{{ data.currentBranch }}</strong>
                </template>
              </div>
            </div>

            <pre v-if="detail && detail.body" class="git-graph-detail-body">{{ detail.body }}</pre>

            <div v-if="detail && detail.files.length > 0" class="git-graph-diffstat">
              <div class="git-graph-diffstat-summary">
                {{ detail.files.length }} file{{ detail.files.length === 1 ? "" : "s" }} changed
                <span v-if="detail.insTotal > 0" class="git-graph-diffstat-ins">
                  +{{ detail.insTotal }}</span
                >
                <span v-if="detail.delTotal > 0" class="git-graph-diffstat-del">
                  −{{ detail.delTotal }}</span
                >
              </div>
              <DiffstatList :files="detail.files" />
            </div>
            <div v-if="detailLoading && !detail" class="git-graph-detail-loading">Loading…</div>

            <div class="git-graph-detail-actions">
              <a
                :class="`git-graph-open-diff${!canOpenDiff ? ' git-graph-open-diff-disabled' : ''}`"
                :href="openDiffHref"
                :aria-disabled="!canOpenDiff"
                @click="onOpenDiffClick"
              >
                Open diff →
              </a>
              <a
                v-if="data?.githubBase"
                class="git-graph-github-link"
                :href="`${data.githubBase}/commit/${selectedCommit.hash}`"
                target="_blank"
                rel="noopener noreferrer"
                title="View on GitHub"
              >
                <OctocatIcon :size="14" />
                <span>GitHub</span>
              </a>
            </div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { api } from "../../services/api";
import type { GitGraphResponse, GitCommitDetail } from "../../types";
import GitRepoPicker from "./GitRepoPicker.vue";
import RefBadge from "./gitGraph/RefBadge.vue";
import CopyButton from "./gitGraph/CopyButton.vue";
import DiffstatList from "./gitGraph/DiffstatList.vue";
import OctocatIcon from "./gitGraph/OctocatIcon.vue";
import LoadMoreRow from "./gitGraph/LoadMoreRow.vue";
import {
  computeLayout,
  normalizeCommits,
  laneColor,
  colX,
  formatRelative,
  gravatarUrl,
  loadScope,
  storeScope,
  loadDetailWidth,
  storeDetailWidth,
  ROW_H,
  LANE_W,
  LEFT_PAD,
  DOT_R,
  INITIAL_LIMIT,
  DETAIL_MIN_PX,
  DETAIL_DEFAULT_PX,
  type Scope,
} from "./gitGraphLayout";

const props = withDefaults(
  defineProps<{
    cwd: string;
    isOpen: boolean;
    // True when a modal (e.g. DiffViewer) is stacked on top of this one.
    // Suppresses Esc handling so the top-most modal closes first.
    covered?: boolean;
    // Mirrors the presence of React's optional onOpenDiff callback, which
    // gates whether "Open diff" is enabled.
    canOpenDiff?: boolean;
  }>(),
  { covered: false },
);

const emit = defineEmits<{
  (e: "close"): void;
  (e: "open-diff", commit: string, cwd: string): void;
}>();

// Internal override so the user can switch directories without re-opening.
const cwdOverride = ref<string | null>(null);
const cwd = computed(() => cwdOverride.value ?? props.cwd);
const showCwdPicker = ref(false);
const data = ref<GitGraphResponse | null>(null);
const loading = ref(false);
const error = ref<string | null>(null);
const selected = ref<string | null>(null);
const limit = ref(INITIAL_LIMIT);
const scope = ref<Scope>(loadScope());
function setScope(s: Scope) {
  scope.value = s;
  storeScope(s);
}
// Full body + numstat for the selected commit. Lazily loaded when the
// selection changes, with a small cache so re-selecting is instant.
const detail = ref<GitCommitDetail | null>(null);
const detailLoading = ref(false);
const detailCache = new Map<string, GitCommitDetail>();
// On mobile (<=768px) the sidebar is a bottom sheet that only shows after
// the user taps a row.
const sheetOpen = ref(false);

// Desktop-only: width of the commit-detail pane, controlled by the draggable
// divider. Persisted in localStorage so it survives reloads.
const detailWidth = ref<number>(loadDetailWidth());
const isDesktop = ref<boolean>(
  typeof window === "undefined" ? true : !window.matchMedia("(max-width: 768px)").matches,
);

const mq = window.matchMedia("(max-width: 768px)");
const onMqChange = () => (isDesktop.value = !mq.matches);
if (mq.addEventListener) mq.addEventListener("change", onMqChange);
else mq.addListener(onMqChange);
onUnmounted(() => {
  if (mq.removeEventListener) mq.removeEventListener("change", onMqChange);
  else mq.removeListener(onMqChange);
});

const bodyRef = ref<HTMLDivElement | null>(null);
let isDragging = false;
function onDividerMouseDown(e: MouseEvent) {
  // Only respond to primary button; ignore on touch/mobile bottom-sheet.
  if (e.button !== 0) return;
  if (window.matchMedia("(max-width: 768px)").matches) return;
  e.preventDefault();
  isDragging = true;
  document.body.style.cursor = "col-resize";
  document.body.style.userSelect = "none";
  const onMove = (ev: MouseEvent) => {
    if (!isDragging) return;
    const body = bodyRef.value;
    if (!body) return;
    const rect = body.getBoundingClientRect();
    // Dragging right shrinks detail; dragging left grows it.
    const next = Math.max(
      DETAIL_MIN_PX,
      Math.min(rect.width - DETAIL_MIN_PX, rect.right - ev.clientX),
    );
    detailWidth.value = next;
  };
  const onUp = () => {
    if (!isDragging) return;
    isDragging = false;
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
  };
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}
// Persist whenever width changes (post-drag, or programmatically).
watch(detailWidth, (w) => storeDetailWidth(w));
// Double-click resets to default — easy escape hatch if a user drags too far.
function onDividerDoubleClick() {
  detailWidth.value = DETAIL_DEFAULT_PX;
}

// Load the graph (React effect on [isOpen, cwd, limit, scope]).
watch(
  [() => props.isOpen, cwd, limit, scope],
  () => {
    if (!props.isOpen || !cwd.value) return;
    let cancelled = false;
    loading.value = true;
    error.value = null;
    api
      .getGitGraph(cwd.value, limit.value, scope.value)
      .then((d) => {
        if (cancelled) return;
        d.commits = normalizeCommits(d.commits || []);
        data.value = d;
        if (d.commits.length > 0) {
          const prev = selected.value;
          if (prev && d.commits.some((c) => c.hash === prev)) {
            // keep
          } else {
            const head = d.commits.find((c) => c.isHead);
            selected.value = (head ?? d.commits[0]).hash;
          }
        }
      })
      .catch((e) => {
        if (!cancelled) error.value = String(e?.message || e);
      })
      .finally(() => {
        if (!cancelled) loading.value = false;
      });
    const stop = watch([() => props.isOpen, cwd, limit, scope], () => {
      cancelled = true;
      stop();
    });
  },
  { immediate: true },
);

// Reset transient state when the viewer closes (React effect on [isOpen]).
watch(
  () => props.isOpen,
  (open) => {
    if (!open) {
      selected.value = null;
      limit.value = INITIAL_LIMIT;
      detail.value = null;
      sheetOpen.value = false;
      cwdOverride.value = null;
      showCwdPicker.value = false;
      detailCache.clear();
    }
  },
);

// Reset transient state when cwd changes so we don't show stale
// selection/detail (React effect on [cwd]).
watch(cwd, () => {
  selected.value = null;
  data.value = null;
  detail.value = null;
  limit.value = INITIAL_LIMIT;
  detailCache.clear();
});

// Load commit body + numstat on selection (React effect on
// [isOpen, cwd, selected]).
watch(
  [() => props.isOpen, cwd, selected],
  () => {
    if (!props.isOpen || !cwd.value || !selected.value) {
      detail.value = null;
      return;
    }
    const cached = detailCache.get(selected.value);
    if (cached) {
      detail.value = cached;
      detailLoading.value = false;
      return;
    }
    let cancelled = false;
    const sel = selected.value;
    detail.value = null;
    detailLoading.value = true;
    api
      .getGitCommitDetail(cwd.value, sel)
      .then((d) => {
        if (cancelled) return;
        detailCache.set(sel, d);
        detail.value = d;
      })
      .catch(() => {
        // Silently ignore; the sidebar still shows the graph-level info.
      })
      .finally(() => {
        if (!cancelled) detailLoading.value = false;
      });
    const stop = watch([() => props.isOpen, cwd, selected], () => {
      cancelled = true;
      stop();
    });
  },
  { immediate: true },
);

const commits = computed(() => data.value?.commits ?? []);
const layout = computed(() => computeLayout(commits.value));

// Per-row width: just wide enough to cover that row's rightmost lane, so
// commit subjects drift leftward and stay snug with the graph.
function rowWidth(i: number): number {
  const row = layout.value.rows[i];
  if (!row) return LEFT_PAD + LANE_W;
  let maxCol = row.col;
  for (const ln of row.lines) {
    if (ln.from > maxCol) maxCol = ln.from;
    if (ln.to > maxCol) maxCol = ln.to;
  }
  return LEFT_PAD + (maxCol - 1) * LANE_W + LANE_W;
}

function selectCommit(hash: string) {
  selected.value = hash;
  sheetOpen.value = true;
}

// Esc handling (React effect on [isOpen, covered, onClose, sheetOpen]).
function onEscKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    // If the mobile bottom sheet is open, close it first instead of tearing
    // down the whole viewer.
    if (sheetOpen.value && window.matchMedia("(max-width: 768px)").matches) {
      sheetOpen.value = false;
      return;
    }
    emit("close");
  }
}
watch(
  [() => props.isOpen, () => props.covered],
  ([open, covered]) => {
    window.removeEventListener("keydown", onEscKey);
    if (open && !covered) window.addEventListener("keydown", onEscKey);
  },
  { immediate: true },
);

// Arrow/j/k/Enter navigation (React effect on
// [isOpen, commits, selected, selectCommit, onOpenDiff, cwd]).
function onNavKey(e: KeyboardEvent) {
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  if (!commits.value.length) return;
  const idx = commits.value.findIndex((c) => c.hash === selected.value);
  if (e.key === "ArrowDown" || e.key === "j") {
    e.preventDefault();
    const next = commits.value[Math.min(commits.value.length - 1, Math.max(0, idx + 1))];
    if (next) selectCommit(next.hash);
  } else if (e.key === "ArrowUp" || e.key === "k") {
    e.preventDefault();
    const prev = commits.value[Math.max(0, idx - 1)];
    if (prev) selectCommit(prev.hash);
  } else if (e.key === "Enter" && selected.value && props.canOpenDiff) {
    e.preventDefault();
    emit("open-diff", selected.value, cwd.value);
  }
}
watch(
  () => props.isOpen,
  (open) => {
    window.removeEventListener("keydown", onNavKey);
    if (open) window.addEventListener("keydown", onNavKey);
  },
  { immediate: true },
);
onUnmounted(() => {
  window.removeEventListener("keydown", onEscKey);
  window.removeEventListener("keydown", onNavKey);
});

const selectedCommit = computed(() => commits.value.find((c) => c.hash === selected.value) || null);

// Scroll the selected row into view (React effect on [selected]).
const rowRefs = new Map<string, HTMLElement>();
function setRowRef(el: Element | { $el: Element } | null, hash: string) {
  if (el && el instanceof HTMLElement) rowRefs.set(hash, el);
  else rowRefs.delete(hash);
}
watch(selected, async (sel) => {
  await nextTick();
  if (sel) {
    const el = rowRefs.get(sel);
    if (el) el.scrollIntoView({ block: "nearest" });
  }
});

const openDiffHref = computed(() => {
  const sel = selectedCommit.value;
  if (!sel) return "#";
  const params = new URLSearchParams();
  params.set("diff", sel.hash);
  if (cwd.value) params.set("cwd", cwd.value);
  return `${window.location.pathname}?${params.toString()}`;
});

function onOpenDiffClick(e: MouseEvent) {
  // Let the browser handle modifier/middle-click so users can open the diff
  // in a new tab/window.
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
    return;
  }
  e.preventDefault();
  if (props.canOpenDiff && selectedCommit.value) {
    emit("open-diff", selectedCommit.value.hash, cwd.value);
  }
}

function onGravatarError(e: Event) {
  (e.currentTarget as HTMLImageElement).style.visibility = "hidden";
}
</script>
