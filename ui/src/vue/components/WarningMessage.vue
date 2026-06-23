<!-- Sub-component of Message.vue (ported from WarningMessage in
     components/Message.tsx). Preserves .message.message-warning, data-testid
     "message-warning", role="status", and the .message-content wrapper. -->
<template>
  <div class="message message-warning" data-testid="message-warning" role="status">
    <div class="message-content">{{ warningText }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";

const props = defineProps<{ message: MessageType }>();

const warningText = computed(() => {
  if (!props.message.user_data) return "Warning";
  try {
    const userData = JSON.parse(props.message.user_data);
    const text = userData.text || "Warning";
    if (userData.suppression_text) {
      return `${text} ${userData.suppression_text}`;
    }
    return text;
  } catch {
    return "Warning";
  }
});
</script>
