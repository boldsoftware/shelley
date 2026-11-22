import assert from "node:assert/strict";
import { test } from "node:test";
import { FRAGMENT, pageText } from "../../bookmarklet/protocol";
import {
  bookmarkletInserts,
  bookmarkletPage,
  bookmarkletSites,
  initBookmarklet,
} from "./bookmarklet";

// Just enough of a top-level browser window for initBookmarklet, fresh for
// each boot.
class FakeStorage {
  private items = new Map<string, string>();
  getItem(key: string) {
    return this.items.get(key) ?? null;
  }
  setItem(key: string, value: string) {
    this.items.set(key, value);
  }
}
const fakeLocation = { hash: "", pathname: "/new", search: "" };
Object.assign(globalThis, {
  location: fakeLocation,
  history: { state: null, replaceState: () => (fakeLocation.hash = "") },
});

function boot(sites: string | null, page: string | null, hash = "", framed = false) {
  const win = Object.assign(new EventTarget(), { opener: null });
  const local = new FakeStorage();
  const session = new FakeStorage();
  if (sites !== null) local.setItem("shelley-bookmarklet-sites", sites);
  if (page !== null) session.setItem("shelley-bookmarklet-page", page);
  Object.assign(win, { parent: framed ? {} : win });
  Object.assign(globalThis, { window: win, localStorage: local, sessionStorage: session });
  fakeLocation.hash = hash;
  bookmarkletInserts.value = [];
  bookmarkletPage.value = null;
  initBookmarklet();
  return { win, local };
}

const page = { url: "https://a.example/", title: "A", selection: "" };
const hash = `#${FRAGMENT}${encodeURIComponent(JSON.stringify(page))}`;

test("takes the page from the address once", () => {
  boot(null, null, hash);
  assert.equal(fakeLocation.hash, "");
  assert.deepEqual(bookmarkletPage.value, page);
  assert.deepEqual(bookmarkletInserts.value, [pageText(page)]);
  // A reload keeps the page, but doesn't fill it in again.
  boot(null, JSON.stringify(page));
  assert.deepEqual(bookmarkletPage.value, page);
  assert.deepEqual(bookmarkletInserts.value, []);
});

test("takes the page only on /new", () => {
  fakeLocation.pathname = "/c/abc";
  try {
    boot(null, null, hash);
  } finally {
    fakeLocation.pathname = "/new";
  }
  assert.equal(bookmarkletPage.value, null);
  assert.deepEqual(bookmarkletInserts.value, []);
});

test("in a frame, takes nothing", () => {
  boot(null, null, hash, true);
  assert.equal(bookmarkletPage.value, null);
  assert.deepEqual(bookmarkletInserts.value, []);
});

// initBookmarklet runs before the app mounts, so whatever is stored, Shelley
// must still load.
test("boots with bad stored allowed sites", () => {
  for (const sites of ["{", "null", '"x"', "{}"]) {
    assert.doesNotThrow(() => boot(sites, null), sites);
    assert.deepEqual(bookmarkletSites.value, [], sites);
  }
  boot('["https://a.example", 3, null]', null);
  assert.deepEqual(bookmarkletSites.value, ["https://a.example"]);
});

test("boots with a bad stored page", () => {
  for (const page of ["{", "null", "[]", '{"url":"javascript:alert(1)"}']) {
    assert.doesNotThrow(() => boot(null, page), page);
    assert.equal(bookmarkletPage.value, null, page);
  }
});

test("ignores bad allowed sites from other windows", () => {
  const { win, local } = boot('["https://a.example"]', null);
  local.setItem("shelley-bookmarklet-sites", "{");
  const event = Object.assign(new Event("storage"), { key: "shelley-bookmarklet-sites" });
  assert.doesNotThrow(() => win.dispatchEvent(event));
  assert.deepEqual(bookmarkletSites.value, []);
});

test("boots with storage blocked, still taking the page", () => {
  const blocked = () => {
    throw new DOMException("The operation is insecure.", "SecurityError");
  };
  boot(null, null);
  for (const name of ["localStorage", "sessionStorage"]) {
    Object.defineProperty(globalThis, name, { get: blocked, configurable: true });
  }
  try {
    fakeLocation.hash = hash;
    assert.doesNotThrow(() => initBookmarklet());
    assert.deepEqual(bookmarkletSites.value, []);
    assert.deepEqual(bookmarkletPage.value, page);
    assert.deepEqual(bookmarkletInserts.value, [pageText(page)]);
  } finally {
    for (const name of ["localStorage", "sessionStorage"]) {
      Object.defineProperty(globalThis, name, { value: null, writable: true, configurable: true });
    }
  }
});
