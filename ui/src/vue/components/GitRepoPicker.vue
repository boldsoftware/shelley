<!-- Vue port of components/GitRepoPicker.tsx. Lists git repos under $HOME with
     fuzzy filtering. Preserves the grp-* class contract, the dialog
     aria-label "Pick a git repository", the visible title "Pick git
     repository", and the "Close"/"Filter" aria-labels. Uses escapeClose.
     The React <Highlight> subcomponent is reproduced by splitting each path
     into highlighted/plain segments via a computed helper. -->
<template>
  <div v-if="isOpen" class="modal-overlay" @click="onBackdrop">
    <div class="modal grp-modal" role="dialog" aria-label="Pick a git repository">
      <div class="modal-header">
        <h2 class="modal-title">Pick git repository</h2>
        <button class="btn-icon" aria-label="Close" @click="emit('close')">
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
      <div class="modal-body grp-body">
        <input
          ref="inputRef"
          class="grp-filter"
          type="text"
          v-model="query"
          :placeholder="
            loading ? 'Scanning…' : repos ? `Filter ${repos.length} repos…` : 'Filter repos…'
          "
          spellcheck="false"
          aria-label="Filter"
          @keydown="handleKey"
        />

        <div v-if="error" class="grp-error">{{ error }}</div>

        <div class="grp-list" ref="listRef">
          <button
            v-for="(hit, idx) in filtered"
            :key="hit.repo.path"
            :data-idx="idx"
            type="button"
            :class="`grp-row${idx === activeIdx ? ' grp-row-active' : ''}${hit.repo.path === currentPath ? ' grp-row-current' : ''}`"
            @mouseenter="activeIdx = idx"
            @click="pick(hit.repo.path)"
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
            <span class="grp-main">
              <span class="grp-path" :title="hit.repo.path">
                <template
                  v-for="(seg, si) in highlightSegments(
                    displayPath(hit.repo.path, home),
                    shiftPositions(hit.positions, hit.repo.path, home),
                  )"
                  :key="si"
                >
                  <mark v-if="seg.hit" class="grp-hit">{{ seg.text }}</mark>
                  <template v-else>{{ seg.text }}</template>
                </template>
              </span>
              <span v-if="hit.repo.branch || hit.repo.last_activity" class="grp-meta">
                <span v-if="hit.repo.branch" class="grp-branch" title="Current branch">{{
                  hit.repo.branch
                }}</span>
                <span
                  v-if="hit.repo.last_activity"
                  class="grp-when"
                  :title="new Date(hit.repo.last_activity * 1000).toLocaleString()"
                >
                  {{ formatRelative(hit.repo.last_activity) }}
                </span>
              </span>
            </span>
          </button>
          <div v-if="!loading && repos && filtered.length === 0" class="grp-empty">
            No matching repos.
          </div>
          <div v-if="loading" class="grp-empty">Scanning your home directory…</div>
        </div>

        <div v-if="truncated" class="grp-truncated">Showing first results — try filtering.</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useEscapeClose } from "../composables/escapeClose";

interface GitRepoInfo {
  path: string;
  branch?: string;
  worktree?: boolean;
  last_activity?: number;
}

interface GitReposResponse {
  repos: GitRepoInfo[];
  roots: string[];
  truncated?: boolean;
  elapsed_ms: number;
}

const props = defineProps<{
  isOpen: boolean;
  currentPath?: string;
}>();
const emit = defineEmits<{ (e: "close"): void; (e: "select", path: string): void }>();

const repos = ref<GitRepoInfo[] | null>(null);
const error = ref<string | null>(null);
const loading = ref(false);
const truncated = ref(false);
const home = ref("");
const query = ref("");
const activeIdx = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);
const listRef = ref<HTMLDivElement | null>(null);

useEscapeClose(
  () => props.isOpen,
  () => emit("close"),
);

function formatRelative(ts: number): string {
  const diff = Date.now() / 1000 - ts;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d`;
  if (diff < 86400 * 365) return `${Math.floor(diff / (86400 * 30))}mo`;
  return `${Math.floor(diff / (86400 * 365))}y`;
}

function fuzzyScore(text: string, queryStr: string): { score: number; positions: number[] } | null {
  if (!queryStr) return { score: 0, positions: [] };
  const t = text.toLowerCase();
  const q = queryStr.toLowerCase();
  const lastSlash = text.lastIndexOf("/");
  const baseStart = lastSlash + 1;
  const base = t.slice(baseStart);

  const range = (start: number, len: number) => {
    const out: number[] = [];
    for (let i = 0; i < len; i++) out.push(start + i);
    return out;
  };

  if (base.startsWith(q)) {
    return { score: 0 + text.length, positions: range(baseStart, q.length) };
  }
  {
    const i = base.indexOf(q);
    if (i >= 0) {
      return { score: 10000 + i + text.length, positions: range(baseStart + i, q.length) };
    }
  }
  {
    const i = t.indexOf(q);
    if (i >= 0) {
      return { score: 20000 + i + text.length, positions: range(i, q.length) };
    }
  }
  const positions: number[] = [];
  let ti = 0;
  let lastMatch = -1;
  let gaps = 0;
  for (let qi = 0; qi < q.length; qi++) {
    const c = q[qi];
    let found = -1;
    while (ti < t.length) {
      if (t[ti] === c) {
        found = ti;
        ti++;
        break;
      }
      ti++;
    }
    if (found === -1) return null;
    positions.push(found);
    if (lastMatch >= 0) gaps += found - lastMatch - 1;
    lastMatch = found;
  }
  const baseMatches = positions.filter((p) => p >= baseStart).length;
  const tier = baseMatches === q.length ? 30000 : 40000;
  return { score: tier + gaps * 10 + text.length, positions };
}

function displayPath(p: string, h: string): string {
  if (!h) return p;
  if (p === h) return "~";
  if (p.startsWith(h + "/")) return "~" + p.slice(h.length);
  return p;
}

function shiftPositions(positions: number[], realPath: string, h: string): number[] {
  if (!h || !realPath.startsWith(h + "/")) return positions;
  const cut = h.length - 1;
  const out: number[] = [];
  for (const pos of positions) {
    if (pos < h.length) continue;
    out.push(pos - cut);
  }
  return out;
}

function highlightSegments(text: string, positions: number[]): { text: string; hit: boolean }[] {
  if (positions.length === 0) return [{ text, hit: false }];
  const out: { text: string; hit: boolean }[] = [];
  let cursor = 0;
  let i = 0;
  while (i < positions.length) {
    let j = i;
    while (j + 1 < positions.length && positions[j + 1] === positions[j] + 1) j++;
    const start = positions[i];
    const end = positions[j] + 1;
    if (start > cursor) out.push({ text: text.slice(cursor, start), hit: false });
    out.push({ text: text.slice(start, end), hit: true });
    cursor = end;
    i = j + 1;
  }
  if (cursor < text.length) out.push({ text: text.slice(cursor), hit: false });
  return out;
}

const filtered = computed<{ repo: GitRepoInfo; positions: number[] }[]>(() => {
  if (!repos.value) return [];
  if (!query.value) {
    const list = [...repos.value].sort((a, b) => {
      if (props.currentPath) {
        if (a.path === props.currentPath) return -1;
        if (b.path === props.currentPath) return 1;
      }
      const ta = a.last_activity ?? 0;
      const tb = b.last_activity ?? 0;
      if (ta !== tb) return tb - ta;
      return a.path.localeCompare(b.path);
    });
    return list.map((r) => ({ repo: r, positions: [] as number[] }));
  }
  const scored: { repo: GitRepoInfo; score: number; positions: number[] }[] = [];
  for (const r of repos.value) {
    const m = fuzzyScore(r.path, query.value);
    if (m) scored.push({ repo: r, score: m.score, positions: m.positions });
  }
  scored.sort((a, b) => a.score - b.score || a.repo.path.localeCompare(b.repo.path));
  return scored.map(({ repo, positions }) => ({ repo, positions }));
});

function pick(path: string) {
  emit("select", path);
  emit("close");
}

function handleKey(e: KeyboardEvent) {
  if (e.isComposing) return;
  if (e.key === "ArrowDown") {
    e.preventDefault();
    activeIdx.value = Math.min(filtered.value.length - 1, activeIdx.value + 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    activeIdx.value = Math.max(0, activeIdx.value - 1);
  } else if (e.key === "Enter") {
    e.preventDefault();
    const hit = filtered.value[activeIdx.value];
    if (hit) pick(hit.repo.path);
  }
}

function onBackdrop(e: MouseEvent) {
  if (e.target === e.currentTarget) emit("close");
}

watch(query, () => {
  activeIdx.value = 0;
});

// Scroll the active row into view.
watch(activeIdx, () => {
  if (!listRef.value) return;
  const row = listRef.value.querySelector<HTMLElement>(`[data-idx="${activeIdx.value}"]`);
  if (row) row.scrollIntoView({ block: "nearest" });
});

watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    let cancelled = false;
    loading.value = true;
    error.value = null;
    repos.value = null;
    query.value = "";
    activeIdx.value = 0;
    fetch("/api/git/repos")
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return (await r.json()) as GitReposResponse;
      })
      .then((d) => {
        if (cancelled) return;
        repos.value = d.repos || [];
        truncated.value = !!d.truncated;
        const r = (d.roots || [])[0];
        if (r) home.value = r;
      })
      .catch((e) => {
        if (!cancelled) error.value = String(e?.message || e);
      })
      .finally(() => {
        if (!cancelled) loading.value = false;
      });

    const isMobile = window.matchMedia("(max-width: 768px)").matches || "ontouchstart" in window;
    if (!isMobile) {
      nextTick(() => inputRef.value?.focus());
    }

    watch(
      () => props.isOpen,
      (stillOpen) => {
        if (!stillOpen) cancelled = true;
      },
      { once: true },
    );
  },
);
</script>
