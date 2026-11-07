<!-- /debug/context/?id=CONVERSATION: the context usage popup's graphs, full
     width, taller, and zoomable, for conversations too long to read in the
     popup. Without an id it lists conversations to pick from. -->
<template>
  <main class="context-graph-page">
    <header class="context-graph-page-header">
      <h1>{{ title }}</h1>
      <a href="/debug/">← Debug</a>
    </header>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <template v-else-if="!conversationId">
      <input v-model="filter" type="search" placeholder="Filter by slug…" autocomplete="off" />
      <ul class="context-graph-page-list">
        <li v-for="c in filtered" :key="c.conversation_id">
          <a :href="`?id=${encodeURIComponent(c.conversation_id)}`">{{
            c.slug || c.conversation_id
          }}</a>
          <span class="muted"> {{ new Date(c.created_at).toLocaleString() }}</span>
        </li>
      </ul>
    </template>
    <p v-else-if="!messages" class="muted">Loading…</p>
    <template v-else>
      <div class="context-graph-page-controls">
        <UsageGraphSwitch v-model="panes" />
        <a v-if="conversation?.slug" :href="`/c/${conversation.slug}`">Open conversation</a>
        <span class="muted"
          >Wheel or pinch zooms, drag pans, double-click or double-tap zooms out.</span
        >
      </div>
      <TokenCostGraph
        :entries="usage.entries"
        :messages="messages"
        :models="models"
        :other-usage-rows="usage.otherRows"
        :conversation-id="conversationId"
        :panes="panes"
        :pane-scale="paneScale"
        zoomable
      />
    </template>
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import type { Conversation, Message, Model } from "../../types";
import { api } from "../../services/api";
import { usageData } from "../../utils/usageData";
import { PANE_HEIGHT, USAGE_PANES, type UsagePane } from "../../utils/usagePanes";
import TokenCostGraph from "./TokenCostGraph.vue";
import UsageGraphSwitch from "./UsageGraphSwitch.vue";

const conversationId = new URLSearchParams(window.location.search).get("id");
const error = ref("");
const conversations = ref<Conversation[]>([]);
const filter = ref("");
const conversation = ref<Conversation | null>(null);
const messages = ref<Message[] | null>(null);
const models = ref<Model[]>([]);
const panes = ref<UsagePane[]>([...USAGE_PANES]);
// The graphs fill the window, leaving room for the controls above and the
// hover readout below.
const CHROME_H = 330;
const windowH = ref(window.innerHeight);
const onResize = () => (windowH.value = window.innerHeight);
window.addEventListener("resize", onResize);
onBeforeUnmount(() => window.removeEventListener("resize", onResize));
const paneScale = computed(() => {
  const natural = panes.value.reduce((h, p) => h + PANE_HEIGHT[p], 0);
  return Math.max(1, (windowH.value - CHROME_H) / (natural || 1));
});

const filtered = computed(() => {
  const q = filter.value.toLowerCase();
  return conversations.value.filter((c) => (c.slug || c.conversation_id).toLowerCase().includes(q));
});
const title = computed(() =>
  conversation.value
    ? `Context · ${conversation.value.slug || conversation.value.conversation_id}`
    : "Context graphs",
);
const usage = computed(() => usageData(messages.value ?? []));

async function load() {
  if (!conversationId) {
    conversations.value = (await api.getConversationsSnapshot()).conversations;
    return;
  }
  const [stream, available] = await Promise.all([
    api.getConversationWithProgress(conversationId),
    api.getModels(),
  ]);
  conversation.value = stream.conversation ?? null;
  messages.value = stream.messages ?? [];
  models.value = available;
  document.title = `${title.value} · Shelley Debug`;
}
load().catch((e: Error) => (error.value = e.message));
</script>
