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
          :disabled="moving || busy"
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
    </div>
    <p v-if="requestError" class="error-line" role="alert">
      {{ requestError }}
      <button type="button" class="text-button" :disabled="busy" @click="retry">
        {{ t("retry") }}
      </button>
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { workspaceApi } from "../../services/workspace";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
// assignable: a top-level user conversation, which may be in no project.
const props = defineProps<{
  conversationId: string | null;
  busy: boolean;
  assignable: boolean;
}>();
const { t } = useI18n();
const workspace = useWorkspace();
const project = computed(() => workspace.projectFor(props.conversationId));
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
