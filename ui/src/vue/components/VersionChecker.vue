<!-- Vue port of the VersionModal from components/VersionChecker.tsx. The
     useVersionChecker hook lives in composables/versionChecker.ts; this file
     is the modal chrome. Uses the shared Modal (PrimeVue Dialog); the
     version-* content classes are preserved. Loads changelog + auto-upgrade
     setting on open, and drives the upgrade / headless-shell upgrade flows.
     Customized builds (see the customizing-shelley skill) get a Customizations
     list and a rebase-based upgrade flow instead of a binary self-update. -->
<template>
  <Modal :is-open="isOpen" title="Version" class-name="version-modal" @close="emit('close')">
    <div v-if="isLoading" class="version-loading">Checking for updates...</div>
    <template v-else-if="versionInfo">
      <div class="version-info-row">
        <span class="version-label">Current:</span>
        <span class="version-value">
          {{
            (versionInfo.customized && versionInfo.current_version !== "dev"
              ? versionInfo.current_version
              : "") ||
            versionInfo.current_tag ||
            versionInfo.current_version ||
            "dev"
          }}
        </span>
        <span v-if="versionInfo.customized" class="version-badge-custom">customized</span>
        <span v-if="versionInfo.current_commit_time" class="version-date">
          ({{ formatDateTime(versionInfo.current_commit_time) }})
        </span>
      </div>

      <div v-if="versionInfo.latest_tag" class="version-info-row">
        <span class="version-label">Latest:</span>
        <span class="version-value">{{ versionInfo.latest_tag }}</span>
        <span v-if="versionInfo.published_at" class="version-date">
          ({{ formatDateTime(versionInfo.published_at) }})
        </span>
      </div>

      <div v-if="versionInfo.error" class="version-error">
        <span>Error: {{ versionInfo.error }}</span>
      </div>

      <!-- Customizations carried on top of mainline -->
      <div
        v-if="versionInfo.customized && versionInfo.custom_commits?.length"
        class="version-changelog"
      >
        <h3>Customizations</h3>
        <ul class="commit-list">
          <li v-for="commit in versionInfo.custom_commits" :key="commit.sha" class="commit-item">
            <span class="commit-sha">{{ commit.sha }}</span>
            <span class="commit-message">{{ commit.message }}</span>
          </li>
        </ul>
      </div>

      <!-- Changelog -->
      <div v-if="versionInfo.has_update" class="version-changelog">
        <h3>
          <a
            :href="`https://github.com/boldsoftware/shelley/compare/${versionInfo.current_tag}...${versionInfo.latest_tag}`"
            target="_blank"
            rel="noopener noreferrer"
            class="changelog-link"
          >
            Changelog
          </a>
        </h3>
        <div v-if="loadingCommits" class="version-loading">Loading...</div>
        <ul v-else-if="commits.length > 0" class="commit-list">
          <li v-for="commit in commits" :key="commit.sha" class="commit-item">
            <a
              :href="getCommitUrl(commit.sha)"
              target="_blank"
              rel="noopener noreferrer"
              class="commit-sha"
            >
              {{ commit.sha }}
            </a>
            <span class="commit-message">{{ commit.message }}</span>
          </li>
        </ul>
        <div v-else-if="commitsError" class="version-no-commits">Couldn't load changelog</div>
        <div v-else class="version-no-commits">No commits found</div>
      </div>
    </template>
    <div v-else class="version-loading">Loading...</div>

    <!-- Footer: auto-upgrade + upgrade button -->
    <template v-if="!isLoading && versionInfo" #footer>
      <div class="version-modal-footer-content">
        <!-- Customized builds: upgrading means rebasing, not a binary swap -->
        <template v-if="versionInfo.customized">
          <div class="version-customized-note">
            This build has diverged from mainline: it carries your customizations on top of
            {{ versionInfo.current_tag || "an unknown release" }}.
            <template v-if="versionInfo.has_update">
              Upgrading starts a Shelley conversation that uses the selected model and reasoning
              effort to rebase your customizations onto {{ versionInfo.latest_tag }} and rebuilds.
            </template>
            <template v-else-if="!versionInfo.error">Mainline has no newer release.</template>
          </div>
          <div v-if="versionInfo.has_update" class="version-actions">
            <div v-if="upgradeError" class="version-error">{{ upgradeError }}</div>
            <div class="version-rebase-bar">
              <button
                :disabled="startingRebase || !rebaseModel"
                class="version-rebase-action"
                @click="handleRebaseUpgrade"
              >
                <template v-if="startingRebase">Starting conversation...</template>
                <template v-else>
                  Rebase onto {{ versionInfo.latest_tag }} using
                  <span class="version-rebase-model-name">{{ rebaseModelLabel }}</span>
                </template>
              </button>
              <span v-tooltip.top="noModelsReason" class="version-rebase-model">
                <ModelPicker
                  append-to-body
                  aria-label-prefix="Rebase model"
                  trigger-label="Model"
                  scroll-height="16rem"
                  :catalog-actions="false"
                  :disabled-reason="noModelsReason"
                  :models="rebaseModels"
                  :selected-model="rebaseModel"
                  :thinking-level="rebaseThinkingLevel"
                  :disabled="startingRebase || !rebaseModel"
                  @select-model="rebaseModel = $event"
                  @thinking-change="rebaseThinkingLevel = $event"
                />
              </span>
            </div>
          </div>
        </template>
        <template v-else>
          <div v-if="!loadingAutoUpgrade" class="version-auto-upgrade">
            <label class="version-checkbox-label">
              <input
                type="checkbox"
                :checked="autoUpgrade"
                @change="handleAutoUpgradeChange(($event.target as HTMLInputElement).checked)"
              />
              <span>Auto-upgrade when idle (checks daily)</span>
            </label>
          </div>

          <div v-if="versionInfo.has_update && versionInfo.download_url" class="version-actions">
            <div v-if="upgradeError" class="version-error">{{ upgradeError }}</div>
            <button
              :disabled="upgrading"
              class="version-btn version-btn-primary"
              @click="handleUpgradeAndRestart"
            >
              {{
                upgrading
                  ? versionInfo.running_under_systemd
                    ? "Upgrading Shelley & Restarting..."
                    : "Upgrading Shelley & Killing..."
                  : versionInfo.running_under_systemd
                    ? "Upgrade Shelley & Restart"
                    : "Upgrade & Kill Shelley Server"
              }}
            </button>
          </div>
        </template>

        <!-- Headless Shell (Browser) section -->
        <div v-if="versionInfo.headless_shell_current" class="version-headless-section">
          <div class="version-info-row">
            <span class="version-label">Browser:</span>
            <span class="version-value">{{ versionInfo.headless_shell_current }}</span>
          </div>
          <div
            v-if="versionInfo.headless_shell_update && versionInfo.headless_shell_latest"
            class="version-info-row"
          >
            <span class="version-label">Latest:</span>
            <span class="version-value">{{ versionInfo.headless_shell_latest }}</span>
          </div>
          <div v-if="versionInfo.headless_shell_update" class="version-actions">
            <div v-if="headlessError" class="version-error">{{ headlessError }}</div>
            <div v-if="headlessSuccess" class="version-success">{{ headlessSuccess }}</div>
            <button
              :disabled="upgradingHeadless"
              class="version-btn version-btn-secondary"
              @click="handleUpgradeHeadlessShell"
            >
              {{ upgradingHeadless ? "Upgrading Browser..." : "Upgrade Browser" }}
            </button>
          </div>
          <div v-else class="version-up-to-date">Browser is up to date</div>
        </div>
      </div>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { api } from "../../services/api";
import { prettyModelLabels } from "../../utils/modelNames";
import type { VersionInfo, CommitInfo, Model } from "../../types";
import Modal from "./Modal.vue";
import ModelPicker from "./ModelPicker.vue";
import { pickReadyModel, storedSelectedModel } from "./selectedModel";
import { createVersionChangelogLoader, versionChangelogTags } from "./versionChangelog";
import {
  DEFAULT_THINKING_LEVEL,
  normalizeThinkingLevelForModel,
  storedThinkingLevel,
  type ThinkingLevel,
} from "./thinkingLevel";

const props = defineProps<{
  isOpen: boolean;
  versionInfo: VersionInfo | null;
  isLoading: boolean;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const commits = ref<CommitInfo[]>([]);
const changelogLoader = createVersionChangelogLoader((currentTag, latestTag) =>
  api.getChangelog(currentTag, latestTag),
);
const loadingCommits = ref(false);
const commitsError = ref(false);
const upgrading = ref(false);
const upgradeError = ref<string | null>(null);
const startingRebase = ref(false);
const autoUpgrade = ref(false);
const loadingAutoUpgrade = ref(true);
const upgradingHeadless = ref(false);
const headlessError = ref<string | null>(null);
const headlessSuccess = ref<string | null>(null);
const rebaseModels = ref<Model[]>([]);
const rebaseModel = ref("");
// Action-scoped: seeded from the composer's stored picks but never written
// back, so configuring a rebase doesn't move the user's chat defaults.
const rebaseThinkingLevel = ref<ThinkingLevel>(DEFAULT_THINKING_LEVEL);
const noModelsReason = computed(() =>
  rebaseModel.value ? undefined : "No models are ready; configure one in Manage models.",
);
const rebaseModelLabel = computed(() => {
  const labels = prettyModelLabels(rebaseModels.value);
  const name = labels.get(rebaseModel.value) || rebaseModel.value || "no model";
  let effort = normalizedRebaseLevel.value;
  if (effort === "default") {
    const m = rebaseModels.value.find((x) => x.id === rebaseModel.value);
    const d = m?.default_reasoning_level;
    if (d && d !== "default") effort = d as ThinkingLevel;
  }
  return effort && effort !== "default" ? `${name} · ${effort}` : name;
});

// The stored level may not be valid for the chosen model (e.g. "max" on a
// model that doesn't advertise it); normalize like the composer does so we
// never send a level the server would reject.
const normalizedRebaseLevel = computed(() =>
  normalizeThinkingLevelForModel(
    rebaseThinkingLevel.value,
    rebaseModels.value.find((x) => x.id === rebaseModel.value),
  ),
);

function initializeRebase() {
  rebaseModels.value = window.__SHELLEY_INIT__?.models || [];
  rebaseModel.value = pickReadyModel(rebaseModels.value, storedSelectedModel());
  rebaseThinkingLevel.value = storedThinkingLevel();
}

async function loadAutoUpgradeSetting() {
  loadingAutoUpgrade.value = true;
  try {
    const settings = await api.getSettings();
    autoUpgrade.value = settings.auto_upgrade === "true";
  } catch (err) {
    console.error("Failed to load auto-upgrade setting:", err);
  } finally {
    loadingAutoUpgrade.value = false;
  }
}

async function handleAutoUpgradeChange(enabled: boolean) {
  try {
    await api.setSetting("auto_upgrade", enabled ? "true" : "false");
    autoUpgrade.value = enabled;
  } catch (err) {
    console.error("Failed to set auto-upgrade:", err);
    autoUpgrade.value = !enabled;
  }
}

async function loadCommits(currentTag: string, latestTag: string) {
  loadingCommits.value = true;
  commitsError.value = false;
  const result = await changelogLoader.load(currentTag, latestTag);
  if (!result) return;

  loadingCommits.value = false;
  if (result.ok) {
    commits.value = result.commits;
    return;
  }

  console.error("Failed to load changelog:", result.error);
  commits.value = [];
  commitsError.value = true;
}

async function handleUpgradeAndRestart() {
  upgrading.value = true;
  upgradeError.value = null;
  try {
    await api.upgrade(true);
  } catch (err) {
    // Connection drop is expected when server restarts, treat as success.
    console.log("Upgrade response failed (expected during restart):", err);
  }
  setTimeout(() => {
    window.location.reload();
  }, 2000);
}

// For customized builds, "upgrade" means starting a Shelley conversation that
// rebases the user's customization branch onto the latest mainline and
// rebuilds. See the customizing-shelley skill. customization_dir is only set
// when the checkout exists on disk; without it, start the conversation in the
// default cwd and let the agent recreate the checkout per the skill.
async function handleRebaseUpgrade() {
  const info = props.versionInfo;
  if (!info || !rebaseModel.value) return;
  startingRebase.value = true;
  upgradeError.value = null;
  const dir = info.customization_dir;
  const prompt =
    `This Shelley server is a customized build based on ${info.current_tag || "an unknown release"}; ` +
    `mainline is now at ${info.latest_tag}. Using the customizing-shelley skill, upgrade it: ` +
    (dir
      ? `in ${dir}, fetch origin and rebase the customization branch onto origin/main, `
      : `the customization checkout is missing, so recreate it at the skill's canonical location, ` +
        `then fetch origin and rebase the customization branch onto origin/main, `) +
    `resolving conflicts carefully, then rebuild with 'make build-custom', run relevant tests, ` +
    `and offer to install the new binary` +
    (info.executable_path ? ` (the running server is ${info.executable_path})` : ``) +
    ` or run it off to the side.`;
  try {
    const response = await api.sendMessageWithNewConversation({
      message: prompt,
      model: rebaseModel.value,
      ...(normalizedRebaseLevel.value === "default"
        ? {}
        : { conversation_options: { thinking_level: normalizedRebaseLevel.value } }),
      ...(dir ? { cwd: dir } : {}),
    });
    emit("close");
    window.history.pushState({}, "", `/c/${response.conversation_id}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  } catch (err) {
    upgradeError.value = err instanceof Error ? err.message : String(err);
  } finally {
    startingRebase.value = false;
  }
}

async function handleUpgradeHeadlessShell() {
  upgradingHeadless.value = true;
  headlessError.value = null;
  headlessSuccess.value = null;
  try {
    const result = await api.upgradeHeadlessShell();
    headlessSuccess.value = result.message;
  } catch (err) {
    headlessError.value = err instanceof Error ? err.message : String(err);
  } finally {
    upgradingHeadless.value = false;
  }
}

function formatDateTime(dateStr: string): string {
  const date = new Date(dateStr);
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  });
}

function getCommitUrl(sha: string): string {
  return `https://github.com/boldsoftware/shelley/commit/${sha}`;
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      initializeRebase();
      loadAutoUpgradeSetting();
    }
  },
  { immediate: true },
);

watch(
  [
    () => props.isOpen,
    () => props.versionInfo?.has_update,
    () => props.versionInfo?.current_tag,
    () => props.versionInfo?.latest_tag,
  ],
  ([isOpen, hasUpdate, currentTag, latestTag]) => {
    changelogLoader.invalidate();
    loadingCommits.value = false;
    commits.value = [];
    commitsError.value = false;
    const tags = versionChangelogTags(isOpen, hasUpdate, currentTag, latestTag);
    if (tags) loadCommits(tags[0], tags[1]);
  },
  { immediate: true },
);
</script>
