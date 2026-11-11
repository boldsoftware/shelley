<!-- The Shelley archive store's browse page (server/archive_store.go), served at /archive-store/
     on the store's own port: every source's pushed conversations, read-only,
     with search, a source filter and each source's projects. The selected
     conversation is in the hash (#VM/ID), so links to it can be shared. -->
<template>
  <div class="archive-store-page">
    <aside class="archive-store-list">
      <header class="archive-store-head">
        <h1>Shelley archive store</h1>
        <input
          v-model="query"
          class="archive-store-search"
          type="search"
          placeholder="Search conversations"
          aria-label="Search conversations"
          data-testid="archive-store-search"
        />
        <div class="archive-store-filters">
          <select v-model="sourceVM" aria-label="Source" data-testid="archive-store-source">
            <option value="">All sources</option>
            <option v-for="m in sources" :key="m.source_vm" :value="m.source_vm">
              {{ m.email }} ({{ m.source_vm }})
            </option>
          </select>
          <label><input v-model="archived" type="checkbox" /> Archived</label>
        </div>
      </header>
      <div v-if="error" class="archive-store-error">{{ error }}</div>
      <ul class="archive-store-items" data-testid="archive-store-conversations">
        <li v-for="c in conversations" :key="c.source_vm + '/' + c.conversation_id">
          <a
            :href="'#' + c.source_vm + '/' + c.conversation_id"
            :class="{ 'archive-store-item': true, selected: isSelected(c) }"
          >
            <span class="archive-store-item-title"
              >{{ c.slug || "untitled" }}<em v-if="c.archived"> (archived)</em></span
            >
            <span class="archive-store-item-meta"
              >{{ c.email }}<template v-if="c.project"> · {{ c.project }}</template> ·
              {{ formatDate(c.updated_at) }}</span
            >
            <span v-if="c.snippet" class="archive-store-item-snippet"
              ><template v-for="(seg, i) in renderSnippetSegments(c.snippet)" :key="i"
                ><mark v-if="seg.mark">{{ seg.text }}</mark
                ><template v-else>{{ seg.text }}</template></template
              ></span
            >
          </a>
        </li>
        <li v-if="!conversations.length && !error" class="archive-store-empty">
          {{ query ? "Nothing matches." : "Nothing pushed yet." }}
        </li>
      </ul>
      <section
        v-if="sourceProjects.length"
        class="archive-store-projects"
        data-testid="archive-store-projects"
      >
        <h2>Projects</h2>
        <details v-for="p in sourceProjects" :key="p.name">
          <summary>{{ p.name }}</summary>
          <dl>
            <template v-if="p.cwd"
              ><dt>Directory</dt>
              <dd>{{ p.cwd }}</dd></template
            >
            <template v-if="p.repos?.length"
              ><dt>Repositories</dt>
              <dd>{{ p.repos.map((r) => r.repository).join(", ") }}</dd></template
            >
            <template v-if="p.settings?.model"
              ><dt>Model</dt>
              <dd>{{ p.settings.model }}</dd></template
            >
          </dl>
          <pre v-if="p.instructions">{{ p.instructions }}</pre>
        </details>
      </section>
    </aside>
    <main class="archive-store-view">
      <div v-if="!selected" class="archive-store-placeholder">Pick a conversation.</div>
      <template v-else-if="detail">
        <header class="archive-store-view-head">
          <h2 data-testid="archive-store-title">{{ detail.conversation.slug || "untitled" }}</h2>
          <div class="archive-store-item-meta">
            {{ detail.conversation.email }} ({{ detail.conversation.source_vm }})
            <template v-if="detail.conversation.project">
              · {{ detail.conversation.project }}</template
            >
            <template v-if="detail.conversation.model"> · {{ detail.conversation.model }}</template>
            · started {{ formatDate(detail.conversation.created_at) }}
          </div>
          <div class="archive-store-item-meta">
            <a
              v-if="detail.conversation.parent_conversation_id"
              :href="
                '#' +
                detail.conversation.source_vm +
                '/' +
                detail.conversation.parent_conversation_id
              "
              >Parent conversation</a
            >
            <template v-for="s in detail.subagents" :key="s.conversation_id">
              <a :href="'#' + detail.conversation.source_vm + '/' + s.conversation_id"
                >Subagent: {{ s.slug || s.conversation_id }}</a
              >{{ " " }}
            </template>
            <label><input v-model="toolOutputs" type="checkbox" /> Tool outputs</label>
          </div>
        </header>
        <article class="archive-store-transcript markdown-content" data-testid="archive-store-transcript">
          <MarkdownContent :text="markdown" />
        </article>
      </template>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import type { Message } from "../../types";
import { conversationToMarkdown } from "../../utils/conversationMarkdown";
import MarkdownContent from "./MarkdownContent.vue";
import { renderSnippetSegments } from "./conversationDrawerShared";

interface StoreConversation {
  source_vm: string;
  conversation_id: string;
  parent_conversation_id: string | null;
  slug: string | null;
  project: string;
  model: string | null;
  cwd: string | null;
  archived: boolean;
  created_at: string;
  updated_at: string;
  email: string;
  snippet?: string;
}

interface StoreProject {
  name: string;
  instructions: string;
  cwd: string;
  repos: { repository: string }[] | null;
  settings?: { model?: string };
}

interface Source {
  source_vm: string;
  email: string;
  projects: StoreProject[];
}

interface Detail {
  conversation: StoreConversation;
  subagents: { conversation_id: string; slug: string | null }[];
  messages: Message[];
}

const query = ref("");
const sourceVM = ref("");
const archived = ref(false);
const toolOutputs = ref(false);
const sources = ref<Source[]>([]);
const conversations = ref<StoreConversation[]>([]);
const detail = ref<Detail | null>(null);
const error = ref<string | null>(null);
const selected = ref(parseHash());

function parseHash(): { vm: string; id: string } | null {
  const [vm, id] = decodeURIComponent(window.location.hash.slice(1)).split("/");
  return vm && id ? { vm, id } : null;
}

async function getJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`${resp.status} ${(await resp.text()).trim()}`);
  return (await resp.json()) as T;
}

function fail(err: unknown) {
  error.value = err instanceof Error ? err.message : String(err);
}

let listVersion = 0;
async function loadList() {
  const version = ++listVersion;
  const params = new URLSearchParams();
  if (query.value.trim()) params.set("q", query.value.trim());
  if (sourceVM.value) params.set("vm", sourceVM.value);
  if (archived.value) params.set("archived", "true");
  try {
    const list = await getJSON<StoreConversation[]>(`/archive-store/api/conversations?${params}`);
    if (version !== listVersion) return;
    conversations.value = list;
    error.value = null;
  } catch (err) {
    fail(err);
  }
}

let searchTimer: ReturnType<typeof setTimeout> | undefined;
watch(query, () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void loadList(), 200);
});
watch([sourceVM, archived], () => void loadList());

watch(
  selected,
  async (sel) => {
    detail.value = null;
    if (!sel) return;
    try {
      detail.value = await getJSON<Detail>(
        `/archive-store/api/conversations/${encodeURIComponent(sel.vm)}/${encodeURIComponent(sel.id)}`,
      );
    } catch (err) {
      fail(err);
    }
  },
  { immediate: true },
);

const sourceProjects = computed(
  () => sources.value.find((m) => m.source_vm === sourceVM.value)?.projects ?? [],
);

const markdown = computed(() =>
  detail.value
    ? conversationToMarkdown(undefined, detail.value.messages, {
        includeToolOutputs: toolOutputs.value,
        header: false,
      })
    : "",
);

function isSelected(c: StoreConversation): boolean {
  return selected.value?.vm === c.source_vm && selected.value?.id === c.conversation_id;
}

function formatDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function onHashChange() {
  selected.value = parseHash();
}

onMounted(async () => {
  document.title = "Shelley archive store";
  window.addEventListener("hashchange", onHashChange);
  try {
    sources.value = await getJSON<Source[]>("/archive-store/api/sources");
  } catch (err) {
    fail(err);
  }
  await loadList();
});

onUnmounted(() => {
  window.removeEventListener("hashchange", onHashChange);
  clearTimeout(searchTimer);
});
</script>
