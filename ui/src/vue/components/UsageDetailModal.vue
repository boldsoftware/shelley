<!-- Vue port of components/UsageDetailModal.tsx. Token/cost/duration breakdown
     for an agent message's usage data. Preserves the .usage-detail-* class
     contract and the aria-label "Close". -->
<template>
  <Teleport to="body">
    <div class="usage-detail-overlay" @click="emit('close')">
      <div class="usage-detail-modal" @click.stop>
        <div class="usage-detail-header">
          <h2 class="usage-detail-title">Usage Details</h2>
          <button class="usage-detail-close-button" aria-label="Close" @click="emit('close')">
            ×
          </button>
        </div>
        <div class="usage-detail-grid">
          <template v-if="usage.model">
            <div class="usage-detail-label">Model:</div>
            <div class="usage-detail-value">{{ usage.model }}</div>
          </template>
          <div class="usage-detail-label">Input Tokens:</div>
          <div class="usage-detail-value">{{ usage.input_tokens.toLocaleString() }}</div>
          <template v-if="usage.cache_read_input_tokens > 0">
            <div class="usage-detail-label">Cache Read:</div>
            <div class="usage-detail-value">
              {{ usage.cache_read_input_tokens.toLocaleString() }}
            </div>
          </template>
          <template v-if="usage.cache_creation_input_tokens > 0">
            <div class="usage-detail-label">Cache Write:</div>
            <div class="usage-detail-value">
              {{ usage.cache_creation_input_tokens.toLocaleString() }}
            </div>
          </template>
          <div class="usage-detail-label">Output Tokens:</div>
          <div class="usage-detail-value">{{ usage.output_tokens.toLocaleString() }}</div>
          <template v-if="usage.cost_usd > 0">
            <div class="usage-detail-label">Cost:</div>
            <div class="usage-detail-value">${{ usage.cost_usd.toFixed(4) }}</div>
          </template>
          <template v-if="durationMs !== null">
            <div class="usage-detail-label">Duration:</div>
            <div class="usage-detail-value">{{ formatDuration(durationMs!) }}</div>
          </template>
          <template v-if="usage.end_time">
            <div class="usage-detail-label">Timestamp:</div>
            <div class="usage-detail-value">{{ formatTimestamp(usage.end_time) }}</div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import type { Usage } from "../../types";
import { useEscapeClose } from "../composables/escapeClose";

const props = defineProps<{
  usage: Usage;
  durationMs: number | null;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(2)}s`;
  return `${(ms / 60000).toFixed(2)}m`;
}

function formatTimestamp(isoString: string): string {
  const date = new Date(isoString);
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

useEscapeClose(
  () => true,
  () => emit("close"),
);
void props;
</script>
