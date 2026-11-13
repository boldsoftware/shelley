// The bookmarklet itself: runs in the page it was clicked on. It opens a new
// Shelley chat about the page in a popup, shows a small toolbar to pick
// elements into the chat and start a recording there, and, while the Shelley
// window records, reports where the user points, clicks, and selects. It
// sends only data and requests; it accepts nothing but "hello" and "trace"
// from the Shelley window it opened.
import {
  caretAt,
  clipSelection,
  collapse,
  describeNode,
  describeSelection,
  startReviewCapture,
  type ReviewCapture,
  type ReviewContext,
  type ReviewEvent,
} from "../vue/components/reviewCapture";
import { FRAGMENT, HTML_LIMIT, TAG, type PageContext, type PageMessage } from "./protocol";

// Bound by the bookmarklet's wrapper; see href.ts.
declare const __shelleyBookmarklet: { origin: string };

interface Toolbar {
  open(): void;
}
declare global {
  interface Window {
    __shelleyBookmarkletPage?: Toolbar;
  }
}

const ANCHOR =
  "a,button,select,input,textarea,label,img,video,p,li,h1,h2,h3,h4,h5,h6,pre,blockquote,td,th,figure,summary";

// A readable CSS path to element: anchored at the nearest id, at most six
// levels, two classes per level.
function cssPath(element: Element): string {
  const parts: string[] = [];
  for (
    let current: Element | null = element;
    current && current !== document.documentElement && parts.length < 6;
    current = current.parentElement
  ) {
    if (current.id) {
      parts.unshift(`#${CSS.escape(current.id)}`);
      break;
    }
    const node = current;
    let part =
      node.localName +
      Array.from(node.classList)
        .slice(0, 2)
        .map((name) => `.${CSS.escape(name)}`)
        .join("");
    const siblings = Array.from(node.parentElement?.children ?? []).filter(
      (sibling) => sibling.localName === node.localName,
    );
    if (siblings.length > 1) part += `:nth-of-type(${siblings.indexOf(node) + 1})`;
    parts.unshift(part);
  }
  return parts.join(" > ") || element.localName;
}

function describe(element: Element, x: number, y: number) {
  const anchor = element.closest(ANCHOR) ?? element;
  const target = describeNode(element, caretAt(x, y));
  const label = target?.where ?? "";
  return {
    where: label ? `${cssPath(anchor)} › ${label}` : cssPath(anchor),
    ...(target?.text ? { text: target.text } : {}),
  };
}

const pageContext: ReviewContext = {
  view: () => ({ where: `${collapse(document.title, 120)} — ${location.href}`, mode: "page" }),
  pointAt: (x, y, element) => describe(element, x, y),
  selection: () => describeSelection(document.documentElement),
  onScreen: () =>
    Array.from(document.querySelectorAll("h1,h2,h3"))
      .filter((heading) => {
        const rect = heading.getBoundingClientRect();
        return rect.bottom > 0 && rect.top < innerHeight && rect.width > 0;
      })
      .map((heading) => collapse(heading.textContent, 80))
      .filter(Boolean)
      .slice(0, 6),
};

const STYLE = `
:host { all: initial; position: fixed; right: 16px; bottom: 16px; z-index: 2147483647; }
.bar { display: flex; align-items: center; gap: 6px; padding: 6px 8px; border-radius: 10px;
  background: #1f2328; color: #f0f3f6; font: 13px/1.2 system-ui, sans-serif;
  box-shadow: 0 4px 16px rgb(0 0 0 / 0.3); }
.status { padding: 0 4px; color: #9198a1; white-space: nowrap; }
.status[data-state="linked"] { color: #3fb950; }
.status[data-state="tracing"] { color: #f85149; }
button { all: unset; cursor: pointer; padding: 4px 8px; border-radius: 6px; background: #30363d; }
button:hover { background: #484f58; }
button:disabled { cursor: default; opacity: 0.5; }
button[aria-pressed="true"] { background: #1f6feb; }
.outline { position: fixed; pointer-events: none; border: 2px solid #1f6feb;
  background: rgb(31 111 235 / 0.12); border-radius: 2px; display: none; }
`;

function start(settings: typeof __shelleyBookmarklet): Toolbar {
  const origin = new URL(settings.origin).origin;
  let win: Window | null = null;
  let linked = false;
  let capture: ReviewCapture | null = null;
  let picking = false;

  const host = document.createElement("shelley-bookmarklet");
  host.setAttribute("data-review-ignore", "");
  const shadow = host.attachShadow({ mode: "open" });
  const sheet = new CSSStyleSheet();
  sheet.replaceSync(STYLE);
  shadow.adoptedStyleSheets = [sheet];
  const bar = document.createElement("div");
  bar.className = "bar";
  const status = document.createElement("span");
  status.className = "status";
  const button = (label: string, title: string, onClick: () => void) => {
    const element = document.createElement("button");
    element.textContent = label;
    element.title = title;
    element.addEventListener("click", onClick);
    return element;
  };
  // Shelley starts it once the user has let this site start recordings.
  const record = button("Record", "Record your voice and what you do here; stop in Shelley", () => {
    send({ tag: TAG, type: "record" });
    // Browsers may hold the microphone until Shelley has focus.
    win?.focus();
  });
  const pick = button("Pick", "Pick an element into the chat", () => setPicking(!picking));
  const focus = button("Shelley", "Show the Shelley chat", () => open());
  const close = button("×", "Close", () => remove());
  close.setAttribute("aria-label", "Close");
  const outline = document.createElement("div");
  outline.className = "outline";
  bar.append(status, record, pick, focus, close);
  shadow.append(bar, outline);
  document.documentElement.append(host);

  function render() {
    if (capture && (!win || win.closed)) stopTrace();
    const state =
      !win || win.closed ? "closed" : capture ? "tracing" : linked ? "linked" : "opening";
    status.dataset.state = state;
    status.textContent = {
      closed: "Not linked",
      opening: "Opening…",
      linked: "Linked",
      tracing: "Recording page",
    }[state];
    record.disabled = state !== "linked";
    pick.disabled = state === "closed" || state === "opening";
    pick.setAttribute("aria-pressed", String(picking));
    if (pick.disabled && picking) setPicking(false);
  }

  function send(message: PageMessage) {
    if (win && !win.closed) win.postMessage(message, origin);
  }

  function open() {
    if (win && !win.closed) {
      win.focus();
      return;
    }
    const page: PageContext = {
      url: location.href,
      title: document.title,
      selection: clipSelection(getSelection()?.toString() ?? ""),
    };
    const url = `${origin}/new#${FRAGMENT}${encodeURIComponent(JSON.stringify(page))}`;
    stopTrace();
    linked = false;
    win = window.open(url, "_blank", "popup,width=480,height=760");
    render();
  }

  function stopTrace() {
    capture?.stop();
    capture = null;
  }

  function onMessage(event: MessageEvent) {
    if (!win || event.source !== win || event.origin !== origin) return;
    const data: unknown = event.data;
    if (typeof data !== "object" || data === null || (data as { tag?: unknown }).tag !== TAG) {
      return;
    }
    const message = data as { type?: unknown; on?: unknown; id?: unknown };
    // A trace ends with its recording; a Shelley window that reloaded or
    // started another one won't say so.
    stopTrace();
    if (message.type === "hello") {
      linked = true;
      send({ tag: TAG, type: "hi" });
    } else if (message.type === "trace" && message.on === true && typeof message.id === "number") {
      const t0 = performance.now();
      send({ tag: TAG, type: "trace", id: message.id, epoch: Date.now() });
      capture = startReviewCapture({
        root: document.documentElement,
        context: pageContext,
        t0,
        emit: (event: ReviewEvent) => {
          if (event.type !== "stop") send({ tag: TAG, type: "event", event });
        },
        onState: () => {},
      });
    }
    render();
  }

  // Picking: the next click anywhere on the page sends that element.
  const outside = (event: Event) => !event.composedPath().includes(host);
  function onPickMove(event: PointerEvent) {
    if (!outside(event) || !(event.target instanceof Element)) {
      outline.style.display = "none";
      return;
    }
    const rect = event.target.getBoundingClientRect();
    Object.assign(outline.style, {
      display: "block",
      left: `${rect.left}px`,
      top: `${rect.top}px`,
      width: `${rect.width}px`,
      height: `${rect.height}px`,
    });
  }
  function onPickClick(event: MouseEvent) {
    if (!outside(event) || !(event.target instanceof Element)) return;
    event.preventDefault();
    event.stopPropagation();
    const element = event.target;
    send({
      tag: TAG,
      type: "pick",
      pick: {
        url: location.href,
        selector: cssPath(element),
        html: element.outerHTML.slice(0, HTML_LIMIT),
      },
    });
    setPicking(false);
  }
  function onPickKey(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    setPicking(false);
  }
  function setPicking(on: boolean) {
    picking = on;
    const listeners = [
      ["pointermove", onPickMove],
      ["click", onPickClick],
      ["keydown", onPickKey],
    ] as const;
    for (const [type, listener] of listeners) {
      if (on) window.addEventListener(type, listener as EventListener, true);
      else window.removeEventListener(type, listener as EventListener, true);
    }
    if (!on) outline.style.display = "none";
    render();
  }

  const poll = setInterval(render, 1000);
  window.addEventListener("message", onMessage);

  function remove() {
    setPicking(false);
    stopTrace();
    clearInterval(poll);
    window.removeEventListener("message", onMessage);
    host.remove();
    delete window.__shelleyBookmarkletPage;
  }

  open();
  return { open };
}

if (window.__shelleyBookmarkletPage) window.__shelleyBookmarkletPage.open();
else window.__shelleyBookmarkletPage = start(__shelleyBookmarklet);
