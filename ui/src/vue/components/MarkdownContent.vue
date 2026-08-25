<!-- Vue port of components/MarkdownContent.tsx. Renders sanitized markdown HTML
     via v-html. The pure pipeline lives in utils/markdownRender.ts.
     Preserves the .markdown-content .break-words container contract.

     With `commentable`, images in the rendered markdown open the image
     annotation view when clicked (or activated from the keyboard). That is
     opt-in because the view is hosted by ChatInterface: the export page renders
     the same markdown with no host, and an image that announces itself as a
     button and then does nothing is worse than a plain image. -->
<template>
  <div
    ref="containerRef"
    class="markdown-content break-words"
    @click="onImageActivate"
    @keydown="onImageActivate"
    v-html="html"
  ></div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { highlightCode, normalizeCodeLanguage } from "../../services/markdownHighlight";
import { applyHighlightTokens } from "../../utils/codeHighlight";
import { COMMENT_ICON } from "../../utils/icons";
import { renderMarkdownToSafeHTML } from "../../utils/markdownRender";
import { perfWrap } from "../../utils/perf";
import { handleImageCommentClick, openImageComment } from "../composables/imageComment";
import { whenNearViewport } from "../composables/nearViewport";

const props = defineProps<{
  text: string;
  // When set, local-path markdown images (relative or absolute file paths) are
  // rewritten to the per-message file endpoint and rendered. Without it we
  // cannot authorize a local file, so such images are dropped.
  messageId?: string;
  // Make images here open the annotation view. Only for hosts rendered inside
  // ChatInterface, which owns that view. Read as a fixed property of the host,
  // not something toggled on a mounted instance: turning it off would need the
  // wrappers below torn down again.
  commentable?: boolean;
  // Object whose lifetime bounds the render cache entry (the owning,
  // immutable Message). Omitted by callers whose text isn't tied to a
  // stable, immutable object — the streaming preview, the distillation
  // preview, export — which always re-render.
  cacheOwner?: object;
  // Distinguishes multiple markdown runs within the same cacheOwner (e.g. a
  // message with several text blocks split by tool calls). Required
  // whenever cacheOwner is set.
  runKey?: string;
}>();

const containerRef = ref<HTMLDivElement | null>(null);

// Highlighting swaps a block's single text node for one span per token —
// measured at 22% of all DOM elements in a large conversation when done
// eagerly, nearly all of it far off-screen. Defer each block until it comes
// within a viewport of view (same shared observer that gates tool cards),
// then tokenize.
let cancelDeferred: (() => void)[] = [];
onBeforeUnmount(() => {
  for (const cancel of cancelDeferred) cancel();
  cancelDeferred = [];
});

const html = computed(
  perfWrap("markdown.render", () =>
    renderMarkdownToSafeHTML(
      props.text,
      props.messageId,
      props.cacheOwner && props.runKey !== undefined
        ? { owner: props.cacheOwner, runKey: props.runKey }
        : undefined,
    ),
  ),
);

// Images inside a link are excluded throughout: there the image is the link's
// label, so the anchor owns activation and calling it a button would both
// mis-announce it and add a redundant tab stop. The badge wrapper is a <span>,
// not an <a>, so `closest("a")` stays an accurate test after wrapping.
function isCommentable(img: HTMLImageElement): boolean {
  return !!props.commentable && !img.parentElement?.closest("a");
}

// Give commentable images button semantics, a tab stop, and the hover badge.
// Done in bulk after each render (v-html replaces the subtree) rather than
// per-image, which is also why activation is handled by one delegated listener
// below. The wrapper is what the badge positions against, matching
// CommentableImage.vue's markup so both get the same affordance.
watch(
  [html, containerRef],
  () => {
    // Cancel before the null guard: if the container vanished, stale
    // registrations would otherwise pin the detached subtree via the
    // observer's target set.
    for (const cancel of cancelDeferred) cancel();
    cancelDeferred = [];
    const root = containerRef.value;
    if (!root) return;
    if (props.commentable) {
      for (const img of root.querySelectorAll("img")) {
        // The wrapper marks an image as already done: this runs whenever the
        // container ref settles, not only when the HTML is replaced.
        if (!isCommentable(img) || img.closest(".commentable-image-link")) continue;
        img.setAttribute("role", "button");
        img.setAttribute("tabindex", "0");
        img.classList.add("commentable-image");
        const wrap = document.createElement("span");
        wrap.className = "commentable-image-link";
        img.replaceWith(wrap);
        wrap.append(img, badge());
      }
    }
    highlightFencedCode(root);
  },
  { flush: "post", immediate: true },
);

function languageFor(code: HTMLElement): string | undefined {
  for (const className of code.classList) {
    const match = /^language-(.+)$/.exec(className);
    if (match) return normalizeCodeLanguage(match[1]);
  }
  return undefined;
}

function highlightFencedCode(root: HTMLElement): void {
  for (const code of root.querySelectorAll<HTMLElement>("pre > code")) {
    const state = code.dataset.shelleyCodeHighlight;
    if (state && state !== "deferred") continue;
    const language = languageFor(code);
    if (!language) continue;

    // "deferred" blocks re-register on every pass (the watch cancels all
    // previous registrations first) because v-html replacement may have
    // produced brand-new elements.
    code.dataset.shelleyCodeHighlight = "deferred";
    cancelDeferred.push(
      whenNearViewport(code, () => highlightBlock(root, code, language), { printReveal: false }),
    );
  }
}

function highlightBlock(root: HTMLElement, code: HTMLElement, language: string): void {
  // The observer can fire in the window between a v-html replacement and the
  // post-flush watch pass that would have canceled this registration.
  if (!code.isConnected) return;
  const source = code.textContent ?? "";
  code.dataset.shelleyCodeHighlight = "pending";
  void highlightCode(language, source)
    .then((result) => {
      // v-html can replace this code block while the worker is still tokenizing.
      if (!root.contains(code) || code.dataset.shelleyCodeHighlight !== "pending") return;
      if (code.textContent !== source) return;
      if (result.kind === "unknown") {
        delete code.dataset.shelleyCodeHighlight;
        return;
      }
      applyHighlightTokens(code, source, result.lines);
      code.dataset.shelleyCodeHighlight = language;
    })
    .catch((error: unknown) => {
      if (root.contains(code) && code.dataset.shelleyCodeHighlight === "pending") {
        delete code.dataset.shelleyCodeHighlight;
      }
      console.error("Syntax highlighting failed", error);
    });
}

function badge(): HTMLElement {
  const el = document.createElement("span");
  el.className = "commentable-image-badge";
  el.setAttribute("aria-hidden", "true");
  el.innerHTML = `${COMMENT_ICON} Comment`;
  return el;
}

function onImageActivate(e: MouseEvent | KeyboardEvent) {
  const img = e.target;
  if (!(img instanceof HTMLImageElement) || !isCommentable(img)) return;
  if (e instanceof KeyboardEvent) {
    // Only the activation keys, and only once the target is known to be an
    // image: a blanket space-prevent here would cost every message its
    // space-to-scroll. Autorepeat is ignored so holding a key doesn't churn.
    if (e.repeat || (e.key !== "Enter" && e.key !== " ")) return;
    e.preventDefault();
    openImageComment({ src: img.src });
    return;
  }
  handleImageCommentClick(e, { src: img.src });
}
</script>
