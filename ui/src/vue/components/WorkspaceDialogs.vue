<template>
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
import { workspaceApi } from "../../services/workspace";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { wording } from "../../utils/workspaceMode";

const noun = wording("conversation", "task");

const { t } = useI18n();
const workspace = useWorkspace();
const saving = ref(false);
const deleting = workspace.projectDeletion;
const deletePlan = computed(() => deleting.value && workspace.deletionPlan(deleting.value));
// After deletion: VMs the server found were not Shelley's, so kept.
const deleteNotice = ref<string[] | null>(null);
watch(deleting, () => {
  deleteNotice.value = null;
  deleteError.value = "";
});
const deleteError = ref("");

function closeDelete() {
  if (!saving.value) deleting.value = null;
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
