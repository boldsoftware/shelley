<!-- The Assistant's first-run state, before the user has a project: ways
     to get started. -->
<template>
  <div class="onboarding">
    <h2 class="text-primary">{{ t("onboardingHeading") }}</h2>
    <GithubStart v-if="githubOpen" @back="githubOpen = false" />
    <template v-else>
      <div class="onboarding-cards">
        <button
          v-if="githubConnectable"
          type="button"
          class="onboarding-card"
          data-testid="onboarding-github"
          @click="githubOpen = true"
        >
          <i class="pi pi-github" aria-hidden="true" />
          <span>
            <strong>{{ t("onboardingGithub") }}</strong>
            <span>{{ t("onboardingGithubHint") }}</span>
          </span>
        </button>
        <button
          type="button"
          class="onboarding-card"
          data-testid="onboarding-web-app"
          :disabled="creatingWebApps || !info"
          @click="buildWebApp"
        >
          <i
            :class="['pi', creatingWebApps ? 'pi-spin pi-spinner' : 'pi-globe']"
            aria-hidden="true"
          />
          <span>
            <strong>{{ t("onboardingWebApp") }}</strong>
            <span>{{ t(info?.exe_dev ? "onboardingWebAppVMs" : "onboardingWebAppPorts") }}</span>
          </span>
        </button>
        <a
          v-if="info?.text_number"
          class="onboarding-card"
          :href="smsLink(info.text_number, 'Hi', userAgent)"
          data-testid="onboarding-phone"
        >
          <i class="pi pi-mobile" aria-hidden="true" />
          <span>
            <strong>{{ t("onboardingPhone") }}</strong>
            <span>{{
              t(
                info.text_connected ? "onboardingPhoneConnected" : "onboardingPhoneConnect",
              ).replace("{number}", info.text_number)
            }}</span>
          </span>
        </a>
      </div>
      <p v-if="error" class="error-line" role="alert">{{ error }}</p>
      <p class="text-sm chat-secondary-text">{{ t("onboardingAskBelow") }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import GithubStart from "./GithubStart.vue";
import { workspaceApi, type Onboarding } from "../../services/workspace";
import { useWorkspace } from "../composables/workspace";
import { useI18n } from "../composables/i18n";
import { errorMessage } from "../../utils/errorMessage";
import { smsLink, uniqueProjectName, webAppsInstructions } from "../../utils/projectSetup";

const workspace = useWorkspace();
const { t } = useI18n();
const githubOpen = ref(false);
// GitHub repositories connect through exe.dev.
const githubConnectable = ref(false);
const info = ref<Onboarding | null>(null);
// Texting works from any device: a phone opens the link, and the card
// shows the number to text from one.
const userAgent = navigator.userAgent;
const creatingWebApps = ref(false);
const error = ref("");
const fail = (err: unknown) => (error.value = errorMessage(err));

void workspaceApi.githubRepos().then((result) => {
  githubConnectable.value = !!result.connect_url;
}, fail);
void workspaceApi.onboarding().then((result) => (info.value = result), fail);

async function buildWebApp() {
  if (!info.value) return;
  const { exe_dev, exe_dev_host, vm_host } = info.value;
  creatingWebApps.value = true;
  error.value = "";
  try {
    const name = uniqueProjectName(
      t("onboardingWebAppsProject"),
      workspace.data.value?.projects.map((p) => p.name) ?? [],
    );
    const project = {
      name,
      instructions: webAppsInstructions(exe_dev, exe_dev_host, vm_host),
      cwd: "",
      actions: [],
      vm: null,
      repos: [],
    };
    await workspaceApi.createProject(project);
    await workspace.refresh();
    workspace.newProjectConversation(name);
  } catch (err) {
    fail(err);
  } finally {
    creatingWebApps.value = false;
  }
}
</script>

<style scoped>
.onboarding {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.75rem;
  width: 40rem;
  max-width: 100%;
  margin: 0 auto;
  padding: 0 1rem;
}

.onboarding-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(16rem, 1fr));
  gap: 0.5rem;
  width: 100%;
}

.onboarding-card {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.7rem 0.9rem;
  border: 1px solid var(--border);
  border-radius: 0.6rem;
  background: var(--bg-base);
  color: var(--text-primary);
  font: inherit;
  text-align: left;
  text-decoration: none;
  cursor: pointer;
  transition:
    border-color 0.15s,
    background 0.15s;
}

.onboarding-card:hover:not(:disabled) {
  border-color: var(--primary);
  background: var(--bg-secondary);
}

.onboarding-card:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: 2px;
}

.onboarding-card > .pi {
  flex: none;
  font-size: 1.15rem;
  color: var(--primary);
}

.onboarding-card > span {
  display: flex;
  flex-direction: column;
  gap: 0.1rem;
  min-width: 0;
  font-size: var(--font-size-14);
  line-height: 1.3;
}

.onboarding-card > span > span {
  font-size: var(--font-size-12);
  color: var(--text-secondary);
}

.onboarding-card:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
