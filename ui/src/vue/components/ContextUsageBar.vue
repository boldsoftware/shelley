<!-- The token-count segment of the status readout ("15k"), where the number is
     the live context window size. Clicking it opens the context usage popup
     (token counts, cost graph, compaction actions), backed by PrimeVue Popover
     so outside-click dismissal and positioning come for free. Keeps the
     chat-context-popup / chat-distill-* class contract (minus the model-name
     header, which the model segment beside this one now owns). Auto-opens once
     per browser on the long-conversation threshold.

     Type (family/size/color) is inherited from the enclosing .status-readout so
     this segment matches the cwd and model segments beside it — with two
     departures, both carrying meaning the segment used to get from a warning
     triangle beside it and a native title attribute: the number takes on a
     warm color as the conversation grows past 100k / 200k / 300k tokens, and
     a dotted underline plus a hover tooltip say that it is clickable. -->
<template>
  <div ref="barRef" class="context-usage-root">
    <Popover
      ref="popoverRef"
      :pt="{
        root: {
          class: 'chat-context-popup',
          id: popupId,
          'aria-label': 'Context usage',
        },
        content: { class: 'chat-context-popup-content' },
      }"
      @show="onPopupShow"
      @hide="popupOpen = false"
    >
      {{ formatTokenCount(contextWindowSize) }} / {{ formatTokenCount(maxContextTokens) }} ({{
        percentage.toFixed(1)
      }}%) tokens used
      <TokenCostGraph
        :entries="usageEntries || []"
        :other-usage-rows="otherUsageRows || []"
        :conversation-id="conversationId"
        :active="popupOpen"
      />
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
          {{ distilling ? "Compacting..." : "Compact Conversation" }}
        </button>
        <button
          v-if="onStartNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleStartNewGeneration"
        >
          Start New Generation
        </button>
      </div>
    </Popover>
    <button
      type="button"
      class="context-usage-label status-readout-control"
      :aria-label="usageTitle"
      aria-haspopup="dialog"
      :aria-expanded="popupOpen"
      :aria-controls="popupOpen ? popupId : undefined"
      v-tooltip.top="usageTooltip"
      @pointerenter="props.onUsageNeeded?.()"
      @focus="props.onUsageNeeded?.()"
      @click="openPopup($event)"
    >
      <span :class="['context-usage-label-tokens', 'status-readout-affordance', usageLevelClass]">{{
        formatTokenCount(contextWindowSize)
      }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import Popover from "primevue/popover";
import { contextUsageLevel, contextUsageLevelLabel } from "../../utils/contextUsage";
import { formatTokenCount } from "../../utils/tokenCostGraph";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import TokenCostGraph from "./TokenCostGraph.vue";

const props = defineProps<{
  contextWindowSize: number;
  maxContextTokens: number;
  conversationId?: string | null;
  usageEntries?: UsageEntry[];
  otherUsageRows?: OtherUsageRow[];
  onDistillNewGeneration?: () => Promise<void> | void;
  onStartNewGeneration?: () => Promise<void> | void;
  /** Called just before the popup opens. The parent computes usageEntries /
   *  otherUsageRows lazily (walking every message and parsing its usage data),
   *  so it needs a beat's warning; the graph renders empty for one tick and
   *  fills in on the next. */
  onUsageNeeded?: () => void;
  agentWorking?: boolean;
}>();

const distilling = ref(false);
// Mirrors the Popover's visibility for aria-expanded. PrimeVue owns the state;
// we only observe its show/hide events (the popover also closes on outside
// click and Escape, which never route through our click handler).
const popupOpen = ref(false);
// The popover panel is teleported out of this subtree, so aria-controls is the
// only thing tying it back to the button. Per-instance: ChatStatusContent is
// rendered twice (standalone status bar + inline in the mobile controls row),
// so a fixed id would be duplicated. Only advertised while open — the panel
// element doesn't exist otherwise, and aria-controls must resolve.
const popupId = useId();
const barRef = ref<HTMLDivElement | null>(null);
const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
let hasAutoOpened = false;

const percentage = computed(() =>
  props.maxContextTokens > 0 ? (props.contextWindowSize / props.maxContextTokens) * 100 : 0,
);
// The token count is the whole warning now: it warms up (amber, orange, red)
// as the conversation grows, rather than a triangle appearing beside it.
const usageLevel = computed(() =>
  contextUsageLevel(props.contextWindowSize, props.maxContextTokens),
);

// The popup's advice and its once-per-browser auto-open fire exactly when the
// count first colors. Same event, said two ways: a number the user might not
// look at, and — once — the sentence explaining what to do about it.
const showLongConversationWarning = computed(() => usageLevel.value !== "");

// A class rather than an inline style so the color lives with the rest of the
// label's styling in styles.css.
const usageLevelClass = computed(() =>
  usageLevel.value ? `context-usage-label-tokens-${usageLevel.value}` : "",
);

// Spelled out for the button's accessible name: the visible label is
// deliberately terse ("15k"), which alone says nothing about what the number is
// or what it is out of. The level is named in words too — it is otherwise
// carried by hue alone, which is no signal at all for some readers.
const usageTitle = computed(() => {
  const used = formatTokenCount(props.contextWindowSize);
  const level = contextUsageLevelLabel(usageLevel.value);
  const suffix = level ? ` — conversation ${level}` : "";
  // A model with no declared context window has no denominator to report;
  // "0 tokens (0.0%)" would read as a limit of zero.
  if (props.maxContextTokens <= 0) return `Context usage: ${used} tokens${suffix}`;
  return (
    `Context usage: ${used} of ${formatTokenCount(props.maxContextTokens)} tokens ` +
    `(${percentage.value.toFixed(1)}%)${suffix}`
  );
});

// The hover hint says what a click does, because nothing else visible here
// can: the segment has to read as part of the "~/dir · 115k · Model" line, so
// it gets a dotted underline and no other affordance. A PrimeVue tooltip
// rather than a native title — title waits about a second, which is long
// enough that most people never see it. Pointer-only: PrimeVue's directive
// binds either focus/blur or mouse events, not both, and the mouse is the case
// that needs the help; a keyboard user gets aria-haspopup="dialog" on the
// button, which announces the same thing.
const usageTooltip = computed(() => `${usageTitle.value}. Click for details.`);

// Warn the parent as early as we can — hover/focus, which precede the click —
// so the usage walk has usually landed by the time the graph mounts.
function openPopup(event: Event) {
  props.onUsageNeeded?.();
  popoverRef.value?.toggle(event);
}

// Every path that makes the graph visible funnels through the Popover's show
// event, including the programmatic auto-open below, so ask again here: the
// hover/focus/click hints above are an optimization, this is the guarantee.
function onPopupShow() {
  popupOpen.value = true;
  props.onUsageNeeded?.();
}

// This component is not remounted on a conversation switch, but the parent
// resets its lazy usage gate on one, so a popup that was already open would
// keep showing the empty graph until dismissed and reopened. Re-ask.
watch(
  () => props.conversationId,
  () => {
    if (popupOpen.value) props.onUsageNeeded?.();
  },
);

// Auto-open popup once per browser at the long-conversation threshold.
// Programmatic open: PrimeVue's show() anchors to event.currentTarget, so pass
// the usage label element explicitly as the target.
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
      // Wait a tick: with { immediate: true } this can fire before mount,
      // when barRef/popoverRef are still null. Only burn the once-per-browser
      // localStorage flag if the popup actually opens.
      void nextTick(() => {
        const anchor = barRef.value?.querySelector<HTMLElement>(".context-usage-label");
        if (!anchor || !popoverRef.value) return;
        localStorage.setItem("shelley_long_convo_popup_shown", "1");
        popoverRef.value.show(new Event("click"), anchor);
      });
    }
  },
  { immediate: true },
);

async function handleDistillNewGeneration() {
  if (distilling.value || !props.onDistillNewGeneration) return;
  distilling.value = true;
  try {
    await props.onDistillNewGeneration();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}

async function handleStartNewGeneration() {
  if (distilling.value || !props.onStartNewGeneration) return;
  distilling.value = true;
  try {
    await props.onStartNewGeneration();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}
</script>
