<!-- The project editor, a page in the main pane in place of the chat. Its
     title is the project's name, which a click renames; saving a project
     that turned on VMs opens its base VM's setup conversation. -->
<template>
  <section class="project-page" :aria-label="t('editProject')" @keydown.esc="close">
    <div class="header">
      <div class="header-left">
        <Button
          class="btn-icon hide-on-desktop"
          text
          severity="secondary"
          icon="pi pi-bars"
          :aria-label="t('openConversations')"
          @click="emit('openDrawer')"
        />
        <h1 class="app-bar-title header-title">
          <span v-if="renaming" class="project-title-rename">
            <ProjectNameField
              :initial="editingName"
              :label="t('rename')"
              :submit="renameProject"
              @done="renaming = false"
            />
          </span>
          <button
            v-else
            type="button"
            class="project-title-button"
            :title="t('rename')"
            :disabled="saving"
            @click="renaming = true"
          >
            {{ editingName }}
          </button>
        </h1>
      </div>
      <div class="header-actions">
        <Button
          class="btn-icon"
          text
          severity="secondary"
          icon="pi pi-times"
          :aria-label="t('cancel')"
          :disabled="saving"
          @click="close"
        />
      </div>
    </div>
    <div class="project-page-body">
      <form
        v-if="draft"
        id="project-form"
        class="project-form project-page-form"
        @submit.prevent="saveProject"
      >
        <p class="project-muted">{{ t("projectFormIntro") }}</p>
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
        <ProjectRepoPicker v-model="draft.repos" :disabled="saving" />
        <ProjectVMFields v-model:vm="draft.vm" heading :disabled="saving" @navigate="close" />
        <label for="project-cwd">{{ t("workingDirectory") }}</label>
        <input
          id="project-cwd"
          v-model="draft.cwd"
          :required="!!draft.vm"
          :pattern="draft.vm ? '/.*' : undefined"
          :placeholder="draft.vm ? `${HOME}/project` : '/path/to/project (optional)'"
          autocomplete="off"
          :disabled="saving"
          @input="cwdTouched = true"
        />
        <p class="project-muted">
          {{ draft.vm ? t("projectCwdHintVm") : t("projectCwdHintHost") }}
        </p>
        <p class="project-muted">{{ t("projectProfileHint") }}</p>
        <div class="project-section-heading">
          <h3>{{ t("actions") }}</h3>
          <Button
            :label="t('addAction')"
            icon="pi pi-plus"
            text
            size="small"
            :disabled="saving"
            @click="addAction"
          />
        </div>
        <p class="project-muted">{{ draft.vm ? t("actionsHintVm") : t("actionsHint") }}</p>
        <fieldset
          v-for="(action, index) in draft.actions"
          :key="index"
          class="project-action-editor"
          :disabled="saving"
        >
          <legend>{{ t("actionNumber").replace("{n}", String(index + 1)) }}</legend>
          <div class="project-action-fields">
            <div>
              <label :for="`action-id-${index}`">{{ t("actionId") }}</label
              ><input
                :id="`action-id-${index}`"
                v-model="action.id"
                required
                pattern="[a-zA-Z0-9_-]+"
                maxlength="64"
                placeholder="publish"
              />
            </div>
            <div>
              <label :for="`action-label-${index}`">{{ t("actionButtonLabel") }}</label
              ><input
                :id="`action-label-${index}`"
                v-model="action.label"
                required
                placeholder="Publish"
              />
            </div>
          </div>
          <label :for="`action-command-${index}`">{{ t("shellCommand") }}</label>
          <textarea
            :id="`action-command-${index}`"
            v-model="action.command"
            class="project-command-input"
            required
            rows="3"
            placeholder="git push"
          />
          <div class="project-section-heading">
            <label class="project-checkbox"
              ><input v-model="action.archive_on_success" type="checkbox" />
              {{ t("archiveOnSuccess") }}</label
            >
            <label class="project-checkbox"
              ><input
                type="checkbox"
                :checked="!action.silent"
                @change="action.silent = !($event.target as HTMLInputElement).checked"
              />
              {{ t("sendResultToAgent") }}</label
            >
            <Button
              :label="t('removeAction')"
              text
              severity="danger"
              size="small"
              @click="draft.actions.splice(index, 1)"
            />
          </div>
        </fieldset>
        <p v-if="projectError" class="error-line" role="alert">{{ projectError }}</p>
      </form>
    </div>
    <div class="project-page-footer">
      <Button :label="t('cancel')" text severity="secondary" :disabled="saving" @click="close" />
      <Button
        :label="saving ? t('savingEllipsis') : t('saveProject')"
        type="submit"
        form="project-form"
        :loading="saving"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import Button from "primevue/button";
import ProjectRepoPicker from "./ProjectRepoPicker.vue";
import ProjectVMFields from "./ProjectVMFields.vue";
import ProjectNameField from "./ProjectNameField.vue";
import { workspaceApi, type Project } from "../../services/workspace";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { navigateToConversationSlug } from "../composables/subagentLive";

const { t } = useI18n();
const emit = defineEmits<{ (e: "openDrawer"): void }>();
const workspace = useWorkspace();
const draft = ref<Project | null>(null);
// The project's name, which renaming (in place, in the title) changes and
// saving saves to.
const editingName = ref("");
const renaming = ref(false);
// The VM user's home, where the base VM's setup clones the repositories.
const HOME = "/home/exedev";
const cwdTouched = ref(false);
const saving = ref(false);
const projectError = ref("");

watch(
  workspace.projectEditor,
  (editor) => {
    if (!editor) return;
    draft.value = JSON.parse(JSON.stringify(editor.project));
    editingName.value = editor.project.name;
    renaming.value = false;
    // A project without a directory gets one that follows its repositories
    // and VMs, until edited.
    cwdTouched.value = !!editor.project.cwd;
    projectError.value = "";
  },
  { immediate: true },
);
// The latest rename, which saving waits for: leaving the title for Save
// renames first.
let renamed = Promise.resolve();
function renameProject(name: string) {
  return (renamed = rename(name));
}
// Renames the project as saved, leaving the draft's other edits unsaved.
async function rename(name: string) {
  const saved = workspace.data.value?.projects.find((p) => p.name === editingName.value);
  if (!saved) throw new Error(`Project ${editingName.value} no longer exists.`);
  await workspaceApi.updateProject(editingName.value, { ...saved, name });
  editingName.value = name;
  if (draft.value) draft.value.name = name;
  await workspace.refresh();
}
const slug = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "") || "project";
// A working directory follows the repositories until edited:
// on a VM, the single repository's clone, or home holding several.
watch(
  () => draft.value && ([draft.value.repos, draft.value.name, !!draft.value.vm] as const),
  (current) => {
    if (!current || cwdTouched.value || !draft.value) return;
    const [repos, name, onVM] = current;
    if (!onVM) draft.value.cwd = "";
    else if (repos.length === 1) draft.value.cwd = `${HOME}/${repos[0].repository.split("/")[1]}`;
    else draft.value.cwd = repos.length ? HOME : `${HOME}/${slug(name)}`;
  },
  { deep: true },
);
function close() {
  if (!saving.value) workspace.projectEditor.value = null;
}
function addAction() {
  draft.value?.actions.push({
    id: "",
    label: "",
    command: "",
    archive_on_success: false,
    silent: false,
  });
}
async function saveProject() {
  const project = draft.value;
  if (saving.value || !project) return;
  saving.value = true;
  projectError.value = "";
  try {
    const actions = project.actions;
    if (new Set(actions.map((action) => action.id)).size !== actions.length)
      throw new Error("Each action needs a unique ID.");
    await renamed.catch(() => undefined); // its error shows in the title
    const before = workspace.projectEditor.value?.project.vm?.setup_conversation_id;
    const result = await workspaceApi.updateProject(editingName.value, project);
    await workspace.refresh();
    workspace.projectEditor.value = null;
    // Turning on VMs starts the base VM setup conversation: follow along there.
    const setup = result.vm?.setup_conversation_id;
    if (setup && setup !== before) navigateToConversationSlug(setup);
  } catch (err) {
    projectError.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}
</script>
