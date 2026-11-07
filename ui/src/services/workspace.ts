import { apiJSON } from "./api";

export interface ProjectAction {
  id: string;
  label: string;
  command: string;
  archive_on_success: boolean;
  /** Don't send the run's result to the conversation's agent. */
  silent: boolean;
}

/** A project: a profile (named the same, holding its conversations' settings) with
 * instructions, a directory, actions, repositories, and VMs. A conversation
 * whose profile is a project is in it. */
export interface Project {
  /** Assigned on creation, kept across renames and never reused. */
  id: number;
  /** The project's place in the user's order, 0 first; lists show projects in it. */
  position: number;
  name: string;
  /** Project instructions (AGENTS.md-style), appended to every conversation's prompt. */
  instructions: string;
  cwd: string;
  actions: ProjectAction[];
  /** If set, conversations run their tools and actions on exe.dev VMs, or on
   * a machine this host reaches over ssh; null runs them on this host. */
  vm: ProjectVM | null;
  /** GitHub repositories attached through exe.dev GitHub integrations. */
  repos: ProjectRepo[];
  /** The web page the project's view opens on, "" for none. */
  page_url: string;
  /** Who the project's tasks commit as; both "" for the default commit
   * identity (Workspace.commit_identity). */
  commit_identity: CommitIdentity;
}

/** A project as created or saved: the server assigns its id and position,
 * and keeps its page_url and commit_identity when they are left out. */
export type ProjectInput = Omit<Project, "id" | "position" | "page_url" | "commit_identity"> & {
  page_url?: string;
  commit_identity?: CommitIdentity;
};

/** The name and email git commits are made as; both "" for none. */
export interface CommitIdentity {
  name: string;
  email: string;
}

/** Names and emails to offer as a commit identity. */
export interface CommitIdentitySuggestions {
  names: string[];
  /** source: a GitHub account's public email ("github"), its no-reply
   * address ("github_noreply"), or the exe.dev account's ("exe_dev"). */
  emails: { email: string; source: "github" | "github_noreply" | "exe_dev" }[];
  /** Sources that couldn't be read. */
  errors: string[];
}

export interface ProjectRepo {
  integration: string;
  /** OWNER/REPO */
  repository: string;
}

/** A repository this host reaches through an exe.dev GitHub integration. */
export interface GithubRepo extends ProjectRepo {
  team: boolean;
}

/** Where a project's tasks run, as edited, for the exe.dev account views:
 * this host, copies of base, or a shared VM; integration acts as the user
 * ("" is this host's default). */
export interface ProjectPlacement {
  integration: string;
  mode: "host" | "own" | "shared";
  base: string;
  shared_vm: string;
}

/** An integration of the user's exe.dev account. */
export interface ExeAccountIntegration {
  name: string;
  type: string;
  team: boolean;
  /** The catalog or database title, or "". */
  title: string;
  comment: string;
  repositories: string[];
  /** Specs: vm:NAME, tag:NAME, auto:all. */
  attachments: string[];
  /** Attached to the project's tag. */
  added: boolean;
  /** Reaches every VM the project's tasks run on now. */
  reaches: boolean;
}

export interface ExeAccountView {
  integration: string;
  /** The tag that brings integrations to the project's VMs, "" until
   * something is added. */
  tag: string;
  vms: { vm_name: string; found: boolean; tags: string[]; tagged: boolean }[];
  integrations: ExeAccountIntegration[];
  /** Why the account can't be shown (e.g. a restricted key), or "". */
  error: string;
  /** Where to manage integrations on exe.dev, "" where unknown. */
  manage_url: string;
}

export interface GithubAccount {
  account: string;
  github_login: string;
  repos: { full_name: string; private: boolean }[];
  /** Why this account's repositories can't be listed, or "". */
  error: string;
}

/** The exe.dev environment, for the workspace's first-run cards. */
export interface Onboarding {
  /** The number to text this VM's Assistant at, "" where there is none. */
  text_number: string;
  /** A number is connected to this VM. */
  text_connected: boolean;
  /** The exe-dev integration a web app's VM is created through, "" for
   * none, and a VM's hostname, with NAME for its name. */
  exe_dev: string;
  vm_host: string;
}

export type ExeAccountChange =
  | { action: "add" | "remove"; name: string; team: boolean }
  | { action: "tag" }
  | { action: "github"; repository: string };

/** Where a project's conversations run off this host: all on shared_vm if
 * it is set, else each on its own VM, copied from base, or new and empty
 * while base is "" and managed_base_vm is off. */
export interface ProjectVM {
  /** The base each conversation's own VM is copied from; "" with shared_vm. */
  base: string;
  /** An agent builds base: while it is "", a setup conversation prepares
   * one and new conversations wait. */
  managed_base_vm: boolean;
  /** The existing VM all conversations share; Shelley never creates or
   * deletes it. With integration "", an ssh destination ([user@]host),
   * reached with this host's own SSH configuration and keys. */
  shared_vm: string;
  integration: string;
  /** Read-only: the conversation preparing the base while base is "". */
  setup_conversation_id: string;
}

/** A conversation's VM: its own, or its project's shared VM or SSH host
 * (integration ""). Subagents share their parent's VM. */
export interface ConversationVM {
  conversation_id: string;
  vm_name: string;
  /** "" for a shared VM. */
  base_vm: string;
  integration: string;
  /** A project's shared VM: archiving the conversation leaves it alone. */
  shared: boolean;
  /** Archiving the conversation deletes its VM: deleting (retried on error), then
   * deleted, or kept when it isn't the VM Shelley created. */
  status: "pending" | "ready" | "failed" | "deleting" | "deleted" | "kept";
  error: string;
  ssh_dest: string;
  https_url: string;
  /** The working directory on the VM: its project's when the conversation was
   * created. */
  dir: string;
  conversation_archived: boolean;
  /** A VM of its own the conversation moved away from (changeConversationVM):
   * kept until archiving the conversation deletes it. */
  replaced: boolean;
}

export interface WorkspaceConversation {
  conversation_id: string;
  /** "" for the Assistant. */
  project: string;
  is_assistant: boolean;
  archived: boolean;
}

/** A base VM Shelley created for a project; deleting the project deletes it. */
export interface ProjectBaseVM {
  project: string;
  vm_name: string;
  integration: string;
  status: "creating" | "created";
}

/** What the workspace lists of a run. */
export interface ActionRunSummary {
  id: string;
  conversation_id: string;
  project: string;
  action_id: string;
  label: string;
  status: "running" | "succeeded" | "failed" | "interrupted";
  started_at: string;
  finished_at?: string;
  exit_code?: number;
  report: ReportState;
}

export interface ActionRun extends ActionRunSummary {
  command: string;
  output: string;
  error: string;
  /** Its success archived the conversation. */
  archived: boolean;
}

export type ReportState = "pending" | "sent" | "skipped";

export interface Workspace {
  assistant_id: string;
  projects: Project[];
  conversations: WorkspaceConversation[];
  runs: ActionRunSummary[];
  vms: ConversationVM[];
  base_vms: ProjectBaseVM[];
  /** Whether the user dismissed the Assistant's onboarding card. */
  onboarding_dismissed: boolean;
  /** The default commit identity, both "" for none. */
  commit_identity: CommitIdentity;
}

export const workspaceApi = {
  workspace: () => apiJSON<Workspace>("/api/workspace", "Failed to load projects"),
  /** This host's exe.dev integrations, and the one new projects' VMs use
   * by default ("" when that is ambiguous). */
  exeDevIntegrations: () =>
    apiJSON<{ integrations: string[]; default: string }>(
      "/api/exe-dev-integrations",
      "Failed to load exe.dev integrations",
    ),
  /** connect_url is "" off exe.dev, where there are no GitHub integrations. */
  githubRepos: () =>
    apiJSON<{ repos: GithubRepo[]; connect_url: string }>(
      "/api/github-repos",
      "Failed to load GitHub repositories",
    ),
  /** The exe.dev account's integrations, as they reach the project's VMs. */
  exeAccount: (project: string, placement: ProjectPlacement) =>
    apiJSON<ExeAccountView>(
      `/api/projects/${encodeURIComponent(project)}/exe-account?${new URLSearchParams({ ...placement })}`,
      "Failed to load exe.dev integrations",
    ),
  /** Changes the account as the user clicked; "github" returns the
   * project repository to add. */
  changeExeAccount: (project: string, placement: ProjectPlacement, change: ExeAccountChange) =>
    apiJSON<ProjectRepo | undefined>(
      `/api/projects/${encodeURIComponent(project)}/exe-account`,
      "exe.dev change failed",
      "POST",
      { ...placement, ...change },
    ),
  /** GitHub accounts connected to the exe.dev account, through exe-dev
   * integration ("" for this host's default), with their repositories;
   * none means GitHub isn't connected, setup_url ("" where unknown)
   * connects it. */
  githubAccounts: (integration: string) =>
    apiJSON<{ accounts: GithubAccount[] | null; error: string; setup_url: string }>(
      `/api/github-accounts?${new URLSearchParams({ integration })}`,
      "Failed to load GitHub repositories",
    ),
  /** Sets the default commit identity; both "" removes it. */
  setCommitIdentity: (identity: CommitIdentity) =>
    apiJSON<CommitIdentity>(
      "/api/commit-identity",
      "Failed to save the commit identity",
      "PUT",
      identity,
    ),
  /** Commit identities to offer, from the public profiles of GitHub
   * accounts logins and the exe.dev account. */
  commitIdentitySuggestions: (logins: string[]) =>
    apiJSON<CommitIdentitySuggestions>(
      `/api/commit-identity/suggestions?${new URLSearchParams(logins.map((login) => ["login", login]))}`,
      "Failed to load commit identity suggestions",
    ),
  /** What the first-run cards need from the exe.dev environment. */
  onboarding: () => apiJSON<Onboarding>("/api/onboarding", "Failed to load onboarding"),
  dismissOnboarding: () =>
    apiJSON<void>("/api/onboarding/dismiss", "Failed to dismiss onboarding", "POST"),
  createProject: (project: ProjectInput) =>
    apiJSON<Project>("/api/projects", "Failed to create the project", "POST", project),
  /** Replaces project name's data; a different project.name renames it. */
  updateProject: (name: string, project: ProjectInput) =>
    apiJSON<Project>(
      `/api/projects/${encodeURIComponent(name)}`,
      "Failed to save the project",
      "PUT",
      project,
    ),
  /** Deletes the project and the base VMs Shelley created for it; VMs it
   * found were not its own are kept, with why. */
  deleteProject: (name: string) =>
    apiJSON<{ deleted_vms: string[]; kept_vms: { vm_name: string; reason: string }[] }>(
      `/api/projects/${encodeURIComponent(name)}`,
      "Failed to delete the project",
      "DELETE",
    ),
  /** Puts the projects in order, which names every project once. */
  reorderProjects: (projects: string[]) =>
    apiJSON<void>("/api/project-order", "Failed to reorder the projects", "PUT", { projects }),
  moveConversation: (id: string, project: string) =>
    apiJSON<void>(
      `/api/conversation/${encodeURIComponent(id)}/project`,
      "Failed to move the conversation",
      "PUT",
      { project },
    ),
  /** Moves an idle conversation's tools to vm from its next turn: null for
   * this host, a shared_vm for an existing machine, else a new VM of its own
   * (a copy of base, or empty). dir "" is the machine's default. */
  changeConversationVM: (id: string, vm: ProjectVM | null, dir: string) =>
    apiJSON<{ vm: ConversationVM | null; cwd: string }>(
      `/api/conversation/${encodeURIComponent(id)}/vm`,
      "Failed to change the machine",
      "PUT",
      { vm: vm && { base: vm.base, shared_vm: vm.shared_vm, integration: vm.integration }, dir },
    ),
  run: (id: string) =>
    apiJSON<ActionRun>(
      `/api/action-runs/${encodeURIComponent(id)}`,
      "Failed to load the action run",
    ),
  /** Kills a running command; the run ends interrupted. */
  stopRun: (id: string) =>
    apiJSON<void>(
      `/api/action-runs/${encodeURIComponent(id)}/cancel`,
      "Failed to stop the action",
      "POST",
    ),
  runAction: (conversationId: string, actionId: string) =>
    apiJSON<ActionRun>(
      `/api/conversation/${encodeURIComponent(conversationId)}/actions/${encodeURIComponent(actionId)}`,
      "Failed to start the action",
      "POST",
    ),
};
