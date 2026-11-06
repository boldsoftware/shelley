<!-- A preview of Block Kit blocks, roughly as Slack lays them out. -->
<template>
  <div class="slack-blocks" data-testid="slack-blocks">
    <template v-for="(block, i) in views" :key="i">
      <div v-if="block.type === 'header'" class="slack-block-header">
        {{ emojify(block.text) }}
      </div>
      <div v-else-if="block.type === 'section'" class="slack-block-section">
        <div class="slack-block-section-body">
          <SlackText v-if="block.text" :text="block.text.text" :plain="!block.text.mrkdwn" />
          <div v-if="block.fields.length" class="slack-block-fields">
            <SlackText
              v-for="(field, j) in block.fields"
              :key="j"
              :text="field.text"
              :plain="!field.mrkdwn"
            />
          </div>
        </div>
        <SlackBlockElement v-if="block.accessory" :element="block.accessory" />
      </div>
      <hr v-else-if="block.type === 'divider'" class="slack-block-divider" />
      <div v-else-if="block.type === 'context'" class="slack-block-context">
        <SlackBlockElement v-for="(el, j) in block.elements" :key="j" :element="el" />
      </div>
      <div v-else-if="block.type === 'image'">
        <SlackBlockElement
          :element="{ type: 'image', url: block.url, alt: block.title || block.alt }"
        />
      </div>
      <div v-else-if="block.type === 'actions'" class="slack-block-actions">
        <SlackBlockElement v-for="(el, j) in block.elements" :key="j" :element="el" />
      </div>
      <SlackBlockElement v-else :element="block" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { slackBlocks } from "../../utils/slack";
import { emojify } from "../../utils/slackMrkdwn";
import SlackBlockElement from "./SlackBlockElement.vue";
import SlackText from "./SlackText.vue";

const props = defineProps<{ blocks: unknown }>();
const views = computed(() => slackBlocks(props.blocks));
</script>

<style scoped>
.slack-blocks {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.slack-block-header {
  font-weight: 700;
  font-size: 1.0625rem;
}
.slack-block-section {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
}
.slack-block-section-body {
  flex: 1;
  min-width: 0;
}
.slack-block-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.25rem 1rem;
  margin-top: 0.25rem;
}
.slack-block-divider {
  width: 100%;
  margin: 0;
  border: 0;
  border-top: 1px solid var(--border);
}
.slack-block-context {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.25rem 0.5rem;
  color: var(--text-secondary);
  font-size: var(--font-size-12);
}
.slack-block-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
</style>
