<!-- Vue port of components/ModelsModal.tsx. Built-in + custom model CRUD /
     duplicate / test. Preserves the models-* / model-form / provider-* /
     endpoint-* / form-* class contract, the modal-xwide modal, and all i18n
     labels. Uses Modal.vue (#title-right slot), useI18n, and
     api/customModelsApi. The React ImageSupportIndicator subcomponent is
     inlined via small computed helpers + template branches. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="t('manageModels')"
    class-name="modal-xwide"
    @close="emit('close')"
  >
    <template v-if="!showForm" #title-right>
      <div class="models-header-actions">
        <button
          class="btn-secondary btn-sm"
          :disabled="refreshing || loading"
          @click="handleRefreshModels"
        >
          {{ refreshing ? t("refreshingModels") : t("refreshModels") }}
        </button>
        <button class="btn-primary btn-sm" @click="handleAddNew">+ {{ t("addModel") }}</button>
      </div>
    </template>

    <div class="models-modal">
      <div v-if="error" class="models-error">
        {{ error }}
        <button class="models-error-dismiss" @click="error = null">×</button>
      </div>

      <div v-if="loading" class="models-loading">
        <div class="spinner"></div>
        <span>{{ t("loadingModels") }}</span>
      </div>

      <!-- Add/Edit form -->
      <div v-else-if="showForm" class="model-form">
        <h3>{{ editingModelId ? t("editModel") : t("addModel") }}</h3>

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
        </div>

        <!-- Reasoning Effort (OpenAI Responses API only) -->
        <div v-if="form.provider_type === 'openai-responses'" class="form-group">
          <label>{{ t("reasoningEffort") }}</label>
          <input
            type="text"
            v-model="form.reasoning_effort"
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

        <!-- Tags -->
        <div class="form-group">
          <label>
            {{ t("tags") }}
            <span
              class="info-icon-wrapper"
              @click.prevent.stop="showTagsTooltip = !showTagsTooltip"
            >
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
          <button type="button" class="btn-secondary" @click="handleCancel">
            {{ t("cancel") }}
          </button>
          <button
            type="button"
            class="btn-secondary"
            :disabled="testing || (!form.api_key && !editingModelId) || !form.model_name"
            :title="
              !form.model_name
                ? 'Enter model name to test'
                : !form.api_key && !editingModelId
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
            {{ editingModelId ? t("save") : t("addModel") }}
          </button>
        </div>
      </div>

      <!-- Empty state -->
      <div v-else-if="builtInModels.length === 0 && models.length === 0" class="models-empty">
        <p>{{ t("noModelsConfigured") }}</p>
        <p class="models-empty-hint">{{ t("noModelsHint") }}</p>
      </div>

      <!-- Model List -->
      <div v-else class="models-modal-scroll">
        <table class="models-table">
          <thead>
            <tr>
              <th>{{ t("columnName") }}</th>
              <th>{{ t("columnModelId") }}</th>
              <th>{{ t("columnProvider") }}</th>
              <th>{{ t("columnSource") }}</th>
              <th>{{ t("endpoint") }}</th>
              <th>{{ t("tags") }}</th>
              <th class="models-table-images-col">{{ t("columnImages") }}</th>
              <th class="models-table-actions-col">
                <span class="sr-only">{{ t("columnActions") }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="model in builtInModelsFiltered"
              :key="model.id"
              class="models-table-row models-table-row-builtin"
            >
              <td class="models-table-name">{{ model.display_name || model.id }}</td>
              <td class="models-table-mono">{{ model.id }}</td>
              <td
                :class="
                  model.api_type && API_TYPE_LABELS[model.api_type]
                    ? undefined
                    : 'models-table-muted'
                "
              >
                {{ (model.api_type && API_TYPE_LABELS[model.api_type]) || "—" }}
              </td>
              <td>{{ model.source }}</td>
              <td
                :class="model.base_url ? 'models-table-endpoint' : 'models-table-muted'"
                :title="model.base_url || undefined"
              >
                {{ model.base_url || "—" }}
              </td>
              <td class="models-table-muted">—</td>
              <td class="models-table-images">
                <span
                  v-if="model.supports_images ?? true"
                  class="models-table-image-yes"
                  role="img"
                  :title="t('imageSupportYes')"
                  :aria-label="t('imageSupportYes')"
                  >✓</span
                >
                <span
                  v-else
                  class="models-table-image-no"
                  role="img"
                  :title="t('imageSupportNo')"
                  :aria-label="t('imageSupportNo')"
                  >✕</span
                >
              </td>
              <td class="models-table-actions"></td>
            </tr>
            <tr v-for="model in models" :key="model.model_id" class="models-table-row">
              <td class="models-table-name">{{ model.display_name }}</td>
              <td class="models-table-mono">{{ model.model_name }}</td>
              <td>{{ PROVIDER_LABELS[model.provider_type] }}</td>
              <td class="models-table-muted">custom</td>
              <td class="models-table-endpoint" :title="model.endpoint">{{ model.endpoint }}</td>
              <td class="models-table-tags" :title="model.tags || undefined">
                {{ model.tags || "—" }}
              </td>
              <td class="models-table-images">
                <span
                  v-if="(model.image_support ?? 'auto') === 'yes'"
                  class="models-table-image-yes"
                  role="img"
                  :title="t('imageSupportYes')"
                  :aria-label="t('imageSupportYes')"
                  >✓</span
                >
                <span
                  v-else-if="(model.image_support ?? 'auto') === 'no'"
                  class="models-table-image-no"
                  role="img"
                  :title="t('imageSupportNo')"
                  :aria-label="t('imageSupportNo')"
                  >✕</span
                >
                <span
                  v-else
                  class="models-table-image-auto"
                  role="img"
                  :title="t('imageSupportAuto')"
                  :aria-label="t('imageSupportAuto')"
                  >{{ t("imageSupportAutoShort") }}</span
                >
              </td>
              <td class="models-table-actions">
                <button class="btn-icon" :title="t('duplicate')" @click="handleDuplicate(model)">
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                    />
                  </svg>
                </button>
                <button class="btn-icon" :title="t('editModel')" @click="handleEdit(model)">
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                    />
                  </svg>
                </button>
                <button
                  class="btn-icon btn-danger"
                  :title="t('delete_')"
                  @click="handleDelete(model.model_id)"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                    />
                  </svg>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import Modal from "./Modal.vue";
import { useI18n } from "../composables/i18n";
import {
  api,
  customModelsApi,
  type AvailableModel,
  type CustomModel,
  type CreateCustomModelRequest,
  type TestCustomModelRequest,
} from "../../services/api";

type ProviderType = "anthropic" | "openai" | "openai-responses" | "gemini";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void; (e: "modelsChanged"): void }>();

const { t } = useI18n();

const DEFAULT_ENDPOINTS: Record<ProviderType, string> = {
  anthropic: "https://api.anthropic.com/v1/messages",
  openai: "https://api.openai.com/v1",
  "openai-responses": "https://api.openai.com/v1",
  gemini: "https://generativelanguage.googleapis.com/v1beta",
};

const PROVIDER_LABELS: Record<ProviderType, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI (Chat API)",
  "openai-responses": "OpenAI (Responses API)",
  gemini: "Google Gemini",
};

const DEFAULT_MODELS: Record<ProviderType, { name: string; model_name: string }[]> = {
  anthropic: [
    { name: "Claude Sonnet 4.6", model_name: "claude-sonnet-4-6" },
    { name: "Claude Opus 4.6", model_name: "claude-opus-4-6" },
    { name: "Claude Haiku 4.5", model_name: "claude-haiku-4-5" },
  ],
  openai: [
    { name: "GPT-5.3 Chat", model_name: "gpt-5.3-chat-latest" },
    { name: "GPT-5.5", model_name: "gpt-5.5" },
    { name: "GPT-5.4", model_name: "gpt-5.4" },
  ],
  "openai-responses": [
    { name: "GPT-5.5", model_name: "gpt-5.5" },
    { name: "GPT-5.4", model_name: "gpt-5.4" },
    { name: "GPT-5.4 mini", model_name: "gpt-5.4-mini" },
    { name: "GPT-5.3 Codex", model_name: "gpt-5.3-codex" },
  ],
  gemini: [
    { name: "Gemini 3 Pro", model_name: "gemini-3-pro-preview" },
    { name: "Gemini 3 Flash", model_name: "gemini-3-flash-preview" },
  ],
};

const API_TYPE_LABELS: Record<string, string> = {
  "anthropic-messages": "Anthropic",
  "openai-chat-completions": "OpenAI (Chat API)",
  "openai-responses": "OpenAI (Responses API)",
  gemini: "Google Gemini",
  builtin: "Built-in",
};

const REASONING_EFFORT_SUGGESTIONS = ["none", "minimal", "low", "medium", "high", "xhigh"];

const providerTypes: ProviderType[] = ["anthropic", "openai", "openai-responses", "gemini"];

interface FormData {
  display_name: string;
  provider_type: ProviderType;
  endpoint: string;
  endpoint_custom: boolean;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string;
  reasoning_effort: string;
  image_support: "auto" | "yes" | "no";
}

const emptyForm: FormData = {
  display_name: "",
  provider_type: "anthropic",
  endpoint: DEFAULT_ENDPOINTS.anthropic,
  endpoint_custom: false,
  api_key: "",
  model_name: "",
  max_tokens: 200000,
  tags: "",
  reasoning_effort: "",
  image_support: "auto",
};

const models = ref<CustomModel[]>([]);
const loading = ref(true);
const refreshing = ref(false);
const error = ref<string | null>(null);
const builtInModels = ref<AvailableModel[]>([]);

const showForm = ref(false);
const editingModelId = ref<string | null>(null);
const form = reactive<FormData>({ ...emptyForm });

const testing = ref(false);
const testResult = ref<{ success: boolean; message: string } | null>(null);
const showTagsTooltip = ref(false);

const builtInModelsFiltered = computed(() =>
  builtInModels.value.filter((m) => m.id !== "predictable"),
);

function resetForm() {
  Object.assign(form, emptyForm);
}

async function loadModels() {
  try {
    loading.value = true;
    error.value = null;
    models.value = await customModelsApi.getCustomModels();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to load models";
  } finally {
    loading.value = false;
  }
}

function setBuiltInFromModelList(modelList: AvailableModel[]) {
  builtInModels.value = modelList.filter((m) => m.source && m.source !== "custom");
}

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
  if (!form.api_key && !editingModelId.value) {
    testResult.value = { success: false, message: t("apiKeyRequired") };
    return;
  }
  testing.value = true;
  testResult.value = null;
  try {
    const request: TestCustomModelRequest = {
      model_id: editingModelId.value || undefined,
      provider_type: form.provider_type,
      endpoint: form.endpoint,
      api_key: form.api_key,
      model_name: form.model_name,
      reasoning_effort: form.reasoning_effort,
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
      image_support: form.image_support,
    };
    if (editingModelId.value) {
      await customModelsApi.updateCustomModel(editingModelId.value, request);
    } else {
      await customModelsApi.createCustomModel(request);
    }
    showForm.value = false;
    editingModelId.value = null;
    resetForm();
    testResult.value = null;
    await loadModels();
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to save model";
  }
}

function handleEdit(model: CustomModel) {
  editingModelId.value = model.model_id;
  Object.assign(form, {
    display_name: model.display_name,
    provider_type: model.provider_type,
    endpoint: model.endpoint,
    endpoint_custom: model.endpoint !== DEFAULT_ENDPOINTS[model.provider_type],
    api_key: model.api_key,
    model_name: model.model_name,
    max_tokens: model.max_tokens,
    tags: model.tags,
    reasoning_effort: model.reasoning_effort || "",
    image_support: model.image_support ?? "auto",
  });
  showForm.value = true;
  testResult.value = null;
}

async function handleDuplicate(model: CustomModel) {
  try {
    error.value = null;
    await customModelsApi.duplicateCustomModel(model.model_id);
    await loadModels();
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to duplicate model";
  }
}

async function handleDelete(modelId: string) {
  try {
    error.value = null;
    await customModelsApi.deleteCustomModel(modelId);
    await loadModels();
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to delete model";
  }
}

function handleCancel() {
  showForm.value = false;
  editingModelId.value = null;
  resetForm();
  testResult.value = null;
}

function handleAddNew() {
  editingModelId.value = null;
  resetForm();
  showForm.value = true;
  testResult.value = null;
}

async function handleRefreshModels() {
  try {
    refreshing.value = true;
    error.value = null;
    const refreshedModels = await api.refreshModels();
    if (window.__SHELLEY_INIT__) {
      window.__SHELLEY_INIT__.models = refreshedModels;
    }
    setBuiltInFromModelList(refreshedModels);
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to refresh models";
  } finally {
    refreshing.value = false;
  }
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      loadModels();
      const initData = window.__SHELLEY_INIT__;
      if (initData?.models) {
        setBuiltInFromModelList(initData.models);
      }
    }
  },
  { immediate: true },
);
</script>
