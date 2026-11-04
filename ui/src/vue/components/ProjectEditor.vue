<!-- The project editor, a page in the main pane in place of the chat: a new
     project, or one being edited. Saving a new project that needs a base VM
     opens its setup conversation. -->
<template>
  <section
    class="project-page"
    :aria-label="editingName ? t('editProject') : t('newProject')"
    @keydown.esc="close"
  >
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
          {{ editingName ? t("editProject") : t("newProject") }}
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
        <label for="project-name">{{ t("projectName") }}</label>
        <input
          id="project-name"
          v-model="draft.name"
          required
          maxlength="64"
          autocomplete="off"
          :placeholder="t('projectNamePlaceholder')"
          :autofocus="!editingName"
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
import { workspaceApi, type Project } from "../../services/workspace";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { workspaceMode } from "../../utils/workspaceMode";
import { navigateToConversationSlug } from "../composables/subagentLive";

const { t } = useI18n();
const emit = defineEmits<{ (e: "openDrawer"): void }>();
const workspace = useWorkspace();
const draft = ref<Project | null>(null);
// The name the project had when editing began, "" for a new one: renaming
// saves to it.
const editingName = ref("");
// The VM user's home, where the base VM's setup clones the repositories.
const HOME = "/home/exedev";
const cwdTouched = ref(false);
const saving = ref(false);
const projectError = ref("");

watch(
  workspace.projectEditor,
  (editor) => {
    if (!editor) return;
    draft.value = editor.project
      ? JSON.parse(JSON.stringify(editor.project))
      : {
          name: "",
          instructions: "",
          cwd: "",
          actions: [],
          repos: [],
          // In workspace mode new projects run each conversation on its own VM
          // (ProjectVMFields fills in this host's default integration);
          // otherwise on this host.
          vm: workspaceMode
            ? { base: "", shared_vm: "", integration: "", setup_conversation_id: "" }
            : null,
        };
    editingName.value = editor.project?.name ?? "";
    cwdTouched.value = !!editor.project;
    projectError.value = "";
  },
  { immediate: true },
);
const slug = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "") || "project";
// A new project's working directory follows its repositories until edited:
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
    const saved = { ...project, name: project.name.trim() };
    const result = await (editingName.value
      ? workspaceApi.updateProject(editingName.value, saved)
      : workspaceApi.createProject(saved));
    await workspace.refresh();
    workspace.projectEditor.value = null;
    // A new project's base VM setup conversation is where to follow along.
    const setup = result.vm?.setup_conversation_id;
    if (!editingName.value && setup) navigateToConversationSlug(setup);
  } catch (err) {
    projectError.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}
</script>
