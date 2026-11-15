<!-- MCP servers, listed like Manage Models, with the add/edit form and the
     tools dialog stacked on top. Servers that require login show their
     login state; logging in happens in another tab, so
     the list reloads when the window regains focus. Phones get no URL and
     Description columns. `loginResult` is the outcome of a login whose
     callback redirected here (see App.vue). -->
<template>
  <Modal :is-open="isOpen" :title="t('mcpServers')" class-name="modal-xwide" @close="emit('close')">
    <template #title-right>
      <div class="models-header-actions">
        <Button
          class="btn-icon"
          text
          severity="secondary"
          v-tooltip.top="t('refresh')"
          :aria-label="t('refresh')"
          :disabled="loading"
          @click="refresh"
        >
          <i class="pi pi-refresh" aria-hidden="true" />
        </Button>
        <Button
          as="a"
          href="/debug/mcp"
          target="_blank"
          rel="noopener"
          size="small"
          severity="secondary"
          :label="t('mcpDebug')"
        />
        <Button size="small" @click="openForm(null)">+ {{ t("addMcpServer") }}</Button>
      </div>
    </template>

    <div class="models-modal" :class="{ 'models-modal-list': showList }">
      <div
        v-if="notice"
        :class="notice.ok ? 'models-notice' : 'models-error'"
        :role="notice.ok ? 'status' : 'alert'"
      >
        {{ notice.text }}
        <button class="models-error-dismiss" :aria-label="t('dismiss')" @click="notice = null">
          ×
        </button>
      </div>

      <div v-if="loading" class="models-loading">
        <div class="spinner"></div>
        <span>{{ t("loadingMcpServers") }}</span>
      </div>

      <div v-else-if="servers.length === 0" class="models-empty">
        <p>{{ t("noMcpServers") }}</p>
        <p class="models-empty-hint">{{ t("noMcpServersHint") }}</p>
      </div>

      <DataTable
        v-else
        :value="servers"
        data-key="name"
        size="small"
        scrollable
        scroll-height="flex"
        :dt="modelsTableDt"
        class="models-datatable"
      >
        <Column :header="t('columnName')" field="name">
          <template #body="{ data }">
            <span class="models-cell-name">{{ data.name }}</span>
          </template>
        </Column>
        <Column v-if="servers.some((s) => s.auth)" :header="t('mcpColumnLogin')">
          <template #body="{ data }">
            <div v-if="data.auth === 'logged_in'" class="mcp-login">
              <span class="mcp-pill mcp-pill-ok">{{ t("mcpLoggedIn") }}</span>
              <Button
                size="small"
                text
                severity="secondary"
                :label="t('mcpLogOut')"
                @click="run(mcpServersApi.logout(data.name))"
              />
            </div>
            <div v-else-if="data.auth === 'login_required' && data.login_url" class="mcp-login">
              <span class="mcp-pill mcp-pill-warn">
                {{ t("mcpLoginRequired") }}
              </span>
              <Button
                as="a"
                :href="data.login_url"
                target="_blank"
                rel="noopener"
                size="small"
                text
                :label="t('mcpLogIn')"
              />
            </div>
          </template>
        </Column>
        <Column header="URL" field="url" class="hide-on-mobile">
          <template #body="{ data }">
            <div class="models-cell-mono mcp-cell-clip" :title="data.url">{{ data.url }}</div>
          </template>
        </Column>
        <Column :header="t('columnDescription')" field="description" class="hide-on-mobile">
          <template #body="{ data }">
            <div class="mcp-cell-clip" :title="data.description">{{ data.description }}</div>
          </template>
        </Column>
        <Column class="models-col-actions" frozen align-frozen="right">
          <template #header>
            <span class="sr-only">{{ t("columnActions") }}</span>
          </template>
          <template #body="{ data }">
            <div
              v-if="!data.integration && pendingDelete === data.name"
              class="models-cell-actions"
            >
              <Button
                size="small"
                severity="danger"
                :label="t('delete_')"
                @click="remove(data.name)"
              />
              <Button
                size="small"
                text
                severity="secondary"
                :label="t('cancel')"
                @click="pendingDelete = null"
              />
            </div>
            <div v-else class="models-cell-actions">
              <Button size="small" text :label="t('mcpTools')" @click="toolsServer = data" />
              <Button
                v-if="!data.integration"
                class="btn-icon"
                text
                severity="secondary"
                v-tooltip.top="t('edit')"
                :aria-label="`${t('edit')} ${data.name}`"
                @click="openForm(data)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                  />
                </svg>
              </Button>
              <Button
                v-if="!data.integration"
                class="btn-icon btn-danger"
                text
                severity="danger"
                v-tooltip.top="t('delete_')"
                :aria-label="`${t('delete_')} ${data.name}`"
                @click="pendingDelete = data.name"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                  />
                </svg>
              </Button>
            </div>
          </template>
        </Column>
      </DataTable>
    </div>
  </Modal>

  <McpServerFormModal
    :is-open="formOpen"
    :edit-server="editServer"
    @saved="load"
    @close="formOpen = false"
  />
  <McpToolsModal :server="toolsServer" @close="closeTools" />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import McpServerFormModal from "./McpServerFormModal.vue";
import McpToolsModal from "./McpToolsModal.vue";
import { modelsTableDt } from "./modelsTableDt";
import { useI18n } from "../composables/i18n";
import { mcpServersApi, type McpServer } from "../../services/api";

const props = defineProps<{
  isOpen: boolean;
  loginResult: { name: string | null; error: string | null } | null;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

const { t } = useI18n();

const servers = ref<McpServer[]>([]);
const loading = ref(true);
const notice = ref<{ text: string; ok?: boolean } | null>(null);
const pendingDelete = ref<string | null>(null);
const formOpen = ref(false);
const editServer = ref<McpServer | null>(null);
const toolsServer = ref<McpServer | null>(null);

const showList = computed(() => !loading.value && servers.value.length > 0);

const errorNotice = (err: unknown) => ({ text: err instanceof Error ? err.message : String(err) });

async function load() {
  try {
    servers.value = await mcpServersApi.list();
  } catch (err) {
    notice.value = errorNotice(err);
  } finally {
    loading.value = false;
  }
}

async function refresh() {
  notice.value = null;
  loading.value = true;
  await load();
}

// Runs a row action (delete, log out), then reloads the list.
async function run(action: Promise<void>) {
  notice.value = null;
  try {
    await action;
  } catch (err) {
    notice.value = errorNotice(err);
  }
  await load();
}

function remove(name: string) {
  pendingDelete.value = null;
  run(mcpServersApi.remove(name));
}

// Listing tools may have found that the server needs a login.
function closeTools() {
  toolsServer.value = null;
  load();
}

function openForm(server: McpServer | null) {
  editServer.value = server;
  formOpen.value = true;
}

// The callback's error message names the server.
function loginNotice(r: typeof props.loginResult) {
  if (!r) return null;
  if (r.error !== null) return { text: r.error };
  return { ok: true, text: t("mcpLoggedInTo").replace("{name}", r.name ?? "") };
}

const onFocus = () => props.isOpen && load();
onMounted(() => window.addEventListener("focus", onFocus));
onBeforeUnmount(() => window.removeEventListener("focus", onFocus));

watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    loading.value = true;
    pendingDelete.value = null;
    notice.value = loginNotice(props.loginResult);
    load();
  },
  { immediate: true },
);
</script>
