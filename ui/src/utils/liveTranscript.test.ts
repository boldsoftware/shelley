import type { Message } from "../types";
import {
  assembleSpeech,
  delegatedSpeech,
  formatVoiceContext,
  isLikelyVoiceEcho,
  messageText,
  truncateCommentary,
} from "./liveTranscript";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

const f = (text: string, startMs: number, endMs: number) => ({ text, startMs, endMs });

run("assembleSpeech joins the entire utterance", () => {
  const frags = [f("Please ", 1000, 1200), f("fix the ", 1200, 1500), f("tests", 1500, 1900)];
  assert(assembleSpeech(frags) === "Please fix the tests", "joined");
});

run("assembleSpeech keeps words that arrive after a delegation signal", () => {
  const frags = [f("Inspect README", 100, 500), f(" and explain it", 700, 1100)];
  assert(assembleSpeech(frags) === "Inspect README and explain it", "trailing words");
});

run("assembleSpeech preserves spaces while trimming the edges", () => {
  assert(assembleSpeech([f("  one ", 0, 100), f(" two  ", 100, 200)]) === "one two", "spaces");
});

run("assembleSpeech returns empty with no speech", () => {
  assert(assembleSpeech([]) === "", "empty");
});

run("delegatedSpeech uses the latest user request and its late continuation", () => {
  const fragments = [
    f("Hi there.", 100, 900),
    f("Please inspect README", 6000, 6800),
    f(" and summarize it.", 6900, 7800),
  ];
  assert(
    delegatedSpeech(fragments, 6800) === "Please inspect README and summarize it.",
    "late continuation",
  );
  assert(
    delegatedSpeech([f("Hi there.", 100, 900)], 11_000) === "",
    "old small talk is not a task",
  );
});

run("formatVoiceContext preserves who said what without one line per word", () => {
  const fragments = [
    { ...f("Hello", 100, 400), speaker: "user" as const },
    { ...f(" there", 400, 700), speaker: "user" as const },
    { ...f("Hi!", 900, 1200), speaker: "assistant" as const },
    { ...f("Check", 5000, 5300), speaker: "user" as const },
    { ...f(" README", 5300, 5700), speaker: "user" as const },
  ];
  assert(
    formatVoiceContext(fragments) === "USER: Hello there\nLIVE: Hi!\nUSER: Check README",
    "speakers",
  );
});

run("formatVoiceContext keeps the last six spoken sentences without rewriting them", () => {
  const fragments = Array.from({ length: 8 }, (_, i) => ({
    ...f(` sentence ${i}.`, i * 4000, i * 4000 + 500),
    speaker: (i % 2 === 0 ? "user" : "assistant") as "user" | "assistant",
  }));
  const expected = fragments
    .slice(-6)
    .map(({ speaker, text }) => `${speaker === "user" ? "USER" : "LIVE"}: ${text}`)
    .join("\n");
  assert(formatVoiceContext(fragments) === expected, "recent speech and spacing unchanged");
});

run("formatVoiceContext splits spoken sentences, not punctuation in file names", () => {
  const fragments = [
    { ...f("Wait.  I meant README.md, not readme.txt.", 0, 1000), speaker: "user" as const },
    { ...f("Got it!", 1200, 1400), speaker: "assistant" as const },
  ];
  assert(
    formatVoiceContext(fragments) ===
      "USER: Wait.\nUSER:   I meant README.md, not readme.txt.\nLIVE: Got it!",
    "sentence boundaries and exact words",
  );
});

run("isLikelyVoiceEcho blocks spoken playback but not short corrections", () => {
  assert(
    isLikelyVoiceEcho(
      "Shelley is working on that request",
      "Shelley is working on that request now",
    ),
    "echo",
  );
  assert(
    !isLikelyVoiceEcho("No, use the other file", "Shelley is working on that request now"),
    "correction",
  );
  assert(!isLikelyVoiceEcho("Yes", "Yes, I can help with that"), "short acknowledgement");
});

function msg(seq: number, type: string, text: string, endOfTurn = false): Message {
  return {
    message_id: `m${seq}`,
    conversation_id: "c",
    sequence_id: seq,
    type,
    llm_data: JSON.stringify({ Content: [{ ID: "", Type: 2, Text: text }] }),
    created_at: "",
    generation: 1,
    end_of_turn: endOfTurn,
  } as Message;
}

run("messageText reads only visible text from a Shelley message", () => {
  assert(messageText(msg(1, "agent", "All green.", true)) === "All green.", "agent text");
  assert(messageText({ ...msg(2, "agent", ""), llm_data: "not JSON" }) === "", "bad data");
});

run("truncateCommentary bounds what Live receives", () => {
  const out = truncateCommentary("a".repeat(5000));
  assert(out.length <= 1201, "short");
});
