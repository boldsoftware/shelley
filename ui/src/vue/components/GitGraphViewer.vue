<!-- Vue port of components/GitGraphViewer.tsx. Renders the commit DAG (GitX-style
     lane layout), a commit-detail pane (subject/meta/diffstat/copy buttons),
     keyboard navigation, scope toggle, repo picker, and a draggable detail
     divider. Preserves the .git-graph-* / .diff-viewer-* class contract, all
     aria-labels/roles, and visible text. Pure helpers + the lane layout live in
     gitGraphLayout.ts; subcomponents under gitGraph/.

     React callback props -> emits:
       onClose   -> emit("close")
       onOpenDiff-> openDiff(commit, file?) (presence gated by
                    `canOpenDiff` prop, mirroring the optional React callback) -->
<template>
  <div v-if="isOpen" class="diff-viewer-overlay" @click="emit('close')">
    <div class="diff-viewer-container git-graph-container" @click.stop>
      <header class="git-graph-header">
        <Select
          v-if="mobileDiff && checkoutOptions.length"
          class="git-graph-checkout-picker"
          :model-value="selectedCheckout"
          :options="checkoutOptions"
          option-label="label"
          option-value="path"
          aria-label="Checkout"
          :title="selectedCheckout"
          :pt="{ overlay: { class: 'git-graph-checkout-options' } }"
          checkmark
          @update:model-value="pickCheckoutOption"
        >
          <template #dropdownicon>
            <span class="commit-picker-trigger-chevron" aria-hidden="true">▾</span>
          </template>
          <template #value>
            <span class="git-graph-checkout-name">{{ selectedCheckoutLabel || 'Choose checkout' }}</span>
            <span v-if="selectedCheckout && selectedCheckout === checkouts.workspace" class="git-graph-workspace-label">Workspace</span>
          </template>
          <template #option="{ option }">
            <span v-if="option.path === null" class="git-graph-browse-repos">{{ option.label }}</span>
            <template v-else>
              <span class="git-graph-checkout-name" :title="option.path">{{ option.label }}</span>
              <span v-if="option.path === checkouts.workspace" class="git-graph-workspace-label">Workspace</span>
            </template>
          </template>
        </Select>
        <div
          v-else-if="checkoutTabs.length"
          class="git-graph-checkouts"
          role="group"
          aria-label="Checkouts"
        >
          <button
            v-for="{ path, label } in checkoutTabs"
            :key="path"
            type="button"
            class="git-graph-checkout"
            :aria-pressed="path === selectedCheckout"
            :title="path"
            @click="pickCheckout(path)"
          >
            <span class="git-graph-checkout-name">{{ label }}</span>
            <span v-if="path === checkouts.workspace" class="git-graph-workspace-label"
              >Workspace</span
            >
          </button>
        </div>
        <div class="git-graph-toolbar" :class="{ 'git-graph-toolbar-mobile': mobileDiff && canOpenDiff }">
          <slot name="review" />
          <div
            class="git-graph-scope"
            role="group"
            aria-label="Branch scope"
            title="Which refs to walk"
          >
            <button
              v-tooltip.top="'Show commits from all branches'"
              type="button"
              :class="`git-graph-scope-btn${scope === 'all' ? ' git-graph-scope-btn-active' : ''}`"
              :aria-pressed="scope === 'all'"
              aria-label="All branches"
              @click="setScope('all')"
            >
              All branches
            </button>
            <button
              v-tooltip.top="'Show commits reachable from HEAD only'"
              type="button"
              :class="`git-graph-scope-btn${scope === 'current' ? ' git-graph-scope-btn-active' : ''}`"
              :aria-pressed="scope === 'current'"
              aria-label="Current branch"
              @click="setScope('current')"
            >
              Current branch
            </button>
          </div>
          <button
            v-if="mobileDiff && canOpenDiff"
            type="button"
            class="git-graph-compare-tool"
            :aria-label="comparing ? 'Cancel compare' : 'Select range'"
            :aria-pressed="comparing"
            @click="toggleCompare"
          >
            {{ comparing ? "Cancel" : "Select range" }}
          </button>
          <button
            v-if="!mobileDiff && canBrowseRepos"
            v-tooltip.top="`Pick repository (current: ${cwd})`"
            class="git-graph-tool"
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
            v-tooltip.top="'Close (Esc)'"
            class="git-graph-tool git-graph-tool-close"
            aria-label="Close"
            @click="emit('close')"
          >
            ×
          </button>
        </div>
      </header>
      <div v-if="checkoutError" class="git-graph-status">{{ checkoutError }}</div>
      <GitRepoPicker
        :is-open="showCwdPicker"
        :current-path="cwd"
        @close="showCwdPicker = false"
        @select="pickCheckout"
      />

      <div class="git-graph-body" ref="bodyRef">
        <div class="git-graph-list">
          <div v-if="loading && !data" class="git-graph-status">Loading…</div>
          <div v-if="error || missingSelection" class="git-graph-status git-graph-error">
            {{ error || `Selected commit ${missingSelection} is outside this graph.` }}
          </div>
          <div v-if="!loading && !error && commits.length === 0" class="git-graph-status">
            No commits.
          </div>
          <template v-if="commits.length > 0">
            <div
              v-if="canOpenDiff && (mobileDiff || selectionFrom === 'working' || selectionTo === 'working')"
              :class="['git-graph-row', {
                'git-graph-row-range-to': selectionFrom === 'working' || selectionTo === 'working',
                'git-graph-row-compare-disabled': workingDisabled,
              }]"
              :aria-disabled="workingDisabled ? true : undefined"
              :title="comparing && !compareFrom ? 'Choose a start commit first' : workingDisabled ? 'Selected commit is not in the working tree history' : undefined"
              :style="{ height: `${ROW_H}px` }"
              @click="selectWorking"
            >
              <span class="git-graph-hash" />
              <span class="git-graph-main">Working changes</span>
              <span v-if="selectionFrom === 'working' || selectionTo === 'working'" class="git-graph-range-tag">{{ selectionFrom === 'working' ? 'Selected' : 'To' }}</span>
            </div>
            <div
              v-for="(c, i) in commits"
              :key="c.hash"
              :ref="(el) => setRowRef(el, c.hash)"
              :class="['git-graph-row', {
                'git-graph-row-selected': c.hash === selected && (!mobileDiff || (!comparing && !selectionFrom)),
                'git-graph-row-compare-from': mobileDiff && c.hash === compareFrom,
                'git-graph-row-compare-disabled': comparableHashes && !comparableHashes.has(c.hash),
                'git-graph-row-range': selectedRangeHashes.has(c.hash) && c.hash !== selectionFrom && c.hash !== selectionTo,
                'git-graph-row-range-from': c.hash === selectionFrom,
                'git-graph-row-range-to': c.hash === selectionTo && selectionTo !== selectionFrom,
              }]"
              :aria-disabled="comparableHashes && !comparableHashes.has(c.hash) ? true : undefined"
              :title="comparableHashes && !comparableHashes.has(c.hash) ? 'Not in the same history as the selected commit' : undefined"
              :style="{ height: `${ROW_H}px` }"
              @click="selectCommit(c.hash)"
              @dblclick="canOpenDiff && !(mobileDiff && comparing) && openDiff(c.hash)"
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
              <span v-if="c.hash === selectionFrom || c.hash === selectionTo" class="git-graph-range-tag">{{ c.hash === selectionFrom ? selectionTo && selectionTo !== 'self' ? 'From' : 'Selected' : 'To' }}</span>
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
            v-tooltip.top="'Drag to resize — double-click to reset'"
            class="git-graph-divider"
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize commit details (double-click to reset)"
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
                v-tooltip.top="'Close details'"
                type="button"
                class="git-graph-sheet-close"
                aria-label="Close commit details"
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

            <!-- Actions sit directly under the subject: long commit bodies and
                 diffstats used to push them far below the fold. -->
            <div class="git-graph-detail-actions">
              <a
                :class="`git-graph-open-diff${!canOpenDiff ? ' git-graph-open-diff-disabled' : ''}`"
                :href="canOpenDiff ? openDiffHref : undefined"
                :aria-disabled="!canOpenDiff"
                :tabindex="canOpenDiff ? undefined : -1"
                @click="onOpenCommitClick($event, selectedCommit.hash)"
              >
                Open diff →
              </a>

              <a
                v-if="data?.githubBase"
                v-tooltip.top="'View on GitHub'"
                class="git-graph-github-link"
                :href="`${data.githubBase}/commit/${selectedCommit.hash}`"
                target="_blank"
                rel="noopener noreferrer"
              >
                <OctocatIcon :size="14" />
                <span>GitHub</span>
              </a>
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
              <DiffstatList
                :files="detail.files"
                :file-href="(path) => diffHref(selectedCommit!.hash, path)"
                @open="(path) => canOpenDiff && openDiff(selectedCommit!.hash, path)"
              />
            </div>
            <div v-if="detailLoading && !detail" class="git-graph-detail-loading">Loading…</div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import Select from "primevue/select";
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { api, checkoutTarget } from "../../services/api";
import type { MachineTarget } from "../../services/api";
import type { GitCheckout, GitGraphResponse, GitCommitDetail } from "../../types";
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
  ALL_LIMIT,
  DETAIL_MIN_PX,
  DETAIL_DEFAULT_PX,
  type Scope,
} from "./gitGraphLayout";

const props = withDefaults(
  defineProps<{
    cwd: string;
    checkout?: string;
    // The conversation whose machine (this host, or its VM) cwd is on.
    machine: MachineTarget;
    activityVersion?: number;
    // cwd is on that conversation's VM, where the repository picker, which
    // lists this host's repositories, can't pick.
    onVm?: boolean;
    isOpen: boolean;
    // True when a modal (e.g. DiffViewer) is stacked on top of this one.
    // Suppresses Esc handling so the top-most modal closes first.
    covered?: boolean;
    // Mirrors the presence of React's optional onOpenDiff callback, which
    // gates whether "Open diff" is enabled.
    canOpenDiff?: boolean;
    mobileDiff?: boolean;
    selectionFrom?: string | null;
    selectionTo?: string;
  }>(),
  { covered: false },
);

const emit = defineEmits<{
  (e: "close"): void;
  (e: "clear-selection"): void;
  (e: "select-commit", hash: string): void;
  (e: "cwd-change", cwd: string, machine?: string): void;
  (e: "open-diff", commit: string, checkout: GitCheckout, file?: string, to?: string): void;
}>();

const cwd = computed(() => props.checkout ?? props.cwd);
const checkouts = ref<{ workspace: string; paths: string[] }>({ workspace: "", paths: [] });
const checkoutError = ref("");
const selectedCheckout = computed(() => props.checkout ?? checkouts.value.workspace);
const checkoutTabs = computed(() => {
  const { workspace, paths: recent } = checkouts.value;
  const paths = [...new Set([workspace, ...recent, props.checkout ?? ""].filter(Boolean))];
  return paths.map((path) => {
    const parts = path.split("/");
    let start = parts.length - 1;
    let label = parts[start] || "/";
    while (start > 0 && paths.some((other) => other !== path && other.endsWith("/" + label))) {
      label = parts.slice(--start).join("/");
    }
    return { path, label };
  });
});
const selectedCheckoutLabel = computed(() => checkoutTabs.value.find(({ path }) => path === selectedCheckout.value)?.label);

function pickCheckout(path: string) {
  if (path !== selectedCheckout.value) emit("cwd-change", path, data.value?.machine);
}

watch(
  [() => props.isOpen, () => props.machine, () => props.cwd, () => props.activityVersion],
  async (_, __, onCleanup) => {
    let current = true;
    onCleanup(() => {
      current = false;
    });
    if (!props.isOpen || !props.machine.conversationId) {
      checkouts.value = { workspace: "", paths: [] };
      return;
    }
    checkoutError.value = "";
    try {
      const result = await api.getRecentCheckouts(props.cwd, props.machine);
      if (current) checkouts.value = result;
    } catch (e) {
      if (current) checkoutError.value = String(e);
    }
  },
  { immediate: true },
);
const showCwdPicker = ref(false);
const data = ref<GitGraphResponse | null>(null);
const canBrowseRepos = computed(() => !(props.machine.expectedMachine || data.value?.machine || props.onVm));
const checkoutOptions = computed(() => canBrowseRepos.value
  ? [...checkoutTabs.value, { path: null, label: "Browse repositories…" }]
  : checkoutTabs.value);

function pickCheckoutOption(path: string | null) {
  if (path === null) showCwdPicker.value = true;
  else pickCheckout(path);
}

const loading = ref(false);
const error = ref<string | null>(null);
const selected = ref<string | null>(null);
const limit = ref(INITIAL_LIMIT);
const scope = ref<Scope>(loadScope());
function setScope(s: Scope) {
  if (s === scope.value) return;
  if (props.mobileDiff) {
    comparing.value = false;
    compareFrom.value = null;
    selected.value = null;
    sheetOpen.value = false;
    emit("clear-selection");
  }
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
const comparing = ref(false);
const compareFrom = ref<string | null>(null);

function toggleCompare() {
  comparing.value = !comparing.value;
  compareFrom.value = null;
}

function openDiff(commit: string, file?: string, to?: string) {
  if (data.value) emit("open-diff", commit, data.value, file, to);
}

function selectWorking() {
  if (comparing.value) {
    if (!compareFrom.value || !workingComparable.value) return;
    openDiff(compareFrom.value, undefined, "working");
    toggleCompare();
  } else {
    openDiff("working");
  }
}

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
watch(() => props.mobileDiff, (mobile) => {
  if (!mobile) {
    comparing.value = false;
    compareFrom.value = null;
  }
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

// Reset transient state when the viewer closes (React effect on [isOpen]).
watch(
  () => props.isOpen,
  (open) => {
    if (!open) {
      selected.value = null;
      data.value = null;
      comparing.value = false;
      compareFrom.value = null;
      limit.value = INITIAL_LIMIT;
      detail.value = null;
      sheetOpen.value = false;
      showCwdPicker.value = false;
      detailCache.clear();
    }
  },
);

// Reset transient state when cwd changes so we don't show stale
// selection/detail (React effect on [cwd]).
watch([cwd, () => props.machine], () => {
  selected.value = null;
  compareFrom.value = null;
  comparing.value = false;
  data.value = null;
  detail.value = null;
  limit.value = INITIAL_LIMIT;
  detailCache.clear();
});

// Load the graph (React effect on [isOpen, cwd, limit, scope]).
watch(
  [() => props.isOpen, cwd, () => props.machine, limit, scope],
  (_, __, onCleanup) => {
    if (!props.isOpen || !cwd.value) return;
    let cancelled = false;
    onCleanup(() => { cancelled = true; });
    const checkout = data.value;
    const target = checkout ? checkoutTarget(props.machine, checkout) : props.machine;
    loading.value = true;
    error.value = null;
    api
      .getGitGraph(checkout?.gitRoot ?? cwd.value, target, limit.value, scope.value)
      .then((d) => {
        if (cancelled) return;
        d.commits = normalizeCommits(d.commits || []);
        data.value = d;
        if (d.commits.length > 0) {
          const prev = selected.value;
          if (prev && d.commits.some((c) => c.hash === prev)) {
            // keep
          } else if (!props.selectionFrom) {
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
  },
  { immediate: true },
);

// Load commit body + numstat on selection (React effect on
// [isOpen, cwd, selected]).
watch(
  [() => props.isOpen, cwd, () => props.machine, selected],
  () => {
    if (!props.isOpen || !data.value || !selected.value) {
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
      .getGitCommitDetail(data.value.gitRoot, checkoutTarget(props.machine, data.value), sel)
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
    const stop = watch([() => props.isOpen, cwd, () => props.machine, selected], () => {
      cancelled = true;
      stop();
    });
  },
  { immediate: true },
);

const commits = computed(() => data.value?.commits ?? []);
const missingSelection = computed(() => {
  if (!data.value || loading.value || (commits.value.length >= limit.value && limit.value < ALL_LIMIT)) return;
  return [props.selectionFrom, props.selectionTo].find(
    (hash) => hash && hash !== "working" && hash !== "self" && !commits.value.some((c) => c.hash === hash),
  );
});
function ancestors(start: string[]): Set<string> {
  const byHash = new Map(commits.value.map((commit) => [commit.hash, commit]));
  const seen = new Set<string>();
  const stack = [...start];
  while (stack.length) {
    const hash = stack.pop()!;
    if (seen.has(hash)) continue;
    seen.add(hash);
    stack.push(...(byHash.get(hash)?.parents ?? []));
  }
  return seen;
}
const comparableHashes = computed(() => {
  if (!props.mobileDiff || !comparing.value || !compareFrom.value) return null;
  const children = new Map<string, string[]>();
  for (const commit of commits.value) {
    for (const parent of commit.parents) {
      const siblings = children.get(parent) ?? [];
      siblings.push(commit.hash);
      children.set(parent, siblings);
    }
  }
  const comparable = ancestors([compareFrom.value]);
  const stack = [compareFrom.value];
  while (stack.length) {
    const hash = stack.pop()!;
    for (const child of children.get(hash) ?? []) {
      if (comparable.has(child)) continue;
      comparable.add(child);
      stack.push(child);
    }
  }
  return comparable;
});
const workingComparable = computed(() => {
  if (!compareFrom.value) return true;
  const head = commits.value.find((commit) => commit.isHead);
  return !!head && ancestors([head.hash]).has(compareFrom.value);
});
const workingDisabled = computed(() => comparing.value && (!compareFrom.value || !workingComparable.value));
const selectedRangeHashes = computed(() => {
  const from = props.selectionFrom;
  const to = props.selectionTo;
  if (!from || !to || to === "self") return new Set<string>();
  const byHash = new Map(commits.value.map((commit) => [commit.hash, commit]));
  const end = to === "working" ? commits.value.find((commit) => commit.isHead)?.hash : to;
  if (!byHash.has(from) || !end || !byHash.has(end)) return new Set<string>();
  const included = ancestors([end]);
  for (const hash of ancestors(byHash.get(from)!.parents)) included.delete(hash);
  return included;
});
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
  if (comparing.value && props.mobileDiff) {
    if (!compareFrom.value) {
      compareFrom.value = hash;
    } else {
      if (hash === compareFrom.value || !comparableHashes.value?.has(hash)) return;
      const first = compareFrom.value;
      const firstIsAncestor = ancestors([hash]).has(first);
      const [from, to] = firstIsAncestor ? [first, hash] : [hash, first];
      openDiff(from, undefined, to);
      toggleCompare();
    }
    return;
  }
  if (!props.mobileDiff && props.selectionFrom && hash !== selected.value) emit("select-commit", hash);
  selected.value = hash;
  if (props.mobileDiff && props.canOpenDiff) {
    sheetOpen.value = false;
    openDiff(hash);
    return;
  }
  sheetOpen.value = true;
}

// Esc handling (React effect on [isOpen, covered, onClose, sheetOpen]).
function onEscKey(e: KeyboardEvent) {
  if (e.key === "Escape" && !showCwdPicker.value) {
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
  if (showCwdPicker.value) return;
  const target = e.target;
  if (target instanceof Element) {
    if (target.closest("input, textarea, select, .git-graph-checkout-picker, .git-graph-checkout-options")) return;
    if (e.key === "Enter" && target.closest("a, button")) return;
  }
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
  } else if (e.key === "Enter" && selected.value && props.canOpenDiff && !(props.mobileDiff && comparing.value)) {
    e.preventDefault();
    openDiff(selected.value);
  }
}
watch(
  [() => props.isOpen, () => props.covered],
  ([open, covered]) => {
    window.removeEventListener("keydown", onNavKey);
    if (open && !covered) window.addEventListener("keydown", onNavKey);
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
  if (sel && !(props.mobileDiff && props.selectionFrom && (props.selectionTo === sel || (props.selectionTo === "working" && props.selectionFrom === sel)))) {
    const el = rowRefs.get(sel);
    if (el) el.scrollIntoView({ block: "nearest" });
  }
});
watch(
  [() => props.isOpen, () => props.covered, () => props.selectionFrom, () => props.selectionTo, () => props.mobileDiff, commits, loading],
  async () => {
    const from = props.selectionFrom;
    const to = props.selectionTo;
    if (!props.isOpen || props.covered || loading.value || !data.value || !from) return;
    if (from === "working") {
      selected.value = null;
      return;
    }
    const hashes = new Set(commits.value.map((commit) => commit.hash));
    const target = !to || to === "self" || to === "working" ? from : to;
    const end = to === "working" ? commits.value.find((commit) => commit.isHead)?.hash : target;
    if (!hashes.has(from) || !end || !hashes.has(end)) {
      selected.value = null;
      if (commits.value.length >= limit.value && limit.value < ALL_LIMIT)
        limit.value = Math.min(limit.value * 2, ALL_LIMIT);
      return;
    }
    selected.value = target;
    await nextTick();
    rowRefs.get(target)?.scrollIntoView({ block: "center" });
  },
);

function diffHref(hash: string, file?: string): string {
  const params = new URLSearchParams();
  params.set("diff", hash);
  if (data.value) {
    params.set("cwd", data.value.gitRoot);
    params.set("expected_checkout", data.value.gitRoot);
    params.set("expected_machine", data.value.machine);
  }
  if (file) params.set("file", file);
  return `${window.location.pathname}?${params.toString()}`;
}

const openDiffHref = computed(() =>
  selectedCommit.value ? diffHref(selectedCommit.value.hash) : "#",
);

function onOpenCommitClick(e: MouseEvent, hash: string) {
  // Let the browser handle modifier/middle-click so users can open the diff
  // in a new tab/window.
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
    return;
  }
  e.preventDefault();
  if (!props.canOpenDiff) return;
  // Keep the graph selection in sync without opening the mobile detail sheet
  // behind the diff viewer.
  selected.value = hash;
  openDiff(hash);
}

function onGravatarError(e: Event) {
  (e.currentTarget as HTMLImageElement).style.visibility = "hidden";
}
</script>
