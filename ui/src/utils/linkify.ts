// Regex for matching URLs. Only matches http:// and https:// URLs.
// Avoids matching trailing punctuation that's likely not part of the URL.
// eslint-disable-next-line no-useless-escape
const URL_REGEX = /https?:\/\/[^\s<>"'`\]\)*]+[^\s<>"'`\]\).,:;!?*]/g;

export interface LocalhostLinkOptions {
  isExeDev: boolean;
  hostname: string;
}

const LOCAL_HOSTNAMES = new Set(["localhost", "127.0.0.1", "0.0.0.0"]);

/**
 * Turn a VM-local web URL into the equivalent user-reachable exe.dev URL.
 */
export function rewriteLocalhostLink(rawURL: string, options: LocalhostLinkOptions): string {
  if (!options.isExeDev || options.hostname.trim() === "") return rawURL;

  let url: URL;
  try {
    url = new URL(rawURL);
  } catch {
    return rawURL;
  }

  const port = Number(url.port);
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.username !== "" ||
    url.password !== "" ||
    !LOCAL_HOSTNAMES.has(url.hostname.toLowerCase()) ||
    url.port === "" ||
    !Number.isInteger(port) ||
    port < 3000 ||
    port > 9999
  ) {
    return rawURL;
  }

  return `https://${options.hostname}:${url.port}${url.pathname}${url.search}${url.hash}`;
}

export function localhostLinkOptionsFromInit(): LocalhostLinkOptions {
  return {
    isExeDev: window.__SHELLEY_INIT__?.is_exe_dev === true,
    hostname: window.__SHELLEY_INIT__?.hostname ?? "",
  };
}

export interface LinkifyResult {
  type: "text" | "link";
  content: string;
  href?: string;
}

/**
 * Parse text and extract URLs as separate segments.
 * Returns an array of text and link segments.
 */
export function parseLinks(text: string, options?: LocalhostLinkOptions): LinkifyResult[] {
  const results: LinkifyResult[] = [];
  let lastIndex = 0;

  // Reset regex state
  URL_REGEX.lastIndex = 0;

  let match;
  while ((match = URL_REGEX.exec(text)) !== null) {
    // Add text before the match
    if (match.index > lastIndex) {
      results.push({
        type: "text",
        content: text.slice(lastIndex, match.index),
      });
    }

    // Add the link; only its href goes through the exe.dev proxy.
    const url = match[0];
    results.push({
      type: "link",
      content: url,
      href: options ? rewriteLocalhostLink(url, options) : url,
    });

    lastIndex = match.index + url.length;
  }

  // Add remaining text after last match
  if (lastIndex < text.length) {
    results.push({
      type: "text",
      content: text.slice(lastIndex),
    });
  }

  return results;
}
