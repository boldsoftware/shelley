<template>
  <Modal
    :is-open="!!workspace.projectEditor.value"
    :title="workspace.projectEditor.value?.project ? t('editProject') : t('newProject')"
    class-name="modal-wide"
    @close="closeProject"
  >
    <form v-if="draft" id="project-form" class="project-form" @submit.prevent="saveProject">
      <p class="project-muted">{{ t("projectFormIntro") }}</p>
      <label for="project-name">{{ t("projectName") }}</label>
      <input
        id="project-name"
        v-model="draft.name"
        required
        maxlength="64"
        autocomplete="off"
        :disabled="saving"
      />
      <label for="project-instructions">{{ t("projectInstructions") }}</label>
      <textarea
        id="project-instructions"
        v-model="draft.instructions"
        rows="6"
        class="project-command-input"
        placeholder="# Conventions&#10;- Run `make test` before committing."
        :disabled="saving"
      />
      <p class="project-muted">{{ t("projectInstructionsHint") }}</p>
      <label for="project-cwd">{{ t("workingDirectory") }}</label>
      <input
        id="project-cwd"
        v-model="draft.cwd"
        placeholder="/path/to/project"
        :disabled="saving"
      />
      <p class="project-muted">{{ t("projectProfileHint") }}</p>
      <p v-if="projectError" class="error-line" role="alert">{{ projectError }}</p>
    </form>
    <template #footer>
      <Button :label="t('cancel')" text severity="secondary" :disabled="saving" @click="closeProject" />
      <Button
        :label="saving ? t('savingEllipsis') : t('saveProject')"
        type="submit"
        form="project-form"
        :loading="saving"
      />
    </template>
  </Modal>

  <Modal :is-open="!!workspace.conversationCreator.value" :title="t('newConversation')" @close="closeCreator">
    <form id="project-conversation-form" class="project-form" @submit.prevent="createConversation">
      <label for="new-conversation-project">{{ t("project") }}</label>
      <select id="new-conversation-project" v-model="newProjectConversation.project" required :disabled="creating">
        <option disabled value="">{{ t("chooseProject") }}</option>
        <option
          v-for="project in workspace.data.value?.projects"
          :key="project.name"
          :value="project.name"
        >
          {{ project.name }}
        </option>
      </select>
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
      <Button :label="t('cancel')" text severity="secondary" :disabled="creating" @click="closeCreator" />
      <Button
        :label="creating ? t('startingEllipsis') : t('startConversation')"
        type="submit"
        form="project-conversation-form"
        :loading="creating"
        :disabled="!newProjectConversation.project || !newProjectConversation.message.trim()"
      />
    </template>
  </Modal>

  <Modal :is-open="!!deleting" :title="t('deleteProjectTitle')" @close="closeDelete">
    <template v-if="deletePlan">
      <p>{{ t("deleteProjectConfirm").replace("{name}", deleting?.name ?? "") }}</p>
      <p v-if="deletePlan.blocker" class="error-line" role="alert">{{ deletePlan.blocker }}.</p>
      <ul v-else class="project-delete-effects" data-testid="project-delete-effects">
        <li v-if="deletePlan.archived">
          {{
            deletePlan.archived === 1
              ? t("archivedConversationMoves")
              : t("archivedConversationsMove").replace("{count}", String(deletePlan.archived))
          }}
        </li>
      </ul>
    </template>
    <p v-if="deleteError" class="error-line" role="alert">{{ deleteError }}</p>
    <template #footer>
      <Button :label="t('cancel')" text severity="secondary" :disabled="saving" @click="closeDelete" />
      <Button
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
import { workspaceApi, type Project } from "../../services/workspace";
import { api } from "../../services/api";
import type { Conversation } from "../../types";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";

const { t } = useI18n();
const emit = defineEmits<{ (e: "created", conversation: Conversation): void }>();
const workspace = useWorkspace();
const draft = ref<Project | null>(null);
// The name the project had when editing began, "" for a new one: renaming
// saves to it.
let editingName = "";
const saving = ref(false);
const creating = ref(false);
const projectError = ref("");
const creatorError = ref("");
const deleting = workspace.projectDeletion;
const deletePlan = computed(() => deleting.value && workspace.deletionPlan(deleting.value));
watch(deleting, () => {
  deleteError.value = "";
});
const deleteError = ref("");
const newProjectConversation = ref({ project: "", title: "", message: "" });
const chosenProject = computed(() =>
  workspace.data.value?.projects.find((p) => p.name === newProjectConversation.value.project),
);

watch(workspace.projectEditor, (editor) => {
  if (!editor) return;
  draft.value = editor.project
    ? JSON.parse(JSON.stringify(editor.project))
    : { name: "", instructions: "", cwd: "" };
  editingName = editor.project?.name ?? "";
  projectError.value = "";
});
watch(workspace.conversationCreator, (creator) => {
  if (!creator) return;
  newProjectConversation.value = { project: creator.project, title: "", message: "" };
  creatorError.value = "";
});
function closeProject() {
  if (!saving.value) workspace.projectEditor.value = null;
}
function closeCreator() {
  if (!creating.value) workspace.conversationCreator.value = null;
}
function closeDelete() {
  if (!saving.value) deleting.value = null;
}
async function saveProject() {
  const project = draft.value;
  if (saving.value || !project) return;
  saving.value = true;
  projectError.value = "";
  try {
    const saved = { ...project, name: project.name.trim() };
    await (editingName
      ? workspaceApi.updateProject(editingName, saved)
      : workspaceApi.createProject(saved));
    await workspace.refresh();
    workspace.projectEditor.value = null;
  } catch (err) {
    projectError.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
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
    await workspaceApi.deleteProject(deleting.value.name);
    await workspace.refresh();
    deleting.value = null;
  } catch (err) {
    deleteError.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}
</script>
