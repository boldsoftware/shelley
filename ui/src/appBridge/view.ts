// The view side of Shelley's MCP Apps bridge, injected ahead of an
// output_html page's own scripts. It speaks the MCP Apps protocol to the host
// (services/mcpAppHost.ts): themes the page, reports its size, and offers
// window.shelley (documented in claudetool/output_iframe.go).

type HostContext = {
  theme?: "light" | "dark";
  styles?: { variables?: Record<string, string | undefined> };
  displayMode?: string;
  [key: string]: unknown;
};

type Message = {
  jsonrpc: "2.0";
  id?: number | string;
  method?: string;
  params?: Record<string, unknown>;
  result?: unknown;
  error?: { message?: string };
};

const PROTOCOL_VERSION = "2026-01-26";
const root = document.documentElement;
const pending = new Map<number, { resolve(v: unknown): void; reject(e: Error): void }>();
const listeners = new Set<(context: HostContext) => void>();
let nextId = 1;
let hostContext: HostContext = {};

function post(message: Omit<Message, "jsonrpc">) {
  window.parent.postMessage({ jsonrpc: "2.0", ...message }, "*");
}

function request(method: string, params: Record<string, unknown> = {}): Promise<unknown> {
  const id = nextId++;
  post({ id, method, params });
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
}

function applyContext(update: HostContext) {
  hostContext = { ...hostContext, ...update };
  for (const [name, value] of Object.entries(update.styles?.variables ?? {})) {
    if (value) root.style.setProperty(name, value);
  }
  if (update.theme) {
    root.style.colorScheme = update.theme;
    root.dataset.theme = update.theme;
  }
  if (update.theme || update.styles || update.displayMode) {
    for (const listener of listeners) listener(hostContext);
  }
}

window.addEventListener("message", (event) => {
  if (event.source !== window.parent) return;
  const message = event.data as Message;
  if (message?.jsonrpc !== "2.0") return;
  if (message.method === undefined) {
    const call = pending.get(message.id as number);
    pending.delete(message.id as number);
    if (message.error) call?.reject(new Error(message.error.message));
    else call?.resolve(message.result);
    return;
  }
  if (message.method === "ui/notifications/host-context-changed") {
    applyContext(message.params as HostContext);
  } else if (message.id !== undefined) {
    // ping and ui/resource-teardown need nothing more than an answer.
    const known = message.method === "ping" || message.method === "ui/resource-teardown";
    post(
      known
        ? { id: message.id, result: {} }
        : { id: message.id, error: { message: `Unknown method ${message.method}` } },
    );
  }
});

// The page starts themed; its own styles win over these.
const base = document.createElement("style");
base.textContent =
  ":where(html){background:var(--color-background-primary);color:var(--color-text-primary);font-family:var(--font-sans)}";
(document.head ?? root).prepend(base);

function reportSize() {
  // Measure the content, not the iframe it fills.
  const height = root.style.height;
  root.style.height = "max-content";
  const size = root.getBoundingClientRect();
  root.style.height = height;
  post({
    method: "ui/notifications/size-changed",
    params: { width: Math.ceil(size.width), height: Math.ceil(size.height) },
  });
}

const ready = request("ui/initialize", {
  protocolVersion: PROTOCOL_VERSION,
  appInfo: { name: document.title || "output_html", version: "1" },
  appCapabilities: { availableDisplayModes: ["inline", "fullscreen"] },
}).then((result) => {
  applyContext((result as { hostContext?: HostContext }).hostContext ?? {});
  post({ method: "ui/notifications/initialized" });
  new ResizeObserver(reportSize).observe(root);
});

/** A request the user declined, as an Error. */
function check(result: unknown) {
  if ((result as { isError?: boolean })?.isError) throw new Error("The user declined.");
}

const text = (value: unknown) => [{ type: "text", text: String(value) }];

Object.assign(window, {
  shelley: {
    ready,
    get hostContext() {
      return hostContext;
    },
    onHostContextChange(listener: (context: HostContext) => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    sendMessage: (value: unknown) =>
      request("ui/message", { role: "user", content: text(value) }).then(check),
    setContext: (value: unknown) =>
      request("ui/update-model-context", { content: text(value) }).then(check),
    openLink: (url: string) => request("ui/open-link", { url }).then(check),
    download: (filename: string, value: string, mimeType = "text/plain") =>
      request("ui/download-file", {
        contents: [
          {
            type: "resource",
            resource: { uri: `file:///${encodeURIComponent(filename)}`, mimeType, text: value },
          },
        ],
      }).then(check),
    requestDisplayMode: (mode: string) =>
      request("ui/request-display-mode", { mode }).then((r) => (r as { mode: string }).mode),
  },
});
