<!-- The tools a conversation offers the agent, and auto compaction (the
     compact_in_place tool plus its context-size nudges), as edited by the
     Tools modal and the profile editor. -->
<template>
  <div class="advanced-settings-header">
    <h3 class="profiles-section-title">Tools</h3>
    <button
      type="button"
      class="advanced-settings-reset"
      :disabled="Object.keys(overrides).length === 0"
      @click="overrides = {}"
    >
      Reset to defaults
    </button>
  </div>
  <div class="auto-compaction-row" data-testid="auto-compaction">
    <span class="auto-compaction-name">{{ t("autoCompaction") }}</span>
    <div class="tool-override-choices" role="radiogroup" :aria-label="t('autoCompaction')">
      <button
        v-for="choice in [
          { on: true, label: 'On' },
          { on: false, label: 'Off' },
        ]"
        :key="choice.label"
        type="button"
        role="radio"
        :aria-checked="autoCompaction === choice.on"
        :class="`tool-override-choice${autoCompaction === choice.on ? ' active' : ''}`"
        @click="
          overrides = withTool(overrides, COMPACT_IN_PLACE_TOOL, choice.on, tools, disableAll)
        "
      >
        {{ choice.label }}
      </button>
    </div>
    <label class="auto-compaction-nudge">
      {{ t("compactNudgeAt") }}
      <select
        :value="nudge || DEFAULT_COMPACT_NUDGE_TOKENS"
        :disabled="!autoCompaction"
        @change="setNudge(Number(($event.target as HTMLSelectElement).value))"
      >
        <option
          v-for="n in compactNudgeChoices(nudge || DEFAULT_COMPACT_NUDGE_TOKENS)"
          :key="n"
          :value="n"
        >
          {{ n / 1000 }}k
        </option>
      </select>
    </label>
  </div>
  <div class="tool-override-list">
    <div v-for="tool in listedTools" :key="tool.name" class="tool-override-row">
      <div class="tool-override-info">
        <span class="tool-override-name">{{ tool.name }}</span>
        <span class="tool-override-summary">{{ tool.summary }}</span>
      </div>
      <div class="tool-override-choices" role="radiogroup" :aria-label="tool.name">
        <button
          v-for="choice in choicesFor(tool)"
          :key="choice.val"
          type="button"
          role="radio"
          :aria-checked="(overrides[tool.name] || 'default') === choice.val"
          :class="[
            'tool-override-choice',
            { active: (overrides[tool.name] || 'default') === choice.val },
            { 'default-off': choice.val === 'default' && !defaultOn(tool) },
          ]"
          @click="setOverride(tool.name, choice.val)"
        >
          {{ choice.label }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "../composables/i18n";
import { toolOn, withTool } from "../composables/profiles";
import {
  COMPACT_IN_PLACE_TOOL,
  compactNudgeChoices,
  DEFAULT_COMPACT_NUDGE_TOKENS,
} from "./autoCompaction";

type ToolInfo = { name: string; summary: string; default_on: boolean };
type Overrides = Record<string, "on" | "off">;

const props = defineProps<{
  tools: ToolInfo[];
  /** Whether the conversation started with -no-tools, which turns off every
   *  tool not turned on. */
  disableAll?: boolean;
}>();
const { t } = useI18n();
const overrides = defineModel<Overrides>("overrides", { required: true });
/** 0 means the default, which is what picking the default sets. */
const nudge = defineModel<number>("nudge", { required: true });

const autoCompaction = computed(() =>
  toolOn(overrides.value, COMPACT_IN_PLACE_TOOL, props.tools, props.disableAll),
);
// Auto compaction has its own row.
const listedTools = computed(() => props.tools.filter((t) => t.name !== COMPACT_IN_PLACE_TOOL));

function setOverride(name: string, value: "default" | "on" | "off") {
  const next = { ...overrides.value };
  if (value === "default") delete next[name];
  else next[name] = value;
  overrides.value = next;
}

function setNudge(n: number) {
  nudge.value = n === DEFAULT_COMPACT_NUDGE_TOKENS ? 0 : n;
}

function defaultOn(tool: ToolInfo): boolean {
  return tool.default_on && !props.disableAll;
}

function choicesFor(tool: ToolInfo): { val: "default" | "on" | "off"; label: string }[] {
  return [
    { val: "default", label: `Default (${defaultOn(tool) ? "on" : "off"})` },
    { val: "on", label: "On" },
    { val: "off", label: "Off" },
  ];
}
</script>
