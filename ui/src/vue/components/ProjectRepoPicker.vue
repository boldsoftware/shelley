<!-- Picks a project's GitHub repositories from the exe.dev GitHub
     integrations attached to this host (one repository each), and links to
     exe.dev to connect another. Off exe.dev it shows only repositories the
     project already has. -->
<template>
  <div v-if="shown" class="project-section-heading"><h3>Repositories</h3></div>
  <div v-if="shown" class="project-repos" :aria-busy="loading">
    <p v-if="loaded && !choices.length" class="project-empty-repos">
      No GitHub repositories are connected to this VM yet.
    </p>
    <label
      v-for="repo in choices"
      :key="repo.integration"
      class="project-choice project-repo"
      :class="{ selected: isSelected(repo) }"
    >
      <input
        type="checkbox"
        :checked="isSelected(repo)"
        :disabled="disabled"
        @change="toggle(repo, ($event.target as HTMLInputElement).checked)"
      />
      <i class="pi pi-github" aria-hidden="true" />
      <span>
        <strong>{{ repo.repository }}</strong>
        <span class="project-muted">
          {{ repo.missing ? "Not connected to this VM" : `Integration ${repo.integration}` }}
          {{ repo.team ? "· team" : "" }}
        </span>
      </span>
    </label>
    <p v-if="error" class="error-line" role="alert">{{ error }}</p>
    <div v-if="connectURL" class="project-repo-links">
      <a :href="connectURL" target="_blank" rel="noopener noreferrer">
        <i class="pi pi-plus" aria-hidden="true" /> Connect a GitHub repository on exe.dev
        <i class="pi pi-external-link" aria-hidden="true" />
      </a>
      <button type="button" class="text-button" :disabled="loading" @click="load">
        Refresh
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { workspaceApi, type GithubRepo, type ProjectRepo } from "../../services/workspace";

const props = defineProps<{ modelValue: ProjectRepo[]; disabled?: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", repos: ProjectRepo[]): void }>();
const available = ref<GithubRepo[]>([]);
const connectURL = ref("");
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
// Attached repositories stay listed even if their integration was detached.
const choices = computed(() => [
  ...available.value.map((repo) => ({ ...repo, missing: false })),
  ...props.modelValue
    .filter((repo) => !available.value.some((a) => a.integration === repo.integration))
    .map((repo) => ({ ...repo, team: false, missing: true })),
]);
// Shown once loaded, where repositories can be connected or are attached.
const shown = computed(
  () => !!error.value || (loaded.value && (!!connectURL.value || !!props.modelValue.length)),
);
const isSelected = (repo: ProjectRepo) =>
  props.modelValue.some((r) => r.integration === repo.integration);
function toggle(repo: ProjectRepo, on: boolean) {
  const rest = props.modelValue.filter((r) => r.integration !== repo.integration);
  emit(
    "update:modelValue",
    on ? [...rest, { integration: repo.integration, repository: repo.repository }] : rest,
  );
}
async function load() {
  loading.value = true;
  error.value = "";
  try {
    const result = await workspaceApi.githubRepos();
    available.value = result.repos;
    connectURL.value = result.connect_url;
    loaded.value = true;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}
void load();
</script>
