import React, { useState, useEffect, useCallback, useMemo, useRef } from "react";
import Modal from "./Modal";
import { useI18n } from "../i18n";
import {
  api,
  AvailableModel,
  customModelsApi,
  CustomModel,
  CreateCustomModelRequest,
  TestCustomModelRequest,
} from "../services/api";

interface ModelsModalProps {
  isOpen: boolean;
  onClose: () => void;
  onModelsChanged?: () => void;
}

type ProviderType = "anthropic" | "openai" | "openai-responses" | "gemini";

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

// Built-in model info from init data
type BuiltInModel = AvailableModel;

// API_TYPE_LABELS maps the wire-protocol enum values from server-side
// models.APIType to the same human-readable strings the custom-model form
// shows for its Provider/API Format field.
const API_TYPE_LABELS: Record<string, string> = {
  "anthropic-messages": "Anthropic",
  "openai-chat-completions": "OpenAI (Chat API)",
  "openai-responses": "OpenAI (Responses API)",
  gemini: "Google Gemini",
  builtin: "Built-in",
};

interface FormData {
  display_name: string;
  provider_type: ProviderType;
  endpoint: string;
  endpoint_custom: boolean;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string; // Comma-separated tags
  reasoning_effort: string; // Free-form reasoning.effort for OpenAI Responses API
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

// Common reasoning.effort values for the OpenAI Responses API. Free-form so
// users can type anything providers add later.
const REASONING_EFFORT_SUGGESTIONS = ["none", "minimal", "low", "medium", "high", "xhigh"];

type ImageSupportIndicatorProps =
  | { mode: "resolved"; resolved: boolean }
  | { mode: "custom"; imageSupport: "auto" | "yes" | "no" };

function ImageSupportIndicator(props: ImageSupportIndicatorProps) {
  const { t } = useI18n();
  let kind: "yes" | "no" | "auto";
  if (props.mode === "resolved") {
    kind = props.resolved ? "yes" : "no";
  } else {
    kind = props.imageSupport;
  }
  if (kind === "yes") {
    return (
      <span
        className="models-table-image-yes"
        role="img"
        title={t("imageSupportYes")}
        aria-label={t("imageSupportYes")}
      >
        ✓
      </span>
    );
  }
  if (kind === "no") {
    return (
      <span
        className="models-table-image-no"
        role="img"
        title={t("imageSupportNo")}
        aria-label={t("imageSupportNo")}
      >
        ✕
      </span>
    );
  }
  return (
    <span
      className="models-table-image-auto"
      role="img"
      title={t("imageSupportAuto")}
      aria-label={t("imageSupportAuto")}
    >
      {t("imageSupportAutoShort")}
    </span>
  );
}

function ModelsModal({ isOpen, onClose, onModelsChanged }: ModelsModalProps) {
  const { t } = useI18n();
  const [models, setModels] = useState<CustomModel[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [builtInModels, setBuiltInModels] = useState<BuiltInModel[]>([]);

  // Form state
  const [showForm, setShowForm] = useState(false);
  const [editingModelId, setEditingModelId] = useState<string | null>(null);
  const [form, setForm] = useState<FormData>(emptyForm);

  // Test state
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ success: boolean; message: string } | null>(null);

  // Tooltip state
  const [showTagsTooltip, setShowTagsTooltip] = useState(false);

  // Remote model discovery from /v1/models (or provider equivalent)
  const [remoteModels, setRemoteModels] = useState<{ name: string; model_name: string }[]>([]);
  const [loadingRemoteModels, setLoadingRemoteModels] = useState(false);
  const discoverRequestId = useRef(0);

  const loadModels = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const result = await customModelsApi.getCustomModels();
      setModels(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load models");
    } finally {
      setLoading(false);
    }
  }, []);

  const setBuiltInFromModelList = useCallback((modelList: AvailableModel[]) => {
    setBuiltInModels(modelList.filter((m) => m.source && m.source !== "custom"));
  }, []);

  useEffect(() => {
    if (isOpen) {
      loadModels();
      // Get built-in models from init data (those with non-custom source)
      const initData = window.__SHELLEY_INIT__;
      if (initData?.models) {
        setBuiltInFromModelList(initData.models);
      }
    }
  }, [isOpen, loadModels, setBuiltInFromModelList]);

  const modelSuggestions = useMemo(() => {
    const seen = new Set<string>();
    const suggestions: { name: string; model_name: string }[] = [];
    const add = (name: string, model_name: string) => {
      if (!model_name || seen.has(model_name)) {
        return;
      }
      seen.add(model_name);
      suggestions.push({ name, model_name });
    };
    for (const preset of remoteModels) {
      add(preset.name, preset.model_name);
    }
    for (const preset of DEFAULT_MODELS[form.provider_type]) {
      add(preset.name, preset.model_name);
    }
    return suggestions;
  }, [remoteModels, form.provider_type]);

  useEffect(() => {
    if (!showForm) {
      setRemoteModels([]);
      return;
    }

    const canDiscover = Boolean(form.endpoint && (form.api_key || editingModelId));
    if (!canDiscover) {
      setRemoteModels([]);
      return;
    }

    const requestId = ++discoverRequestId.current;
    const timer = window.setTimeout(async () => {
      setLoadingRemoteModels(true);
      try {
        const result = await customModelsApi.discoverRemoteModels({
          model_id: editingModelId || undefined,
          provider_type: form.provider_type,
          endpoint: form.endpoint,
          api_key: form.api_key,
        });
        if (requestId !== discoverRequestId.current) {
          return;
        }
        if (!result.success) {
          setRemoteModels([]);
          return;
        }
        const discovered = result.models.map((model) => ({
          name: model.display_name || model.id,
          model_name: model.id,
        }));
        setRemoteModels(discovered);
        if (discovered.length === 1) {
          setForm((prev) => {
            if (prev.model_name) {
              return prev;
            }
            const only = discovered[0];
            return {
              ...prev,
              model_name: only.model_name,
              display_name: prev.display_name || only.name,
            };
          });
        }
      } catch {
        if (requestId === discoverRequestId.current) {
          setRemoteModels([]);
        }
      } finally {
        if (requestId === discoverRequestId.current) {
          setLoadingRemoteModels(false);
        }
      }
    }, 500);

    return () => window.clearTimeout(timer);
  }, [
    showForm,
    editingModelId,
    form.provider_type,
    form.endpoint,
    form.api_key,
    form.endpoint_custom,
  ]);

  const handleProviderChange = (provider: ProviderType) => {
    setRemoteModels([]);
    setForm((prev) => ({
      ...prev,
      provider_type: provider,
      endpoint: prev.endpoint_custom ? prev.endpoint : DEFAULT_ENDPOINTS[provider],
    }));
  };

  const handleEndpointModeChange = (custom: boolean) => {
    setRemoteModels([]);
    setForm((prev) => ({
      ...prev,
      endpoint_custom: custom,
      endpoint: custom ? prev.endpoint : DEFAULT_ENDPOINTS[prev.provider_type],
    }));
  };

  const handleTest = async () => {
    // Need model_name always, and either api_key or editing an existing model
    if (!form.model_name) {
      setTestResult({ success: false, message: t("modelNameRequired") });
      return;
    }
    if (!form.api_key && !editingModelId) {
      setTestResult({ success: false, message: t("apiKeyRequired") });
      return;
    }

    setTesting(true);
    setTestResult(null);

    try {
      const request: TestCustomModelRequest = {
        model_id: editingModelId || undefined, // Pass model_id to use stored key
        provider_type: form.provider_type,
        endpoint: form.endpoint,
        api_key: form.api_key,
        model_name: form.model_name,
        reasoning_effort: form.reasoning_effort,
      };
      const result = await customModelsApi.testCustomModel(request);
      setTestResult(result);
    } catch (err) {
      setTestResult({
        success: false,
        message: err instanceof Error ? err.message : "Test failed",
      });
    } finally {
      setTesting(false);
    }
  };

  const handleSave = async () => {
    if (!form.display_name || !form.api_key || !form.model_name) {
      setError("Display name, API key, and model name are required");
      return;
    }

    try {
      setError(null);
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

      if (editingModelId) {
        await customModelsApi.updateCustomModel(editingModelId, request);
      } else {
        await customModelsApi.createCustomModel(request);
      }

      setShowForm(false);
      setEditingModelId(null);
      setForm(emptyForm);
      setTestResult(null);
      await loadModels();
      onModelsChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save model");
    }
  };

  const handleEdit = (model: CustomModel) => {
    setEditingModelId(model.model_id);
    setForm({
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
    setShowForm(true);
    setTestResult(null);
  };

  const handleDuplicate = async (model: CustomModel) => {
    try {
      setError(null);
      await customModelsApi.duplicateCustomModel(model.model_id);
      await loadModels();
      onModelsChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to duplicate model");
    }
  };

  const handleDelete = async (modelId: string) => {
    try {
      setError(null);
      await customModelsApi.deleteCustomModel(modelId);
      await loadModels();
      onModelsChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete model");
    }
  };

  const handleCancel = () => {
    setShowForm(false);
    setEditingModelId(null);
    setForm(emptyForm);
    setTestResult(null);
    setRemoteModels([]);
  };

  const handleAddNew = () => {
    setEditingModelId(null);
    setForm(emptyForm);
    setShowForm(true);
    setTestResult(null);
    setRemoteModels([]);
  };

  const handleRefreshModels = async () => {
    try {
      setRefreshing(true);
      setError(null);
      const refreshedModels = await api.refreshModels();
      if (window.__SHELLEY_INIT__) {
        window.__SHELLEY_INIT__.models = refreshedModels;
      }
      setBuiltInFromModelList(refreshedModels);
      onModelsChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to refresh models");
    } finally {
      setRefreshing(false);
    }
  };

  const headerRight = !showForm ? (
    <div className="models-header-actions">
      <button
        className="btn-secondary btn-sm"
        onClick={handleRefreshModels}
        disabled={refreshing || loading}
      >
        {refreshing ? t("refreshingModels") : t("refreshModels")}
      </button>
      <button className="btn-primary btn-sm" onClick={handleAddNew}>
        + {t("addModel")}
      </button>
    </div>
  ) : null;

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={t("manageModels")}
      titleRight={headerRight}
      className="modal-xwide"
    >
      <div className="models-modal">
        {error && (
          <div className="models-error">
            {error}
            <button onClick={() => setError(null)} className="models-error-dismiss">
              ×
            </button>
          </div>
        )}

        {loading ? (
          <div className="models-loading">
            <div className="spinner"></div>
            <span>{t("loadingModels")}</span>
          </div>
        ) : showForm ? (
          // Add/Edit form
          <div className="model-form">
            <h3>{editingModelId ? t("editModel") : t("addModel")}</h3>

            {/* Provider Selection */}
            <div className="form-group">
              <label>{t("providerApiFormat")}</label>
              <div className="provider-buttons">
                {(["anthropic", "openai", "openai-responses", "gemini"] as ProviderType[]).map(
                  (p) => (
                    <button
                      key={p}
                      type="button"
                      className={`provider-btn ${form.provider_type === p ? "selected" : ""}`}
                      onClick={() => handleProviderChange(p)}
                    >
                      {PROVIDER_LABELS[p]}
                    </button>
                  ),
                )}
              </div>
            </div>

            {/* Endpoint Selection */}
            <div className="form-group">
              <label>{t("endpoint")}</label>
              <div className="endpoint-toggle">
                <button
                  type="button"
                  className={`toggle-btn ${!form.endpoint_custom ? "selected" : ""}`}
                  onClick={() => handleEndpointModeChange(false)}
                >
                  {t("defaultEndpoint")}
                </button>
                <button
                  type="button"
                  className={`toggle-btn ${form.endpoint_custom ? "selected" : ""}`}
                  onClick={() => handleEndpointModeChange(true)}
                >
                  {t("customEndpoint")}
                </button>
              </div>
              {form.endpoint_custom ? (
                <input
                  type="text"
                  value={form.endpoint}
                  onChange={(e) => setForm((prev) => ({ ...prev, endpoint: e.target.value }))}
                  placeholder="https://..."
                  className="form-input"
                />
              ) : (
                <div className="endpoint-display">{form.endpoint}</div>
              )}
            </div>

            {/* Model Name with autocomplete suggestions */}
            <div className="form-group">
              <label>{t("model")}</label>
              <input
                type="text"
                value={form.model_name}
                onChange={(e) => {
                  const v = e.target.value;
                  setForm((prev) => {
                    // If the user picked a known suggestion and the display
                    // name is empty, pre-fill it from the preset's friendly
                    // name. Never overwrite a non-empty display name.
                    const preset = modelSuggestions.find((p) => p.model_name === v);
                    return {
                      ...prev,
                      model_name: v,
                      display_name: preset && !prev.display_name ? preset.name : prev.display_name,
                    };
                  });
                }}
                placeholder="Model name (e.g., claude-sonnet-4-6)"
                className="form-input"
                list={`model-name-suggestions-${form.provider_type}`}
                autoComplete="off"
              />
              {loadingRemoteModels && (
                <div className="form-hint">{t("loadingModels")}</div>
              )}
              <datalist id={`model-name-suggestions-${form.provider_type}`}>
                {modelSuggestions.map((preset) => (
                  <option key={preset.model_name} value={preset.model_name}>
                    {preset.name}
                  </option>
                ))}
              </datalist>
            </div>

            {/* Display Name */}
            <div className="form-group">
              <label>{t("displayName")}</label>
              <input
                type="text"
                value={form.display_name}
                onChange={(e) => setForm((prev) => ({ ...prev, display_name: e.target.value }))}
                placeholder={t("nameShownInSelector")}
                className="form-input"
              />
            </div>

            {/* API Key */}
            <div className="form-group">
              <label>{t("apiKey")}</label>
              <input
                type="text"
                value={form.api_key}
                onChange={(e) => setForm((prev) => ({ ...prev, api_key: e.target.value }))}
                placeholder={t("enterApiKey")}
                className="form-input"
                autoComplete="off"
              />
            </div>

            {/* Max Tokens */}
            <div className="form-group">
              <label>{t("maxContextTokens")}</label>
              <input
                type="number"
                value={form.max_tokens}
                onChange={(e) =>
                  setForm((prev) => ({ ...prev, max_tokens: parseInt(e.target.value) || 200000 }))
                }
                className="form-input"
              />
            </div>

            {/* Image input support */}
            <div className="form-group">
              <label>{t("imageSupport")}</label>
              <select
                value={form.image_support}
                onChange={(e) =>
                  setForm((prev) => ({
                    ...prev,
                    image_support: e.target.value as "auto" | "yes" | "no",
                  }))
                }
                className="form-input"
              >
                <option value="auto">{t("imageSupportAuto")}</option>
                <option value="yes">{t("imageSupportYes")}</option>
                <option value="no">{t("imageSupportNo")}</option>
              </select>
              <div className="form-hint">{t("imageSupportHelp")}</div>
            </div>

            {/* Reasoning Effort (OpenAI Responses API only) */}
            {form.provider_type === "openai-responses" && (
              <div className="form-group">
                <label>{t("reasoningEffort")}</label>
                <input
                  type="text"
                  value={form.reasoning_effort}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, reasoning_effort: e.target.value }))
                  }
                  placeholder={t("reasoningEffortPlaceholder")}
                  className="form-input"
                  list="reasoning-effort-suggestions"
                  autoComplete="off"
                />
                <datalist id="reasoning-effort-suggestions">
                  {REASONING_EFFORT_SUGGESTIONS.map((suggestion) => (
                    <option key={suggestion} value={suggestion} />
                  ))}
                </datalist>
                <div className="form-hint">{t("reasoningEffortHint")}</div>
              </div>
            )}

            {/* Tags */}
            <div className="form-group">
              <label>
                {t("tags")}
                <span
                  className="info-icon-wrapper"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setShowTagsTooltip(!showTagsTooltip);
                  }}
                >
                  <span className="info-icon">
                    <svg
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                      width="14"
                      height="14"
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                      />
                    </svg>
                  </span>
                  {showTagsTooltip && <span className="info-tooltip">{t("tagsTooltip")}</span>}
                </span>
              </label>
              <input
                type="text"
                value={form.tags}
                onChange={(e) => setForm((prev) => ({ ...prev, tags: e.target.value }))}
                placeholder={t("tagsPlaceholder")}
                className="form-input"
              />
            </div>

            {/* Test Result */}
            {testResult && (
              <div className={`test-result ${testResult.success ? "success" : "error"}`}>
                {testResult.success ? "✓" : "✗"} {testResult.message}
              </div>
            )}

            {/* Form Actions */}
            <div className="form-actions">
              <button type="button" className="btn-secondary" onClick={handleCancel}>
                {t("cancel")}
              </button>
              <button
                type="button"
                className="btn-secondary"
                onClick={handleTest}
                disabled={testing || (!form.api_key && !editingModelId) || !form.model_name}
                title={
                  !form.model_name
                    ? "Enter model name to test"
                    : !form.api_key && !editingModelId
                      ? "Enter API key to test"
                      : ""
                }
              >
                {testing ? t("testingButton") : t("testButton")}
              </button>
              <button
                type="button"
                className="btn-primary"
                onClick={handleSave}
                disabled={!form.display_name || !form.api_key || !form.model_name}
              >
                {editingModelId ? t("save") : t("addModel")}
              </button>
            </div>
          </div>
        ) : // Model List
        builtInModels.length === 0 && models.length === 0 ? (
          <div className="models-empty">
            <p>{t("noModelsConfigured")}</p>
            <p className="models-empty-hint">{t("noModelsHint")}</p>
          </div>
        ) : (
          <div className="models-modal-scroll">
            <table className="models-table">
              <thead>
                <tr>
                  <th>{t("columnName")}</th>
                  <th>{t("columnModelId")}</th>
                  <th>{t("columnProvider")}</th>
                  <th>{t("columnSource")}</th>
                  <th>{t("endpoint")}</th>
                  <th>{t("tags")}</th>
                  <th className="models-table-images-col">{t("columnImages")}</th>
                  <th className="models-table-actions-col">
                    <span className="sr-only">{t("columnActions")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {builtInModels
                  .filter((m) => m.id !== "predictable")
                  .map((model) => (
                    <tr key={model.id} className="models-table-row models-table-row-builtin">
                      <td className="models-table-name">{model.display_name || model.id}</td>
                      <td className="models-table-mono">{model.id}</td>
                      <td
                        className={
                          model.api_type && API_TYPE_LABELS[model.api_type]
                            ? undefined
                            : "models-table-muted"
                        }
                      >
                        {(model.api_type && API_TYPE_LABELS[model.api_type]) || "—"}
                      </td>
                      <td>{model.source}</td>
                      <td
                        className={model.base_url ? "models-table-endpoint" : "models-table-muted"}
                        title={model.base_url || undefined}
                      >
                        {model.base_url || "—"}
                      </td>
                      <td className="models-table-muted">—</td>
                      <td className="models-table-images">
                        <ImageSupportIndicator
                          mode="resolved"
                          resolved={model.supports_images ?? true}
                        />
                      </td>
                      <td className="models-table-actions"></td>
                    </tr>
                  ))}
                {models.map((model) => (
                  <tr key={model.model_id} className="models-table-row">
                    <td className="models-table-name">{model.display_name}</td>
                    <td className="models-table-mono">{model.model_name}</td>
                    <td>{PROVIDER_LABELS[model.provider_type]}</td>
                    <td className="models-table-muted">custom</td>
                    <td className="models-table-endpoint" title={model.endpoint}>
                      {model.endpoint}
                    </td>
                    <td className="models-table-tags" title={model.tags || undefined}>
                      {model.tags || "—"}
                    </td>
                    <td className="models-table-images">
                      <ImageSupportIndicator
                        mode="custom"
                        imageSupport={model.image_support ?? "auto"}
                      />
                    </td>
                    <td className="models-table-actions">
                      <button
                        className="btn-icon"
                        onClick={() => handleDuplicate(model)}
                        title={t("duplicate")}
                      >
                        <svg
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                          width="16"
                          height="16"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                          />
                        </svg>
                      </button>
                      <button
                        className="btn-icon"
                        onClick={() => handleEdit(model)}
                        title={t("editModel")}
                      >
                        <svg
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                          width="16"
                          height="16"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                          />
                        </svg>
                      </button>
                      <button
                        className="btn-icon btn-danger"
                        onClick={() => handleDelete(model.model_id)}
                        title={t("delete_")}
                      >
                        <svg
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                          width="16"
                          height="16"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            strokeWidth={2}
                            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                          />
                        </svg>
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Modal>
  );
}

export default ModelsModal;
