import { isImeComposing } from "./imeComposing";

let passed = 0;
let failed = 0;
function assert(cond: boolean, msg: string) {
  if (cond) passed++;
  else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// tsx runs under Node, no DOM, so fake the KeyboardEvent shape we read.
function ev(fields: { isComposing: boolean; keyCode: number }): KeyboardEvent {
  return fields as unknown as KeyboardEvent;
}

// Plain Enter (Chrome/Firefox/Safari, no IME).
assert(!isImeComposing(ev({ isComposing: false, keyCode: 13 })), "plain Enter -> false");

// Mid-composition keydown (all browsers set isComposing).
assert(isImeComposing(ev({ isComposing: true, keyCode: 229 })), "isComposing -> true");

// Safari's Enter confirming an IME conversion: compositionend has already
// fired so isComposing is false, but keyCode is still 229.
assert(isImeComposing(ev({ isComposing: false, keyCode: 229 })), "Safari keyCode 229 -> true");

console.log(`imeComposing: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
