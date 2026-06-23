<!-- Vue port of components/CommitPicker.tsx. Single-select popover over commit
     history. Preserves the commit-picker-* class contract and all aria text:
     trigger aria-label "Commit", dialog aria-label "Choose commit", the
     modal-header "Choose commit"/"Commits"/"Commit details" text where
     present, the RangeToggle's "Diff range"/"Branch scope" arias, and
     aria-haspopup="dialog". Closes on outside click / Escape (capture +
     stopImmediatePropagation so Escape closes only the picker). -->
<template>
  <div class="commit-picker">
    <button
      ref="triggerRef"
      type="button"
      class="commit-picker-trigger"
      aria-haspopup="dialog"
      :aria-expanded="open"
      aria-label="Commit"
      @click="open = !open"
    >
      <div class="commit-picker-trigger-text">
        <div class="commit-picker-trigger-primary">
          <code>{{ triggerPrimary }}</code>
        </div>
      </div>
      <span class="commit-picker-trigger-chevron" aria-hidden="true">{{ "\u25be" }}</span>
    </button>

    <!-- Mobile modal -->
    <div v-if="open && isMobile" class="commit-picker-modal-backdrop" @click="open = false">
      <div
        ref="popoverRef"
        class="commit-picker-modal"
        role="dialog"
        aria-label="Choose commit"
        @click.stop
      >
        <div class="commit-picker-modal-header">
          <span>Choose commit</span>
          <button
            type="button"
            class="commit-picker-modal-close"
            aria-label="Close"
            @click="open = false"
          >
            {{ "\u00d7" }}
          </button>
        </div>
        <component :is="statusLine" />
        <component :is="list" />
      </div>
    </div>

    <!-- Desktop popover -->
    <div
      v-if="open && !isMobile"
      ref="popoverRef"
      class="commit-picker-popover"
      role="dialog"
      aria-label="Choose commit"
    >
      <component :is="statusLine" />
      <component :is="list" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, nextTick, onUnmounted, ref, watch, type VNode } from "vue";
import type { GitDiffInfo } from "../../types";
import RangeToggle from "./RangeToggle.vue";

const props = defineProps<{
  diffs: GitDiffInfo[];
  selectedDiff: string | null;
  selectedTo: "working" | "self";
  isMobile: boolean;
}>();
const emit = defineEmits<{
  (e: "change", selectedDiff: string, selectedTo: "working" | "self"): void;
}>();

const open = ref(false);
const triggerRef = ref<HTMLButtonElement | null>(null);
const popoverRef = ref<HTMLDivElement | null>(null);

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, Math.max(0, n - 1)) + "\u2026";
}

function shortHash(id: string): string {
  if (id === "working") return "";
  return id.slice(0, 8);
}

function commitLabel(diffs: GitDiffInfo[], id: string, maxLen = 40): string {
  const d = diffs.find((x) => x.id === id);
  if (!d) return shortHash(id);
  return truncate(d.message, maxLen);
}

function rangeSyntax(
  diffs: GitDiffInfo[],
  selectedDiff: string | null,
  selectedTo: "working" | "self",
): string {
  if (!selectedDiff) return "Choose\u2026";
  if (selectedDiff === "working") return "Working Changes";
  const from = commitLabel(diffs, selectedDiff);
  if (selectedTo === "self") return `${from} (Single Commit)`;
  return `${from} \u2192 Now`;
}

const commitDiffs = computed(() => props.diffs.filter((d) => d.id !== "working"));
const workingDiff = computed(() => props.diffs.find((d) => d.id === "working"));

function indexOf(id: string) {
  return commitDiffs.value.findIndex((d) => d.id === id);
}

const fromIdx = computed(() =>
  props.selectedDiff && props.selectedDiff !== "working" ? indexOf(props.selectedDiff) : -1,
);

function rowInRange(idx: number): boolean {
  if (props.selectedDiff === "working") return false;
  if (fromIdx.value < 0) return false;
  if (props.selectedTo === "self") return idx === fromIdx.value;
  return idx <= fromIdx.value;
}

const workingInRange = computed(
  () =>
    props.selectedDiff === "working" ||
    (props.selectedDiff !== null && props.selectedTo === "working"),
);

function pickCommit(id: string) {
  emit("change", id, props.selectedTo);
  open.value = false;
}
function pickWorking() {
  emit("change", "working", "working");
  open.value = false;
}

const triggerPrimary = computed(() =>
  rangeSyntax(props.diffs, props.selectedDiff, props.selectedTo),
);

// --- Render functions for refs / rows / list / status (mirror the JSX) ---

function renderRefs(d: GitDiffInfo): VNode | null {
  const refs = d.refs ?? [];
  const hasRemote = refs.some((r) => r.includes("/"));
  const showMergeBase = !!d.isMergeBase && !hasRemote;
  const chips: VNode[] = refs.map((ref) => {
    const isHead = ref === "HEAD";
    const isRemote = ref.includes("/");
    const cls = [
      "commit-picker-ref",
      isHead && "commit-picker-ref-head",
      isRemote && "commit-picker-ref-remote",
    ]
      .filter(Boolean)
      .join(" ");
    return h("span", { key: ref, class: cls }, ref);
  });
  if (showMergeBase) {
    chips.push(
      h(
        "span",
        {
          key: "__mergebase",
          class: "commit-picker-ref commit-picker-ref-mergebase",
          title: "Merge-base with @{upstream}",
        },
        "merge-base",
      ),
    );
  }
  if (chips.length === 0) return null;
  return h("span", { class: "commit-picker-refs" }, chips);
}

function renderCommitRow(d: GitDiffInfo, idx: number): VNode {
  const isFrom = d.id === props.selectedDiff;
  const inRange = !isFrom && rowInRange(idx);
  const stats = `+${d.additions}/-${d.deletions}`;
  const hash = shortHash(d.id);
  const classes = [
    "commit-picker-row",
    isFrom && "commit-picker-row-from",
    inRange && "commit-picker-row-in-range",
  ]
    .filter(Boolean)
    .join(" ");
  return h("div", { key: d.id, class: classes }, [
    h(
      "button",
      { type: "button", class: "commit-picker-row-main", onClick: () => pickCommit(d.id) },
      [
        h(
          "div",
          { class: "commit-picker-row-marker", "aria-hidden": "true" },
          isFrom ? "\u25cf" : inRange ? "\u2502" : "",
        ),
        h("div", { class: "commit-picker-row-text" }, [
          h("div", { class: "commit-picker-row-subject" }, [
            renderRefs(d),
            h("span", { class: "commit-picker-row-message" }, d.message),
          ]),
          h("div", { class: "commit-picker-row-meta" }, [
            h("span", { class: "commit-picker-row-hash" }, hash),
            h("span", { class: "commit-picker-row-author" }, d.author),
            h(
              "span",
              { class: "commit-picker-row-stats" },
              `${d.filesCount} files \u00b7 ${stats}`,
            ),
          ]),
        ]),
      ],
    ),
  ]);
}

function onListKeyDown(e: KeyboardEvent) {
  if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
  const root = popoverRef.value;
  if (!root) return;
  const rows = Array.from(root.querySelectorAll<HTMLElement>(".commit-picker-row-main"));
  if (rows.length === 0) return;
  const active = document.activeElement as HTMLElement | null;
  const idx = active ? rows.indexOf(active) : -1;
  if (idx < 0 && (e.key === "ArrowDown" || e.key === "ArrowUp")) return;
  let next = idx;
  if (e.key === "ArrowDown") next = Math.min(idx + 1, rows.length - 1);
  else if (e.key === "ArrowUp") next = Math.max(idx - 1, 0);
  else if (e.key === "Home") next = 0;
  else if (e.key === "End") next = rows.length - 1;
  if (next !== idx) {
    e.preventDefault();
    rows[next]?.focus();
  }
}

const rangeToggle = () =>
  h(RangeToggle, {
    selectedDiff: props.selectedDiff,
    selectedTo: props.selectedTo,
    onChange: (sd: string, st: "working" | "self") => emit("change", sd, st),
  });

const list = () => {
  const children: VNode[] = [];
  if (workingDiff.value) {
    const wd = workingDiff.value;
    const cls =
      "commit-picker-row commit-picker-row-working" +
      (props.selectedDiff === "working" ? " commit-picker-row-from" : "") +
      (workingInRange.value && props.selectedDiff !== "working"
        ? " commit-picker-row-in-range"
        : "");
    children.push(
      h("div", { class: cls }, [
        h("button", { type: "button", class: "commit-picker-row-main", onClick: pickWorking }, [
          h(
            "div",
            { class: "commit-picker-row-marker", "aria-hidden": "true" },
            props.selectedDiff === "working" ? "\u25cf" : workingInRange.value ? "\u2502" : "",
          ),
          h("div", { class: "commit-picker-row-text" }, [
            h("div", { class: "commit-picker-row-subject" }, "Working Changes"),
            h("div", { class: "commit-picker-row-meta" }, [
              h(
                "span",
                { class: "commit-picker-row-stats" },
                `${wd.filesCount} files \u00b7 +${wd.additions}/-${wd.deletions}`,
              ),
            ]),
          ]),
        ]),
      ]),
    );
  }
  commitDiffs.value.forEach((d, idx) => children.push(renderCommitRow(d, idx)));
  if (commitDiffs.value.length === 0 && !workingDiff.value) {
    children.push(h("div", { class: "commit-picker-empty" }, "No commits or working changes."));
  }
  return h("div", { class: "commit-picker-list", onKeydown: onListKeyDown }, children);
};

const statusLine = () =>
  h("div", { class: "commit-picker-status" }, [
    h("span", [
      "Showing ",
      h("code", rangeSyntax(props.diffs, props.selectedDiff, props.selectedTo)),
    ]),
    rangeToggle(),
  ]);

// Close on outside click + Escape (capture so Escape closes only the picker).
function onDocDown(e: MouseEvent) {
  const t = e.target as Node;
  if (popoverRef.value?.contains(t)) return;
  if (triggerRef.value?.contains(t)) return;
  open.value = false;
}
function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.stopImmediatePropagation();
    e.preventDefault();
    open.value = false;
  }
}

function detach() {
  document.removeEventListener("mousedown", onDocDown);
  document.removeEventListener("keydown", onKey, true);
}

const wasOpen = ref(false);
watch(open, (isOpen) => {
  detach();
  if (isOpen) {
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKey, true);
    nextTick(() => {
      requestAnimationFrame(() => {
        const root = popoverRef.value;
        if (!root) return;
        const selected = root.querySelector<HTMLElement>(
          ".commit-picker-row-from .commit-picker-row-main",
        );
        const first = root.querySelector<HTMLElement>(".commit-picker-row-main");
        (selected || first)?.focus();
      });
    });
  } else if (wasOpen.value) {
    triggerRef.value?.focus();
  }
  wasOpen.value = isOpen;
});

onUnmounted(detach);
</script>
