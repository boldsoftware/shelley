import React, { useState, useEffect, useCallback } from "react";
import {
  Loader2Icon,
  PlusIcon,
  CopyIcon,
  PencilIcon,
  Trash2Icon,
  InfoIcon,
  XIcon,
} from "lucide-react";
import Modal from "./Modal";
import { useI18n } from "../i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
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
        className="font-medium text-emerald-600 dark:text-emerald-500"
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
        className="font-medium text-muted-foreground"
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
      className="text-xs text-muted-foreground"
      role="img"
      title={t("imageSupportAuto")}
      aria-label={t("imageSupportAuto")}
    >
      {t("imageSupportAutoShort")}
    </span>
  );
}

// Shared field wrapper for the add/edit form.
function FormGroup({
  label,
  htmlFor,
  children,
}: {
  label: React.ReactNode;
  htmlFor?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={htmlFor} className="text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
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

  const handleProviderChange = (provider: ProviderType) => {
    setForm((prev) => ({
      ...prev,
      provider_type: provider,
      endpoint: prev.endpoint_custom ? prev.endpoint : DEFAULT_ENDPOINTS[provider],
    }));
  };

  const handleEndpointModeChange = (custom: boolean) => {
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
  };

  const handleAddNew = () => {
    setEditingModelId(null);
    setForm(emptyForm);
    setShowForm(true);
    setTestResult(null);
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
    <div className="flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        onClick={handleRefreshModels}
        disabled={refreshing || loading}
      >
        {refreshing ? t("refreshingModels") : t("refreshModels")}
      </Button>
      <Button size="sm" onClick={handleAddNew}>
        <PlusIcon className="size-4" />
        {t("addModel")}
      </Button>
    </div>
  ) : null;

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={t("manageModels")}
      titleRight={headerRight}
      className="sm:max-w-5xl"
    >
      <div className="flex flex-col gap-4">
        {error && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            <span className="flex-1">{error}</span>
            <button
              onClick={() => setError(null)}
              className="shrink-0 rounded p-0.5 hover:bg-destructive/15"
            >
              <XIcon className="size-4" />
            </button>
          </div>
        )}

        {loading ? (
          <div className="flex items-center justify-center gap-2 py-12 text-muted-foreground">
            <Loader2Icon className="size-5 animate-spin" />
            <span>{t("loadingModels")}</span>
          </div>
        ) : showForm ? (
          // Add/Edit form
          <div className="flex max-w-2xl flex-col gap-4">
            <h3 className="text-base font-semibold">
              {editingModelId ? t("editModel") : t("addModel")}
            </h3>

            {/* Provider Selection */}
            <FormGroup label={t("providerApiFormat")}>
              <div className="flex flex-wrap gap-2">
                {(["anthropic", "openai", "openai-responses", "gemini"] as ProviderType[]).map(
                  (p) => (
                    <button
                      key={p}
                      type="button"
                      className={cn(
                        "rounded-md border px-3 py-1.5 text-sm transition-colors",
                        form.provider_type === p
                          ? "border-primary bg-primary text-primary-foreground"
                          : "border-border bg-card text-foreground hover:bg-muted",
                      )}
                      onClick={() => handleProviderChange(p)}
                    >
                      {PROVIDER_LABELS[p]}
                    </button>
                  ),
                )}
              </div>
            </FormGroup>

            {/* Endpoint Selection */}
            <FormGroup label={t("endpoint")}>
              <div className="flex gap-0.5 self-start rounded-md border border-border p-0.5 text-sm">
                <button
                  type="button"
                  className={cn(
                    "rounded-sm px-3 py-1 transition-colors",
                    !form.endpoint_custom
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                  onClick={() => handleEndpointModeChange(false)}
                >
                  {t("defaultEndpoint")}
                </button>
                <button
                  type="button"
                  className={cn(
                    "rounded-sm px-3 py-1 transition-colors",
                    form.endpoint_custom
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                  onClick={() => handleEndpointModeChange(true)}
                >
                  {t("customEndpoint")}
                </button>
              </div>
              {form.endpoint_custom ? (
                <Input
                  type="text"
                  value={form.endpoint}
                  onChange={(e) => setForm((prev) => ({ ...prev, endpoint: e.target.value }))}
                  placeholder="https://..."
                />
              ) : (
                <div className="rounded-md bg-muted px-2.5 py-1.5 font-mono text-xs text-muted-foreground">
                  {form.endpoint}
                </div>
              )}
            </FormGroup>

            {/* Model Name with autocomplete suggestions */}
            <FormGroup label={t("model")} htmlFor="model-name-input">
              <Input
                id="model-name-input"
                type="text"
                value={form.model_name}
                onChange={(e) => {
                  const v = e.target.value;
                  setForm((prev) => {
                    // If the user picked a known suggestion and the display
                    // name is empty, pre-fill it from the preset's friendly
                    // name. Never overwrite a non-empty display name.
                    const preset = DEFAULT_MODELS[prev.provider_type].find(
                      (p) => p.model_name === v,
                    );
                    return {
                      ...prev,
                      model_name: v,
                      display_name: preset && !prev.display_name ? preset.name : prev.display_name,
                    };
                  });
                }}
                placeholder="Model name (e.g., claude-sonnet-4-6)"
                list={`model-name-suggestions-${form.provider_type}`}
                autoComplete="off"
              />
              <datalist id={`model-name-suggestions-${form.provider_type}`}>
                {DEFAULT_MODELS[form.provider_type].map((preset) => (
                  <option key={preset.model_name} value={preset.model_name}>
                    {preset.name}
                  </option>
                ))}
              </datalist>
            </FormGroup>

            {/* Display Name */}
            <FormGroup label={t("displayName")} htmlFor="display-name-input">
              <Input
                id="display-name-input"
                type="text"
                value={form.display_name}
                onChange={(e) => setForm((prev) => ({ ...prev, display_name: e.target.value }))}
                placeholder={t("nameShownInSelector")}
              />
            </FormGroup>

            {/* API Key */}
            <FormGroup label={t("apiKey")} htmlFor="api-key-input">
              <Input
                id="api-key-input"
                type="text"
                value={form.api_key}
                onChange={(e) => setForm((prev) => ({ ...prev, api_key: e.target.value }))}
                placeholder={t("enterApiKey")}
                autoComplete="off"
              />
            </FormGroup>

            {/* Max Tokens */}
            <FormGroup label={t("maxContextTokens")} htmlFor="max-tokens-input">
              <Input
                id="max-tokens-input"
                type="number"
                value={form.max_tokens}
                onChange={(e) =>
                  setForm((prev) => ({ ...prev, max_tokens: parseInt(e.target.value) || 200000 }))
                }
              />
            </FormGroup>

            {/* Image input support */}
            <FormGroup label={t("imageSupport")}>
              <Select
                value={form.image_support}
                onValueChange={(value) =>
                  setForm((prev) => ({
                    ...prev,
                    image_support: value as "auto" | "yes" | "no",
                  }))
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">{t("imageSupportAuto")}</SelectItem>
                  <SelectItem value="yes">{t("imageSupportYes")}</SelectItem>
                  <SelectItem value="no">{t("imageSupportNo")}</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">{t("imageSupportHelp")}</p>
            </FormGroup>

            {/* Reasoning Effort (OpenAI Responses API only) */}
            {form.provider_type === "openai-responses" && (
              <FormGroup label={t("reasoningEffort")} htmlFor="reasoning-effort-input">
                <Input
                  id="reasoning-effort-input"
                  type="text"
                  value={form.reasoning_effort}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, reasoning_effort: e.target.value }))
                  }
                  placeholder={t("reasoningEffortPlaceholder")}
                  list="reasoning-effort-suggestions"
                  autoComplete="off"
                />
                <datalist id="reasoning-effort-suggestions">
                  {REASONING_EFFORT_SUGGESTIONS.map((suggestion) => (
                    <option key={suggestion} value={suggestion} />
                  ))}
                </datalist>
                <p className="text-xs text-muted-foreground">{t("reasoningEffortHint")}</p>
              </FormGroup>
            )}

            {/* Tags */}
            <FormGroup
              htmlFor="tags-input"
              label={
                <span className="flex items-center gap-1.5">
                  {t("tags")}
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <button
                        type="button"
                        className="text-muted-foreground hover:text-foreground"
                        onClick={(e) => {
                          e.preventDefault();
                          e.stopPropagation();
                        }}
                      >
                        <InfoIcon className="size-3.5" />
                      </button>
                    </TooltipTrigger>
                    <TooltipContent>{t("tagsTooltip")}</TooltipContent>
                  </Tooltip>
                </span>
              }
            >
              <Input
                id="tags-input"
                type="text"
                value={form.tags}
                onChange={(e) => setForm((prev) => ({ ...prev, tags: e.target.value }))}
                placeholder={t("tagsPlaceholder")}
              />
            </FormGroup>

            {/* Test Result */}
            {testResult && (
              <div
                className={cn(
                  "rounded-md border px-3 py-2 text-sm",
                  testResult.success
                    ? "border-emerald-600/30 bg-emerald-600/10 text-emerald-700 dark:text-emerald-400"
                    : "border-destructive/30 bg-destructive/10 text-destructive",
                )}
              >
                {testResult.success ? "✓" : "✗"} {testResult.message}
              </div>
            )}

            {/* Form Actions */}
            <div className="flex justify-end gap-2 border-t border-border pt-4">
              <Button type="button" variant="outline" onClick={handleCancel}>
                {t("cancel")}
              </Button>
              <Button
                type="button"
                variant="secondary"
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
              </Button>
              <Button
                type="button"
                onClick={handleSave}
                disabled={!form.display_name || !form.api_key || !form.model_name}
              >
                {editingModelId ? t("save") : t("addModel")}
              </Button>
            </div>
          </div>
        ) : // Model List
        builtInModels.length === 0 && models.length === 0 ? (
          <div className="flex flex-col items-center gap-1 py-12 text-center">
            <p className="text-foreground">{t("noModelsConfigured")}</p>
            <p className="text-sm text-muted-foreground">{t("noModelsHint")}</p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs font-medium text-muted-foreground">
                  <th className="px-3 py-2 font-medium">{t("columnName")}</th>
                  <th className="px-3 py-2 font-medium">{t("columnModelId")}</th>
                  <th className="px-3 py-2 font-medium">{t("columnProvider")}</th>
                  <th className="px-3 py-2 font-medium">{t("columnSource")}</th>
                  <th className="px-3 py-2 font-medium">{t("endpoint")}</th>
                  <th className="px-3 py-2 font-medium">{t("tags")}</th>
                  <th className="px-3 py-2 text-center font-medium">{t("columnImages")}</th>
                  <th className="w-px px-3 py-2 font-medium">
                    <span className="sr-only">{t("columnActions")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {builtInModels
                  .filter((m) => m.id !== "predictable")
                  .map((model) => (
                    <tr
                      key={model.id}
                      className="border-b border-border/60 bg-muted/30 align-middle"
                    >
                      <td className="px-3 py-2 font-medium">{model.display_name || model.id}</td>
                      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                        {model.id}
                      </td>
                      <td
                        className={cn(
                          "px-3 py-2",
                          !(model.api_type && API_TYPE_LABELS[model.api_type]) &&
                            "text-muted-foreground",
                        )}
                      >
                        {(model.api_type && API_TYPE_LABELS[model.api_type]) || "—"}
                      </td>
                      <td className="px-3 py-2">{model.source}</td>
                      <td
                        className={cn(
                          "max-w-[16rem] truncate px-3 py-2 font-mono text-xs",
                          model.base_url ? "text-muted-foreground" : "text-muted-foreground",
                        )}
                        title={model.base_url || undefined}
                      >
                        {model.base_url || "—"}
                      </td>
                      <td className="px-3 py-2 text-muted-foreground">—</td>
                      <td className="px-3 py-2 text-center">
                        <ImageSupportIndicator
                          mode="resolved"
                          resolved={model.supports_images ?? true}
                        />
                      </td>
                      <td className="px-3 py-2"></td>
                    </tr>
                  ))}
                {models.map((model) => (
                  <tr
                    key={model.model_id}
                    className="border-b border-border/60 align-middle hover:bg-muted/40"
                  >
                    <td className="px-3 py-2 font-medium">{model.display_name}</td>
                    <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                      {model.model_name}
                    </td>
                    <td className="px-3 py-2">{PROVIDER_LABELS[model.provider_type]}</td>
                    <td className="px-3 py-2 text-muted-foreground">custom</td>
                    <td
                      className="max-w-[16rem] truncate px-3 py-2 font-mono text-xs text-muted-foreground"
                      title={model.endpoint}
                    >
                      {model.endpoint}
                    </td>
                    <td
                      className="max-w-[10rem] truncate px-3 py-2 text-muted-foreground"
                      title={model.tags || undefined}
                    >
                      {model.tags || "—"}
                    </td>
                    <td className="px-3 py-2 text-center">
                      <ImageSupportIndicator
                        mode="custom"
                        imageSupport={model.image_support ?? "auto"}
                      />
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex items-center justify-end gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => handleDuplicate(model)}
                          title={t("duplicate")}
                          aria-label={t("duplicate")}
                        >
                          <CopyIcon className="size-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => handleEdit(model)}
                          title={t("editModel")}
                          aria-label={t("editModel")}
                        >
                          <PencilIcon className="size-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          className="text-muted-foreground hover:text-destructive"
                          onClick={() => handleDelete(model.model_id)}
                          title={t("delete_")}
                          aria-label={t("delete_")}
                        >
                          <Trash2Icon className="size-4" />
                        </Button>
                      </div>
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
