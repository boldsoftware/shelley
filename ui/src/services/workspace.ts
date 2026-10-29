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
  name: string;
  /** Project instructions (AGENTS.md-style), appended to every conversation's prompt. */
  instructions: string;
  cwd: string;
  actions: ProjectAction[];
  /** If set, conversations run their tools and actions on exe.dev VMs: each on its
   * own copy of base, or all on shared_vm. */
  vm: ProjectVM | null;
  /** GitHub repositories attached through exe.dev GitHub integrations. */
  repos: ProjectRepo[];
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

export interface ProjectVM {
  /** "" while a setup conversation prepares the base; saving with "" (and no
   * shared_vm) starts one. */
  base: string;
  /** An existing VM all conversations share; Shelley never creates or deletes it. */
  shared_vm: string;
  integration: string;
  /** Read-only: the conversation preparing the base while base is "". */
  setup_conversation_id: string;
}

/** A conversation's exe.dev VM: its own, or its project's shared VM. Subagents
 * share their parent's VM. */
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
  createProject: (project: Project) =>
    apiJSON<Project>("/api/projects", "Failed to create the project", "POST", project),
  /** Replaces project name's data; a different project.name renames it. */
  updateProject: (name: string, project: Project) =>
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
  /** Starts a conversation in project; a title becomes its slug. */
  createConversation: (input: { project: string; title: string; message: string }) =>
    apiJSON<{ conversation_id: string }>(
      "/api/conversations/new",
      "Failed to start the conversation",
      "POST",
      input,
    ),
  moveConversation: (id: string, project: string) =>
    apiJSON<void>(
      `/api/conversation/${encodeURIComponent(id)}/project`,
      "Failed to move the conversation",
      "PUT",
      { project },
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
