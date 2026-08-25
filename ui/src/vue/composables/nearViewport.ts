// Sticky "has this element ever been near the viewport?" tracking, built on a
// single shared IntersectionObserver.
//
// Used to defer expensive hydration (e.g. @pierre/diffs FileDiff instances,
// fenced-code syntax highlighting) until the user can actually see the
// result. In a huge conversation the message list contains hundreds of diffs
// and thousands of code fences; hydrating them all up front puts hundreds of
// thousands of elements into the document, which makes every viewport resize
// re-lay-out for seconds. Deferring keeps the typical case (reading the
// recent tail of the conversation) small.
//
// The flag is sticky: once an element has been near the viewport it stays
// "near" forever, so hydrated content is never torn down by scrolling away.
import { onBeforeUnmount, ref, watch, type Ref } from "vue";

// One viewport-height of lookahead. Note the margin expands the viewport
// root only: targets inside a scroll container are clipped by its box first,
// so for message-list content hydration effectively lands on scrollport
// entry rather than one viewport ahead.
const ROOT_MARGIN = "100%";

type Callback = () => void;
let sharedObserver: IntersectionObserver | null = null;
const callbacks = new WeakMap<Element, Callback>();
const pendingElements = new Set<Element>();

function reveal(element: Element): void {
  const cb = callbacks.get(element);
  if (!cb) return;
  callbacks.delete(element);
  pendingElements.delete(element);
  sharedObserver?.unobserve(element);
  cb();
}

// Printing must include the real tool cards, not blank geometry placeholders.
// One shared listener keeps this O(1) in listener count even for huge histories.
if (typeof window !== "undefined") {
  window.addEventListener("beforeprint", () => {
    for (const element of [...pendingElements]) reveal(element);
  });
}

function observer(): IntersectionObserver {
  if (!sharedObserver) {
    sharedObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          reveal(entry.target);
        }
      },
      { rootMargin: ROOT_MARGIN },
    );
  }
  return sharedObserver;
}

// Calls `cb` once, when `element` comes within one viewport of view. Returns
// a cancel function; canceling after the callback fired (or after a newer
// registration replaced this one) is a no-op. If IntersectionObserver is
// unavailable (jsdom), the callback runs synchronously. This is the
// raw-element form for content that Vue does not own per-node (e.g. v-html
// markdown); components should prefer useNearViewport.
//
// printReveal opts the element into the beforeprint reveal above. Content
// whose hydration changes what prints (tool cards: geometry placeholders)
// must pass true; content whose hydration is print-invisible (code
// highlighting: colors only, and async — the worker response lands after the
// print snapshot anyway) must pass false so printing a huge conversation
// does not stampede thousands of pointless hydrations.
export function whenNearViewport(
  element: Element,
  cb: Callback,
  opts: { printReveal: boolean },
): () => void {
  if (typeof IntersectionObserver === "undefined") {
    cb();
    return () => {};
  }
  if (callbacks.has(element)) throw new Error("whenNearViewport: element already registered");
  callbacks.set(element, cb);
  if (opts.printReveal) pendingElements.add(element);
  observer().observe(element);
  return () => {
    if (callbacks.get(element) !== cb) return;
    callbacks.delete(element);
    pendingElements.delete(element);
    sharedObserver?.unobserve(element);
  };
}

// Returns a ref that flips to true once `el` comes within one viewport of
// view (and stays true). If IntersectionObserver is unavailable (jsdom), the
// ref is true immediately.
export function useNearViewport(el: Ref<Element | null>): Ref<boolean> {
  const printing =
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("print").matches;
  const near = ref(typeof IntersectionObserver === "undefined" || printing);
  let cancel: (() => void) | null = null;

  watch(
    el,
    (element) => {
      cancel?.();
      cancel = null;
      if (element && !near.value) {
        cancel = whenNearViewport(
          element,
          () => {
            near.value = true;
          },
          { printReveal: true },
        );
      }
    },
    { immediate: true, flush: "post" },
  );

  onBeforeUnmount(() => {
    cancel?.();
    cancel = null;
  });

  return near;
}
