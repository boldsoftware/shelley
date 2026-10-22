import assert from "node:assert/strict";
import { test } from "node:test";
import type { ConversationWithState } from "../types";
import { workingConversationIds } from "./workingConversations";

function conversation(
  id: string,
  parent: string | null = null,
  working = false,
  jobs = 0,
): ConversationWithState {
  return {
    conversation_id: id,
    parent_conversation_id: parent,
    working,
    running_background_jobs: jobs,
  } as ConversationWithState;
}

test("includes foreground work, own jobs, and nested work through idle ancestors", () => {
  const conversations = [
    conversation("grandchild", "idle-child", false, 1),
    conversation("root"),
    conversation("idle-child", "root"),
    conversation("working-child", "root", true),
    conversation("finished-child", "root"),
    conversation("own-job", null, false, 1),
    conversation("own-turn", null, true),
    conversation("unrelated"),
  ];
  assert.deepEqual(
    workingConversationIds(conversations),
    new Set(["root", "idle-child", "grandchild", "working-child", "own-job", "own-turn"]),
  );
});

test("stays working until the last task or subagent finishes", () => {
  const root = conversation("root", null, true, 1);
  const child = conversation("child", "root", true);
  const nested = conversation("nested", "child", false, 2);
  const conversations = [root, child, nested];
  const active = () => workingConversationIds(conversations).has("root");
  assert.equal(active(), true);
  root.working = false;
  assert.equal(active(), true);
  child.working = false;
  assert.equal(active(), true);
  root.running_background_jobs = 0;
  assert.equal(active(), true);
  nested.running_background_jobs = 1;
  assert.equal(active(), true);
  nested.running_background_jobs = 0;
  assert.deepEqual(workingConversationIds(conversations), new Set());
  nested.working = true;
  assert.equal(active(), true);
});
