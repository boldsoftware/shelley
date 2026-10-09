// Pure helpers for Shelley's Live delegation. GPT-Live emits transcript
// fragments without turn boundaries. Preserve recent speaker-labeled speech
// for both the small task interpreter and Shelley's actual backend message.
import type { LLMContent, Message } from "../types";

const LLM_TYPE_TEXT = 2;
// GPT-Live appends accept up to 500 tokens; stay well under it.
const MAX_COMMENTARY_CHARS = 1200;

export interface TranscriptFragment {
  text: string;
  startMs: number;
  endMs: number;
}

export interface VoiceFragment extends TranscriptFragment {
  speaker: "user" | "assistant";
}

export function assembleSpeech(fragments: TranscriptFragment[]): string {
  return fragments
    .map((f) => f.text)
    .join("")
    .replace(/\s+/g, " ")
    .trim();
}

// A delegation may be created while the user is still talking. The caller
// waits briefly for late transcript deltas, then uses the latest continuous
// request; earlier chat remains available in the separate voice context.
export function delegatedSpeech(fragments: TranscriptFragment[], offsetMs: number): string {
  const last = fragments.at(-1);
  // Do not mistake an old non-delegated greeting for the new task when its
  // transcript has not arrived yet.
  if (!last || last.endMs + 2500 < offsetMs) return "";
  let first = fragments.length - 1;
  while (first > 0) {
    const before = fragments[first - 1];
    const current = fragments[first];
    if (current.startMs - before.endMs > 5000 || last.endMs - before.startMs > 60_000) break;
    first--;
  }
  return assembleSpeech(fragments.slice(first));
}

export function formatVoiceContext(fragments: VoiceFragment[]): string {
  const turns: { speaker: VoiceFragment["speaker"]; text: string; endMs: number }[] = [];
  for (const fragment of fragments) {
    const previous = turns.at(-1);
    if (
      previous &&
      previous.speaker === fragment.speaker &&
      fragment.startMs - previous.endMs <= 3000
    ) {
      previous.text += fragment.text;
      previous.endMs = fragment.endMs;
    } else {
      turns.push({ speaker: fragment.speaker, text: fragment.text, endMs: fragment.endMs });
    }
  }

  // GPT-Live provides fragments, not sentence-end events. Split only on
  // punctuation followed by a pause/space; keep the exact fragment text,
  // including corrections, punctuation and internal whitespace.
  const sentences: { speaker: VoiceFragment["speaker"]; text: string }[] = [];
  for (const turn of turns) {
    let start = 0;
    for (const match of turn.text.matchAll(/[.!?。！？]+(?=\s|$)/gu)) {
      const end = (match.index ?? 0) + match[0].length;
      if (turn.text.slice(start, end).trim()) {
        sentences.push({ speaker: turn.speaker, text: turn.text.slice(start, end) });
      }
      start = end;
    }
    if (turn.text.slice(start).trim()) {
      sentences.push({ speaker: turn.speaker, text: turn.text.slice(start) });
    }
  }
  const line = (sentence: (typeof sentences)[number]) =>
    `${sentence.speaker === "user" ? "USER" : "LIVE"}: ${sentence.text}`;
  const recent = sentences.slice(-6);
  while (recent.length > 1 && recent.map(line).join("\n").length > 5000) recent.shift();
  return recent.map(line).join("\n");
}

// Avoid accidentally feeding Shelley's spoken answer back into Shelley when
// a speaker plays near the microphone. Echo cancellation is also requested
// from the browser; this is a second guard against near-verbatim feedback.
export function isLikelyVoiceEcho(speech: string, liveReply: string): boolean {
  const words = (text: string) => text.toLocaleLowerCase().match(/[\p{L}\p{N}]+/gu) ?? [];
  const input = words(speech);
  const output = words(liveReply);
  if (input.length < 4 || output.length < 4) return false;
  const short = input.length <= output.length ? input : output;
  const long = input.length > output.length ? input : output;
  return long.join(" ").includes(short.join(" ")) && short.join(" ").length >= 20;
}

export function messageText(message: Message): string {
  if (!message.llm_data) return "";
  try {
    const llm =
      typeof message.llm_data === "string" ? JSON.parse(message.llm_data) : message.llm_data;
    const content: LLMContent[] = llm?.Content || [];
    return content
      .filter((c) => c.Type === LLM_TYPE_TEXT && c.Text)
      .map((c) => c.Text)
      .join("\n")
      .trim();
  } catch {
    return "";
  }
}

export function truncateCommentary(text: string): string {
  return text.length <= MAX_COMMENTARY_CHARS ? text : `${text.slice(0, MAX_COMMENTARY_CHARS)}…`;
}
