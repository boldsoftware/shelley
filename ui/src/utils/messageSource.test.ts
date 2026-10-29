import assert from "node:assert/strict";
import { messageSource, parseLegacyBackgroundJobNotice, actionReportSource } from "./messageSource";

for (const relationship of ["subagent", "parent"] as const) {
  const data = {
    sender_conversation_id: "sender-conversation",
    sender_slug: relationship === "parent" ? "implement-api" : "backend",
    sender_relationship: relationship,
    Text: "progress",
  };
  const expected = {
    conversationId: data.sender_conversation_id,
    slug: data.sender_slug,
    relationship,
  };
  assert.deepEqual(messageSource(data), expected);
  assert.deepEqual(messageSource(JSON.stringify(data)), expected);
}

assert.deepEqual(
  messageSource({
    sender_conversation_id: "unnamed-parent",
    sender_slug: "",
    sender_relationship: "parent",
  }),
  { conversationId: "unnamed-parent", slug: "", relationship: "parent" },
);

for (const data of [
  null,
  "not json",
  [],
  { sender_slug: "missing-id", sender_relationship: "parent" },
  { sender_conversation_id: "missing-slug", sender_relationship: "subagent" },
  { sender_conversation_id: "id", sender_slug: "slug" },
  { sender_conversation_id: "id", sender_slug: "slug", sender_relationship: "user" },
]) {
  assert.equal(messageSource(data), null);
}

for (const data of [
  { background_job_id: "1a2b3c4d", Text: "Background job 1a2b3c4d finished: exit 0, 2m3s." },
  '{"background_job_id":"1a2b3c4d","Text":"done"}',
]) {
  assert.deepEqual(messageSource(data), { backgroundJobId: "1a2b3c4d" });
}
assert.equal(messageSource({ background_job_id: "" }), null);

const finished = {
  background_job_id: "1a2b3c4d",
  command: "make test",
  exit_code: 2,
  duration: "18s",
  log_path: "/tmp/shelley-jobs/1a2b3c4d.log",
  tail: "FAIL",
  Text: "Background job 1a2b3c4d finished: exit 2, 18s.",
};
assert.deepEqual(messageSource(JSON.stringify(finished)), {
  backgroundJobId: "1a2b3c4d",
  outcome: {
    command: "make test",
    exitCode: 2,
    duration: "18s",
    logPath: "/tmp/shelley-jobs/1a2b3c4d.log",
    tail: "FAIL",
  },
});
const lost: Record<string, unknown> = { ...finished, duration: "" };
delete lost.exit_code;
assert.deepEqual(messageSource(lost), {
  backgroundJobId: "1a2b3c4d",
  outcome: {
    command: "make test",
    exitCode: null,
    duration: "",
    logPath: "/tmp/shelley-jobs/1a2b3c4d.log",
    tail: "FAIL",
  },
});

// Notices recorded before outcomes were structured must render as the same
// bash card, with their output kept verbatim (including newlines).
assert.deepEqual(
  parseLegacyBackgroundJobNotice(
    "Background job 1a2b3c4d finished: exit 0, 4m31s. Log: /tmp/shelley-jobs/1a2b3c4d.log\n" +
      "Command: bin/q --only 59e4fc203f\n\nq: checking\n\nq: passed",
    "1a2b3c4d",
  ),
  {
    command: "bin/q --only 59e4fc203f",
    exitCode: 0,
    duration: "4m31s",
    logPath: "/tmp/shelley-jobs/1a2b3c4d.log",
    tail: "q: checking\n\nq: passed",
  },
);
assert.deepEqual(
  parseLegacyBackgroundJobNotice(
    "Background job 1a2b3c4d lost (host rebooted or killed). Log: /tmp/job.log\nCommand: sleep 100",
    "1a2b3c4d",
  ),
  { command: "sleep 100", exitCode: null, duration: "", logPath: "/tmp/job.log", tail: "" },
);
assert.deepEqual(
  parseLegacyBackgroundJobNotice(
    "Background job 1a2b3c4d lost (host rebooted or killed). Log: /tmp/job.log\nCommand: sleep 100\n\none\ntwo",
    "1a2b3c4d",
  ),
  { command: "sleep 100", exitCode: null, duration: "", logPath: "/tmp/job.log", tail: "one\ntwo" },
);
assert.deepEqual(
  parseLegacyBackgroundJobNotice(
    "Background job 1a2b3c4d finished: exit 7, 1s. Log: /tmp/job.log\nCommand: false",
    "1a2b3c4d",
  ),
  { command: "false", exitCode: 7, duration: "1s", logPath: "/tmp/job.log", tail: "" },
);
assert.deepEqual(
  parseLegacyBackgroundJobNotice(
    "Background job 1a2b3c4d finished: exit 0, 1s. Log: /tmp/a. Log: b/job.log\nCommand: true\n\nok",
    "1a2b3c4d",
  ),
  { command: "true", exitCode: 0, duration: "1s", logPath: "/tmp/a. Log: b/job.log", tail: "ok" },
);
assert.equal(
  parseLegacyBackgroundJobNotice(
    "Background job other finished: exit 0, 1s. Log: /tmp/job.log\nCommand: true",
    "1a2b3c4d",
  ),
  null,
);
assert.equal(
  parseLegacyBackgroundJobNotice(
    "Background job 1a2b3c4d finished: exit 0, 1s. Log: /tmp/job.log\nCommand: true\nnot blank",
    "1a2b3c4d",
  ),
  null,
);
const tail = "first\n\n`literal`\n<script>alert(1)</script>\n\u001b[31mred\u001b[0m";
assert.equal(
  parseLegacyBackgroundJobNotice(
    `Background job 1a2b3c4d finished: exit 0, 2s. Log: /tmp/job.log\nCommand: echo ok\n\n${tail}`,
    "1a2b3c4d",
  )?.tail,
  tail,
);
assert.equal(parseLegacyBackgroundJobNotice("unrecognized notice\nfoo", "1a2b3c4d"), null);

// Project action reports carry their run's snapshot.
assert.deepEqual(
  actionReportSource(
    '{"action_run_id":"r1","action_report":"result","label":"make release","command":"make release","status":"failed","exit_code":1,"Text":"x"}',
  ),
  {
    runId: "r1",
    label: "make release",
    command: "make release",
    status: "failed",
    exitCode: 1,
  },
);
assert.equal(actionReportSource({ action_run_id: "" }), null);
assert.equal(actionReportSource({ action_run_id: "r1" }), null, "only reports");
assert.equal(actionReportSource({ background_job_id: "1" }), null);
