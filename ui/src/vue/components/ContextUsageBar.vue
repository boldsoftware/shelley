<!-- Vue port of the ContextUsageBar inner component from ChatInterface.tsx.
     Preserves the context-usage-bar / chat-context-popup / chat-distill-*
     class contract. Auto-opens once per browser on the long-conversation
     threshold; closes on outside click. -->
<template>
  <div ref="barRef">
    <div
      v-if="showPopup && popupPosition"
      class="chat-context-popup"
      :style="{
        bottom: popupPosition.bottom + 'px',
        right: popupPosition.right + 'px',
        maxWidth: `calc(100vw - ${popupPosition.right + 8}px)`,
      }"
    >
      <div v-if="modelName" class="chat-popup-model-name">{{ modelName }}</div>
      {{ formatTokens(contextWindowSize) }} / {{ formatTokens(maxContextTokens) }} ({{
        percentage.toFixed(1)
      }}%) tokens used
      <div v-if="showLongConversationWarning" class="chat-popup-warning">
        This conversation is getting long.
        <br />
        For best results, start a new conversation.
      </div>
      <div
        v-if="conversationId && (onDistillNewGeneration || onStartNewGeneration)"
        class="chat-distill-container"
      >
        <button
          v-if="onDistillNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleDistillNewGeneration"
        >
          {{ distilling ? "Distilling..." : "Distill in New Generation" }}
        </button>
        <button
          v-if="onStartNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleStartNewGeneration"
        >
          Start New Generation
        </button>
        <div
          class="chat-distill-info"
          title="Yeah, we're trying some stuff. Come to discord and talk about it with us!"
        >
          ⓘ Yeah, we're trying some stuff. Come to discord and talk about it with us!
        </div>
      </div>
    </div>
    <div class="context-usage-bar-container">
      <span
        v-if="showLongConversationWarning"
        class="context-warning-icon"
        title="This conversation is getting long. For best results, start a new conversation."
      >
        ⚠️
      </span>
      <div
        class="context-usage-bar"
        :title="`Context: ${formatTokens(contextWindowSize)} / ${formatTokens(maxContextTokens)} tokens (${percentage.toFixed(1)}%)`"
        @click="showPopup = !showPopup"
      >
        <div
          class="context-usage-fill"
          :style="{ width: clampedPercentage + '%', backgroundColor: barColor }"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";

const props = defineProps<{
  contextWindowSize: number;
  maxContextTokens: number;
  conversationId?: string | null;
  modelName?: string;
  onDistillNewGeneration?: () => Promise<void> | void;
  onStartNewGeneration?: () => Promise<void> | void;
  agentWorking?: boolean;
}>();

const showPopup = ref(false);
const distilling = ref(false);
const barRef = ref<HTMLDivElement | null>(null);
let hasAutoOpened = false;
const popupPosition = ref<{ bottom: number; right: number } | null>(null);

const percentage = computed(() =>
  props.maxContextTokens > 0 ? (props.contextWindowSize / props.maxContextTokens) * 100 : 0,
);
const clampedPercentage = computed(() => Math.min(percentage.value, 100));
const showLongConversationWarning = computed(() => props.contextWindowSize >= 100000);

const barColor = computed(() => {
  if (percentage.value >= 90) return "var(--error-text)";
  if (percentage.value >= 70) return "var(--warning-text, #f59e0b)";
  return "var(--blue-text)";
});

function formatTokens(tokens: number): string {
  if (tokens >= 1000000) return `${(tokens / 1000000).toFixed(1)}M`;
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(0)}k`;
  return tokens.toString();
}

// Auto-open popup once per browser at the long-conversation threshold.
watch(
  [showLongConversationWarning, () => props.agentWorking, () => props.conversationId],
  () => {
    const isMobile = window.innerWidth <= 768;
    if (
      showLongConversationWarning.value &&
      !props.agentWorking &&
      !isMobile &&
      props.conversationId &&
      !hasAutoOpened &&
      localStorage.getItem("shelley_long_convo_popup_shown") !== "1"
    ) {
      hasAutoOpened = true;
      localStorage.setItem("shelley_long_convo_popup_shown", "1");
      showPopup.value = true;
    }
  },
  { immediate: true },
);

function handleClickOutside(e: MouseEvent) {
  if (barRef.value && !barRef.value.contains(e.target as Node)) {
    showPopup.value = false;
  }
}

// Close on outside click + compute fixed popup position when shown.
watch(showPopup, (open) => {
  document.removeEventListener("click", handleClickOutside);
  if (open) {
    document.addEventListener("click", handleClickOutside);
    if (barRef.value) {
      const rect = barRef.value.getBoundingClientRect();
      popupPosition.value = {
        bottom: window.innerHeight - rect.top + 4,
        right: window.innerWidth - rect.right,
      };
    }
  } else {
    popupPosition.value = null;
  }
});

async function handleDistillNewGeneration() {
  if (distilling.value || !props.onDistillNewGeneration) return;
  distilling.value = true;
  try {
    await props.onDistillNewGeneration();
    showPopup.value = false;
  } finally {
    distilling.value = false;
  }
}

async function handleStartNewGeneration() {
  if (distilling.value || !props.onStartNewGeneration) return;
  distilling.value = true;
  try {
    await props.onStartNewGeneration();
    showPopup.value = false;
  } finally {
    distilling.value = false;
  }
}
</script>
