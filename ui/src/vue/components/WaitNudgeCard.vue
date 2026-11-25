<!-- A wait nudge: Shelley's note to the agent that delegated work it waits on,
     a background job or a subagent, has gone long without news. A tool card
     with the note's first line, expanding into the note in full. The same
     card renders persisted and queued nudges. -->
<template>
  <div class="tool" data-testid="wait-nudge-card">
    <div class="tool-header" @click="isExpanded = !isExpanded">
      <div class="tool-summary">
        <span class="tool-emoji">⏰</span>
        <span class="tool-tag">nudge</span>
        <span class="tool-command" data-testid="wait-nudge-headline">{{ firstLine }}</span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <ToolChevron :expanded="isExpanded" />
      </button>
    </div>
    <div v-if="isExpanded" class="tool-details">
      <div v-if="subagent" class="tool-section">
        <div class="tool-label">Subagent: <ConversationSourceLink :source="subagent" /></div>
      </div>
      <div class="tool-section">
        <div class="tool-label">Message to the agent:</div>
        <div class="tool-code">{{ text }}</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { ConversationMessageSource, WaitNudgeSource } from "../../utils/messageSource";
import ConversationSourceLink from "./ConversationSourceLink.vue";
import ToolChevron from "./tools/ToolChevron.vue";

const props = defineProps<{ source: WaitNudgeSource; text: string }>();

const isExpanded = ref(false);
const firstLine = computed(() => props.text.trim().split(/\r?\n/)[0]);
const subagent = computed<ConversationMessageSource | null>(() =>
  props.source.kind === "subagent"
    ? { conversationId: props.source.subject, slug: props.source.slug, relationship: "subagent" }
    : null,
);
</script>
