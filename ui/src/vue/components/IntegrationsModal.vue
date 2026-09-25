<template>
  <Modal
    :is-open="isOpen"
    :title="t('vmIntegrations')"
    class-name="modal-wide integrations-modal"
    @close="emit('close')"
  >
    <template #title-right>
      <Button
        :label="t('refresh')"
        severity="secondary"
        size="small"
        :disabled="loadingList || loadingDetail || busy"
        @click="refresh"
      />
    </template>

    <div class="integrations-tabs" :aria-label="t('vmIntegrations')">
      <button
        :aria-pressed="tab === 'list'"
        :class="{ active: tab === 'list' }"
        :disabled="busy"
        @click="tab = 'list'"
      >
        {{ t("attached") }} <span v-if="!loadingList">{{ integrations.length }}</span>
      </button>
      <button
        v-if="selected"
        :aria-pressed="tab === 'detail'"
        :class="{ active: tab === 'detail' }"
        :disabled="busy"
        @click="showDetails(selected)"
      >
        {{ selected.name }}
      </button>
    </div>

    <div v-if="tab === 'list'">
      <div v-if="loadingList" class="integrations-status">{{ t("loadingIntegrations") }}</div>
      <div v-else-if="error" class="integrations-status" role="alert">{{ error }}</div>
      <div v-else-if="integrations.length === 0" class="integrations-status">
        {{ t("noIntegrationsAttached") }}
      </div>
      <div v-else class="integrations-table-scroll">
        <table class="integrations-table">
          <thead>
            <tr>
              <th>{{ t("integrationName") }}</th>
              <th>{{ t("integrationType") }}</th>
              <th>{{ t("integrationScope") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="integration in integrations"
              :key="`${integration.team}-${integration.name}`"
            >
              <th scope="row">
                <button class="integrations-name-btn" @click="showDetails(integration)">
                  {{ integration.name }} <span aria-hidden="true">→</span>
                </button>
              </th>
              <td>{{ integration.type }}</td>
              <td>{{ integration.team ? t("team") : t("personal") }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else-if="selected" class="integrations-detail">
      <div class="integrations-detail-head">
        <strong>{{ selected.name }}</strong>
        <span>{{ selected.type }} · {{ selected.team ? t("team") : t("personal") }}</span>
        <a href="https://exe.dev/integrations" target="_blank" rel="noopener noreferrer"
          >{{ t("manageOnExeDev") }} ↗</a
        >
      </div>
      <div v-if="loadingDetail" class="integrations-status">
        {{ t("loadingIntegrationDetails") }}
      </div>
      <div v-else-if="detailError" class="integrations-status" role="alert">{{ detailError }}</div>
      <template v-else-if="detail">
        <section v-if="detail.type === 'llm'" class="integrations-models">
          <div class="integrations-models-head">
            <strong>{{ t("whatVmGets") }}</strong>
          </div>
          <div v-if="detail.details?.models_error" class="integrations-status" role="alert">
            {{ detail.details.models_error }}
          </div>
          <table
            v-if="llmProviders.length"
            class="integrations-detail-table integrations-providers-table"
          >
            <thead>
              <tr>
                <th scope="col">{{ t("provider") }}</th>
                <th scope="col">{{ t("accessVia") }}</th>
                <th scope="col">{{ t("availableModels") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="count in llmProviders" :key="count.provider">
                <th scope="row">{{ count.provider }}</th>
                <td>{{ count.mode === "managed" ? t("exeDevManaged") : count.mode || "—" }}</td>
                <td>{{ modelSummary(count) || "—" }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else-if="!detail.details?.models_error" class="integrations-status">
            {{ t("noProvidersEnabled") }}
          </div>
          <Button
            :label="t('browseModelsInShelley')"
            severity="secondary"
            size="small"
            @click="emit('open-models-modal')"
          />
        </section>
        <section v-if="detail.details?.repositories?.length" class="integrations-gh-repo">
          <table class="integrations-detail-table integrations-gh-table">
            <thead>
              <tr>
                <th scope="col">{{ t("repository") }}</th>
                <th scope="col">{{ t("copyCloneCommand") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="repo in detail.details.repositories" :key="repo.name">
                <th scope="row">
                  <a :href="repo.url" target="_blank" rel="noopener noreferrer">{{ repo.name }}</a>
                </th>
                <td>
                  <Button
                    text
                    severity="secondary"
                    size="small"
                    :label="copied === `clone:${repo.name}` ? t('copied') : t('copy')"
                    @click="copy(repo.clone_command, `clone:${repo.name}`)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </section>
        <section v-if="detail.type === 'notify'" class="integrations-notify">
          <strong>{{ t("testPushNotification") }}</strong>
          <p>{{ t("testPushNotificationDescription") }}</p>
          <form class="integrations-notify-form" @submit.prevent="sendTestNotification">
            <label for="integrations-test-message">{{ t("testMessage") }}</label>
            <div class="integrations-notify-controls">
              <input
                id="integrations-test-message"
                v-model="testMessage"
                maxlength="200"
                :disabled="sendingTest"
                @input="testResult = ''"
              />
              <Button
                type="submit"
                :label="sendingTest ? t('sending') : t('sendTestNotification')"
                :disabled="sendingTest || !testMessage.trim()"
              />
            </div>
          </form>
          <div
            v-if="testResult"
            class="integrations-status"
            :role="testFailed ? 'alert' : 'status'"
          >
            {{ testResult }}
          </div>
        </section>
        <SlackIntegration
          v-if="detail.type === 'slack'"
          :integration="detail"
          :hooks="slackHooks"
          :sending="slackSending"
          :result="slackResult"
          :failed="slackFailed"
          v-model:message="slackMessage"
          @select-hook="showDetails"
          @send="sendSlack"
          @clear-result="slackResult = ''"
        />
        <div v-if="detail.comment" class="integrations-comment">
          <strong>{{ t("comment") }}</strong> {{ detail.comment }}
        </div>

        <details class="integrations-advanced">
          <summary>{{ t("connectionAndCli") }}</summary>
          <div class="integrations-copy-line">
            <code>{{ detail.url }}</code>
            <Button
              text
              severity="secondary"
              size="small"
              :label="copied === 'url' ? t('copied') : t('copyEndpoint')"
              @click="copy(detail.url, 'url')"
            />
          </div>
          <div class="integrations-copy-line">
            <code>{{ editCommand(detail) }}</code>
            <Button
              text
              severity="secondary"
              size="small"
              :label="copied === 'edit' ? t('copied') : t('copyEditCommand')"
              @click="copy(editCommand(detail), 'edit')"
            />
          </div>
          <a href="https://exe.dev/docs/cli-integrations" target="_blank" rel="noopener noreferrer"
            >{{ t("cliEditOptions") }} ↗</a
          >
        </details>
        <details
          v-if="detail.help && detail.type !== 'notify'"
          class="integrations-guide"
          :open="!['llm', 'slack'].includes(detail.type) && !detail.details?.repositories?.length"
        >
          <summary>{{ t("howToUseIt") }}</summary>
          <pre>{{ detail.help }}</pre>
          <Button
            text
            severity="secondary"
            size="small"
            :label="copied === 'help' ? t('copied') : t('copyGuide')"
            @click="copy(detail.help, 'help')"
          />
        </details>
      </template>
      <div v-if="copyError" class="integrations-status" role="alert">{{ copyError }}</div>
    </div>

    <template #footer>
      <span class="integrations-note">{{ t("integrationAccountNote") }}</span>
      <a href="https://exe.dev/integrations" target="_blank" rel="noopener noreferrer"
        >{{ t("allIntegrations") }} ↗</a
      >
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import { api, ApiError, type AttachedIntegration } from "../../services/api";
import { useI18n } from "../composables/i18n";
import Modal from "./Modal.vue";
import SlackIntegration from "./SlackIntegration.vue";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close" | "open-models-modal"): void }>();
const { t } = useI18n();
const integrations = ref<AttachedIntegration[]>([]);
const selected = ref<AttachedIntegration | null>(null);
const detail = ref<AttachedIntegration | null>(null);
const tab = ref<"list" | "detail">("list");
const loadingList = ref(false);
const loadingDetail = ref(false);
const error = ref("");
const detailError = ref("");
const copyError = ref("");
const copied = ref("");
const testMessage = ref(t("defaultTestMessage"));
const slackMessage = ref(t("defaultTestMessage"));
const testResult = ref("");
const testFailed = ref(false);
const sendingTest = ref(false);
const slackSending = ref(false);
const slackResult = ref("");
const slackFailed = ref(false);
const busy = computed(() => sendingTest.value || slackSending.value);
const slackHooks = computed(() =>
  integrations.value.filter((integration) => integration.type === "slack"),
);
let listRequest = 0;
let detailRequest = 0;
const llmProviders = computed(() => detail.value?.details?.model_counts ?? []);

function modelSummary(count: {
  chat?: number;
  embeddings?: number;
  transcription?: number;
  other?: number;
}): string {
  return [
    count.chat && `${count.chat} ${t("modelChat")}`,
    count.embeddings && `${count.embeddings} ${t("modelEmbeddings")}`,
    count.transcription && `${count.transcription} ${t("modelTranscription")}`,
    count.other && `${count.other} ${t("modelOther")}`,
  ]
    .filter(Boolean)
    .join(" · ");
}

function editCommand(integration: AttachedIntegration): string {
  const name = `'${integration.name.replace(/'/g, "'\\''")}'`;
  const command = `integrations edit ${name}${integration.team ? " --team" : ""} --comment='<new comment>'`;
  return `ssh exe.dev "${command.replace(/["\\$`]/g, "\\$&")}"`;
}

async function copy(text: string, key: string) {
  const request = detailRequest;
  try {
    await navigator.clipboard.writeText(text);
    if (request !== detailRequest || !props.isOpen) return;
    copied.value = key;
    copyError.value = "";
  } catch (e) {
    if (request !== detailRequest || !props.isOpen) return;
    copyError.value = e instanceof Error ? e.message : t("errorOccurred");
  }
}

async function sendTestNotification() {
  if (sendingTest.value || !testMessage.value.trim()) return;
  const request = detailRequest;
  sendingTest.value = true;
  testResult.value = "";
  try {
    await api.sendTestNotification(testMessage.value.trim());
    if (request !== detailRequest || !props.isOpen) return;
    testFailed.value = false;
    testResult.value = t("testAccepted");
  } catch (e) {
    if (request !== detailRequest || !props.isOpen) return;
    testFailed.value = true;
    testResult.value = e instanceof Error ? e.message : t("errorOccurred");
  } finally {
    sendingTest.value = false;
  }
}

async function sendSlack() {
  const target = detail.value;
  if (slackSending.value || !slackMessage.value.trim() || target?.type !== "slack") return;
  const request = detailRequest;
  slackSending.value = true;
  slackResult.value = "";
  try {
    await api.sendSlackTest(target.name, !!target.team, slackMessage.value.trim());
    if (request !== detailRequest || !props.isOpen) return;
    slackFailed.value = false;
    slackResult.value = t("slackMessageSent").replace("{name}", target.name);
  } catch (e) {
    if (request !== detailRequest || !props.isOpen) return;
    slackFailed.value = true;
    slackResult.value =
      e instanceof ApiError && e.status === 422
        ? t("slackBotTestError")
        : e instanceof Error
          ? e.message
          : t("errorOccurred");
  } finally {
    slackSending.value = false;
  }
}

async function loadList() {
  const request = ++listRequest;
  loadingList.value = true;
  error.value = "";
  try {
    const loaded = await api.getIntegrations();
    if (request !== listRequest || !props.isOpen) return;
    integrations.value = loaded.integrations.sort((a, b) => a.name.localeCompare(b.name));
  } catch (e) {
    if (request !== listRequest || !props.isOpen) return;
    integrations.value = [];
    error.value = e instanceof Error ? e.message : t("errorOccurred");
  } finally {
    if (request === listRequest && props.isOpen) loadingList.value = false;
  }
}

async function showDetails(integration: AttachedIntegration) {
  selected.value = integration;
  tab.value = "detail";

  if (detail.value?.name === integration.name && !!detail.value.team === !!integration.team) return;
  const request = ++detailRequest;
  detail.value = null;
  loadingDetail.value = true;
  detailError.value = "";
  copyError.value = "";
  copied.value = "";
  testResult.value = "";
  slackResult.value = "";
  try {
    const loaded = await api.getIntegrationDetails(integration.name, !!integration.team);
    if (request === detailRequest && props.isOpen) detail.value = loaded;
  } catch (e) {
    if (request === detailRequest && props.isOpen) {
      detailError.value = e instanceof Error ? e.message : t("errorOccurred");
    }
  } finally {
    if (request === detailRequest && props.isOpen) loadingDetail.value = false;
  }
}

async function refresh() {
  if (busy.value || loadingList.value || loadingDetail.value) return;
  if (tab.value === "list") {
    detailRequest++;
    selected.value = null;
    detail.value = null;
    void loadList();
  } else if (selected.value) {
    const previous = selected.value;
    const request = ++detailRequest;
    detail.value = null;
    detailError.value = "";
    loadingDetail.value = true;
    await loadList();
    if (request !== detailRequest || !props.isOpen) return;
    loadingDetail.value = false;
    if (error.value) {
      detailError.value = error.value;
      return;
    }
    const updated = integrations.value.find(
      (integration) => integration.name === previous.name && !!integration.team === !!previous.team,
    );
    selected.value = updated ?? null;
    if (!updated) {
      tab.value = "list";
      return;
    }
    if (tab.value === "detail") void showDetails(updated);
  }
}

watch(
  () => props.isOpen,
  (open) => {
    listRequest++;
    detailRequest++;
    loadingList.value = false;
    loadingDetail.value = false;

    copyError.value = "";
    copied.value = "";
    testResult.value = "";
    slackResult.value = "";
    if (!open) return;
    tab.value = "list";
    selected.value = null;
    detail.value = null;
    void loadList();
  },
);
</script>
