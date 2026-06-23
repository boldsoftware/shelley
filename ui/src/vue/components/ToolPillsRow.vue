<!-- Vue port of the ToolPillsRow inner component from ChatInterface.tsx.
     Renders a wrapped row of tool "pills"; clicking a pill opens its detail in
     a Modal. Preserves the tool-pill* class + data-tool-name/testid contract. -->
<template>
  <div class="message message-tool tool-pills-row-wrap">
    <div class="message-content">
      <ul class="tool-pills-row">
        <li
          v-for="(item, idx) in items"
          :key="item.toolUseId || `tool-pill-${idx}-${item.toolName || 'tool'}`"
          class="tool-pill-item"
        >
          <button
            type="button"
            :class="`tool-pill${pillErrored(item) ? ' tool-pill--error' : ''}${isExpanded(item) ? ' tool-pill--expanded' : ''}`"
            :disabled="!item.toolUseId"
            :aria-label="pillLabel(item)"
            aria-haspopup="dialog"
            :aria-expanded="isExpanded(item)"
            :title="pillTitle(item)"
            :data-testid="!item.hasResult ? 'tool-call-running' : 'tool-call-completed'"
            :data-tool-name="item.toolName || 'tool'"
            @click="item.toolUseId && (selectedId = item.toolUseId)"
          >
            <span class="tool-pill-emoji" aria-hidden="true">{{
              toolEmoji(item.toolName || "tool", item.toolInput)
            }}</span>
            <span class="tool-pill-text">{{ headlineFor(item) }}</span>
            <span v-if="!item.hasResult" class="tool-pill-spinner" aria-hidden="true" />
            <span v-if="pillErrored(item)" class="tool-pill-err" aria-hidden="true">✗</span>
          </button>
        </li>
      </ul>
      <Modal
        :is-open="!!selected"
        :title="selected ? toolDisplayName(selectedName) : ''"
        class-name="tool-detail-modal"
        @close="selectedId = null"
      >
        <template v-if="statusState" #title-right>
          <div :class="`tool-detail-status tool-detail-status--${statusState}`">
            <template v-if="statusState === 'running'">
              <span class="tool-detail-status-spinner" aria-hidden="true" />
              <span class="tool-detail-status-label">Running</span>
            </template>
            <template v-else-if="statusState === 'cancelled'">
              <span class="tool-detail-status-glyph" aria-hidden="true">✗</span>
              <span class="tool-detail-status-label">Cancelled</span>
            </template>
            <template v-else-if="statusState === 'failed'">
              <span class="tool-detail-status-glyph" aria-hidden="true">✗</span>
              <span class="tool-detail-status-label">Failed</span>
            </template>
            <template v-else>
              <span class="tool-detail-status-glyph" aria-hidden="true">✓</span>
              <span class="tool-detail-status-label">Success</span>
            </template>
            <span v-if="statusDuration" class="tool-detail-status-time">{{ statusDuration }}</span>
          </div>
        </template>
        <div v-if="selected" class="tool-pill-expanded" :data-tool-name="selectedName">
          <CoalescedToolCall
            :tool-name="selected.toolName || 'Unknown Tool'"
            :tool-input="selected.toolInput"
            :tool-result="selected.toolResult"
            :tool-error="selected.toolError"
            :tool-start-time="selected.toolStartTime"
            :tool-end-time="selected.toolEndTime"
            :has-result="selected.hasResult"
            :display="selected.display"
            :on-comment-text-change="onCommentTextChange"
            :streaming-output="
              selected.toolUseId ? toolProgress[selected.toolUseId]?.output : undefined
            "
          />
        </div>
      </Modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import type { ToolProgress } from "../../types";
import type { CoalescedItem } from "./coalesce";
import {
  toolEmoji,
  toolHeadline,
  toolDisplayName,
  HEADLINE_BUDGET_WIDE,
  HEADLINE_BUDGET_NARROW,
} from "../../utils/toolMeta";
import { provideToolDetail } from "../composables/toolDetail";
import Modal from "./Modal.vue";
import CoalescedToolCall from "./CoalescedToolCall.vue";

const props = defineProps<{
  items: CoalescedItem[];
  onCommentTextChange?: (text: string) => void;
  toolProgress: Record<string, ToolProgress>;
}>();

// The detail modal's tool card starts expanded.
provideToolDetail(true);

const selectedId = ref<string | null>(null);

// Headline character budget keyed to viewport width.
const narrow = ref(
  typeof window !== "undefined" ? window.matchMedia("(max-width: 600px)").matches : false,
);
const mq = typeof window !== "undefined" ? window.matchMedia("(max-width: 600px)") : null;
const onMq = (e: MediaQueryListEvent) => {
  narrow.value = e.matches;
};
onMounted(() => mq?.addEventListener("change", onMq));
onUnmounted(() => mq?.removeEventListener("change", onMq));
const budget = computed(() => (narrow.value ? HEADLINE_BUDGET_NARROW : HEADLINE_BUDGET_WIDE));

const selected = computed(
  () => props.items.find((i) => i.toolUseId && i.toolUseId === selectedId.value) || null,
);
const selectedName = computed(() => selected.value?.toolName || "tool");

function headlineFor(item: CoalescedItem): string {
  return toolHeadline(item.toolName || "tool", item.toolInput, budget.value);
}
function pillErrored(item: CoalescedItem): boolean {
  return !!item.toolError && !!item.hasResult;
}
function isExpanded(item: CoalescedItem): boolean {
  return !!item.toolUseId && item.toolUseId === selectedId.value;
}
function pillLabel(item: CoalescedItem): string {
  const name = item.toolName || "tool";
  const headline = toolHeadline(name, item.toolInput, budget.value);
  const label = headline || name;
  const stateSuffix = !item.hasResult ? ", running" : pillErrored(item) ? ", failed" : "";
  return `${label}${stateSuffix}`;
}
function pillTitle(item: CoalescedItem): string {
  const name = item.toolName || "tool";
  return toolHeadline(name, item.toolInput, budget.value) || name;
}

const statusState = computed<"running" | "cancelled" | "failed" | "success" | null>(() => {
  const s = selected.value;
  if (!s) return null;
  const running = !s.hasResult;
  const resultText = s.toolResult?.[0]?.Text ?? "";
  const cancelled =
    !!s.toolError && !!s.hasResult && resultText.includes("Tool execution cancelled by user");
  const failed = !!s.toolError && !!s.hasResult && !cancelled;
  return running ? "running" : cancelled ? "cancelled" : failed ? "failed" : "success";
});

const statusDuration = computed(() => {
  const s = selected.value;
  if (s && s.hasResult && s.toolStartTime && s.toolEndTime) {
    const diffMs = new Date(s.toolEndTime).getTime() - new Date(s.toolStartTime).getTime();
    return diffMs < 1000 ? `${diffMs}ms` : `${(diffMs / 1000).toFixed(1)}s`;
  }
  return "";
});
</script>
