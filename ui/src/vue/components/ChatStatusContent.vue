<!-- Status-bar content extracted from renderStatusContent() in
     ChatInterface.tsx. Rendered in the standalone status bar (desktop) and
     inline in the message input controls row (mobile). Preserves the
     status-* / context bar / agent-thinking contract. -->
<template>
  <!-- Archived -->
  <template v-if="currentConversation?.archived">
    <span class="status-message">This conversation is archived.</span>
    <button class="status-button status-button-primary" @click="onUnarchive">Unarchive</button>
  </template>

  <!-- Disconnected -->
  <template v-else-if="streamStatus === 'disconnected'">
    <span class="status-message status-warning">Disconnected</span>
  </template>

  <!-- Reconnecting -->
  <template v-else-if="streamStatus === 'reconnecting'">
    <span class="status-message status-reconnecting">
      Reconnecting<span class="reconnecting-dots">...</span>
    </span>
  </template>

  <!-- Error -->
  <template v-else-if="error">
    <span :class="['status-message', models.length === 0 ? 'status-no-models' : 'status-error']">{{
      error
    }}</span>
    <button class="status-button status-button-text" @click="onClearError">
      <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          :stroke-width="2"
          d="M6 18L18 6M6 6l12 12"
        />
      </svg>
    </button>
  </template>

  <!-- Agent working -->
  <div
    v-else-if="agentWorking && conversationId"
    class="status-bar-active"
    data-testid="agent-thinking"
  >
    <div class="status-working-group">
      <AnimatedWorkingStatus />
      <button
        :disabled="cancelling"
        class="status-stop-button"
        v-tooltip.top="'Stop'"
        :aria-label="cancelling ? 'Cancelling...' : 'Stop'"
        @click="onCancel"
      >
        <svg viewBox="0 0 24 24" fill="currentColor">
          <rect x="6" y="6" width="12" height="12" rx="1" />
        </svg>
        <span class="status-stop-label">{{ cancelling ? "Cancelling..." : "Stop" }}</span>
      </button>
    </div>
    <StatusReadout
      v-bind="readoutProps"
      :cwd="cwd"
      :conversation-id="conversationId"
      :agent-working="agentWorking"
    />
  </div>

  <!-- New conversation or draft -->
  <div
    v-else-if="!conversationId || currentConversation?.is_draft"
    class="status-bar-new-conversation"
  >
    <div class="status-field status-field-model">
      <ModelPicker
        :models="models"
        :selected-model="selectedModel"
        :thinking-level="thinkingLevel"
        :disabled="sending"
        :refreshing="refreshingModels"
        @select-model="onSelectModel"
        @thinking-change="onThinkingChange"
        @manage-models="onManageModels"
        @refresh-models="onRefreshModels"
      />
      <div ref="advancedSettingsRef" class="advanced-settings-wrapper">
        <button
          :class="`advanced-settings-trigger${toolOverrideCount > 0 ? ' active' : ''}`"
          v-tooltip.top="'Advanced settings'"
          aria-label="Advanced settings"
          :disabled="sending"
          @click="showAdvancedSettings = !showAdvancedSettings"
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
          >
            <circle cx="12" cy="12" r="3" />
            <path
              d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"
            />
          </svg>
        </button>
        <div
          v-if="showAdvancedSettings"
          ref="advancedPopoverRef"
          class="advanced-settings-popover"
          :style="popoverStyle"
        >
          <div class="advanced-settings-header">
            <span>Tools</span>
            <button
              type="button"
              class="advanced-settings-reset"
              :disabled="toolOverrideCount === 0"
              v-tooltip.top="'Clear all overrides'"
              @click="onResetToolOverrides"
            >
              Reset to defaults
            </button>
          </div>
          <div class="tool-override-list">
            <template v-for="tool in toolOverrideList" :key="tool.name">
              <div class="tool-override-row">
                <div class="tool-override-info">
                  <span class="tool-override-name">{{ tool.name }}</span>
                  <span class="tool-override-summary">{{ tool.summary }}</span>
                </div>
                <div class="tool-override-choices" role="radiogroup">
                  <button
                    v-for="choice in choicesFor(tool)"
                    :key="choice.val"
                    type="button"
                    role="radio"
                    :aria-checked="currentOverride(tool.name) === choice.val"
                    :class="`tool-override-choice${currentOverride(tool.name) === choice.val ? ' active' : ''}`"
                    :disabled="sending"
                    @click="onSetToolOverride(tool.name, choice.val)"
                  >
                    {{ choice.label }}
                  </button>
                </div>
              </div>
            </template>
          </div>
        </div>
      </div>
    </div>
    <div
      :class="`status-field status-field-cwd${cwdError ? ' status-field-error' : ''}`"
      v-tooltip.top="cwdError || 'Working directory for file operations'"
    >
      <span class="status-field-label">{{ t("dirLabel") }}</span>
      <button
        :class="`status-chip${cwdError ? ' status-chip-error' : ''}`"
        :disabled="sending"
        @click="onOpenDirectoryPicker"
      >
        {{ tildifyPath(selectedCwd) || "(no cwd)" }}
      </button>
    </div>
  </div>

  <!-- Active conversation -->
  <div v-else class="status-bar-active">
    <span class="status-message status-ready">
      <span class="hide-on-mobile">Ready on </span>{{ hostname }}
    </span>
    <StatusReadout
      v-bind="readoutProps"
      :cwd="cwd"
      :conversation-id="conversationId"
      :agent-working="agentWorking"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, onUnmounted, nextTick } from "vue";
import type { Conversation } from "../../types";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import { tildifyPath } from "../../utils/tildify";
import { useI18n } from "../composables/i18n";
import type { ThinkingLevel } from "./thinkingLevel";
import AnimatedWorkingStatus from "./AnimatedWorkingStatus.vue";
import ModelPicker from "./ModelPicker.vue";
import StatusReadout from "./StatusReadout.vue";

type ModelInfo = {
  id: string;
  display_name?: string;
  source?: string;
  ready: boolean;
  max_context_tokens?: number;
  supports_reasoning?: boolean;
  reasoning_levels?: Exclude<ThinkingLevel, "default">[];
  default_reasoning_level?: string;
};
type ToolInfo = { name: string; summary: string; default_on: boolean };

const props = defineProps<{
  currentConversation?: Conversation;
  conversationId: string | null;
  streamStatus: "connected" | "reconnecting" | "disconnected";
  error: string | null;
  agentWorking: boolean;
  cancelling: boolean;
  selectedCwd: string;
  contextWindowSize: number;
  maxContextTokens: number;
  usageEntries: UsageEntry[];
  otherUsageRows: OtherUsageRow[];
  hostname: string;
  models: ModelInfo[];
  selectedModel: string;
  sending: boolean;
  refreshingModels: boolean;
  thinkingLevel: ThinkingLevel;
  toolOverrides: Record<string, "on" | "off">;
  toolOverrideList: ToolInfo[];
  toolOverrideCount: number;
  cwdError: string | null;
  // callbacks
  onUnarchive: () => void;
  onClearError: () => void;
  onCancel: () => void;
  onDistillNewGeneration?: () => Promise<void> | void;
  onStartNewGeneration: () => Promise<void> | void;
  onSelectModel: (model: string) => void;
  /** Model / reasoning-level picks from the status readout, which only renders
   *  for an existing conversation — different operations from onSelectModel and
   *  onThinkingChange, which are client-side only (see sendModelCommand in
   *  ChatInterface). */
  onSwitchConversationModel: (model: string) => void;
  onSwitchConversationThinkingLevel: (level: ThinkingLevel) => void;
  onManageModels: () => void;
  onRefreshModels: () => void;
  onThinkingChange: (level: ThinkingLevel) => void;
  onSetToolOverride: (name: string, value: "default" | "on" | "off") => void;
  onResetToolOverrides: () => void;
  onOpenDirectoryPicker: () => void;
  /** Told before the context usage popup opens, so ChatInterface can start
   *  computing the cost graph's usage entries (see usageWanted there). */
  onUsageNeeded: () => void;
}>();

const { t } = useI18n();

// The conversation's cwd once saved, the picked one while it is still a draft.
const cwd = computed(() => props.currentConversation?.cwd || props.selectedCwd);

// Props bundle for the two StatusReadout call sites (idle and agent-working
// branches). Everything here is identical between them; the branch-specific
// bits are passed separately at each site.
const readoutProps = computed(() => ({
  contextWindowSize: props.contextWindowSize,
  maxContextTokens: props.maxContextTokens,
  usageEntries: props.usageEntries,
  otherUsageRows: props.otherUsageRows,
  models: props.models,
  selectedModel: props.selectedModel,
  thinkingLevel: props.thinkingLevel,
  refreshingModels: props.refreshingModels,
  onDistillNewGeneration: props.onDistillNewGeneration,
  onStartNewGeneration: props.onStartNewGeneration,
  onUsageNeeded: props.onUsageNeeded,
  // The readout's cwd segment. Same picker as the composer's cwd chip, but for
  // a conversation that already exists, where the pick has to go through the
  // server (see applyPickedCwd in ChatInterface).
  onChangeConversationCwd: props.onOpenDirectoryPicker,
  onSwitchConversationModel: props.onSwitchConversationModel,
  onSwitchConversationThinkingLevel: props.onSwitchConversationThinkingLevel,
  onManageModels: props.onManageModels,
  onRefreshModels: props.onRefreshModels,
}));

// Local advanced-settings popover state + outside-click close.
const showAdvancedSettings = ref(false);
const advancedSettingsRef = ref<HTMLDivElement | null>(null);
const advancedPopoverRef = ref<HTMLDivElement | null>(null);
// Placement (relative to the gear wrapper) that keeps the popover within the
// viewport. The gear sits toward the left of the status bar, so a static CSS
// anchor either overflows off the left edge (right-anchored) or off the right
// edge on narrow desktop widths (left-anchored) — hence we measure. Height has
// the same problem vertically: the popover opens upward (bottom: 100%) and is
// one row per tool, so on a short window it ran off the top of the screen and
// the first tools in the list were unreachable.
const popoverStyle = ref<Record<string, string>>({});
// Distance from the popover to the gear: mirrors margin-bottom on
// .advanced-settings-popover in styles.css, which is noted there too.
const POPOVER_GAP = 4;
// Roughly three tool rows. Below this the popover is too small to be worth
// anchoring above the gear, so it overlaps the status bar instead (see below).
const POPOVER_MIN_HEIGHT = 120;
function positionPopover() {
  const wrapper = advancedSettingsRef.value;
  const popover = advancedPopoverRef.value;
  if (!wrapper || !popover) return;
  // The mobile media query pins the popover with position:fixed; don't fight
  // it. Use documentElement.clientWidth (scrollbar-excluded) so this boundary
  // matches the CSS @media (max-width: 640px) exactly.
  const viewportWidth = document.documentElement.clientWidth;
  if (viewportWidth <= 640) {
    popoverStyle.value = {};
    return;
  }
  const viewportHeight = document.documentElement.clientHeight;
  const margin = 8;
  const wrapRect = wrapper.getBoundingClientRect();
  const width = popover.offsetWidth;
  const maxLeft = viewportWidth - margin - width;
  // Prefer aligning the popover's left edge to the gear, clamped into view.
  const desiredLeft = Math.max(margin, Math.min(wrapRect.left, maxLeft));
  // All the room there is above the gear, less the gap it opens with and a
  // margin off the top edge. The CSS max-height (70vh) measures the viewport
  // rather than this gap, so it happily overflows the top on a short window;
  // capping here turns the overflow into a scroll instead of a clip. The extra
  // pixel absorbs subpixel skew: these rects are fractional, and the bar can
  // settle by a fraction of a pixel between this measurement and the paint
  // that follows, which is enough to put the popover back over the edge.
  const spaceAbove = wrapRect.top - POPOVER_GAP - margin - 1;
  const minHeight = Math.min(POPOVER_MIN_HEIGHT, Math.max(0, viewportHeight - 2 * margin));
  const maxHeight = Math.max(minHeight, spaceAbove);
  const style: Record<string, string> = {
    left: `${Math.round(desiredLeft - wrapRect.left)}px`,
    right: "auto",
    // Floored, not rounded: rounding a bound *up* spends a fraction of a pixel
    // more room than there is, putting the popover back over the very edge it
    // is being held clear of.
    maxHeight: `${Math.floor(maxHeight)}px`,
  };
  // Honouring that floor means the popover no longer fits above the gear, and
  // `bottom: 100%` would push it back off the top of the screen — the very
  // thing being fixed. Overlap the status bar instead: pin the bottom edge so
  // the top lands on the margin. A popover over the bar is worth more than a
  // two-row one, and more than one that scrolls off-screen. `bottom` positions
  // the margin edge, so POPOVER_GAP comes out of it too — without that the
  // popover sits 4px higher than asked — as does the same spare pixel of
  // subpixel allowance, and flooring rounds it down, away from the top edge.
  if (maxHeight > spaceAbove) {
    style.bottom = `${Math.floor(wrapRect.bottom - margin - maxHeight - POPOVER_GAP - 1)}px`;
  }
  popoverStyle.value = style;
}
// Repositioning is driven by a ResizeObserver rather than window's resize
// event. Both fire on a viewport change, but resize fires before the layout
// that follows it: the status bar sits at the bottom of a flex column, so on a
// resize the gear has not moved yet and positionPopover reads its old rect (a
// window shrunk to 900x320 while open left the popover 25px off the top).
// ResizeObserver callbacks run after layout, so the rect is current.
// documentElement covers the viewport; the status bar row covers the gear
// moving without one, which is a height change of that row and of nothing
// above it (a longer cwd or model name wrapping the row onto two lines). The
// gear's own wrapper is not worth observing: it is a fixed-size box, and a
// ResizeObserver reports size changes, never position ones.
let resizeObserver: ResizeObserver | null = null;
function observeGeometry() {
  stopObservingGeometry();
  if (typeof ResizeObserver === "undefined") return;
  resizeObserver = new ResizeObserver(() => positionPopover());
  resizeObserver.observe(document.documentElement);
  // The popover is absolutely positioned, so its own size never feeds back
  // into the row's — no observer loop.
  const bar = advancedSettingsRef.value?.closest(".status-bar-new-conversation");
  if (bar) resizeObserver.observe(bar);
}
function stopObservingGeometry() {
  resizeObserver?.disconnect();
  resizeObserver = null;
}
function onOutside(e: MouseEvent) {
  if (advancedSettingsRef.value && !advancedSettingsRef.value.contains(e.target as Node)) {
    showAdvancedSettings.value = false;
  }
}
watch(showAdvancedSettings, (open) => {
  document.removeEventListener("mousedown", onOutside);
  stopObservingGeometry();
  if (open) {
    document.addEventListener("mousedown", onOutside);
    nextTick(() => {
      positionPopover();
      observeGeometry();
    });
  } else {
    popoverStyle.value = {};
  }
});
onUnmounted(() => {
  document.removeEventListener("mousedown", onOutside);
  stopObservingGeometry();
});

function currentOverride(name: string): "default" | "on" | "off" {
  return props.toolOverrides[name] || "default";
}
function choicesFor(tool: ToolInfo): { val: "default" | "on" | "off"; label: string }[] {
  return [
    { val: "default", label: `Default (${tool.default_on ? "on" : "off"})` },
    { val: "on", label: "On" },
    { val: "off", label: "Off" },
  ];
}
</script>
