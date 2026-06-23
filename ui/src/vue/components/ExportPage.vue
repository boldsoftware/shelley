<!-- Vue port of components/ExportPage.tsx. Standalone conversation export view,
     served at /export/<conversation_id>. Fetches the conversation, converts it
     to Markdown on the client (conversationToMarkdown), and shows a split
     editor: editable Markdown source on the left, live-rendered preview on the
     right. Reuses the bundled marked + DOMPurify via MarkdownContent.vue so it
     works offline. Preserves the .export-* class contract, the aria-labels
     "Markdown source" / "Rendered preview" / "Editable markdown" / "Loading",
     the mobile pane toggle, the download + include-tool-outputs checkbox.

     The exportConversationIdFromPath() helper lives in src/vue/export.ts and
     is NOT duplicated here. -->
<template>
  <div v-if="loading" class="export-page export-centered">
    <div class="export-spinner" aria-label="Loading" />
  </div>
  <div v-else-if="error" class="export-page export-centered">
    <div class="export-error">Failed to load conversation: {{ error }}</div>
  </div>
  <div v-else class="export-page">
    <header class="export-bar">
      <div class="export-title" :title="conversation?.slug || 'Conversation'">
        {{ conversation?.slug || "Conversation" }}
      </div>
      <label class="export-opt">
        <input
          type="checkbox"
          :checked="includeToolOutputs"
          @change="onToggleToolOutputs(($event.target as HTMLInputElement).checked)"
        />
        Include tool outputs
      </label>
      <div class="export-tabs" role="tablist">
        <button
          :class="`export-tab${mobilePane === 'edit' ? ' export-tab-active' : ''}`"
          role="tab"
          @click="mobilePane = 'edit'"
        >
          Markdown
        </button>
        <button
          :class="`export-tab${mobilePane === 'preview' ? ' export-tab-active' : ''}`"
          role="tab"
          @click="mobilePane = 'preview'"
        >
          Preview
        </button>
      </div>
    </header>

    <main class="export-panes">
      <section
        :class="`export-pane export-pane-edit${mobilePane === 'edit' ? ' export-pane-shown' : ''}`"
        aria-label="Markdown source"
      >
        <div class="export-pane-head">
          <span class="export-pane-label">Markdown</span>
          <div class="export-pane-actions">
            <button class="export-btn" @click="onCopySource">Copy</button>
            <button class="export-btn export-btn-primary" @click="onDownload">Download .md</button>
          </div>
        </div>
        <textarea
          class="export-src"
          :spellcheck="false"
          :value="source"
          aria-label="Editable markdown"
          @input="onEditSource(($event.target as HTMLTextAreaElement).value)"
        />
      </section>

      <section
        :class="`export-pane export-pane-preview${mobilePane === 'preview' ? ' export-pane-shown' : ''}`"
        aria-label="Rendered preview"
      >
        <div class="export-pane-head">
          <span class="export-pane-label">Preview</span>
          <div class="export-pane-actions">
            <button class="export-btn" @click="onCopyPreview">Copy</button>
          </div>
        </div>
        <article class="export-preview markdown-content">
          <MarkdownContent :text="source" />
        </article>
      </section>
    </main>

    <div v-if="toast" class="export-toast export-toast-show">{{ toast }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { api } from "../../services/api";
import type { Conversation, Message } from "../../types";
import { conversationToMarkdown } from "../../utils/conversationMarkdown";
import MarkdownContent from "./MarkdownContent.vue";

const props = defineProps<{ conversationId: string }>();

type MobilePane = "edit" | "preview";

function filenameFor(conversation: Conversation | null): string {
  const base = (conversation?.slug || "conversation")
    .replace(/[^a-z0-9-_]+/gi, "-")
    .replace(/^-+|-+$/g, "")
    .toLowerCase();
  return `${base || "conversation"}.md`;
}

function download(name: string, text: string, mime: string) {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text);
  }
  // Fallback for non-secure contexts.
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  document.execCommand("copy");
  document.body.removeChild(ta);
}

const conversation = ref<Conversation | null>(null);
const messages = ref<Message[]>([]);
const loading = ref(true);
const error = ref<string | null>(null);

const includeToolOutputs = ref(true);
// The editable source. We seed it from the generated markdown and let the
// user edit; toggling the checkbox regenerates (with an edit guard).
const source = ref("");
const edited = ref(false);
const mobilePane = ref<MobilePane>("edit");
const toast = ref<string | null>(null);

// Load the conversation (React effect on [conversationId]).
watch(
  () => props.conversationId,
  (conversationId) => {
    let cancelled = false;
    loading.value = true;
    api
      .getConversationWithProgress(conversationId)
      .then((resp) => {
        if (cancelled) return;
        conversation.value = resp.conversation ?? null;
        messages.value = (resp.messages ?? []) as Message[];
        loading.value = false;
      })
      .catch((err) => {
        if (cancelled) return;
        error.value = err instanceof Error ? err.message : String(err);
        loading.value = false;
      });
    // Cleanup: cancel if conversationId changes again.
    const stop = watch(
      () => props.conversationId,
      () => {
        cancelled = true;
        stop();
      },
    );
  },
  { immediate: true },
);

// Markdown generated from the current options. Memoized so toggling the
// checkbox is cheap and the edit-guard can compare against it.
const generated = computed(() =>
  conversationToMarkdown(conversation.value ?? undefined, messages.value, {
    includeToolOutputs: includeToolOutputs.value,
  }),
);

// Seed the editor exactly once, when the conversation finishes loading.
const seeded = ref(false);
watch(
  [seeded, loading, error, generated],
  () => {
    if (!seeded.value && !loading.value && !error.value) {
      source.value = generated.value;
      edited.value = false;
      seeded.value = true;
    }
  },
  { immediate: true },
);

watch(conversation, (conv) => {
  if (conv) {
    document.title = `${conv.slug || "Conversation"} \u2014 Export`;
  }
});

let toastTimer: ReturnType<typeof setTimeout> | undefined;
function showToast(msg: string) {
  toast.value = msg;
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toast.value = null), 1600);
}
onUnmounted(() => {
  if (toastTimer) clearTimeout(toastTimer);
});

function onToggleToolOutputs(next: boolean) {
  // Regenerate with the new option. Guard against clobbering hand edits.
  const nextMd = conversationToMarkdown(conversation.value ?? undefined, messages.value, {
    includeToolOutputs: next,
  });
  if (edited.value && source.value !== generated.value) {
    if (!window.confirm("Switching tool outputs will discard your edits. Continue?")) {
      return;
    }
  }
  includeToolOutputs.value = next;
  source.value = nextMd;
  edited.value = false;
}

const filename = computed(() => filenameFor(conversation.value));

function onEditSource(value: string) {
  source.value = value;
  edited.value = true;
}

function onCopySource() {
  copyText(source.value).then(
    () => showToast("Markdown copied"),
    () => showToast("Copy failed"),
  );
}

function onCopyPreview() {
  copyText(source.value).then(
    () => showToast("Copied as text"),
    () => showToast("Copy failed"),
  );
}

function onDownload() {
  download(filename.value, source.value, "text/markdown");
  showToast(`Downloaded ${filename.value}`);
}
</script>
