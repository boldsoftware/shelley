<!-- Vue equivalent of utils/inlineText.tsx's renderInlineText(). Renders
     user-typed text with Slack-style backtick formatting: ```fenced``` ->
     <pre class="inline-code-block"><code>, `inline` -> <code class="inline-code">,
     and bare URLs in plain segments become <a class="text-link"> links. URLs
     inside code are not linkified. Mirrors the React output node-for-node. -->
<template>
  <template v-for="(seg, i) in segments" :key="i">
    <pre
      v-if="seg.type === 'codeblock'"
      class="inline-code-block"
    ><code>{{ seg.content }}</code></pre>
    <code v-else-if="seg.type === 'code'" class="inline-code">{{ seg.content }}</code>
    <template v-else>
      <template v-for="(p, j) in parseLinks(seg.content)" :key="j">
        <a
          v-if="p.type === 'link'"
          :href="p.href"
          target="_blank"
          rel="noopener noreferrer"
          class="text-link"
          >{{ p.content }}</a
        >
        <template v-else>{{ p.content }}</template>
      </template>
    </template>
  </template>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { parseInlineSegments } from "../../utils/inlineText";
import { parseLinks } from "../../utils/linkify";

const props = defineProps<{ text: string }>();
const segments = computed(() => parseInlineSegments(props.text));
</script>
