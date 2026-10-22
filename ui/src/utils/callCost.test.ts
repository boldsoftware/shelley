import { callCostParts, callNote, missedBars, readPrompts } from "./callCost";
import { TOKEN_BANDS, type UsageEntry } from "./tokenCostGraph";

let failed = 0;
function assert(cond: boolean, msg: string) {
  if (!cond) {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}
function eq(got: string, want: string, msg: string) {
  assert(got === want, `${msg}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

const opus = { input: 5, output: 25, cache_read: 0.5, cache_write: 6.25 };
const costs = { opus, sonnet: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 } };

const T0 = Date.parse("2026-10-18T00:00:00Z");
const iso = (ms: number) => new Date(ms).toISOString();

function entry(partial: Partial<UsageEntry>): UsageEntry {
  return {
    input_tokens: 0,
    cache_creation_input_tokens: 0,
    cache_read_input_tokens: 0,
    output_tokens: 0,
    cost_usd: 0,
    model: "opus",
    ...partial,
  };
}

// Call i's note, read against the calls before it.
function note(
  entries: UsageEntry[],
  i: number,
  growth?: { label: string; tokens: number },
): string {
  const e = entries[i];
  return callNote(e, readPrompts(entries)[i], costs[e.model as keyof typeof costs], growth);
}

// A steady call: it reads the cached context and caches what's new.
const steady = entry({
  cache_read_input_tokens: 150_000,
  cache_creation_input_tokens: 1_000,
  input_tokens: 2,
  output_tokens: 300,
  start_time: iso(T0),
  end_time: iso(T0 + 5_000),
  timestamp: T0 + 5_000,
});

// Parts: costliest first, empty token types dropped.
{
  const parts = callCostParts(
    entry({
      cache_read_input_tokens: 100_000,
      cache_creation_input_tokens: 0,
      output_tokens: 4_000,
    }),
    opus,
  );
  eq(
    parts.map((p) => TOKEN_BANDS[p.band].label).join(),
    "Output,Cache read",
    "parts are sorted by cost and skip empty types",
  );
  assert(Math.abs(parts[0].usd! - 0.1) < 1e-9, `output cost ${parts[0].usd}`);
  assert(Math.abs(parts[1].usd! - 0.05) < 1e-9, `cache read cost ${parts[1].usd}`);
  assert(parts[1].tokens === 100_000, "parts carry token counts");
}
{
  const parts = callCostParts(
    entry({
      cache_creation_input_tokens: 147_755,
      cache_read_input_tokens: 7_167,
      input_tokens: 2,
    }),
    opus,
  );
  eq(
    parts.map((p) => TOKEN_BANDS[p.band].label).join(),
    "Cache write",
    "parts skip types costing under 1% of the call",
  );
}
// Context-size pricing tiers: callCostParts prices the whole call at the tier
// rate once the prompt crosses the threshold.
{
  const haikuTiered = {
    input: 0.1,
    output: 0.5,
    cache_read: 0.01,
    cache_write: 0.125,
    tiers: [{ context: 100_000, input: 0.5, output: 2.5, cache_read: 0.05, cache_write: 0.625 }],
  };
  // Prompt 60k (< 100k): base rate. Output 50k @ $0.5/M = $0.025.
  const below = callCostParts(entry({ input_tokens: 60_000, output_tokens: 50_000 }), haikuTiered);
  const belowOut = below.find((p) => TOKEN_BANDS[p.band].label === "Output")!;
  assert(Math.abs(belowOut.usd! - 0.025) < 1e-9, `below-threshold output ${belowOut.usd}`);
  // Prompt 150k (> 100k): tier rate. Output 50k @ $2.5/M = $0.125 (5x).
  const above = callCostParts(entry({ input_tokens: 150_000, output_tokens: 50_000 }), haikuTiered);
  const aboveOut = above.find((p) => TOKEN_BANDS[p.band].label === "Output")!;
  assert(Math.abs(aboveOut.usd! - 0.125) < 1e-9, `above-threshold output ${aboveOut.usd}`);
}
{
  const parts = callCostParts(entry({ cache_read_input_tokens: 100, output_tokens: 4_000 }), null);
  eq(
    parts.map((p) => TOKEN_BANDS[p.band].label).join(),
    "Output,Cache read",
    "without pricing, parts are sorted by tokens",
  );
  assert(
    parts.every((p) => p.usd === null),
    "without pricing, parts have no cost",
  );
}

// The cache expired while idle: the whole context is written to it again.
{
  const expired = entry({
    cache_read_input_tokens: 7_167,
    cache_creation_input_tokens: 147_755,
    input_tokens: 2,
    output_tokens: 1_852,
    start_time: iso(T0 + 5_000 + 18 * 60_000),
  });
  eq(
    note([steady, expired], 1),
    "Cache miss, 18m after the previous call: 7k of 155k prompt tokens were still cached.",
    "cache miss after a long gap",
  );
}

// A miss moments after the previous call, and one with nothing cached.
{
  const missed = entry({
    cache_read_input_tokens: 7_167,
    cache_creation_input_tokens: 147_755,
    input_tokens: 2,
    start_time: iso(T0 + 11_000),
  });
  eq(
    note([steady, missed], 1),
    "Cache miss, 6s after the previous call: 7k of 155k prompt tokens were still cached.",
    "cache miss without a gap",
  );
  const cold = entry({ cache_creation_input_tokens: 152_000, start_time: iso(T0 + 11_000) });
  eq(
    note([steady, cold], 1),
    "Cache miss, 6s after the previous call: none of 152k prompt tokens were still cached.",
    "cache miss with nothing cached",
  );
}

// A partial hit is a miss when most of the uncached tokens aren't new, and
// new input counts only what the prompt grew by.
{
  const suffix = entry({
    cache_read_input_tokens: 141_000,
    cache_creation_input_tokens: 15_000,
    start_time: iso(T0 + 11_000),
  });
  eq(
    note([steady, suffix], 1),
    "Cache miss, 6s after the previous call: 141k of 156k prompt tokens were still cached (5k new).",
    "recached suffix bigger than the growth",
  );
  const grew = entry({ cache_read_input_tokens: 146_000, cache_creation_input_tokens: 15_000 });
  eq(note([steady, grew], 1), "10k new prompt tokens.", "new input counts the growth");
  const partial = entry({
    cache_read_input_tokens: 100_000,
    cache_creation_input_tokens: 55_000,
    start_time: iso(T0 + 11_000),
  });
  eq(
    note([steady, partial], 1),
    "Cache miss, 6s after the previous call: 100k of 155k prompt tokens were still cached.",
    "partial hit",
  );
  // Re-sending a little is no miss.
  const nudge = entry({ cache_read_input_tokens: 149_500, cache_creation_input_tokens: 1_600 });
  eq(note([steady, nudge], 1), "", "small resend on a cheap call");
  eq(readPrompts([steady, nudge])[1].kind, "new", "small resend isn't a miss");
}

// The gap runs from the previous call's end (else its message time) to this
// call's start; without that start, a message time would include the call's
// own run, so no gap is claimed.
{
  const prev = entry({ cache_read_input_tokens: 150_000, timestamp: T0 });
  const missed = entry({
    cache_read_input_tokens: 1_000,
    cache_creation_input_tokens: 150_000,
    start_time: iso(T0 + 1_000),
    timestamp: T0 + 8 * 60_000,
  });
  eq(
    note([prev, missed], 1),
    "Cache miss, 1s after the previous call: 1k of 151k prompt tokens were still cached.",
    "gap starts at the previous message time without an end time",
  );
  eq(
    note([prev, { ...missed, start_time: undefined }], 1),
    "Cache miss: 1k of 151k prompt tokens were still cached.",
    "no gap without a start time",
  );
}

// A miss is said even when something else cost more, and without pricing.
{
  const chatty = entry({ cache_creation_input_tokens: 20_000, output_tokens: 30_000 });
  const small = entry({ cache_read_input_tokens: 19_000, cache_creation_input_tokens: 500 });
  eq(
    note([small, chatty], 1),
    "Cache miss: none of 20k prompt tokens were still cached.",
    "miss on an output-heavy call",
  );
  const unpriced = (e: UsageEntry) => ({ ...e, model: "mystery" });
  const entries = [unpriced(small), unpriced(chatty)];
  eq(
    callNote(entries[1], readPrompts(entries)[1], null),
    "Cache miss: none of 20k prompt tokens were still cached.",
    "miss without pricing",
  );
}

// A compaction rewrites the context, so nothing after it is cached yet.
{
  const after = entry({ cache_read_input_tokens: 7_000, cache_creation_input_tokens: 45_000 });
  const want = "First call after compaction: 45k prompt tokens weren't cached yet.";
  eq(note([steady, { ...after, afterCompaction: true }], 1), want, "after in-place compaction");
  eq(note([steady, { ...after, generation: 2 }], 1), want, "first call of a new generation");
}

// The first call, and the first on another model, start with an empty cache,
// though a shared prefix may already be cached.
{
  const first = entry({ cache_read_input_tokens: 9_000, cache_creation_input_tokens: 20_000 });
  eq(note([first], 0), "First call: 20k prompt tokens weren't cached yet.", "first call");
  const switched = entry({ model: "sonnet", cache_creation_input_tokens: 151_000 });
  eq(
    note([steady, switched], 1),
    "First call on sonnet: 151k prompt tokens weren't cached yet.",
    "model switch",
  );
  // Switching back reads against what this model cached before.
  const back = (read: number) =>
    entry({ cache_read_input_tokens: read, cache_creation_input_tokens: 160_000 - read });
  eq(
    note([steady, switched, back(145_000)], 2, { label: "text", tokens: 9_000 }),
    "9k new prompt tokens.",
    "switch back to a warm cache, without the previous call's growth",
  );
  eq(
    note([steady, switched, back(0)], 2),
    "Cache miss on switching back to opus: none of 160k prompt tokens were still cached.",
    "switch back to a cold cache",
  );
  // ... but not against calls from before a compaction.
  eq(
    readPrompts([steady, switched, { ...back(0), afterCompaction: true }, switched])[3].kind,
    "switch",
    "a compaction forgets what each model cached",
  );
  // OpenAI reports no cache writes, but its cache reads show it caches.
  const openai = (input: number, read: number) =>
    entry({ input_tokens: input, cache_read_input_tokens: read });
  eq(
    note([openai(30_000, 0), openai(2_000, 30_000)], 0),
    "First call: 30k prompt tokens weren't cached yet.",
    "first call on a model that reports only reads",
  );
  const missed = entry({ cache_read_input_tokens: 1_000, cache_creation_input_tokens: 151_000 });
  eq(
    note([{ ...steady, model: undefined }, missed], 1),
    "Cache miss: 1k of 152k prompt tokens were still cached.",
    "no model switch without a previous model",
  );
}

// Without a name, a call isn't a switch, and reads against the call before.
{
  const back = entry({ model: undefined, cache_read_input_tokens: 150_000 });
  eq(readPrompts([steady, back])[1].kind, "new", "unnamed model isn't a switch");
  eq(
    note(
      [steady, { ...back, cache_read_input_tokens: 0, cache_creation_input_tokens: 150_000 }],
      1,
    ),
    "Cache miss: none of 150k prompt tokens were still cached.",
    "unnamed model's miss",
  );
}

// A turn resumed after a pause reports the usage of all its requests, so
// the next prompt looks smaller: only a mostly uncached one is a miss.
{
  const paused = entry({ cache_read_input_tokens: 200_000, cache_creation_input_tokens: 2_000 });
  const hit = entry({ cache_read_input_tokens: 107_000, cache_creation_input_tokens: 6_000 });
  eq(readPrompts([paused, hit])[1].kind, "new", "hit after a paused turn");
  eq(note([paused, hit], 1), "", "hit after a paused turn needs no note");
  const bigResult = entry({
    cache_read_input_tokens: 101_000,
    cache_creation_input_tokens: 40_000,
  });
  eq(note([paused, bigResult], 1), "40k new prompt tokens.", "new input after a paused turn");
  const missed = entry({ cache_read_input_tokens: 3_000, cache_creation_input_tokens: 110_000 });
  eq(
    note([paused, missed], 1),
    "Cache miss: 3k of 113k prompt tokens were still cached.",
    "miss after a paused turn",
  );
}

// A bar is marked when the call it draws missed, not one hidden behind it.
{
  const bar = (call: number, first: number, count: number) => ({
    call,
    first,
    count,
    left: 0,
    right: 0,
  });
  const readings = readPrompts([steady, steady, steady]);
  readings[1] = { kind: "miss", added: 0, gapMs: null, switched: false };
  eq(
    missedBars([bar(0, 0, 1), bar(1, 1, 1), bar(2, 2, 1)], readings)
      .map((b) => b.call)
      .join(),
    "1",
    "ungrouped miss",
  );
  eq(
    missedBars([bar(0, 0, 1), bar(2, 1, 2)], readings)
      .map((b) => b.call)
      .join(),
    "",
    "miss behind a costlier call",
  );
}

// A miss can hide behind growth: a long build expires the cache and adds a
// big result. Both parts are said.
{
  const small = entry({
    cache_read_input_tokens: 29_000,
    cache_creation_input_tokens: 1_000,
    end_time: iso(T0),
  });
  const afterBuild = entry({
    cache_creation_input_tokens: 70_000,
    start_time: iso(T0 + 11 * 60_000),
  });
  eq(
    note([small, afterBuild], 1, { label: "bash · build/test", tokens: 38_000 }),
    "Cache miss, 11m after the previous call: none of 70k prompt tokens were still cached (40k new, mostly bash · build/test).",
    "miss and growth together",
  );
  const partial = entry({ cache_read_input_tokens: 100_000, cache_creation_input_tokens: 111_000 });
  eq(
    note([steady, partial], 1),
    "Cache miss: 100k of 211k prompt tokens were still cached (60k new).",
    "partial miss with more growth than was missed",
  );
}

// Some models never report caching: that's not a miss on every call. Cache
// writes alone show it caches.
{
  const plain = (input: number) => entry({ input_tokens: input, output_tokens: 100 });
  eq(
    note([plain(90_000), plain(102_000)], 1),
    "opus reports no prompt caching.",
    "model without caching",
  );
  const write = (tokens: number) => entry({ cache_creation_input_tokens: tokens });
  eq(
    note([write(150_000), write(155_000)], 1),
    "Cache miss: none of 155k prompt tokens were still cached.",
    "cache writes without reads still mean caching",
  );
}

// A cache hit with a lot of new input: say what it mostly was, if anything.
{
  const big = entry({
    cache_read_input_tokens: 151_000,
    cache_creation_input_tokens: 40_000,
    output_tokens: 200,
  });
  eq(
    note([steady, big], 1, { label: "bash · build/test", tokens: 38_000 }),
    "40k new prompt tokens, mostly bash · build/test.",
    "new input with growth",
  );
  eq(
    note([steady, big], 1, { label: "text", tokens: 3_000 }),
    "40k new prompt tokens.",
    "growth too small to say what the input mostly was",
  );
  eq(note([steady, big], 1), "40k new prompt tokens.", "new input without growth");
}

// When output or re-reading the cached context cost the most, the cost
// split says it all.
{
  const talky = entry({ cache_read_input_tokens: 151_000, output_tokens: 6_459 });
  eq(note([steady, talky], 1), "", "output dominates");
  const reread = entry({ cache_read_input_tokens: 190_000, output_tokens: 100 });
  eq(note([steady, reread], 1), "", "cache reads dominate");
}

// Without pricing, or with free pricing, only a miss is worth a note.
{
  const plain = entry({ model: "mystery", input_tokens: 100 });
  eq(callNote(plain, readPrompts([plain])[0], null), "", "unpriced call");
  const free = { input: 0, output: 0, cache_read: 0, cache_write: 0 };
  eq(callNote(plain, readPrompts([plain])[0], free), "", "free call");
}

if (failed > 0) process.exit(1);
console.log("callCost tests passed");
