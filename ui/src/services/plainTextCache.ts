type PlainTextStripper = (content: string) => string;

function contentHash(content: string): string {
  let hash = 2166136261;
  for (let i = 0; i < content.length; i++) {
    hash ^= content.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

function stripMarkdown(content: string): string {
  return content
    .replace(/```[^\n]*\n?/g, "")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^\s{0,3}(?:#{1,6}|>|[-+*]|\d+\.)\s+/gm, "")
    .replace(/<[^>]+>/g, "")
    .replace(/[*_~`]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

export class PlainTextCache {
  private readonly values = new Map<string, string>();
  private readonly latestKeyByMessage = new Map<string, string>();

  constructor(private readonly strip: PlainTextStripper = stripMarkdown) {}

  get(messageId: string, content: string): string {
    const key = `${messageId}:${contentHash(content)}`;
    const previousKey = this.latestKeyByMessage.get(messageId);
    if (previousKey && previousKey !== key) this.values.delete(previousKey);
    this.latestKeyByMessage.set(messageId, key);

    const cached = this.values.get(key);
    if (cached !== undefined) return cached;
    const plainText = this.strip(content);
    this.values.set(key, plainText);
    return plainText;
  }

  invalidate(messageId: string): void {
    const key = this.latestKeyByMessage.get(messageId);
    if (key) this.values.delete(key);
    this.latestKeyByMessage.delete(messageId);
  }

  get size(): number {
    return this.values.size;
  }
}

export const plainTextCache = new PlainTextCache();
