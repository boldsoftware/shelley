<!-- Workspace mode's GitHub repository picker. Through an exe-dev
     integration with full access to the account, it lists every repository
     the account's GitHub connections can see, and adding one creates or
     attaches the exe.dev GitHub integration its VMs need (on the click,
     never on load); without GitHub connected, it links to exe.dev's GitHub
     sign-in and reloads on return. Where the account can't be listed (no
     exe-dev integration, or a restricted one, whose refusal it explains),
     it is the stock picker of the repositories attached to this host. -->
<template>
  <template v-if="accounts">
    <div class="project-section-heading">
      <h3>{{ t("projectGithubHeading") }}</h3>
      <button type="button" class="text-button" :disabled="loading" @click="reload">
        {{ t("projectGithubRefresh") }}
      </button>
    </div>
    <div class="project-repos" :aria-busy="loading">
      <div v-if="!accounts.length" class="project-empty-repos project-github-connect">
        <p>{{ t("projectGithubNotConnected") }}</p>
        <template v-if="setupURL">
          <a
            class="project-connect-button"
            :href="setupURL"
            target="_blank"
            rel="noopener noreferrer"
            @click="awaitReturn"
          >
            <i class="pi pi-github" aria-hidden="true" /> {{ t("projectGithubConnect") }}
            <i class="pi pi-external-link" aria-hidden="true" />
          </a>
          <p class="project-muted">{{ t("projectGithubConnectHint") }}</p>
        </template>
      </div>
      <template v-else>
        <div class="project-repo-filters">
          <input
            v-model="query"
            type="search"
            :placeholder="t('projectGithubSearchPlaceholder').replace('{count}', String(total))"
            :aria-label="t('projectGithubSearch')"
            autocomplete="off"
          />
          <select v-if="owners.length > 1" v-model="owner" :aria-label="t('projectGithubOwner')">
            <option value="">{{ t("projectGithubAllOwners") }}</option>
            <option v-for="name in owners" :key="name" :value="name">{{ name }}</option>
          </select>
        </div>
        <ul class="project-repo-list" data-testid="github-repo-list">
          <li
            v-for="row in rows.slice(0, limit)"
            :key="row.repository"
            class="project-repo-row"
            :class="{ selected: row.selected }"
          >
            <i class="pi pi-github" aria-hidden="true" />
            <span class="project-repo-name">
              <strong>{{ row.repository }}</strong>
              <span v-if="row.private" class="project-badge">{{ t("projectGithubPrivate") }}</span>
              <span v-if="statusKnown" class="project-muted">{{ repoRowStatus(row, t) }}</span>
            </span>
            <Button
              v-if="row.selected"
              :label="t('projectExeRemove')"
              text
              severity="secondary"
              size="small"
              :disabled="disabled || !!busy"
              @click="remove(row.repository)"
            />
            <Button
              v-else
              :label="t('projectExeAdd')"
              outlined
              size="small"
              :loading="busy === row.repository"
              :disabled="disabled || !!busy"
              @click="add(row)"
            />
          </li>
          <li v-if="!rows.length" class="project-muted project-repo-none">
            {{ t("projectGithubNoMatch") }}
          </li>
        </ul>
        <button v-if="rows.length > limit" type="button" class="text-button" @click="limit += PAGE">
          {{ t("projectGithubShowMore").replace("{count}", String(rows.length - limit)) }}
        </button>
        <p
          v-for="failed in accounts.filter((a) => a.error)"
          :key="failed.account"
          class="error-line"
        >
          {{ failed.account }}: {{ failed.error }}
        </p>
        <div class="project-repo-links">
          <span class="project-muted">
            {{
              t("projectGithubAccounts").replace(
                "{accounts}",
                accounts.map((a) => a.account).join(", "),
              )
            }}
          </span>
          <a
            v-if="setupURL"
            :href="setupURL"
            target="_blank"
            rel="noopener noreferrer"
            @click="awaitReturn"
          >
            <i class="pi pi-plus" aria-hidden="true" /> {{ t("projectGithubConnectAnother") }}
            <i class="pi pi-external-link" aria-hidden="true" />
          </a>
        </div>
      </template>
      <p v-if="error" class="error-line" role="alert">{{ error }}</p>
    </div>
  </template>
  <template v-else-if="account.checked.value && (!account.available.value || loaded)">
    <p v-if="accountsError" class="project-notice">
      {{
        t("projectGithubRestricted")
          .replace("{integration}", integrationName)
          .replace("{error}", accountsError)
      }}
    </p>
    <ProjectRepoPicker
      :model-value="modelValue"
      :disabled="disabled"
      @update:model-value="emit('update:modelValue', $event)"
    />
    <p v-if="error" class="error-line" role="alert">{{ error }}</p>
  </template>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import Button from "primevue/button";
import ProjectRepoPicker from "./ProjectRepoPicker.vue";
import {
  workspaceApi,
  type GithubAccount,
  type ProjectPlacement,
  type ProjectRepo,
} from "../../services/workspace";
import { reloadOnReturn, type ExeAccount } from "../composables/exeAccount";
import { useI18n } from "../composables/i18n";
import { repoRows, repoRowStatus, type RepoRow } from "../../utils/githubRepos";
import { errorMessage } from "../../utils/errorMessage";

const props = defineProps<{
  modelValue: ProjectRepo[];
  placement: ProjectPlacement;
  account: ExeAccount;
  disabled?: boolean;
}>();
const emit = defineEmits<{ (e: "update:modelValue", repos: ProjectRepo[]): void }>();
const { t } = useI18n();
const PAGE = 50;
// The account's GitHub connections, or null where they can't be listed
// (accountsError says why).
const accounts = ref<GithubAccount[] | null>(null);
const accountsError = ref("");
const setupURL = ref("");
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const busy = ref("");
const query = ref("");
const owner = ref("");
const limit = ref(PAGE);
watch([query, owner], () => (limit.value = PAGE));

const view = computed(() => props.account.view.value);
const integrationName = computed(() => view.value?.integration || props.placement.integration);
// Repository states need the account's integrations.
const statusKnown = computed(() => !!view.value && !view.value.error);
const rows = computed(() =>
  repoRows(
    accounts.value ?? [],
    view.value?.integrations ?? [],
    props.modelValue,
    query.value,
    owner.value,
  ),
);
const total = computed(() => repoRows(accounts.value ?? [], [], [], "", "").length);
const owners = computed(() =>
  [
    ...new Set(repoRows(accounts.value ?? [], [], props.modelValue, "", "").map((r) => r.owner)),
  ].sort(),
);
function remove(repository: string) {
  emit(
    "update:modelValue",
    props.modelValue.filter((r) => r.repository.toLowerCase() !== repository.toLowerCase()),
  );
}
// Adds row, first making its integration reach the project's VMs unless
// it already does.
async function add(row: RepoRow) {
  error.value = "";
  let repo: ProjectRepo | undefined = row.integration?.reaches
    ? { integration: row.integration.name, repository: row.repository }
    : undefined;
  if (!repo) {
    busy.value = row.repository;
    try {
      repo = await props.account.change({ action: "github", repository: row.repository });
    } catch (err) {
      error.value = errorMessage(err);
    } finally {
      busy.value = "";
    }
  }
  if (repo) emit("update:modelValue", [...props.modelValue, repo]);
}
async function load() {
  if (!props.account.available.value) return;
  loading.value = true;
  error.value = "";
  try {
    const result = await workspaceApi.githubAccounts(props.placement.integration);
    accounts.value = result.error ? null : (result.accounts ?? []);
    accountsError.value = result.error;
    setupURL.value = result.setup_url;
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    loading.value = false;
    loaded.value = true;
  }
}
function reload() {
  void load();
  void props.account.load();
}
// After a link to exe.dev, coming back reloads, once.
let stopWaiting: (() => void) | undefined;
function awaitReturn() {
  stopWaiting?.();
  stopWaiting = reloadOnReturn(() => {
    stopWaiting?.();
    stopWaiting = undefined;
    reload();
  });
}
onUnmounted(() => stopWaiting?.());
// The integration listing the accounts follows where tasks run.
watch(() => [props.account.available.value, props.placement.integration], load, {
  immediate: true,
});
</script>
