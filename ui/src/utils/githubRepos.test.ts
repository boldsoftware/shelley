import assert from "node:assert/strict";
import { test } from "node:test";
import type { ExeAccountIntegration, GithubAccount } from "../services/workspace";
import { en } from "../i18n/en";
import type { TranslationKeys } from "../i18n/types";
import { githubIntegrationFor, repoRows, repoRowStatus } from "./githubRepos";

const t = (key: keyof TranslationKeys) => en[key];

const ig = (name: string, repos: string[], over: Partial<ExeAccountIntegration> = {}) => ({
  name,
  type: "github",
  team: false,
  title: "",
  comment: "",
  repositories: repos,
  attachments: [],
  added: false,
  reaches: false,
  ...over,
});
const accounts: GithubAccount[] = [
  {
    account: "acme",
    github_login: "me",
    error: "",
    repos: [
      { full_name: "acme/web", private: true },
      { full_name: "acme/api", private: false },
    ],
  },
  {
    account: "me",
    github_login: "me",
    error: "",
    repos: [{ full_name: "me/dots", private: false }],
  },
];

test("githubIntegrationFor prefers one reaching the VMs, then added, then personal", () => {
  const team = ig("team", ["acme/web"], { team: true });
  const personal = ig("personal", ["Acme/Web"]);
  const added = ig("added", ["acme/web"], { added: true });
  const reaches = ig("reaches", ["acme/web", "acme/api"], { reaches: true });
  assert.equal(githubIntegrationFor([team, personal], "acme/web")?.name, "personal");
  assert.equal(githubIntegrationFor([team, personal, added], "acme/web")?.name, "added");
  assert.equal(githubIntegrationFor([team, added, reaches], "acme/web")?.name, "reaches");
  assert.equal(
    githubIntegrationFor([ig("p", ["acme/web"], { type: "http-proxy" })], "acme/web"),
    null,
  );
});

test("repoRows lists the project's first, then filters by query and owner", () => {
  const integrations = [ig("acme-api", ["acme/api"], { added: true, reaches: true })];
  const selected = [
    { integration: "acme-api", repository: "acme/api" },
    { integration: "gone", repository: "old/thing" },
  ];
  const rows = repoRows(accounts, integrations, selected, "", "");
  assert.deepEqual(
    rows.map((r) => [r.repository, r.selected, r.integrationName]),
    [
      ["acme/api", true, "acme-api"],
      ["old/thing", true, "gone"],
      ["acme/web", false, ""],
      ["me/dots", false, ""],
    ],
  );
  assert.equal(rows[2].private, true);
  assert.deepEqual(
    repoRows(accounts, [], [], "acme W", "").map((r) => r.repository),
    ["acme/web"],
  );
  assert.deepEqual(
    repoRows(accounts, [], [], "", "ME").map((r) => r.repository),
    ["me/dots"],
  );
});

test("repoRowStatus says what adding does", () => {
  const [row] = repoRows(accounts, [], [], "web", "");
  assert.equal(repoRowStatus(row, t), "No integration yet: adding creates one");
  const [covered] = repoRows(accounts, [ig("web", ["acme/web"])], [], "web", "");
  assert.equal(
    repoRowStatus(covered, t),
    "Integration web: adding attaches it to the project's VMs",
  );
  const [chosen] = repoRows(
    accounts,
    [ig("web", ["acme/web"], { added: true })],
    [{ integration: "web", repository: "acme/web" }],
    "web",
    "",
  );
  assert.equal(repoRowStatus(chosen, t), "Integration web, not on the project's VMs");
});
