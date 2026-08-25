<!-- Vue port of components/ConversationTOC.tsx. Floating table-of-contents
     button + popover, backed by PrimeVue Popover (outside-click dismissal,
     Escape, viewport-aware positioning come for free — the manual
     getBoundingClientRect math and document listeners are gone). Preserves the
     toc-* class contract and the aria labels "Conversation table of contents"
     (button) and "Table of contents" (popover dialog), plus the "Jump to…"
     header. `containerRef` is passed as a plain HTMLElement (or null).

     The entry list is a plain scrollable div, NOT a PrimeVue VirtualScroller.
     Even huge conversations produce only a few hundred TOC entries, so
     virtualization buys nothing — and it cost a lot: VirtualScroller lazily
     injected three stylesheets on first open, which invalidates styles for
     the whole document (1.5s+ of recalc in a 5,000-message conversation), and
     its absolutely-positioned content div shrink-wraps the nowrap entry
     labels, forcing horizontal overflow instead of ellipsis. -->
<template>
  <button
    :class="`toc-button${open ? ' toc-button-open' : ''}`"
    aria-label="Conversation table of contents"
    aria-haspopup="true"
    :aria-expanded="open"
    v-tooltip.top="'Table of contents'"
    @click="popoverRef?.toggle($event)"
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

  <Popover
    ref="popoverRef"
    :pt="{
      root: { class: 'toc-popover', 'aria-label': 'Table of contents' },
      content: { class: 'toc-popover-content' },
    }"
    @show="handleShow"
    @hide="open = false"
  >
    <div class="toc-popover-header">
      <span class="toc-popover-title">Jump to…</span>
    </div>
    <div ref="listRef" class="toc-popover-list">
      <button
        v-for="entry in entries"
        :key="entry.id"
        :class="`toc-entry toc-entry-${entry.kind}${entry.thumbnails?.length ? ' toc-entry-with-thumbnail' : ''}${activeId === entry.id ? ' toc-entry-active' : ''}`"
        @click="handleGoto(entry)"
      >
        <span
          v-if="entry.kind !== 'gen' && entry.kind !== 'image'"
          class="toc-entry-icon"
          aria-hidden="true"
        >
          <template v-if="entry.kind === 'top'">↑</template>
          <template v-if="entry.kind === 'bottom'">↓</template>
          <template v-if="entry.kind === 'user'">•</template>
          <template v-if="entry.kind === 'eot'">✓</template>
        </span>
        <span v-if="entry.thumbnails?.length" class="toc-entry-thumbnail-wrap" aria-hidden="true">
          <img
            class="toc-entry-thumbnail"
            :src="entry.thumbnails[0].src"
            alt=""
            loading="lazy"
            decoding="async"
          />
          <span v-if="entry.thumbnails.length > 1" class="toc-entry-thumbnail-count">
            +{{ entry.thumbnails.length - 1 }}
          </span>
        </span>
        <span class="toc-entry-label">{{ entry.label }}</span>
      </button>
    </div>
  </Popover>
</template>

<script setup lang="ts">
import { computed, inject, nextTick, onUnmounted, ref, watch } from "vue";
import Popover from "primevue/popover";
import { isCompactionCarried, type Message, type LLMMessage, type LLMContent } from "../../types";
import { perfCount, perfWrap } from "../../utils/perf";
import { chunkMountKey } from "./chunkMount";

interface TOCThumbnail {
  src: string;
  alt: string;
}

interface TOCEntry {
  id: string;
  kind: "top" | "user" | "eot" | "image" | "gen" | "bottom";
  label: string;
  messageId?: string;
  toolUseId?: string;
  sourceMessageId?: string;
  generation?: number;
  thumbnails?: TOCThumbnail[];
}

const props = defineProps<{
  messages: Message[];
  containerRef: HTMLElement | null;
  nearBottom: boolean;
  conversationSlug?: string | null;
}>();
const emit = defineEmits<{
  (e: "scroll-bottom"): void;
  (e: "scroll-away"): void;
}>();

const open = ref(false);
const activeId = ref<string | null>(null);
const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const listRef = ref<HTMLElement | null>(null);
const renderedThumbnails = ref<Map<string, TOCThumbnail[]>>(new Map());

function parseLLMMessage(message: Message): LLMMessage | null {
  if (!message.llm_data) return null;
  try {
    return typeof message.llm_data === "string"
      ? (JSON.parse(message.llm_data) as LLMMessage)
      : (message.llm_data as LLMMessage);
  } catch {
    return null;
  }
}

function extractMessageLabel(message: Message, maxLen = 70): string {
  const llm = parseLLMMessage(message);
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

function fragmentForToolUse(toolUseId: string): string {
  const short = toolUseId.replace(/[^a-zA-Z0-9]/g, "").slice(0, 8);
  return `t-${short}`;
}

function targetKey(kind: "message" | "tool", id: string): string {
  return `${kind}:${id}`;
}

function thumbnailsIn(root: Element): TOCThumbnail[] {
  const seen = new Set<string>();
  const thumbnails: TOCThumbnail[] = [];
  for (const img of root.querySelectorAll("img")) {
    const src = img.getAttribute("src") || img.currentSrc;
    if (!src || seen.has(src)) continue;
    seen.add(src);
    thumbnails.push({ src, alt: img.getAttribute("alt")?.trim() || "" });
  }
  return thumbnails;
}

function collectRenderedThumbnails(container: HTMLElement): Map<string, TOCThumbnail[]> {
  const result = new Map<string, TOCThumbnail[]>();
  for (const message of container.querySelectorAll<HTMLElement>("[data-message-id]")) {
    const id = message.dataset.messageId;
    if (!id) continue;
    const thumbnails = thumbnailsIn(message);
    if (thumbnails.length) result.set(targetKey("message", id), thumbnails);
  }
  for (const anchor of container.querySelectorAll<HTMLElement>("[data-tool-use-id]")) {
    const id = anchor.dataset.toolUseId;
    const tool = anchor.nextElementSibling;
    if (!id || !tool) continue;
    result.set(targetKey("tool", id), thumbnailsIn(tool));
  }
  return result;
}

function refreshRenderedThumbnails() {
  renderedThumbnails.value = props.containerRef
    ? collectRenderedThumbnails(props.containerRef)
    : new Map();
}

function imageLabel(thumbnails: TOCThumbnail[], fallback: string): string {
  const alt = thumbnails.find((thumbnail) => thumbnail.alt)?.alt;
  return alt || fallback;
}

function record(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : null;
}

function stringField(value: unknown, field: string): string {
  const object = record(value);
  return object && typeof object[field] === "string" ? object[field] : "";
}

function browserAction(input: unknown): string {
  return stringField(input, "action");
}

function isImageTool(toolUse: LLMContent): boolean {
  if (toolUse.ToolName === "browser") return browserAction(toolUse.ToolInput) === "screenshot";
  return (
    toolUse.ToolName === "screenshot" ||
    toolUse.ToolName === "browser_take_screenshot" ||
    toolUse.ToolName === "read_image" ||
    toolUse.ToolName === "llm_one_shot"
  );
}

function fallbackToolThumbnails(toolUse: LLMContent, toolResult?: LLMContent): TOCThumbnail[] {
  if (!toolResult || !isImageTool(toolUse)) return [];
  const display = record(toolResult.Display);
  const displayPath = stringField(display, "path");
  const inputPath = stringField(toolUse.ToolInput, "path");
  const screenshotName =
    displayPath ||
    inputPath ||
    stringField(toolUse.ToolInput, "id") ||
    stringField(toolUse.ToolInput, "selector") ||
    "screenshot";
  const alt =
    toolUse.ToolName === "read_image"
      ? `Image: ${displayPath || inputPath || "image"}`
      : `Screenshot: ${screenshotName}`;
  const thumbnails: TOCThumbnail[] = [];

  for (const result of toolResult.ToolResult || []) {
    if (result.DisplayImageURL) {
      thumbnails.push({ src: result.DisplayImageURL, alt });
    }
  }

  if (toolUse.ToolName === "llm_one_shot") {
    const images = display?.images;
    if (Array.isArray(images)) {
      for (const image of images) {
        const src = stringField(image, "url");
        if (!src) continue;
        const path = stringField(image, "path");
        thumbnails.push({ src, alt: `Image: ${path || "attachment"}` });
      }
    }
  } else if (thumbnails.length === 0) {
    const src = stringField(display, "url");
    if (src) thumbnails.push({ src, alt });
  }

  const seen = new Set<string>();
  return thumbnails.filter((thumbnail) => {
    if (seen.has(thumbnail.src)) return false;
    seen.add(thumbnail.src);
    return true;
  });
}

function buildEntries(
  messages: Message[],
  thumbnailsByTarget: Map<string, TOCThumbnail[]>,
): TOCEntry[] {
  const entries: TOCEntry[] = [];
  entries.push({ id: "top", kind: "top", label: "Top of conversation" });

  const toolResultsById = new Map<string, LLMContent>();
  for (const message of messages) {
    for (const content of parseLLMMessage(message)?.Content || []) {
      if ((content.Type === 6 || content.Type === 8) && content.ToolUseID) {
        toolResultsById.set(content.ToolUseID, content);
      }
    }
  }

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

    const llm = parseLLMMessage(m);
    const content = (llm?.Content || []) as LLMContent[];
    const messageThumbnails = thumbnailsByTarget.get(targetKey("message", m.message_id)) || [];
    const text = extractMessageLabel(m);
    const onlyToolResults =
      content.length > 0 && content.every((c) => c.Type === 6 || c.Type === 4);
    let hasMessageEntry = false;

    if (m.type === "user" && !onlyToolResults && text) {
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "user",
        label: text,
        messageId: m.message_id,
        sourceMessageId: m.message_id,
        thumbnails: messageThumbnails,
      });
      hasMessageEntry = true;
    } else if (m.type === "agent" && m.end_of_turn && text) {
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "eot",
        label: text,
        messageId: m.message_id,
        sourceMessageId: m.message_id,
        thumbnails: messageThumbnails,
      });
      hasMessageEntry = true;
    }

    if (!hasMessageEntry && messageThumbnails.length) {
      entries.push({
        id: fragmentForMessage(m.message_id),
        kind: "image",
        label: imageLabel(messageThumbnails, text || "Image"),
        messageId: m.message_id,
        sourceMessageId: m.message_id,
        thumbnails: messageThumbnails,
      });
    }

    // Compaction-carried copies preserve tool-use IDs. The original occurrence
    // already owns the TOC entry and hash; adding the replay would duplicate both.
    if (isCompactionCarried(m)) continue;
    for (const item of content) {
      if ((item.Type !== 5 && item.Type !== 7) || !item.ID) continue;
      const renderedToolThumbnails = thumbnailsByTarget.get(targetKey("tool", item.ID));
      if (!renderedToolThumbnails) continue;
      const toolThumbnails = renderedToolThumbnails.length
        ? renderedToolThumbnails
        : fallbackToolThumbnails(item, toolResultsById.get(item.ID));
      if (!toolThumbnails.length) continue;
      entries.push({
        id: fragmentForToolUse(item.ID),
        kind: "image",
        label: imageLabel(toolThumbnails, "Image"),
        toolUseId: item.ID,
        sourceMessageId: m.message_id,
        thumbnails: toolThumbnails,
      });
    }
  }

  entries.push({ id: "bottom", kind: "bottom", label: "End of conversation" });
  return entries;
}

function findMessageElement(container: HTMLElement, messageId: string): HTMLElement | null {
  return container.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(messageId)}"]`);
}

function findToolElement(container: HTMLElement, toolUseId: string): HTMLElement | null {
  const anchor = container.querySelector<HTMLElement>(
    `[data-tool-use-id="${CSS.escape(toolUseId)}"]`,
  );
  return anchor?.nextElementSibling instanceof HTMLElement ? anchor.nextElementSibling : anchor;
}

function findElementByFragment(container: HTMLElement, fragment: string): HTMLElement | null {
  const isMessage = fragment.startsWith("m-");
  const isTool = fragment.startsWith("t-");
  if (!isMessage && !isTool) return null;
  const short = fragment.slice(2);
  const attr = isMessage ? "data-message-id" : "data-tool-use-id";
  const all = container.querySelectorAll<HTMLElement>(`[${attr}]`);
  for (const anchor of all) {
    const id = anchor.getAttribute(attr) || "";
    const norm = id.replace(/[^a-zA-Z0-9]/g, "");
    if (!norm.startsWith(short)) continue;
    if (isTool && anchor.nextElementSibling instanceof HTMLElement) {
      return anchor.nextElementSibling;
    }
    return anchor;
  }
  return null;
}

function highlight(el: HTMLElement) {
  el.classList.remove("message-highlight");
  void el.offsetWidth;
  el.classList.add("message-highlight");
  window.setTimeout(() => el.classList.remove("message-highlight"), 2200);
}

function toolUseIdForElement(el: HTMLElement): string | null {
  if (el.dataset.toolUseId) return el.dataset.toolUseId;
  const anchor = el.previousElementSibling;
  return anchor instanceof HTMLElement ? anchor.dataset.toolUseId || null : null;
}

function highlightTool(container: HTMLElement, toolUseId: string, el: HTMLElement) {
  highlight(el);
  if (!el.classList.contains("tool-card-mount-placeholder")) return;

  let tries = 0;
  const requery = () => {
    const current = findToolElement(container, toolUseId);
    if (current && current !== el && !current.classList.contains("tool-card-mount-placeholder")) {
      highlight(current);
      return;
    }
    if (++tries < 40) window.setTimeout(requery, 100);
  };
  requery();
}

// Defined in <script setup> (uses local helpers). Not exported: <script setup>
// cannot contain ES module exports, and nothing imports this symbol. The React
// module exported it for parity but only used it internally, as we do here.
function scrollToFragment(
  container: HTMLElement,
  fragment: string,
  options: { highlight?: boolean } = {},
): boolean {
  const el = findElementByFragment(container, fragment);
  if (!el) {
    // Tail-first mounting may not have reached the target's chunk yet. Ask
    // for it; the caller's retry loop re-queries after the mount lands.
    chunkMount?.revealTarget({ fragment });
    return false;
  }
  el.scrollIntoView({
    behavior: el.classList.contains("tool-card-mount-placeholder") ? "auto" : "smooth",
    block: "start",
  });
  if (options.highlight !== false) {
    const toolUseId = fragment.startsWith("t-") ? toolUseIdForElement(el) : null;
    if (toolUseId) highlightTool(container, toolUseId, el);
    else highlight(el);
  }
  return true;
}

const entries = computed(
  perfWrap("toc.buildEntries", () => buildEntries(props.messages, renderedThumbnails.value)),
);
const activeEntryByMessageId = computed(() => {
  const entriesBySourceMessageId = new Map<string, TOCEntry[]>();
  for (const entry of entries.value) {
    if (!entry.sourceMessageId) continue;
    const sourceEntries = entriesBySourceMessageId.get(entry.sourceMessageId) || [];
    sourceEntries.push(entry);
    entriesBySourceMessageId.set(entry.sourceMessageId, sourceEntries);
  }

  const result = new Map<string, string>();
  let active = "top";
  for (const message of props.messages) {
    const sourceEntries = entriesBySourceMessageId.get(message.message_id) || [];
    const messageEntry = sourceEntries.find((entry) => entry.messageId === message.message_id);
    if (messageEntry) active = messageEntry.id;
    result.set(message.message_id, active);
    for (const entry of sourceEntries) {
      if (entry.toolUseId) active = entry.id;
    }
  }
  return result;
});
const activeEntryByToolUseId = computed(() => {
  const result = new Map<string, string>();
  for (const entry of entries.value) {
    if (entry.toolUseId) result.set(entry.toolUseId, entry.id);
  }
  return result;
});

function handleShow() {
  refreshRenderedThumbnails();
  open.value = true;
  nextTick(() => {
    const list = listRef.value;
    if (!list) return;
    const index = entries.value.findIndex((entry) => entry.id === activeId.value);
    if (index <= 0) {
      list.scrollTop = 0;
      return;
    }
    const el = list.children[index] as HTMLElement | undefined;
    // Center the active entry (block: "center" without scrolling ancestors).
    if (el) list.scrollTop = el.offsetTop - (list.clientHeight - el.offsetHeight) / 2;
  });
}

interface TOCTarget {
  messageId?: string;
  toolUseId?: string;
}

function targetAtCutoff(container: HTMLElement): TOCTarget | null {
  const rect = container.getBoundingClientRect();
  const x = rect.left + rect.width / 2;
  const cutoff = Math.min(rect.bottom - 1, rect.top + 80);
  for (const offset of [0, -1, 1, -2, 2, -4, 4]) {
    const target = document.elementFromPoint(x, cutoff + offset);
    let el = target instanceof HTMLElement ? target : null;
    while (el && container.contains(el)) {
      if (el.dataset.messageId) return { messageId: el.dataset.messageId };
      const previous = el.previousElementSibling;
      if (previous instanceof HTMLElement && previous.dataset.toolUseId) {
        return { toolUseId: previous.dataset.toolUseId };
      }
      el = el.parentElement;
    }
  }
  return null;
}

// Active-entry tracking on scroll.
let scrollContainer: HTMLElement | null = null;
let scrollHandler: (() => void) | null = null;

function attachScroll() {
  detachScroll();
  const container = props.containerRef;
  if (!container) return;
  const update = () => {
    perfCount("toc.scrollUpdate");
    if (props.nearBottom) {
      activeId.value = "bottom";
      return;
    }
    if (container.scrollTop <= 40) {
      activeId.value = "top";
      return;
    }

    const target = targetAtCutoff(container);
    if (target?.toolUseId) {
      const entryId = activeEntryByToolUseId.value.get(target.toolUseId);
      if (entryId) activeId.value = entryId;
    } else if (target?.messageId) {
      activeId.value = activeEntryByMessageId.value.get(target.messageId) ?? null;
    }
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

watch([() => props.containerRef, entries, () => props.nearBottom], attachScroll, {
  immediate: true,
});

watch([() => props.messages.length, () => props.containerRef], () => {
  if (open.value) nextTick(refreshRenderedThumbnails);
});

const chunkMount = inject(chunkMountKey, null);

/** Mount the target's chunk (tail-first rendering) before a DOM query for
 * it. Resolves after Vue patched the mount in, so the query will see it. */
async function ensureTargetMounted(target: {
  messageId?: string;
  toolUseId?: string;
}): Promise<void> {
  if (chunkMount?.revealTarget(target)) await nextTick();
}

function handleGoto(entry: TOCEntry) {
  const container = props.containerRef;
  if (!container) return;
  popoverRef.value?.hide();
  if (entry.kind === "bottom") {
    if (!props.nearBottom) emit("scroll-bottom");
    history.replaceState(null, "", window.location.pathname + window.location.search);
    return;
  }
  emit("scroll-away");
  if (entry.kind === "top") {
    container.scrollTo({ top: 0, behavior: "smooth" });
    history.replaceState(null, "", window.location.pathname + window.location.search);
    return;
  }
  if (entry.kind === "gen") {
    const target = props.messages.find((m) => m.generation === entry.generation);
    if (target) {
      void ensureTargetMounted({ messageId: target.message_id }).then(() => {
        const el = findMessageElement(container, target.message_id);
        if (el) {
          el.scrollIntoView({ behavior: "smooth", block: "start" });
          highlight(el);
        }
      });
    }
    return;
  }
  if (entry.toolUseId) {
    const toolUseId = entry.toolUseId;
    void ensureTargetMounted({ toolUseId }).then(() => {
      const el = findToolElement(container, toolUseId);
      if (!el) return;
      el.scrollIntoView({
        behavior: el.classList.contains("tool-card-mount-placeholder") ? "auto" : "smooth",
        block: "start",
      });
      highlightTool(container, toolUseId, el);
      const url = `${window.location.pathname}${window.location.search}#${entry.id}`;
      history.replaceState(null, "", url);
    });
    return;
  }
  if (!entry.messageId) return;
  const messageId = entry.messageId;
  void ensureTargetMounted({ messageId }).then(() => {
    const el = findMessageElement(container, messageId);
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "start" });
    highlight(el);
    const url = `${window.location.pathname}${window.location.search}#${entry.id}`;
    history.replaceState(null, "", url);
  });
}

// Resolve URL fragment on mount + on messages/hash change.
function resolveFragmentWithRetry() {
  const container = props.containerRef;
  if (!container) return;
  const fragment = window.location.hash.slice(1);
  if (!fragment) return;
  emit("scroll-away");
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
  if (fragment) {
    emit("scroll-away");
    scrollToFragment(container, fragment);
  }
}
window.addEventListener("hashchange", onHashChange);

onUnmounted(() => {
  detachScroll();
  window.removeEventListener("hashchange", onHashChange);
});
</script>
