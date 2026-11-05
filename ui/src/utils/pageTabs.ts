import type { LLMContent, LLMMessage, Message } from "../types";
import { MESSAGE_USER_TOOL } from "./conversationView";

/** A link to a web page, as a page tab may show it. declared: the agent
 *  asked for a tab; otherwise the link only moves an existing tab. */
export interface PageLink {
  url: string;
  title: string;
  declared: boolean;
}

/** A web page shown as a tab above a conversation: one per origin (key),
 *  showing the latest page linked there. */
export interface PageTab {
  key: string;
  url: string;
  title: string;
  /** How many links reached its current page: one more reloads it. */
  linked: number;
}

/** At most this many page tabs; the oldest give way. */
export const MAX_PAGE_TABS = 6;

// A markdown link titled "tab": [Draft deploy page](https://… "tab").
const DECLARED = /\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\s+"tab"\)/g;
// Any other link, as linkify finds them.
// eslint-disable-next-line no-useless-escape
const MENTIONED = /https?:\/\/[^\s<>"'`\]\)*]+[^\s<>"'`\]\).,:;!?*]/g;

const linksByMessage = new WeakMap<Message, PageLink[]>();

/** The links in an agent message's text or message_user's: the mentioned
 *  ones first, so a declared one wins its origin. */
function linksIn(message: Message): PageLink[] {
  let links = linksByMessage.get(message);
  if (links) return links;
  const mentioned: PageLink[] = [];
  const declared: PageLink[] = [];
  if (message.type === "agent" && message.llm_data) {
    const llm = JSON.parse(message.llm_data) as LLMMessage;
    for (const content of llm.Content ?? []) {
      const text = contentText(content).replace(DECLARED, (_, title: string, url: string) => {
        declared.push({ title: title.trim(), url, declared: true });
        return "";
      });
      for (const match of text.matchAll(MENTIONED))
        mentioned.push({ title: "", url: match[0], declared: false });
    }
  }
  links = [...mentioned, ...declared];
  linksByMessage.set(message, links);
  return links;
}

function contentText(content: LLMContent): string {
  if (content.Type === 2) return content.Text ?? "";
  if (content.Type === 5 && content.ToolName === MESSAGE_USER_TOOL) {
    const text = (content.ToolInput as { text?: unknown } | undefined)?.text;
    return typeof text === "string" ? text : "";
  }
  return "";
}

/** The links in the agent's messages, oldest first. */
export function pageLinks(messages: Message[]): PageLink[] {
  return messages.flatMap(linksIn);
}

function originOf(url: string): string | null {
  try {
    return new URL(url).origin;
  } catch {
    return null;
  }
}

/** One tab per origin, in the place its first declared link gave it. A
 *  later link there, declared or not, moves it to that page; a declared one
 *  also renames it. Tabs showing a closed URL are hidden; the newest
 *  MAX_PAGE_TABS remain. */
export function pageTabs(links: PageLink[], closed: ReadonlySet<string>): PageTab[] {
  const byOrigin = new Map<string, PageTab>();
  for (const link of links) {
    const key = originOf(link.url);
    if (!key) continue;
    const tab = byOrigin.get(key);
    if (!tab && !link.declared) continue;
    byOrigin.set(key, {
      key,
      url: link.url,
      title: (link.declared && link.title) || tab?.title || link.url,
      linked: tab?.url === link.url ? tab.linked + 1 : 1,
    });
  }
  return [...byOrigin.values()].filter((tab) => !closed.has(tab.url)).slice(-MAX_PAGE_TABS);
}

/** The URL a tab's frame loads: reloaded (at a nonzero time) under a query
 *  parameter of its own, so the browser's HTTP cache can't serve a page the
 *  agent has changed since. */
export function frameURL(url: string, reloadedAt: number): string {
  if (!reloadedAt) return url;
  const reloaded = new URL(url);
  reloaded.searchParams.set("shelley_reload", String(reloadedAt));
  return reloaded.href;
}
