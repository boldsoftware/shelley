<!-- Add/edit a single model or a saved provider account. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="editProvider ? t('editProvider') : editModel ? t('editModel') : t('addModels')"
    class-name="modal-wide"
    @close="requestClose"
  >
    <div v-if="!editModel && !editProvider" class="form-group">
      <div class="endpoint-toggle" role="group" :aria-label="t('addModels')">
        <button
          type="button"
          data-testid="model-mode-provider"
          :class="['toggle-btn', { selected: providerMode }]"
          :aria-pressed="providerMode"
          :disabled="providerSaving"
          @click="addMode = 'provider'"
        >{{ t("addProvider") }}</button>
        <button
          type="button"
          data-testid="model-mode-single"
          :class="['toggle-btn', { selected: !providerMode }]"
          :aria-pressed="!providerMode"
          :disabled="providerSaving"
          @click="addMode = 'single'"
        >{{ t("addModelSingle") }}</button>
      </div>
    </div>
    <ModelProviderForm
      ref="providerForm"
      v-if="isOpen && !editModel"
      v-show="providerMode"
      :provider="editProvider"
      @saved="emit('providerSaved', $event)"
      @close="requestClose"
    />
    <div v-show="!providerMode" class="model-form">
      <div v-if="error" class="models-error">
        {{ error }}
        <button class="models-error-dismiss" @click="error = null">×</button>
      </div>

      <!-- Provider Selection -->
      <div class="form-group">
        <label>{{ t("providerApiFormat") }}</label>
        <div class="provider-buttons" role="group" :aria-label="t('providerApiFormat')">
          <button
            v-for="p in providerTypes"
            :key="p"
            type="button"
            :class="`provider-btn ${form.provider_type === p ? 'selected' : ''}`"
            :aria-label="PROVIDER_LABELS[p]"
            :aria-pressed="form.provider_type === p"
            @click="handleProviderChange(p)"
          >
            {{ PROVIDER_TAB_LABELS[p] }}
          </button>
        </div>
      </div>

      <div class="form-group">
        <label for="model-endpoint">{{ t("endpoint") }}</label>
        <ModelEndpointInput
          v-model="form.endpoint"
          input-id="model-endpoint"
          :presets="endpointPresets"
        />
      </div>

      <!-- Model Name with autocomplete suggestions -->
      <div class="form-group">
        <label>{{ t("model") }}</label>
        <InputText
          :model-value="form.model_name"
          placeholder="Model name (e.g., claude-sonnet-4-6)"
          fluid
          :dt="inputFieldDt"
          :list="`model-name-suggestions-${form.provider_type}`"
          autocomplete="off"
          @update:model-value="onModelNameInput($event ?? '')"
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
        <InputText
          v-model="form.display_name"
          :placeholder="t('nameShownInSelector')"
          fluid
          :dt="inputFieldDt"
        />
      </div>

      <!-- API Key -->
      <div class="form-group">
        <label>{{ t("apiKey") }}</label>
        <InputText
          v-model="form.api_key"
          type="password"
          :placeholder="editModel?.has_api_key ? t('providerApiKeyKeepExisting') : t('enterApiKey')"
          fluid
          :dt="inputFieldDt"
          autocomplete="off"
        />
      </div>

      <!-- Maximum generated response tokens for custom models. -->
      <div class="form-group">
        <label for="custom-model-max-output-tokens">{{ t("maxOutputTokens") }}</label>
        <InputText
          id="custom-model-max-output-tokens"
          type="number"
          min="1"
          step="1"
          :model-value="form.max_tokens ? String(form.max_tokens) : ''"
          :placeholder="t('maxOutputTokensPlaceholder')"
          fluid
          :dt="inputFieldDt"
          @update:model-value="form.max_tokens = parseInt($event ?? '', 10) || 0"
        />
        <div class="form-hint">{{ t("maxOutputTokensHelp") }}</div>
      </div>

      <!-- Image input support -->
      <div class="form-group">
        <label>{{ t("imageSupport") }}</label>
        <Select
          v-model="form.image_support"
          :options="imageSupportOptions"
          option-label="label"
          option-value="value"
          fluid
          :dt="selectFieldDt"
        />
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
        <InputText
          v-model="form.reasoning_effort"
          :placeholder="t('reasoningEffortPlaceholder')"
          fluid
          :dt="inputFieldDt"
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
        <Select
          v-model="form.reasoning_support"
          :options="reasoningSupportOptions"
          option-label="label"
          option-value="value"
          fluid
          :dt="selectFieldDt"
        />
        <div class="form-hint">
          {{ t("reasoningSupportHelp") }}
        </div>
      </div>
      <div
        v-if="form.provider_type === 'openai' || form.provider_type === 'openai-responses'"
        class="form-group"
      >
        <label>{{ t("reasoningReplay") }}</label>
        <Select
          v-model="form.reasoning_replay"
          :options="reasoningReplayOptions"
          option-label="label"
          option-value="value"
          fluid
          :dt="selectFieldDt"
        />
        <div class="form-hint">{{ t("reasoningReplayHelp") }}</div>
        <div v-if="editingReasoningReplayAuto" class="form-hint">
          {{ t("reasoningReplayAutoResolved") }}
          <code>{{ editingReasoningReplayAuto }}</code>
        </div>
      </div>
      <div v-if="form.reasoning_support !== 'no'" class="form-group">
        <label>{{ t("reasoningLevelMapping") }}</label>
        <div class="reasoning-map-grid">
          <label v-for="level in REASONING_LEVELS" :key="level" class="reasoning-map-row">
            <span>{{ level }}</span>
            <span class="reasoning-map-arrow" aria-hidden="true">→</span>
            <InputText
              v-model="form.reasoning_map[level]"
              class="reasoning-map-input"
              fluid
              :dt="inputFieldDt"
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

      <!-- Test Result -->
      <div v-if="testResult" :class="`test-result ${testResult.success ? 'success' : 'error'}`">
        {{ testResult.success ? "✓" : "✗" }} {{ testResult.message }}
      </div>

      <!-- Form Actions -->
      <div class="form-actions">
        <Button type="button" severity="secondary" :label="t('cancel')" @click="requestClose" />
        <Button
          type="button"
          severity="secondary"
          :label="testing ? t('testingButton') : t('testButton')"
          :disabled="testing || !hasApiKey || !form.model_name"
          :title="
            !form.model_name
              ? 'Enter model name to test'
              : !hasApiKey
                ? 'Enter API key to test'
                : ''
          "
          @click="handleTest"
        />
        <Button
          type="button"
          :label="editModel ? t('save') : t('addModel')"
          :disabled="!form.display_name || !hasApiKey || !form.model_name"
          @click="handleSave"
        />
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Select from "primevue/select";
import { inputFieldDt, selectFieldDt } from "./configFieldDt";
import Modal from "./Modal.vue";
import ModelProviderForm from "./ModelProviderForm.vue";
import ModelEndpointInput from "./ModelEndpointInput.vue";
import { useI18n } from "../composables/i18n";
import {
  customModelsApi,
  type CustomModel,
  type ModelProviderConnection,
  type CreateCustomModelRequest,
  type TestCustomModelRequest,
} from "../../services/api";
import {
  DEFAULT_ENDPOINTS,
  DEFAULT_MODELS,
  DEFAULT_REASONING_MAP,
  PROVIDER_LABELS,
  PROVIDER_TAB_LABELS,
  REASONING_EFFORT_SUGGESTIONS,
  REASONING_LEVELS,
  emptyForm,
  endpointPresetsFor,
  providerTypes,
  type FormData,
  type ProviderType,
} from "./customModelConstants";

const props = defineProps<{
  isOpen: boolean;
  editModel: CustomModel | null;
  editProvider: ModelProviderConnection | null;
}>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "saved"): void;
  (e: "providerSaved", provider: ModelProviderConnection): void;
}>();
const providerForm = ref<InstanceType<typeof ModelProviderForm> | null>(null);
const providerSaving = computed(() => providerForm.value?.saving ?? false);
function requestClose() {
  if (!providerSaving.value) emit("close");
}
const addMode = ref<"provider" | "single">("provider");
const providerMode = computed(() => !!props.editProvider || (!props.editModel && addMode.value === "provider"));

const { t } = useI18n();

const imageSupportOptions = computed(() => [
  { label: t("imageSupportAuto"), value: "auto" },
  { label: t("imageSupportYes"), value: "yes" },
  { label: t("imageSupportNo"), value: "no" },
]);
const reasoningSupportOptions = computed(() => [
  { label: t("reasoningSupportAuto"), value: "auto" },
  { label: t("reasoningSupportYes"), value: "yes" },
  { label: t("reasoningSupportNo"), value: "no" },
]);
const reasoningReplayOptions = computed(() => [
  { label: t("reasoningReplayAuto"), value: "auto" },
  { label: t("reasoningReplayNone"), value: "none" },
  { label: "reasoning_content", value: "reasoning_content" },
]);

const form = reactive<FormData>({ ...emptyForm });
const hasApiKey = computed(() => !!form.api_key || !!props.editModel?.has_api_key);
const endpointPresets = computed(() => endpointPresetsFor(form.provider_type));

const error = ref<string | null>(null);
const testing = ref(false);
const testResult = ref<{ success: boolean; message: string } | null>(null);

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

const editingReasoningReplayAuto = computed(() => {
  const saved = props.editModel;
  if (
    form.reasoning_replay !== "auto" ||
    !saved ||
    saved.reasoning_replay !== "auto" ||
    form.endpoint !== saved.endpoint ||
    form.model_name !== saved.model_name
  )
    return null;
  return saved.resolved_reasoning_replay || t("reasoningReplayNone");
});

// Populate the form from editModel (or reset to blank for add) each time the
// dialog opens, and clear any stale error/test state.
watch(
  () => props.isOpen,
  (open) => {
    if (!open) return;
    addMode.value = "provider";
    error.value = null;
    testResult.value = null;
    const m = props.editModel;
    if (m) {
      Object.assign(form, {
        display_name: m.display_name,
        provider_type: m.provider_type,
        endpoint: m.endpoint,
        api_key: "",
        model_name: m.model_name,
        max_tokens: m.max_tokens,
        reasoning_effort: m.reasoning_effort || "",
        reasoning_replay: m.reasoning_replay || "auto",
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
  const wasPreset = endpointPresets.value.some((preset) => preset.url === form.endpoint);
  form.provider_type = provider;
  if (wasPreset && !endpointPresets.value.some((preset) => preset.url === form.endpoint)) {
    form.endpoint = DEFAULT_ENDPOINTS[provider];
  }
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
  if (!hasApiKey.value) {
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
      max_tokens: form.max_tokens,
      reasoning_effort: form.reasoning_effort,
      reasoning_replay: form.reasoning_replay,
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
  if (!form.display_name || !hasApiKey.value || !form.model_name) {
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
      reasoning_effort: form.reasoning_effort,
      reasoning_replay: form.reasoning_replay,
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
