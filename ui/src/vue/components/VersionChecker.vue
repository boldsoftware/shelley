<!-- Vue port of the VersionModal from components/VersionChecker.tsx. The
     useVersionChecker hook lives in composables/versionChecker.ts; this file
     is the modal chrome. Preserves the version-modal-* / version-* class
     contract and the "Close" aria-label. Loads changelog + auto-upgrade
     setting on open, and drives the upgrade / headless-shell upgrade flows.
     Uses escapeClose + api. -->
<template>
  <div v-if="isOpen" class="version-modal-overlay" @click="emit('close')">
    <div class="version-modal" @click.stop>
      <div class="version-modal-header">
        <h2>Version</h2>
        <button class="version-modal-close" aria-label="Close" @click="emit('close')">
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              :stroke-width="2"
              d="M6 18L18 6M6 6l12 12"
            />
          </svg>
        </button>
      </div>

      <div class="version-modal-content">
        <div v-if="isLoading" class="version-loading">Checking for updates...</div>
        <template v-else-if="versionInfo">
          <div class="version-info-row">
            <span class="version-label">Current:</span>
            <span class="version-value">
              {{ versionInfo.current_tag || versionInfo.current_version || "dev" }}
            </span>
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
            <div v-else class="version-no-commits">No commits found</div>
          </div>
        </template>
        <div v-else class="version-loading">Loading...</div>
      </div>

      <!-- Footer: auto-upgrade + upgrade button -->
      <div v-if="!isLoading && versionInfo" class="version-modal-footer">
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { api } from "../../services/api";
import type { VersionInfo, CommitInfo } from "../../types";
import { useEscapeClose } from "../composables/escapeClose";

const props = defineProps<{
  isOpen: boolean;
  versionInfo: VersionInfo | null;
  isLoading: boolean;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const commits = ref<CommitInfo[]>([]);
const loadingCommits = ref(false);
const upgrading = ref(false);
const upgradeError = ref<string | null>(null);
const autoUpgrade = ref(false);
const loadingAutoUpgrade = ref(true);
const upgradingHeadless = ref(false);
const headlessError = ref<string | null>(null);
const headlessSuccess = ref<string | null>(null);

useEscapeClose(
  () => props.isOpen,
  () => emit("close"),
);

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
  try {
    const result = await api.getChangelog(currentTag, latestTag);
    commits.value = result || [];
  } catch (err) {
    console.error("Failed to load changelog:", err);
    commits.value = [];
  } finally {
    loadingCommits.value = false;
  }
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
      if (
        props.versionInfo?.has_update &&
        props.versionInfo.current_tag &&
        props.versionInfo.latest_tag
      ) {
        loadCommits(props.versionInfo.current_tag, props.versionInfo.latest_tag);
      }
      loadAutoUpgradeSetting();
    }
  },
  { immediate: true },
);
</script>
