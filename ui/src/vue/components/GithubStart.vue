<!-- First-run onboarding's GitHub path: connect GitHub (through the
     project editor's picker), choose repositories, and start: it creates a
     project for them, whose base VM setup is the first task (on this host,
     without an exe-dev integration, the composer opens in it). -->
<template>
  <div class="project-form github-start">
    <button type="button" class="text-button github-start-back" @click="emit('back')">
      <i class="pi pi-arrow-left" aria-hidden="true" /> {{ t("githubStartBack") }}
    </button>
    <ProjectGithubRepos
      v-model="repos"
      :placement="placement"
      :account="account"
      :disabled="starting"
      @logins="logins = $event"
    />
    <template v-if="repos.length">
      <div class="project-section-heading">
        <h3>{{ t("commitIdentityHeading") }}</h3>
      </div>
      <CommitIdentityFields
        v-model="identity"
        :logins="logins"
        :prefill="!hasCommitIdentity(defaultIdentity)"
        required
        :disabled="starting"
      />
      <p class="project-muted">{{ t("commitIdentityStartHint") }}</p>
    </template>
    <p v-if="repos.length" class="project-muted">
      {{ t(integration ? "githubStartCreatesVM" : "githubStartCreatesProject") }}
    </p>
    <p v-if="error" class="error-line" role="alert">{{ error }}</p>
    <Button
      :label="t('githubStartWorking')"
      icon="pi pi-arrow-right"
      icon-pos="right"
      :disabled="!repos.length || !identity.name.trim() || !identity.email.trim()"
      :loading="starting"
      data-testid="github-start"
      @click="start"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import ProjectGithubRepos from "./ProjectGithubRepos.vue";
import CommitIdentityFields from "./CommitIdentityFields.vue";
import {
  workspaceApi,
  type CommitIdentity,
  type ProjectPlacement,
  type ProjectRepo,
} from "../../services/workspace";
import { useExeAccount } from "../composables/exeAccount";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { navigateToConversationSlug } from "../composables/subagentLive";
import { errorMessage } from "../../utils/errorMessage";
import { projectCwd, uniqueProjectName } from "../../utils/projectSetup";
import {
  NO_COMMIT_IDENTITY,
  hasCommitIdentity,
  newProjectCommitIdentity,
} from "../../utils/commitIdentity";

const emit = defineEmits<{ (e: "back"): void }>();
const workspace = useWorkspace();
const { t } = useI18n();
const repos = ref<ProjectRepo[]>([]);
// Who the project's tasks commit as: the default, if there is one, else
// what the user's GitHub and exe.dev accounts suggest, which they confirm.
const defaultIdentity = computed(() => workspace.data.value?.commit_identity ?? NO_COMMIT_IDENTITY);
const identity = ref<CommitIdentity>({ ...defaultIdentity.value });
watch(
  () => `${defaultIdentity.value.name}\n${defaultIdentity.value.email}`,
  () => {
    if (hasCommitIdentity(defaultIdentity.value)) identity.value = { ...defaultIdentity.value };
  },
);
const logins = ref<string[]>([]);
// The exe-dev integration the project's VMs use: this host's default, or
// none (tasks run on this host).
const integration = ref("");
const placement = computed<ProjectPlacement>(() => ({
  integration: integration.value,
  mode: integration.value ? "own" : "host",
  base: "",
  shared_vm: "",
}));
// The project isn't created until Start: repositories added before then
// get their integrations once it is.
const account = useExeAccount(ref(""), placement);
const starting = ref(false);
const error = ref("");
// The project a failed start created, which a retry finishes.
let created = "";

void workspaceApi.exeDevIntegrations().then(
  (result) => (integration.value = result.default),
  (err: unknown) => (error.value = errorMessage(err)),
);

async function start() {
  starting.value = true;
  error.value = "";
  try {
    const commit = newProjectCommitIdentity(defaultIdentity.value, identity.value);
    if (commit.setDefault) await workspaceApi.setCommitIdentity(identity.value);
    if (!created) {
      const name = uniqueProjectName(
        repos.value[0].repository.split("/")[1],
        workspace.data.value?.projects.map((p) => p.name) ?? [],
      );
      const project = { name, instructions: "", cwd: "", actions: [], vm: null, repos: [] };
      created = (await workspaceApi.createProject(project)).name;
    }
    for (const [i, repo] of repos.value.entries()) {
      if (repo.integration) continue;
      const added = await workspaceApi.changeExeAccount(created, placement.value, {
        action: "github",
        repository: repo.repository,
      });
      if (!added)
        throw new Error(t("githubStartNoIntegration").replace("{repository}", repo.repository));
      repos.value[i] = added;
    }
    const vm = integration.value
      ? {
          base: "",
          managed_base_vm: true,
          shared_vm: "",
          integration: integration.value,
          setup_conversation_id: "",
        }
      : null;
    const result = await workspaceApi.updateProject(created, {
      name: created,
      instructions: "",
      cwd: projectCwd(created, repos.value, !!vm),
      actions: [],
      vm,
      repos: repos.value,
      commit_identity: commit.project,
    });
    await workspace.refresh();
    const setup = result.vm?.setup_conversation_id;
    if (setup) navigateToConversationSlug(setup);
    else workspace.newProjectConversation(created);
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    starting.value = false;
  }
}
</script>

<style scoped>
.github-start {
  gap: 0.75rem;
  width: 100%;
  text-align: left;
}

.github-start-back {
  align-self: flex-start;
}

.github-start > :deep(.p-button) {
  align-self: flex-end;
}
</style>
