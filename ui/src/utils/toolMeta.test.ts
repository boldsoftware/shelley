import {
  HEADLINE_BUDGET_NARROW,
  HEADLINE_BUDGET_WIDE,
  isAutoExpandTool,
  toolEmoji,
  toolHeadline,
} from "./toolMeta";

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

const bash = (command: string, maxLen?: number) => toolHeadline("bash", { command }, maxLen);

run("strips leading 'cd <dir> &&' prefix", () => {
  assert(
    bash("cd /home/exedev/exe && git status") === "git status",
    bash("cd /home/exedev/exe && git status"),
  );
  assert(
    bash("cd shelley/ui; pnpm run build") === "pnpm run build",
    bash("cd shelley/ui; pnpm run build"),
  );
});

run("strips nested cd prefixes and quoted dirs", () => {
  assert(
    bash('cd "my dir" && cd sub && go test ./...') === "go test ./...",
    bash('cd "my dir" && cd sub && go test ./...'),
  );
});

run("strips leading env assignments", () => {
  assert(bash("FOO=bar go test ./...") === "go test ./...", bash("FOO=bar go test ./..."));
  assert(
    bash('KEY="a b" ANOTHER=1 npm run dev') === "npm run dev",
    bash('KEY="a b" ANOTHER=1 npm run dev'),
  );
});

run("strips wrapper programs (sudo/time/nohup)", () => {
  assert(
    bash("sudo systemctl restart srv") === "systemctl restart srv",
    bash("sudo systemctl restart srv"),
  );
  assert(bash("time make build") === "make build", bash("time make build"));
});

run("surfaces subcommand for git/go/npm/pnpm", () => {
  assert(bash("git diff HEAD~1") === "git diff HEAD~1", bash("git diff HEAD~1"));
  assert(
    bash("go test ./e1e -run TestFoo") === "go test ./e1e",
    bash("go test ./e1e -run TestFoo"),
  );
  assert(bash("npm run dev") === "npm run dev", bash("npm run dev"));
  assert(bash("pnpm run build") === "pnpm run build", bash("pnpm run build"));
});

run("ignores flags when choosing subcommand", () => {
  assert(bash("git -C /repo status") === "git status", bash("git -C /repo status"));
});

run("long --flags are standalone, not value-consuming", () => {
  assert(bash("git --no-pager diff") === "git diff", bash("git --no-pager diff"));
  assert(bash("npm --silent run build") === "npm run build", bash("npm --silent run build"));
  assert(
    bash("kubectl --context=x get pods") === "kubectl get pods",
    bash("kubectl --context=x get pods"),
  );
});

run("surfaces the pattern for search tools", () => {
  assert(
    bash("grep -rn 'toolHeadline' src/") === "grep toolHeadline",
    bash("grep -rn 'toolHeadline' src/"),
  );
  assert(bash('rg "func main"') === "rg func main", bash('rg "func main"'));
});

run("uses basename of a path-like argument", () => {
  assert(
    bash("cat shelley/server/handlers.go") === "cat handlers.go",
    bash("cat shelley/server/handlers.go"),
  );
  assert(
    bash("sed -n '1,20p' src/styles.css") === "sed styles.css",
    bash("sed -n '1,20p' src/styles.css"),
  );
});

run("falls back to program name when no args", () => {
  assert(bash("ls") === "ls", bash("ls"));
  assert(bash("make") === "make", bash("make"));
});

run("skips blank lines and comments", () => {
  assert(bash("# just a comment\ngit status") === "git status", bash("# comment\\ngit status"));
  assert(bash("\n\n  go build ./...") === "go build ./...", bash("blank lines"));
});

run("wider budget surfaces one more subcommand token than narrow", () => {
  // "git show abc123def456..." : narrow keeps just "git show"; wide has
  // room to append the ref. We don't hard-truncate — we surface whole
  // tokens and let CSS ellipsis clip the visual overflow.
  const cmd = "git show 0123456789abcdef0123456789abcdef";
  const narrow = bash(cmd, HEADLINE_BUDGET_NARROW);
  const wide = bash(cmd, HEADLINE_BUDGET_WIDE);
  assert(narrow === "git show", `narrow: ${narrow}`);
  assert(wide === "git show 0123456789abcdef0123456789abcdef", `wide: ${wide}`);
});

run("both budgets keep meaningful whole tokens (no mid-token cut)", () => {
  const cmd = "npm run build";
  assert(bash(cmd, HEADLINE_BUDGET_NARROW) === "npm run build", bash(cmd, HEADLINE_BUDGET_NARROW));
  assert(bash(cmd, HEADLINE_BUDGET_WIDE) === "npm run build", bash(cmd, HEADLINE_BUDGET_WIDE));
});

run("empty command degrades gracefully", () => {
  assert(bash("") === "shell", bash(""));
});

run("emoji unchanged for shell", () => {
  assert(toolEmoji("bash") === "\u{1F6E0}\uFE0F", "wrench");
});

run("umbrella browser tool picks per-family emoji for folded-in actions", () => {
  const cases: Array<[string, string]> = [
    ["emulate_device", "\u{1F4F1}"],
    ["network_enable", "\u{1F4E1}"],
    ["accessibility_tree", "\u{1F333}"],
    ["profile_metrics", "\u{1F4CA}"],
    ["navigate", "\u{1F310}"],
    ["eval", "\u26A1"],
  ];
  for (const [action, emoji] of cases) {
    const got = toolEmoji("browser", { action });
    assert(got === emoji, `browser ${action} -> ${got}, want ${emoji}`);
  }
});

run("llm_one_shot image output stays inline", () => {
  assert(
    isAutoExpandTool("llm_one_shot", {}, { images: [{ url: "/api/read?path=image.png" }] }),
    "image output should auto-expand",
  );
  assert(!isAutoExpandTool("llm_one_shot", {}, { images: [] }), "text-only output stays a pill");
});

console.log("\ntoolMeta tests passed");
