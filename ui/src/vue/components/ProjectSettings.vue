<!-- A project's settings, its view's Settings tab. Saving a project that
     turned on VMs opens its base VM's setup conversation; moving it in the
     sidebar and deleting it act at once. -->
<template>
  <section class="project-page" :aria-label="t('editProject')">
    <div class="project-page-body">
      <form id="project-form" class="project-form project-page-form" @submit.prevent="saveProject">
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
        <ProjectRepoPicker v-if="!account" v-model="draft.repos" :disabled="saving" />
        <ProjectVMFields v-model:vm="draft.vm" heading :disabled="saving" />
        <template v-if="account">
          <ProjectGithubRepos
            v-model="draft.repos"
            :placement="placement"
            :account="account"
            :disabled="saving"
            @logins="logins = $event"
          />
          <ProjectIntegrations :account="account" :placement="placement" :disabled="saving" />
        </template>
        <div class="project-section-heading">
          <h3>{{ t("commitIdentityHeading") }}</h3>
        </div>
        <label class="project-checkbox"
          ><input
            type="checkbox"
            :checked="usesDefault"
            :disabled="saving"
            data-testid="commit-identity-default"
            @change="setUsesDefault(($event.target as HTMLInputElement).checked)"
          />
          {{ t("commitIdentityUseDefault") }}</label
        >
        <CommitIdentityFields
          v-if="usesDefault"
          v-model="defaultDraft"
          :logins="logins"
          :prefill="reposAdded && !hasCommitIdentity(defaultDraft)"
          :disabled="saving"
        />
        <CommitIdentityFields
          v-else
          v-model="draft.commit_identity"
          :logins="logins"
          :placeholder="defaultIdentity"
          required
          :disabled="saving"
        />
        <p class="project-muted">
          {{ t("commitIdentityHint") }}
          {{
            usesDefault && hasCommitIdentity(defaultIdentity) ? t("commitIdentityDefaultHint") : ""
          }}
        </p>
        <p
          v-if="!hasCommitIdentity(usesDefault ? defaultDraft : draft.commit_identity)"
          class="project-muted"
        >
          {{ t("commitIdentityNone") }}
        </p>
        <label for="project-cwd">{{ t("workingDirectory") }}</label>
        <input
          id="project-cwd"
          v-model="draft.cwd"
          :required="!!draft.vm"
          :pattern="draft.vm ? '/.*' : undefined"
          :placeholder="draft.vm ? `${VM_HOME}/project` : '/path/to/project (optional)'"
          autocomplete="off"
          :disabled="saving"
          @input="cwdTouched = true"
        />
        <p class="project-muted">
          {{ draft.vm ? t("projectCwdHintVm") : t("projectCwdHintHost") }}
        </p>
        <p class="project-muted">{{ t("projectProfileHint") }}</p>
        <template v-if="workspaceMode">
          <label for="project-page-url">{{ t("projectPageUrl") }}</label>
          <input
            id="project-page-url"
            v-model="draft.page_url"
            type="url"
            :placeholder="suggestedPage || 'https://example.com/'"
            autocomplete="off"
            :disabled="saving"
          />
          <p class="project-muted">
            {{ t("projectPageHint") }}
            <button
              v-if="suggestedPage && !draft.page_url"
              type="button"
              class="text-button"
              :disabled="saving"
              @click="draft.page_url = suggestedPage"
            >
              {{ t("projectPageUse").replace("{url}", suggestedPage) }}
            </button>
          </p>
        </template>
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
        <div class="project-section-heading">
          <h3>{{ t("projectPosition") }}</h3>
          <span>
            <Button
              :label="t('moveProjectUp')"
              icon="pi pi-arrow-up"
              text
              size="small"
              :disabled="moving || position <= 0"
              @click="moveProject(-1)"
            />
            <Button
              :label="t('moveProjectDown')"
              icon="pi pi-arrow-down"
              text
              size="small"
              :disabled="moving || position < 0 || position === projectNames.length - 1"
              @click="moveProject(1)"
            />
          </span>
        </div>
        <p v-if="moveError" class="error-line" role="alert">{{ moveError }}</p>
        <div class="project-danger-zone">
          <h3>{{ t("dangerZone") }}</h3>
          <p class="project-muted">{{ deletion.blocker || t("deleteProjectHint") }}</p>
          <Button
            :label="t('deleteProjectEllipsis')"
            icon="pi pi-trash"
            severity="danger"
            outlined
            size="small"
            :disabled="!!deletion.blocker || saving"
            @click="workspace.deleteProject(project)"
          />
        </div>
      </form>
    </div>
    <div class="project-page-footer">
      <span v-if="saved && !dirty" class="project-muted" role="status">{{
        t("projectSaved")
      }}</span>
      <Button
        v-if="dirty"
        :label="t('discardChanges')"
        text
        severity="secondary"
        :disabled="saving"
        @click="discard"
      />
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
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import ProjectRepoPicker from "./ProjectRepoPicker.vue";
import ProjectGithubRepos from "./ProjectGithubRepos.vue";
import ProjectVMFields from "./ProjectVMFields.vue";
import ProjectIntegrations from "./ProjectIntegrations.vue";
import CommitIdentityFields from "./CommitIdentityFields.vue";
import {
  workspaceApi,
  type CommitIdentity,
  type Project,
  type ProjectPlacement,
} from "../../services/workspace";
import { useExeAccount } from "../composables/exeAccount";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { navigateToConversationSlug } from "../composables/subagentLive";
import { projectCwd, VM_HOME } from "../../utils/projectSetup";
import { workspaceMode } from "../../utils/workspaceMode";
import {
  NO_COMMIT_IDENTITY,
  hasCommitIdentity,
  sameCommitIdentity,
} from "../../utils/commitIdentity";

const props = defineProps<{
  project: Project;
  /** Settles once a rename in the view's title is saved, which saving waits
   *  for: leaving the title for Save renames first. */
  renamed: () => Promise<void>;
}>();
const { t } = useI18n();
const workspace = useWorkspace();
// The saved project as edited; its name is the saved one's (the title renames).
const draft = ref<Project>(copy(props.project));
const cwdTouched = ref(false);
const saving = ref(false);
const saved = ref(false);
const projectError = ref("");
function copy(project: Project): Project {
  return JSON.parse(JSON.stringify(project)) as Project;
}
// Comparable: the name follows the title, not the draft.
function settings(project: Project) {
  return JSON.stringify({ ...project, name: "", position: 0 });
}
// The default commit identity, which the project uses unless it has its
// own; while it does, the fields edit the default, saved with the project.
const defaultIdentity = computed(() => workspace.data.value?.commit_identity ?? NO_COMMIT_IDENTITY);
const defaultDraft = ref<CommitIdentity>({ ...defaultIdentity.value });
const usesDefault = ref(true);
const logins = ref<string[]>([]);
// An edit that hasn't touched the default follows its changes.
watch(
  () => ({ ...defaultIdentity.value }),
  (now, before) => {
    if (sameCommitIdentity(defaultDraft.value, before)) defaultDraft.value = now;
  },
);
function setUsesDefault(on: boolean) {
  usesDefault.value = on;
  draft.value.commit_identity = on ? { ...NO_COMMIT_IDENTITY } : { ...defaultDraft.value };
}
// Repositories added in this edit: the identity is asked for along with them.
const reposAdded = computed(() => draft.value.repos.length > props.project.repos.length);
const dirty = computed(
  () =>
    settings(draft.value) !== settings(props.project) ||
    !sameCommitIdentity(defaultDraft.value, defaultIdentity.value),
);
function load(project: Project) {
  draft.value = copy(project);
  usesDefault.value = !hasCommitIdentity(project.commit_identity);
  defaultDraft.value = { ...defaultIdentity.value };
  // A project without a directory gets one that follows its repositories
  // and VMs, until edited.
  cwdTouched.value = !!project.cwd;
  projectError.value = "";
}
load(props.project);
function discard() {
  load(props.project);
}
// Where the draft's tasks run, for the exe.dev account's repositories and
// integrations (workspace mode's), which follow it.
const placement = computed<ProjectPlacement>(() => {
  const vm = draft.value.vm;
  return {
    integration: vm?.integration ?? "",
    // An SSH host (a shared_vm with no integration) is no exe.dev VM: like
    // own VMs not created yet, the account's integrations reach none.
    mode: !vm ? "host" : vm.shared_vm && vm.integration ? "shared" : "own",
    base: vm?.base ?? "",
    shared_vm: vm?.shared_vm ?? "",
  };
});
const account = workspaceMode
  ? useExeAccount(
      computed(() => props.project.name),
      placement,
    )
  : null;
// A shared VM's site is the obvious page; it is only ever saved explicitly.
const suggestedPage = computed(() =>
  draft.value.vm?.shared_vm ? `https://${draft.value.vm.shared_vm}.exe.xyz/` : "",
);
// A working directory follows the repositories until edited.
watch(
  () => [draft.value.repos, props.project.name, !!draft.value.vm] as const,
  ([repos, name, vm]) => {
    if (!cwdTouched.value) draft.value.cwd = projectCwd(name, repos, vm);
  },
  { deep: true },
);
const deletion = computed(() => workspace.deletionPlan(props.project));
const projectNames = computed(() => workspace.data.value?.projects.map((p) => p.name) ?? []);
const position = computed(() => projectNames.value.indexOf(props.project.name));
const moving = ref(false);
const moveError = ref("");
async function moveProject(by: number) {
  const order = [...projectNames.value];
  const from = position.value;
  [order[from], order[from + by]] = [order[from + by], order[from]];
  moving.value = true;
  moveError.value = "";
  try {
    await workspaceApi.reorderProjects(order);
    await workspace.refresh();
  } catch (err) {
    moveError.value = errorMessage(err);
  } finally {
    moving.value = false;
  }
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
  if (saving.value) return;
  saving.value = true;
  saved.value = false;
  projectError.value = "";
  try {
    const actions = draft.value.actions;
    if (new Set(actions.map((action) => action.id)).size !== actions.length)
      throw new Error("Each action needs a unique ID.");
    await props.renamed().catch(() => undefined); // its error shows in the title
    const before = props.project.vm?.setup_conversation_id;
    const name = props.project.name;
    if (!sameCommitIdentity(defaultDraft.value, defaultIdentity.value))
      await workspaceApi.setCommitIdentity(defaultDraft.value);
    const result = await workspaceApi.updateProject(name, { ...draft.value, name });
    await workspace.refresh();
    load(props.project);
    saved.value = true;
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
