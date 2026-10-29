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
 * instructions, a directory, actions, and repositories. A conversation
 * whose profile is a project is in it. */
export interface Project {
  name: string;
  /** Project instructions (AGENTS.md-style), appended to every conversation's prompt. */
  instructions: string;
  cwd: string;
  actions: ProjectAction[];
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

export interface WorkspaceConversation {
  conversation_id: string;
  /** "" for the Assistant. */
  project: string;
  is_assistant: boolean;
  archived: boolean;
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
}

export const workspaceApi = {
  workspace: () => apiJSON<Workspace>("/api/workspace", "Failed to load projects"),
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
  deleteProject: (name: string) =>
    apiJSON<void>(
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
