// Convert a Shelley conversation (its raw Message[] history) into a portable
// Markdown document. This runs entirely on the client; it walks the same
// llm_data structure the chat UI renders and flattens it into headings,
// prose, thinking blocks, and fenced tool-call / tool-result sections.
import { Conversation, Message, LLMContent } from "../types";
import { conversationMessageSource } from "./messageSource";

// Content type constants mirror llm/llm.go (see Message.tsx getContentType).
const TYPE_TEXT = 2;
const TYPE_THINKING = 3;
const TYPE_REDACTED_THINKING = 4;
const TYPE_TOOL_USE = 5;
const TYPE_TOOL_RESULT = 6;
const TYPE_SERVER_TOOL_USE = 7;
const TYPE_WEB_SEARCH_TOOL_RESULT = 8;

function parseLLMData(message: Message): { Content?: LLMContent[] } | null {
  if (!message.llm_data) return null;
  try {
    return typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
  } catch {
    return null;
  }
}

function parseUserData(message: Message): Record<string, unknown> | null {
  if (!message.user_data) return null;
  try {
    return typeof message.user_data === "string"
      ? JSON.parse(message.user_data)
      : (message.user_data as Record<string, unknown>);
  } catch {
    return null;
  }
}

// Render an arbitrary tool input/result value as a compact, readable string.
function stringifyValue(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

// Collect the plain text from a list of tool-result content blocks.
function toolResultText(results: LLMContent[] | undefined): string {
  if (!results || results.length === 0) return "";
  return results
    .map((r) => {
      if (r.Text) return r.Text;
      if (r.Type === TYPE_TEXT) return r.Text || "";
      // Image / structured results: note them rather than dumping bytes.
      if (r.MediaType || r.DisplayImageURL) return "[image]";
      return stringifyValue(r);
    })
    .filter(Boolean)
    .join("\n");
}

function fence(body: string, lang = ""): string {
  // Pick a fence long enough to not collide with backticks inside the body.
  let ticks = "```";
  while (body.includes(ticks)) ticks += "`";
  return `${ticks}${lang}\n${body}\n${ticks}`;
}

export interface ConversationMarkdownOptions {
  // Include the agent's hidden "thinking" blocks. On by default.
  includeThinking?: boolean;
  // Include tool *results* (outputs). Tool calls and their inputs are always
  // recorded; this only gates the (often large) output blocks. On by default.
  includeToolOutputs?: boolean;
}

// Build the title/front-matter block for the document.
function header(conversation: Conversation | undefined, messages: Message[]): string {
  const lines: string[] = [];
  const title = conversation?.slug || "Conversation";
  lines.push(`# ${title}`);
  lines.push("");
  const meta: string[] = [];
  if (conversation?.model) meta.push(`**Model:** ${conversation.model}`);
  if (conversation?.cwd) meta.push(`**Directory:** ${conversation.cwd}`);
  const created = conversation?.created_at || messages[0]?.created_at;
  if (created) {
    const d = new Date(created);
    if (!isNaN(d.getTime())) meta.push(`**Started:** ${d.toLocaleString()}`);
  }
  meta.push(`**Exported:** ${new Date().toLocaleString()}`);
  if (meta.length) {
    lines.push(meta.join("  \n"));
    lines.push("");
  }
  return lines.join("\n");
}

// Render a single message's content blocks into markdown sections.
function renderMessage(message: Message, opts: Required<ConversationMarkdownOptions>): string[] {
  const out: string[] = [];

  // Error / warning messages carry their text in user_data or llm_data.
  if (message.type === "error" || message.type === "warning") {
    const ud = parseUserData(message);
    let text = (ud?.text as string) || "";
    if (!text) {
      const llm = parseLLMData(message);
      const tc = llm?.Content?.find((c) => c.Type === TYPE_TEXT);
      text = tc?.Text || (message.type === "error" ? "An error occurred" : "Warning");
    }
    const label = message.type === "error" ? "⚠️ Error" : "⚠️ Warning";
    out.push(`### ${label}`);
    out.push("");
    out.push(`> ${text.split("\n").join("\n> ")}`);
    out.push("");
    return out;
  }

  const llm = parseLLMData(message);
  if (!llm?.Content) return out;

  // Decide a role heading. User messages that carry tool results are
  // rendered under the preceding assistant turn, so skip a heading for them
  // and emit only their tool results.
  const hasToolResult = llm.Content.some((c) => c.Type === TYPE_TOOL_RESULT);
  const isUser = message.type === "user" && !hasToolResult;

  if (isUser) {
    const sender = conversationMessageSource(message.user_data);
    out.push(
      sender
        ? `## ${sender.relationship === "subagent" ? "Subagent" : "Parent conversation"}: ${sender.slug || sender.conversationId}`
        : "## User",
    );
    out.push("");
  } else if (message.type === "agent") {
    // Only add an Assistant heading when there's something visible to show.
    const hasVisible = llm.Content.some(
      (c) =>
        (c.Type === TYPE_TEXT && (c.Text || "").trim()) ||
        (opts.includeThinking && (c.Type === TYPE_THINKING || c.Type === TYPE_REDACTED_THINKING)) ||
        c.Type === TYPE_TOOL_USE ||
        c.Type === TYPE_SERVER_TOOL_USE,
    );
    if (hasVisible) {
      out.push("## Assistant");
      out.push("");
    }
  }

  for (const content of llm.Content) {
    switch (content.Type) {
      case TYPE_TEXT: {
        const text = (content.Text || "").trim();
        if (text) {
          out.push(text);
          out.push("");
        }
        break;
      }
      case TYPE_THINKING:
      case TYPE_REDACTED_THINKING: {
        if (!opts.includeThinking) break;
        const thinking = (content.Thinking || content.Text || "").trim();
        if (thinking) {
          out.push("<details><summary>💭 Thinking</summary>");
          out.push("");
          out.push(thinking);
          out.push("");
          out.push("</details>");
          out.push("");
        }
        break;
      }
      case TYPE_TOOL_USE:
      case TYPE_SERVER_TOOL_USE: {
        // Tool calls and their inputs are always recorded.
        const name = content.ToolName || "tool";
        out.push(`**🛠️ Tool: \`${name}\`**`);
        out.push("");
        const input = stringifyValue(content.ToolInput);
        if (input.trim()) {
          out.push(fence(input, "json"));
          out.push("");
        }
        // Server tool results (e.g. web_search) ride along on the same block.
        break;
      }
      case TYPE_TOOL_RESULT: {
        if (!opts.includeToolOutputs) break;
        const text = toolResultText(content.ToolResult);
        if (text.trim()) {
          const label = content.ToolError ? "Result (error)" : "Result";
          out.push(`<details><summary>${label}</summary>`);
          out.push("");
          out.push(fence(text));
          out.push("");
          out.push("</details>");
          out.push("");
        }
        break;
      }
      case TYPE_WEB_SEARCH_TOOL_RESULT: {
        if (!opts.includeToolOutputs) break;
        const text = toolResultText(content.ToolResult);
        if (text.trim()) {
          out.push("<details><summary>🔍 Search results</summary>");
          out.push("");
          out.push(fence(text));
          out.push("");
          out.push("</details>");
          out.push("");
        }
        break;
      }
      default:
        break;
    }
  }

  return out;
}

// Convert a full conversation history into a Markdown document.
export function conversationToMarkdown(
  conversation: Conversation | undefined,
  messages: Message[],
  options: ConversationMarkdownOptions = {},
): string {
  const opts: Required<ConversationMarkdownOptions> = {
    includeThinking: options.includeThinking ?? true,
    includeToolOutputs: options.includeToolOutputs ?? true,
  };

  const parts: string[] = [header(conversation, messages)];

  for (const message of messages) {
    // Skip distill/system bookkeeping messages; they're UI scaffolding.
    if (message.type === "system") continue;
    const rendered = renderMessage(message, opts);
    if (rendered.length) {
      parts.push(rendered.join("\n"));
    }
  }

  // Collapse runs of blank lines and trim.
  return (
    parts
      .join("\n")
      .replace(/\n{3,}/g, "\n\n")
      .trim() + "\n"
  );
}
