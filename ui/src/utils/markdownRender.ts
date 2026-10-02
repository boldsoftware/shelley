// Framework-agnostic markdown rendering + sanitization pipeline.
// Extracted from components/MarkdownContent.tsx so both the React component
// and the Vue SFC can share an identical implementation. The React file now
// re-exports renderMarkdownToSafeHTML + classifyImageSrc from here so the
// existing test (components/MarkdownContent.test.ts) keeps passing.
import { Marked } from "marked";
import DOMPurify from "dompurify";
import { rewriteLocalhostLink, type LocalhostLinkOptions } from "./linkify";
import * as markdownMath from "./markdownMath";

// Maximum size (in characters of the data: URI) we are willing to inline.
// Keeps the DOM and persisted payloads from ballooning when a model emits a
// huge base64 image directly in its markdown.
const MAX_DATA_URI_LENGTH = 2_000_000;

// Prefixes of the per-message endpoints that serve local images and file
// downloads. Mirror the routes registered in server/server.go.
const FILE_ENDPOINT_RE = /^\/api\/message\/[^/]+\/file\?path=/;
const DOWNLOAD_ENDPOINT_RE = /^\/api\/message\/[^/]+\/download\?path=/;

// Links like [name](sandbox:/path/to/file) download that file. The scheme is
// ChatGPT's convention for files in its sandbox, and models use it unprompted.
const SANDBOX_SCHEME = "sandbox:";

type ImageKind = "local" | "data" | "remote" | "invalid";

// classifyImageSrc decides how a markdown image src should be handled.
export function classifyImageSrc(src: string): ImageKind {
  const s = src.trim();
  if (s === "") return "invalid";
  // Protocol-relative URLs (//host/...) are remote.
  if (s.startsWith("//")) return "remote";
  // data: URIs are inlined when they are images and small enough.
  if (/^data:/i.test(s)) {
    return /^data:image\//i.test(s) && s.length <= MAX_DATA_URI_LENGTH ? "data" : "invalid";
  }
  // Any other explicit scheme (http:, https:, file:, javascript:, etc.) is
  // treated as remote and not auto-loaded.
  if (/^[a-z][a-z0-9+.-]*:/i.test(s)) return "remote";
  // Everything else is a local path: absolute (/foo.png) or relative
  // (./out/x.png, out/x.png, ../shared/x.png).
  return "local";
}

// fileEndpointURL builds the same-origin URL that serves a local image
// referenced by a specific message.
export function fileEndpointURL(messageId: string, path: string): string {
  return `/api/message/${encodeURIComponent(messageId)}/file?path=${encodeURIComponent(path)}`;
}

// fileDownloadURL builds the same-origin URL that downloads a local file
// linked by a specific message.
function fileDownloadURL(messageId: string, path: string): string {
  return `/api/message/${encodeURIComponent(messageId)}/download?path=${encodeURIComponent(path)}`;
}

// buildMarked returns a Marked instance that rewrites local-path image tokens
// to the per-message file endpoint. Remote images are left with their original
// href (and later stripped by the sanitizer); data images are passed through.
function buildMarked(messageId?: string, localhostLinks?: LocalhostLinkOptions): Marked {
  const instance = new Marked({ gfm: true, breaks: true });
  instance.use({
    walkTokens(token) {
      if (token.type === "image") {
        const kind = classifyImageSrc(token.href ?? "");
        if (kind === "local") {
          // Only rewrite (and thus render) when we know the owning message.
          token.href = messageId ? fileEndpointURL(messageId, token.href) : "";
        }
        // data: kept as-is; remote/invalid left untouched and dropped by sanitize.
        return;
      }
      if (token.type === "link" && token.href.startsWith(SANDBOX_SCHEME)) {
        // Without the owning message there is nothing to authorize the
        // download; the untouched href is then stripped by the sanitizer.
        const path = token.href.slice(SANDBOX_SCHEME.length);
        if (messageId && path) token.href = fileDownloadURL(messageId, path);
        return;
      }
      if (token.type === "link" && localhostLinks) {
        const original = token.href;
        const rewritten = rewriteLocalhostLink(original, localhostLinks);
        token.href = rewritten;
        // Bare URLs and <autolinks> use the URL as their visible label.
        if (rewritten !== original && token.text === original) {
          token.text = rewritten;
          const labelToken = token.tokens?.length === 1 ? token.tokens[0] : undefined;
          if (labelToken?.type === "text") {
            labelToken.text = rewritten;
            labelToken.raw = rewritten;
          }
        }
      }
    },
  });
  return instance;
}

function setLinkTarget(node: Element): void {
  node.setAttribute("target", "_blank");
  node.setAttribute("rel", "noopener noreferrer");
}

// Work on the sanitized DOM so code text stays literal, attributes are escaped
// by the DOM, and existing links (including raw HTML anchors) are easy to skip.
function linkifyCodeSpans(root: HTMLElement, localhostLinks?: LocalhostLinkOptions): void {
  for (const code of root.querySelectorAll("code")) {
    if (code.closest("pre, a") || code.children.length > 0) continue;
    const text = code.textContent ?? "";
    if (!/^https?:\/\/\S+$/i.test(text)) continue;
    try {
      new URL(text);
    } catch {
      continue;
    }
    const href = localhostLinks ? rewriteLocalhostLink(text, localhostLinks) : text;
    const link = root.ownerDocument.createElement("a");
    link.setAttribute("href", href);
    setLinkTarget(link);
    code.textContent = href;
    code.replaceWith(link);
    link.append(code);
  }
}

// Make links open in new tabs (downloads stay put), and restrict <input> to
// checkboxes only.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    if (DOWNLOAD_ENDPOINT_RE.test(node.getAttribute("href") ?? "")) {
      node.setAttribute("download", "");
    } else {
      setLinkTarget(node);
    }
  }
  // Only allow checkbox inputs (for GFM task lists); remove all others.
  if (node.tagName === "INPUT" && node.getAttribute("type") !== "checkbox") {
    node.remove();
  }
  // Images are admitted only when they point at the same-origin per-message
  // file endpoint or are a small inline image data: URI. Anything else
  // (remote URLs, oversized/non-image data URIs, unrewritten local paths) is
  // removed so we never auto-load arbitrary remote or unauthorized content.
  if (node.tagName === "IMG") {
    const src = node.getAttribute("src") ?? "";
    const allowed =
      FILE_ENDPOINT_RE.test(src) ||
      (/^data:image\//i.test(src) && src.length <= MAX_DATA_URI_LENGTH);
    if (!allowed) {
      node.remove();
      return;
    }
    node.setAttribute("loading", "lazy");
  }
});

const SANITIZE_OPTS = {
  ALLOWED_TAGS: [
    "p",
    "br",
    "strong",
    "em",
    "code",
    "pre",
    "blockquote",
    "ul",
    "ol",
    "li",
    "a",
    "img",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "hr",
    "table",
    "thead",
    "tbody",
    "tr",
    "th",
    "td",
    "del",
    "input",
    "span",
    "sup",
    "div",
    "details",
    "summary",
  ],
  ALLOWED_ATTR: [
    "href",
    "src",
    "alt",
    "title",
    "loading",
    "target",
    "rel",
    "type",
    "checked",
    "disabled",
    "class",
    "open",
  ],
};

// Cache of rendered+sanitized HTML, scoped to the lifetime of the immutable
// object (in practice, a Message) that owns a markdown run. Keying on the
// object reference itself — rather than on the rendered text — means no
// source text is retained as a cache key, and entries disappear for free once
// their owner becomes unreachable (conversation pruned, tab closed, etc.): no
// eviction policy or size cap needed.
//
// A single owner can have multiple markdown runs (coalesceContent splits a
// message's content into several adjacent text blocks whenever tool calls
// interleave with prose), so each owner maps to a small Map<runKey, html>
// rather than a single string. Callers supply a runKey that's stable and
// unique for a given run within that owner (Message.vue uses the
// coalescedContent index).
const cache = new WeakMap<object, Map<string, string>>();

export interface MarkdownCacheKey {
  // Object whose lifetime bounds the cache entry.
  owner: object;
  // Distinguishes multiple runs within the same owner.
  runKey: string;
}

// renderMarkdownToSafeHTML parses markdown and returns sanitized HTML.
//
// `messageId` drives the local-image URL rewrite only (see buildMarked above)
// and plays no part in caching. `cacheKey`, when supplied, memoizes the result
// for the lifetime of `cacheKey.owner`; callers whose text can change without
// a new owner — the streaming preview, the distillation preview, export —
// omit it and always re-render.
export function renderMarkdownToSafeHTML(
  text: string,
  messageId?: string,
  cacheKey?: MarkdownCacheKey,
  localhostLinks?: LocalhostLinkOptions,
): string {
  let runs = cacheKey ? cache.get(cacheKey.owner) : undefined;
  const cached = cacheKey ? runs?.get(cacheKey.runKey) : undefined;
  if (cached !== undefined) return cached;

  const raw = buildMarked(messageId, localhostLinks)
    .use(markdownMath.createMathExtension())
    .parse(text, { async: false }) as string;
  const root = DOMPurify.sanitize(raw, { ...SANITIZE_OPTS, RETURN_DOM: true }) as HTMLElement;
  linkifyCodeSpans(root, localhostLinks);
  const html = root.innerHTML;

  if (cacheKey) {
    if (!runs) {
      runs = new Map();
      cache.set(cacheKey.owner, runs);
    }
    runs.set(cacheKey.runKey, html);
  }
  return html;
}
