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

  const vmFor = (id: string | null | undefined) =>
    data.value?.vms.find((vm) => vm.conversation_id === id);
  /** What archiving a conversation on its own VM destroys; "" for others. */
  const archiveNote = (id: string | null | undefined) => {
    const vm = vmFor(id);
    return vm && !vm.shared && !vm.conversation_archived && vm.status !== "deleted"
      ? `Archiving deletes VM ${vm.vm_name}; uncommitted work on it is lost.`
      : "";
  };
  /** The archive waiting for the user to confirm deleting its VM. */
  const archiveConfirmation = shallowRef<{
    note: string;
    answer: (confirmed: boolean) => void;
  } | null>(null);
  /** Runs archive, which archives conversation id, once the user confirms
   * deleting its VM if it has one: every archive route in the UI goes through
   * here. */
  async function confirmArchive(id: string, archive: () => Promise<void> | void) {
    const note = archiveNote(id);
    if (note) {
      archiveConfirmation.value?.answer(false);
      const confirmed = await new Promise<boolean>((answer) => {
        archiveConfirmation.value = { note, answer };
      });
      if (!confirmed) return;
    }
    await archive();
  }
  function answerArchive(confirmed: boolean) {
    const pending = archiveConfirmation.value;
    archiveConfirmation.value = null;
    pending?.answer(confirmed);
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
    vmFor,
    archiveNote,
    archiveConfirmation,
    confirmArchive,
    answerArchive,
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
  /** The base VM setup conversation deletion stops and archives, or "". */
  setupId: string;
  /** Archived conversations, which move to "No project" with their history (as
   * does the setup conversation). */
  archived: number;
  /** VMs Shelley created for the project, which deletion deletes. */
  deletesVMs: string[];
  /** The project's VMs Shelley did not create, which it keeps. */
  keepsVMs: string[];
}

export function projectDeletionPlan(
  data: Workspace | null,
  project: Project,
  t: Translate,
): ProjectDeletionPlan {
  const setup = project.vm?.setup_conversation_id ?? "";
  const members = data?.conversations.filter((c) => c.project === project.name) ?? [];
  const active = members.filter((c) => !c.archived && c.conversation_id !== setup).length;
  const ids = new Set(members.map((c) => c.conversation_id));
  const running = data?.runs.some(
    (run) => run.status === "running" && ids.has(run.conversation_id),
  );
  const deletesVMs = (data?.base_vms ?? [])
    .filter((vm) => vm.project === project.name)
    .map((vm) => vm.vm_name);
  const keepsVMs: string[] = [];
  if (project.vm?.base && !deletesVMs.includes(project.vm.base))
    keepsVMs.push(`base VM ${project.vm.base}`);
  if (project.vm?.shared_vm) keepsVMs.push(`shared VM ${project.vm.shared_vm}`);
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
    setupId: members.some((c) => c.conversation_id === setup && !c.archived) ? setup : "",
    archived: members.filter((c) => c.archived).length,
    deletesVMs,
    keepsVMs,
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
