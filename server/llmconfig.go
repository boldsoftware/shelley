package server

import (
	"log/slog"

	"shelley.exe.dev/db"
)

// Link represents a link displayed in the UI overflow menu.
type Link struct {
	Title   string `json:"title"`
	IconSVG string `json:"icon_svg,omitempty"` // SVG path data for the icon
	URL     string `json:"url"`
}

// LLMConfig holds all configuration for LLM services
type LLMConfig struct {
	// API keys for each provider
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	FireworksAPIKey string
	TogetherAPIKey  string

	// Gateway is the base URL of the LLM gateway (optional)
	Gateway string

	// DefaultModel is the default model to use (optional, defaults to models.Default())
	DefaultModel string

	// NotificationChannels is a list of notification channel configs from shelley.json.
	// Each entry is a map with at least a "type" key, plus channel-specific fields.
	NotificationChannels []map[string]any

	// DB is the database for recording LLM requests (optional)
	DB *db.DB

	Logger *slog.Logger
}
