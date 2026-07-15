<!-- Add / edit dialog for a custom model. Renders as its own Modal (a stacked
     PrimeVue Dialog) layered on top of ModelsModal's list, so it has a proper
     title + close button and the list stays visible/unchanged underneath
     rather than being swapped out. Owns the whole form + test/save lifecycle;
     emits `saved` (parent reloads the list) and `close`. `editModel` is the
     custom model being edited, or null when adding. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="editModel ? t('editModel') : t('addModel')"
    class-name="modal-wide"
    @close="emit('close')"
  >
    <div class="model-form">
      <div v-if="error" class="models-error">
        {{ error }}
        <button class="models-error-dismiss" @click="error = null">×</button>
      </div>

      <!-- Provider Selection -->
      <div class="form-group">
        <label>{{ t("providerApiFormat") }}</label>
        <div class="provider-buttons">
          <button
            v-for="p in providerTypes"
            :key="p"
            type="button"
            :class="`provider-btn ${form.provider_type === p ? 'selected' : ''}`"
            @click="handleProviderChange(p)"
          >
            {{ PROVIDER_LABELS[p] }}
          </button>
        </div>
      </div>

      <!-- Endpoint Selection -->
      <div class="form-group">
        <label>{{ t("endpoint") }}</label>
        <div class="endpoint-toggle">
          <button
            type="button"
            :class="`toggle-btn ${!form.endpoint_custom ? 'selected' : ''}`"
            @click="handleEndpointModeChange(false)"
          >
            {{ t("defaultEndpoint") }}
          </button>
          <button
            type="button"
            :class="`toggle-btn ${form.endpoint_custom ? 'selected' : ''}`"
            @click="handleEndpointModeChange(true)"
          >
            {{ t("customEndpoint") }}
          </button>
        </div>
        <input
          v-if="form.endpoint_custom"
          type="text"
          v-model="form.endpoint"
          placeholder="https://..."
          class="form-input"
        />
        <div v-else class="endpoint-display">{{ form.endpoint }}</div>
      </div>

      <!-- Model Name with autocomplete suggestions -->
      <div class="form-group">
        <label>{{ t("model") }}</label>
        <input
          type="text"
          :value="form.model_name"
          placeholder="Model name (e.g., claude-sonnet-4-6)"
          class="form-input"
          :list="`model-name-suggestions-${form.provider_type}`"
          autocomplete="off"
          @input="onModelNameInput(($event.target as HTMLInputElement).value)"
        />
        <datalist :id="`model-name-suggestions-${form.provider_type}`">
          <option
            v-for="preset in DEFAULT_MODELS[form.provider_type]"
            :key="preset.model_name"
            :value="preset.model_name"
          >
            {{ preset.name }}
          </option>
        </datalist>
      </div>

      <!-- Display Name -->
      <div class="form-group">
        <label>{{ t("displayName") }}</label>
        <input
          type="text"
          v-model="form.display_name"
          :placeholder="t('nameShownInSelector')"
          class="form-input"
        />
      </div>

      <!-- API Key -->
      <div class="form-group">
        <label>{{ t("apiKey") }}</label>
        <input
          type="text"
          v-model="form.api_key"
          :placeholder="t('enterApiKey')"
          class="form-input"
          autocomplete="off"
        />
      </div>

      <!-- User-Agent override -->
      <div class="form-group">
        <label>User-Agent</label>
        <input
          v-model="form.user_agent"
          type="text"
          placeholder="Shelley default (for example: codex_cli_rs/0.144.0)"
          class="form-input"
          autocomplete="off"
        />
        <div class="form-hint">
          Optional. Overrides the User-Agent only for this custom model.
        </div>
      </div>

      <!-- Max Tokens -->
      <div class="form-group">
        <label>{{ t("maxContextTokens") }}</label>
        <input
          type="number"
          :value="form.max_tokens"
          class="form-input"
          @input="form.max_tokens = parseInt(($event.target as HTMLInputElement).value) || 200000"
        />
      </div>

      <!-- Image input support -->
      <div class="form-group">
        <label>{{ t("imageSupport") }}</label>
        <select v-model="form.image_support" class="form-input">
          <option value="auto">{{ t("imageSupportAuto") }}</option>
          <option value="yes">{{ t("imageSupportYes") }}</option>
          <option value="no">{{ t("imageSupportNo") }}</option>
        </select>
        <div class="form-hint">{{ t("imageSupportHelp") }}</div>
        <div v-if="editingResolvedAuto" class="form-hint">
          <code>auto({{ editingResolvedAuto.endpoint }}, {{ editingResolvedAuto.model }})</code>
          {{ t("imageSupportAutoResolved") }}
          {{ editingResolvedAuto.supported ? t("imageSupportYes") : t("imageSupportNo") }}
        </div>
      </div>

      <!-- Legacy provider default for OpenAI Responses models -->
      <div v-if="form.provider_type === 'openai-responses'" class="form-group">
        <label>{{ t("reasoningEffort") }}</label>
        <input
          v-model="form.reasoning_effort"
          type="text"
          :placeholder="t('reasoningEffortPlaceholder')"
          class="form-input"
          list="reasoning-effort-suggestions"
          autocomplete="off"
        />
        <datalist id="reasoning-effort-suggestions">
          <option
            v-for="suggestion in REASONING_EFFORT_SUGGESTIONS"
            :key="suggestion"
            :value="suggestion"
          />
        </datalist>
        <div class="form-hint">{{ t("reasoningEffortHint") }}</div>
      </div>

      <!-- Reasoning capability and generic level mapping -->
      <div class="form-group">
        <label>{{ t("supportsReasoning") }}</label>
        <select v-model="form.reasoning_support" class="form-input">
          <option value="auto">{{ t("reasoningSupportAuto") }}</option>
          <option value="yes">{{ t("reasoningSupportYes") }}</option>
          <option value="no">{{ t("reasoningSupportNo") }}</option>
        </select>
        <div class="form-hint">
          {{ t("reasoningSupportHelp") }}
        </div>
      </div>
      <div v-if="form.reasoning_support !== 'no'" class="form-group">
        <label>{{ t("reasoningLevelMapping") }}</label>
        <div class="reasoning-map-grid">
          <label v-for="level in REASONING_LEVELS" :key="level" class="reasoning-map-row">
            <span>{{ level }}</span>
            <span class="reasoning-map-arrow" aria-hidden="true">→</span>
            <input
              v-model="form.reasoning_map[level]"
              class="form-input reasoning-map-input"
              :list="`reasoning-map-${level}`"
              :placeholder="t('reasoningMappingUnsupported')"
            />
            <datalist :id="`reasoning-map-${level}`">
              <option v-for="target in REASONING_LEVELS" :key="target" :value="target" />
              <option value="none" />
            </datalist>
          </label>
        </div>
        <div class="form-hint">
          {{ t("reasoningMappingHelp") }}
        </div>
      </div>

      <!-- Tags -->
      <div class="form-group">
        <label>
          {{ t("tags") }}
          <span class="info-icon-wrapper" @click.prevent.stop="showTagsTooltip = !showTagsTooltip">
            <span class="info-icon">
              <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="14" height="14">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  :stroke-width="2"
                  d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                />
              </svg>
            </span>
            <span v-if="showTagsTooltip" class="info-tooltip">{{ t("tagsTooltip") }}</span>
          </span>
        </label>
        <input
          type="text"
          v-model="form.tags"
          :placeholder="t('tagsPlaceholder')"
          class="form-input"
        />
      </div>

      <!-- Test Result -->
      <div v-if="testResult" :class="`test-result ${testResult.success ? 'success' : 'error'}`">
        {{ testResult.success ? "✓" : "✗" }} {{ testResult.message }}
      </div>

      <!-- Form Actions -->
      <div class="form-actions">
        <button type="button" class="btn-secondary" @click="emit('close')">
          {{ t("cancel") }}
        </button>
        <button
          type="button"
          class="btn-secondary"
          :disabled="testing || (!form.api_key && !editModel) || !form.model_name"
          :title="
            !form.model_name
              ? 'Enter model name to test'
              : !form.api_key && !editModel
                ? 'Enter API key to test'
                : ''
          "
          @click="handleTest"
        >
          {{ testing ? t("testingButton") : t("testButton") }}
        </button>
        <button
          type="button"
          class="btn-primary"
          :disabled="!form.display_name || !form.api_key || !form.model_name"
          @click="handleSave"
        >
          {{ editModel ? t("save") : t("addModel") }}
        </button>
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import Modal from "./Modal.vue";
import { useI18n } from "../composables/i18n";
import {
  customModelsApi,
  type CustomModel,
  type CreateCustomModelRequest,
  type TestCustomModelRequest,
} from "../../services/api";
import {
  DEFAULT_ENDPOINTS,
  DEFAULT_MODELS,
  DEFAULT_REASONING_MAP,
  PROVIDER_LABELS,
  REASONING_EFFORT_SUGGESTIONS,
  REASONING_LEVELS,
  emptyForm,
  providerTypes,
  type FormData,
  type ProviderType,
} from "./customModelConstants";

const props = defineProps<{ isOpen: boolean; editModel: CustomModel | null }>();
const emit = defineEmits<{ (e: "close"): void; (e: "saved"): void }>();

const { t } = useI18n();

const form = reactive<FormData>({ ...emptyForm });
const error = ref<string | null>(null);
const testing = ref(false);
const testResult = ref<{ success: boolean; message: string } | null>(null);
const showTagsTooltip = ref(false);

function resetForm() {
  Object.assign(form, emptyForm, { reasoning_map: { ...DEFAULT_REASONING_MAP } });
}

function serializeReasoningMap(): string {
  return JSON.stringify(
    Object.fromEntries(
      REASONING_LEVELS.flatMap((level) => {
        const target = form.reasoning_map[level];
        return target ? [[level, target]] : [];
      }),
    ),
  );
}

function parseReasoningMap(raw: string) {
  const result = { ...DEFAULT_REASONING_MAP };
  if (!raw) return result;
  try {
    const parsed = JSON.parse(raw) as Record<string, string>;
    for (const level of REASONING_LEVELS) result[level] = parsed[level] || "";
  } catch {
    // Invalid stored mappings are rejected by the server; keep safe defaults.
  }
  return result;
}

// When editing a model whose image support is Auto, surface what auto() would
// resolve to (from the model's stored endpoint/model + server verdict).
const editingResolvedAuto = computed(() => {
  if (form.image_support !== "auto" || !props.editModel) return null;
  return {
    endpoint: props.editModel.endpoint || "\u2014",
    model: props.editModel.model_name || "\u2014",
    supported: props.editModel.supports_images ?? true,
  };
});

// Populate the form from editModel (or reset to blank for add) each time the
// dialog opens, and clear any stale error/test state.
watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    error.value = null;
    testResult.value = null;
    showTagsTooltip.value = false;
    const m = props.editModel;
    if (m) {
      Object.assign(form, {
        display_name: m.display_name,
        provider_type: m.provider_type,
        endpoint: m.endpoint,
        endpoint_custom: m.endpoint !== DEFAULT_ENDPOINTS[m.provider_type as ProviderType],
        api_key: m.api_key,
        model_name: m.model_name,
        max_tokens: m.max_tokens,
        tags: m.tags,
        reasoning_effort: m.reasoning_effort || "",
        user_agent: m.user_agent || "",
        reasoning_support: m.reasoning_support || "auto",
        reasoning_map: parseReasoningMap(m.reasoning_map),
        image_support: m.image_support ?? "auto",
      });
    } else {
      resetForm();
    }
  },
  { immediate: true },
);

function handleProviderChange(provider: ProviderType) {
  form.provider_type = provider;
  form.endpoint = form.endpoint_custom ? form.endpoint : DEFAULT_ENDPOINTS[provider];
}

function handleEndpointModeChange(custom: boolean) {
  form.endpoint_custom = custom;
  form.endpoint = custom ? form.endpoint : DEFAULT_ENDPOINTS[form.provider_type];
}

function onModelNameInput(v: string) {
  const preset = DEFAULT_MODELS[form.provider_type].find((p) => p.model_name === v);
  form.model_name = v;
  if (preset && !form.display_name) form.display_name = preset.name;
}

async function handleTest() {
  if (!form.model_name) {
    testResult.value = { success: false, message: t("modelNameRequired") };
    return;
  }
  if (!form.api_key && !props.editModel) {
    testResult.value = { success: false, message: t("apiKeyRequired") };
    return;
  }
  testing.value = true;
  testResult.value = null;
  try {
    const request: TestCustomModelRequest = {
      model_id: props.editModel?.model_id || undefined,
      provider_type: form.provider_type,
      endpoint: form.endpoint,
      api_key: form.api_key,
      model_name: form.model_name,
      reasoning_effort: form.reasoning_effort,
      user_agent: form.user_agent,
      reasoning_support: form.reasoning_support,
      reasoning_map: serializeReasoningMap(),
    };
    testResult.value = await customModelsApi.testCustomModel(request);
  } catch (err) {
    testResult.value = {
      success: false,
      message: err instanceof Error ? err.message : "Test failed",
    };
  } finally {
    testing.value = false;
  }
}

async function handleSave() {
  if (!form.display_name || !form.api_key || !form.model_name) {
    error.value = "Display name, API key, and model name are required";
    return;
  }
  try {
    error.value = null;
    const request: CreateCustomModelRequest = {
      display_name: form.display_name,
      provider_type: form.provider_type,
      endpoint: form.endpoint,
      api_key: form.api_key,
      model_name: form.model_name,
      max_tokens: form.max_tokens,
      tags: form.tags,
      reasoning_effort: form.reasoning_effort,
      user_agent: form.user_agent,
      reasoning_support: form.reasoning_support,
      reasoning_map: serializeReasoningMap(),
      image_support: form.image_support,
    };
    if (props.editModel) {
      await customModelsApi.updateCustomModel(props.editModel.model_id, request);
    } else {
      await customModelsApi.createCustomModel(request);
    }
    emit("saved");
    emit("close");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to save model";
  }
}
</script>
