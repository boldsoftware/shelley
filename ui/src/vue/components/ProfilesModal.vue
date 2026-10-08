<!-- Profiles: named settings a conversation starts from or switches to. The
     list says what each sets and switches to one, makes one the default, or
     deletes one; the editor sets everything a profile holds: model and
     reasoning, tools, and system prompt. Saving the settings in effect into
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
            append-to-body
            aria-label-prefix="Profile model"
            @select-model="setModel"
            @select-combination="setModel"
            @thinking-change="setThinking"
          />
          <span v-if="!draft.settings.model" class="profiles-hint">The server's default</span>
          <Button
            v-else-if="defaultModel"
            size="small"
            text
            label="Use the server's default"
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
        <h3 class="advanced-settings-header profiles-section-title">System prompt</h3>
        <SystemPromptEditor
          v-model="draft.settings.system_prompt"
          @problem="promptProblem = $event"
        />
      </section>
    </div>

    <!-- List -->
    <template v-else>
      <div v-if="error" class="profiles-error" role="alert">{{ error }}</div>
      <ul class="profiles-list">
        <li v-for="p in profiles" :key="p.name" class="profiles-item" data-testid="profile-row">
          <div class="profiles-item-head">
            <span class="profiles-item-name">{{ p.name }}</span>
            <span v-if="p.default" class="profiles-badge">default</span>
            <span v-if="p.name === current" class="profiles-badge profiles-badge-current">
              in use
            </span>
          </div>
          <div class="profiles-item-summary">{{ summarize(p) }}</div>
          <div v-if="pendingDelete === p.name" class="profiles-item-actions profiles-item-confirm">
            <Button
              size="small"
              severity="danger"
              :label="`Delete “${p.name}”`"
              :disabled="busy"
              @click="remove(p.name)"
            />
            <Button
              size="small"
              text
              severity="secondary"
              label="Cancel"
              @click="pendingDelete = null"
            />
          </div>
          <div v-else class="profiles-item-actions">
            <Button
              v-if="p.name !== current || modified"
              size="small"
              text
              label="Use"
              :disabled="locked"
              @click="run(async () => onUse(p.name))"
            />
            <Button size="small" text label="Edit" @click="edit(p)" />
            <Button
              v-if="!p.default"
              size="small"
              text
              label="Make default"
              @click="run(() => profilesApi.makeDefault(p.name))"
            />
            <Button
              v-if="!p.default"
              size="small"
              text
              severity="secondary"
              label="Delete"
              @click="pendingDelete = p.name"
            />
          </div>
        </li>
      </ul>
      <p v-if="locked" class="profiles-hint">Switching waits for the agent to finish.</p>
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
import { settingsOf, useProfiles } from "../composables/profiles";
import { prettyModelLabels } from "../../utils/modelNames";
import { COMPACT_IN_PLACE_TOOL, DEFAULT_COMPACT_NUDGE_TOKENS } from "./autoCompaction";
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

type Draft = { isNew: boolean; name: string; settings: Settings };

const { profiles, reloadProfiles } = useProfiles();
const pendingDelete = ref<string | null>(null);
const error = ref("");
const busy = ref(false);
const draft = ref<Draft | null>(null);
const promptProblem = ref<TemplateProblem | null>(null);

const defaultModel = serverDefaultModel();
const labels = computed(() => prettyModelLabels(props.models));
const title = computed(() =>
  !draft.value ? "Profiles" : draft.value.isNew ? "New profile" : `Profile: ${draft.value.name}`,
);
const canSave = computed(
  () => !!draft.value && !promptProblem.value && (!draft.value.isNew || !!draft.value.name.trim()),
);

watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    error.value = "";
    draft.value = null;
    pendingDelete.value = null;
    reloadProfiles().catch((err) => (error.value = String(err)));
  },
  { immediate: true },
);
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
  if (s.tool_overrides[COMPACT_IN_PLACE_TOOL] === "on") {
    const nudge = s.compact_nudge_tokens || DEFAULT_COMPACT_NUDGE_TOKENS;
    parts.push(`compaction nudge ${nudge / 1000}k`);
  }
  parts.push(s.system_prompt ? "custom system prompt" : "built-in system prompt");
  return parts.join(" · ");
}

// Runs a change to the profiles, then shows the result.
async function remove(name: string) {
  await run(() => profilesApi.remove(name));
  pendingDelete.value = null;
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
  const from = p ?? profiles.value.find((candidate) => candidate.default);
  const settings = from ? settingsOf(from) : settingsOf(emptySettings());
  promptProblem.value = null;
  draft.value = {
    isNew: !p,
    name: p?.name ?? "",
    settings: { ...settings, tool_overrides: { ...settings.tool_overrides } },
  };
}

function emptySettings(): Settings {
  return {
    model: "",
    thinking_level: "",
    tool_overrides: {},
    compact_nudge_tokens: 0,
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

async function save() {
  const d = draft.value!;
  const name = d.name.trim();
  const saved = await run(() =>
    d.isNew ? profilesApi.create(name, d.settings) : profilesApi.update(name, d.settings),
  );
  if (!saved) return;
  // Unless another edit has begun meanwhile.
  if (draft.value === d) draft.value = null;
  props.onSaved(name);
}
</script>
