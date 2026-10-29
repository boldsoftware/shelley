import { computed, inject, onUnmounted, provide, ref, shallowRef, type InjectionKey } from "vue";
import {
  workspaceApi,
  type ActionRunSummary,
  type Project,
  type Workspace,
} from "../../services/workspace";
import { errorMessage } from "../../utils/errorMessage";
import { useI18n, type TranslationKeys } from "./i18n";

type Translate = (key: keyof TranslationKeys) => string;

/** State/transport separate from component lifecycle so refresh races are testable.
 * The server invalidates the workspace (SSE) when it changes; run cards load
 * their own output. */
export function createWorkspaceState(t: Translate, onRunFinished: (run: ActionRunSummary) => void) {
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
  let revision = 0;
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
        const requestedRevision = revision;
        const next = await workspaceApi.workspace();
        if (stopped) return;
        if (requestedRevision !== revision) {
          refreshAgain = true;
          continue;
        }
        error.value = null;
        const nextSignature = JSON.stringify(next);
        if (nextSignature === signature) continue;
        const previous = data.value;
        data.value = next;
        signature = nextSignature;
        for (const run of next.runs) {
          if (
            run.status !== "running" &&
            previous?.runs.some((old) => old.id === run.id && old.status === "running")
          )
            onRunFinished(run);
        }
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

  function rememberRun(run: ActionRunSummary) {
    if (!data.value) throw new Error("Workspace has not loaded");
    revision++;
    data.value = { ...data.value, runs: [run, ...data.value.runs.filter((r) => r.id !== run.id)] };
    signature = JSON.stringify(data.value);
  }

  return {
    data,
    error,
    refresh,
    rememberRun,
    projectEditor,
    projectDeletion,
    conversationCreator,
    dispose: () => {
      stopped = true;
    },
    editProject: (project: Project | null = null) => {
      projectEditor.value = { project };
    },
    /** Whether the user has projects; true until the workspace loads. */
    hasProjects: computed(() => !data.value || data.value.projects.length > 0),
    deleteProject: (project: Project) => {
      projectDeletion.value = project;
    },
    newProjectConversation: (project = "") => {
      conversationCreator.value = { project };
    },
    isAssistant: (id: string | null | undefined) => !!id && id === data.value?.assistant_id,
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
  const ids = new Set(members.map((c) => c.conversation_id));
  const running = data?.runs.some(
    (run) => run.status === "running" && ids.has(run.conversation_id),
  );
  return {
    blocker: !data
      ? t("loading")
      : active === 1
        ? t("projectHasActiveConversation")
        : active
          ? t("projectHasActiveConversations").replace("{count}", String(active))
          : running
            ? t("waitForRunningAction")
            : "",
    archived: members.filter((c) => c.archived).length,
  };
}

export type WorkspaceContext = ReturnType<typeof createWorkspaceState>;
export const WorkspaceKey: InjectionKey<WorkspaceContext> = Symbol("workspace");

export function createWorkspace(onRunFinished: (run: ActionRunSummary) => void) {
  const workspace = createWorkspaceState(useI18n().t, onRunFinished);
  provide(WorkspaceKey, workspace);
  onUnmounted(workspace.dispose);
  return workspace;
}

export function useWorkspace() {
  const workspace = inject(WorkspaceKey);
  if (!workspace) throw new Error("Workspace provider is required");
  return workspace;
}
