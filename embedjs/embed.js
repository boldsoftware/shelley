// Docks Shelley in a pane on the right of the page that includes it:
//
//   <script src="https://shelley.example.com/embed.js" defer></script>
//
// Shelley must let the page's site frame it: "Edit with Shelley" in its
// command palette lists the sites that may. The button at the top right
// opens the pane. Its x hides the button in this browser until the page's
// URL ends in #shelley (or #shelley-embed, if the page has its own
// #shelley), which also opens the pane.
//
// Load it as a classic script, once. It builds its DOM and styles without
// markup or <style>, so it runs under Trusted Types and a style-src without
// 'unsafe-inline'. In a frame, as the page is in Shelley's own browser, it
// does nothing.
(() => {
  if (window !== top || document.querySelector("shelley-embed")) return;
  const origin = new URL(document.currentScript.src).origin;
  const openKey = "shelley-embed-open";
  const hiddenKey = "shelley-embed-hidden";
  const docked = "data-shelley-embed-docked";
  const width = "min(480px, 100vw)";
  const font = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

  // Storage may be off, as in some private windows.
  const stored = (f) => {
    try {
      return f();
    } catch {}
  };
  const sheet = (css) => {
    const s = new CSSStyleSheet();
    s.replaceSync(css);
    return s;
  };
  const el = (tag, attrs = {}, ...children) => {
    const svg = tag === "svg" || tag === "path";
    const e = svg
      ? document.createElementNS("http://www.w3.org/2000/svg", tag)
      : document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    e.append(...children);
    return e;
  };

  // Wide pages move over for the open pane; narrow ones and print don't.
  document.adoptedStyleSheets = [
    ...document.adoptedStyleSheets,
    sheet(
      `@media screen and (min-width: 900px) { html[${docked}] { margin-right: ${width} !important; } }`,
    ),
  ];

  const host = el("shelley-embed");
  const root = host.attachShadow({ mode: "open" });
  root.adoptedStyleSheets = [
    sheet(`
    :host { all: initial; color-scheme: inherit; }
    .pane { position: fixed; top: 0; right: 0; bottom: 0; width: ${width}; z-index: 2147483646;
      border-left: 1px solid #8884; background: Canvas; box-shadow: -4px 0 16px #0002; }
    .pane[hidden], .launcher[hidden] { display: none; }
    iframe { border: 0; width: 100%; height: 100%; display: block; }
    .launcher { --pen: #1d3fd8;
      position: fixed; top: 12px; right: 12px; z-index: 2147483647; display: flex; align-items: center;
      height: 34px; box-sizing: border-box; border: 1px solid #8885; border-radius: 17px;
      background: Canvas; color: CanvasText; box-shadow: 0 2px 10px #0002;
      font: 600 13px/1 ${font}; white-space: nowrap; }
    button { all: unset; box-sizing: border-box; display: flex; align-items: center; cursor: pointer; }
    button:focus-visible { outline: 2px solid var(--pen); outline-offset: 2px; }
    .open { height: 100%; gap: 8px; padding: 0 8px 0 13px; border-radius: 17px; }
    .open:hover { color: var(--pen); }
    .mark { width: 10px; height: 20px; fill: none; stroke: var(--pen); stroke-width: 34;
      stroke-linecap: round; stroke-linejoin: round; overflow: visible; }
    .hide { justify-content: center; width: 22px; height: 22px; margin-right: 5px; border-radius: 50%;
      opacity: 0.55; }
    .hide:hover { opacity: 1; background: #8883; }
    .hide svg { width: 8px; height: 8px; stroke: currentColor; stroke-width: 1.6; stroke-linecap: round; }
    .launcher[data-open] { right: calc(${width} + 12px); }
    .launcher[data-open] .open { padding-right: 13px; }
    .launcher[data-open] .hide { display: none; }
    .toast { position: fixed; top: 12px; right: 12px; z-index: 2147483647; max-width: calc(100vw - 48px);
      padding: 9px 12px; border-radius: 8px; background: CanvasText; color: Canvas;
      box-shadow: 0 2px 10px #0003; font: 13px/1.4 ${font}; }
    .toast:empty { padding: 0; box-shadow: none; }
    @supports (color: light-dark(#000, #fff)) {
      .launcher { --pen: light-dark(#1d3fd8, #8aa2ff); }
    }
    @media not all and (min-width: 900px) {
      .launcher[data-open] { right: 12px; }
      .pane { top: 58px; border-top: 1px solid #8884; border-top-left-radius: 12px; overflow: hidden; }
    }
    @media print { .pane, .launcher, .toast { display: none; } }`),
  ];

  const pane = el("div", { class: "pane", id: "pane", hidden: "" });
  const label = el("span", {}, "Edit with Shelley");
  const openButton = el(
    "button",
    { type: "button", class: "open", "aria-controls": "pane", "aria-expanded": "false" },
    el(
      "svg",
      { class: "mark", viewBox: "-20 -20 240 480", "aria-hidden": "true" },
      el("path", {
        d: "M0 70V170M100 70V170M200 70V170M0 270V370M100 270V370M200 270V370M0 170L100 270M100 170L200 270M200 170L150 220M0 270L50 220M0 70L100 0L200 70M0 370L100 440L200 370",
      }),
    ),
    label,
  );
  const hideButton = el(
    "button",
    { type: "button", class: "hide", "aria-label": "Hide this button", title: "Hide this button" },
    el("svg", { viewBox: "0 0 8 8", "aria-hidden": "true" }, el("path", { d: "M1 1L7 7M7 1L1 7" })),
  );
  const launcher = el("div", { class: "launcher", hidden: "" }, openButton, hideButton);
  const toast = el("div", { class: "toast", role: "status" });
  root.append(pane, launcher, toast);

  const show = (open) => {
    if (open && !pane.firstChild) {
      pane.append(
        el("iframe", {
          src: origin + "/",
          title: "Shelley",
          allow: "clipboard-read; clipboard-write; microphone; display-capture",
        }),
      );
    }
    pane.hidden = !open;
    launcher.toggleAttribute("data-open", open);
    document.documentElement.toggleAttribute(docked, open);
    openButton.setAttribute("aria-expanded", open);
    label.textContent = open ? "Close" : "Edit with Shelley";
    stored(() => localStorage.setItem(openKey, open ? "1" : ""));
  };
  const hide = (hidden) => {
    launcher.hidden = hidden;
    stored(() => localStorage.setItem(hiddenKey, hidden ? "1" : ""));
  };
  let toastTimer = 0;
  const say = (text) => {
    clearTimeout(toastTimer);
    toast.textContent = text;
    if (text) toastTimer = setTimeout(() => (toast.textContent = ""), 6000);
  };

  openButton.addEventListener("click", () => show(pane.hidden));
  // #shelley brings the button back and opens the pane, unless the page
  // has its own #shelley; #shelley-embed always does. The fragment goes
  // from the URL, so a reload keeps whatever the user does next.
  const trigger = () =>
    document.getElementById("shelley") || document.querySelector('a[name="shelley"]')
      ? "#shelley-embed"
      : "#shelley";
  hideButton.addEventListener("click", () => {
    hide(true);
    say(`Hidden. Add ${trigger()} to the URL to show it again.`);
  });
  const reveal = () => {
    if (location.hash !== "#shelley-embed" && location.hash !== trigger()) return false;
    const url = new URL(location.href);
    url.hash = "";
    history.replaceState(history.state, "", url);
    say("");
    hide(false);
    show(true);
    return true;
  };
  addEventListener("hashchange", reveal);

  // Outside the body, which some pages swap out as they navigate.
  document.documentElement.append(host);
  // Without defer, the script may run before the page's own #shelley.
  const start = () => {
    if (!reveal()) {
      launcher.hidden = stored(() => localStorage.getItem(hiddenKey)) === "1";
      show(!launcher.hidden && stored(() => localStorage.getItem(openKey)) === "1");
    }
  };
  if (document.readyState === "loading") addEventListener("DOMContentLoaded", start);
  else start();
})();
