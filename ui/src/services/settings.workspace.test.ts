export {};

// In workspace mode, conversations start in the brief view until the user
// picks one. Run via `pnpm test` (see scripts/run-tests.mjs).

const store = new Map<string, string>();
Object.defineProperty(globalThis, "localStorage", {
  value: {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
  },
  configurable: true,
});
Object.defineProperty(globalThis, "window", {
  value: { __SHELLEY_INIT__: { workspace: true } },
  configurable: true,
});

const settings = await import("./settings");

function check(name: string, got: unknown, want: unknown): void {
  if (got !== want) throw new Error(`${name}: got ${String(got)}, want ${String(want)}`);
  console.log(`\u2713 ${name}`);
}

check("never chose: brief", settings.getConversationViewMode(), "end-of-turn");
check("never chose, message_user: brief", settings.getMessageUserViewMode(), "brief");
settings.setConversationViewMode("all");
settings.setMessageUserViewMode("all");
check("chose all: all", settings.getConversationViewMode(), "all");
check("chose all, message_user: all", settings.getMessageUserViewMode(), "all");
settings.setConversationViewMode("end-of-turn");
check("chose end-of-turn: end-of-turn", settings.getConversationViewMode(), "end-of-turn");
