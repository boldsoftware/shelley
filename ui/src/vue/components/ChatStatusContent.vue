<!-- Status-bar content extracted from renderStatusContent() in
     ChatInterface.tsx. Rendered in the standalone status bar (desktop) and
     inline in the message input controls row (mobile). Preserves the
     status-* / context bar / agent-thinking contract. -->
<template>
  <!-- Archived -->
  <template v-if="currentConversation?.archived">
    <span class="status-message">{{ wording("This conversation is archived.", "This task is archived.") }}</span>
    <button class="status-button status-button-primary" @click="onUnarchive">Unarchive</button>
  </template>

  <!-- Error, with the connection state beside it: the stream may be failing
       for the same reason (e.g. a full disk), so neither hides the other. -->
  <template v-else-if="error">
    <span
      v-if="streamStatus === 'disconnected'"
      class="status-message status-warning status-connection"
      >Disconnected</span
    >
    <span
      v-else-if="streamStatus === 'reconnecting'"
      class="status-message status-reconnecting status-connection"
    >
      Reconnecting<span class="reconnecting-dots">...</span>
    </span>
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

  <!-- Open conversation: working, interrupted, or ready. One container, so
       the running-work counts and readout stay mounted (an open popover
       included) as the turn starts and ends; a job exiting starts one. -->
  <div
    v-else-if="conversationId && (working || interrupted || !currentConversation?.is_draft)"
    class="status-bar-active"
  >
    <template v-if="working">
      <AnimatedWorkingStatus data-testid="agent-thinking" />
      <button
        v-if="agentWorking"
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
      <span v-else class="working-indicator" title="Background work running" aria-hidden="true" />
    </template>
    <div
      v-else-if="interrupted"
      class="status-interrupted-group"
      data-testid="conversation-interrupted"
    >
      <span class="status-message">{{ wording("Conversation Interrupted", "Turn interrupted") }}</span>
      <button
        type="button"
        class="status-button status-interrupted-button"
        :disabled="resumingInterrupted"
        data-testid="resume-interrupted-button"
        v-tooltip.top="'Retries the interrupted turn. An unfinished tool may run again.'"
        @click="onResumeInterrupted"
      >
        {{ resumingInterrupted ? "Continuing…" : "Continue" }}
      </button>
    </div>
    <!-- The Assistant isn't tied to a host or directory: no "Ready on", no cwd. -->
    <span v-else-if="!isAssistant" class="status-message status-ready">
      <span class="hide-on-mobile">Ready on </span>{{ hostname }}
    </span>
    <StatusReadout
      v-bind="readoutProps"
      :cwd="isAssistant ? undefined : cwd"
      :conversation-id="conversationId"
      :agent-working="agentWorking"
    />
  </div>

  <!-- New conversation or draft. Phones stack the fields under labels of
       their own; desktop keeps them in a row. -->
  <div v-else class="status-bar-new-conversation">
    <div class="status-field status-field-model">
      <span class="status-field-label hide-on-desktop">{{ t("modelLabel") }}</span>
      <ModelPicker
        :models="models"
        :selected-model="selectedModel"
        :thinking-level="thinkingLevel"
        :disabled="sending"
        :refreshing="refreshingModels"
        :knob="knob"
        @select-model="onSelectModel"
        @select-combination="onSelectCombination"
        @thinking-change="onThinkingChange"
        @manage-models="onManageModels"
        @refresh-models="onRefreshModels"
      />
    </div>
    <div
      v-if="!isAssistant"
      :class="`status-field status-field-cwd${cwdError ? ' status-field-error' : ''}`"
      v-tooltip.top="cwdError || 'Working directory for file operations'"
    >
      <span class="status-field-label">{{ t("dirLabel") }}</span>
      <button
        :class="`status-chip${cwdError ? ' status-chip-error' : ''}`"
        :disabled="sending"
        @click="onOpenDirectoryPicker"
      >
        <span class="status-chip-text ellipsize-start"
          ><bdi>{{ tildifyPath(selectedCwd) || "(no cwd)" }}</bdi></span
        >
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { Conversation, Message, Model } from "../../types";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import { tildifyPath } from "../../utils/tildify";
import { useI18n } from "../composables/i18n";
import { wording } from "../../utils/workspaceMode";
import type { ThinkingLevel } from "./thinkingLevel";
import type { SettingsKnob } from "../composables/profiles";
import AnimatedWorkingStatus from "./AnimatedWorkingStatus.vue";
import ModelPicker from "./ModelPicker.vue";
import StatusReadout from "./StatusReadout.vue";

const props = defineProps<{
  currentConversation?: Conversation;
  conversationId: string | null;
  streamStatus: "connected" | "reconnecting" | "disconnected";
  error: string | null;
  agentWorking: boolean;
  working: boolean;
  interrupted: boolean;
  resumingInterrupted: boolean;
  cancelling: boolean;
  selectedCwd: string;
  contextWindowSize: number;
  maxContextTokens: number;
  usageEntries: UsageEntry[];
  otherUsageRows: OtherUsageRow[];
  messages: Message[];
  hostname: string;
  models: Model[];
  selectedModel: string;
  sending: boolean;
  refreshingModels: boolean;
  thinkingLevel: ThinkingLevel;
  /** Profiles and tools, for whichever picker shows. */
  knob: SettingsKnob;
  cwdError: string | null;
  /** The workspace Assistant, which hides the host and cwd readouts. */
  isAssistant?: boolean;
  // callbacks
  onUnarchive: () => void;
  onClearError: () => void;
  onCancel: () => void;
  onResumeInterrupted: () => void;
  onCompactInPlace: () => Promise<void> | void;
  compactInPlaceBusy?: boolean;
  contextNudges?: boolean;
  onSelectModel: (model: string) => void;
  onSelectCombination: (model: string, level: Exclude<ThinkingLevel, "default"> | null) => void;
  /** Model / reasoning-level picks from the status readout, which only renders
   *  for an existing conversation — different operations from onSelectModel and
   *  onThinkingChange, which are client-side only (see changeConversationSettings
   *  in ChatInterface). */
  onSwitchConversationModel: (model: string) => void;
  onSwitchConversationCombination: (
    model: string,
    level: Exclude<ThinkingLevel, "default"> | null,
  ) => void;
  onSwitchConversationThinkingLevel: (level: ThinkingLevel) => void;
  onManageModels: () => void;
  onRefreshModels: () => void;
  onThinkingChange: (level: ThinkingLevel) => void;
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
  messages: props.messages,
  models: props.models,
  selectedModel: props.selectedModel,
  thinkingLevel: props.thinkingLevel,
  refreshingModels: props.refreshingModels,
  onCompactInPlace: props.onCompactInPlace,
  compactInPlaceBusy: props.compactInPlaceBusy,
  contextNudges: props.contextNudges,
  onUsageNeeded: props.onUsageNeeded,
  // The readout's cwd segment. Same picker as the composer's cwd chip, but for
  // a conversation that already exists, where the pick has to go through the
  // server (see applyPickedCwd in ChatInterface).
  onChangeConversationCwd: props.onOpenDirectoryPicker,
  onSwitchConversationModel: props.onSwitchConversationModel,
  onSwitchConversationCombination: props.onSwitchConversationCombination,
  onSwitchConversationThinkingLevel: props.onSwitchConversationThinkingLevel,
  onManageModels: props.onManageModels,
  onRefreshModels: props.onRefreshModels,
  knob: props.knob,
}));
</script>
