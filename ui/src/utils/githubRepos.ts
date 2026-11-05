import type { TranslationKeys } from "../i18n/types";
import type { ExeAccountIntegration, GithubAccount, ProjectRepo } from "../services/workspace";

/** A GitHub repository the project editor offers. */
export interface RepoRow {
  repository: string;
  owner: string;
  private: boolean;
  /** The project has it. */
  selected: boolean;
  /** The project's integration for it when selected; else the GitHub
   * integration adding it would use, or null if adding creates one. */
  integration: ExeAccountIntegration | null;
  /** The integration's name (the project's, when selected). */
  integrationName: string;
}

const rank = (ig: ExeAccountIntegration) => (ig.reaches ? 0 : ig.added ? 1 : ig.team ? 3 : 2);

/** The best of the account's GitHub integrations covering repository, as
 * the server picks it: one reaching the project's VMs, then one added,
 * then a personal one. */
export function githubIntegrationFor(
  integrations: ExeAccountIntegration[],
  repository: string,
): ExeAccountIntegration | null {
  const covering = integrations.filter(
    (ig) =>
      ig.type === "github" &&
      ig.repositories.some((r) => r.toLowerCase() === repository.toLowerCase()),
  );
  return covering.sort((a, b) => rank(a) - rank(b))[0] ?? null;
}

/** The repositories of accounts and the project's (selected), matching
 * query and owner ("" for all), the project's first. */
export function repoRows(
  accounts: GithubAccount[],
  integrations: ExeAccountIntegration[],
  selected: ProjectRepo[],
  query: string,
  owner: string,
): RepoRow[] {
  const rows = new Map<string, RepoRow>();
  const add = (repository: string, isPrivate: boolean) => {
    const key = repository.toLowerCase();
    if (rows.has(key)) return;
    const chosen = selected.find((r) => r.repository.toLowerCase() === key);
    const integration = chosen
      ? (integrations.find((ig) => ig.name === chosen.integration) ?? null)
      : githubIntegrationFor(integrations, repository);
    rows.set(key, {
      repository,
      owner: repository.split("/")[0],
      private: isPrivate,
      selected: !!chosen,
      integration,
      integrationName: chosen?.integration ?? integration?.name ?? "",
    });
  };
  for (const account of accounts)
    for (const repo of account.repos) add(repo.full_name, repo.private);
  for (const repo of selected) add(repo.repository, false);
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  return [...rows.values()]
    .filter(
      (row) =>
        (!owner || row.owner.toLowerCase() === owner.toLowerCase()) &&
        terms.every((term) => row.repository.toLowerCase().includes(term)),
    )
    .sort(
      (a, b) => Number(b.selected) - Number(a.selected) || a.repository.localeCompare(b.repository),
    );
}

/** What adding or having row means for the account, in a few words,
 * translated by t. */
export function repoRowStatus(row: RepoRow, t: (key: keyof TranslationKeys) => string): string {
  const ig = row.integration;
  const named = (key: keyof TranslationKeys, name: string) => t(key).replace("{name}", name);
  if (row.selected)
    return named(
      ig && !ig.reaches ? "projectGithubStatusOffVMs" : "projectGithubStatusChosen",
      row.integrationName,
    );
  if (!ig) return t("projectGithubStatusCreates");
  return named(ig.reaches ? "projectGithubStatusReaches" : "projectGithubStatusAttaches", ig.name);
}
