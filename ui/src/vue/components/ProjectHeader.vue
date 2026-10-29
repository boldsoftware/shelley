<template>
  <section
    v-if="
      (project || (assignable && workspace.data.value?.projects.length)) &&
      !workspace.isAssistant(conversationId)
    "
    class="project-header"
    :aria-label="t('project')"
  >
    <div class="project-header-row">
      <div class="project-picker">
        <i class="pi pi-folder" aria-hidden="true" />
        <label class="sr-only" for="current-conversation-project">{{ t("conversationProject") }}</label>
        <select
          id="current-conversation-project"
          :value="project?.name ?? ''"
          :disabled="moving || !!activeRun || !!starting || busy"
          :title="project ? project.name : t('chooseConversationProject')"
          @change="moveConversation"
        >
          <option v-if="!project" value="" disabled>{{ t("noProject") }}</option>
          <option v-for="item in workspace.data.value?.projects" :key="item.name" :value="item.name">
            {{ item.name }}
          </option>
        </select>
        <button
          v-if="project"
          type="button"
          class="project-icon-button"
          :aria-label="t('editConversationProject')"
          :title="t('editProjectInstructions')"
          @click="workspace.editProject(project)"
        >
          <i class="pi pi-pencil" aria-hidden="true" />
        </button>
      </div>
      <div v-if="project?.actions.length" class="project-header-actions">
        <Button
          v-for="action in project.actions"
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
    </div>
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
    <p v-if="!activeRun && busy && project?.actions.length" class="project-muted project-action-hint">
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
    action.archive_on_success ? t("actionArchivesOnSuccess") : "",
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
    if (id === props.conversationId)
      requestError.value = errorMessage(err);
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
    if (id === props.conversationId)
      requestError.value = errorMessage(err);
  } finally {
    moving.value = false;
  }
}
function retry() {
  void retryOperation?.();
}
</script>
