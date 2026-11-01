<!-- Sub-component of Message.vue (ported from WarningMessage in
     components/Message.tsx). Preserves .message.message-warning, data-testid
     "message-warning", role="status", and the .message-content wrapper. -->
<template>
  <div
    class="message message-warning"
    :class="{ 'message-routing-warning': routing, 'routing-restored': routing?.recovered }"
    data-testid="message-warning"
    role="status"
  >
    <div class="message-content">
      <template v-if="routing">
        <span class="routing-warning-icon" aria-hidden="true">{{ routing.recovered ? "↩" : "↳" }}</span>
        <div class="routing-warning-body">
          <strong class="routing-warning-title">{{ routing.title }}</strong>
          <div class="routing-warning-models">
            <span class="msg-modelchange-chip msg-modelchange-from">{{ routing.from }}</span>
            <span aria-hidden="true">→</span>
            <span class="msg-modelchange-chip msg-modelchange-to">{{ routing.to }}</span>
          </div>
          <details class="routing-warning-details">
            <summary>Details</summary>
            <p>{{ routing.detail }}</p>
          </details>
        </div>
      </template>
      <template v-else>{{ warningText }}</template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Message as MessageType } from "../../types";

const props = defineProps<{ message: MessageType }>();

interface RoutingNotice {
  title: string;
  from: string;
  to: string;
  detail: string;
  recovered: boolean;
}

interface WarningData {
  text?: string;
  suppression_text?: string;
  routing?: RoutingNotice;
}

const data = computed<WarningData>(() => {
  if (!props.message.user_data) return {};
  try {
    return JSON.parse(props.message.user_data) ?? {};
  } catch {
    return {};
  }
});

const routing = computed(() => data.value.routing);
const warningText = computed(() => {
  const text = data.value.text || "Warning";
  return data.value.suppression_text ? `${text} ${data.value.suppression_text}` : text;
});
</script>

<style scoped>
.message-routing-warning .message-content {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  padding: 10px;
  border-radius: 7px;
  font-size: var(--font-size-12);
  line-height: 1.5;
  white-space: normal;
}

.routing-warning-icon {
  flex-shrink: 0;
  font-size: 17px;
}

.routing-warning-body {
  flex: 1;
  min-width: 0;
}

.routing-warning-title {
  display: block;
  margin-bottom: 6px;
  font-weight: 650;
}

.routing-warning-models {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 5px;
}

.routing-warning-models .msg-modelchange-chip {
  font-size: var(--font-size-11);
}

.message-routing-warning:not(.routing-restored) .msg-modelchange-chip {
  color: inherit;
  border-color: var(--warning-border);
  background: transparent;
}

.message-routing-warning:not(.routing-restored) .msg-modelchange-to {
  font-weight: 650;
  background: var(--warning-bg);
}

.routing-warning-details {
  margin-top: 6px;
  font-size: var(--font-size-11);
}

.routing-warning-details summary {
  width: fit-content;
  cursor: pointer;
  opacity: 0.85;
}

.routing-warning-details[open] summary {
  margin-bottom: 8px;
}

.routing-warning-details summary:focus-visible {
  outline: 2px solid currentColor;
  outline-offset: 3px;
  border-radius: 2px;
}

.routing-warning-details p {
  margin: 6px 0;
}

.routing-restored .message-content {
  color: var(--text-primary);
  background: var(--bg-tertiary);
  border-color: transparent;
}
</style>
