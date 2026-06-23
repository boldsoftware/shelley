<!-- Vue port of components/MarkdownContent.tsx. Renders sanitized markdown HTML
     via v-html. The pure pipeline lives in utils/markdownRender.ts and is
     shared with the React component. Preserves the .markdown-content
     .break-words container contract. -->
<template>
  <div class="markdown-content break-words" v-html="html"></div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { renderMarkdownToSafeHTML } from "../../utils/markdownRender";

const props = defineProps<{
  text: string;
  // When set, local-path markdown images (relative or absolute file paths) are
  // rewritten to the per-message file endpoint and rendered. Without it we
  // cannot authorize a local file, so such images are dropped.
  messageId?: string;
}>();

const html = computed(() => renderMarkdownToSafeHTML(props.text, props.messageId));
</script>
