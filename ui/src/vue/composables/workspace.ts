import { computed, inject, onUnmounted, provide, ref, shallowRef, type InjectionKey } from "vue";
import {
  workspaceApi,
  type ActionRunSummary,
  type Project,
  type Workspace,
} from "../../services/workspace";
import { errorMessage } from "../../utils/errorMessage";
import { workspaceMode } from "../../utils/workspaceMode";
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
  /** The project shown in the main pane in place of the chat, and its tab. */
  const projectView = shallowRef<ProjectView | null>(null);
  /** Whether the drawer shows the row naming a new project. */
  const namingProject = ref(false);
  const projectDeletion = shallowRef<Project | null>(null);
  /** The project the new conversation composer starts its conversation
   * in, "" for none; newConversationTrigger has App open the composer. */
  const composerProject = ref("");
  const newConversationTrigger = ref(0);
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
    projectView,
    namingProject,
    projectDeletion,
    composerProject,
    newConversationTrigger,
    dispose: () => {
      stopped = true;
    },
    /** Shows project in the main pane, on tab. */
    openProject: (project: Project, tab: ProjectTab = "page") => {
      projectView.value = { id: project.id, tab };
    },
    projectById: (id: number) => data.value?.projects.find((project) => project.id === id),
    /** Why the project named current (or "" for a new one) can't be named
     * name, or "". */
    projectNameCheck:
      (current: string) =>
      (name: string): string =>
        projectNameProblem(
          name,
          (data.value?.projects ?? []).map((p) => p.name).filter((other) => other !== current),
        ),
    startNewProject: () => {
      namingProject.value = true;
    },
    /** Whether the user has projects besides Shelley's own, which is built
     * in; true until the workspace loads. */
    hasProjects: computed(
      () =>
        !data.value ||
        data.value.projects.some((project) => project.name !== data.value?.shelley_project),
    ),
    deleteProject: (project: Project) => {
      projectDeletion.value = project;
    },
    /** Opens the new conversation composer in project ("" for none). */
    newProjectConversation: (project = "") => {
      composerProject.value = project;
      newConversationTrigger.value++;
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

/** A project's view's tabs: its web page (with none, its settings show
 * instead) and its settings. */
export type ProjectTab = "page" | "settings";

/** The web page a project's view opens on, or "": pages are framed through
 * workspace mode's /api/page-frame, so stock Shelley shows none. */
export function projectPage(project: Project): string {
  return workspaceMode ? project.page_url : "";
}

export interface ProjectView {
  id: number;
  tab: ProjectTab;
}

/** Where a project's view lives: /project/ID, /project/ID/settings. */
export function projectViewPath(view: ProjectView): string {
  return `/project/${view.id}${view.tab === "settings" ? "/settings" : ""}`;
}

/** The project view at path, or null. */
export function projectViewFromPath(path: string): ProjectView | null {
  const match = /^\/project\/(\d+)(\/settings)?\/?$/.exec(path);
  return match ? { id: Number(match[1]), tab: match[2] ? "settings" : "page" } : null;
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
  const vm = project.vm;
  if (vm?.base && !deletesVMs.includes(vm.base)) keepsVMs.push(`base VM ${vm.base}`);
  if (vm?.shared_vm) keepsVMs.push(`${vm.integration ? "shared VM" : "SSH host"} ${vm.shared_vm}`);
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

/** Why name (trimmed) can't name a project, as the server would refuse it,
 * or "". taken holds the other projects' names. */
export function projectNameProblem(name: string, taken: string[]): string {
  if (!name) return "A name is required.";
  if (new TextEncoder().encode(name).length > 64) return "The name is longer than 64 bytes.";
  // eslint-disable-next-line no-control-regex
  if (/[/\u0000-\u001f\u007f-\u009f]/.test(name))
    return "The name can't contain / or control characters.";
  if (name === "." || name === "..") return "The name can't be . or ..";
  if (taken.includes(name)) return `A project named ${name} already exists.`;
  return "";
}
