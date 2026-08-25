<!-- Right-hand status-bar readout: "~/dir · 15k · model-name".

     Three segments, dot-separated, sharing one type style (set here, inherited
     by the segments, so they can't drift apart the way they did when each
     declared its own). All three are controls with distinct destinations: the
     cwd opens the directory picker, the token count opens the context/cost
     popup, the model name opens the model picker. None of them can look like a
     control in the usual way — they have to read as one line of plain text — so
     each wears the same dotted underline instead (.status-readout-affordance)
     and names its destination in a tooltip.

     Rendered for a conversation that already exists — both while it is idle and
     while the agent works — so the model and cwd here are server state and a
     change has to go through the server (see onSwitchConversationModel and
     onChangeConversationCwd). The composer's boxed ModelPicker and cwd chip
     cover the pre-first-send case instead. -->
<template>
  <div class="status-readout">
    <template v-if="cwd">
      <!-- Changing the directory mid-turn would move the ground under a running
           bash command, so the server refuses it (409) and the button is
           disabled to match, with the tooltip saying why. -->
      <button
        type="button"
        class="status-readout-cwd status-readout-control hide-on-mobile"
        :disabled="agentWorking || !onChangeConversationCwd"
        v-tooltip.top="cwdTooltip"
        :aria-label="cwdAriaLabel"
        @click="onChangeConversationCwd?.()"
      >
        <span class="status-readout-cwd-path status-readout-affordance">{{
          tildifyPath(cwd)
        }}</span>
      </button>
      <span class="status-readout-sep hide-on-mobile" aria-hidden="true">·</span>
    </template>

    <ContextUsageBar
      :context-window-size="contextWindowSize"
      :max-context-tokens="maxContextTokens"
      :conversation-id="conversationId"
      :usage-entries="usageEntries"
      :other-usage-rows="otherUsageRows"
      :on-distill-new-generation="onDistillNewGeneration"
      :on-start-new-generation="onStartNewGeneration"
      :on-usage-needed="onUsageNeeded"
      :agent-working="agentWorking"
    />

    <template v-if="selectedModel">
      <span class="status-readout-sep" aria-hidden="true">·</span>
      <!-- Switching model rebuilds the conversation's loop, which cancels a
           running turn (ApplyModelSettings -> CancelConversation). Disable the
           picker while the agent works rather than silently killing the turn the
           user is watching, and say why two ways: the tooltip has to hang off
           this wrapper (PrimeVue gives a disabled control pointer-events: none,
           so nothing on the picker itself would ever fire), while the ARIA
           description goes on the combobox, which is what a screen reader
           actually lands on. No `title` alongside the tooltip — that renders
           both the PrimeVue bubble and the browser's native one.

           The same wrapper carries the idle hint, for the same reason the token
           count has one: the segment is bare text, so nothing but the dotted
           underline says it can be clicked. -->
      <span
        v-tooltip.top="busyReason || t('modelSwitchHint')"
        class="status-readout-model status-readout-control"
      >
        <ModelPicker
          inline
          :disabled-reason="busyReason"
          :models="models"
          :selected-model="selectedModel"
          :thinking-level="thinkingLevel"
          :disabled="agentWorking"
          :refreshing="refreshingModels"
          @select-model="onSwitchConversationModel"
          @thinking-change="onSwitchConversationThinkingLevel"
          @manage-models="onManageModels"
          @refresh-models="onRefreshModels"
        />
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Model } from "../../types";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import { tildifyPath } from "../../utils/tildify";
import { useI18n } from "../composables/i18n";
import type { ThinkingLevel } from "./thinkingLevel";
import ContextUsageBar from "./ContextUsageBar.vue";
import ModelPicker from "./ModelPicker.vue";

const props = defineProps<{
  cwd?: string;
  conversationId?: string | null;
  contextWindowSize: number;
  maxContextTokens: number;
  usageEntries?: UsageEntry[];
  otherUsageRows?: OtherUsageRow[];
  models: Model[];
  selectedModel: string;
  thinkingLevel: ThinkingLevel;
  refreshingModels?: boolean;
  agentWorking?: boolean;
  onDistillNewGeneration?: () => Promise<void> | void;
  onStartNewGeneration?: () => Promise<void> | void;
  onUsageNeeded?: () => void;
  onChangeConversationCwd?: () => void;
  onSwitchConversationModel: (model: string) => void;
  onSwitchConversationThinkingLevel: (level: ThinkingLevel) => void;
  onManageModels: () => void;
  onRefreshModels: () => void;
}>();

const { t } = useI18n();

// Undefined rather than "" when idle: v-tooltip treats an empty string as a
// tooltip to render, and the ModelPicker prop is optional.
const busyReason = computed(() => (props.agentWorking ? t("modelSwitchBusy") : undefined));

// The cwd segment says the full path (the visible text is tildified, and
// ellipsized when the bar is tight) plus what a click does — or why it can't.
const cwdTooltip = computed(() => {
  if (props.agentWorking) return t("cwdChangeBusy");
  if (!props.onChangeConversationCwd) return props.cwd;
  return `${props.cwd} — ${t("cwdChangeHint")}`;
});

// The accessible name has to carry what the tooltip carries for a pointer: the
// visible text is just a path, which says nothing about the button doing
// anything. A tooltip alone wouldn't do it — PrimeVue's is not wired up as an
// aria-describedby — so the destination goes in the name itself.
const cwdAriaLabel = computed(() => {
  const label = `${t("dirLabel")} ${props.cwd}`;
  if (props.agentWorking) return `${label} — ${t("cwdChangeBusy")}`;
  if (!props.onChangeConversationCwd) return label;
  return `${label} — ${t("cwdChangeHint")}`;
});
</script>
