<template>
  <Modal
    :is-open="!!workspace.conversationCreator.value"
    :title="t('newConversation')"
    @close="closeCreator"
  >
    <form id="project-conversation-form" class="project-form" @submit.prevent="createConversation">
      <label for="new-conversation-project">{{ t("project") }}</label>
      <select
        id="new-conversation-project"
        v-model="newProjectConversation.project"
        required
        :disabled="creating"
      >
        <option disabled value="">{{ t("chooseProject") }}</option>
        <option
          v-for="project in workspace.data.value?.projects"
          :key="project.name"
          :value="project.name"
        >
          {{ project.name }}
        </option>
      </select>
      <ProjectVMSetup :vm="chosenProject?.vm ?? null" @navigate="closeCreator" />
      <label for="new-conversation-title">
        {{ t("conversationTitle") }} <span class="project-muted">{{ t("optional") }}</span>
      </label>
      <input
        id="new-conversation-title"
        v-model="newProjectConversation.title"
        :placeholder="t('conversationTitlePlaceholder')"
        :disabled="creating"
      />
      <label for="new-conversation-message">{{ t("conversationInstructions") }}</label>
      <textarea
        id="new-conversation-message"
        v-model="newProjectConversation.message"
        required
        rows="6"
        :placeholder="t('conversationInstructionsPlaceholder')"
        :disabled="creating"
      />
      <p v-if="chosenProject?.cwd" class="project-muted">
        {{ t("workingIn") }} <code>{{ chosenProject.cwd }}</code>
      </p>
      <p v-if="creatorError" class="error-line" role="alert">{{ creatorError }}</p>
    </form>
    <template #footer>
      <Button
        :label="t('cancel')"
        text
        severity="secondary"
        :disabled="creating"
        @click="closeCreator"
      />
      <Button
        :label="creating ? t('startingEllipsis') : t('startConversation')"
        type="submit"
        form="project-conversation-form"
        :loading="creating"
        :disabled="
          !newProjectConversation.project ||
          !newProjectConversation.message.trim() ||
          (chosenProject?.vm?.base === '' && !chosenProject.vm.shared_vm)
        "
      />
    </template>
  </Modal>

  <Modal
    :is-open="!!workspace.archiveConfirmation.value"
    :title="`Archive ${noun}?`"
    @close="workspace.answerArchive(false)"
  >
    <p>{{ workspace.archiveConfirmation.value?.note }}</p>
    <template #footer>
      <Button
        :label="t('cancel')"
        text
        severity="secondary"
        @click="workspace.answerArchive(false)"
      />
      <Button
        label="Archive and delete VM"
        severity="danger"
        data-testid="archive-confirm"
        @click="workspace.answerArchive(true)"
      />
    </template>
  </Modal>

  <Modal :is-open="!!deleting" :title="t('deleteProjectTitle')" @close="closeDelete">
    <template v-if="deleteNotice">
      <p>
        Deleted <strong>{{ deleting?.name }}</strong
        >.
      </p>
      <p v-for="note in deleteNotice" :key="note" class="error-line" role="alert">{{ note }}</p>
    </template>
    <template v-else-if="deletePlan">
      <p>{{ t("deleteProjectConfirm").replace("{name}", deleting?.name ?? "") }}</p>
      <p v-if="deletePlan.blocker" class="error-line" role="alert">{{ deletePlan.blocker }}.</p>
      <ul v-else class="project-delete-effects" data-testid="project-delete-effects">
        <li v-if="deletePlan.setupId">
          Stops its base VM setup {{ noun }} and archives it under No project.
        </li>
        <li v-for="vm in deletePlan.deletesVMs" :key="vm">
          <strong>Deletes VM {{ vm }}</strong> (Shelley created it as the base VM).
        </li>
        <li v-for="vm in deletePlan.keepsVMs" :key="vm">
          Keeps {{ vm }} (Shelley didn't create it).
        </li>
        <li v-if="deletePlan.archived">
          {{
            deletePlan.archived === 1
              ? t("archivedConversationMoves")
              : t("archivedConversationsMove").replace("{count}", String(deletePlan.archived))
          }}
        </li>
        <li v-if="deleting?.vm && !deletePlan.deletesVMs.length">Deletes no VMs.</li>
      </ul>
    </template>
    <p v-if="deleteError" class="error-line" role="alert">{{ deleteError }}</p>
    <template #footer>
      <Button
        :label="deleteNotice ? t('closeAction') : t('cancel')"
        text
        severity="secondary"
        :disabled="saving"
        @click="closeDelete"
      />
      <Button
        v-if="!deleteNotice"
        :label="saving ? t('deletingEllipsis') : t('deleteProject')"
        severity="danger"
        :loading="saving"
        :disabled="!!deletePlan?.blocker"
        @click="deleteProject"
      />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ProjectVMSetup from "./ProjectVMSetup.vue";
import { workspaceApi } from "../../services/workspace";
import { api } from "../../services/api";
import type { Conversation } from "../../types";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { wording } from "../../utils/workspaceMode";

const noun = wording("conversation", "task");

const { t } = useI18n();
const emit = defineEmits<{ (e: "created", conversation: Conversation): void }>();
const workspace = useWorkspace();
const saving = ref(false);
const creating = ref(false);
const creatorError = ref("");
const deleting = workspace.projectDeletion;
const deletePlan = computed(() => deleting.value && workspace.deletionPlan(deleting.value));
// After deletion: VMs the server found were not Shelley's, so kept.
const deleteNotice = ref<string[] | null>(null);
watch(deleting, () => {
  deleteNotice.value = null;
  deleteError.value = "";
});
const deleteError = ref("");
const newProjectConversation = ref({ project: "", title: "", message: "" });
const chosenProject = computed(() =>
  workspace.data.value?.projects.find((p) => p.name === newProjectConversation.value.project),
);

watch(workspace.conversationCreator, (creator) => {
  if (!creator) return;
  newProjectConversation.value = { project: creator.project, title: "", message: "" };
  creatorError.value = "";
});
function closeCreator() {
  if (!creating.value) workspace.conversationCreator.value = null;
}
function closeDelete() {
  if (!saving.value) deleting.value = null;
}
async function createConversation() {
  if (creating.value) return;
  creating.value = true;
  creatorError.value = "";
  try {
    const result = await workspaceApi.createConversation({
      ...newProjectConversation.value,
      title: newProjectConversation.value.title.trim(),
      message: newProjectConversation.value.message.trim(),
    });
    // Close after creation, before navigation: never offer a duplicate submission
    // if loading the already-created conversation subsequently fails.
    workspace.conversationCreator.value = null;
    const conversation = await api.getConversationBySlug(result.conversation_id);
    if (!conversation) throw new Error(t("conversationCreatedNotOpened"));
    emit("created", conversation);
    await workspace.refresh();
  } catch (err) {
    if (workspace.conversationCreator.value) creatorError.value = errorMessage(err);
    else workspace.error.value = errorMessage(err);
  } finally {
    creating.value = false;
  }
}
async function deleteProject() {
  if (!deleting.value || saving.value) return;
  saving.value = true;
  deleteError.value = "";
  try {
    const result = await workspaceApi.deleteProject(deleting.value.name);
    await workspace.refresh();
    if (result.kept_vms.length)
      deleteNotice.value = result.kept_vms.map((vm) => `Kept VM ${vm.vm_name}: ${vm.reason}.`);
    else deleting.value = null;
  } catch (err) {
    deleteError.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}
</script>
