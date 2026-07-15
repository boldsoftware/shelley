// Shared constants + form types for the custom-model UI, used by both
// ModelsModal.vue (the list) and ModelFormModal.vue (the add/edit dialog).

export type ProviderType = "anthropic" | "openai" | "openai-responses" | "gemini";

export const DEFAULT_ENDPOINTS: Record<ProviderType, string> = {
  anthropic: "https://api.anthropic.com/v1/messages",
  openai: "https://api.openai.com/v1",
  "openai-responses": "https://api.openai.com/v1",
  gemini: "https://generativelanguage.googleapis.com/v1beta",
};

export const PROVIDER_LABELS: Record<ProviderType, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI (Chat API)",
  "openai-responses": "OpenAI (Responses API)",
  gemini: "Google Gemini",
};

// Autocomplete suggestions offered for the model-name field, per provider.
export const DEFAULT_MODELS: Record<ProviderType, { name: string; model_name: string }[]> = {
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

// Maps the server-reported api_type of built-in models to a display label.
export const API_TYPE_LABELS: Record<string, string> = {
  "anthropic-messages": "Anthropic",
  "openai-chat-completions": "OpenAI (Chat API)",
  "openai-responses": "OpenAI (Responses API)",
  gemini: "Google Gemini",
  builtin: "Built-in",
};

export const REASONING_EFFORT_SUGGESTIONS = ["none", "minimal", "low", "medium", "high", "xhigh"];

export const REASONING_LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh"] as const;
export type ReasoningLevel = (typeof REASONING_LEVELS)[number];
export type ReasoningMap = Record<ReasoningLevel, string>;
export const DEFAULT_REASONING_MAP: ReasoningMap = Object.fromEntries(
  REASONING_LEVELS.map((level) => [level, level]),
) as ReasoningMap;

export const providerTypes: ProviderType[] = ["anthropic", "openai", "openai-responses", "gemini"];

export interface FormData {
  display_name: string;
  provider_type: ProviderType;
  endpoint: string;
  endpoint_custom: boolean;
  api_key: string;
  model_name: string;
  max_tokens: number;
  tags: string;
  reasoning_effort: string;
  user_agent: string;
  reasoning_support: "auto" | "yes" | "no";
  reasoning_map: ReasoningMap;
  image_support: "auto" | "yes" | "no";
}

export const emptyForm: FormData = {
  display_name: "",
  provider_type: "anthropic",
  endpoint: DEFAULT_ENDPOINTS.anthropic,
  endpoint_custom: false,
  api_key: "",
  model_name: "",
  max_tokens: 200000,
  tags: "",
  reasoning_effort: "",
  user_agent: "",
  reasoning_support: "auto",
  reasoning_map: { ...DEFAULT_REASONING_MAP },
  image_support: "auto",
};
