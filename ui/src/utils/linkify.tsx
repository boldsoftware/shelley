// Regex for matching URLs. Only matches http:// and https:// URLs.
// Avoids matching trailing punctuation that's likely not part of the URL.
// eslint-disable-next-line no-useless-escape
const URL_REGEX = /https?:\/\/[^\s<>"'`\]\)*]+[^\s<>"'`\]\).,:;!?*]/g;

export interface LinkifyResult {
  type: "text" | "link";
  content: string;
  href?: string;
}

/**
 * Parse text and extract URLs as separate segments.
 * Returns an array of text and link segments.
 */
export function parseLinks(text: string): LinkifyResult[] {
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

    // Add the link
    const url = match[0];
    results.push({
      type: "link",
      content: url,
      href: url,
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
