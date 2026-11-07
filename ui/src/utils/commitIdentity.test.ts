import assert from "node:assert/strict";
import { test } from "node:test";
import {
  newProjectCommitIdentity,
  prefillCommitIdentity,
  sameCommitIdentity,
} from "./commitIdentity";

const ada = { name: "Ada Lovelace", email: "ada@example.com" };
const none = { name: "", email: "" };

test("prefills only empty fields, from the first suggestions", () => {
  const suggestions = {
    names: ["Ada Lovelace", "Ada"],
    emails: [
      { email: "ada@example.com", source: "github" as const },
      { email: "1+ada@users.noreply.github.com", source: "github_noreply" as const },
    ],
    errors: [],
  };
  assert.deepEqual(prefillCommitIdentity(none, suggestions), ada);
  assert.deepEqual(prefillCommitIdentity({ name: "Countess", email: "" }, suggestions), {
    name: "Countess",
    email: "ada@example.com",
  });
  assert.deepEqual(prefillCommitIdentity(none, { names: [], emails: [], errors: ["x"] }), none);
});

test("compares trimmed", () => {
  assert.ok(sameCommitIdentity(ada, { name: " Ada Lovelace", email: "ada@example.com " }));
});

test("a new project's identity is the default when there is none, else its own if it differs", () => {
  assert.deepEqual(newProjectCommitIdentity(none, ada), { setDefault: true, project: none });
  assert.deepEqual(newProjectCommitIdentity(ada, ada), { setDefault: false, project: none });
  const grace = { name: "Grace Hopper", email: "grace@example.com" };
  assert.deepEqual(newProjectCommitIdentity(ada, grace), { setDefault: false, project: grace });
});
