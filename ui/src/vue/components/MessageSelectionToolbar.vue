<!-- Vue port of components/MessageSelectionToolbar.tsx. A floating "Comment"
     button anchored to the current text selection when it sits entirely within
     one commentable message. Preserves .message-selection-toolbar, the
     data-testid "message-selection-comment-btn", and aria-label "Comment". -->
<template>
  <Teleport to="body">
    <button
      v-if="state"
      class="message-selection-toolbar"
      :style="{ top: `${pos.top}px`, left: `${pos.left}px`, width: `${BTN_WIDTH}px` }"
      data-testid="message-selection-comment-btn"
      aria-label="Comment"
      title="Comment"
      @mousedown="swallow"
      @pointerdown="swallow"
      @touchstart="swallow"
      @click="handleClick"
    >
      <svg
        width="18"
        height="18"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
      </svg>
    </button>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";

const props = defineProps<{
  onComment: (messageId: string, snippet: string) => void;
}>();

interface ToolbarState {
  messageId: string;
  snippet: string;
  rect: DOMRect;
}

const BTN_WIDTH = 40;
const BTN_HEIGHT = 36;

// Walk up from a node to find the nearest commentable message container.
function findCommentableAncestor(node: Node | null): HTMLElement | null {
  let el: Node | null = node;
  while (el) {
    if (el.nodeType === Node.ELEMENT_NODE) {
      const he = el as HTMLElement;
      if (he.dataset && he.dataset.commentable === "true" && he.dataset.messageId) {
        return he;
      }
    }
    el = (el as Node).parentNode;
  }
  return null;
}

// Build toolbar state from the current selection, or null if it isn't a
// non-collapsed selection entirely within one commentable message.
function computeState(): ToolbarState | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const text = sel.toString();
  if (!text.trim()) return null;
  const range = sel.getRangeAt(0);
  const startEl = findCommentableAncestor(range.startContainer);
  const endEl = findCommentableAncestor(range.endContainer);
  if (!startEl || startEl !== endEl) return null;
  const rect = range.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0) return null;
  return {
    messageId: startEl.dataset.messageId!,
    snippet: text,
    rect,
  };
}

const state = ref<ToolbarState | null>(null);
let rafId: number | null = null;

const pos = computed<{ top: number; left: number }>(() => {
  const s = state.value;
  if (!s) return { top: 0, left: 0 };
  // The native copy/paste menu on mobile sits directly above the selection
  // and is horizontally centered. To avoid colliding with it we anchor our
  // button to the right edge of the selection, vertically centered with the
  // selection's midpoint. On narrow selections we clamp to the viewport.
  const isCoarse = window.matchMedia("(pointer: coarse)").matches;
  let top: number;
  let left: number;
  if (isCoarse) {
    const GAP = 8;
    // Prefer to the right of the selection.
    const rightCandidate = s.rect.right + GAP;
    if (rightCandidate + BTN_WIDTH <= window.innerWidth - 8) {
      left = rightCandidate;
    } else {
      // Otherwise to the left.
      const leftCandidate = s.rect.left - BTN_WIDTH - GAP;
      left = leftCandidate >= 8 ? leftCandidate : window.innerWidth - BTN_WIDTH - 8;
    }
    const midY = s.rect.top + s.rect.height / 2 - BTN_HEIGHT / 2;
    top = Math.max(8, Math.min(window.innerHeight - BTN_HEIGHT - 8, midY));
  } else {
    // Desktop: anchor above the selection; fall back below if no room.
    const BTN_GAP = 8;
    const preferTop = s.rect.top - BTN_HEIGHT - BTN_GAP;
    top = preferTop > 8 ? preferTop : s.rect.bottom + BTN_GAP;
    const centered = s.rect.left + s.rect.width / 2 - BTN_WIDTH / 2;
    left = Math.max(8, Math.min(window.innerWidth - BTN_WIDTH - 8, centered));
  }
  return { top, left };
});

function recompute() {
  if (rafId != null) cancelAnimationFrame(rafId);
  rafId = requestAnimationFrame(() => {
    rafId = null;
    state.value = computeState();
  });
}
function hide() {
  state.value = null;
}

onMounted(() => {
  document.addEventListener("selectionchange", recompute);
  window.addEventListener("resize", hide);
  // Hide during scrolling to avoid stale positioning; user can re-select.
  window.addEventListener("scroll", hide, true);
});

onUnmounted(() => {
  document.removeEventListener("selectionchange", recompute);
  window.removeEventListener("resize", hide);
  window.removeEventListener("scroll", hide, true);
  if (rafId != null) cancelAnimationFrame(rafId);
});

function handleClick(e: MouseEvent) {
  e.stopPropagation();
  if (state.value) {
    props.onComment(state.value.messageId, state.value.snippet);
  }
  // Clear selection after injecting so toolbar hides.
  window.getSelection()?.removeAllRanges();
  state.value = null;
}

// preventDefault on pointerdown prevents the selection from being
// cleared/collapsed before we can read it in onClick.
function swallow(e: Event) {
  e.preventDefault();
}
</script>
