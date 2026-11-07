// Walks a conversation's messages into the per-LLM-call usage entries and
// "other" (indirect) usage rows the token cost graph draws. Parses are memoized
// per message_id, so the chat can rerun the walk on every stream update.
import type { LLMContent, Message, Usage } from "../types";
import { isHumanUserMessage } from "./conversationView";
import {
  aggregateOtherUsage,
  type OtherUsageEntry,
  type OtherUsageRow,
  type UsageEntry,
} from "./tokenCostGraph";
import { llmData } from "./llmData";

export interface UsageData {
  entries: UsageEntry[];
  otherRows: OtherUsageRow[];
}

// Content type constants mirror llm/llm.go.
const LLM_TYPE_TEXT = 2;
const LLM_TYPE_TOOL_USE = 5;

// Short excerpt of an agent message for the token cost graph hover readout:
// first text block, or the first tool call when the message is tools-only.
// Cached by message_id: llm_data can be large and messages with usage data
// are complete, so their snippet never changes.
const snippetCache = new Map<string, string>();
function messageSnippet(m: Message): string {
  const cached = snippetCache.get(m.message_id);
  if (cached !== undefined) return cached;
  let snippet = "";
  if (m.llm_data) {
    try {
      const llm = llmData(m);
      const content: LLMContent[] = llm?.Content || [];
      for (const c of content) {
        if (c.Type === LLM_TYPE_TEXT && c.Text?.trim()) {
          snippet = c.Text.trim().slice(0, 100);
          break;
        }
      }
      if (!snippet) {
        for (const c of content) {
          if (c.Type === LLM_TYPE_TOOL_USE && c.ToolName) {
            snippet = c.ToolName;
            break;
          }
        }
      }
    } catch {
      /* ignore malformed llm_data */
    }
  }
  snippetCache.set(m.message_id, snippet);
  return snippet;
}

// Parsed usage_data / other_usage_data, cached by message_id like
// snippetCache above. The chat reruns the walk below on every stream update —
// its messages are replaced wholesale — so re-parsing would be
// O(conversation) JSON.parse per streamed token. The chat drops the caches on
// conversation switch (clearUsageCaches) so they stay bounded by one
// conversation's message count rather than the session's.
//
// Only messages that ALREADY carry the field are cached: a row is written once,
// complete, with its usage (there is no UPDATE ... SET usage_data), so a cached
// parse can't go stale — but caching "field absent" would be a bet on that
// invariant rather than a consequence of it, and would silently ignore usage
// that arrived later for the same message_id. Absent is a cheap early return
// anyway; malformed-but-present is cached so bad JSON is parsed at most once.
const usageParseCache = new Map<string, Usage | null>();
function parseUsage(m: Message): Usage | null {
  if (!m.usage_data) return null;
  const cached = usageParseCache.get(m.message_id);
  if (cached !== undefined) return cached;
  let u: Usage | null = null;
  try {
    u = typeof m.usage_data === "string" ? JSON.parse(m.usage_data) : m.usage_data;
  } catch {
    /* ignore malformed usage */
  }
  usageParseCache.set(m.message_id, u);
  return u;
}

// Shared cache-miss result. Readonly so a caller can't mutate every message's
// "no other usage" answer at once; the cached parses are handed out the same
// way, since callers only ever read them.
const NO_OTHER_USAGE: readonly OtherUsageEntry[] = Object.freeze([]);
const otherUsageParseCache = new Map<string, readonly OtherUsageEntry[]>();
function parseOtherUsage(m: Message): readonly OtherUsageEntry[] {
  if (!m.other_usage_data) return NO_OTHER_USAGE;
  const cached = otherUsageParseCache.get(m.message_id);
  if (cached !== undefined) return cached;
  let entries: readonly OtherUsageEntry[] = NO_OTHER_USAGE;
  try {
    const parsed = JSON.parse(m.other_usage_data);
    if (Array.isArray(parsed)) entries = parsed;
  } catch {
    /* ignore malformed other usage */
  }
  otherUsageParseCache.set(m.message_id, entries);
  return entries;
}

// Per-LLM-call usage entries (in order) for the token cost graph in the
// context usage popup. Includes every generation: the graph shows cumulative
// conversation cost, not just the live context window. All-zero records
// (e.g. error placeholders) are skipped.
//
// The same single walk also collects "other" (indirect) LLM usage —
// compaction summarization, LLM-backed tools, slug generation, … — from any
// message (any type) carrying other_usage_data, aggregated into per-
// (purpose, model, url) rows. Inclusion semantics are identical to
// usage_data: forked copies carry both fields and both are counted.
export function usageData(messages: readonly Message[]): UsageData {
  const out: UsageEntry[] = [];
  const otherEntries: OtherUsageEntry[] = [];
  // A turn starts at the first call, after a human user message, or after an
  // agent message that declared end_of_turn. Tool results also arrive as
  // "user" messages; those don't start turns.
  let nextStartsTurn = true;
  // Timestamp of the message that triggered the pending turn; anchors the
  // first call's duration (created_at only marks call completion).
  let turnStartTs = 0;
  // An in-place compaction rewrote the context since the last call.
  let compacted = false;
  for (const m of messages) {
    otherEntries.push(...parseOtherUsage(m));
    if (m.type === "inplacecompaction") compacted = true;
    if (isHumanUserMessage(m)) {
      nextStartsTurn = true;
      turnStartTs = Date.parse(m.created_at) || 0;
      continue;
    }
    if (m.type !== "agent") continue;
    // end_of_turn doesn't depend on usage; honor it even for agent messages
    // without (or with malformed) usage data. Read it up front, but apply it
    // after this call so the call itself stays in its own turn.
    const endsTurn = !!m.end_of_turn;
    const u = parseUsage(m);
    if (
      u &&
      (u.input_tokens || 0) +
        (u.cache_creation_input_tokens || 0) +
        (u.cache_read_input_tokens || 0) +
        (u.output_tokens || 0) >
        0
    ) {
      out.push({
        ...u,
        snippet: messageSnippet(m),
        generation: m.generation,
        afterCompaction: compacted,
        timestamp: Date.parse(m.created_at) || 0,
        startsTurn: nextStartsTurn,
        turnStartTimestamp: nextStartsTurn && turnStartTs ? turnStartTs : undefined,
      });
      nextStartsTurn = false;
      compacted = false;
    }
    if (endsTurn) {
      nextStartsTurn = true;
      // No anchor until a human message triggers the next turn; anchoring to
      // this agent message would count idle time as active.
      turnStartTs = 0;
    }
  }
  return { entries: out, otherRows: aggregateOtherUsage(otherEntries) };
}

/** Drops the memo caches, e.g. on a conversation switch. */
export function clearUsageCaches(): void {
  snippetCache.clear();
  usageParseCache.clear();
  otherUsageParseCache.clear();
}
