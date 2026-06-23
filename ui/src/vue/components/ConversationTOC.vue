<!-- Vue port of components/ConversationTOC.tsx. Floating table-of-contents
     button + popover. Preserves the toc-* class contract and the aria labels
     "Conversation table of contents" (button) and "Table of contents"
     (popover dialog), plus the "Jump to…" header. `containerRef` is passed as
     a plain HTMLElement (or null) instead of a React ref object.
     scrollToFragment is exported for parity with the React module. -->
<template>
  <button
    ref="buttonRef"
    :class="`toc-button${open ? ' toc-button-open' : ''}`"
    aria-label="Conversation table of contents"
    aria-haspopup="true"
    :aria-expanded="open"
    title="Table of contents"
    @click="open = !open"
  >
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" class="toc-button-icon">
      <line x1="8" y1="6" x2="20" y2="6" :stroke-width="2" stroke-linecap="round" />
      <line x1="8" y1="12" x2="20" y2="12" :stroke-width="2" stroke-linecap="round" />
      <line x1="8" y1="18" x2="20" y2="18" :stroke-width="2" stroke-linecap="round" />
      <circle cx="4" cy="6" r="1.4" fill="currentColor" />
      <circle cx="4" cy="12" r="1.4" fill="currentColor" />
      <circle cx="4" cy="18" r="1.4" fill="currentColor" />
    </svg>
  </button>

  <div
    v-if="open"
    ref="popoverRef"
    class="toc-popover"
    role="dialog"
    aria-label="Table of contents"
    :style="
      popoverPos ? { bottom: `${popoverPos.bottom}px`, right: `${popoverPos.right}px` } : undefined
    "
  >
    <div class="toc-popover-header">
      <span class="toc-popover-title">Jump to…</span>
    </div>
    <div class="toc-popover-list">
      <button
        v-for="entry in entries"
        :key="entry.id"
        :class="`toc-entry toc-entry-${entry.kind}${activeId === entry.id ? ' toc-entry-active' : ''}`"
        @click="handleGoto(entry)"
      >
        <span v-if="entry.kind !== 'gen'" class="toc-entry-icon" aria-hidden="true">
          <template v-if="entry.kind === 'top'">↑</template>
          <template v-if="entry.kind === 'bottom'">↓</template>
          <template v-if="entry.kind === 'user'">•</template>
          <template v-if="entry.kind === 'eot'">✓</template>
        </span>
        <span class="toc-entry-label">{{ entry.label }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import type { Message, LLMMessage, LLMContent } from "../../types";

interface TOCEntry {
  id: string;
  kind: "top" | "user" | "eot" | "gen" | "bottom";
  label: string;
  messageId?: string;
  generation?: number;
}

const props = defineProps<{
  messages: Message[];
  containerRef: HTMLElement | null;
  conversationSlug?: string | null;
}>();

const open = ref(false);
const activeId = ref<string | null>(null);
const popoverRef = ref<HTMLDivElement | null>(null);
const buttonRef = ref<HTMLButtonElement | null>(null);
const popoverPos = ref<{ bottom: number; right: number } | null>(null);

function extractMessageLabel(message: Message, maxLen = 70): string {
  if (!message.llm_data) return "";
  let llm: LLMMessage | null = null;
  try {
    llm =
      typeof message.llm_data === "string"
        ? (JSON.parse(message.llm_data) as LLMMessage)
        : (message.llm_data as LLMMessage);
  } catch {
    return "";
  }
  if (!llm?.Content) return "";
  const parts: string[] = [];
  for (const c of llm.Content as LLMContent[]) {
    if (c.Type === 2 && c.Text) parts.push(c.Text);
  }
  let s = parts.join(" ").replace(/\s+/g, " ").trim();
  s = s.replace(/^[#>*\-`\s]+/, "").trim();
  if (s.length > maxLen) s = s.slice(0, maxLen - 1) + "…";
  return s;
}

function fragmentForMessage(messageId: string): string {
  const short = messageId.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8);
  return `m-${short}`;
}

function buildEntries(messages: Message[]): TOCEntry[] {
  const entries: TOCEntry[] = [];
  entries.push({ id: "top", kind: "top", label: "Top of conversation" });

  let prevGen: number | null = null;
  for (const m of messages) {
    if (m.generation !== prevGen) {
      if (prevGen !== null) {
        entries.push({
          id: `gen-${m.generation}`,
          kind: "gen",
          label: `New generation (${m.generation})`,
          generation: m.generation,
        });
      }
      prevGen = m.generation;
    }
    if (m.type === "user") {
      let onlyToolResults = false;
      if (m.llm_data) {
        try {
          const llm =
            typeof m.llm_data === "string" ? JSON.parse(m.llm_data) : (m.llm_data as LLMMessage);
          const content = (llm?.Content || []) as LLMContent[];
          onlyToolResults =
            content.length > 0 && content.every((c) => c.Type === 6 || c.Type === 4);
        } catch {
          // ignore
        }
      }
      if (onlyToolResults) continue;
      const text = extractMessageLabel(m);
      if (!text) continue;
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "user",
        label: text,
        messageId: m.message_id,
      });
    } else if (m.type === "agent" && m.end_of_turn) {
      const text = extractMessageLabel(m);
      if (!text) continue;
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "eot",
        label: text,
        messageId: m.message_id,
      });
    }
  }

  entries.push({ id: "bottom", kind: "bottom", label: "End of conversation" });
  return entries;
}

function findMessageElement(container: HTMLElement, messageId: string): HTMLElement | null {
  return container.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(messageId)}"]`);
}

function findMessageElementByFragment(
  container: HTMLElement,
  fragment: string,
): HTMLElement | null {
  if (!fragment.startsWith("m-")) return null;
  const short = fragment.slice(2);
  const all = container.querySelectorAll<HTMLElement>("[data-message-id]");
  for (const el of all) {
    const mid = el.getAttribute("data-message-id") || "";
    const norm = mid.replace(/[^a-zA-Z0-9]/g, "");
    if (norm.startsWith(short)) return el;
  }
  return null;
}

function highlight(el: HTMLElement) {
  el.classList.remove("message-highlight");
  void el.offsetWidth;
  el.classList.add("message-highlight");
  window.setTimeout(() => el.classList.remove("message-highlight"), 2200);
}

// Defined in <script setup> (uses local helpers). Not exported: <script setup>
// cannot contain ES module exports, and nothing imports this symbol. The React
// module exported it for parity but only used it internally, as we do here.
function scrollToFragment(
  container: HTMLElement,
  fragment: string,
  options: { highlight?: boolean } = {},
): boolean {
  const el = findMessageElementByFragment(container, fragment);
  if (!el) return false;
  el.scrollIntoView({ behavior: "smooth", block: "start" });
  if (options.highlight !== false) highlight(el);
  return true;
}

const entries = computed(() => buildEntries(props.messages));

// Active-entry tracking on scroll.
let scrollContainer: HTMLElement | null = null;
let scrollHandler: (() => void) | null = null;

function attachScroll() {
  detachScroll();
  const container = props.containerRef;
  if (!container) return;
  const update = () => {
    let active: string | null = null;
    const containerRect = container.getBoundingClientRect();
    const cutoff = containerRect.top + 80;
    for (const entry of entries.value) {
      if (entry.kind === "top") {
        if (container.scrollTop <= 40) active = entry.id;
        continue;
      }
      if (entry.kind === "bottom") continue;
      if (entry.kind === "gen") continue;
      if (!entry.messageId) continue;
      const el = findMessageElement(container, entry.messageId);
      if (!el) continue;
      if (el.getBoundingClientRect().top <= cutoff) active = entry.id;
    }
    const nearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 80;
    if (nearBottom) active = "bottom";
    activeId.value = active;
  };
  update();
  container.addEventListener("scroll", update, { passive: true });
  scrollContainer = container;
  scrollHandler = update;
}

function detachScroll() {
  if (scrollContainer && scrollHandler) {
    scrollContainer.removeEventListener("scroll", scrollHandler);
  }
  scrollContainer = null;
  scrollHandler = null;
}

watch([() => props.containerRef, entries], attachScroll, { immediate: true });

// Popover positioning.
function updatePos() {
  const btn = buttonRef.value;
  if (!btn) return;
  const rect = btn.getBoundingClientRect();
  popoverPos.value = {
    bottom: window.innerHeight - rect.top + 8,
    right: window.innerWidth - rect.right,
  };
}

function onDown(e: MouseEvent) {
  const t = e.target as Node;
  if (popoverRef.value?.contains(t)) return;
  if (buttonRef.value?.contains(t)) return;
  open.value = false;
}
function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") open.value = false;
}

function detachOpen() {
  window.removeEventListener("resize", updatePos);
  window.removeEventListener("scroll", updatePos, true);
  document.removeEventListener("mousedown", onDown);
  document.removeEventListener("keydown", onKey);
}

watch(open, (isOpen) => {
  detachOpen();
  if (isOpen) {
    updatePos();
    window.addEventListener("resize", updatePos);
    window.addEventListener("scroll", updatePos, true);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
  }
});

function handleGoto(entry: TOCEntry) {
  const container = props.containerRef;
  if (!container) return;
  open.value = false;
  if (entry.kind === "top") {
    container.scrollTo({ top: 0, behavior: "smooth" });
    history.replaceState(null, "", window.location.pathname + window.location.search);
    return;
  }
  if (entry.kind === "bottom") {
    let lastHeight = -1;
    let stable = 0;
    let frames = 0;
    const step = () => {
      const el = props.containerRef;
      if (!el) return;
      el.scrollTop = el.scrollHeight;
      if (el.scrollHeight === lastHeight) {
        if (++stable >= 3) return;
      } else {
        stable = 0;
        lastHeight = el.scrollHeight;
      }
      if (++frames < 60) requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
    history.replaceState(null, "", window.location.pathname + window.location.search);
    return;
  }
  if (entry.kind === "gen") {
    const target = props.messages.find((m) => m.generation === entry.generation);
    if (target) {
      const el = findMessageElement(container, target.message_id);
      if (el) {
        el.scrollIntoView({ behavior: "smooth", block: "start" });
        highlight(el);
      }
    }
    return;
  }
  if (!entry.messageId) return;
  const el = findMessageElement(container, entry.messageId);
  if (!el) return;
  el.scrollIntoView({ behavior: "smooth", block: "start" });
  highlight(el);
  const url = `${window.location.pathname}${window.location.search}#${entry.id}`;
  history.replaceState(null, "", url);
}

// Resolve URL fragment on mount + on messages/hash change.
function resolveFragmentWithRetry() {
  const container = props.containerRef;
  if (!container) return;
  const fragment = window.location.hash.slice(1);
  if (!fragment) return;
  let tries = 0;
  const tryScroll = () => {
    if (scrollToFragment(container, fragment)) return;
    if (++tries < 10) window.setTimeout(tryScroll, 100);
  };
  tryScroll();
}

watch([() => props.messages.length, () => props.containerRef], resolveFragmentWithRetry, {
  immediate: true,
});

function onHashChange() {
  const container = props.containerRef;
  if (!container) return;
  const fragment = window.location.hash.slice(1);
  if (fragment) scrollToFragment(container, fragment);
}
window.addEventListener("hashchange", onHashChange);

onUnmounted(() => {
  detachScroll();
  detachOpen();
  window.removeEventListener("hashchange", onHashChange);
});
</script>
