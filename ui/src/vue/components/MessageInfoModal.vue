<!-- Vue port of components/MessageInfoModal.tsx. Lightweight metadata modal for
     messages without token-usage data (e.g. user messages). Mirrors
     UsageDetailModal's chrome (.usage-detail-overlay/-modal/-header/-title/
     -close-button/-grid/-label/-value) and the aria-label "Close". -->
<template>
  <Teleport to="body">
    <div class="usage-detail-overlay" @click="emit('close')">
      <div class="usage-detail-modal" @click.stop>
        <div class="usage-detail-header">
          <h2 class="usage-detail-title">Message Details</h2>
          <button class="usage-detail-close-button" aria-label="Close" @click="emit('close')">
            ×
          </button>
        </div>
        <div class="usage-detail-grid">
          <div class="usage-detail-label">Type:</div>
          <div class="usage-detail-value">{{ message.type }}</div>
          <template v-if="message.created_at">
            <div class="usage-detail-label">Timestamp:</div>
            <div class="usage-detail-value">{{ formatTimestamp(message.created_at) }}</div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import type { Message } from "../../types";
import { useEscapeClose } from "../composables/escapeClose";

const props = defineProps<{ message: Message }>();
const emit = defineEmits<{ (e: "close"): void }>();

function formatTimestamp(isoString: string): string {
  const date = new Date(isoString);
  if (Number.isNaN(date.getTime())) return isoString;
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// Mounted only while open (parent v-if), so Escape is always active here.
useEscapeClose(
  () => true,
  () => emit("close"),
);
void props;
</script>
