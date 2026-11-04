import type { ConversationWithState } from "../types";
import { recentProjectGroups } from "./recentProjectDirectories";

function conversation(
  id: string,
  updatedAt: string,
  cwd: string | null,
  overrides: Partial<ConversationWithState> = {},
): ConversationWithState {
  return {
    conversation_id: id,
    slug: id,
    user_initiated: true,
    created_at: updatedAt,
    updated_at: updatedAt,
    cwd,
    archived: false,
    parent_conversation_id: null,
    model: null,
    conversation_options: "{}",
    current_generation: 0,
    agent_working: false,
    tags: "[]",
    is_draft: false,
    draft: "",
    queued_messages: "[]",
    working: false,
    subagent_count: 0,
    max_sequence_id: 1,
    turn_interrupted: false,
    external_conversation_id: null,
    external_endpoint: null,
    next_prompt_suggestion: "",
    next_prompt_suggestion_seq: 0,
    running_background_jobs: 0,
    ...overrides,
  };
}

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function paths(conversations: readonly ConversationWithState[]): string {
  return recentProjectGroups(conversations)
    .flatMap((group) => group.directories)
    .map((directory) => directory.path)
    .join(",");
}

function assertExcluded(
  label: string,
  overrides: Partial<ConversationWithState>,
  cwd: string | null = `/${label}`,
): void {
  assert(
    paths([
      conversation(label, "2026-09-10T12:01:00Z", cwd, overrides),
      conversation("meaningful", "2026-09-10T12:00:00Z", "/meaningful"),
    ]) === "/meaningful",
    `filters ${label}`,
  );
}

const base = "2026-09-10T12:00:00.000Z";

assert(
  paths([
    conversation("older", "2026-09-10T12:00:01.001001Z", "/older"),
    conversation("newer", "2026-09-10T12:00:01.001002Z", "/newer"),
  ]) === "/newer,/older",
  "sorts by exact updated_at, including sub-millisecond precision",
);

assert(
  paths([
    conversation("cwd", base, "/repo/subdir"),
    conversation("root", "2026-09-10T12:00:01Z", "/repo/other", {
      git_repo_root: "/repo",
    }),
  ]) === "/repo,/repo/subdir",
  "normalizes each candidate to git_repo_root when available",
);

assert(
  paths([
    conversation("newest-duplicate", "2026-09-10T12:04:00Z", "/repo/subdir", {
      git_repo_root: "/repo",
    }),
    conversation("older-duplicate", "2026-09-10T12:03:00Z", "/repo/other", {
      git_repo_root: "/repo",
    }),
    conversation("second", "2026-09-10T12:02:00Z", "/second"),
  ]) === "/repo,/second",
  "deduplicates normalized roots while keeping the newest",
);

assert(
  paths(
    Array.from({ length: 14 }, (_, i) =>
      conversation(`c${i}`, `2026-09-10T12:${String(i).padStart(2, "0")}:00Z`, `/d${i}`),
    ),
  ).split(",").length === 12,
  "limits the list to twelve directories",
);

const mixed = recentProjectGroups([
  conversation("plain", "2026-09-10T12:05:00Z", "/plain"),
  conversation("git", "2026-09-10T12:00:00Z", "/repo", { git_repo_root: "/repo" }),
]);
assert(
  mixed.map((group) => group.name).join(",") === "repo,Other",
  "puts non-git directories in a trailing Other group",
);
assert(
  mixed[1]?.directories[0]?.updatedAt === "2026-09-10T12:05:00Z",
  "keeps each directory's last-used time",
);

assertExcluded("drafts", { is_draft: true });
assertExcluded("message-less rows", { max_sequence_id: 0 });
assertExcluded("subagents", { parent_conversation_id: "parent" });
assertExcluded("missing working directories", {}, null);
assertExcluded("blank working directories", {}, "   ");

const linkedWorktree = recentProjectGroups([
  conversation("newest-linked", "2026-09-10T12:01:00Z", "/repo-worktrees/feature/subdir", {
    git_repo_root: "/repo-worktrees/feature",
    git_worktree_root: "/projects/repo",
  }),
  conversation("older-linked", base, "/repo-worktrees/other/subdir", {
    git_repo_root: "/repo-worktrees/other",
    git_worktree_root: "/projects/repo",
  }),
]);
assert(
  linkedWorktree.length === 1 && linkedWorktree[0]?.directories.length === 2,
  "groups linked worktrees that belong to the same project",
);
assert(
  linkedWorktree[0]?.root === "/projects/repo" && linkedWorktree[0]?.name === "repo",
  "labels the group with the shared project root",
);
assert(linkedWorktree[0]?.directories[0]?.name === "feature", "labels each worktree directory");
assert(
  linkedWorktree[0]?.directories[0]?.path === "/repo-worktrees/feature",
  "keeps the newest worktree first",
);

console.log("\nrecentProjectDirectories tests passed");
