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
    <span class="status-message status-error">{{ error }}</span>
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
    <span
      v-if="currentConversation?.cwd || selectedCwd"
      class="status-cwd-readonly hide-on-mobile"
      :title="currentConversation?.cwd || selectedCwd"
    >
      {{ tildifyPath(currentConversation?.cwd || selectedCwd) }}
    </span>
    <ContextUsageBar
      :context-window-size="contextWindowSize"
      :max-context-tokens="maxContextTokens"
      :conversation-id="conversationId"
      :model-name="selectedModelDisplayName"
      :usage-entries="usageEntries"
      :on-distill-new-generation="onDistillNewGeneration"
      :on-start-new-generation="onStartNewGeneration"
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
        <div v-if="showAdvancedSettings" class="advanced-settings-popover">
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
    <span
      v-if="currentConversation?.cwd || selectedCwd"
      class="status-cwd-readonly hide-on-mobile"
      :title="currentConversation?.cwd || selectedCwd"
    >
      {{ tildifyPath(currentConversation?.cwd || selectedCwd) }}
    </span>
    <ContextUsageBar
      :context-window-size="contextWindowSize"
      :max-context-tokens="maxContextTokens"
      :conversation-id="conversationId"
      :model-name="selectedModelDisplayName"
      :usage-entries="usageEntries"
      :on-distill-new-generation="onDistillNewGeneration"
      :on-start-new-generation="onStartNewGeneration"
      :agent-working="agentWorking"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from "vue";
import type { Conversation } from "../../types";
import type { UsageEntry } from "../../utils/tokenCostGraph";
import { tildifyPath } from "../../utils/tildify";
import { useI18n } from "../composables/i18n";
import type { ThinkingLevel } from "./thinkingLevel";
import AnimatedWorkingStatus from "./AnimatedWorkingStatus.vue";
import ContextUsageBar from "./ContextUsageBar.vue";
import ModelPicker from "./ModelPicker.vue";

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
  selectedModelDisplayName: string;
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
  onManageModels: () => void;
  onRefreshModels: () => void;
  onThinkingChange: (level: ThinkingLevel) => void;
  onSetToolOverride: (name: string, value: "default" | "on" | "off") => void;
  onResetToolOverrides: () => void;
  onOpenDirectoryPicker: () => void;
}>();

const { t } = useI18n();

// Local advanced-settings popover state + outside-click close.
const showAdvancedSettings = ref(false);
const advancedSettingsRef = ref<HTMLDivElement | null>(null);
function onOutside(e: MouseEvent) {
  if (advancedSettingsRef.value && !advancedSettingsRef.value.contains(e.target as Node)) {
    showAdvancedSettings.value = false;
  }
}
watch(showAdvancedSettings, (open) => {
  document.removeEventListener("mousedown", onOutside);
  if (open) document.addEventListener("mousedown", onOutside);
});
onUnmounted(() => document.removeEventListener("mousedown", onOutside));

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
