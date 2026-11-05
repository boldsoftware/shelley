import {
  loadCollapsedGroups,
  loadProjectFolds,
  saveCollapsedGroups,
  saveProjectFolds,
} from "./drawerFolds";

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

function throwingStorage(): Storage {
  const fail = () => {
    throw new Error("storage disabled");
  };
  return { ...fakeStorage(), getItem: fail, setItem: fail };
}

run("project folds round-trip", () => {
  const storage = fakeStorage();
  saveProjectFolds(
    new Map([
      ["project:3", true],
      ["__no_project__", false],
    ]),
    storage,
  );
  const folds = loadProjectFolds(storage);
  assert(folds.size === 2, "both choices kept");
  assert(folds.get("project:3") === true, "folded choice kept");
  assert(folds.get("__no_project__") === false, "unfolded choice kept");
});

run("project folds start empty", () => {
  assert(loadProjectFolds(fakeStorage()).size === 0, "nothing stored");
});

run("bad project folds read as none", () => {
  for (const raw of ["{", "[true]", "null", "7", '"x"']) {
    const storage = fakeStorage({ "shelley-project-folds": raw });
    assert(loadProjectFolds(storage).size === 0, `${raw} reads as none`);
  }
  const mixed = fakeStorage({ "shelley-project-folds": '{"project:1":true,"project:2":"yes"}' });
  const folds = loadProjectFolds(mixed);
  assert(folds.size === 1 && folds.get("project:1") === true, "non-boolean choices dropped");
});

run("collapsed groups round-trip", () => {
  const storage = fakeStorage();
  saveCollapsedGroups(new Set(["/home/a", "/home/b"]), storage);
  const groups = loadCollapsedGroups(storage);
  assert(groups.size === 2 && groups.has("/home/a") && groups.has("/home/b"), "keys kept");
});

run("bad collapsed groups read as none", () => {
  for (const raw of ["[", "{}", "null"]) {
    assert(
      loadCollapsedGroups(fakeStorage({ "shelley-collapsed-groups": raw })).size === 0,
      `${raw} reads as none`,
    );
  }
  const mixed = fakeStorage({ "shelley-collapsed-groups": '["a",1,null]' });
  assert(loadCollapsedGroups(mixed).size === 1, "non-string keys dropped");
});

run("throwing storage reads as none and drops writes", () => {
  const storage = throwingStorage();
  assert(loadProjectFolds(storage).size === 0, "project folds");
  assert(loadCollapsedGroups(storage).size === 0, "collapsed groups");
  saveProjectFolds(new Map([["project:1", true]]), storage);
  saveCollapsedGroups(new Set(["a"]), storage);
});
