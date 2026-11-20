// The host side of MCP Apps (modelcontextprotocol.io/extensions/apps): a view
// runs in a sandboxed iframe and speaks JSON-RPC over postMessage. MCP tools'
// views and output_html pages (appBridge/view.ts) share this host.

import { apiFetch, api, sendJSON } from "./api";
import { setAppContext } from "./appContext";

export const APP_PROTOCOL_VERSION = "2026-01-26";

export type DisplayMode = "inline" | "fullscreen";
export const DISPLAY_MODES: DisplayMode[] = ["inline", "fullscreen"];

export interface ContentBlock {
  type: string;
  text?: string;
  [key: string]: unknown;
}

export interface CallToolResult {
  content?: ContentBlock[];
  structuredContent?: unknown;
  isError?: boolean;
  [key: string]: unknown;
}

export interface AppTool {
  name: string;
  inputSchema: unknown;
  [key: string]: unknown;
}

/** An MCP server's UI resource, as GET .../app returns it (mcp.App). */
export interface AppResource {
  uri: string;
  html: string;
  csp: Record<string, string[] | undefined>;
  policy: string;
  permissions: Record<string, object | undefined>;
  prefersBorder?: boolean;
}

export interface HostContext {
  theme: "light" | "dark";
  displayMode: DisplayMode;
  containerDimensions: Record<string, number>;
  [key: string]: unknown;
}

/** Something a view asks to do as the user, which the user must allow. */
export interface Consent {
  kind: "send" | "open" | "save";
  /** What exactly: the message, the URL, the files. */
  detail: string;
}

export interface AppHostOptions {
  /** Names the view to the model, e.g. "server.tool". */
  source: string;
  /** The ID of what shows the view, an mcpapp message or a tool call;
   *  keys what it asked the model to know. */
  key: string;
  conversationId: string;
  /** The MCP server the view comes from, whose tools and resources it may use. */
  server?: string;
  /** Identifies the server as it was when the view was shown. */
  serverId?: string;
  tool?: AppTool;
  toolCallId?: string;
  toolInput?: Record<string, unknown>;
  toolResult?: CallToolResult;
  /** The sandbox the view got: permissions and CSP domains. */
  sandbox: { permissions: Record<string, object | undefined>; csp: Record<string, unknown> };
  /** What the host's state says about the view now. */
  context(): HostContext;
  onSize(size: { width?: number; height?: number }): void;
  /** Switches display mode; returns the one in effect. */
  onDisplayMode(mode: DisplayMode): DisplayMode;
  /** Asks the user; whether they allowed it. */
  confirm(consent: Consent): Promise<boolean>;
}

interface RPC {
  jsonrpc: "2.0";
  id?: string | number;
  method?: string;
  params?: Record<string, unknown>;
  result?: unknown;
  error?: { code: number; message: string };
}

class RPCError extends Error {
  constructor(
    readonly code: number,
    message: string,
  ) {
    super(message);
  }
}

/**
 * Characters that don't show, or don't show as what they are: controls but
 * tab and newlines, format characters (zero-width, bidi, tags), private use,
 * default-ignorables (variation selectors, fillers...), and the braille
 * blank. Writing uses some (SHOWN_USES, DIRECTION_MARK).
 */
const HIDDEN =
  // eslint-disable-next-line no-control-regex
  /[\0-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f\p{Cf}\p{Co}\p{DI}\u2800]/u;

/** Hidden characters that do something visible: emoji's presentation
 *  selectors, joiners between two, keycaps' selector, and the tags of the
 *  three subdivision flags (England, Scotland, Wales); one joiner or
 *  non-joiner between letters of a script that joins them (Arabic, Indic). */
const SHOWN_USES =
  // eslint-disable-next-line no-misleading-character-class
  /(?<=\p{Extended_Pictographic})[\ufe0e\ufe0f]|(?<=\p{Extended_Pictographic}[\ufe0f\u{1f3fb}-\u{1f3ff}]?)\u200d(?=\p{Extended_Pictographic})|(?<=[#*0-9])\ufe0f(?=\u20e3)|(?<=\u{1f3f4})\u{e0067}\u{e0062}(?:\u{e0065}\u{e006e}\u{e0067}|\u{e0073}\u{e0063}\u{e0074}|\u{e0077}\u{e006c}\u{e0073})\u{e007f}|(?<=(?=\p{L})[\p{sc=Arabic}\p{sc=Syriac}\p{sc=Nko}\p{sc=Mongolian}\p{sc=Devanagari}\p{sc=Bengali}\p{sc=Gurmukhi}\p{sc=Gujarati}\p{sc=Oriya}\p{sc=Tamil}\p{sc=Telugu}\p{sc=Kannada}\p{sc=Malayalam}\p{sc=Sinhala}]\p{M}{0,4})[\u200c\u200d](?=\p{L})(?=[\p{sc=Arabic}\p{sc=Syriac}\p{sc=Nko}\p{sc=Mongolian}\p{sc=Devanagari}\p{sc=Bengali}\p{sc=Gurmukhi}\p{sc=Gujarati}\p{sc=Oriya}\p{sc=Tamil}\p{sc=Telugu}\p{sc=Kannada}\p{sc=Malayalam}\p{sc=Sinhala}])/gu;

/** In text with right-to-left letters, direction marks, one at a time. */
const RIGHT_TO_LEFT = /(?=\p{L})[\p{sc=Hebrew}\p{sc=Arabic}\p{sc=Syriac}\p{sc=Thaana}\p{sc=Nko}]/u;
const DIRECTION_MARK = /(?<![\u200e\u200f\u061c])[\u200e\u200f\u061c](?![\u200e\u200f\u061c])/gu;

/** Whether text has characters that don't show (HIDDEN) for no visible use. */
function hasHidden(text: string): boolean {
  let shown = text.replace(SHOWN_USES, "");
  if (RIGHT_TO_LEFT.test(shown)) shown = shown.replace(DIRECTION_MARK, "");
  return HIDDEN.test(shown);
}

/** The answer to a request the user declined. */
const DECLINED = { isError: true };

const NOT_FOUND = -32601;
const INVALID = -32602;
const FAILED = -32000;

const css = (name: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim();

/** Shelley's theme as MCP Apps' standard style variables. */
export function styleVariables(): Record<string, string> {
  const v: Record<string, string> = {};
  const tones = { info: "blue", danger: "error", success: "success", warning: "warning" };
  for (const [tone, ours] of Object.entries(tones)) {
    v[`--color-background-${tone}`] = css(`--${ours}-bg`);
    v[`--color-text-${tone}`] = css(`--${ours}-text`);
    v[`--color-border-${tone}`] = css(`--${ours}-border`);
    v[`--color-ring-${tone}`] = css(`--${ours}-text`);
  }
  for (const level of ["primary", "secondary", "tertiary"]) {
    v[`--color-text-${level}`] = css(`--text-${level}`);
    v[`--color-border-${level}`] = css("--border");
  }
  Object.assign(v, {
    "--color-background-primary": css("--bg-base"),
    "--color-background-secondary": css("--bg-secondary"),
    "--color-background-tertiary": css("--bg-tertiary"),
    "--color-background-inverse": css("--text-primary"),
    "--color-background-ghost": "transparent",
    "--color-background-disabled": css("--bg-tertiary"),
    "--color-text-inverse": css("--bg-base"),
    "--color-text-ghost": css("--text-tertiary"),
    "--color-text-disabled": css("--text-tertiary"),
    "--color-border-inverse": css("--text-primary"),
    "--color-border-ghost": "transparent",
    "--color-border-disabled": css("--border"),
    "--color-ring-primary": css("--primary"),
    "--color-ring-secondary": css("--text-secondary"),
    "--color-ring-inverse": css("--bg-base"),
    "--font-sans": css("--font-sans"),
    "--font-mono": css("--font-mono"),
    "--font-weight-normal": "400",
    "--font-weight-medium": "500",
    "--font-weight-semibold": "600",
    "--font-weight-bold": "700",
    "--border-width-regular": "1px",
    "--shadow-hairline": "0 0 0 1px rgb(0 0 0 / 0.06)",
    "--shadow-sm": "0 1px 2px rgb(0 0 0 / 0.08)",
    "--shadow-md": "0 4px 8px rgb(0 0 0 / 0.1)",
    "--shadow-lg": "0 12px 24px rgb(0 0 0 / 0.14)",
  });
  const text = { xs: [0.75, 1], sm: [0.875, 1.25], md: [1, 1.5], lg: [1.125, 1.75] };
  for (const [size, [rem, line]] of Object.entries(text)) {
    v[`--font-text-${size}-size`] = `${rem}rem`;
    v[`--font-text-${size}-line-height`] = `${line}rem`;
  }
  const headings = {
    xs: [0.875, 1.25],
    sm: [1, 1.5],
    md: [1.125, 1.75],
    lg: [1.25, 1.75],
    xl: [1.5, 2],
    "2xl": [1.875, 2.25],
    "3xl": [2.25, 2.5],
  };
  for (const [size, [rem, line]] of Object.entries(headings)) {
    v[`--font-heading-${size}-size`] = `${rem}rem`;
    v[`--font-heading-${size}-line-height`] = `${line}rem`;
  }
  const radii = { xs: "2px", sm: "4px", md: "6px", lg: "8px", xl: "12px", full: "9999px" };
  for (const [size, radius] of Object.entries(radii)) v[`--border-radius-${size}`] = radius;
  return v;
}

/** The text of content blocks, with structured content as JSON. */
export function contentText(blocks: unknown, structured?: unknown): string {
  const parts = Array.isArray(blocks)
    ? (blocks as ContentBlock[])
        .filter((b) => b?.type === "text" && typeof b.text === "string")
        .map((b) => b.text as string)
    : [];
  if (structured !== undefined) parts.push(JSON.stringify(structured));
  return parts.join("\n").trim();
}

/** A token for a view's channel (channelScript), as hex. */
export function channelToken(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/**
 * The first script in a view's page. It opens a channel to the host, which
 * talks to the view only on it, and stands in for window.parent, so the view
 * speaks MCP Apps' postMessage protocol as usual. Only this page can use the
 * channel: a page the view goes to, which shares its window and opaque
 * origin, can't reach the host, nor can anything the view hands window.parent
 * to. The page never holds the channel's port, only a stand-in that goes
 * nowhere but forwards what the page posts. The script uses only built-ins
 * it captured before the page's scripts could change them, and runs none of
 * the page's code while the port's event is current (window.event): it
 * queues the host's messages and passes them on when a private channel of
 * its own wakes it (unlike timers, that doesn't wait in hidden tabs). The
 * wake-ups carry nothing, so the page, which can get that channel's port as
 * the event that wakes it, gets nothing from handing it on.
 */
export function channelScript(token: string): string {
  return `<script>(() => {
  const host = window.parent;
  const { port1: port, port2 } = new MessageChannel();
  const send = MessagePort.prototype.postMessage.bind(port);
  const dataOf = Function.prototype.call.bind(
    Object.getOwnPropertyDescriptor(MessageEvent.prototype, "data").get,
  );
  const dispatch = EventTarget.prototype.dispatchEvent.bind(window);
  const Message = MessageEvent;
  const relay = new MessageChannel();
  const pass = MessagePort.prototype.postMessage.bind(relay.port1);
  const standIn = new MessageChannel().port1;
  Object.defineProperty(standIn, "postMessage", { value: (data) => send(data) });
  // No prototype, so filling it runs none of the page's code.
  const inbox = { __proto__: null };
  let first = 0;
  let next = 0;
  port.onmessage = (event) => {
    inbox[next++] = dataOf(event);
    pass(null);
  };
  relay.port2.onmessage = () => {
    const data = inbox[first];
    delete inbox[first++];
    dispatch(new Message("message", { __proto__: null, data, source: standIn }));
  };
  Object.defineProperty(window, "parent", { value: standIn, writable: true, configurable: true });
  host.postMessage({ "shelley/channel": ${JSON.stringify(token)} }, "*", [port2]);
  document.currentScript.remove();
})();</script>`;
}

/** Hosts one view: answers its requests and tells it what changes. */
export class AppHost {
  private closed = false;
  private initialized = false;
  private displayModes?: DisplayMode[];
  private nextId = 1;
  private port?: MessagePort;
  private queued: unknown[] = [];

  /** token is the one in the view's channelScript. */
  constructor(
    private readonly frame: HTMLIFrameElement,
    private readonly token: string,
    private readonly opts: AppHostOptions,
  ) {}

  /** Takes the view's channel, if event brings it. */
  receive(event: MessageEvent) {
    const data = event.data as Record<string, unknown> | null;
    if (
      this.closed ||
      this.port ||
      event.source !== this.frame.contentWindow ||
      data?.["shelley/channel"] !== this.token ||
      event.ports.length !== 1
    ) {
      return;
    }
    this.port = event.ports[0];
    this.port.onmessage = (e) => this.handle(e.data);
    for (const message of this.queued.splice(0)) this.port.postMessage(message);
  }

  private handle(data: unknown) {
    if (this.closed) return;
    const message = data as RPC;
    if (message?.jsonrpc !== "2.0" || typeof message.method !== "string") return;
    const { id, method, params = {} } = message;
    if (id === undefined) {
      this.notified(method, params);
      return;
    }
    this.answer(method, params).then(
      (result) => this.post({ id, result }),
      (err: unknown) =>
        this.post({
          id,
          error: {
            code: err instanceof RPCError ? err.code : FAILED,
            message: err instanceof Error ? err.message : String(err),
          },
        }),
    );
  }

  /** Asks the user, unless the view is gone by then. */
  private async allowed(consent: Consent): Promise<boolean> {
    if (hasHidden(consent.detail)) {
      throw new RPCError(INVALID, "Views can't ask with text the user can't see.");
    }
    return (await this.opts.confirm(consent)) && !this.closed;
  }

  /** Tells an initialized view what changed. */
  changed(update: Partial<HostContext> & Record<string, unknown>) {
    if (this.initialized) this.notify("ui/notifications/host-context-changed", update);
  }

  /** Asks the view to clean up before it goes; it won't get to answer. */
  teardown() {
    if (this.initialized) {
      this.post({ id: this.nextId++, method: "ui/resource-teardown", params: {} });
    }
  }

  /** Stops hosting: the frame shows another document now. */
  close() {
    this.closed = true;
    this.port?.close();
  }

  /** Sends the view a message outside the protocol, once it has a channel. */
  send(message: unknown) {
    if (this.closed) return;
    // As JSON: Vue's reactive proxies can't be cloned.
    const data: unknown = JSON.parse(JSON.stringify(message));
    if (this.port) this.port.postMessage(data);
    else this.queued.push(data);
  }

  private post(message: Omit<RPC, "jsonrpc">) {
    this.send({ jsonrpc: "2.0", ...message });
  }

  private notify(method: string, params: unknown) {
    this.post({ method, params: params as Record<string, unknown> });
  }

  private notified(method: string, params: Record<string, unknown>) {
    switch (method) {
      case "ui/notifications/initialized":
        if (this.initialized) return;
        this.initialized = true;
        if (this.opts.toolInput) {
          this.notify("ui/notifications/tool-input", { arguments: this.opts.toolInput });
        }
        if (this.opts.toolResult) {
          this.notify("ui/notifications/tool-result", this.opts.toolResult);
        }
        return;
      case "ui/notifications/size-changed":
        this.opts.onSize(params as { width?: number; height?: number });
        return;
      case "notifications/message":
        console.info(`${this.opts.source}:`, params.data);
        return;
    }
  }

  private hostContext() {
    const coarse = matchMedia("(pointer: coarse)").matches;
    return {
      ...(this.opts.tool && {
        toolInfo: {
          ...(this.opts.toolCallId && { id: this.opts.toolCallId }),
          tool: this.opts.tool,
        },
      }),
      ...this.opts.context(),
      styles: { variables: styleVariables() },
      availableDisplayModes: DISPLAY_MODES,
      locale: navigator.language,
      timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      userAgent: "Shelley",
      platform: coarse ? "mobile" : "web",
      deviceCapabilities: { touch: coarse, hover: matchMedia("(hover: hover)").matches },
    };
  }

  private async answer(method: string, params: Record<string, unknown>): Promise<unknown> {
    const { server, serverId = "", conversationId, source, key } = this.opts;
    const scoped = (init: RequestInit): RequestInit => ({
      ...init,
      headers: {
        ...(init.headers as Record<string, string>),
        ...appHeaders(conversationId, serverId),
      },
    });
    switch (method) {
      case "ui/initialize": {
        const caps = params.appCapabilities as { availableDisplayModes?: DisplayMode[] };
        this.displayModes = caps?.availableDisplayModes;
        return {
          protocolVersion: APP_PROTOCOL_VERSION,
          hostInfo: { name: "Shelley", version: window.__SHELLEY_INIT__?.ui_build || "dev" },
          hostCapabilities: {
            openLinks: {},
            downloadFile: {},
            ...(server && { serverTools: {}, serverResources: {} }),
            logging: {},
            sandbox: this.opts.sandbox,
            updateModelContext: { text: {}, structuredContent: {} },
            message: { text: {} },
          },
          hostContext: this.hostContext(),
        };
      }
      case "ping":
        return {};
      case "tools/call": {
        if (!server) throw new RPCError(NOT_FOUND, "This view has no MCP server.");
        const call = { tool: params.name, arguments: params.arguments ?? {} };
        const response = await apiFetch(
          `/api/mcp/servers/${encodeURIComponent(server)}/app-call`,
          `${server}.${String(params.name)} failed`,
          scoped(sendJSON("POST", call)),
        );
        return response.json();
      }
      case "resources/read": {
        if (!server) throw new RPCError(NOT_FOUND, "This view has no MCP server.");
        const query = new URLSearchParams({ uri: String(params.uri) });
        const response = await apiFetch(
          `/api/mcp/servers/${encodeURIComponent(server)}/resource?${query}`,
          `Reading ${String(params.uri)} failed`,
          scoped({}),
        );
        return response.json();
      }
      case "ui/open-link": {
        const url = new URL(String(params.url));
        if (url.protocol !== "https:" && url.protocol !== "http:") {
          throw new RPCError(INVALID, "Views may only open http(s) links.");
        }
        if (!(await this.allowed({ kind: "open", detail: url.href }))) return DECLINED;
        window.open(url, "_blank", "noopener,noreferrer");
        return {};
      }
      case "ui/message": {
        const text = contentText(params.content);
        if (params.role !== "user" || !text) {
          throw new RPCError(INVALID, "Views send text messages from the user.");
        }
        if (!(await this.allowed({ kind: "send", detail: text }))) return DECLINED;
        await api.sendMessage(conversationId, { message: text });
        return {};
      }
      case "ui/update-model-context": {
        // Without what shows it, the server can't tell if it may go.
        if (!key) throw new RPCError(INVALID, "This view can't set context.");
        const text = contentText(params.content, params.structuredContent);
        setAppContext(conversationId, key, text ? { source, text, view: key } : null);
        return {};
      }
      case "ui/request-display-mode": {
        const mode = params.mode as DisplayMode;
        if (
          !DISPLAY_MODES.includes(mode) ||
          (this.displayModes && !this.displayModes.includes(mode))
        ) {
          return { mode: this.opts.context().displayMode };
        }
        return { mode: this.opts.onDisplayMode(mode) };
      }
      case "ui/download-file": {
        const contents = ((params.contents as ContentBlock[]) ?? []).filter(downloadable);
        if (!contents.length) throw new RPCError(INVALID, "Nothing to download.");
        const detail = contents.map(describeDownload).join("\n");
        if (!(await this.allowed({ kind: "save", detail }))) return DECLINED;
        contents.forEach(download);
        return {};
      }
    }
    throw new RPCError(NOT_FOUND, `Unknown method ${method}`);
  }
}

interface EmbeddedResource {
  uri: string;
  mimeType?: string;
  text?: string;
  blob?: string;
}

/** Whether item is an embedded resource, or a link to an http(s) one. */
function downloadable(item: ContentBlock): boolean {
  if (item?.type === "resource_link") return /^https?:/i.test(String(item.uri));
  const resource = item?.resource as EmbeddedResource | undefined;
  return item?.type === "resource" && typeof resource?.uri === "string";
}

function fileName(resource: EmbeddedResource): string {
  try {
    return decodeURIComponent(resource.uri.split("/").pop() || "") || "download";
  } catch {
    return "download";
  }
}

function fileData(resource: EmbeddedResource): BlobPart {
  return resource.blob !== undefined
    ? Uint8Array.from(atob(resource.blob), (c) => c.charCodeAt(0))
    : (resource.text ?? "");
}

/** A download as the user sees it to allow it. */
function describeDownload(item: ContentBlock): string {
  if (item.type === "resource_link") return String(item.uri);
  const resource = item.resource as EmbeddedResource;
  const size = new Blob([fileData(resource)]).size;
  return `${fileName(resource)} (${size.toLocaleString()} B)`;
}

/** Saves an embedded resource, or opens a linked one. */
function download(item: ContentBlock) {
  if (item.type === "resource_link") {
    window.open(String(item.uri), "_blank", "noopener,noreferrer");
    return;
  }
  const resource = item.resource as EmbeddedResource;
  const type = resource.mimeType || "application/octet-stream";
  const url = URL.createObjectURL(new Blob([fileData(resource)], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = fileName(resource);
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/**
 * Headers for a view's requests: they share the conversation's MCP sessions,
 * and reach the server only if it is still the one that showed the view.
 */
function appHeaders(conversationId: string, serverId: string): Record<string, string> {
  return { "X-Shelley-Conversation-Id": conversationId, "X-Shelley-Mcp-App-Server": serverId };
}

// Views are static; one fetch serves every showing in a conversation, whose
// MCP session the fetch uses.
const apps = new Map<string, Promise<AppResource>>();

/** Fetches an MCP server's view. */
export function fetchApp(
  server: string,
  serverId: string,
  uri: string,
  conversationId: string,
): Promise<AppResource> {
  const key = JSON.stringify([conversationId, server, serverId, uri]);
  let app = apps.get(key);
  if (!app) {
    const query = new URLSearchParams({ uri });
    app = apiFetch(
      `/api/mcp/servers/${encodeURIComponent(server)}/app?${query}`,
      `Couldn't load ${server}'s view`,
      { headers: appHeaders(conversationId, serverId) },
    ).then((r) => r.json() as Promise<AppResource>);
    app.catch(() => apps.delete(key));
    apps.set(key, app);
  }
  return app;
}

/**
 * Puts markup ahead of everything in a page, so it lands first in the head
 * whether or not the page spells one out. Only a plain HTML doctype, which
 * the parser can't read any other way, may stay ahead of it; other pages
 * lose theirs.
 */
export function prependToPage(html: string, markup: string): string {
  const doctype = /^[\t\n\f\r ]*<!doctype[\t\n\f\r ]+html[\t\n\f\r ]*>/i.exec(html);
  return doctype ? doctype[0] + markup + html.slice(doctype[0].length) : markup + html;
}

/**
 * The view's HTML under its CSP, which a meta tag sets in its document. The
 * policy covers only what follows it, so it goes first.
 */
export function withPolicy(html: string, policy: string): string {
  const meta = `<meta http-equiv="Content-Security-Policy" content="${policy.replace(/"/g, "&quot;")}">`;
  return prependToPage(html, meta);
}
