<!-- The token-count segment of the status readout ("15k"). Opens token/cost
     graphs and manual compaction actions. The count warms up at 100k / 200k /
     300k tokens (or 70/80/90% of a known context window), unless auto
     compaction is on; the popup advises compacting later (see
     longConversationAdvice). -->
<template>
  <div ref="barRef" class="context-usage-root">
    <Popover
      ref="popoverRef"
      :pt="{
        root: {
          class: 'chat-context-popup',
          id: popupId,
          'aria-label': 'Context usage',
          style: { '--context-popup-available-height': popupAvailableHeight },
        },
        content: { class: 'chat-context-popup-content' },
      }"
      @show="onPopupShow"
      @hide="popupOpen = false"
    >
      <div class="usage-popup-header">
        <div class="usage-popup-title">{{ popupTitle }}</div>
        <div v-if="usageEntries && usageEntries.length > 0" class="usage-popup-actions">
          <UsageGraphSwitch v-model="panes" />
          <a
            v-if="conversationId"
            :href="`/debug/context/?id=${encodeURIComponent(conversationId)}`"
            target="_blank"
            rel="noopener"
            class="usage-popup-expand"
            aria-label="Open the graphs full size"
            title="Open the graphs full size"
            ><i class="pi pi-external-link" aria-hidden="true"
          /></a>
        </div>
      </div>
      <TokenCostGraph
        v-if="popupOpen"
        :entries="usageEntries || []"
        :messages="messages || []"
        :models="models"
        :other-usage-rows="otherUsageRows || []"
        :conversation-id="conversationId"
        :panes="panes"
        active
      />
      <div v-if="showLongConversationWarning" class="chat-popup-warning">
        This conversation is getting long.
        <br />
        Compact it or start a new conversation.
      </div>
      <div v-if="conversationId" class="chat-distill-container">
        <button
          :disabled="actionPending || compactInPlaceBusy"
          class="chat-distill-button chat-distill-generation-button"
          data-testid="compact-in-place-button"
          :aria-describedby="compactInPlaceBusy ? compactInPlaceHintId : undefined"
          @click="handleCompactInPlace"
        >
          Compact in Place
        </button>
        <span v-if="compactInPlaceBusy" :id="compactInPlaceHintId" class="token-cost-graph-note">
          Finish or stop the current turn to compact in place.
        </span>
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
import { computed, ref, useId, watch } from "vue";
import Popover from "primevue/popover";
import type { Message, Model } from "../../types";
import {
  contextUsageLevel,
  contextUsageLevelLabel,
  longConversationAdvice,
} from "../../utils/contextUsage";
import { formatTokenCount } from "../../utils/tokenCostGraph";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import { useUsagePanesPreference } from "../composables/usagePanesPreference";
import TokenCostGraph from "./TokenCostGraph.vue";
import UsageGraphSwitch from "./UsageGraphSwitch.vue";

const props = defineProps<{
  contextWindowSize: number;
  /** Model context window (models.dev, pricing-tier clamped); 0 when unknown.
   *  Never displayed — only floors the warning color as the window fills. */
  maxContextTokens: number;
  conversationId?: string | null;
  usageEntries?: UsageEntry[];
  models: Model[];
  otherUsageRows?: OtherUsageRow[];
  messages?: Message[];
  /** Asks the agent to use compact_in_place, enabling the tool first if the
   *  conversation was started without it. */
  onCompactInPlace: () => Promise<void> | void;
  /** The tool still has to be enabled, which waits for the turn to end. */
  compactInPlaceBusy?: boolean;
  /** The agent is told its context size, so it compacts on its own: the
   *  count stays plain and the popup gives no advice. */
  contextNudges?: boolean;
  /** Called just before the popup opens. The parent computes usageEntries /
   *  otherUsageRows lazily (walking every message and parsing its usage data),
   *  so it needs a beat's warning; the graph renders empty for one tick and
   *  fills in on the next. */
  onUsageNeeded?: () => void;
}>();

const actionPending = ref(false);
// Which graphs the popup stacks; remembered across page loads.
const { panes } = useUsagePanesPreference();
const popupOpen = ref(false);
const popupAvailableHeight = ref<string>();
const popupId = useId();
const compactInPlaceHintId = useId();
const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const barRef = ref<HTMLElement | null>(null);

// The token count is the whole warning: it warms up (amber, orange, red) as
// the conversation grows, rather than a triangle appearing beside it.
const usageLevel = computed(() =>
  contextUsageLevel(props.contextWindowSize, props.maxContextTokens, !!props.contextNudges),
);
const usageLevelClass = computed(() =>
  usageLevel.value ? `context-usage-label-tokens-${usageLevel.value}` : "",
);
const showLongConversationWarning = computed(() =>
  longConversationAdvice(props.contextWindowSize, props.maxContextTokens, !!props.contextNudges),
);

// Spelled out for the accessible name; the level is named in words too since
// hue alone is no signal for some readers.
const usageTitle = computed(() => {
  const level = contextUsageLevelLabel(usageLevel.value);
  const suffix = level ? ` — conversation ${level}` : "";
  return `Context usage: ${formatTokenCount(props.contextWindowSize)} tokens${suffix}`;
});
const popupTitle = computed(
  () => `Current context: ${formatTokenCount(props.contextWindowSize)} tokens`,
);
const usageTooltip = computed(() => `${usageTitle.value}. Click for details.`);

// Warn the parent as early as we can — hover/focus, which precede the click —
// so the usage walk has usually landed by the time the graph mounts.
function openPopup(event: Event) {
  props.onUsageNeeded?.();
  popoverRef.value?.toggle(event);
}

function onPopupShow() {
  // Keep a tall breakdown above its trigger, including on short viewports.
  // Otherwise Popover clamps it to the viewport top and can cover the label.
  if (barRef.value) {
    popupAvailableHeight.value = `${Math.max(0, barRef.value.getBoundingClientRect().top - 16)}px`;
  }
  popupOpen.value = true;
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

async function handleCompactInPlace() {
  if (actionPending.value) return;
  actionPending.value = true;
  try {
    await props.onCompactInPlace();
    popoverRef.value?.hide();
  } finally {
    actionPending.value = false;
  }
}
</script>
