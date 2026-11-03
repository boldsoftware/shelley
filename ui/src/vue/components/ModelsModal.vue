<!-- Manage Models: lists built-in + custom models and owns duplicate/delete +
     refresh. The list is a compact PrimeVue DataTable (size="small" +
     modelsTableDt tokens) grouped by source via rowGroupMode="subheader": in
     the default single-gateway install this collapses what used to be two
     full columns of the same repeated hostname/URL into one quiet group
     header line. Endpoints render per-row only when they differ from the
     group's common endpoint. Only custom rows carry a `model` and thus the
     edit/duplicate/delete actions. Adding/editing opens ModelFormModal — a
     separate stacked dialog layered on top of this one. Uses Modal.vue
     (#title-right slot), useI18n, customModelsApi; shared form constants live
     in customModelConstants.ts. -->
<template>
  <Modal
    :is-open="mode !== null"
    :title="t('manageModels')"
    class-name="modal-xwide"
    @close="emit('close')"
  >
    <template #title-right>
      <div class="models-header-actions">
        <Button
          :label="refreshing ? t('refreshingModels') : t('refreshModels')"
          severity="secondary"
          size="small"
          :disabled="refreshing || loading"
          @click="handleRefreshModels"
        />
        <Button size="small" @click="handleAddNew">+ {{ t("addModels") }}</Button>
      </div>
    </template>

    <div class="models-modal" :class="{ 'models-modal-list': showList }">
      <div v-if="error" class="models-error">
        {{ error }}
        <button class="models-error-dismiss" @click="error = null">×</button>
      </div>

      <div v-if="loading" class="models-loading">
        <div class="spinner"></div>
        <span>{{ t("loadingModels") }}</span>
      </div>

      <p v-if="!loading && providerLoadFailed" class="model-provider-load-error" role="status">
        {{ t("providerListUnavailable") }}
      </p>

      <!-- Empty state -->
      <div v-if="!loading && !hasAnyEntries && !providerLoadFailed" class="models-empty">
        <p>{{ t("noModelsConfigured") }}</p>
        <p class="models-empty-hint">{{ t("noModelsHint") }}</p>
      </div>

      <!-- Model List -->
      <DataTable
        v-if="showList"
        data-testid="model-provider-list"
        :value="tableRows"
        data-key="key"
        size="small"
        scrollable
        scroll-height="flex"
        :dt="modelsTableDt"
        class="models-datatable"
        row-group-mode="subheader"
        group-rows-by="groupKey"
        :pt="{ rowGroupHeaderCell: { colspan: 5 } }"
      >
        <template #groupheader="{ data }">
          <div v-if="data.provider" class="models-provider-group" data-testid="provider-connection">
            <div class="models-provider-line">
              <span class="models-group-name">{{ data.provider ? providerName(data.provider) : data.groupLabel }}</span>
              <span
                v-if="providerSuffix(data.provider)"
                class="models-group-endpoint"
                :title="t('providerDisplayName')"
                >{{ providerSuffix(data.provider) }}</span
              >
              <span class="models-group-count">({{ data.provider.model_count }})</span>
              <span class="models-group-endpoint">{{ data.provider.endpoint }}</span>
              <span class="models-provider-actions">
                <Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('refreshModels')"
                  :aria-label="t('refreshModels')"
                  :disabled="refreshingProviderIds.has(data.provider.provider_id)"
                  :data-testid="`refresh-provider-${data.provider.provider_id}`"
                  @click="refreshProvider(data.provider)"
                >
                  <svg
                    :class="{ spinning: refreshingProviderIds.has(data.provider.provider_id) }"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    width="16"
                    height="16"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                    />
                  </svg>
                </Button>
                <Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('editProvider')"
                  :aria-label="t('editProvider')"
                  :data-testid="`edit-provider-${data.provider.provider_id}`"
                  @click="openProviderForm(data.provider)"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                    />
                  </svg>
                </Button>
                <Button
                  class="btn-icon btn-danger"
                  text
                  severity="danger"
                  v-tooltip.top="t('removeProvider')"
                  :aria-label="t('removeProvider')"
                  :data-testid="`remove-provider-${data.provider.provider_id}`"
                  @click="openRemoveProvider(data.provider)"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                    />
                  </svg>
                </Button>
              </span>
            </div>
          </div>
          <template v-else>
            <span class="models-group-name">{{ data.groupLabel }}</span>
            <span class="models-group-count">({{ groupCounts[data.groupKey] }})</span>
            <span v-if="data.groupEndpoint" class="models-group-endpoint">{{
              data.groupEndpoint
            }}</span>
          </template>
        </template>
        <Column :header="t('columnName')" field="name">
          <template #body="{ data }">
            <span v-if="data.placeholder" class="models-cell-muted">{{ t("noModelsFound") }}</span>
            <span v-else class="models-cell-name">{{ data.name }}</span>
          </template>
        </Column>
        <Column :header="t('columnModelId')" field="modelId">
          <template #body="{ data }">
            <span class="models-cell-mono" :title="data.modelId">{{ data.wireId }}</span>
            <div v-if="data.endpoint" class="models-cell-endpoint" :title="data.endpoint">
              {{ data.endpoint }}
            </div>
          </template>
        </Column>
        <Column :header="t('columnProvider')" field="apiShape">
          <template #body="{ data }">
            <span :class="{ 'models-cell-muted': !data.apiShape }">{{ data.placeholder ? "" : data.apiShape || "—" }}</span>
          </template>
        </Column>
        <Column :header="t('columnImages')" field="supportsImages" class="models-col-images">
          <template #body="{ data }">
            <span
              v-if="!data.placeholder"
              :class="data.supportsImages ? 'models-table-image-yes' : 'models-table-image-no'"
              role="img"
              :title="data.imageTitle"
              :aria-label="data.imageTitle"
              >{{ data.supportsImages ? "✓" : "✕"
              }}<span v-if="data.imageAuto" class="models-table-image-auto-tag">{{
                t("imageSupportAutoShort")
              }}</span></span
            >
          </template>
        </Column>
        <Column class="models-col-actions">
          <template #header>
            <span class="sr-only">{{ t("columnActions") }}</span>
          </template>
          <template #body="{ data }">
            <div v-if="data.model" class="models-cell-actions">
              <Button
                class="btn-icon"
                text
                severity="secondary"
                v-tooltip.top="t('duplicate')"
                :aria-label="t('duplicate')"
                @click="handleDuplicate(data.model)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                  />
                </svg>
              </Button>
              <Button
                class="btn-icon"
                text
                severity="secondary"
                v-tooltip.top="t('editModel')"
                :aria-label="t('editModel')"
                @click="handleEdit(data.model)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                  />
                </svg>
              </Button>
              <Button
                class="btn-icon btn-danger"
                text
                severity="danger"
                v-tooltip.top="t('delete_')"
                :aria-label="t('delete_')"
                @click="handleDelete(data.model.model_id)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                  />
                </svg>
              </Button>
            </div>
          </template>
        </Column>
      </DataTable>
    </div>
  </Modal>

  <!-- Stacked add/edit dialog, layered on top of the list. -->
  <ModelFormModal
    :is-open="formOpen"
    :edit-model="editModel"
    :edit-provider="editingProvider"
    :existing-models="builtInModelsFiltered"
    @saved="handleFormSaved"
    @provider-saved="handleProviderSaved"
    @close="formOpen = false"
  />
  <Modal
    :is-open="!!providerToRemove"
    :title="t('removeProviderTitle')"
    class-name="modal-wide"
    @close="providerToRemove = null"
  >
    <div class="model-provider-confirm">
      <p>
        {{
          t("removeProviderConfirmation").replace(
            "{name}",
            providerToRemove ? providerName(providerToRemove) : "",
          )
        }}
      </p>
      <div v-if="removeError" class="models-error" role="alert">{{ removeError }}</div>
      <div class="form-actions">
        <Button
          type="button"
          severity="secondary"
          :label="t('cancel')"
          :disabled="removingProvider"
          @click="providerToRemove = null"
        />
        <Button
          type="button"
          severity="danger"
          :label="removingProvider ? t('removingProvider') : t('removeProvider')"
          :disabled="removingProvider || !providerToRemove"
          data-testid="confirm-remove-provider"
          @click="confirmRemoveProvider"
        />
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ModelFormModal from "./ModelFormModal.vue";
import { modelsTableDt } from "./modelsTableDt";
import { prettyModelLabels } from "../../utils/modelNames";
import { accountSuffixes } from "../../utils/providerAccounts";
import { API_TYPE_LABELS, PROVIDER_LABELS } from "./customModelConstants";
import { useI18n } from "../composables/i18n";
import {
  api,
  customModelsApi,
  modelProvidersApi,
  type AvailableModel,
  type CustomModel,
  type ModelProviderConnection,
} from "../../services/api";

const props = defineProps<{ mode: "manage" | "add" | null }>();
const emit = defineEmits<{ (e: "close"): void; (e: "modelsChanged"): void }>();

const { t } = useI18n();

const models = ref<CustomModel[]>([]);
const loading = ref(true);
const refreshing = ref(false);
const error = ref<string | null>(null);
const builtInModels = ref<AvailableModel[]>([]);
const providerConnections = ref<ModelProviderConnection[]>([]);
const providerLoadFailed = ref(false);
const refreshingProviderIds = ref(new Set<string>());
const editingProvider = ref<ModelProviderConnection | null>(null);
const providerToRemove = ref<ModelProviderConnection | null>(null);
const removingProvider = ref(false);
const removeError = ref<string | null>(null);

// Stacked add/edit dialog state. `editModel` is the custom model being edited,
// or null when adding a new one.
const formOpen = ref(false);
const editModel = ref<CustomModel | null>(null);

const builtInModelsFiltered = computed(() =>
  builtInModels.value.filter((m) => m.id !== "predictable"),
);

// True when the model-list DataTable (not the empty/loading views) is showing.
// The list fills the modal edge-to-edge, so we drop the wrapper padding in that
// state (see .models-modal-list).
const hasAnyEntries = computed(
  () => builtInModelsFiltered.value.length > 0 || models.value.length > 0 || providerConnections.value.length > 0,
);
const showList = computed(() => !loading.value && hasAnyEntries.value);
const providerSuffixes = computed(() => {
  const accounts = new Map<string, string>();
  for (const provider of providerConnections.value) {
    accounts.set(provider.provider_id, providerName(provider));
  }
  for (const model of builtInModels.value) {
    if (model.source_id && !accounts.has(model.source_id)) {
      accounts.set(model.source_id, model.source || model.source_id);
    }
  }
  return accountSuffixes([...accounts].map(([id, label]) => ({ id, label })));
});

function providerName(provider: ModelProviderConnection): string {
  return provider.display_name || PROVIDER_LABELS[provider.provider_type];
}

function providerSuffix(provider: ModelProviderConnection): string {
  return providerSuffixes.value.get(provider.provider_id) ?? "";
}

// Normalized rows so built-in + custom models render through one DataTable.
// `model` is only present for custom rows, which are the editable/deletable
// ones (built-ins have no actions). Rows are grouped by source (groupKey /
// groupLabel drive the subheader); a group's common endpoint renders once in
// the header, so per-row `endpoint` is only set when it differs (or for
// custom rows, whose endpoints are user-configured and always shown).
interface TableRow {
  key: string;
  groupKey: string;
  groupLabel: string;
  groupEndpoint: string;
  name: string;
  modelId: string;
  apiShape: string | null;
  endpoint: string;
  supportsImages: boolean;
  imageTitle: string;
  imageAuto: boolean;
  model: CustomModel | null;
  provider: ModelProviderConnection | null;
  placeholder: boolean;
  wireId: string;
}

const tableRows = computed<TableRow[]>(() => {
  const labels = prettyModelLabels(builtInModelsFiltered.value);
  const providersByID = new Map(
    providerConnections.value.map((provider) => [provider.provider_id, provider]),
  );
  const groups = new Map<string, { label: string; models: AvailableModel[] }>();
  for (const provider of providerConnections.value) {
    groups.set(`provider:${provider.provider_id}`, { label: providerName(provider), models: [] });
  }
  for (const m of builtInModelsFiltered.value) {
    const src = m.source || "";
    const sourceID = m.source_id || "";
    const key = sourceID ? `provider:${sourceID}` : `builtin:${src}`;
    if (!groups.has(key)) {
      const provider = providersByID.get(sourceID);
      const label = provider ? providerName(provider) : src;
      const suffix = sourceID ? providerSuffixes.value.get(sourceID) : undefined;
      groups.set(key, { label: suffix ? `${label} ${suffix}` : label, models: [] });
    }
    groups.get(key)!.models.push(m);
  }
  const rows: TableRow[] = [];
  for (const [groupKey, { label, models: group }] of groups) {
    const provider = groupKey.startsWith("provider:")
      ? providersByID.get(groupKey.slice("provider:".length)) ?? null
      : null;
    if (provider && group.length === 0) {
      rows.push({
        key: `${groupKey}:empty`, groupKey, groupLabel: label, groupEndpoint: "",
        name: "", modelId: "", wireId: "", apiShape: null, endpoint: "",
        supportsImages: false, imageTitle: "", imageAuto: false, model: null,
        provider, placeholder: true,
      });
    }
    // Endpoint shared by every model in the group renders once in the group
    // header; otherwise it stays per-row.
    const endpoints = new Set(group.map((m) => m.base_url || ""));
    const common = endpoints.size === 1 ? (group[0].base_url ?? "") : "";
    for (const m of group) {
      rows.push({
        key: `builtin:${m.id}`,
        groupKey,
        groupLabel: label,
        groupEndpoint: common,
        name: labels.get(m.id) || m.id,
        modelId: m.id,
        apiShape: (m.api_type && API_TYPE_LABELS[m.api_type]) || null,
        endpoint: common ? "" : m.base_url || "",
        supportsImages: m.supports_images ?? true,
        imageTitle: (m.supports_images ?? true) ? t("imageSupportYes") : t("imageSupportNo"),
        imageAuto: false,
        model: null,
        provider,
        placeholder: false,
        wireId: m.api_model_name || m.id,
      });
    }
  }
  for (const m of models.value) {
    rows.push({
      key: `custom:${m.model_id}`,
      groupKey: "custom",
      groupLabel: t("customModelsGroup"),
      groupEndpoint: "",
      name: m.display_name,
      modelId: m.model_name,
      apiShape: PROVIDER_LABELS[m.provider_type],
      endpoint: m.endpoint,
      supportsImages: customModelSupportsImages(m),
      imageTitle: customModelImageTitle(m),
      imageAuto: (m.image_support ?? "auto") === "auto",
      model: m,
      provider: null,
      placeholder: false,
      wireId: m.model_name,
    });
  }
  return rows;
});

// Row counts per group for the "(N)" in each group header.
const groupCounts = computed<Record<string, number>>(() => {
  const counts: Record<string, number> = {};
  for (const row of tableRows.value) {
    if (!row.placeholder) counts[row.groupKey] = (counts[row.groupKey] || 0) + 1;
  }
  return counts;
});

// For a custom model, the boolean its image_support setting evaluates to. When
// set to "auto" we use the server-resolved supports_images; explicit yes/no win.
function customModelSupportsImages(model: CustomModel): boolean {
  const setting = model.image_support ?? "auto";
  if (setting === "yes") return true;
  if (setting === "no") return false;
  return model.supports_images ?? true;
}

function customModelImageTitle(model: CustomModel): string {
  const label = customModelSupportsImages(model) ? t("imageSupportYes") : t("imageSupportNo");
  // Surface what auto resolved to for auto models.
  if ((model.image_support ?? "auto") === "auto") {
    return `${t("imageSupportAuto")} \u2014 ${label}`;
  }
  return label;
}

async function loadModels() {
  loading.value = true;
  error.value = null;
  const [customResult, modelsResult, providersResult] = await Promise.allSettled([
    customModelsApi.getCustomModels(),
    api.getModels(),
    modelProvidersApi.list(),
  ]);
  if (customResult.status === "fulfilled") models.value = customResult.value;
  else error.value = customResult.reason instanceof Error ? customResult.reason.message : String(customResult.reason);
  if (modelsResult.status === "fulfilled") setBuiltInFromModelList(modelsResult.value);
  else if (!error.value) error.value = modelsResult.reason instanceof Error ? modelsResult.reason.message : String(modelsResult.reason);
  providerLoadFailed.value = providersResult.status === "rejected";
  if (providersResult.status === "fulfilled") providerConnections.value = providersResult.value;
  loading.value = false;
}

function setBuiltInFromModelList(modelList: AvailableModel[]) {
  builtInModels.value = modelList.filter((m) => m.source && (m.source_id || m.source !== "custom"));
}

function handleAddNew() {
  editingProvider.value = null;
  editModel.value = null;
  formOpen.value = true;
}

function handleEdit(model: CustomModel) {
  editingProvider.value = null;
  editModel.value = model;
  formOpen.value = true;
}

// The stacked form dialog saved a model; reload the list and notify the app.
async function handleFormSaved() {
  await loadModels();
  emit("modelsChanged");
}

function openProviderForm(provider: ModelProviderConnection) {
  editModel.value = null;
  editingProvider.value = provider;
  formOpen.value = true;
}

function openRemoveProvider(provider: ModelProviderConnection) {
  removeError.value = null;
  providerToRemove.value = provider;
}

function saveProviderLocally(provider: ModelProviderConnection) {
  const index = providerConnections.value.findIndex((saved) => saved.provider_id === provider.provider_id);
  if (index < 0) providerConnections.value.push(provider);
  else providerConnections.value[index] = provider;
}


async function refreshModelList() {
  try {
    const modelList = await api.getModels();
    setBuiltInFromModelList(modelList);
    if (window.__SHELLEY_INIT__) window.__SHELLEY_INIT__.models = modelList;
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

async function handleProviderSaved(provider: ModelProviderConnection) {
  formOpen.value = false;
  editingProvider.value = null;
  providerLoadFailed.value = false;
  saveProviderLocally(provider);
  error.value = null;
  await refreshModelList();
  emit("modelsChanged");
}

async function refreshProvider(provider: ModelProviderConnection) {
  if (refreshingProviderIds.value.has(provider.provider_id)) return;
  refreshingProviderIds.value = new Set(refreshingProviderIds.value).add(provider.provider_id);
  error.value = null;
  try {
    saveProviderLocally(await modelProvidersApi.refresh(provider.provider_id));
    providerLoadFailed.value = false;
    await refreshModelList();
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    const ids = new Set(refreshingProviderIds.value);
    ids.delete(provider.provider_id);
    refreshingProviderIds.value = ids;
  }
}

async function confirmRemoveProvider() {
  const provider = providerToRemove.value;
  if (!provider || removingProvider.value) return;
  removingProvider.value = true;
  removeError.value = null;
  try {
    await modelProvidersApi.remove(provider.provider_id);
    providerConnections.value = providerConnections.value.filter(
      (saved) => saved.provider_id !== provider.provider_id,
    );
    providerLoadFailed.value = false;
    providerToRemove.value = null;
    await refreshModelList();
    emit("modelsChanged");
  } catch (err) {
    removeError.value = err instanceof Error ? err.message : String(err);
  } finally {
    removingProvider.value = false;
  }
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
  () => props.mode,
  (mode) => {
    if (mode) {
      if (mode === "add") handleAddNew();
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
