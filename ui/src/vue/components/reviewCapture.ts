// Timestamped capture of what a reviewer sees, points at, and selects while
// narrating a recording. Times are milliseconds from the audio start (t0), so
// the server can interleave them with word timings. The DOM is described via
// `data-review` labels (outermost to innermost form a breadcrumb) and nearby
// block text. Content the DOM cannot describe (Monaco's virtualized editor,
// what is on screen) is supplied by the ReviewContext hooks.

export type ReviewSide = "old" | "new";

export interface ReviewTarget {
  where: string;
  file?: string;
  side?: ReviewSide;
  line?: number;
  end_line?: number;
  text?: string;
}

export interface ReviewView {
  where: string;
  mode: string;
  commit?: string;
  file?: string;
}

export type ReviewEvent = { ms: number; type: string } & Partial<ReviewTarget> &
  Partial<ReviewView> & {
    items?: string[];
    open?: boolean;
    state?: string;
  };

export interface ReviewContext {
  view(): ReviewView;
  // undefined defers to DOM resolution; null means "nothing there".
  pointAt(x: number, y: number, element: Element): ReviewTarget | null | undefined;
  selection(): ReviewTarget | null | undefined;
  onScreen(): string[];
}

export interface ReviewCaptureState {
  pointer: string;
  // The text under the pointer (a line, sentence, or short block).
  pointerText: string;
  selection: string;
  // First region on screen; what touch users see, with no pointer.
  screen: string;
  view: string;
}

const BLOCK_SELECTOR =
  "p,li,h1,h2,h3,h4,h5,h6,pre,blockquote,td,th,summary,dt,dd,figcaption,label,button,a,select,[role=option],[role=treeitem]";
const POINTER_TEXT_LIMIT = 200;
const SELECTION_TEXT_LIMIT = 4000;

export function clipSelection(text: string): string {
  return text.length > SELECTION_TEXT_LIMIT ? `${text.slice(0, SELECTION_TEXT_LIMIT)}…` : text;
}

export function collapse(text: string | null | undefined, limit = POINTER_TEXT_LIMIT): string {
  const value = (text ?? "").replace(/\s+/g, " ").trim();
  return value.length > limit ? `${value.slice(0, limit - 1)}…` : value;
}

// Parent across shadow boundaries; Monaco's context menu is in a shadow root.
function composedParent(node: Node): Node | null {
  if (node.parentNode instanceof ShadowRoot) return node.parentNode.host;
  return node.parentNode;
}

function composedContains(root: Node, node: Node | null): boolean {
  for (let current = node; current; current = composedParent(current)) {
    if (current === root) return true;
  }
  return false;
}

function composedClosest(node: Node | null, selector: string): Element | null {
  for (let current = node; current; current = composedParent(current)) {
    if (current instanceof Element && current.matches(selector)) return current;
  }
  return null;
}

export function breadcrumb(node: Node | null): string[] {
  const labels: string[] = [];
  for (let current = node; current; current = composedParent(current)) {
    if (current instanceof HTMLElement && current.dataset.review)
      labels.unshift(current.dataset.review);
  }
  return labels;
}

function joinWhere(labels: string[], leaf?: string): string {
  return [...labels, ...(leaf ? [leaf] : [])].join(" › ");
}

function elementLabel(element: Element): string {
  return collapse(
    element.getAttribute("aria-label") ||
      element.getAttribute("title") ||
      element.getAttribute("data-tooltip") ||
      element.textContent,
    80,
  );
}

export interface Caret {
  node: Text;
  offset: number;
}

// The character under a viewport point. The caret APIs snap to the nearest
// text, so this checks the point is on the glyph itself.
export function caretAt(x: number, y: number): Caret | null {
  let node: Node | null = null;
  let offset = 0;
  if (document.caretPositionFromPoint) {
    const position = document.caretPositionFromPoint(x, y);
    node = position?.offsetNode ?? null;
    offset = position?.offset ?? 0;
  } else if (document.caretRangeFromPoint) {
    const range = document.caretRangeFromPoint(x, y);
    node = range?.startContainer ?? null;
    offset = range?.startOffset ?? 0;
  }
  if (!(node instanceof Text)) return null;
  const range = document.createRange();
  // The caret sits between characters: the glyph is on either side of it.
  for (const at of [offset, offset - 1]) {
    if (at < 0 || at >= node.length) continue;
    range.setStart(node, at);
    range.setEnd(node, at + 1);
    for (const rect of range.getClientRects()) {
      if (x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom)
        return { node, offset: at };
    }
  }
  return null;
}

// ES2022's Intl.Segmenter, where the browser has it.
type SentenceSegmenter = {
  segment(text: string): {
    containing(index: number): { segment: string; index: number } | undefined;
  };
};
const Segmenter = (
  Intl as unknown as {
    Segmenter?: new (locale?: string, options?: { granularity: "sentence" }) => SentenceSegmenter;
  }
).Segmenter;
let sentences: SentenceSegmenter | null = null;

// text collapsed, or when too long, the stretch of it around index at.
// Stretches start at fixed steps, so the text (and with it the pointer
// event) changes every hundred characters, not with every one.
function around(text: string, at: number): string {
  const value = collapse(text, Infinity);
  if (value.length <= POINTER_TEXT_LIMIT) return value;
  const step = POINTER_TEXT_LIMIT / 2;
  const center = collapse(text.slice(0, at), Infinity).length;
  const start = Math.max(
    0,
    Math.min(Math.floor(center / step) * step - step / 2, value.length - POINTER_TEXT_LIMIT),
  );
  const end = start + POINTER_TEXT_LIMIT;
  const head = start > 0 ? "…" : "";
  const tail = end < value.length ? "…" : "";
  return head + value.slice(start + head.length, end - tail.length) + tail;
}

// The text a pointer at caret means within block: its source line in
// preformatted text, else the whole block when short, else the sentence
// under the pointer. A long paragraph's opening says little about "this".
// Overlong lines and sentences clip to the stretch around the pointer.
export function pointedText(block: Element, caret: Caret | null): string {
  const full = block.textContent ?? "";
  const whole = collapse(full);
  if (!caret || !block.contains(caret.node)) return whole;
  let at = caret.offset;
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node && node !== caret.node; node = walker.nextNode())
    at += (node as Text).length;
  if (block.closest("pre")) {
    const start = full.lastIndexOf("\n", at - 1) + 1;
    const end = full.indexOf("\n", at);
    return around(full.slice(start, end < 0 ? undefined : end), at - start) || whole;
  }
  if (whole.length < POINTER_TEXT_LIMIT) return whole;
  if (!Segmenter) return around(full, at);
  sentences ??= new Segmenter(undefined, { granularity: "sentence" });
  const sentence = sentences.segment(full).containing(at);
  return (sentence && around(sentence.segment, at - sentence.index)) || whole;
}

// Describe the element under the pointer (or clicked) from the DOM alone;
// caret, when known, is the character under the pointer.
export function describeNode(node: Node | null, caret: Caret | null = null): ReviewTarget | null {
  if (!node) return null;
  const labels = breadcrumb(node);
  const control = composedClosest(node, "button,a,select,[role=option],[role=treeitem]");
  if (control) return { where: joinWhere(labels, `"${elementLabel(control)}"`) };
  // Text outside any block element still says what the pointer is on.
  const block =
    composedClosest(node, BLOCK_SELECTOR) ??
    (caret && composedContains(node, caret.node) ? caret.node.parentElement : null);
  if (!labels.length && !block) return null;
  const text = block ? pointedText(block, caret) : "";
  return text && text !== labels.at(-1)
    ? { where: joinWhere(labels), text }
    : { where: joinWhere(labels) };
}

// Drill through open shadow roots to the innermost element at a point.
export function elementAt(x: number, y: number): Element | null {
  let element = document.elementFromPoint(x, y);
  while (element?.shadowRoot) {
    const inner = element.shadowRoot.elementFromPoint(x, y);
    if (!inner || inner === element) break;
    element = inner;
  }
  return element;
}

// The DOM selection within root, or null when nothing (non-whitespace) is
// selected there.
export function describeSelection(root: Element): ReviewTarget | null {
  const selection = window.getSelection();
  const start = selection?.anchorNode ?? null;
  const text = selection?.toString() ?? "";
  if (!text.trim() || !root.contains(start)) return null;
  return { where: joinWhere(breadcrumb(start)), text: clipSelection(text) };
}

function openDialogs(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"], dialog[open]'))
    .filter((dialog) => dialog.getClientRects().length > 0)
    .map(
      (dialog) =>
        collapse(
          dialog.getAttribute("aria-label") ||
            dialog.querySelector("h1,h2,h3,[class*=header],[class*=title]")?.textContent,
          80,
        ) || "dialog",
    );
}

const targetKey = (target: ReviewTarget | null) =>
  target ? `${target.where}\u0000${target.text ?? ""}` : "";

export interface ReviewCapture {
  // Snapshots on-screen/selection/view immediately and ends capture.
  stop(): void;
  note(event: Omit<ReviewEvent, "ms">): void;
}

export function startReviewCapture(options: {
  root: HTMLElement;
  context: ReviewContext;
  t0: number;
  emit: (event: ReviewEvent) => void;
  onState: (state: ReviewCaptureState) => void;
}): ReviewCapture {
  const { root, context, t0, emit } = options;
  const state: ReviewCaptureState = {
    pointer: "",
    pointerText: "",
    selection: "",
    screen: "",
    view: "",
  };
  let viewKey = "";
  let screenKey = "";
  let pointerKey = "";
  let selectionKey = "";
  let dialogsKey = "[]";
  let pointerPoint: { x: number; y: number } | null = null;
  let pointerFrame: number | null = null;
  let screenTimer: number | null = null;
  let selectionTimer: number | null = null;
  let clickTimer: number | null = null;
  let stopped = false;
  // The state when recording starts is the state at audio t=0.
  let initial = true;
  let pointerDown: { x: number; y: number } | null = null;
  let lastClick = { key: "", ms: -Infinity };

  const now = () => (initial ? 0 : Math.max(0, Math.round(performance.now() - t0)));
  const record = (event: Omit<ReviewEvent, "ms">) => {
    if (!stopped) emit({ ms: now(), ...event } as ReviewEvent);
  };
  const publish = () => {
    if (!stopped) options.onState({ ...state });
  };

  function checkView() {
    const view = context.view();
    const key = JSON.stringify(view);
    if (key === viewKey) return;
    viewKey = key;
    state.view = view.where;
    record({ type: "view", ...view });
    publish();
  }

  function checkScreen() {
    const items = context.onScreen();
    const key = JSON.stringify(items);
    if (key === screenKey) return;
    screenKey = key;
    state.screen = items[0] ?? "";
    publish();
    record({ type: "on_screen", items });
  }

  function checkPointer() {
    pointerFrame = null;
    if (!pointerPoint) return;
    const { x, y } = pointerPoint;
    const element = elementAt(x, y);
    if (
      !element ||
      !composedContains(root, element) ||
      composedClosest(element, "[data-review-ignore]")
    ) {
      onPointerLeave();
      return;
    }
    const provided = context.pointAt(x, y, element);
    const target = provided === undefined ? describeNode(element, caretAt(x, y)) : provided;
    const key = targetKey(target);
    if (key === pointerKey) return;
    pointerKey = key;
    state.pointer = target?.where ?? "";
    state.pointerText = target?.text ?? "";
    record(target ? { type: "pointer", ...target } : { type: "pointer_leave" });
    publish();
  }

  function checkSelection() {
    selectionTimer = null;
    const provided = context.selection();
    const target = provided === undefined ? describeSelection(root) : provided;
    const key = targetKey(target);
    if (key === selectionKey) return;
    selectionKey = key;
    state.selection = target?.where ?? "";
    record(target ? { type: "selection", ...target } : { type: "selection", text: "" });
    publish();
  }

  function checkDialogs() {
    const dialogs = openDialogs();
    const key = JSON.stringify(dialogs);
    if (key === dialogsKey) return;
    const previous = JSON.parse(dialogsKey) as string[];
    for (const label of dialogs)
      if (!previous.includes(label)) record({ type: "dialog", where: label, open: true });
    for (const label of previous)
      if (!dialogs.includes(label)) record({ type: "dialog", where: label, open: false });
    dialogsKey = key;
  }

  const schedulePointer = () => {
    if (pointerFrame === null) pointerFrame = requestAnimationFrame(checkPointer);
  };
  const scheduleScreen = () => {
    if (screenTimer !== null) clearTimeout(screenTimer);
    screenTimer = window.setTimeout(() => {
      screenTimer = null;
      checkScreen();
    }, 250);
  };
  const scheduleSelection = () => {
    if (selectionTimer !== null) clearTimeout(selectionTimer);
    selectionTimer = window.setTimeout(checkSelection, 300);
  };

  const ignored = (event: Event) =>
    event
      .composedPath()
      .some((node) => node instanceof HTMLElement && node.hasAttribute("data-review-ignore"));
  const onPointerMove = (event: PointerEvent) => {
    pointerPoint = { x: event.clientX, y: event.clientY };
    schedulePointer();
  };
  const onPointerDown = (event: PointerEvent) => {
    pointerDown = { x: event.clientX, y: event.clientY };
    onPointerMove(event);
  };
  function onPointerLeave() {
    if (!pointerKey) return;
    record({ type: "pointer_leave" });
    pointerKey = "";
    state.pointer = "";
    state.pointerText = "";
    publish();
  }
  const onRootLeave = () => {
    pointerPoint = null;
    onPointerLeave();
  };
  const onClick = (event: MouseEvent) => {
    const element = event.composedPath()[0];
    // A drag ends in a click on the common ancestor; the selection says more.
    const dragged =
      pointerDown && Math.hypot(event.clientX - pointerDown.x, event.clientY - pointerDown.y) > 5;
    pointerDown = null;
    if (!(element instanceof Element) || dragged || ignored(event)) return;
    const provided = context.pointAt(event.clientX, event.clientY, element);
    const target =
      provided === undefined
        ? describeNode(element, caretAt(event.clientX, event.clientY))
        : provided;
    const key = targetKey(target);
    const ms = now();
    // The second click of a double click adds nothing.
    if (target && !(key === lastClick.key && ms - lastClick.ms < 500)) {
      record({ type: "click", where: target.where, text: target.text });
    }
    lastClick = { key, ms };
    // Clicks usually change the view; check once the UI has reacted.
    if (clickTimer !== null) clearTimeout(clickTimer);
    clickTimer = window.setTimeout(() => {
      clickTimer = null;
      checkView();
      scheduleScreen();
    }, 50);
  };
  const onScroll = () => {
    scheduleScreen();
    schedulePointer();
  };
  const onVisibility = () =>
    record({ type: "page", state: document.hidden ? "hidden" : "visible" });
  const onBlur = () => record({ type: "page", state: "blur" });
  const onFocus = () => record({ type: "page", state: "focus" });

  root.addEventListener("pointermove", onPointerMove, { passive: true });
  root.addEventListener("pointerdown", onPointerDown, { passive: true });
  root.addEventListener("pointerleave", onRootLeave);
  root.addEventListener("click", onClick, true);
  root.addEventListener("scroll", onScroll, { capture: true, passive: true });
  document.addEventListener("selectionchange", scheduleSelection);
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("resize", scheduleScreen);
  window.addEventListener("blur", onBlur);
  window.addEventListener("focus", onFocus);
  // Backstop for changes without DOM events: Monaco selections and scrolls,
  // keyboard navigation, dialogs.
  const poll = window.setInterval(() => {
    checkView();
    checkScreen();
    checkSelection();
    checkDialogs();
    schedulePointer();
  }, 500);

  function detach() {
    stopped = true;
    root.removeEventListener("pointermove", onPointerMove);
    root.removeEventListener("pointerdown", onPointerDown);
    root.removeEventListener("pointerleave", onRootLeave);
    root.removeEventListener("click", onClick, true);
    root.removeEventListener("scroll", onScroll, true);
    document.removeEventListener("selectionchange", scheduleSelection);
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("resize", scheduleScreen);
    window.removeEventListener("blur", onBlur);
    window.removeEventListener("focus", onFocus);
    window.clearInterval(poll);
    if (pointerFrame !== null) cancelAnimationFrame(pointerFrame);
    if (screenTimer !== null) clearTimeout(screenTimer);
    if (selectionTimer !== null) clearTimeout(selectionTimer);
    if (clickTimer !== null) clearTimeout(clickTimer);
  }

  try {
    checkView();
    checkScreen();
    checkSelection();
    checkDialogs();
  } catch (err) {
    detach();
    throw err;
  }
  initial = false;

  return {
    note: record,
    // Always detaches; a failing final snapshot is rethrown afterwards.
    stop() {
      if (stopped) return;
      try {
        checkView();
        checkScreen();
        checkSelection();
        record({ type: "stop" });
      } finally {
        detach();
      }
    },
  };
}
