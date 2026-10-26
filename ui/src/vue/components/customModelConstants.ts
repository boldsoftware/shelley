// Shared constants + form types for the custom-model UI, used by both
// ModelsModal.vue (the list) and ModelFormModal.vue (the add/edit dialog).

export type ProviderType = "anthropic" | "openai" | "openai-responses" | "gemini";

export const DEFAULT_ENDPOINTS: Record<ProviderType, string> = {
  anthropic: "https://api.anthropic.com/v1/messages",
  openai: "https://api.openai.com/v1",
  "openai-responses": "https://api.openai.com/v1",
  gemini: "https://generativelanguage.googleapis.com/v1beta",
};

export const ENDPOINT_PRESETS: Record<ProviderType, { name: string; url: string }[]> = {
  anthropic: [{ name: "Anthropic", url: DEFAULT_ENDPOINTS.anthropic }],
  openai: [
    { name: "OpenAI", url: DEFAULT_ENDPOINTS.openai },
    { name: "OpenRouter", url: "https://openrouter.ai/api/v1" },
    { name: "z.ai", url: "https://api.z.ai/api/paas/v4" },
    { name: "z.ai Coding Plan", url: "https://api.z.ai/api/coding/paas/v4" },
    { name: "Groq", url: "https://api.groq.com/openai/v1" },
    { name: "DeepSeek", url: "https://api.deepseek.com/v1" },
    { name: "Mistral", url: "https://api.mistral.ai/v1" },
    { name: "Together AI", url: "https://api.together.ai/v1" },
  ],
  "openai-responses": [
    { name: "OpenAI", url: DEFAULT_ENDPOINTS["openai-responses"] },
    { name: "OpenRouter", url: "https://openrouter.ai/api/v1" },
    { name: "Groq", url: "https://api.groq.com/openai/v1" },
  ],
  gemini: [{ name: "Google Gemini", url: DEFAULT_ENDPOINTS.gemini }],
};

export function endpointPresetsFor(provider: ProviderType, connection = false) {
  return ENDPOINT_PRESETS[provider].map((preset) => ({
    ...preset,
    url:
      connection && provider === "anthropic" ? preset.url.replace(/\/messages$/, "") : preset.url,
  }));
}

export const PROVIDER_LABELS: Record<ProviderType, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI (Chat API)",
  "openai-responses": "OpenAI (Responses API)",
  gemini: "Google Gemini",
};

export const PROVIDER_TAB_LABELS: Record<ProviderType, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI Chat",
  "openai-responses": "OpenAI Responses",
  gemini: "Gemini",
};

// Autocomplete suggestions offered for the model-name field, per provider.
export const DEFAULT_MODELS: Record<ProviderType, { name: string; model_name: string }[]> = {
  anthropic: [
    { name: "Claude Sonnet 4.6", model_name: "claude-sonnet-4-6" },
    { name: "Claude Opus 4.6", model_name: "claude-opus-4-6" },
    { name: "Claude Haiku 4.5", model_name: "claude-haiku-4-5" },
  ],
  openai: [
    { name: "GPT-6 Astra", model_name: "gpt-6-astra" },
    { name: "GPT-6.1 Sol", model_name: "gpt-6.1-sol" },
    { name: "GPT-6 Sol", model_name: "gpt-6-sol" },
    { name: "GPT-6 Luna", model_name: "gpt-6-luna" },
    { name: "GPT-5.6 Sol", model_name: "gpt-5.6-sol" },
    { name: "GPT-5.5", model_name: "gpt-5.5" },
    { name: "GPT-5.4", model_name: "gpt-5.4" },
  ],
  "openai-responses": [
    { name: "GPT-6 Astra", model_name: "gpt-6-astra" },
    { name: "GPT-6.1 Sol", model_name: "gpt-6.1-sol" },
    { name: "GPT-6.1 Terra", model_name: "gpt-6.1-terra" },
    { name: "GPT-6 Sol", model_name: "gpt-6-sol" },
    { name: "GPT-6 Luna", model_name: "gpt-6-luna" },
    { name: "GPT-5.5", model_name: "gpt-5.5" },
    { name: "GPT-5.4", model_name: "gpt-5.4" },
    { name: "GPT-5.4 mini", model_name: "gpt-5.4-mini" },
    { name: "GPT-5.3 Codex", model_name: "gpt-5.3-codex" },
  ],
  gemini: [
    { name: "Gemini 3.1 Pro", model_name: "gemini-3.1-pro-preview" },
    { name: "Gemini 3.8 Flash", model_name: "gemini-3.8-flash" },
    { name: "Gemini 3.6 Flash", model_name: "gemini-3.6-flash" },
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

export const REASONING_EFFORT_SUGGESTIONS = [
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
];

export const REASONING_LEVELS = [
  "off",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
] as const;
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
  api_key: string;
  model_name: string;
  max_tokens: number;
  reasoning_effort: string;
  reasoning_replay: "auto" | "none" | "reasoning_content";
  reasoning_support: "auto" | "yes" | "no";
  reasoning_map: ReasoningMap;
  image_support: "auto" | "yes" | "no";
}

export const emptyForm: FormData = {
  display_name: "",
  provider_type: "anthropic",
  endpoint: DEFAULT_ENDPOINTS.anthropic,
  api_key: "",
  model_name: "",
  max_tokens: 0,
  reasoning_effort: "",
  reasoning_replay: "auto",
  reasoning_support: "auto",
  reasoning_map: { ...DEFAULT_REASONING_MAP },
  image_support: "auto",
};
