import { ref, watch, type Ref } from "vue";
import {
  workspaceApi,
  type ExeAccountChange,
  type ExeAccountView,
  type ProjectPlacement,
  type ProjectRepo,
} from "../../services/workspace";
import { errorMessage } from "../../utils/errorMessage";

/** The project editor's view of the user's exe.dev account, through the
 * exe-dev integration of where its tasks run (placement, as edited): its
 * integrations and how they reach the project's VMs. Loading reads only;
 * change() makes the one change the user clicked, then reloads. Off
 * exe.dev (no exe-dev integration on this host), available stays false;
 * checked says whether that is known yet. Workspace mode's only.
 * A project not created yet ("") has no view; change() defers adding a
 * GitHub repository until it is, returning it with integration "". */
export function useExeAccount(project: Ref<string>, placement: Ref<ProjectPlacement>) {
  const available = ref(false);
  const checked = ref(false);
  const view = ref<ExeAccountView | null>(null);
  const loading = ref(false);
  const error = ref("");
  let latest = 0;
  async function load() {
    if (!available.value || !project.value) return;
    const request = ++latest;
    loading.value = true;
    try {
      const result = await workspaceApi.exeAccount(project.value, placement.value);
      if (request !== latest) return;
      view.value = result;
      error.value = "";
    } catch (err) {
      if (request === latest) error.value = errorMessage(err);
    } finally {
      if (request === latest) loading.value = false;
    }
  }
  async function change(change: ExeAccountChange): Promise<ProjectRepo | undefined> {
    if (!project.value)
      return change.action === "github"
        ? { integration: "", repository: change.repository }
        : undefined;
    try {
      return await workspaceApi.changeExeAccount(project.value, placement.value, change);
    } finally {
      await load();
    }
  }
  // Typing a VM name reloads once it settles.
  let timer: ReturnType<typeof setTimeout> | undefined;
  watch(
    () => JSON.stringify([project.value, placement.value]),
    () => {
      clearTimeout(timer);
      timer = setTimeout(load, 400);
    },
  );
  void workspaceApi.exeDevIntegrations().then(
    (result) => {
      available.value = result.integrations.length > 0;
      checked.value = true;
      void load();
    },
    (err: unknown) => {
      error.value = errorMessage(err);
      checked.value = true;
    },
  );
  return { available, checked, view, loading, error, load, change };
}

export type ExeAccount = ReturnType<typeof useExeAccount>;

/** Calls reload when the page is shown again, after the user went to
 * exe.dev in another tab: e.g. to connect GitHub. */
export function reloadOnReturn(reload: () => void) {
  const onVisible = () => {
    if (document.visibilityState === "visible") reload();
  };
  document.addEventListener("visibilitychange", onVisible);
  return () => document.removeEventListener("visibilitychange", onVisible);
}
