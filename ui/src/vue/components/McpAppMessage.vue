<!-- The view of an MCP tool call (an "mcpapp" message, server/mcp_apps.go):
     the tool's UI resource, hosted per MCP Apps, fed the call's input and
     result. -->
<template>
  <div class="mcp-app-message" data-testid="message-mcpapp" :data-message-id="message.message_id">
    <div class="mcp-app-message-header">
      <span aria-hidden="true">🧩</span>
      <span class="mcp-app-message-name">{{ source }}</span>
    </div>
    <McpAppFrame
      v-if="app && options"
      :html="html"
      :title="source"
      sandbox="allow-scripts allow-forms"
      :allow="allow"
      :prefers-border="app.prefersBorder"
      :options="options"
    />
    <div v-else-if="error" class="mcp-app-message-error" role="status">
      {{ error }}
      <pre v-if="resultText" class="mcp-app-message-result">{{ resultText }}</pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, watchEffect } from "vue";
import type { Message } from "../../types";
import {
  contentText,
  fetchApp,
  withPolicy,
  type AppResource,
  type AppTool,
  type CallToolResult,
} from "../../services/mcpAppHost";
import McpAppFrame from "./McpAppFrame.vue";

const props = defineProps<{ message: Message }>();

interface Data {
  server: string;
  server_id: string;
  resource: string;
  tool: AppTool;
  arguments?: Record<string, unknown> | null;
  result: CallToolResult;
}

const data = computed<Data | null>(() => {
  try {
    const d = JSON.parse(String(props.message.user_data));
    return d?.server && d.resource && d.tool ? d : null;
  } catch {
    return null;
  }
});
const source = computed(() => (data.value ? `${data.value.server}.${data.value.tool.name}` : ""));
const resultText = computed(() => contentText(data.value?.result?.content));

const app = shallowRef<AppResource | null>(null);
const error = ref("");
watchEffect(() => {
  const d = data.value;
  if (!d) {
    error.value = "This view's record is unreadable.";
    return;
  }
  fetchApp(d.server, d.server_id ?? "", d.resource, props.message.conversation_id).then(
    (a) => {
      app.value = a;
      error.value = "";
    },
    (err: Error) => {
      error.value = err.message;
    },
  );
});

const html = computed(() => (app.value ? withPolicy(app.value.html, app.value.policy) : ""));

// Of the permissions a view may ask for, Shelley grants only writing to the
// clipboard; it has no way to ask the user about cameras, microphones, or
// location.
const clipboard = computed(() => !!app.value?.permissions?.clipboardWrite);
const allow = computed(() => (clipboard.value ? "clipboard-write" : undefined));

const options = computed(() => {
  const d = data.value;
  if (!d || !app.value) return null;
  return {
    source: source.value,
    key: props.message.message_id,
    conversationId: props.message.conversation_id,
    server: d.server,
    serverId: d.server_id ?? "",
    tool: d.tool,
    toolCallId: props.message.message_id,
    toolInput: d.arguments ?? {},
    toolResult: d.result,
    sandbox: {
      permissions: clipboard.value ? { clipboardWrite: {} } : {},
      csp: app.value.csp,
    },
  };
});
</script>

<style scoped>
.mcp-app-message {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  margin: 0.5rem 0;
}

.mcp-app-message-header {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}

.mcp-app-message-name {
  font-family: var(--font-mono);
}

.mcp-app-message-error {
  color: var(--error-text);
  font-size: var(--font-size-13);
}

.mcp-app-message-result {
  margin-top: 0.375rem;
  color: var(--text-primary);
  white-space: pre-wrap;
  font-family: var(--font-mono);
  font-size: var(--font-size-12);
}
</style>
