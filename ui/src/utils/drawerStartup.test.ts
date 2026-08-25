import { initialDrawerCollapsed, saveDrawerCollapsedPreference } from "./drawerStartup";

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

function fakeStorage(initial: Record<string, string> = {}): Storage {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => void data.set(key, value),
    removeItem: (key: string) => void data.delete(key),
    clear: () => data.clear(),
    key: (index: number) => [...data.keys()][index] ?? null,
    get length() {
      return data.size;
    },
  };
}

run("starts expanded without a saved preference", () => {
  assert(!initialDrawerCollapsed(fakeStorage()), "drawer should start expanded by default");
});

run("a saved collapsed preference keeps the drawer collapsed", () => {
  const storage = fakeStorage();
  saveDrawerCollapsedPreference(true, storage);
  assert(initialDrawerCollapsed(storage), "saved collapsed preference should be honored");
});

run("a saved expanded preference keeps the drawer expanded", () => {
  const storage = fakeStorage();
  saveDrawerCollapsedPreference(false, storage);
  assert(!initialDrawerCollapsed(storage), "saved expanded preference should be honored");
});

run("garbage stored values start expanded", () => {
  const storage = fakeStorage({ "shelley-drawer-collapsed": "maybe" });
  assert(!initialDrawerCollapsed(storage), "invalid stored value should start expanded");
});

run("storage errors start expanded", () => {
  const storage = fakeStorage();
  storage.getItem = () => {
    throw new Error("denied");
  };
  assert(!initialDrawerCollapsed(storage), "throwing storage should start expanded");
});

console.log("\ndrawerStartup tests passed");
