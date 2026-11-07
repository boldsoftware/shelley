<!-- Renders a local .md file linked as sandbox:<path>; opened by a plain click
     on its download link (see composables/markdownDoc.ts). Uses the file
     editor's full-height frame (EditableFileModal) so the two read as one
     family: name on the left, path on the right, then Download and close. -->
<template>
  <Teleport to="body">
    <div class="diff-viewer-overlay" role="presentation" @click.self="emit('close')">
      <div class="diff-viewer-container" role="dialog" aria-modal="true" :aria-label="name">
        <div class="diff-viewer-header">
          <div class="diff-viewer-header-row">
            <span class="agents-md-header-title">{{ name }}</span>
            <code class="agents-md-header-path markdown-doc-path" :title="doc.path"
              ><bdi>{{ doc.path }}</bdi></code
            >
            <a
              v-tooltip.top="'Download'"
              class="diff-viewer-close"
              :href="doc.url"
              download
              aria-label="Download"
            >
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="2"
                stroke-linecap="round"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <path d="M12 4v11M7 10l5 5 5-5M5 20h14" />
              </svg>
            </a>
            <button
              v-tooltip.top="'Close (Esc)'"
              class="diff-viewer-close"
              aria-label="Close (Esc)"
              @click="emit('close')"
            >
              ×
            </button>
          </div>
        </div>
        <div class="diff-viewer-content markdown-doc-body">
          <div v-if="error" class="diff-viewer-loading">{{ error }}</div>
          <div v-else-if="text === null" class="diff-viewer-loading">Loading…</div>
          <MarkdownContent v-else :text="text" file-preview />
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { responseDetail } from "../../services/responseDetail";
import { onBeforeUnmount, ref } from "vue";
import type { MarkdownDoc } from "../composables/markdownDoc";
import { popModalEscape, pushModalEscape } from "../composables/modalEscapeStack";
import MarkdownContent from "./MarkdownContent.vue";

const props = defineProps<{ doc: MarkdownDoc }>();
const emit = defineEmits<{ (e: "close"): void }>();

const name = props.doc.path.split("/").pop();
const text = ref<string | null>(null);
const error = ref<string | null>(null);

const close = () => emit("close");
pushModalEscape(close);
onBeforeUnmount(() => popModalEscape(close));

void (async () => {
  try {
    const res = await fetch(props.doc.url);
    if (!res.ok) throw new Error(await responseDetail(res));
    text.value = await res.text();
  } catch (e) {
    error.value = `Could not load document: ${e instanceof Error ? e.message : e}`;
  }
})();
</script>

<style scoped>
/* Cut long paths from the start so the directory and name stay visible. */
.markdown-doc-path {
  text-align: right;
  direction: rtl;
}
.markdown-doc-path bdi {
  direction: ltr;
  unicode-bidi: isolate;
}
.diff-viewer-close svg {
  width: 1.05rem;
  height: 1.05rem;
}
.markdown-doc-body {
  overflow: auto;
  padding: 1.25rem 1.5rem;
}
@media (max-width: 640px) {
  .markdown-doc-path {
    text-align: left;
  }
  .markdown-doc-body {
    padding: 1rem;
  }
}
</style>
