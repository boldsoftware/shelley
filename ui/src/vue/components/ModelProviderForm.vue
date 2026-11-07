<template>
  <form class="model-form" data-testid="model-provider-form" @submit.prevent="save">
    <div v-if="error" class="models-error" role="alert">
      {{ error }}
    </div>

    <div class="form-group">
      <label>{{ t("providerApiFormat") }}</label>
      <div class="provider-buttons" role="group" :aria-label="t('providerApiFormat')">
        <button
          v-for="option in providerTypes"
          :key="option.value"
          type="button"
          :class="`provider-btn ${!useChatGPT && providerType === option.value ? 'selected' : ''}`"
          :aria-pressed="!useChatGPT && providerType === option.value"
          :aria-label="PROVIDER_LABELS[option.value]"
          @click="changeProvider(option.value)"
        >
          {{ PROVIDER_TAB_LABELS[option.value] }}
        </button>
        <!-- A ChatGPT plan connects by signing in instead of with an API key. -->
        <button
          v-if="chatgpt && !provider"
          type="button"
          :class="`provider-btn provider-btn-chatgpt ${useChatGPT ? 'selected' : ''}`"
          :aria-pressed="useChatGPT"
          data-testid="provider-chatgpt"
          @click="useChatGPT = true"
        >
          <ChatGPTLogo :size="14" />
          <span>ChatGPT</span>
        </button>
      </div>
    </div>

    <template v-if="useChatGPT">
      <ChatGPTSignInPending
        v-if="chatgptPending"
        :pending="chatgptPending"
        :busy="!!chatgptBusy"
        @complete="emit('chatgptComplete', $event)"
        @cancel="emit('chatgptCancel')"
      />
      <p v-else class="chatgpt-connect-hint">{{ t("chatgptPlanIntro") }}</p>
      <div v-if="chatgptMessage" class="models-error">{{ chatgptMessage }}</div>
      <div v-if="!chatgptPending" class="form-actions">
        <Button type="button" severity="secondary" :label="t('cancel')" @click="emit('close')" />
        <Button
          type="button"
          :disabled="chatgptBusy"
          data-testid="continue-with-chatgpt"
          @click="emit('chatgptSignIn')"
        >
          <ChatGPTLogo :size="14" />
          <span>{{ t("addChatGPTAccount") }}</span>
        </Button>
      </div>
    </template>

    <template v-else>
      <div class="form-group">
        <label for="provider-endpoint">{{ t("endpoint") }}</label>
        <ModelEndpointInput
          v-model="endpoint"
          input-id="provider-endpoint"
          :presets="endpointPresets"
        />
      </div>

      <div class="form-group">
        <label for="model-provider-api-key">{{ t("apiKey") }}</label>
        <InputText
          id="model-provider-api-key"
          v-model="apiKey"
          data-testid="provider-api-key"
          :placeholder="provider?.has_api_key ? t('providerApiKeyKeepExisting') : t('enterApiKey')"
          type="password"
          fluid
          :dt="inputFieldDt"
          autocomplete="new-password"
        />
      </div>

      <div class="form-group">
        <label for="model-provider-display-name">{{ t("providerDisplayName") }}</label>
        <InputText
          id="model-provider-display-name"
          v-model="displayName"
          data-testid="provider-display-name"
          :placeholder="t('accountLabelPlaceholder')"
          fluid
          :dt="inputFieldDt"
          autocomplete="organization"
        />
      </div>

      <div
        v-if="checkResult"
        :class="['test-result', checkResult.success ? 'success' : 'error']"
        role="status"
      >
        {{ checkResult.message }}
      </div>

      <div class="form-actions">
        <Button
          type="button"
          severity="secondary"
          :label="t('cancel')"
          :disabled="saving"
          @click="emit('close')"
        />
        <Button
          type="button"
          severity="secondary"
          data-testid="provider-check-models"
          :label="checking ? t('checkingModels') : t('checkModels')"
          :disabled="saving || checking || !canCheck"
          @click="checkModels"
        />
        <Button
          type="submit"
          :label="saving ? t('providerSaving') : provider ? t('save') : t('connectProvider')"
          :disabled="saving || checking || !canCheck"
        />
      </div>
    </template>
  </form>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import { useI18n } from "../composables/i18n";
import { inputFieldDt } from "./configFieldDt";
import ModelEndpointInput from "./ModelEndpointInput.vue";
import ChatGPTLogo from "./ChatGPTLogo.vue";
import ChatGPTSignInPending from "./ChatGPTSignInPending.vue";
import { endpointPresetsFor, PROVIDER_LABELS, PROVIDER_TAB_LABELS } from "./customModelConstants";
import {
  modelProvidersApi,
  type ChatGPTSignIn,
  type ModelProviderConnection,
  type ModelProviderInput,
  type ModelProviderType,
} from "../../services/api";

const props = defineProps<{
  provider: ModelProviderConnection | null;
  // chatgpt offers ChatGPT, which signs in, beside the API key providers.
  chatgpt?: boolean;
  chatgptBusy?: boolean;
  chatgptPending?: ChatGPTSignIn | null;
  chatgptMessage?: string | null;
}>();
const emit = defineEmits<{
  (e: "saved", provider: ModelProviderConnection): void;
  (e: "close"): void;
  (e: "chatgptSignIn"): void;
  (e: "chatgptComplete", url: string): void;
  (e: "chatgptCancel"): void;
}>();
const useChatGPT = ref(false);
// A sign-in in progress shows under ChatGPT when the dialog reopens.
watch(
  () => props.chatgptPending,
  (pending) => {
    if (pending) useChatGPT.value = true;
  },
);
const { t } = useI18n();

const endpoints: Record<ModelProviderType, string> = {
  anthropic: "https://api.anthropic.com/v1",
  openai: "https://api.openai.com/v1",
  "openai-responses": "https://api.openai.com/v1",
};
const providerTypes: { value: ModelProviderType; label: string }[] = [
  { value: "anthropic", label: "Anthropic" },
  { value: "openai", label: "OpenAI (Chat API)" },
  { value: "openai-responses", label: "OpenAI (Responses API)" },
];

const providerType = ref<ModelProviderType>("anthropic");
const endpoint = ref(endpoints.anthropic);
const endpointPresets = computed(() => endpointPresetsFor(providerType.value, true));
const displayName = ref("");
const apiKey = ref("");
const saving = ref(false);
defineExpose({ saving });
const checking = ref(false);
const checkResult = ref<{ success: boolean; message: string } | null>(null);
const canCheck = computed(() => !!endpoint.value.trim() && (!!props.provider || !!apiKey.value.trim()));
const discoveryInput = computed(() => JSON.stringify([
  props.provider?.provider_id, providerType.value, endpoint.value.trim(), apiKey.value,
]));
watch(discoveryInput, () => { checkResult.value = null; });
const error = ref<string | null>(null);

function reset() {
  providerType.value = props.provider?.provider_type ?? "anthropic";
  endpoint.value = props.provider?.endpoint ?? endpoints[providerType.value];
  displayName.value = props.provider?.display_name ?? "";
  apiKey.value = "";
  error.value = null;
  checkResult.value = null;
  useChatGPT.value = !props.provider && !!props.chatgptPending;
}

watch(() => props.provider, reset, { immediate: true });

function changeProvider(value: ModelProviderType) {
  useChatGPT.value = false;
  const wasPreset = endpointPresets.value.some((preset) => preset.url === endpoint.value);
  providerType.value = value;
  if (wasPreset && !endpointPresets.value.some((preset) => preset.url === endpoint.value)) {
    endpoint.value = endpoints[value];
  }
}

function providerInput(): ModelProviderInput {
  return {
    provider_type: providerType.value,
    endpoint: endpoint.value.trim(),
    api_key: apiKey.value,
    display_name: displayName.value.trim(),
  };
}

async function checkModels() {
  if (saving.value || checking.value || !canCheck.value) return;
  const signature = discoveryInput.value;
  checking.value = true;
  checkResult.value = null;
  error.value = null;
  try {
    const models = await modelProvidersApi.check(providerInput(), props.provider?.provider_id);
    if (signature !== discoveryInput.value) return;
    checkResult.value = {
      success: models.length > 0,
      message: models.length === 0
        ? t("providerNoModels")
        : t("providerChatModelCount").replace("{count}", String(models.length)) +
          `: ${models.slice(0, 5).map((model) => model.id).join(", ")}${models.length > 5 ? ", …" : ""}`,
    };
  } catch (err) {
    if (signature === discoveryInput.value) {
      checkResult.value = { success: false, message: err instanceof Error ? err.message : String(err) };
    }
  } finally {
    checking.value = false;
  }
}

async function save() {
  if (saving.value || checking.value || !canCheck.value) return;
  saving.value = true;
  error.value = null;
  const input = providerInput();
  try {
    const saved = props.provider
      ? await modelProvidersApi.update(props.provider.provider_id, input)
      : await modelProvidersApi.create(input);
    emit("saved", saved);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    saving.value = false;
  }
}
</script>
