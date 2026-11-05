<!-- A task's project row under its title. Stock Shelley: its project
     (picker and editor), the project's action buttons, and its VM. Workspace
     mode, whose title row says where the task runs and whose drawer moves and
     edits projects: only the action buttons and the state of a Shelley build
     the task verified, and no row without them. -->
<template>
  <section
    v-if="
      (workspaceMode
        ? actions.length || build
        : project || (assignable && workspace.data.value?.projects.length)) &&
      !workspace.isAssistant(conversationId)
    "
    class="project-header"
    :aria-label="t('project')"
  >
    <div class="project-header-row">
      <div v-if="!workspaceMode" class="project-picker">
        <i class="pi pi-folder" aria-hidden="true" />
        <label class="sr-only" for="current-conversation-project">{{
          t("conversationProject")
        }}</label>
        <select
          id="current-conversation-project"
          :value="project?.name ?? ''"
          :disabled="moving || !!activeRun || !!starting || busy"
          :title="project ? project.name : t('chooseConversationProject')"
          @change="moveConversation"
        >
          <option v-if="!project" value="" disabled>{{ t("noProject") }}</option>
          <option
            v-for="item in workspace.data.value?.projects"
            :key="item.name"
            :value="item.name"
          >
            {{ item.name }}
          </option>
        </select>
        <button
          v-if="project"
          type="button"
          class="project-icon-button"
          :aria-label="t('editConversationProject')"
          :title="t('editProjectInstructions')"
          @click="workspace.openProject(project, 'settings')"
        >
          <i class="pi pi-pencil" aria-hidden="true" />
        </button>
      </div>
      <div v-if="actions.length" class="project-header-actions">
        <Button
          v-for="action in actions"
          :key="action.id"
          :label="
            starting === action.id
              ? t('startingEllipsis')
              : activeRun?.action_id === action.id
                ? `${action.label}…`
                : action.label
          "
          size="small"
          severity="secondary"
          :loading="starting === action.id || activeRun?.action_id === action.id"
          :disabled="!!starting || !!activeRun || busy || archived"
          :title="actionTitle(action)"
          @click="runAction(action.id)"
        />
      </div>
      <CustomizationStatus
        v-if="customizations && build"
        :state="build"
        :pending-conversation="customizations.pendingConversation.value"
        :unavailable="!!customizations.error.value"
        @restart="customizations.mutate(build, 'restart', 'header')"
        @verify="customizations.mutate(build, 'verify', 'header')"
        @cancel-auto-restart="customizations.mutate(build, 'cancelAutoRestart', 'header')"
      />
    </div>
    <p v-if="customizations && buildError" class="error-line" role="alert">
      {{ buildError }}
      <button type="button" class="text-button" @click="customizations.requestError.value = null">
        {{ t("dismiss") }}
      </button>
    </p>
    <p
      v-if="!workspaceMode && vm && status"
      class="project-vm project-muted"
      :class="`project-status-${status.tone}`"
      role="status"
    >
      <i class="pi pi-server" aria-hidden="true" />
      {{ vmPlace(vm, t) }}
      <span v-if="vm.status === 'deleted'">{{ vm.vm_name }}</span>
      <a
        v-else
        :href="vm.https_url || `https://${vm.vm_name}.exe.xyz/`"
        target="_blank"
        rel="noopener noreferrer"
      >
        {{ vm.vm_name }}
        <span class="sr-only">{{ t("opensInNewTab") }}</span>
      </a>
      · {{ status.label }}
      <template v-if="archiveNote"> · {{ archiveNote }}</template>
    </p>
    <p v-if="!workspaceMode && vm?.error" class="error-line" role="alert">{{ vm.error }}</p>
    <p v-if="requestError" class="error-line" role="alert">
      {{ requestError }}
      <button
        type="button"
        class="text-button"
        :disabled="!!starting || !!activeRun || busy || archived"
        @click="retry"
      >
        {{ t("retry") }}
      </button>
    </p>
    <p v-if="!activeRun && busy && actions.length" class="project-muted project-action-hint">
      {{ t("actionsAfterWorking") }}
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import { workspaceApi, type ProjectAction } from "../../services/workspace";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { workspaceMode } from "../../utils/workspaceMode";
import { vmPlace, vmStatus } from "../../utils/vmStatus";
import { useCustomizations } from "../composables/customizations";
import { notRunning } from "../../services/customizations";
import CustomizationStatus from "./CustomizationStatus.vue";
// assignable: a top-level user conversation, which may be in no project.
const props = defineProps<{
  conversationId: string | null;
  busy: boolean;
  archived: boolean;
  assignable: boolean;
}>();
const { t } = useI18n();
const workspace = useWorkspace();
const project = computed(() => workspace.projectFor(props.conversationId));
const actions = computed(() => project.value?.actions ?? []);
const vm = computed(() => workspace.vmFor(props.conversationId));
const status = computed(() => vm.value && vmStatus(vm.value, t));
// Only workspace mode has Shelley builds.
const customizations = workspaceMode ? useCustomizations() : null;
// A Shelley build this task verified that the server doesn't run yet.
const build = computed(() =>
  customizations?.entries.value
    .filter(notRunning)
    .find((entry) => entry.conversation_id === props.conversationId),
);
// A failed click on this task's build buttons here; the drawer shows its own.
const buildError = computed(() => {
  const failure = customizations?.requestError.value;
  return failure?.place === "header" && failure.conversationId === props.conversationId
    ? failure.message
    : "";
});
const archiveNote = computed(() => workspace.archiveNote(props.conversationId));
const runs = computed(() =>
  (workspace.data.value?.runs ?? [])
    .filter((run) => run.conversation_id === props.conversationId)
    .sort((a, b) => b.started_at.localeCompare(a.started_at)),
);
const activeRun = computed(() => runs.value.find((run) => run.status === "running"));
function actionTitle(action: ProjectAction) {
  return [
    t("runCommand").replace("{command}", action.command),
    action.silent ? t("actionNotReported") : t("actionReportsToAgent"),
    action.archive_on_success ? `${t("actionArchivesOnSuccess")} ${archiveNote.value}` : "",
  ]
    .join(" ")
    .trim();
}
const starting = ref("");
const moving = ref(false);
const requestError = ref("");
let retryOperation: (() => Promise<void>) | null = null;
watch(
  () => props.conversationId,
  () => {
    requestError.value = "";
    retryOperation = null;
  },
);
async function runAction(actionId: string) {
  if (!props.conversationId || starting.value || activeRun.value) return;
  const id = props.conversationId;
  starting.value = actionId;
  requestError.value = "";
  retryOperation = () => runAction(actionId);
  try {
    const run = await workspaceApi.runAction(id, actionId);
    workspace.rememberRun(run);
  } catch (err) {
    if (id === props.conversationId) requestError.value = errorMessage(err);
  } finally {
    starting.value = "";
  }
}
async function moveConversation(event: Event) {
  const select = event.target as HTMLSelectElement;
  const target = select.value;
  // Keep the current project visible until the server accepts the move.
  select.value = project.value?.name ?? "";
  await changeProject(target);
}
async function changeProject(target: string) {
  if (!props.conversationId) return;
  const id = props.conversationId;
  moving.value = true;
  requestError.value = "";
  retryOperation = () => changeProject(target);
  try {
    await workspaceApi.moveConversation(id, target);
    await workspace.refresh();
  } catch (err) {
    if (id === props.conversationId) requestError.value = errorMessage(err);
  } finally {
    moving.value = false;
  }
}
function retry() {
  void retryOperation?.();
}
</script>
