// /debug/llm/: browse the recent LLM HTTP exchanges Shelley keeps in memory.
"use strict";

const $ = (sel) => document.querySelector(sel);

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid != null && kid !== false && kid !== "") el.append(kid);
  }
  return el;
}

const state = {
  list: [],
  listText: "",
  selected: null,
  detail: null,
  seq: 0, // latest detail load; older results are dropped
  loading: false, // a detail load is in flight; tick doesn't start another
  live: false, // keep polling the selection: not yet done, loaded, or evicted
  // Nodes (by data-path) the user opened or closed in the current selection.
  // Re-renders reapply them.
  userOpen: new Map(),
  tab: "request",
  mode: "tree",
};

// ---- formatting ----

function fmtBytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

function fmtMs(ms) {
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)} s`;
}

function fmtTime(iso) {
  return new Date(iso).toLocaleTimeString([], { hour12: false });
}

function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function durationText(e) {
  return fmtMs(e.duration_ms) + (e.done ? "" : "…");
}

function sizesText(e) {
  return `${fmtBytes(e.request_bytes)} → ${fmtBytes(e.response_bytes)}`;
}

function statusInfo(e) {
  if (e.error) return { cls: "err", text: e.status ? String(e.status) : "ERR" };
  if (!e.status) return { cls: "pending", text: "…" };
  if (e.status >= 400) return { cls: "err", text: String(e.status) };
  return { cls: e.done ? "ok" : "pending", text: String(e.status) };
}

function parseJSON(text) {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

// ---- list ----

function filteredList() {
  const terms = $("#filter").value.toLowerCase().split(/\s+/).filter(Boolean);
  return state.list.filter((e) => {
    const hay = [
      e.model,
      e.provider,
      e.slug,
      e.conversation_id,
      e.request_id,
      e.status,
      e.url,
      e.error,
    ]
      .join(" ")
      .toLowerCase();
    return terms.every((t) => hay.includes(t));
  });
}

function renderList() {
  const rows = filteredList();
  $("#count").textContent =
    rows.length === state.list.length
      ? String(rows.length)
      : `${rows.length} of ${state.list.length}`;
  // Reuse unchanged rows so polling doesn't steal focus or swallow clicks.
  const list = $("#list");
  const old = new Map([...list.children].map((el) => [el.dataset.key, el]));
  const els = rows.map((e) => {
    const key = rowKey(e);
    const el = old.get(key);
    if (!el) return listRow(e, key);
    el.querySelector(".dur").textContent = durationText(e);
    el.querySelector(".sizes").textContent = sizesText(e);
    return el;
  });
  if (!els.length) {
    const text = state.list.length ? "No matches." : "No LLM requests yet.";
    els.push(h("p", { class: "none muted", "data-key": text }, text));
  }
  const keep = new Set(els);
  for (const el of [...list.children]) if (!keep.has(el)) el.remove();
  els.forEach((el, i) => {
    if (list.children[i] !== el) list.insertBefore(el, list.children[i] ?? null);
  });
}

// rowKey covers everything a row shows except the in-flight duration and
// response size, which tick on every poll and are updated in place.
function rowKey(e) {
  return JSON.stringify([e.id === state.selected, { ...e, duration_ms: 0, response_bytes: 0 }]);
}

function listRow(e, key) {
  const st = statusInfo(e);
  return h(
    "a",
    {
      class: `row${e.id === state.selected ? " selected" : ""}`,
      href: `#${e.id}`,
      "data-key": key,
    },
    h(
      "div",
      { class: "row-top" },
      h("span", { class: `pill ${st.cls}` }, st.text),
      h("span", { class: "model" }, e.model || e.provider || "unknown model"),
      h("span", { class: "dur" }, durationText(e)),
    ),
    h(
      "div",
      { class: "row-bottom" },
      h("span", { class: "conv" }, e.slug || e.conversation_id || "no conversation"),
      h("span", { class: "sizes" }, sizesText(e)),
      h("span", {}, fmtTime(e.start)),
    ),
  );
}

async function refreshList() {
  const res = await fetch("/api/debug/llm/exchanges");
  if (!res.ok) throw new Error(`list: ${res.status}`);
  const text = await res.text();
  if (text === state.listText) return;
  state.listText = text;
  state.list = JSON.parse(text);
  renderList();
}

// ---- selection ----

function selectFromHash() {
  const id = parseInt(location.hash.slice(1), 10);
  select(Number.isFinite(id) ? id : null);
}

async function select(id) {
  if (id !== state.selected) {
    state.selected = id;
    state.detail = null;
    state.userOpen = new Map();
    if (id != null) renderMessage("Loading…");
  }
  state.live = id != null;
  document.body.classList.toggle("show-detail", id != null);
  renderList();
  if (id == null) {
    state.seq++;
    renderMessage("Select a request to inspect it.");
    return;
  }
  await loadDetail(true);
}

// loadDetail fetches the selected exchange. On a transient failure,
// state.live stays set so the next tick retries.
async function loadDetail(full) {
  const seq = ++state.seq;
  state.loading = true;
  try {
    const res = await fetch(`/api/debug/llm/exchanges/${state.selected}`);
    if (seq !== state.seq) return;
    if (res.status === 404) {
      state.live = false;
      state.detail = null;
      renderMessage("This request has been evicted from memory.");
      return;
    }
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const d = await res.json();
    if (seq !== state.seq) return;
    const prev = state.detail;
    state.detail = d;
    state.live = !d.done;
    renderDetail(full, prev);
  } catch (err) {
    if (seq !== state.seq) return;
    // Keep showing what we have, if anything; tick retries.
    if (state.detail) $("#offline").hidden = false;
    else renderMessage(`Failed to load: ${err.message}`);
  } finally {
    if (seq === state.seq) state.loading = false;
  }
}

// ---- detail ----

function renderMessage(text) {
  $("#detail").replaceChildren(backButton(), h("div", { class: "empty muted" }, text));
}

function backButton() {
  return h(
    "button",
    {
      class: "btn back",
      onclick: () => {
        history.pushState("", "", location.pathname);
        select(null);
      },
    },
    "← Requests",
  );
}

// renderDetail draws the selected exchange. On a live update (full unset)
// the content is redrawn only if the response changed since prev.
function renderDetail(full, prev) {
  const d = state.detail;
  const content = $("#content");
  if (full || !content) {
    $("#detail").replaceChildren(
      backButton(),
      detailHead(d),
      tabBar(d),
      h("div", { class: "content", id: "content" }),
    );
    renderContent();
    return;
  }
  $(".detail-head").replaceWith(detailHead(d));
  $(".bar").replaceWith(tabBar(d));
  const changed = ["response_bytes", "status", "done", "error"].some((k) => prev[k] !== d[k]);
  if (state.tab !== "request" && changed) {
    const top = content.scrollTop;
    renderContent();
    content.scrollTop = top;
  }
}

function fact(label, ...value) {
  return h("div", {}, h("dt", {}, label), h("dd", {}, ...value));
}

function detailHead(d) {
  const st = statusInfo(d);
  const slug = state.list.find((e) => e.id === d.id)?.slug;
  const conv = d.conversation_id
    ? [
        h(
          "a",
          {
            href: `/c/${encodeURIComponent(d.conversation_id)}`,
            target: "_blank",
          },
          slug || d.conversation_id,
        ),
        " ",
        h(
          "button",
          {
            class: "link",
            title: "Show only this conversation",
            onclick: () => setFilter(d.conversation_id),
          },
          "filter",
        ),
      ]
    : [h("span", { class: "muted" }, "none")];
  return h(
    "header",
    { class: "detail-head" },
    h(
      "div",
      { class: "title" },
      h("span", { class: `pill ${st.cls}` }, st.text),
      h("span", {}, d.model || "unknown model"),
      d.provider && h("span", { class: "muted provider" }, d.provider),
    ),
    h("div", { class: "url" }, `${d.method} ${d.url}`),
    h(
      "dl",
      { class: "facts" },
      fact("Conversation", ...conv),
      fact("Started", fmtTime(d.start)),
      fact("First byte", d.ttfb_ms ? fmtMs(d.ttfb_ms) : "—"),
      fact(d.done ? "Total" : "Elapsed", durationText(d)),
      ...timingFacts(d),
      fact("Shelley request id", h("code", {}, d.request_id || "—")),
    ),
    d.error && h("div", { class: "errbox" }, d.error),
  );
}

function tabBar(d) {
  const tab = (id, label, meta) =>
    h(
      "button",
      {
        class: `tab${state.tab === id ? " active" : ""}`,
        onclick: () => {
          state.tab = id;
          renderDetail(true);
        },
      },
      label,
      meta && h("span", { class: "muted" }, ` ${meta}`),
    );
  const truncated = (b) => (b ? " (truncated)" : "");
  return h(
    "div",
    { class: "bar" },
    tab("request", "Request", fmtBytes(d.request_bytes) + truncated(d.request_truncated)),
    tab("response", "Response", fmtBytes(d.response_bytes) + truncated(d.response_truncated)),
    tab("headers", "Headers"),
    state.tab !== "headers" &&
      h(
        "div",
        { class: "tools" },
        h(
          "div",
          { class: "seg" },
          ["tree", "raw"].map((m) =>
            h(
              "button",
              {
                class: `btn${state.mode === m ? " on" : ""}`,
                onclick: () => {
                  state.mode = m;
                  renderDetail(true);
                },
              },
              m === "tree" ? "Tree" : "Raw",
            ),
          ),
        ),
        h("button", { class: "btn", onclick: (ev) => copyBody(ev.currentTarget) }, "Copy"),
      ),
  );
}

function currentBody() {
  return state.tab === "request" ? state.detail.request_body : state.detail.response_body;
}

// prettyText returns body pretty-printed when it is JSON.
function prettyText(text) {
  const v = parseJSON(text);
  return v === undefined ? text : JSON.stringify(v, null, 2);
}

async function copyBody(btn) {
  await navigator.clipboard.writeText(prettyText(currentBody()));
  btn.textContent = "Copied";
  setTimeout(() => (btn.textContent = "Copy"), 1200);
}

function renderContent() {
  const d = state.detail;
  const box = $("#content");
  if (state.tab === "headers") {
    box.replaceChildren(
      headerTable("Request headers", d.request_header),
      headerTable("Response headers", d.response_header),
    );
    return;
  }
  const text = currentBody();
  if (!text) {
    const msg = d.done || state.tab === "request" ? "(empty)" : "Waiting for response…";
    box.replaceChildren(h("p", { class: "muted" }, msg));
    return;
  }
  const ct = d.response_header?.["Content-Type"]?.[0] || "";
  const v = parseJSON(text);
  if (state.mode === "raw") {
    box.replaceChildren(
      h("pre", { class: "raw" }, v === undefined ? text : JSON.stringify(v, null, 2)),
    );
  } else if (state.tab === "response" && ct.startsWith("text/event-stream")) {
    box.replaceChildren(sseView(text));
  } else if (v === undefined) {
    box.replaceChildren(h("pre", { class: "raw" }, text));
  } else {
    box.replaceChildren(h("div", { class: "tree" }, node(null, v, 0, null, state.tab)));
  }
  restore(box);
}

function headerTable(title, hdrs) {
  const rows = Object.keys(hdrs || {})
    .sort()
    .flatMap((k) => hdrs[k].map((v) => h("tr", {}, h("th", {}, k), h("td", {}, v))));
  return h(
    "section",
    { class: "headers" },
    h("h3", {}, title),
    rows.length ? h("table", {}, h("tbody", {}, rows)) : h("p", { class: "muted" }, "None yet."),
  );
}

function setFilter(text) {
  $("#filter").value = text;
  renderList();
}

// ---- JSON tree ----

function keyLabel(key) {
  if (key == null) return null;
  if (key instanceof Node) return [key, h("span", { class: "punct" }, " ")];
  return [
    h("span", { class: typeof key === "number" ? "key idx" : "key" }, String(key)),
    h("span", { class: "punct" }, ": "),
  ];
}

function scalar(v) {
  if (v === null) return h("span", { class: "null" }, "null");
  const cls = typeof v === "number" ? "num" : typeof v === "boolean" ? "bool" : "str";
  return h("span", { class: cls }, typeof v === "string" ? JSON.stringify(v) : String(v));
}

// node renders one key/value pair. parent is the containing object, used to
// recognize base64 image data by its sibling media type. path identifies the
// node across re-renders so they keep what the user opened.
function node(key, value, depth, parent, path) {
  if (value !== null && typeof value === "object") {
    return container(key, value, {
      open: depth === 0 || undefined,
      root: depth === 0,
      depth,
      path,
    });
  }
  if (typeof value === "string") return stringNode(key, value, parent, depth, path);
  return h("div", { class: "line" }, keyLabel(key), scalar(value));
}

// lazyDetails returns a <details> whose children are built by fill on first
// open. Clicks (and Enter/Space) on its summary are recorded in userOpen.
function lazyDetails({ cls = "node", path, open }, summary, fill) {
  const det = h(
    "details",
    { class: cls, "data-path": path },
    h("summary", { onclick: () => state.userOpen.set(path, !det.open) }, summary),
  );
  det.populate = () => {
    det.populate = null;
    det.append(h("div", { class: "kids" }, fill()));
  };
  det.addEventListener("toggle", () => det.open && det.populate?.());
  if (open) {
    det.populate();
    det.open = true;
  }
  return det;
}

// restore reapplies the user's open/close choices after a re-render.
function restore(box) {
  const seen = new Set();
  for (let again = true; again;) {
    again = false;
    for (const el of box.querySelectorAll("details[data-path]")) {
      const path = el.dataset.path;
      const want = state.userOpen.get(path);
      if (seen.has(path)) continue;
      seen.add(path);
      if (want === undefined || want === el.open) continue;
      if (want) {
        el.populate?.();
        again = true; // populating added children to restore
      }
      el.open = want;
    }
  }
}

// container renders an object or array. Small ones start open; collapsed
// ones show a one-line preview.
function container(key, value, { open, root, depth, path, badge }) {
  const isArr = Array.isArray(value);
  const keys = isArr ? value.map((_, i) => i) : Object.keys(value);
  if (!keys.length) {
    return h(
      "div",
      { class: "line" },
      keyLabel(key),
      h("span", { class: "punct" }, isArr ? "[]" : "{}"),
    );
  }
  const json = JSON.stringify(value);
  return lazyDetails(
    {
      cls: `node${root ? " root" : ""}`,
      path,
      open: open ?? (depth < 8 && json.length <= 160),
    },
    [
      keyLabel(key),
      badge && h("span", { class: "badge" }, badge),
      h("span", { class: "count" }, plural(keys.length, isArr ? "item" : "key")),
      h("span", { class: "preview" }, json.length > 140 ? `${json.slice(0, 140)}…` : json),
    ],
    () => keys.map((k) => node(k, value[k], depth + 1, value, `${path}/${k}`)),
  );
}

function imageSrc(s, key, parent) {
  if (s.startsWith("data:image/")) return s;
  const mt = parent && (parent.media_type || parent.mime_type || parent.mimeType);
  if (key === "data" && typeof mt === "string" && mt.startsWith("image/"))
    return `data:${mt};base64,${s}`;
  return null;
}

function stringNode(key, s, parent, depth, path) {
  const img = imageSrc(s, key, parent);
  if (img) {
    return h(
      "div",
      { class: "line" },
      keyLabel(key),
      h("span", { class: "meta" }, `image · ${fmtBytes(s.length)}`),
      h("div", {}, h("img", { class: "thumb", src: img, loading: "lazy", alt: "" })),
    );
  }
  const t = s.trimStart();
  if (t[0] === "{" || t[0] === "[") {
    const v = parseJSON(s);
    if (v !== null && typeof v === "object")
      return container(key, v, { depth, path, badge: "JSON string" });
  }
  if (s.length <= 120 && !s.includes("\n"))
    return h("div", { class: "line" }, keyLabel(key), scalar(s));
  return longString(key, s, path, false);
}

// longString shows text as-is; long text collapses behind a preview.
function longString(key, s, path, open) {
  const lines = s.split("\n").length;
  const meta = h(
    "span",
    { class: "meta" },
    `${plural(lines, "line")} · ${s.length.toLocaleString()} chars`,
  );
  const block = () => h("div", { class: "strblock" }, s);
  if (lines <= 16 && s.length <= 2400)
    return h("div", { class: "line" }, keyLabel(key), meta, block());
  const preview = h("span", { class: "preview" }, JSON.stringify(s.slice(0, 140)));
  return lazyDetails({ path, open }, [keyLabel(key), meta, preview], block);
}

// ---- server-sent events ----

// parseSSE splits an event stream into events. Each event's end is the UTF-8
// byte offset just past it, for matching against response_timeline.
function parseSSE(text) {
  const events = [];
  const enc = new TextEncoder();
  const sep = /\r?\n\r?\n|$/g;
  let start = 0;
  let bytes = 0;
  while (start < text.length) {
    sep.lastIndex = start;
    const m = sep.exec(text);
    const block = text.slice(start, m.index);
    const stop = m.index + m[0].length;
    bytes += enc.encode(text.slice(start, stop)).length;
    start = stop;
    if (!block.trim()) continue;
    let name = "";
    const data = [];
    for (const line of block.split(/\r?\n/)) {
      if (line.startsWith(":")) continue;
      const i = line.indexOf(":");
      const field = i < 0 ? line : line.slice(0, i);
      const val = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
      if (field === "event") name = val;
      else if (field === "data") data.push(val);
    }
    const raw = data.join("\n");
    const json = parseJSON(raw);
    events.push({
      name: name || (typeof json?.type === "string" ? json.type : "") || json?.object || "data",
      raw,
      json,
      end: bytes,
    });
  }
  return events;
}

// outputTokens returns the output token count reported by a usage-bearing
// event, across Anthropic, OpenAI chat completions, and OpenAI Responses.
function outputTokens(j) {
  const u = j?.usage ?? j?.response?.usage ?? j?.message?.usage;
  const n = u?.output_tokens ?? u?.completion_tokens;
  return typeof n === "number" ? n : null;
}

// streamTiming derives TTFT, time between tokens (TBT), and output tokens
// per second (TPS) from a streamed response and its arrival timeline. Times
// are ms since the request started. Tokens are streamed text deltas. TPS is
// measured from the first response byte rather than the first delta because
// hidden reasoning is counted in output tokens but streams no deltas.
const timingCache = { key: "", value: null };
function streamTiming(d) {
  const tl = d.response_timeline;
  const ct = d.response_header?.["Content-Type"]?.[0] || "";
  if (!tl?.length || !ct.startsWith("text/event-stream")) return null;
  const key = `${d.id}:${d.response_bytes}:${d.done}`;
  if (timingCache.key === key) return timingCache.value;
  const times = [];
  let tokens = null;
  let i = 0;
  for (const ev of parseSSE(d.response_body || "")) {
    while (i < tl.length - 1 && tl[i][0] < ev.end) i++;
    if (tl[i][0] < ev.end) break; // not arrived yet (or past a truncation)
    if (deltaText(ev)) times.push(tl[i][1]);
    tokens = outputTokens(ev.json) ?? tokens;
  }
  let value = null;
  if (times.length) {
    const first = times[0];
    const last = times.at(-1);
    let maxGap = 0;
    for (let k = 1; k < times.length; k++) maxGap = Math.max(maxGap, times[k] - times[k - 1]);
    value = {
      ttft: first,
      deltas: times.length,
      tbt: times.length > 1 ? (last - first) / (times.length - 1) : null,
      maxGap,
      tokens,
      tps: tokens != null && last > d.ttfb_ms ? (tokens * 1000) / (last - d.ttfb_ms) : null,
    };
  }
  timingCache.key = key;
  timingCache.value = value;
  return value;
}

function timingFacts(d) {
  const t = streamTiming(d);
  if (!t) return [];
  const ms = (v) => (v < 10 ? `${v.toFixed(1)} ms` : fmtMs(Math.round(v)));
  return [
    fact("TTFT", fmtMs(t.ttft)),
    fact(
      "TBT",
      t.tbt == null ? "—" : `${ms(t.tbt)} avg, ${fmtMs(t.maxGap)} max`,
      h("span", { class: "muted" }, ` (${plural(t.deltas, "delta")})`),
    ),
    fact(
      "TPS",
      t.tps == null ? "—" : `${t.tps.toFixed(1)} tok/s`,
      t.tokens != null && h("span", { class: "muted" }, ` (${plural(t.tokens, "output token")})`),
    ),
  ];
}

// deltaText extracts the streamed text fragment from a delta event, across
// Anthropic, OpenAI chat completions, and OpenAI Responses formats.
function deltaText(ev) {
  const j = ev.json;
  if (j === null || typeof j !== "object") return null;
  const d = j.delta ?? j.choices?.[0]?.delta;
  if (typeof d === "string") return d;
  if (d === null || typeof d !== "object") return null;
  for (const k of [
    "text",
    "thinking",
    "partial_json",
    "content",
    "reasoning_content",
    "reasoning",
  ]) {
    if (typeof d[k] === "string") return d[k];
  }
  const args = d.tool_calls?.[0]?.function?.arguments;
  return typeof args === "string" ? args : null;
}

function sseView(text) {
  const events = parseSSE(text);
  const groups = [];
  for (const ev of events) {
    const g = groups.at(-1);
    if (g && g.name === ev.name) g.events.push(ev);
    else groups.push({ name: ev.name, events: [ev] });
  }
  return h(
    "div",
    { class: "tree sse" },
    h(
      "p",
      { class: "muted sse-head" },
      `${plural(events.length, "server-sent event")}; consecutive events of the same type are grouped.`,
    ),
    groups.map((g, i) =>
      g.events.length === 1
        ? eventNode(g.events[0], h("span", { class: "ev" }, g.name), `response:${i}:event`)
        : groupNode(g, `response:${i}:group`),
    ),
  );
}

function eventNode(ev, label, path) {
  const j = ev.json;
  if (j !== null && typeof j === "object")
    return container(label, j, { open: false, depth: 1, path });
  return h(
    "div",
    { class: "line" },
    keyLabel(label),
    j === undefined ? h("span", { class: "str" }, ev.raw) : scalar(j),
  );
}

// groupNode shows a run of same-type events, leading with their streamed
// text deltas concatenated.
function groupNode(g, path) {
  const text = g.events
    .map(deltaText)
    .filter((s) => s != null)
    .join("");
  return lazyDetails(
    { cls: "node group", path, open: !!text },
    [
      keyLabel(h("span", { class: "ev" }, g.name)),
      h("span", { class: "count" }, `×${g.events.length}`),
      text && h("span", { class: "preview" }, JSON.stringify(text.slice(0, 200))),
    ],
    () => [
      text && longString("concatenated", text, `${path}/text`, true),
      lazyDetails(
        { path: `${path}/events` },
        [keyLabel("events"), h("span", { class: "count" }, plural(g.events.length, "event"))],
        () =>
          g.events.map((ev, i) =>
            eventNode(ev, h("span", { class: "key idx" }, String(i)), `${path}/events/${i}`),
          ),
      ),
    ],
  );
}

// ---- wiring ----

async function tick() {
  if (document.visibilityState === "visible") {
    try {
      await refreshList();
      $("#offline").hidden = true;
    } catch {
      $("#offline").hidden = false;
    }
    if (state.live && !state.loading) await loadDetail(!state.detail);
  }
  setTimeout(tick, 1500);
}

$("#filter").addEventListener("input", renderList);
window.addEventListener("hashchange", selectFromHash);
selectFromHash();
tick();
