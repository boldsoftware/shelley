<!-- Vue equivalent of utils/inlineText.tsx's renderInlineText(). Renders
     user-typed text with Slack-style backtick formatting: ```fenced``` ->
     <pre class="inline-code-block"><code>, `inline` -> <code class="inline-code">,
     and bare URLs in plain segments become <a class="text-link"> links,
     {{secret:ID}} placeholders SecretPills. URLs
     inside code are not linkified. Mirrors the React output node-for-node. -->
<template>
  <template v-for="(seg, i) in segments" :key="i">
    <pre
      v-if="seg.type === 'codeblock'"
      class="inline-code-block"
    ><code>{{ seg.content }}</code></pre>
    <code v-else-if="seg.type === 'code'" class="inline-code">{{ seg.content }}</code>
    <template v-else>
      <template v-for="(part, k) in splitPlaceholders(seg.content)" :key="k">
        <SecretPill v-if="part.secretId" :id="part.secretId" />
        <template v-else>
          <template v-for="(p, j) in links(part.text)" :key="j">
            <a
              v-if="p.type === 'link' && p.href"
              :href="p.href"
              :target="p.href.startsWith('/c/') ? undefined : '_blank'"
              rel="noopener noreferrer"
              class="text-link"
              >{{ p.content }}</a
            >
            <template v-else>{{ p.content }}</template>
          </template>
        </template>
      </template>
    </template>
  </template>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { parseInlineSegments } from "../../utils/inlineText";
import { localhostLinkOptionsFromInit, parseLinks } from "../../utils/linkify";
import { splitPlaceholders } from "../../utils/secretDetect";
import SecretPill from "./SecretPill.vue";

const props = defineProps<{ text: string; rewriteLocalhostLinks?: boolean }>();
const segments = computed(() => parseInlineSegments(props.text));

function links(text: string) {
  return parseLinks(text, props.rewriteLocalhostLinks ? localhostLinkOptionsFromInit() : undefined);
}
</script>
