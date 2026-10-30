<!-- Profiles: named settings a conversation starts from or switches to. The
     list says what each sets and switches to one, makes one the default, or
     deletes one; the editor sets everything a profile holds: model and
     reasoning, tools, system prompt, and the voice prompt calls use. Saving the settings in effect into
     a profile happens in the model picker, where they're changed. -->
<template>
  <Modal :is-open="isOpen" :title="title" class-name="modal-wide" @close="emit('close')">
    <!-- Editor -->
    <div v-if="draft" class="profiles-editor">
      <section v-if="draft.isNew" class="profiles-section">
        <label class="advanced-settings-header" for="profiles-name">Name</label>
        <input id="profiles-name" v-model="draft.name" class="profiles-name" autocomplete="off" />
      </section>
      <section class="profiles-section">
        <h3 class="advanced-settings-header profiles-section-title">Model</h3>
        <div class="profiles-model">
          <ModelPicker
            :models="models"
            :selected-model="draft.settings.model || defaultModel"
            :thinking-level="(draft.settings.thinking_level || 'default') as ThinkingLevel"
            :show-recent="false"
            :catalog-actions="false"
            aria-label-prefix="Profile model"
            @select-model="setModel"
            @select-combination="setModel"
            @thinking-change="setThinking"
          />
          <span v-if="!draft.settings.model" class="profiles-hint">Server default</span>
          <Button
            v-else-if="defaultModel"
            size="small"
            text
            label="Use server default"
            @click="setModel('')"
          />
        </div>
      </section>
      <section class="profiles-section">
        <ToolSettings
          v-model:overrides="draft.settings.tool_overrides"
          v-model:nudge="draft.settings.compact_nudge_tokens"
          :tools="tools"
        />
      </section>
      <section class="profiles-section">
        <h3 class="advanced-settings-header profiles-section-title">Idle behavior</h3>
        <div class="profiles-row">
          <div class="profiles-row-text">
            <label for="profiles-idle">Compact when idle</label>
            <span class="profiles-hint">Once, after this long idle. Needs compact_in_place.</span>
          </div>
          <span class="profiles-num">
            <input
              id="profiles-idle"
              v-model.number="draft.settings.idle_compaction_minutes"
              type="number"
              min="0"
              max="153722867"
              step="1"
              placeholder="0"
            />
            <span>min</span>
          </span>
        </div>
        <textarea
          v-if="
            (draft.settings.idle_compaction_minutes ?? 0) > 0 ||
            draft.settings.idle_compaction_prompt
          "
          id="profiles-idle-prompt"
          v-model="draft.settings.idle_compaction_prompt"
          class="profiles-voice"
          rows="5"
          aria-label="Idle compaction prompt"
          placeholder="What to keep when compacting (optional; defaults to compact_in_place's guidance)"
        />
        <div class="profiles-row">
          <div class="profiles-row-text">
            <label for="profiles-keep-warm">Keep prompt cache warm</label>
            <span class="profiles-hint"
              >After each turn; refreshes billed as cached input. Claude and GPT-5.6+.</span
            >
          </div>
          <span class="profiles-num">
            <input
              id="profiles-keep-warm"
              v-model.number="draft.settings.cache_keep_warm_minutes"
              type="number"
              min="0"
              max="153722867"
              step="1"
              placeholder="0"
            />
            <span>min</span>
          </span>
        </div>
        <p class="profiles-hint">
          0 disables. Applies to new conversations and ones that switch here.
        </p>
      </section>
      <section class="profiles-section">
        <h3 class="advanced-settings-header profiles-section-title">System prompt</h3>
        <SystemPromptEditor
          v-model="draft.settings.system_prompt"
          @problem="promptProblem = $event"
        />
      </section>
      <section class="profiles-section">
        <label class="advanced-settings-header" for="profiles-voice">Voice prompt</label>
        <p class="profiles-hint">Persona for voice calls. Leave empty for none.</p>
        <textarea
          id="profiles-voice"
          v-model="draft.voicePrompt"
          class="profiles-voice"
          rows="5"
          :maxlength="VOICE_PROMPT_MAX"
          :disabled="generating"
        />
        <div class="profiles-voice-actions">
          <Button
            size="small"
            severity="secondary"
            label="Generate from system prompt"
            :loading="generating"
            :disabled="!!promptProblem"
            @click="generateVoice"
          />
        </div>
      </section>
    </div>

    <!-- List -->
    <template v-else>
      <div v-if="error" class="profiles-error" role="alert">{{ error }}</div>
      <ul class="profiles-list">
        <li v-for="p in profiles" :key="p.name" class="profiles-item" data-testid="profile-row">
          <div class="profiles-item-head">
            <span class="profiles-item-name">{{ p.name }}</span>
            <span v-if="p.builtin" class="profiles-badge">
              {{ p.user_edited ? "customized built-in" : "built-in" }}
            </span>
            <span v-if="p.default" class="profiles-badge">default</span>
            <span v-if="p.project" class="profiles-badge">project</span>
            <span v-if="p.name === current" class="profiles-badge profiles-badge-current">
              in use
            </span>
          </div>
          <div class="profiles-item-summary">{{ summarize(p) }}</div>
          <template v-if="pending?.name === p.name">
            <p v-if="pending.action === 'restore'" class="profiles-hint">
              Reset settings and voice prompt to built-in defaults? Conversations that haven't
              changed their own settings take them at their next turn.
            </p>
            <div class="profiles-item-actions profiles-item-confirm">
              <Button
                size="small"
                severity="danger"
                :label="`${pending.action === 'restore' ? 'Restore' : 'Delete'} “${p.name}”`"
                :disabled="busy"
                @click="pending.action === 'restore' ? restore(p.name) : remove(p.name)"
              />
              <Button
                size="small"
                text
                severity="secondary"
                label="Cancel"
                :disabled="busy"
                @click="pending = null"
              />
            </div>
          </template>
          <div v-else class="profiles-item-actions">
            <Button
              v-if="p.name === current ? modified : !p.project && !currentIsProject"
              size="small"
              text
              label="Use"
              :disabled="locked"
              @click="run(async () => onUse(p.name))"
            />
            <Button size="small" text label="Edit" @click="edit(p)" />
            <Button
              v-if="p.builtin"
              size="small"
              text
              label="Restore defaults"
              :disabled="busy || !p.user_edited"
              @click="pending = { name: p.name, action: 'restore' }"
            />
            <Button
              v-if="!p.default && !p.project"
              size="small"
              text
              label="Make default"
              @click="run(() => profilesApi.makeDefault(p.name))"
            />
            <Button
              v-if="!p.default && !p.project"
              size="small"
              text
              severity="secondary"
              label="Delete"
              @click="pending = { name: p.name, action: 'delete' }"
            />
          </div>
        </li>
      </ul>
      <p v-if="locked" class="profiles-hint">Switch once the agent finishes.</p>
      <Button size="small" severity="secondary" label="New profile" @click="edit(null)" />
    </template>

    <template v-if="draft" #footer>
      <span class="settings-modal-error" role="alert">{{ error }}</span>
      <Button severity="secondary" label="Cancel" @click="draft = null" />
      <Button label="Save" :loading="busy" :disabled="!canSave" @click="save" />
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ModelPicker from "./ModelPicker.vue";
import SystemPromptEditor from "./SystemPromptEditor.vue";
import ToolSettings from "./ToolSettings.vue";
import { profilesApi } from "../../services/api";
import { settingsOf, toolOn, useProfiles } from "../composables/profiles";
import { prettyModelLabels } from "../../utils/modelNames";
import { COMPACT_IN_PLACE_TOOL } from "./autoCompaction";
import { serverDefaultModel } from "./selectedModel";
import { normalizeThinkingLevelForModel, type ThinkingLevel } from "./thinkingLevel";
import type { Model, Profile, Settings, TemplateProblem } from "../../types";

const props = defineProps<{
  isOpen: boolean;
  models: Model[];
  tools: { name: string; summary: string; default_on: boolean }[];
  /** The profile the settings in effect came from. */
  current: string;
  /** Whether those settings have since moved away from it. */
  modified: boolean;
  /** Switches the settings in effect to the named profile. */
  onUse: (name: string) => Promise<void> | void;
  /** Called after a profile's settings are saved. */
  onSaved: (name: string) => void;
  /** Whether switching profiles has to wait, for the agent to finish. */
  locked: boolean;
}>();
const emit = defineEmits<{ (e: "close"): void }>();

type Draft = { isNew: boolean; name: string; settings: Settings; voicePrompt: string };

// The server's limit, in characters.
const VOICE_PROMPT_MAX = 4000;

const { profiles, reloadProfiles } = useProfiles();
const pending = ref<{ name: string; action: "delete" | "restore" } | null>(null);
const error = ref("");
const busy = ref(false);
const draft = ref<Draft | null>(null);
// A project's conversation neither leaves its project's profile nor joins another.
const currentIsProject = computed(
  () => !!profiles.value.find((p) => p.name === props.current)?.project,
);
const promptProblem = ref<TemplateProblem | null>(null);
const generating = ref(false);

const defaultModel = serverDefaultModel();
const labels = computed(() => prettyModelLabels(props.models));
const title = computed(() =>
  !draft.value ? "Profiles" : draft.value.isNew ? "New profile" : `Profile: ${draft.value.name}`,
);
const canSave = computed(
  () =>
    !!draft.value &&
    !promptProblem.value &&
    !generating.value &&
    Number.isInteger(draft.value.settings.idle_compaction_minutes) &&
    draft.value.settings.idle_compaction_minutes >= 0 &&
    draft.value.settings.idle_compaction_minutes <= 153722867 &&
    Number.isInteger(draft.value.settings.cache_keep_warm_minutes) &&
    draft.value.settings.cache_keep_warm_minutes >= 0 &&
    draft.value.settings.cache_keep_warm_minutes <= 153722867 &&
    (!draft.value.isNew || !!draft.value.name.trim()),
);

watch(
  () => props.isOpen,
  (open) => {
    pending.value = null;
    if (!open) return;
    error.value = "";
    draft.value = null;

    reloadProfiles().catch((err) => (error.value = String(err)));
  },
  { immediate: true },
);
watch(profiles, () => (pending.value = null));
watch(draft, () => (error.value = ""), { deep: true });

function summarize(s: Settings): string {
  // Unset model and reasoning resolve to the server's and the model's
  // defaults.
  const model = s.model || defaultModel;
  const parts = [labels.value.get(model) || model];
  const m = props.models.find((candidate) => candidate.id === model);
  const level = m?.supports_reasoning ? s.thinking_level || m.default_reasoning_level : "";
  if (level) parts.push(`${level} reasoning`);
  const tools = Object.entries(s.tool_overrides);
  if (tools.length) parts.push(tools.map(([name, v]) => `${name} ${v}`).join(", "));
  if (s.compact_nudge_tokens && toolOn(s.tool_overrides, COMPACT_IN_PLACE_TOOL, props.tools)) {
    parts.push(`compaction nudge ${s.compact_nudge_tokens / 1000}k`);
  }
  if (s.idle_compaction_minutes) parts.push(`idle compaction ${s.idle_compaction_minutes} min`);
  if (s.cache_keep_warm_minutes) parts.push(`keep cache warm ${s.cache_keep_warm_minutes} min`);
  parts.push(s.system_prompt ? "custom system prompt" : "built-in system prompt");
  return parts.join(" · ");
}

// Runs a change to the profiles, then shows the result.
async function remove(name: string) {
  await run(() => profilesApi.remove(name));
}

async function restore(name: string) {
  if (await run(() => profilesApi.restore(name))) props.onSaved(name);
}

async function run(action: () => Promise<unknown>): Promise<boolean> {
  error.value = "";
  busy.value = true;
  try {
    await action();
    await reloadProfiles();
    return true;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
    return false;
  } finally {
    busy.value = false;
  }
}

// Edits p, or a new profile starting from the default one's settings.
function edit(p: Profile | null) {
  pending.value = null;
  const from = p ?? profiles.value.find((candidate) => candidate.default);
  const settings = from ? settingsOf(from) : settingsOf(emptySettings());
  promptProblem.value = null;
  draft.value = {
    isNew: !p,
    name: p?.name ?? "",
    settings: { ...settings, tool_overrides: { ...settings.tool_overrides } },
    voicePrompt: p?.voice_prompt ?? "",
  };
}

function emptySettings(): Settings {
  return {
    model: "",
    thinking_level: "",
    tool_overrides: {},
    compact_nudge_tokens: 0,
    idle_compaction_minutes: 0,
    idle_compaction_prompt: "",
    cache_keep_warm_minutes: 0,
    system_prompt: "",
  };
}

function setModel(id: string) {
  const s = draft.value!.settings;
  s.model = id;
  // Keep the reasoning level if the new model has it.
  const level = normalizeThinkingLevelForModel(
    (s.thinking_level || "default") as ThinkingLevel,
    props.models.find((m) => m.id === (id || defaultModel)),
  );
  s.thinking_level = level === "default" ? "" : level;
}

function setThinking(level: ThinkingLevel) {
  draft.value!.settings.thinking_level = level === "default" ? "" : level;
}

// Fills the editor with a voice prompt written from the draft's model and
// system prompt; it's saved, or not, with the rest.
async function generateVoice() {
  const d = draft.value!;
  error.value = "";
  generating.value = true;
  try {
    const voicePrompt = await profilesApi.generateVoicePrompt(
      d.settings.model,
      d.settings.system_prompt,
    );
    if (draft.value === d) d.voicePrompt = voicePrompt;
  } catch (err) {
    if (draft.value === d) error.value = err instanceof Error ? err.message : String(err);
  } finally {
    generating.value = false;
  }
}

async function save() {
  const d = draft.value!;
  const name = d.name.trim();
  const voicePrompt = d.voicePrompt.trim();
  const saved = await run(() =>
    d.isNew
      ? profilesApi.create(name, d.settings, voicePrompt)
      : profilesApi.update(name, d.settings, voicePrompt),
  );
  if (!saved) return;
  // Unless another edit has begun meanwhile.
  if (draft.value === d) draft.value = null;
  props.onSaved(name);
}
</script>
