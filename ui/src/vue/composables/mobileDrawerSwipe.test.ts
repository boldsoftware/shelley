import { JSDOM } from "jsdom";
import { hasHorizontalScrollContainer } from "./mobileDrawerSwipe";

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

const dom = new JSDOM("<main><div id='wide'><code id='target'></code></div></main>");
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
});

const wide = document.querySelector("#wide") as HTMLElement;
const target = document.querySelector("#target") as HTMLElement;

Object.defineProperties(wide, {
  clientWidth: { value: 320, configurable: true },
  scrollWidth: { value: 640, configurable: true },
});

run("detects a horizontally scrollable ancestor", () => {
  wide.style.overflowX = "auto";
  assert(hasHorizontalScrollContainer(target), "wide code content should own the swipe");
});

run("ignores an ancestor whose content fits", () => {
  Object.defineProperty(wide, "scrollWidth", { value: 320, configurable: true });
  assert(
    !hasHorizontalScrollContainer(target),
    "non-overflowing content should allow drawer swipes",
  );
});

run("ignores clipped overflow", () => {
  Object.defineProperty(wide, "scrollWidth", { value: 640, configurable: true });
  wide.style.overflowX = "hidden";
  assert(!hasHorizontalScrollContainer(target), "hidden overflow is not horizontally scrollable");
});

console.log("\nmobileDrawerSwipe tests passed");
