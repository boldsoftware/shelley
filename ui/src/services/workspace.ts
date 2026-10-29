import { apiJSON } from "./api";

/** A project: a profile (named the same, holding its conversations' settings) with
 * instructions and a directory. A conversation whose profile is a
 * project is a member of it. */
export interface Project {
  name: string;
  /** Project instructions (AGENTS.md-style), appended to every conversation's prompt. */
  instructions: string;
  cwd: string;
}

export interface WorkspaceConversation {
  conversation_id: string;
  /** "" for the Assistant. */
  project: string;
  is_assistant: boolean;
  archived: boolean;
}

export interface Workspace {
  assistant_id: string;
  projects: Project[];
  conversations: WorkspaceConversation[];
}

export const workspaceApi = {
  workspace: () => apiJSON<Workspace>("/api/workspace", "Failed to load projects"),
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
      {
        project,
      },
    ),
};
