import type { LLMMessage, Message } from "../types";

// Each row's llm_data is parsed once and shared. A large conversation carries
// tens of megabytes of it, and coalescing, the TOC, view filters, and every
// mounted Message each read it; parsing it per reader cost several full passes
// on every open. Keyed by the row (a reactive messages list hands out one
// stable proxy per row) and checked against the string itself so a row whose
// llm_data is replaced reparses.
const cache = new WeakMap<Message, { raw: string; parsed: LLMMessage | null }>();

/** The row's parsed llm_data, or null when absent or malformed. Shared: do not
 *  mutate the result. */
export function llmData(message: Message): LLMMessage | null {
  const raw = message.llm_data;
  if (!raw) return null;
  const hit = cache.get(message);
  if (hit && hit.raw === raw) return hit.parsed;
  let parsed: LLMMessage | null = null;
  try {
    parsed = JSON.parse(raw) as LLMMessage;
  } catch (err) {
    console.error("Failed to parse message llm_data:", err);
  }
  cache.set(message, { raw, parsed });
  return parsed;
}
