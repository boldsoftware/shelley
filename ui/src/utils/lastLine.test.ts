import { lastLine } from "./lastLine";

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

run("empty input yields empty string", () => {
  assert(lastLine("") === "", "expected empty");
});

run("returns last non-empty line", () => {
  const got = lastLine("one\r\ntwo\r\n\n  \n");
  assert(got === "two", `got ${JSON.stringify(got)}`);
});

run("truncates long lines with an ellipsis", () => {
  const got = lastLine("x".repeat(500));
  assert(got.length === 121, `got length ${got.length}`);
  assert(got.endsWith("\u2026"), "expected ellipsis");
});

run("honors an explicit maxLen", () => {
  const got = lastLine("abcdefghij", 4);
  assert(got === "abcd\u2026", `got ${JSON.stringify(got)}`);
});

run("a line exactly maxLen long is not truncated", () => {
  const got = lastLine("abcd", 4);
  assert(got === "abcd", `got ${JSON.stringify(got)}`);
});

run("strips ANSI escapes", () => {
  assert(lastLine("\u001b[32mgreen done\u001b[0m") === "green done", "expected stripped text");
});

run("skips lines that are empty once ANSI is stripped", () => {
  const got = lastLine("real output\n\u001b[2K\u001b[1G\n");
  assert(got === "real output", `got ${JSON.stringify(got)}`);
});

run("drops an escape sequence cut off by the end of the stream", () => {
  const csi = lastLine("building \u001b[3");
  assert(csi === "building", `got ${JSON.stringify(csi)}`);
  const osc = lastLine("building \u001b]0;wind");
  assert(osc === "building", `got ${JSON.stringify(osc)}`);
  const trailingSpace = lastLine("building \u001b[3 ");
  assert(trailingSpace === "building", `got ${JSON.stringify(trailingSpace)}`);
});

run("never truncates in the middle of a surrogate pair", () => {
  const got = lastLine("ab\u{1F600}cd", 3);
  assert(got === "ab\u2026", `got ${JSON.stringify(got)}`);
});

run("splits on carriage returns so progress bars show their latest state", () => {
  assert(
    lastLine("50%\r75%\r100%") === "100%",
    `got ${JSON.stringify(lastLine("50%\r75%\r100%"))}`,
  );
});

run("a line of only zero-width characters counts as empty", () => {
  // Each of these survives trim(), so BLANK has to catch them itself.
  for (const invisible of ["\u200b", "\u200c", "\u200d", "\u2060"]) {
    const got = lastLine(`visible line\n${invisible}\n`);
    assert(got === "visible line", `${JSON.stringify(invisible)}: got ${JSON.stringify(got)}`);
  }
});

run("keeps a zero-width joiner that is holding an emoji sequence together", () => {
  const woman = "\u{1F469}\u200D\u{1F4BB}";
  const got = lastLine(`built by ${woman}`);
  assert(got === `built by ${woman}`, `got ${JSON.stringify(got)}`);
});
