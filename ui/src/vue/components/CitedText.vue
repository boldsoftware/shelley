<!-- Vue port of components/CitedText.tsx. Renders one coalesced run of
     adjacent text blocks: marker-augmented markdown plus a numbered Sources
     list in markdown mode, or plain inline-formatted text otherwise. The
     coalescing + marker logic lives in utils/coalesceContent.ts, shared with
     React. The markdown itself goes through MarkdownContent so image
     click-to-comment works here too. -->
<template>
  <div v-if="!renderMarkdown" class="whitespace-pre-wrap break-words">
    <InlineText :text="text" :rewrite-localhost-links="rewriteLocalhostLinks" />
  </div>
  <template v-else>
    <MarkdownContent
      :text="markdownText"
      :message-id="messageId"
      :cache-owner="cacheOwner"
      :run-key="runKey"
      :rewrite-localhost-links="rewriteLocalhostLinks"
      commentable
    />
    <ol v-if="citations.length > 0" class="citation-sources">
      <li v-for="(c, i) in citations" :key="i" class="citation-source">
        <span class="citation-source-num">{{ c.num }}</span>
        <a
          :href="c.url"
          target="_blank"
          rel="noopener noreferrer"
          class="citation-source-link"
          :title="c.url"
          >{{ c.title || c.url }}</a
        >
      </li>
    </ol>
  </template>
  <GoogleSearchSuggestions
    v-for="(html, index) in googleSearchSuggestions"
    :key="index"
    :html="html"
  />
</template>

<script setup lang="ts">
import GoogleSearchSuggestions from "./GoogleSearchSuggestions.vue";
import InlineText from "./InlineText.vue";
import MarkdownContent from "./MarkdownContent.vue";
import type { Citation } from "../../utils/coalesceContent";

defineProps<{
  text: string;
  markdownText: string;
  citations: Citation[];
  renderMarkdown: boolean;
  messageId?: string;
  // See MarkdownContent.vue: bounds the render cache to this run's owning
  // Message, distinguished from other runs in the same message by runKey.
  cacheOwner?: object;
  runKey?: string;
  rewriteLocalhostLinks?: boolean;
  googleSearchSuggestions?: string[];
}>();
</script>
