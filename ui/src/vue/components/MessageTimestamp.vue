<!-- Vue port of components/MessageTimestamp.tsx. Unobtrusive fixed clock-time
     label; the relative "x ago" tooltip is computed lazily on hover/focus.
     Preserves the .message-timestamp-row / .message-timestamp classes and the
     data-testid "message-timestamp". -->
<template>
  <div v-if="isValid" class="message-timestamp-row" data-testid="message-timestamp">
    <time
      class="message-timestamp"
      :datetime="date.toISOString()"
      :title="tooltip ?? formatAbsolute(date)"
      @mouseenter="refreshTooltip"
      @focus="refreshTooltip"
    >
      {{ formatTime(date) }}
    </time>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { formatTime, formatAbsolute, formatRelative } from "../../utils/messageTime";

const props = defineProps<{ createdAt: string }>();

const date = computed(() => new Date(props.createdAt));
const isValid = computed(() => !isNaN(date.value.getTime()));
const tooltip = ref<string | null>(null);

function refreshTooltip() {
  tooltip.value = `${formatAbsolute(date.value)} (${formatRelative(Date.now() - date.value.getTime())})`;
}
</script>
