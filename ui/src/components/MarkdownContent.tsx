import React, { useMemo } from "react";
import { Marked } from "marked";
import DOMPurify from "dompurify";

interface MarkdownContentProps {
  text: string;
  // When set, local-path markdown images (relative or absolute file paths) are
  // rewritten to the per-message file endpoint and rendered. Without it we
  // cannot authorize a local file, so such images are dropped.
  messageId?: string;
}

// Maximum size (in characters of the data: URI) we are willing to inline.
// Keeps the DOM and persisted payloads from ballooning when a model emits a
// huge base64 image directly in its markdown.
const MAX_DATA_URI_LENGTH = 2_000_000;

// Prefix of the per-message file endpoint that serves local images. Mirrors
// the route registered in server/server.go.
const FILE_ENDPOINT_RE = /^\/api\/message\/[^/]+\/file\?path=/;

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

// buildMarked returns a Marked instance that rewrites local-path image tokens
// to the per-message file endpoint. Remote images are left with their original
// href (and later stripped by the sanitizer); data images are passed through.
function buildMarked(messageId?: string): Marked {
  const instance = new Marked({ gfm: true, breaks: true });
  instance.use({
    walkTokens(token) {
      if (token.type !== "image") return;
      const kind = classifyImageSrc(token.href ?? "");
      if (kind === "local") {
        // Only rewrite (and thus render) when we know the owning message.
        token.href = messageId ? fileEndpointURL(messageId, token.href) : "";
      }
      // data: kept as-is; remote/invalid left untouched and dropped by sanitize.
    },
  });
  return instance;
}

// Make all links open in new tabs, and restrict <input> to checkboxes only.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
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

// renderMarkdownToSafeHTML parses markdown and returns sanitized HTML. Exported
// so tests can exercise the exact pipeline used by the component.
export function renderMarkdownToSafeHTML(text: string, messageId?: string): string {
  const raw = buildMarked(messageId).parse(text, { async: false }) as string;
  return DOMPurify.sanitize(raw, SANITIZE_OPTS);
}

function MarkdownContent({ text, messageId }: MarkdownContentProps) {
  const html = useMemo(() => renderMarkdownToSafeHTML(text, messageId), [text, messageId]);

  return (
    <div className="markdown-content break-words" dangerouslySetInnerHTML={{ __html: html }} />
  );
}

export default MarkdownContent;
