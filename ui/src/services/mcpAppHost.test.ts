import assert from "node:assert/strict";
import { test } from "node:test";
import { AppHost, withPolicy, type AppHostOptions, type Consent } from "./mcpAppHost";

const HEAD = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'">`;
const policed = (html: string) => withPolicy(html, "default-src 'none'");

test("the policy follows only a plain doctype", () => {
  assert.equal(policed("<!DOCTYPE html><p>hi"), `<!DOCTYPE html>${HEAD}<p>hi`);
  assert.equal(policed("\n <!doctype  HTML ><p>hi"), `\n <!doctype  HTML >${HEAD}<p>hi`);
  assert.equal(policed("<p>hi"), `${HEAD}<p>hi`);
});

test("nothing the parser might read as markup goes ahead of the policy", () => {
  for (const html of [
    "<!--x--!><script>evil()</script><!-- --><!doctype html>",
    "<!----><script>evil()</script><!-- --><!doctype html>",
    "\u00a0<!doctype html><script>evil()</script>",
    '<!doctype html public "x"><script>evil()</script>',
  ]) {
    assert.ok(policed(html).startsWith(HEAD), html);
  }
});

test("requests with text the user can't see are refused", async () => {
  let asked = 0;
  const host = new AppHost({} as HTMLIFrameElement, "t", {
    confirm: async () => (asked++, true),
  } as unknown as AppHostOptions);
  const answer = (detail: string) =>
    (host as unknown as { allowed(c: Consent): Promise<boolean> }).allowed({
      kind: "send",
      detail,
    });
  for (const hidden of [
    "a\u200bb",
    "a\u202eb",
    "a\u{e0041}",
    "a\u{e0100}",
    "a\u0007",
    "a\ue000",
    "a\ufe00",
    "a\ufe0f",
    "a\u034f",
    "a\u3164",
    "a\u2800",
    "a\u200d",
    "\u200da",
    "a\u200c\u200cb",
    "L\u200co\u200dok",
    "Looks good\u200e",
    "Looks good\u06d4\u200e",
    "\u06d4\u200c\u06d4",
    "\u05e9\u05dc\u05d5\u05dd\u200e\u200f",
    "a\u00adb",
    "a\u17b4",
    "a\u2065",
    "a\ufff0",
    "a\u{e0002}",
    "a\u{e0080}",
    "👨\u200d\u200d👩",
    "\u{1f3f4}\u{e0067}",
    "\u{1f3f4}\u{e0068}\u{e0069}\u{e007f}",
  ]) {
    await assert.rejects(answer(hidden), /can't see/, JSON.stringify(hidden));
  }
  assert.equal(asked, 0);
  const flag = "\u{1f3f4}\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}";
  const emoji = `👨\u200d👩\u200d👧 ❤\ufe0f 🏳\ufe0f\u200d🌈 1\ufe0f\u20e3 👍🏽 ${flag}`;
  assert.equal(await answer("Looks good\n\tthanks " + emoji), true);
  // Joiners between letters (Persian, Indic) and direction marks.
  const scripts = "می\u200cخواهم क्\u200dष \u05e9\u05dc\u05d5\u05dd\u200e 123\u200f \u061c";
  assert.equal(await answer(scripts), true);
  assert.equal(asked, 2);
});
