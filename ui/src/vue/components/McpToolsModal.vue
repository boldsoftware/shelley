<!-- An MCP server's tools, as the same cards as the System Prompt view.
     Listing connects to the server, and an open request keeps its session in
     use, so the request is aborted when the dialog closes. -->
<template>
  <Modal
    :is-open="server !== null"
    :title="t('mcpToolsTitle').replace('{name}', name)"
    class-name="modal-wide"
    @close="emit('close')"
  >
    <div v-if="loading" class="models-loading">
      <div class="spinner"></div>
      <span>{{ t("mcpConnecting").replace("{name}", name) }}</span>
    </div>
    <div v-else-if="authRequired" class="models-empty" role="status">
      <p>{{ t("mcpLoginRequired") }}</p>
      <Button
        as="a"
        :href="server?.integration ? 'https://exe.dev/integrations' : server?.login_url"
        target="_blank"
        rel="noopener"
        :label="t(server?.integration ? 'manageOnExeDev' : 'mcpLogIn')"
      />
    </div>
    <div v-else-if="error" class="models-error" role="alert">{{ error }}</div>
    <p v-else-if="tools.length === 0" class="models-empty">{{ t("mcpNoTools") }}</p>
    <div v-else class="system-prompt-card-grid">
      <ToolDescriptionCard
        v-for="tool in tools"
        :key="tool.name"
        :name="tool.name"
        :description="tool.description"
        :parameters="tool.inputSchema"
      />
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ToolDescriptionCard from "./ToolDescriptionCard.vue";
import { useI18n } from "../composables/i18n";
import { ApiError, mcpServersApi, type McpServer, type McpTool } from "../../services/api";

const props = defineProps<{ server: McpServer | null }>();
const emit = defineEmits<{ (e: "close"): void }>();

const { t } = useI18n();

// Kept after closing so the title doesn't blank out while the dialog fades.
const name = ref("");
const tools = ref<McpTool[]>([]);
const loading = ref(false);
const error = ref<string | null>(null);
const authRequired = ref(false);

watch(
  () => props.server,
  async (server, _, onCleanup) => {
    if (server === null) return;
    const target = server;
    let controller: AbortController | null = null;
    name.value = target.name;
    async function load() {
      controller?.abort();
      const request = new AbortController();
      controller = request;
      tools.value = [];
      error.value = null;
      authRequired.value = false;
      loading.value = true;
      try {
        const result = await mcpServersApi.tools(target.name, request.signal);
        if (!request.signal.aborted) tools.value = result;
      } catch (err) {
        if (!request.signal.aborted) {
          authRequired.value =
            err instanceof ApiError &&
            err.status === 401 &&
            !!(target.integration || target.login_url);
          error.value = err instanceof Error ? err.message : String(err);
        }
      }
      if (!request.signal.aborted) loading.value = false;
    }
    const onFocus = () => {
      if (authRequired.value) void load();
    };
    window.addEventListener("focus", onFocus);
    onCleanup(() => {
      controller?.abort();
      window.removeEventListener("focus", onFocus);
    });
    await load();
  },
  { immediate: true },
);
</script>
