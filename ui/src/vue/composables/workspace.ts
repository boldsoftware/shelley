import { computed, inject, onUnmounted, provide, ref, shallowRef, type InjectionKey } from "vue";
import { workspaceApi, type Project, type Workspace } from "../../services/workspace";
import { errorMessage } from "../../utils/errorMessage";
import { useI18n, type TranslationKeys } from "./i18n";

type Translate = (key: keyof TranslationKeys) => string;

/** State/transport separate from component lifecycle so refresh races are testable. */
export function createWorkspaceState(t: Translate) {
  const data = shallowRef<Workspace | null>(null);
  const projectsByName = computed(
    () => new Map(data.value?.projects.map((project) => [project.name, project])),
  );
  const memberships = computed(
    () => new Map(data.value?.conversations.map((c) => [c.conversation_id, c.project])),
  );
  const error = ref<string | null>(null);
  /** The project being edited, or a new one (project null). */
  const projectEditor = shallowRef<{ project: Project | null } | null>(null);
  const projectDeletion = shallowRef<Project | null>(null);
  const conversationCreator = shallowRef<{ project: string } | null>(null);
  let pending: Promise<void> | null = null;
  let stopped = false;
  let refreshAgain = false;
  let signature = "";

  function refresh(): Promise<void> {
    if (pending) {
      // A mutation may have completed after this request began. Its caller
      // must get a snapshot taken after that mutation, not the in-flight one.
      refreshAgain = true;
      return pending;
    }
    pending = (async () => {
      do {
        refreshAgain = false;
        const next = await workspaceApi.workspace();
        if (stopped) return;
        error.value = null;
        const nextSignature = JSON.stringify(next);
        if (nextSignature === signature) continue;
        data.value = next;
        signature = nextSignature;
      } while (refreshAgain);
    })()
      .catch((err: unknown) => {
        error.value = errorMessage(err);
        throw err;
      })
      .finally(() => {
        pending = null;
      });
    return pending;
  }

  function dispose() {
    stopped = true;
  }

  return {
    data,
    error,
    refresh,
    projectEditor,
    projectDeletion,
    conversationCreator,
    dispose,
    editProject: (project: Project | null = null) => {
      projectEditor.value = { project };
    },
    deleteProject: (project: Project) => {
      projectDeletion.value = project;
    },
    newProjectConversation: (project = "") => {
      conversationCreator.value = { project };
    },
    /** What deleting a project does, or why it can't yet (blocker). */
    deletionPlan: (project: Project) => projectDeletionPlan(data.value, project, t),
    projectFor: (id: string | null | undefined) => {
      const name = id ? memberships.value.get(id) : undefined;
      return name ? projectsByName.value.get(name) : undefined;
    },
    /** Whether profile name is a project's. */
    isProject: (name: string | null | undefined) => !!name && projectsByName.value.has(name),
  };
}

export interface ProjectDeletionPlan {
  /** Why the project can't be deleted yet, or "". */
  blocker: string;
  /** Archived conversations, which move to "No project" with their history. */
  archived: number;
}

export function projectDeletionPlan(
  data: Workspace | null,
  project: Project,
  t: Translate,
): ProjectDeletionPlan {
  const members = data?.conversations.filter((c) => c.project === project.name) ?? [];
  const active = members.filter((c) => !c.archived).length;
  return {
    blocker: !data
      ? t("loading")
      : active === 1
        ? t("projectHasActiveConversation")
        : active
          ? t("projectHasActiveConversations").replace("{count}", String(active))
          : "",
    archived: members.filter((c) => c.archived).length,
  };
}

export type WorkspaceContext = ReturnType<typeof createWorkspaceState>;
export const WorkspaceKey: InjectionKey<WorkspaceContext> = Symbol("workspace");

export function createWorkspace() {
  const workspace = createWorkspaceState(useI18n().t);
  provide(WorkspaceKey, workspace);
  onUnmounted(workspace.dispose);
  return workspace;
}

export function useWorkspace() {
  const workspace = inject(WorkspaceKey);
  if (!workspace) throw new Error("Workspace provider is required");
  return workspace;
}
