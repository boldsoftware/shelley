<!-- Vue port of components/MessageInfoModal.tsx. Lightweight metadata modal for
     messages without token-usage data (e.g. user messages). Uses the shared
     Modal (PrimeVue Dialog) chrome and mirrors UsageDetailModal's grid classes
     (.usage-detail-grid/-label/-value). Mounted only while open (parent v-if). -->
<template>
  <Modal
    :is-open="true"
    title="Message Details"
    class-name="usage-detail-modal"
    @close="emit('close')"
  >
    <div class="usage-detail-grid">
      <div class="usage-detail-label">Type:</div>
      <div class="usage-detail-value">{{ message.type }}</div>
      <template v-if="message.created_at">
        <div class="usage-detail-label">Timestamp:</div>
        <div class="usage-detail-value">{{ formatTimestamp(message.created_at) }}</div>
      </template>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import type { Message } from "../../types";
import Modal from "./Modal.vue";

defineProps<{ message: Message }>();
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
</script>
