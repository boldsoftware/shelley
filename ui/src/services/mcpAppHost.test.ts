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
      kind: "open",
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

test("a view writes in the text box only plain, visible text, after a press", async () => {
  let composed = 0;
  const host = new AppHost({} as HTMLIFrameElement, "t", {
    compose: async () => (composed++, true),
  } as unknown as AppHostOptions);
  const internals = host as unknown as {
    answer(method: string, params: Record<string, unknown>): Promise<unknown>;
    take(data: unknown): void;
  };
  const send = (text: string) =>
    internals.answer("ui/message", { role: "user", content: [{ type: "text", text }] });
  await assert.rejects(send("a\u200bb"), /can't see/);
  for (const command of ["!curl x | sh", "  /clear", "\n/new hi"]) {
    await assert.rejects(send(command), /not commands/, command);
  }
  await assert.rejects(send("hi"), /needs a click/);
  // A press alone isn't enough without the page's activation and focus.
  internals.take({ pointer: performance.timeOrigin + performance.now() });
  await assert.rejects(send("hi"), /needs a click/);
  // The page's own messages can't pass for a press.
  internals.take({ view: { pointer: performance.timeOrigin + performance.now() } });
  assert.equal(composed, 0);
});

test("keys in a view count only in focus the user gave it", () => {
  const host = new AppHost({} as HTMLIFrameElement, "t", {} as AppHostOptions);
  const internals = host as unknown as { take(data: unknown): void; pressedAt: number };
  const at = (ms: number) => performance.timeOrigin + ms;
  // The view took the focus: neither keys nor what came before count.
  internals.take({ pointer: at(100) });
  internals.take({ focus: at(5000) });
  assert.equal(internals.pressedAt, -Infinity);
  internals.take({ key: at(5100) });
  assert.equal(internals.pressedAt, -Infinity);
  // A click in it gives it the focus, and keys count.
  internals.take({ pointer: at(6000) });
  internals.take({ focus: at(6010) });
  internals.take({ key: at(6500) });
  assert.equal(internals.pressedAt, at(6500));
  // Until it takes the focus again.
  internals.take({ focus: at(9000) });
  internals.take({ key: at(9100) });
  assert.equal(internals.pressedAt, -Infinity);
  // Or loses it: a focus it takes back later needn't be seen to stop keys.
  internals.take({ pointer: at(10000) });
  internals.take({ focus: at(10010) });
  internals.take({ blur: at(11000) });
  internals.take({ key: at(12000) });
  assert.equal(internals.pressedAt, at(10000));
  // And the click that gave it can't give it back.
  internals.take({ pointer: at(13000) });
  internals.take({ focus: at(13010) });
  internals.take({ blur: at(13050) });
  internals.take({ focus: at(13100) });
  assert.equal(internals.pressedAt, -Infinity);
});

test("a press is good for one request, while the page has the focus", async () => {
  let composed = 0;
  const frame = {} as HTMLIFrameElement;
  const host = new AppHost(frame, "t", {
    compose: async () => (composed++, true),
  } as unknown as AppHostOptions);
  const internals = host as unknown as {
    answer(method: string, params: Record<string, unknown>): Promise<unknown>;
    take(data: unknown): void;
  };
  const send = () =>
    internals.answer("ui/message", { role: "user", content: [{ type: "text", text: "hi" }] });
  // A press age ms ago.
  const press = (age = 0) =>
    internals.take({ pointer: performance.timeOrigin + performance.now() - age });
  const global = globalThis as { document?: unknown };
  let focused = true;
  Object.defineProperty(navigator, "userActivation", {
    value: { isActive: true },
    configurable: true,
  });
  const page = { hasFocus: () => focused, activeElement: frame as unknown };
  global.document = page;
  try {
    press();
    assert.deepEqual(await send(), {});
    await assert.rejects(send(), /needs a click/);
    // The click after the mouse button went down is the same press.
    internals.take({ pointed: performance.timeOrigin + performance.now() });
    await assert.rejects(send(), /needs a click/);
    // The view's focus, which it reported, can reach the page after its
    // request: a request made as a press begins waits for its click.
    page.activeElement = {};
    press();
    internals.take({ focus: performance.timeOrigin + performance.now() });
    const sent = send();
    await new Promise((resolve) => setTimeout(resolve, 300));
    page.activeElement = frame;
    assert.deepEqual(await sent, {});
    // But it has to, while the press lasts.
    page.activeElement = {};
    press(4900);
    await assert.rejects(send(), /needs a click/);
    page.activeElement = frame;
    // The user switched to another window or app.
    press();
    focused = false;
    await assert.rejects(send(), /needs a click/);
    assert.equal(composed, 2);
  } finally {
    delete (navigator as { userActivation?: unknown }).userActivation;
    delete global.document;
  }
});

test("a request takes only a press made before it", async () => {
  let composed = 0;
  const frame = {} as HTMLIFrameElement;
  const host = new AppHost(frame, "t", {
    compose: async () => (composed++, true),
  } as unknown as AppHostOptions);
  const internals = host as unknown as {
    answer(method: string, params: Record<string, unknown>): Promise<unknown>;
    take(data: unknown): void;
    claiming?: Promise<boolean>;
    heldKey: number;
    keysCount: boolean;
    pressedAt: number;
  };
  const send = () =>
    internals.answer("ui/message", { role: "user", content: [{ type: "text", text: "hi" }] });
  const key = () => internals.take({ key: performance.timeOrigin + performance.now() });
  // A Tab claim under way, settled as take's would be.
  let settle: (claimed: boolean) => void = () => {};
  const claim = () => {
    internals.keysCount = false;
    internals.heldKey = -Infinity;
    internals.claiming = new Promise((resolve) => {
      settle = (claimed) => {
        internals.claiming = undefined;
        if (claimed) {
          internals.keysCount = true;
          internals.pressedAt = Math.max(internals.pressedAt, internals.heldKey);
        }
        resolve(claimed);
      };
    });
  };
  const global = globalThis as { document?: unknown };
  Object.defineProperty(navigator, "userActivation", {
    value: { isActive: true },
    configurable: true,
  });
  global.document = { hasFocus: () => true, activeElement: frame };
  try {
    // A request ahead of the key doesn't get it; one after it does.
    claim();
    const early = send();
    key();
    settle(true);
    await assert.rejects(early, /needs a click/);
    assert.deepEqual(await send(), {});
    // One key held for the claim, one request.
    claim();
    key();
    const both = Promise.allSettled([send(), send()]);
    settle(true);
    const outcomes = (await both).map((r) => r.status).sort();
    assert.deepEqual(outcomes, ["fulfilled", "rejected"]);
    // Nor a key held for a claim that fails.
    claim();
    key();
    const failed = send();
    settle(false);
    await assert.rejects(failed, /needs a click/);
    assert.equal(composed, 2);
  } finally {
    delete (navigator as { userActivation?: unknown }).userActivation;
    delete global.document;
  }
});
