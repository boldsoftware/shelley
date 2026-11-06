<!-- A Block Kit element in a preview: a button, which links to its URL if it
     has one and otherwise says what a click would send; an image, as a link,
     since remote images are not loaded (as in markdown); or text. -->
<template>
  <component
    :is="href ? 'a' : 'span'"
    v-if="element.type === 'button'"
    :class="['slack-block-button', element.style && `slack-block-button--${element.style}`]"
    :href="href"
    :target="href && '_blank'"
    :rel="href && 'noopener noreferrer'"
    :title="element.title"
    >{{ emojify(element.text) }}</component
  >
  <component
    :is="href ? 'a' : 'span'"
    v-else-if="element.type === 'image'"
    class="slack-block-image"
    :href="href"
    :target="href && '_blank'"
    :rel="href && 'noopener noreferrer'"
    :title="element.url"
    >🖼️ {{ element.alt || "image" }}</component
  >
  <SlackText
    v-else-if="element.type === 'text'"
    :text="element.text.text"
    :plain="!element.text.mrkdwn"
  />
  <span v-else class="slack-block-other">[{{ element.name }}]</span>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { SlackElementView } from "../../utils/slack";
import { emojify } from "../../utils/slackMrkdwn";
import SlackText from "./SlackText.vue";

const props = defineProps<{ element: SlackElementView }>();

const href = computed(() => {
  const el = props.element;
  const url = el.type === "button" || el.type === "image" ? el.url : "";
  return /^https?:\/\//i.test(url) ? url : undefined;
});
</script>

<style scoped>
.slack-block-button {
  display: inline-block;
  padding: 0.25rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 0.375rem;
  background: var(--bg-base);
  color: var(--text-primary);
  font-size: var(--font-size-13);
  font-weight: 600;
  text-decoration: none;
  white-space: nowrap;
}
a.slack-block-button:hover {
  background: var(--bg-hover);
}
.slack-block-button--primary {
  border-color: #007a5a;
  background: #007a5a;
  color: #fff;
}
a.slack-block-button--primary:hover {
  background: #148567;
}
.slack-block-button--danger {
  border-color: var(--error-border);
  color: var(--error-text);
}
.slack-block-image {
  color: var(--link-color);
  font-size: var(--font-size-13);
}
.slack-block-other {
  color: var(--text-tertiary);
  font-size: var(--font-size-12);
}
</style>
