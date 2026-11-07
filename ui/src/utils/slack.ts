// The Slack Assistant's conversation, read for display: the Slack events
// delivered to it (user messages in the tag server/slack_channel.go's
// slackEvent.tagged writes, which the model reads), the names they carry, and
// the Block Kit blocks of its replies.
import type { Message } from "../types";
import { mrkdwnText, slackEmoji, type SlackNames } from "./slackMrkdwn";
import { llmData } from "./llmData";

export type SlackEventKind = "message" | "reaction" | "action";

export interface SlackEvent {
  kind: SlackEventKind;
  channel: string;
  /** "#name", "DM", or "" if unknown. */
  channelName: string;
  threadTS: string;
  ts: string;
  user: string;
  /** "" if unknown. */
  userName: string;
  mentionsYou: boolean;
  /** A reaction's emoji name. */
  emoji: string;
  /** mrkdwn: a message's text, the text of the message reacted to, or a
   * click's description. */
  text: string;
  /** Local paths of the files shared with a message. */
  files: string[];
}

const TAG =
  /^<slack_(message|reaction|action)((?: [a-z_]+="(?:[^"\\]|\\.)*")*)>\n([\s\S]*)\n<\/slack_\1>$/;
const ATTR = / ([a-z_]+)="((?:[^"\\]|\\.)*)"/g;
const REQUIRED: Record<SlackEventKind, string[]> = {
  message: ["mentions_you"],
  reaction: ["emoji"],
  action: [],
};
// Files are referenced by path after the text (channelMessageText).
const FILES = /(?:^|\n\n)(\[\/[^\]\n]+\](?: \[\/[^\]\n]+\])*)$/;

const GO_ESCAPES: Record<string, string> = {
  a: "\x07",
  b: "\b",
  f: "\f",
  n: "\n",
  r: "\r",
  t: "\t",
  v: "\v",
  "\\": "\\",
  '"': '"',
};

// unquote undoes Go's %q, which the tag's attributes are written with.
function unquote(s: string): string {
  return s.replace(
    /\\(?:x([0-9a-fA-F]{2})|u([0-9a-fA-F]{4})|U([0-9a-fA-F]{8})|(.))/g,
    (m, x?: string, u?: string, U?: string, c?: string) => {
      const hex = x || u || U;
      return hex ? String.fromCodePoint(parseInt(hex, 16)) : (GO_ESCAPES[c!] ?? m);
    },
  );
}

// unescapeAttr undoes Go's html.EscapeString.
function unescapeAttr(s: string): string {
  const entities: Record<string, string> = {
    "&lt;": "<",
    "&gt;": ">",
    "&amp;": "&",
    "&#39;": "'",
    "&#34;": '"',
  };
  return s.replace(/&(?:lt|gt|amp|#39|#34);/g, (e) => entities[e]);
}

/** parseSlackEvent parses text that is exactly one slack_* tag. */
export function parseSlackEvent(text: string): SlackEvent | null {
  const m = TAG.exec(text);
  if (!m) return null;
  const kind = m[1] as SlackEventKind;
  const attrs = new Map<string, string>();
  for (const [, key, value] of m[2].matchAll(ATTR)) {
    if (attrs.has(key)) return null;
    attrs.set(key, unescapeAttr(unquote(value)));
  }
  const required = ["channel", "channel_name", "thread_ts", "ts", "user", "user_name"];
  if (![...required, ...REQUIRED[kind]].every((key) => attrs.has(key))) return null;
  const attr = (key: string) => attrs.get(key) ?? "";
  const files = kind === "message" ? FILES.exec(m[3]) : null;
  return {
    kind,
    channel: attr("channel"),
    channelName: attr("channel_name"),
    threadTS: attr("thread_ts"),
    ts: attr("ts"),
    user: attr("user"),
    userName: attr("user_name"),
    mentionsYou: attr("mentions_you") === "true",
    emoji: attr("emoji"),
    text: files ? m[3].slice(0, files.index) : m[3],
    files: files ? Array.from(files[1].matchAll(/\[([^\]]+)\]/g), (f) => f[1]) : [],
  };
}

const eventCache = new WeakMap<Message, SlackEvent | null>();

/** slackEventOf returns the Slack event a message delivered: a user message
 * from a channel whose text is one slack_* tag. */
export function slackEventOf(message: Message): SlackEvent | null {
  if (message.type !== "user" || !message.external_message_id) return null;
  const cached = eventCache.get(message);
  if (cached !== undefined) return cached;
  let event: SlackEvent | null = null;
  try {
    const llm = llmData(message);
    const content = llm?.Content ?? [];
    if (content.length === 1 && content[0].Type === 2 && content[0].Text) {
      event = parseSlackEvent(content[0].Text);
    }
  } catch {
    // Not a Slack event, then.
  }
  eventCache.set(message, event);
  return event;
}

// The bot is the leading mention of a message that mentions it.
function leadingMention(event: SlackEvent): string | undefined {
  if (event.kind !== "message" || !event.mentionsYou) return undefined;
  return /^<@([UW][A-Z0-9]+)(?:\|[^>]*)?>/.exec(event.text)?.[1];
}

/** What the conversation's Slack events tell about the workspace. */
export interface SlackDirectory extends SlackNames {
  /** Messages, by slackKey(channel, ts). */
  messages: ReadonlyMap<string, SlackEvent>;
}

export const EMPTY_SLACK_DIRECTORY: SlackDirectory = {
  users: new Map(),
  channels: new Map(),
  messages: new Map(),
};

export function slackKey(channel: string, ts: string): string {
  return `${channel}/${ts}`;
}

// A mention Slack labeled with the name it had: <#C1|general>, <@U1|bob>.
const LABELED_MENTION = /<([#@])([CDGUW][A-Z0-9]+)\|([^>]+)>/g;

export function slackDirectory(messages: Message[]): SlackDirectory {
  const users = new Map<string, string>();
  const channels = new Map<string, string>();
  const byKey = new Map<string, SlackEvent>();
  const mentioned: string[] = [];
  for (const m of messages) {
    const event = slackEventOf(m);
    if (!event) continue;
    if (event.userName) users.set(event.user, event.userName);
    if (event.channelName) channels.set(event.channel, event.channelName);
    if (event.kind === "message") byKey.set(slackKey(event.channel, event.ts), event);
    const lead = leadingMention(event);
    if (lead) mentioned.push(lead);
    for (const [, sigil, id, label] of event.text.matchAll(LABELED_MENTION)) {
      if (sigil === "#" && !channels.has(id)) channels.set(id, `#${label}`);
      if (sigil === "@" && !users.has(id)) users.set(id, label);
    }
  }
  // Someone who wrote in Slack is not the bot.
  const botID = mentioned.find((id) => !users.has(id));
  return { users, channels, botID, messages: byKey };
}

/** Whether two directories say the same, so that the new one can be dropped
 * and what reads it need not render again. */
export function sameSlackDirectory(a: SlackDirectory, b: SlackDirectory): boolean {
  const same = <V>(x: ReadonlyMap<string, V>, y: ReadonlyMap<string, V>) =>
    x.size === y.size && Array.from(x).every(([k, v]) => y.get(k) === v);
  return (
    a.botID === b.botID &&
    same(a.users, b.users) &&
    same(a.channels, b.channels) &&
    same(a.messages, b.messages)
  );
}

/** The names an event carries, for reading it alone. */
export function slackEventNames(event: SlackEvent): SlackNames {
  return {
    users: new Map(event.userName ? [[event.user, event.userName]] : []),
    channels: new Map(event.channelName ? [[event.channel, event.channelName]] : []),
    botID: leadingMention(event),
  };
}

/** Where a channel is: "#name", "DM", or its ID. */
export function slackPlace(channel: string, names: SlackNames): string {
  return names.channels.get(channel) || channel;
}

/** The label of the button a slack_action clicked. */
export function clickedLabel(text: string): string {
  const m = /^Clicked "((?:[^"\\]|\\.)*)"/.exec(text);
  return m ? unquote(m[1]) : text;
}

/** slackEventSummary is an event on one line. */
export function slackEventSummary(event: SlackEvent, names: SlackNames): string {
  const who = event.userName || event.user;
  switch (event.kind) {
    case "message":
      return `${who}: ${mrkdwnText(event.text, names) || event.files.map(fileName).join(", ")}`;
    case "reaction":
      return `${who} reacted ${slackEmoji(event.emoji)}`;
    case "action":
      return `${who} clicked ${clickedLabel(event.text)}`;
  }
}

/** A file's name, without the suffix an upload gets to be unique. */
export function fileName(path: string): string {
  return path.replace(/^.*\//, "").replace(/_[0-9a-f]{16}(\.[^.]*)?$/, "$1");
}

export function isImagePath(path: string): boolean {
  return /\.(png|jpe?g|gif|webp|svg|avif|bmp)$/i.test(path);
}

/** A Block Kit text object: mrkdwn, or plain text. */
export interface SlackTextView {
  text: string;
  mrkdwn: boolean;
}

export type SlackElementView =
  | { type: "button"; text: string; url: string; style: string; title: string }
  | { type: "image"; url: string; alt: string }
  | { type: "text"; text: SlackTextView }
  | { type: "other"; name: string };

export type SlackBlockView =
  | { type: "header"; text: string }
  | {
      type: "section";
      text?: SlackTextView;
      fields: SlackTextView[];
      accessory?: SlackElementView;
    }
  | { type: "divider" }
  | { type: "context"; elements: SlackElementView[] }
  | { type: "image"; url: string; alt: string; title: string }
  | { type: "actions"; elements: SlackElementView[] }
  | { type: "other"; name: string };

type JSONObject = Record<string, unknown>;
const obj = (v: unknown): JSONObject =>
  typeof v === "object" && v !== null && !Array.isArray(v) ? (v as JSONObject) : {};
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

function textObject(v: unknown): SlackTextView | undefined {
  const o = obj(v);
  return str(o.text) ? { text: str(o.text), mrkdwn: o.type === "mrkdwn" } : undefined;
}

function element(v: unknown): SlackElementView {
  const o = obj(v);
  switch (o.type) {
    case "button": {
      const value = str(o.value) ? `, value ${str(o.value)}` : "";
      return {
        type: "button",
        text: str(obj(o.text).text),
        url: str(o.url),
        style: str(o.style),
        title: str(o.url) || `action_id ${str(o.action_id)}${value}`,
      };
    }
    case "image":
      return { type: "image", url: str(o.image_url), alt: str(o.alt_text) };
    case "mrkdwn":
    case "plain_text":
      return { type: "text", text: textObject(o) ?? { text: "", mrkdwn: false } };
    default:
      return { type: "other", name: str(o.type) || "element" };
  }
}

/** slackBlocks reads Block Kit blocks for a preview; what it doesn't know
 * shows as its type. */
export function slackBlocks(blocks: unknown): SlackBlockView[] {
  return arr(blocks).map((v): SlackBlockView => {
    const b = obj(v);
    switch (b.type) {
      case "header":
        return { type: "header", text: str(obj(b.text).text) };
      case "section":
        return {
          type: "section",
          text: textObject(b.text),
          fields: arr(b.fields).flatMap((f) => textObject(f) ?? []),
          accessory: b.accessory ? element(b.accessory) : undefined,
        };
      case "divider":
        return { type: "divider" };
      case "context":
        return { type: "context", elements: arr(b.elements).map(element) };
      case "image":
        return {
          type: "image",
          url: str(b.image_url),
          alt: str(b.alt_text),
          title: str(obj(b.title).text),
        };
      case "actions":
        return { type: "actions", elements: arr(b.elements).map(element) };
      default:
        return { type: "other", name: str(b.type) || "block" };
    }
  });
}
