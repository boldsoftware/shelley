import assert from "node:assert/strict";
import { backgroundOutput } from "./backgroundOutput";

const job = {
  jobId: "1a2b3c4d",
  pgid: 123,
  logPath: "/tmp/shelley-jobs/1a2b3c4d.log",
};
const footer =
  ` background job ${job.jobId} (PGID ${job.pgid}). Log: ${job.logPath}\n` +
  "Its output will arrive as a new message. " +
  `Cancel with \`kill -- -${job.pgid}\`.]`;

assert.equal(backgroundOutput(`[Started as${footer}`, job), "");
assert.equal(
  backgroundOutput(`first\n\nsecond\n\n[Still running after 1m0s; moved to${footer}`, job),
  "first\n\nsecond",
);
assert.equal(
  backgroundOutput(
    `out\n[Still running after 5s with a user message waiting; moved to${footer}`,
    job,
  ),
  "out",
);
assert.equal(
  backgroundOutput(`\u001b[31mearly output\u001b[0m\n\n[Started as${footer}`, job),
  "\u001b[31mearly output\u001b[0m",
);
assert.equal(backgroundOutput(`first\n\n[Started as${footer}`, { ...job, pgid: 124 }), "");
assert.equal(backgroundOutput("unrecognized background tool result", job), "");
// The model's instructions may change wording without leaking into the UI.
assert.equal(
  backgroundOutput(
    `out\n\n[Started as background job ${job.jobId} (PGID ${job.pgid}). Log: ${job.logPath}\nAny guidance at all. Cancel with \`kill -- -${job.pgid}\`.]`,
    job,
  ),
  "out",
);
// Output that merely mentions the header is not mistaken for the trailer.
assert.equal(backgroundOutput(`[Started as${footer}\ntrailing junk`, job), "");
